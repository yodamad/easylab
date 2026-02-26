package k8s

import (
	"fmt"
	"os"

	"github.com/ovh/pulumi-ovh/sdk/v2/go/ovh/cloudproject"
	k8s "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	k8score "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	"github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v3"
	k8smeta "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func InitK8sProvider(ctx *pulumi.Context, kubeCluster *cloudproject.Kube, nodePools []*cloudproject.KubeNodePool) (*k8s.Provider, error) {
	dependencies := []pulumi.Resource{kubeCluster}
	for _, np := range nodePools {
		dependencies = append(dependencies, np)
	}

	provider, err := k8s.NewProvider(ctx, "k8sProvider", &k8s.ProviderArgs{
		Kubeconfig: kubeCluster.Kubeconfig,
	}, pulumi.DependsOn(dependencies))
	if err != nil {
		return nil, err
	}
	return provider, nil
}

func InitK8sProviderFromKubeconfig(ctx *pulumi.Context, kubeconfigPath string) (*k8s.Provider, error) {
	content, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read kubeconfig from %s: %w", kubeconfigPath, err)
	}

	provider, err := k8s.NewProvider(ctx, "k8sProvider", &k8s.ProviderArgs{
		Kubeconfig: pulumi.String(string(content)),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes provider from kubeconfig: %w", err)
	}
	return provider, nil
}

func InitNamespace(ctx *pulumi.Context, provider *k8s.Provider) (*k8score.Namespace, error) {
	namespace, err := k8score.NewNamespace(ctx, "coder", &k8score.NamespaceArgs{
		Metadata: &k8smeta.ObjectMetaArgs{
			Name: pulumi.String("coder"),
		},
	}, pulumi.Provider(provider))
	if err != nil {
		return nil, fmt.Errorf("failed to create namespace: %w", err)
	}
	return namespace, nil
}

func GetExternalIP(ctx *pulumi.Context, provider *k8s.Provider, coderRelease *helm.Release) (pulumi.StringOutput, error) {
	service, err := k8score.GetService(ctx, "coder", pulumi.ID("coder/coder"), nil, pulumi.Provider(provider), pulumi.DependsOn([]pulumi.Resource{coderRelease}))
	if err != nil {
		return pulumi.StringOutput{}, fmt.Errorf("failed to get coder service: %w", err)
	}

	// Attendre que l'IP externe soit assignée et la retourner
	externalIP := service.Status.LoadBalancer().Ingress().Index(pulumi.Int(0)).Ip().ApplyT(func(ip *string) string {
		if ip != nil {
			return *ip
		}
		return ""
	}).(pulumi.StringOutput)

	ctx.Export("externalIP", externalIP)

	return externalIP, nil
}
