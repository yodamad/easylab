package coder

import (
	"encoding/json"
	"fmt"

	"easylab/utils"

	k8s "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	k8score "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	helmv3 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v3"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// externalDNSNamespace is where ExternalDNS and its own copy of the DNS
// credentials live. cert-manager's copy stays in the cert-manager namespace: a
// secret cannot be read across namespaces, and the two want different key shapes.
const externalDNSNamespace = "external-dns"

// externalDNSSecretName holds the provider credentials ExternalDNS authenticates with.
const externalDNSSecretName = "external-dns-credentials"

// azureConfigMountPath is where ExternalDNS looks for azure.json by default.
const azureConfigMountPath = "/etc/kubernetes"

// setupExternalDNS installs ExternalDNS, which watches the workspace ingresses the
// server creates at runtime and maintains one DNS record per workspace.
//
// This is the alternative to the wildcard A record: it costs a component and a
// short propagation delay, but it needs no wildcard record — which some DNS
// administrators will not hand out — and it removes each record when its
// workspace goes away. The delay is already absorbed by the student UI, which
// holds the "ready" signal back until the hostname actually resolves
// (workspaceDNSReady in internal/server/handler.go).
//
// It does not affect certificates: the wildcard certificate is issued through
// DNS-01 TXT records written by cert-manager, whatever creates the A records.
func setupExternalDNS(
	ctx *pulumi.Context,
	k8sProvider *k8s.Provider,
	providerName string,
	domain string,
	deps []pulumi.Resource,
) error {
	ns, err := k8score.NewNamespace(ctx, "external-dns-ns", &k8score.NamespaceArgs{
		Metadata: &metav1.ObjectMetaArgs{Name: pulumi.String(externalDNSNamespace)},
	}, pulumi.Provider(k8sProvider), pulumi.DependsOn(deps))
	if err != nil {
		return fmt.Errorf("failed to create external-dns namespace: %w", err)
	}

	secretData, err := externalDNSCredentials(ctx, providerName)
	if err != nil {
		return err
	}

	secret, err := k8score.NewSecret(ctx, "external-dns-credentials-secret", &k8score.SecretArgs{
		Metadata: &metav1.ObjectMetaArgs{
			Name:      pulumi.String(externalDNSSecretName),
			Namespace: ns.Metadata.Name(),
		},
		StringData: secretData,
	}, pulumi.Provider(k8sProvider), pulumi.DependsOn([]pulumi.Resource{ns}))
	if err != nil {
		return fmt.Errorf("failed to create external-dns credential secret: %w", err)
	}

	values := pulumi.Map{
		"provider": pulumi.Map{"name": pulumi.String(providerName)},
		// Only ingresses: the workspace Service is ClusterIP and carries no hostname.
		"sources":       pulumi.Array{pulumi.String("ingress")},
		"domainFilters": pulumi.Array{pulumi.String(domain)},
		// "sync" lets ExternalDNS delete records for workspaces that are gone. It
		// only ever touches records its TXT registry marks as its own, so the base
		// A record created by Pulumi and cert-manager's DNS-01 TXT records are safe.
		"policy":     pulumi.String("sync"),
		"registry":   pulumi.String("txt"),
		"txtOwnerId": pulumi.String(externalDNSOwnerID(ctx)),
	}

	switch providerName {
	case "azure":
		// ExternalDNS reads Azure credentials from a JSON file, not the environment.
		values["extraVolumes"] = pulumi.Array{
			pulumi.Map{
				"name":   pulumi.String("azure-config"),
				"secret": pulumi.Map{"secretName": pulumi.String(externalDNSSecretName)},
			},
		}
		values["extraVolumeMounts"] = pulumi.Array{
			pulumi.Map{
				"name":      pulumi.String("azure-config"),
				"mountPath": pulumi.String(azureConfigMountPath),
				"readOnly":  pulumi.Bool(true),
			},
		}
	case "ovh":
		values["env"] = pulumi.Array{
			externalDNSSecretEnv("OVH_APPLICATION_KEY", utils.DNSOvhAppKey),
			externalDNSSecretEnv("OVH_APPLICATION_SECRET", utils.DNSOvhAppSecret),
			externalDNSSecretEnv("OVH_CONSUMER_KEY", utils.DNSOvhConsumerKey),
		}
	}

	// helmv3.NewRelease directly rather than k8s.InitHelm: the release has to wait
	// on the credential secret as well as the namespace, and InitHelm only depends
	// on the namespace. Same reason the OVH cert-manager webhook does it this way
	// (internal/providers/dns/ovh/ovh.go).
	if _, err := helmv3.NewRelease(ctx, "external-dns", &helmv3.ReleaseArgs{
		Chart:           pulumi.String("external-dns"),
		Name:            pulumi.String("external-dns"),
		Namespace:       ns.Metadata.Name(),
		CreateNamespace: pulumi.Bool(false),
		RepositoryOpts: &helmv3.RepositoryOptsArgs{
			Repo: pulumi.String("https://kubernetes-sigs.github.io/external-dns/"),
		},
		Values:  values,
		Timeout: pulumi.Int(600),
	}, pulumi.Provider(k8sProvider), pulumi.DependsOn([]pulumi.Resource{ns, secret})); err != nil {
		return fmt.Errorf("failed to install external-dns: %w", err)
	}

	return nil
}

// externalDNSSecretEnv builds a container env var sourced from the credential secret.
func externalDNSSecretEnv(envName, secretKey string) pulumi.Map {
	return pulumi.Map{
		"name": pulumi.String(envName),
		"valueFrom": pulumi.Map{
			"secretKeyRef": pulumi.Map{
				"name": pulumi.String(externalDNSSecretName),
				"key":  pulumi.String(secretKey),
			},
		},
	}
}

// externalDNSCredentials builds the secret payload in the shape the provider's
// ExternalDNS integration expects. The values are the same ones cert-manager
// uses; only the keys and encoding differ.
func externalDNSCredentials(ctx *pulumi.Context, providerName string) (pulumi.StringMap, error) {
	switch providerName {
	case "azure":
		cfg := map[string]string{
			"tenantId":        utils.DNSConfigOptional(ctx, utils.DNSAzureTenantId),
			"subscriptionId":  utils.DNSConfigOptional(ctx, utils.DNSAzureSubscriptionId),
			"resourceGroup":   utils.DNSConfigOptional(ctx, utils.DNSAzureResourceGroup),
			"aadClientId":     utils.DNSConfigOptional(ctx, utils.DNSAzureClientId),
			"aadClientSecret": utils.DNSConfigOptional(ctx, utils.DNSAzureClientSecret),
		}
		encoded, err := json.Marshal(cfg)
		if err != nil {
			return nil, fmt.Errorf("failed to encode Azure DNS credentials: %w", err)
		}
		return pulumi.StringMap{"azure.json": pulumi.String(string(encoded))}, nil

	case "ovh":
		return pulumi.StringMap{
			utils.DNSOvhAppKey:      pulumi.String(utils.DNSConfigOptional(ctx, utils.DNSOvhAppKey)),
			utils.DNSOvhAppSecret:   pulumi.String(utils.DNSConfigOptional(ctx, utils.DNSOvhAppSecret)),
			utils.DNSOvhConsumerKey: pulumi.String(utils.DNSConfigOptional(ctx, utils.DNSOvhConsumerKey)),
		}, nil

	default:
		return nil, fmt.Errorf("ExternalDNS does not support DNS provider %q", providerName)
	}
}

// externalDNSOwnerID scopes the TXT registry to this lab so two labs sharing a
// DNS zone never delete each other's records.
func externalDNSOwnerID(ctx *pulumi.Context) string {
	if stack := ctx.Stack(); stack != "" {
		return "easylab-" + stack
	}
	return "easylab"
}
