package utils

import (
	"fmt"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

func getConfig(ctx *pulumi.Context, group string, key string) string {
	return config.Require(ctx, fmt.Sprintf("%s:%s", group, key))
}

// Internal config group
const DEFAULT_WORK_DIR = "/tmp/easylab-jobs"
const DEFAULT_DATA_DIR = "/tmp/easylab-data"

// OVH config keys from environment variables
const OvhApplicationKey = "OVH_APPLICATION_KEY"
const OvhApplicationSecret = "OVH_APPLICATION_SECRET"
const OvhConsumerKey = "OVH_CONSUMER_KEY"
const OvhServiceName = "OVH_SERVICE_NAME"

// OVH config group
const OvhGroup = "network"
const OvhRegion = "region"
const OvhGatewayName = "gatewayName"
const OvhGatewayModel = "gatewayModel"
const OvhPrivateNetworkName = "privateNetworkName"
const OvhNetworkId = "networkId"
const OvhNetworkMask = "networkMask"
const OvhNetworkStartIP = "networkStartIp"
const OvhNetworkEndIP = "networkEndIp"

func OvhConfig(ctx *pulumi.Context, key string) string {
	return getConfig(ctx, OvhGroup, key)
}

// Node pool config group
const NodePoolGroup = "nodepool"
const NodePoolName = "name"
const NodePoolFlavor = "flavor"
const NodePoolDesiredNodeCount = "desiredNodeCount"
const NodePoolMinNodeCount = "minNodeCount"
const NodePoolMaxNodeCount = "maxNodeCount"

func NodePoolConfig(ctx *pulumi.Context, key string) string {
	return getConfig(ctx, NodePoolGroup, key)
}

func NodePoolConfigInt(ctx *pulumi.Context, key string) int {
	return config.RequireInt(ctx, fmt.Sprintf("%s:%s", NodePoolGroup, key))
}

// K8s config group
const K8sGroup = "k8s"
const K8sClusterName = "clusterName"

func K8sConfig(ctx *pulumi.Context, key string) string {
	return getConfig(ctx, K8sGroup, key)
}

// Coder config group
const CoderGroup = "coder"
const CoderAdminEmail = "adminEmail"
const CoderAdminPassword = "adminPassword"
const CoderVersion = "version"
const CoderDbUser = "dbUser"
const CoderDbPassword = "dbPassword"
const CoderDbName = "dbName"
const CoderTemplateName = "templateName"
const CoderTemplateFilePath = "templateFilePath"
const CoderTemplateSource = "templateSource"
const CoderTemplateGitRepo = "templateGitRepo"
const CoderTemplateGitFolder = "templateGitFolder"
const CoderTemplateGitBranch = "templateGitBranch"

func CoderConfig(ctx *pulumi.Context, key string) string {
	return getConfig(ctx, CoderGroup, key)
}

// CoderConfigOptional returns an optional config value (empty string if not set)
func CoderConfigOptional(ctx *pulumi.Context, key string) string {
	cfg := config.New(ctx, CoderGroup)
	// Try to get the config value, return empty string if not set
	val := cfg.Get(key)
	if val == "" {
		return ""
	}
	return val
}
