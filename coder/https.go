package coder

import (
	"fmt"
	"strings"

	dnsregistry "easylab/internal/providers/dns"
	internalK8s "easylab/k8s"
	"easylab/utils"

	k8s "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	"github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/apiextensions"
	k8score "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	helmv3 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v3"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	rbacv1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/rbac/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// SetupHTTPS installs ingress-nginx and cert-manager and creates a Let's Encrypt
// ClusterIssuer, plus (when a DNS provider is configured) the DNS A-records that
// let per-student workspace subdomains resolve and the wildcard certificate they
// are served with. Per-student ingresses are created at runtime by the server;
// this function only provisions the shared TLS/ingress infrastructure. Returns
// the ingress-nginx release and the LoadBalancer IP assigned to the ingress-nginx
// controller service.
// kubeconfigOut is the kubeconfig file content as a StringOutput, used to read
// the LoadBalancer IP directly from the Kubernetes API (avoiding the Pulumi
// provider's await which blocks on OVHcloud's ipMode:VIP).
// workspaceNs is the namespace student workspaces are created in: the wildcard
// certificate must live there, because an ingress can only reference a TLS secret
// in its own namespace. It may be nil when the namespace is not managed here.
//
// If coder:domain is not set, only ingress-nginx is installed: the server then
// exposes workspaces over plain HTTP via nip.io on the returned LoadBalancer IP,
// so the controller is still required, but there is no domain to certify.
func SetupHTTPS(
	ctx *pulumi.Context,
	k8sProvider *k8s.Provider,
	kubeconfigOut pulumi.StringOutput,
	workspaceNs *k8score.Namespace,
) (*helmv3.Release, pulumi.StringOutput, error) {
	domain := utils.CoderConfigOptional(ctx, utils.CoderDomain)

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
	// Without a domain there is no ClusterIssuer and no certificate to request,
	// so cert-manager would sit idle — skip it.
	var certManagerRelease *helmv3.Release
	if installCertManager && domain != "" {
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

	// No domain: the ingress controller is all that is needed. Hand back its
	// LoadBalancer IP so the server can route workspaces at "{name}.{ip}.nip.io"
	// over plain HTTP, and skip the ACME ClusterIssuer and DNS records entirely.
	if domain == "" {
		ingressIP, ipErr := GetIngressIP(kubeconfigOut, ingressRelease, nginxNsName, nginxServiceName)
		if ipErr != nil {
			return nil, pulumi.StringOutput{}, fmt.Errorf("failed to get ingress-nginx IP: %w", ipErr)
		}
		return ingressRelease, ingressIP, nil
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
	useExternalDNS := utils.DNSConfigOptional(ctx, utils.DNSExternalDNS) == "true"
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

		// The base record: no workspace ingress advertises it, so ExternalDNS would
		// never create it — it is always ours to make.
		subdomain := subdomainOf(domain, zone)
		if aErr := dnsProvider.CreateARecord(ctx, zone, subdomain, ingressIP, certDeps); aErr != nil {
			return nil, pulumi.StringOutput{}, aErr
		}

		// Workspace hosts are always "{name}.{domain}", so the wildcard record is
		// what makes any of them resolve — it is not optional. The one case that
		// does not need it is ExternalDNS, which creates a record per workspace
		// ingress instead.
		//
		// coder:wildcardDomain used to be the switch that turned this record on,
		// which meant a lab could be deployed with a domain and no way to reach any
		// workspace. It is now an override of the record name for the labs that set
		// it, and the record is created either way.
		if !useExternalDNS {
			wildcardRecord := wildcardOf(subdomain)
			if override := utils.CoderConfigOptional(ctx, utils.CoderWildcardDomain); override != "" {
				wildcardRecord = subdomainOf(override, zone)
			}
			if aErr := dnsProvider.CreateARecord(ctx, zone, wildcardRecord, ingressIP, certDeps); aErr != nil {
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

	issuer, err := apiextensions.NewCustomResource(ctx, "letsencrypt-prod-issuer", &apiextensions.CustomResourceArgs{
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

	// Per-student workspace ingresses are created at runtime by the server. With a
	// DNS provider they share the wildcard certificate created below; without one
	// each requests its own certificate through the ClusterIssuer above. No shared
	// Coder ingress is created here either way.
	//
	// The wildcard certificate needs a DNS-01 challenge — Let's Encrypt does not
	// issue wildcards over HTTP-01 — so it is only possible with a DNS provider.
	// It is worth the trouble: one certificate covers every workspace, which keeps
	// a workshop clear of Let's Encrypt's limit of 50 certificates per registered
	// domain per week, and workspaces come up already served over TLS instead of
	// waiting on an ACME round trip.
	if dnsProviderName != "" {
		// The certificate is issued by the ClusterIssuer, into the workspace
		// namespace: both have to exist first.
		wildcardDeps := make([]pulumi.Resource, 0, len(certDeps)+2)
		wildcardDeps = append(wildcardDeps, certDeps...)
		wildcardDeps = append(wildcardDeps, issuer)
		if workspaceNs != nil {
			wildcardDeps = append(wildcardDeps, workspaceNs)
		}
		if certErr := createWildcardCertificate(ctx, k8sProvider, workspaceNamespace(ctx), domain, wildcardDeps); certErr != nil {
			return nil, pulumi.StringOutput{}, certErr
		}

		if useExternalDNS {
			if edErr := setupExternalDNS(ctx, k8sProvider, dnsProviderName, domain, certDeps); edErr != nil {
				return nil, pulumi.StringOutput{}, edErr
			}
		}
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

// WildcardTLSSecretName is the secret the wildcard certificate is written to, in
// the workspace namespace. The server hands the same name to workspace ingresses
// so they are served from it instead of requesting a certificate each.
const WildcardTLSSecretName = "easylab-wildcard-tls"

// subdomainOf reduces an FQDN to the record name relative to its DNS zone:
// ("lab.example.com", "example.com") -> "lab". A domain that is not inside the
// zone, or an empty zone, is returned unchanged — validateDNSConfig rejects that
// combination before a lab is ever deployed.
func subdomainOf(domain, zone string) string {
	if zone == "" || !strings.HasSuffix(domain, "."+zone) {
		return domain
	}
	return strings.TrimSuffix(domain, "."+zone)
}

// wildcardOf returns the wildcard form of a record name, matching every
// single-label host under it: "lab" -> "*.lab".
func wildcardOf(subdomain string) string {
	return "*." + subdomain
}

// workspaceNamespace returns the namespace student workspaces run in. It mirrors
// the default in k8s.InitNamespace, which is what actually creates it.
func workspaceNamespace(ctx *pulumi.Context) string {
	if ns := utils.CoderConfigOptional(ctx, utils.CoderNamespace); ns != "" {
		return ns
	}
	return "workshops"
}

// createWildcardCertificate requests a single certificate covering the lab domain
// and every host directly under it. It is created in the workspace namespace
// because an ingress may only reference a TLS secret in its own namespace.
func createWildcardCertificate(
	ctx *pulumi.Context,
	k8sProvider *k8s.Provider,
	nsName string,
	domain string,
	deps []pulumi.Resource,
) error {
	_, err := apiextensions.NewCustomResource(ctx, "workspace-wildcard-certificate", &apiextensions.CustomResourceArgs{
		ApiVersion: pulumi.String("cert-manager.io/v1"),
		Kind:       pulumi.String("Certificate"),
		Metadata: &metav1.ObjectMetaArgs{
			Name:      pulumi.String(WildcardTLSSecretName),
			Namespace: pulumi.String(nsName),
		},
		OtherFields: map[string]any{
			"spec": map[string]any{
				"secretName": WildcardTLSSecretName,
				"dnsNames":   []any{domain, wildcardOf(domain)},
				"issuerRef": map[string]any{
					"name":  "letsencrypt-prod",
					"kind":  "ClusterIssuer",
					"group": "cert-manager.io",
				},
			},
		},
	}, pulumi.Provider(k8sProvider), pulumi.DependsOn(deps))
	if err != nil {
		return fmt.Errorf("failed to create wildcard certificate: %w", err)
	}
	return nil
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
