package ovh

import (
	"easylab/utils"
	"fmt"
	"strconv"

	"github.com/ovh/pulumi-ovh/sdk/v2/go/ovh/cloudproject"
	local "github.com/pulumi/pulumi-command/sdk/go/command/local"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// NetworkInfrastructure holds network infrastructure that can be either created or reused
type NetworkInfrastructure struct {
	PrivateNetwork   *cloudproject.NetworkPrivate
	Subnet           *cloudproject.NetworkPrivateSubnet
	Gateway          *cloudproject.Gateway
	IsExisting       bool   // True if using existing infrastructure
	ExistingNetID    string // Existing network ID (if reusing)
	ExistingSubnetID string // Existing subnet ID (if reusing)
}

// InitNetworkInfrastructure creates or reuses network infrastructure based on configuration
func InitNetworkInfrastructure(ctx *pulumi.Context, serviceName string) (*NetworkInfrastructure, error) {
	// Check for existing infrastructure configuration
	existingNetworkId := utils.OvhConfigOptional(ctx, utils.OvhExistingNetworkId)
	existingSubnetId := utils.OvhConfigOptional(ctx, utils.OvhExistingSubnetId)

	// If both existing IDs are provided, reuse existing infrastructure
	if existingNetworkId != "" && existingSubnetId != "" {
		ctx.Log.Info(fmt.Sprintf("Reusing existing network infrastructure: network=%s, subnet=%s",
			existingNetworkId, existingSubnetId), nil)

		ctx.Export("privateNetworkId", pulumi.String(existingNetworkId))
		ctx.Export("subnetId", pulumi.String(existingSubnetId))
		ctx.Export("usingExistingNetwork", pulumi.Bool(true))

		return &NetworkInfrastructure{
			PrivateNetwork:   nil,
			Subnet:           nil,
			Gateway:          nil,
			IsExisting:       true,
			ExistingNetID:    existingNetworkId,
			ExistingSubnetID: existingSubnetId,
		}, nil
	}

	// Create new infrastructure
	ctx.Log.Info("Creating new network infrastructure", nil)

	privateNetwork, err := InitPrivateNetwork(ctx, serviceName)
	if err != nil {
		return nil, err
	}

	subnet, err := InitSubnet(ctx, serviceName, privateNetwork)
	if err != nil {
		return nil, err
	}

	gateway, err := InitGateway(ctx, serviceName, privateNetwork, subnet)
	if err != nil {
		return nil, err
	}

	ctx.Export("usingExistingNetwork", pulumi.Bool(false))

	return &NetworkInfrastructure{
		PrivateNetwork: privateNetwork,
		Subnet:         subnet,
		Gateway:        gateway,
		IsExisting:     false,
	}, nil
}

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

// InitManagedKubernetesClusterWithNetwork creates a K8s cluster using the network infrastructure
func InitManagedKubernetesClusterWithNetwork(ctx *pulumi.Context, serviceName string, netInfra *NetworkInfrastructure) (*cloudproject.Kube, error) {
	var kubeCluster *cloudproject.Kube
	var err error

	if netInfra.IsExisting {
		// Using existing network - use the pre-configured network ID
		kubeCluster, err = cloudproject.NewKube(ctx, "kubeCluster", &cloudproject.KubeArgs{
			ServiceName:      pulumi.String(serviceName),
			Name:             pulumi.String(utils.K8sConfig(ctx, utils.K8sClusterName)),
			Region:           pulumi.String(utils.OvhConfig(ctx, utils.OvhRegion)),
			PrivateNetworkId: pulumi.String(netInfra.ExistingNetID),
		})
	} else {
		// Using newly created network
		kubeCluster, err = cloudproject.NewKube(ctx, "kubeCluster", &cloudproject.KubeArgs{
			ServiceName:      pulumi.String(serviceName),
			Name:             pulumi.String(utils.K8sConfig(ctx, utils.K8sClusterName)),
			Region:           pulumi.String(utils.OvhConfig(ctx, utils.OvhRegion)),
			PrivateNetworkId: getNetworkId(ctx, netInfra.PrivateNetwork),
		}, pulumi.DependsOn([]pulumi.Resource{netInfra.Gateway}))
	}

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
		ctx.Log.Warn(fmt.Sprintf("failed to write kubeconfig to file: %v", err), nil)
	}

	return kubeCluster, nil
}

// InitManagedKubernetesCluster creates a K8s cluster (legacy function for backward compatibility)
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
		ctx.Log.Warn(fmt.Sprintf("failed to write kubeconfig to file: %v", err), nil)
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
