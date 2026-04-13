package pulumi

import (
	"easylab/coder"
	"easylab/k8s"
	"easylab/ovh"
	"easylab/utils"
	"fmt"
	"os"
	"path/filepath"

	k8sPkg "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// CreateLabProgram creates a Pulumi RunFunc that implements the lab infrastructure.
// It handles only declarative resource creation (cloud infra, Helm charts).
// Imperative operations (Coder user creation, template upload) are performed
// after Stack.Up() returns — see PulumiExecutor.initCoderAndTemplates().
func CreateLabProgram() pulumi.RunFunc {
	return func(ctx *pulumi.Context) error {
		useExisting := utils.K8sConfigOptional(ctx, utils.K8sUseExistingCluster)

		var k8sProvider *k8sPkg.Provider

		if useExisting == "true" {
			utils.LogInfo(ctx, "Using existing Kubernetes cluster...")
			kubeconfigPath := utils.K8sConfig(ctx, utils.K8sExternalKubeconfigPath)

			cwd, cwdErr := os.Getwd()
			if cwdErr != nil {
				return fmt.Errorf("failed to get current working directory: %w", cwdErr)
			}
			absKubeconfigPath := filepath.Join(cwd, kubeconfigPath)

			var err error
			k8sProvider, err = k8s.InitK8sProviderFromKubeconfig(ctx, absKubeconfigPath)
			if err != nil {
				return fmt.Errorf("failed to create Kubernetes provider from kubeconfig: %w", err)
			}
		} else {
			serviceName := checkRequirements(ctx)

			utils.LogInfo(ctx, "Starting infrastructure setup...")

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

		extIp, err := k8s.GetExternalIP(ctx, k8sProvider, infraResult.CoderRelease)
		if err != nil {
			return fmt.Errorf("failed to get external IP: %w", err)
		}

		ctx.Export("coderURL", pulumi.Sprintf("http://%s", extIp))
		ctx.Export("externalIp", extIp)

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
