package pulumi

import (
	"easylab/azure"
	"easylab/coder"
	"easylab/k8s"
	"easylab/ovh"
	"easylab/utils"
	"fmt"
	"os"
	"path/filepath"

	_ "easylab/internal/providers/dns/ovh" // register OVH DNS provider

	k8sPkg "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi/config"
)

func resolveKubeconfigPath(relOrAbs, jobDir string) (string, error) {
	if filepath.IsAbs(relOrAbs) {
		return relOrAbs, nil
	}
	if jobDir != "" {
		return filepath.Join(jobDir, relOrAbs), nil
	}
	cwd, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("failed to get current working directory: %w", err)
	}
	return filepath.Join(cwd, relOrAbs), nil
}

// CreateLabProgram creates a Pulumi RunFunc that implements the lab infrastructure.
// It handles only declarative resource creation (cloud infra, Helm charts).
// Imperative operations (Coder user creation, template upload) are performed
// after Stack.Up() returns — see PulumiExecutor.initCoderAndTemplates().
func CreateLabProgram(jobDir string) pulumi.RunFunc {
	return func(ctx *pulumi.Context) error {
		useExisting := utils.K8sConfigOptional(ctx, utils.K8sUseExistingCluster)

		var k8sProvider *k8sPkg.Provider
		var kubeconfigOut pulumi.StringOutput

		if useExisting == "true" {
			utils.LogInfo(ctx, "Using existing Kubernetes cluster...")
			kubeconfigPath := utils.K8sConfig(ctx, utils.K8sExternalKubeconfigPath)

			absKubeconfigPath, pathErr := resolveKubeconfigPath(kubeconfigPath, jobDir)
			if pathErr != nil {
				return pathErr
			}

			var err error
			k8sProvider, err = k8s.InitK8sProviderFromKubeconfig(ctx, absKubeconfigPath)
			if err != nil {
				return fmt.Errorf("failed to create Kubernetes provider from kubeconfig: %w", err)
			}
			kubeconfigOut, err = k8s.KubeconfigFromFile(absKubeconfigPath)
			if err != nil {
				return fmt.Errorf("failed to read kubeconfig: %w", err)
			}
		} else {
			location := utils.AzureConfigOptional(ctx, utils.AzureLocation)
			isAzure := location != "" // azure:location is set only for Azure labs

			if isAzure {
				utils.LogInfo(ctx, "Starting Azure AKS infrastructure setup...")
				stackName := ctx.Stack()

				rg, err := azure.InitResourceGroup(ctx, location, stackName)
				if err != nil {
					return fmt.Errorf("failed to create Azure resource group: %w", err)
				}

				clusterName := utils.K8sConfigOptional(ctx, utils.K8sClusterName)
				npCfg := config.New(ctx, utils.NodePoolGroup)
				nodepoolName := npCfg.Get(utils.NodePoolName)
				vmSize := npCfg.Get(utils.NodePoolFlavor)
				desiredCount := npCfg.GetInt(utils.NodePoolDesiredNodeCount)
				minCount := npCfg.GetInt(utils.NodePoolMinNodeCount)
				maxCount := npCfg.GetInt(utils.NodePoolMaxNodeCount)

				clusterCfg := azure.ClusterConfig{
					ClusterName:  clusterName,
					NodePoolName: nodepoolName,
					VMSize:       vmSize,
					NodeCount:    desiredCount,
					MinNodeCount: minCount,
					MaxNodeCount: maxCount,
				}

				cluster, err := azure.InitManagedKubernetesCluster(ctx, rg, clusterCfg)
				if err != nil {
					return fmt.Errorf("failed to create AKS cluster: %w", err)
				}

				kubeconfig := azure.GetKubeconfig(ctx, cluster, rg)
				kubeconfigOut = kubeconfig

				k8sProvider, err = k8s.InitK8sProviderFromString(ctx, kubeconfig, []pulumi.Resource{cluster})
				if err != nil {
					return fmt.Errorf("failed to create Kubernetes provider: %w", err)
				}
			} else {
				serviceName := checkRequirements(ctx)

				utils.LogInfo(ctx, "Starting OVH infrastructure setup...")

				netInfra, err := ovh.InitNetworkInfrastructure(ctx, serviceName)
				if err != nil {
					return fmt.Errorf("failed to initialize network infrastructure: %w", err)
				}

				kubeCluster, err := ovh.InitManagedKubernetesClusterWithNetwork(ctx, serviceName, netInfra)
				if err != nil {
					return fmt.Errorf("failed to create Kubernetes cluster: %w", err)
				}

				nodepool, err := ovh.InitNodePools(ctx, serviceName, kubeCluster)
				if err != nil {
					return fmt.Errorf("failed to create node pools: %w", err)
				}

				k8sProvider, err = k8s.InitK8sProvider(ctx, kubeCluster, nodepool)
				if err != nil {
					return fmt.Errorf("failed to create Kubernetes provider: %w", err)
				}
				kubeconfigOut = kubeCluster.Kubeconfig
			}
		}

		utils.LogInfo(ctx, "Starting Coder setup (parallel mode)...")
		ns, err := k8s.InitNamespace(ctx, k8sProvider)
		if err != nil {
			return fmt.Errorf("failed to create namespace: %w", err)
		}

		infraResult, err := coder.SetupInfrastructureParallel(ctx, k8sProvider, ns)
		if err != nil {
			return fmt.Errorf("failed to setup infrastructure: %w", err)
		}

		extIp, err := k8s.GetExternalIP(ctx, kubeconfigOut, infraResult.CoderRelease)
		if err != nil {
			return fmt.Errorf("failed to get external IP: %w", err)
		}

		ctx.Export("externalIp", extIp)

		domain := utils.CoderConfigOptional(ctx, utils.CoderDomain)
		if domain != "" {
			_, ingressIP, httpsErr := coder.SetupHTTPS(ctx, k8sProvider, ns, infraResult.CoderRelease, kubeconfigOut)
			if httpsErr != nil {
				return fmt.Errorf("failed to setup HTTPS ingress: %w", httpsErr)
			}
			ctx.Export("ingressIP", ingressIP)
			ctx.Export("coderURL", pulumi.Sprintf("https://%s", domain))
		} else {
			ctx.Export("coderURL", pulumi.Sprintf("http://%s", extIp))
		}

		utils.LogInfo(ctx, "Infrastructure setup completed! Coder initialization continues after deployment.")
		return nil
	}
}

func checkRequirements(ctx *pulumi.Context) string {
	// Read service name from Pulumi config first (works with inline programs
	// where auto.EnvVars only propagates to plugin child processes, not to
	// the current process's os.Getenv).
	serviceName := utils.OvhCloudConfigOptional(ctx, utils.OvhCloudServiceName)
	if serviceName == "" {
		serviceName = os.Getenv(utils.OvhServiceName)
	}
	if serviceName == "" {
		_ = ctx.Log.Error("OVH service name (project ID) is not configured. "+
			"Set it via the credentials UI or the OVH_SERVICE_NAME environment variable.", nil)
	}
	return serviceName
}
