package coder

import (
	"fmt"

	dnsregistry "easylab/internal/providers/dns"
	internalK8s "easylab/k8s"
	"easylab/utils"

	k8s "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	"github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apiextensions"
	k8score "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	helmv3 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v3"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	k8snetv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/networking/v1"
	rbacv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/rbac/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// SetupHTTPS installs ingress-nginx and cert-manager, creates a ClusterIssuer,
// and creates an Ingress for Coder with TLS. Returns the ingress-nginx release
// and the LoadBalancer IP assigned to the ingress-nginx controller service.
// kubeconfigOut is the kubeconfig file content as a StringOutput, used to read
// the LoadBalancer IP directly from the Kubernetes API (avoiding the Pulumi
// provider's await which blocks on OVHcloud's ipMode:VIP).
//
// If coder:domain is not set, returns nil, zero StringOutput, nil (HTTPS disabled).
func SetupHTTPS(
	ctx *pulumi.Context,
	k8sProvider *k8s.Provider,
	coderNs *k8score.Namespace,
	coderRelease *helmv3.Release,
	kubeconfigOut pulumi.StringOutput,
) (*helmv3.Release, pulumi.StringOutput, error) {
	domain := utils.CoderConfigOptional(ctx, utils.CoderDomain)
	if domain == "" {
		return nil, pulumi.StringOutput{}, nil
	}

	acmeEmail := utils.CoderConfigOptional(ctx, utils.CoderAcmeEmail)

	// Absent config key means "install" (default). Explicit "false" means skip (pre-installed).
	installCertManager := utils.CoderConfigOptional(ctx, utils.CoderInstallCertManager) != "false"
	installNginxIngress := utils.CoderConfigOptional(ctx, utils.CoderInstallNginxIngress) != "false"

	// Configurable namespace / service names (fall back to well-known defaults).
	certManagerNsName := utils.CoderConfigOptional(ctx, utils.CoderCertManagerNamespace)
	if certManagerNsName == "" {
		certManagerNsName = "cert-manager"
	}
	nginxNsName := utils.CoderConfigOptional(ctx, utils.CoderNginxIngressNamespace)
	if nginxNsName == "" {
		nginxNsName = "ingress-nginx"
	}
	nginxServiceName := utils.CoderConfigOptional(ctx, utils.CoderNginxIngressServiceName)
	if nginxServiceName == "" {
		nginxServiceName = "ingress-nginx-controller"
	}

	// ── cert-manager ────────────────────────────────────────────────────────
	var certManagerRelease *helmv3.Release
	if installCertManager {
		certManagerNs, err := k8score.NewNamespace(ctx, "cert-manager-ns", &k8score.NamespaceArgs{
			Metadata: &metav1.ObjectMetaArgs{Name: pulumi.String(certManagerNsName)},
		}, pulumi.Provider(k8sProvider))
		if err != nil {
			return nil, pulumi.StringOutput{}, fmt.Errorf("failed to create cert-manager namespace: %w", err)
		}

		certManagerRelease, err = internalK8s.InitHelm(ctx, k8sProvider, internalK8s.HelmChartInfo{
			Name:        "cert-manager",
			ChartName:   "cert-manager",
			Url:         "https://charts.jetstack.io",
			ReleaseName: "cert-manager",
			Values:      pulumi.Map{"installCRDs": pulumi.Bool(true)},
		}, certManagerNs)
		if err != nil {
			return nil, pulumi.StringOutput{}, fmt.Errorf("failed to install cert-manager: %w", err)
		}
	}

	// ── ingress-nginx ────────────────────────────────────────────────────────
	var ingressRelease *helmv3.Release
	if installNginxIngress {
		ingressNs, err := k8score.NewNamespace(ctx, "ingress-nginx-ns", &k8score.NamespaceArgs{
			Metadata: &metav1.ObjectMetaArgs{Name: pulumi.String(nginxNsName)},
		}, pulumi.Provider(k8sProvider))
		if err != nil {
			return nil, pulumi.StringOutput{}, fmt.Errorf("failed to create ingress-nginx namespace: %w", err)
		}

		ingressRelease, err = internalK8s.InitHelm(ctx, k8sProvider, internalK8s.HelmChartInfo{
			Name:        "ingress-nginx",
			ChartName:   "ingress-nginx",
			Url:         "https://kubernetes.github.io/ingress-nginx",
			ReleaseName: "ingress-nginx",
			// OVHcloud sets ipMode:VIP on LoadBalancer services, which causes the Pulumi
			// Kubernetes provider's GetService await to block indefinitely. Adding the
			// skipAwait annotation to the controller service tells the provider to skip
			// the readiness check when reading it.
			Values: pulumi.Map{
				"controller": pulumi.Map{
					"service": pulumi.Map{
						"annotations": pulumi.StringMap{
							"pulumi.kubernetes.io/skipAwait": pulumi.String("true"),
						},
					},
				},
			},
		}, ingressNs)
		if err != nil {
			return nil, pulumi.StringOutput{}, fmt.Errorf("failed to install ingress-nginx: %w", err)
		}
	}

	// ── ClusterIssuer (Let's Encrypt) ────────────────────────────────────────
	var solverSpec map[string]any
	// ingressIP is populated exactly once — either inside the DNS-01 branch (after
	// the webhook Helm install, giving the cloud time to provision the LoadBalancer)
	// or at the end for HTTP-01.  A second GetIngressIP call with the same Pulumi
	// resource name would cause a duplicate-URN error.
	var ingressIP pulumi.StringOutput
	ingressIPResolved := false

	dnsProviderName := utils.DNSConfigOptional(ctx, utils.DNSProviderKey)
	certDeps := []pulumi.Resource{}
	if certManagerRelease != nil {
		certDeps = append(certDeps, certManagerRelease)
	}

	if dnsProviderName != "" {
		dnsProvider, lookupErr := dnsregistry.Get(dnsProviderName)
		if lookupErr != nil {
			return nil, pulumi.StringOutput{}, fmt.Errorf("unknown DNS provider %q: %w", dnsProviderName, lookupErr)
		}

		// Store DNS credentials in a Kubernetes Secret for cert-manager webhook
		credSecret, secretErr := createDNSCredentialSecret(ctx, k8sProvider, certManagerNsName, dnsProviderName, dnsProvider)
		if secretErr != nil {
			return nil, pulumi.StringOutput{}, secretErr
		}

		zone := utils.DNSConfigOptional(ctx, utils.DNSZone)

		webhookSolver, webhookRelease, setupErr := dnsProvider.SetupCertManagerDNS01(
			ctx, k8sProvider, zone, credSecret, certDeps,
		)
		if setupErr != nil {
			return nil, pulumi.StringOutput{}, fmt.Errorf("failed to setup DNS-01 solver: %w", setupErr)
		}
		if webhookRelease != nil {
			certDeps = append(certDeps, webhookRelease)
		}

		// Grant the webhook SA permission to read secrets in the cert-manager namespace.
		// cert-manager passes ResourceNamespace=cert-manager for ClusterIssuer challenges,
		// so credentials must live there and the webhook must be able to read them.
		webhookRole, rbacErr := rbacv1.NewRole(ctx, "cert-manager-webhook-ovh-secret-reader", &rbacv1.RoleArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Name:      pulumi.String("cert-manager-webhook-ovh-secret-reader"),
				Namespace: pulumi.String(certManagerNsName),
			},
			Rules: rbacv1.PolicyRuleArray{
				&rbacv1.PolicyRuleArgs{
					ApiGroups: pulumi.StringArray{pulumi.String("")},
					Resources: pulumi.StringArray{pulumi.String("secrets")},
					Verbs:     pulumi.StringArray{pulumi.String("get"), pulumi.String("list")},
				},
			},
		}, pulumi.Provider(k8sProvider), pulumi.DependsOn(certDeps))
		if rbacErr != nil {
			return nil, pulumi.StringOutput{}, fmt.Errorf("failed to create webhook RBAC role: %w", rbacErr)
		}

		_, rbacErr = rbacv1.NewRoleBinding(ctx, "cert-manager-webhook-ovh-secret-reader", &rbacv1.RoleBindingArgs{
			Metadata: &metav1.ObjectMetaArgs{
				Name:      pulumi.String("cert-manager-webhook-ovh-secret-reader"),
				Namespace: pulumi.String(certManagerNsName),
			},
			RoleRef: &rbacv1.RoleRefArgs{
				ApiGroup: pulumi.String("rbac.authorization.k8s.io"),
				Kind:     pulumi.String("Role"),
				Name:     pulumi.String("cert-manager-webhook-ovh-secret-reader"),
			},
			Subjects: rbacv1.SubjectArray{
				&rbacv1.SubjectArgs{
					Kind:      pulumi.String("ServiceAccount"),
					Name:      pulumi.String("cert-manager-webhook-ovh"),
					Namespace: pulumi.String("cert-manager-webhook-ovh"),
				},
			},
		}, pulumi.Provider(k8sProvider), pulumi.DependsOn([]pulumi.Resource{webhookRole}))
		if rbacErr != nil {
			return nil, pulumi.StringOutput{}, fmt.Errorf("failed to create webhook RBAC role binding: %w", rbacErr)
		}
		certDeps = append(certDeps, webhookRole)

		solverSpec = map[string]any{"dns01": map[string]any(webhookSolver)}

		// Resolve the LoadBalancer IP here — after the webhook Helm install, which
		// takes several minutes and gives the cloud provider time to assign the IP.
		var ipErr error
		ingressIP, ipErr = GetIngressIP(kubeconfigOut, ingressRelease, nginxNsName, nginxServiceName)
		if ipErr != nil {
			return nil, pulumi.StringOutput{}, ipErr
		}
		ingressIPResolved = true

		// Create A record automatically
		subdomain := domain
		if zone != "" && len(subdomain) > len(zone)+1 {
			subdomain = subdomain[:len(subdomain)-len(zone)-1]
		}
		if aErr := dnsProvider.CreateARecord(ctx, zone, subdomain, ingressIP, certDeps); aErr != nil {
			return nil, pulumi.StringOutput{}, aErr
		}

		// Create wildcard A record if configured
		wildcardDomain := utils.CoderConfigOptional(ctx, utils.CoderWildcardDomain)
		if wildcardDomain != "" {
			if aErr := dnsProvider.CreateARecord(ctx, zone, "*"+"."+subdomain, ingressIP, certDeps); aErr != nil {
				return nil, pulumi.StringOutput{}, aErr
			}
		}
	} else {
		// HTTP-01 solver: cert-manager handles the challenge via ingress-nginx
		solverSpec = map[string]any{
			"http01": map[string]any{
				"ingress": map[string]any{"class": "nginx"},
			},
		}
	}

	_, err := apiextensions.NewCustomResource(ctx, "letsencrypt-prod-issuer", &apiextensions.CustomResourceArgs{
		ApiVersion: pulumi.String("cert-manager.io/v1"),
		Kind:       pulumi.String("ClusterIssuer"),
		Metadata: &metav1.ObjectMetaArgs{
			Name: pulumi.String("letsencrypt-prod"),
		},
		OtherFields: map[string]any{
			"spec": map[string]any{
				"acme": map[string]any{
					"server": "https://acme-v02.api.letsencrypt.org/directory",
					"email":  acmeEmail,
					"privateKeySecretRef": map[string]any{
						"name": "letsencrypt-prod",
					},
					"solvers": []any{solverSpec},
				},
			},
		},
	}, pulumi.Provider(k8sProvider), pulumi.DependsOn(certDeps))
	if err != nil {
		return nil, pulumi.StringOutput{}, fmt.Errorf("failed to create ClusterIssuer: %w", err)
	}

	// ── Ingress for Coder ────────────────────────────────────────────────────
	ingressDeps := []pulumi.Resource{coderRelease}
	if certManagerRelease != nil {
		ingressDeps = append(ingressDeps, certManagerRelease)
	}
	if ingressRelease != nil {
		ingressDeps = append(ingressDeps, ingressRelease)
	}

	tls := k8snetv1.IngressTLSArray{
		&k8snetv1.IngressTLSArgs{
			Hosts:      pulumi.StringArray{pulumi.String(domain)},
			SecretName: pulumi.String("coder-tls"),
		},
	}

	wildcardDomain := utils.CoderConfigOptional(ctx, utils.CoderWildcardDomain)
	if wildcardDomain != "" {
		tls = append(tls, &k8snetv1.IngressTLSArgs{
			Hosts:      pulumi.StringArray{pulumi.String(wildcardDomain)},
			SecretName: pulumi.String("coder-wildcard-tls"),
		})
	}

	ingressClassName := pulumi.String("nginx")
	_, err = k8snetv1.NewIngress(ctx, "coder-ingress", &k8snetv1.IngressArgs{
		Metadata: &metav1.ObjectMetaArgs{
			Namespace: coderNs.Metadata.Name(),
			Annotations: pulumi.StringMap{
				"cert-manager.io/cluster-issuer":                  pulumi.String("letsencrypt-prod"),
				"nginx.ingress.kubernetes.io/proxy-read-timeout":  pulumi.String("3600"),
				"nginx.ingress.kubernetes.io/proxy-send-timeout":  pulumi.String("3600"),
				"nginx.ingress.kubernetes.io/proxy-body-size":     pulumi.String("0"),
				"nginx.ingress.kubernetes.io/proxy-http-version":  pulumi.String("1.1"),
			},
		},
		Spec: &k8snetv1.IngressSpecArgs{
			IngressClassName: &ingressClassName,
			Rules: k8snetv1.IngressRuleArray{
				&k8snetv1.IngressRuleArgs{
					Host: pulumi.String(domain),
					Http: &k8snetv1.HTTPIngressRuleValueArgs{
						Paths: k8snetv1.HTTPIngressPathArray{
							&k8snetv1.HTTPIngressPathArgs{
								Path:     pulumi.String("/"),
								PathType: pulumi.String("Prefix"),
								Backend: &k8snetv1.IngressBackendArgs{
									Service: &k8snetv1.IngressServiceBackendArgs{
										Name: pulumi.String("coder"),
										Port: &k8snetv1.ServiceBackendPortArgs{
											Number: pulumi.Int(80),
										},
									},
								},
							},
						},
					},
				},
			},
			Tls: tls,
		},
	}, pulumi.Provider(k8sProvider), pulumi.DependsOn(ingressDeps))
	if err != nil {
		return nil, pulumi.StringOutput{}, fmt.Errorf("failed to create Coder ingress: %w", err)
	}

	// For HTTP-01 (no DNS provider), GetIngressIP hasn't been called yet.
	// Resolve it here after all setup is complete, giving the cloud provider
	// time to assign the LoadBalancer IP during the cert-manager/ingress install.
	if !ingressIPResolved {
		var ipErr error
		ingressIP, ipErr = GetIngressIP(kubeconfigOut, ingressRelease, nginxNsName, nginxServiceName)
		if ipErr != nil {
			return nil, pulumi.StringOutput{}, fmt.Errorf("failed to get ingress-nginx IP: %w", ipErr)
		}
	}

	return ingressRelease, ingressIP, nil
}

