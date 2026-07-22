package main

import (
	"easylab/k8s"
	"easylab/ovh"
	"easylab/utils"
	"fmt"
	"os"
	"slices"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

func main() {
	pulumi.Run(func(ctx *pulumi.Context) error {
		serviceName := checkRequirements(ctx)

		// Initialize infrastructure
		utils.LogInfo(ctx, "Starting infrastructure setup...")

		privateNetwork, err := ovh.InitPrivateNetwork(ctx, serviceName)
		if err != nil {
			return fmt.Errorf("failed to create private network: %w", err)
		}

		subnet, err := ovh.InitSubnet(ctx, serviceName, privateNetwork)
		if err != nil {
			return fmt.Errorf("failed to create subnet: %w", err)
		}

		gateway, err := ovh.InitGateway(ctx, serviceName, privateNetwork, subnet)
		if err != nil {
			return fmt.Errorf("failed to create gateway: %w", err)
		}

		kubeCluster, err := ovh.InitManagedKubernetesCluster(ctx, serviceName, privateNetwork, subnet, gateway)
		if err != nil {
			return fmt.Errorf("failed to create Kubernetes cluster: %w", err)
		}

		nodepool, err := ovh.InitNodePools(ctx, serviceName, kubeCluster)
		if err != nil {
			return fmt.Errorf("failed to create node pools: %w", err)
		}

		k8sProvider, err := k8s.InitK8sProvider(ctx, kubeCluster, nodepool)
		if err != nil {
			return fmt.Errorf("failed to create Kubernetes provider: %w", err)
		}

		// Create the namespace student workspaces will be provisioned into.
		utils.LogInfo(ctx, "Creating workspace namespace...")
		if _, err = k8s.InitNamespace(ctx, k8sProvider); err != nil {
			return fmt.Errorf("failed to create namespace: %w", err)
		}

		ctx.Export("kubeconfig", kubeCluster.Kubeconfig)
		utils.LogInfo(ctx, "Setup completed successfully!")

		return nil
	})
}

func checkRequirements(ctx *pulumi.Context) string {
	ovhVars := []string{os.Getenv(utils.OvhApplicationSecret), os.Getenv(utils.OvhApplicationKey),
		os.Getenv(utils.OvhServiceName), os.Getenv(utils.OvhConsumerKey)}
	if slices.Contains(ovhVars, "") {
		_ = ctx.Log.Error("A mandatory variable is missing, "+
			"check that all these variables are set: "+
			"OVH_APPLICATION_SECRET, OVH_APPLICATION_KEY, OVH_SERVICE_NAME, OVH_CONSUMER_KEY",
			nil)
	}
	return os.Getenv(utils.OvhServiceName)
}
