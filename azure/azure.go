package azure

import (
	"encoding/base64"
	"fmt"

	containerservice "github.com/pulumi/pulumi-azure-native-sdk/containerservice/v3"
	"github.com/pulumi/pulumi-azure-native-sdk/resources/v3"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// ClusterConfig holds configuration for AKS cluster provisioning.
type ClusterConfig struct {
	ClusterName  string
	NodePoolName string
	VMSize       string
	NodeCount    int
	MinNodeCount int
	MaxNodeCount int
}

// InitResourceGroup creates an Azure Resource Group for the lab.
func InitResourceGroup(ctx *pulumi.Context, location, stackName string) (*resources.ResourceGroup, error) {
	rgName := fmt.Sprintf("easylab-%s", stackName)
	rg, err := resources.NewResourceGroup(ctx, "resourceGroup", &resources.ResourceGroupArgs{
		ResourceGroupName: pulumi.String(rgName),
		Location:          pulumi.String(location),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create resource group: %w", err)
	}
	ctx.Export("resourceGroupName", rg.Name)
	return rg, nil
}

// InitManagedKubernetesCluster creates an AKS cluster with AKS-managed networking.
func InitManagedKubernetesCluster(ctx *pulumi.Context, rg *resources.ResourceGroup, cfg ClusterConfig) (*containerservice.ManagedCluster, error) {
	enableAutoScaling := cfg.MinNodeCount > 0 && cfg.MaxNodeCount > 0
	nodeCount := cfg.NodeCount
	if nodeCount == 0 {
		nodeCount = 1
	}

	pool := containerservice.ManagedClusterAgentPoolProfileArgs{
		Name:         pulumi.String(cfg.NodePoolName),
		Count:        pulumi.Int(nodeCount),
		VmSize:       pulumi.String(cfg.VMSize),
		Mode:         pulumi.String("System"),
		OsType:       pulumi.String("Linux"),
		OsDiskSizeGB: pulumi.Int(0), // Use the default disk size for the selected vmSize
	}
	if enableAutoScaling {
		pool.EnableAutoScaling = pulumi.Bool(true)
		pool.MinCount = pulumi.Int(cfg.MinNodeCount)
		pool.MaxCount = pulumi.Int(cfg.MaxNodeCount)
	}

	cluster, err := containerservice.NewManagedCluster(ctx, "aksCluster", &containerservice.ManagedClusterArgs{
		ResourceGroupName: rg.Name,
		Location:          rg.Location,
		ResourceName:      pulumi.String(fmt.Sprintf("%s-%s", "easylab", cfg.ClusterName)),
		DnsPrefix:         pulumi.String(cfg.ClusterName),
		AgentPoolProfiles: containerservice.ManagedClusterAgentPoolProfileArray{pool},
		Identity: &containerservice.ManagedClusterIdentityArgs{
			Type: containerservice.ResourceIdentityTypeSystemAssigned,
		},
	}, pulumi.DependsOn([]pulumi.Resource{rg}))
	if err != nil {
		return nil, fmt.Errorf("failed to create AKS cluster: %w", err)
	}

	ctx.Export("aksClusterName", cluster.Name)
	return cluster, nil
}

// GetKubeconfig retrieves the admin kubeconfig for the AKS cluster as a Pulumi output.
// The credential value returned by the API is base64-encoded; this function decodes it.
func GetKubeconfig(ctx *pulumi.Context, cluster *containerservice.ManagedCluster, rg *resources.ResourceGroup) pulumi.StringOutput {
	creds := containerservice.ListManagedClusterUserCredentialsOutput(ctx,
		containerservice.ListManagedClusterUserCredentialsOutputArgs{
			ResourceGroupName: rg.Name,
			ResourceName:      cluster.Name,
		},
		pulumi.DependsOn([]pulumi.Resource{cluster}),
	)

	kubeconfig := creds.Kubeconfigs().Index(pulumi.Int(0)).Value().ApplyT(func(encoded string) (string, error) {
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return "", fmt.Errorf("failed to decode kubeconfig: %w", err)
		}
		return string(decoded), nil
	}).(pulumi.StringOutput)

	ctx.Export("kubeconfig", pulumi.ToSecret(kubeconfig))
	return kubeconfig
}