// GetIngressIP returns the LoadBalancer IP assigned to the ingress-nginx controller service.
// It uses a direct Kubernetes API call instead of GetService to avoid the Pulumi provider's
// pending-initialisation await, which blocks indefinitely on OVHcloud clusters because
// OVHcloud sets ipMode:VIP on LoadBalancer services (not recognised as ready by the provider).
// ingressRelease may be nil when ingress-nginx is pre-installed on the cluster.
func GetIngressIP(kubeconfigOut pulumi.StringOutput, ingressRelease *helmv3.Release, namespace, serviceName string) (pulumi.StringOutput, error) {
	var trigger interface{}
	if ingressRelease != nil {
		trigger = ingressRelease.ResourceNames
	} else {
		trigger = pulumi.String("") // no-op: service is already present
	}
	return internalK8s.GetServiceIP(kubeconfigOut, trigger, namespace, serviceName), nil
}

// createDNSCredentialSecret stores DNS provider API credentials in a Kubernetes Secret
// in the cert-manager namespace so cert-manager webhooks can access them.
func createDNSCredentialSecret(
	ctx *pulumi.Context,
	k8sProvider *k8s.Provider,
	nsName string,
	providerName string,
	dnsProvider dnsregistry.Provider,
) (string, error) {
	secretName := "dns-credentials-" + providerName
	secretData := pulumi.StringMap{}

	for _, f := range dnsProvider.GetCredentialFields() {
		val := utils.DNSConfigOptional(ctx, f.Name)
		secretData[f.Name] = pulumi.String(val)
	}

	// Map well-known OVH fields to the keys expected by cert-manager-webhook-ovh
	if providerName == "ovh" {
		if appKey := utils.DNSConfigOptional(ctx, utils.DNSOvhAppKey); appKey != "" {
			secretData["applicationKey"] = pulumi.String(appKey)
		}
		if appSecret := utils.DNSConfigOptional(ctx, utils.DNSOvhAppSecret); appSecret != "" {
			secretData["applicationSecret"] = pulumi.String(appSecret)
		}
		if consumerKey := utils.DNSConfigOptional(ctx, utils.DNSOvhConsumerKey); consumerKey != "" {
			secretData["consumerKey"] = pulumi.String(consumerKey)
		}
	}

	// Map Azure client secret to the key expected by cert-manager native Azure DNS solver
	if providerName == "azure" {
		if clientSecret := utils.DNSConfigOptional(ctx, utils.DNSAzureClientSecret); clientSecret != "" {
			secretData["client-secret"] = pulumi.String(clientSecret)
		}
	}

	_, err := k8score.NewSecret(ctx, "dns-credentials-secret", &k8score.SecretArgs{
		Metadata: &metav1.ObjectMetaArgs{
			Name:      pulumi.String(secretName),
			Namespace: pulumi.String(nsName),
		},
		StringData: secretData,
	}, pulumi.Provider(k8sProvider))
	if err != nil {
		return "", fmt.Errorf("failed to create DNS credential secret: %w", err)
	}

	return secretName, nil
}
