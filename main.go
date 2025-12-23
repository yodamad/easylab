package main

import (
	"fmt"
	"labascode/coder"
	"labascode/k8s"
	"labascode/ovh"
	"labascode/utils"
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

		// Initiliaze Coder elements
		utils.LogInfo(ctx, "Starting Coder setup...")
		ns, err := k8s.InitNamespace(ctx, k8sProvider)
		if err != nil {
			return fmt.Errorf("failed to create namespace: %w", err)
		}

		coder.SetupDB(ctx, k8sProvider, ns)
		coder.SetupDBSecret(ctx, k8sProvider, ns)
		coderRelease, err := coder.SetupCoder(ctx, k8sProvider, ns)
		if err != nil {
			return fmt.Errorf("failed to setup coder: %w", err)
		}

		extIp, err := k8s.GetExternalIP(ctx, k8sProvider, coderRelease)
		if err != nil {
			return fmt.Errorf("failed to get external IP: %w", err)
		}

		ctx.Export("coderURL", pulumi.Sprintf("http://%s", extIp))

		coderConfig := coder.InitCoderOutput(ctx, extIp)
		ctx.Export("coderServerURL", coderConfig.ServerURL)
		ctx.Export("coderSessionToken", coderConfig.SessionToken)
		ctx.Export("coderOrganizationID", coderConfig.OrganizationID)
		utils.LogInfo(ctx, "Setup completed successfully!")

		zipFile, _ := utils.CloneFolderFromGitAndZipIt("https://gitlab.com/yodamad-workshops/coder-templates#", "docker")

		templateOutput := coder.CreateTemplateFromZip(ctx, coderConfig, utils.CoderConfig(ctx, utils.CoderTemplateName), "file://"+zipFile)
		templateOutput.ApplyT(func(_ interface{}) error {
			utils.LogInfo(ctx, "Template created successfully!")
			return nil
		})

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
