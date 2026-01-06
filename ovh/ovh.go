package ovh

import (
	"fmt"
	"labascode/utils"
	"strconv"

	"github.com/ovh/pulumi-ovh/sdk/v2/go/ovh/cloudproject"
	local "github.com/pulumi/pulumi-command/sdk/go/command/local"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func InitSubnet(ctx *pulumi.Context, serviceName string, privateNetwork *cloudproject.NetworkPrivate) (*cloudproject.NetworkPrivateSubnet, error) {
	subnet, _ := cloudproject.NewNetworkPrivateSubnet(ctx, "subnet", &cloudproject.NetworkPrivateSubnetArgs{
		ServiceName: pulumi.String(serviceName),
		NetworkId:   privateNetwork.ID(),
		Network:     pulumi.String(utils.OvhConfig(ctx, utils.OvhNetworkMask)),
		Region:      pulumi.String(utils.OvhConfig(ctx, utils.OvhRegion)),
		Start:       pulumi.String(utils.OvhConfig(ctx, utils.OvhNetworkStartIP)),
		End:         pulumi.String(utils.OvhConfig(ctx, utils.OvhNetworkEndIP)),
	})
	ctx.Export("subnetId", subnet.ID())
	return subnet, nil
}

func InitPrivateNetwork(ctx *pulumi.Context, serviceName string) (*cloudproject.NetworkPrivate, error) {

	networkId, _ := strconv.Atoi(utils.OvhConfig(ctx, utils.OvhNetworkId))

	privateNetwork, err := cloudproject.NewNetworkPrivate(ctx, "privateNetwork-v2", &cloudproject.NetworkPrivateArgs{
		VlanId:      pulumi.Int(networkId),
		ServiceName: pulumi.String(serviceName),
		Name:        pulumi.String(utils.OvhConfig(ctx, utils.OvhPrivateNetworkName)),
		Regions:     pulumi.StringArray{pulumi.String(utils.OvhConfig(ctx, utils.OvhRegion))},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create private network: %w", err)
	}

	ctx.Export("privateNetworkId", privateNetwork.ID())
	return privateNetwork, nil
}

func getNetworkId(ctx *pulumi.Context, privateNetwork *cloudproject.NetworkPrivate) pulumi.StringOutput {
	location := utils.OvhConfig(ctx, utils.OvhRegion)
	return privateNetwork.RegionsOpenstackIds.ApplyT(func(ids map[string]string) string {
		return ids[location]
	}).(pulumi.StringOutput)
}

func InitGateway(ctx *pulumi.Context, serviceName string, privateNetwork *cloudproject.NetworkPrivate, subnet *cloudproject.NetworkPrivateSubnet) (*cloudproject.Gateway, error) {
	gateway, err := cloudproject.NewGateway(ctx, "gateway", &cloudproject.GatewayArgs{
		ServiceName: pulumi.String(serviceName),
		Name:        pulumi.String(utils.OvhConfig(ctx, utils.OvhGatewayName)),
		Model:       pulumi.String(utils.OvhConfig(ctx, utils.OvhGatewayModel)),
		Region:      pulumi.String(utils.OvhConfig(ctx, utils.OvhRegion)),
		NetworkId:   getNetworkId(ctx, privateNetwork),
		SubnetId:    subnet.ID(),
	}, pulumi.DependsOn([]pulumi.Resource{privateNetwork, subnet}))
	if err != nil {
		return nil, fmt.Errorf("failed to create gateway: %w", err)
	}
	return gateway, nil
}

func InitManagedKubernetesCluster(ctx *pulumi.Context, serviceName string, privateNetwork *cloudproject.NetworkPrivate, subnet *cloudproject.NetworkPrivateSubnet, gateway *cloudproject.Gateway) (*cloudproject.Kube, error) {
	// Create managed Kubernetes cluster
	kubeCluster, err := cloudproject.NewKube(ctx, "kubeCluster", &cloudproject.KubeArgs{
		ServiceName:      pulumi.String(serviceName),
		Name:             pulumi.String(utils.K8sConfig(ctx, utils.K8sClusterName)),
		Region:           pulumi.String(utils.OvhConfig(ctx, utils.OvhRegion)),
		PrivateNetworkId: getNetworkId(ctx, privateNetwork),
	}, pulumi.DependsOn([]pulumi.Resource{gateway}))
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes cluster: %w", err)
	}

	ctx.Export("kubeClusterId", kubeCluster.ID())
	ctx.Export("kubeClusterName", kubeCluster.Name)
	ctx.Export("kubeconfig", pulumi.ToSecret(kubeCluster.Kubeconfig))
	// Export kubeconfig to file
	_, err = local.NewCommand(ctx, "writeKubeconfig", &local.CommandArgs{
		Create: pulumi.Sprintf(
			"echo '%s' > kubeconfig.yaml",
			kubeCluster.Kubeconfig,
		),
	})
	if err != nil {
		fmt.Errorf("failed to write kubeconfig to file: %w", err)
	}

	return kubeCluster, nil
}

func InitNodePools(ctx *pulumi.Context, serviceName string, kubeCluster *cloudproject.Kube) ([]*cloudproject.KubeNodePool, error) {
	// Create node pools
	nodePoolIds := pulumi.StringArray{}
	var nodePools []*cloudproject.KubeNodePool
	for i := 0; i < 1; i++ {
		nodePoolName := fmt.Sprintf("%s-%d", utils.NodePoolConfig(ctx, utils.NodePoolName), i+1)
		nodePool, err := cloudproject.NewKubeNodePool(ctx, fmt.Sprintf("nodePool%d", i+1), &cloudproject.KubeNodePoolArgs{
			ServiceName:  pulumi.String(serviceName),
			KubeId:       kubeCluster.ID(),
			Name:         pulumi.String(nodePoolName),
			FlavorName:   pulumi.String(utils.NodePoolConfig(ctx, utils.NodePoolFlavor)),
			DesiredNodes: pulumi.Int(utils.NodePoolConfigInt(ctx, utils.NodePoolDesiredNodeCount)),
			MaxNodes:     pulumi.Int(utils.NodePoolConfigInt(ctx, utils.NodePoolMaxNodeCount)),
			MinNodes:     pulumi.Int(utils.NodePoolConfigInt(ctx, utils.NodePoolMinNodeCount)),
		}, pulumi.DependsOn([]pulumi.Resource{kubeCluster}))
		if err != nil {
			return nil, fmt.Errorf("failed to create node pool %d: %w", i+1, err)
		}
		nodePoolIds = append(nodePoolIds, nodePool.ID())
		// add nodepool in an array
		nodePools = append(nodePools, nodePool)
		ctx.Export(fmt.Sprintf("nodePool%dId", i+1), nodePool.ID())
		ctx.Export(fmt.Sprintf("nodePool%dName", i+1), nodePool.Name)
	}

	ctx.Export("nodePoolIds", nodePoolIds)
	return nodePools, nil
}
