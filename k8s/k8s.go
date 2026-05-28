package k8s

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"easylab/utils"

	"github.com/ovh/pulumi-ovh/sdk/v2/go/ovh/cloudproject"
	k8s "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	k8score "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	"github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v3"
	k8smeta "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"gopkg.in/yaml.v3"
)

func InitK8sProvider(ctx *pulumi.Context, kubeCluster *cloudproject.Kube, nodePools []*cloudproject.KubeNodePool) (*k8s.Provider, error) {
	dependencies := []pulumi.Resource{kubeCluster}
	for _, np := range nodePools {
		dependencies = append(dependencies, np)
	}

	provider, err := k8s.NewProvider(ctx, "k8sProvider", &k8s.ProviderArgs{
		Kubeconfig: kubeCluster.Kubeconfig,
		KubeClientSettings: &k8s.KubeClientSettingsArgs{
			Timeout: pulumi.Int(900), // 15 min - avoid "context deadline exceeded" during Helm installs
		},
	}, pulumi.DependsOn(dependencies))
	if err != nil {
		return nil, err
	}
	return provider, nil
}

func InitK8sProviderFromString(ctx *pulumi.Context, kubeconfig pulumi.StringOutput, dependsOn []pulumi.Resource) (*k8s.Provider, error) {
	provider, err := k8s.NewProvider(ctx, "k8sProvider", &k8s.ProviderArgs{
		Kubeconfig: kubeconfig,
		KubeClientSettings: &k8s.KubeClientSettingsArgs{
			Timeout: pulumi.Int(900), // 15 min - avoid "context deadline exceeded" during Helm installs
		},
	}, pulumi.DependsOn(dependsOn))
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes provider from kubeconfig string: %w", err)
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
		KubeClientSettings: &k8s.KubeClientSettingsArgs{
			Timeout: pulumi.Int(900), // 15 min - avoid "context deadline exceeded" during Helm installs
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to create Kubernetes provider from kubeconfig: %w", err)
	}
	return provider, nil
}

// KubeconfigFromFile reads a kubeconfig file and returns its content as a StringOutput.
func KubeconfigFromFile(kubeconfigPath string) (pulumi.StringOutput, error) {
	content, err := os.ReadFile(kubeconfigPath)
	if err != nil {
		return pulumi.StringOutput{}, fmt.Errorf("failed to read kubeconfig from %s: %w", kubeconfigPath, err)
	}
	return pulumi.String(string(content)).ToStringOutput(), nil
}

func InitNamespace(ctx *pulumi.Context, provider *k8s.Provider) (*k8score.Namespace, error) {
	nsName := utils.CoderConfigOptional(ctx, utils.CoderNamespace)
	if nsName == "" {
		nsName = "coder"
	}
	namespace, err := k8score.NewNamespace(ctx, "coder", &k8score.NamespaceArgs{
		Metadata: &k8smeta.ObjectMetaArgs{
			Name: pulumi.String(nsName),
		},
	}, pulumi.Provider(provider))
	if err != nil {
		return nil, fmt.Errorf("failed to create namespace: %w", err)
	}
	return namespace, nil
}

// GetExternalIP returns the LoadBalancer IP assigned to the Coder service.
// kubeconfigOut is the kubeconfig content as a StringOutput, used to call the
// Kubernetes API directly — bypassing the Pulumi provider's pending-initialisation
// await which blocks indefinitely when OVHcloud sets ipMode:VIP on LoadBalancer services.
func GetExternalIP(ctx *pulumi.Context, kubeconfigOut pulumi.StringOutput, coderRelease *helm.Release) (pulumi.StringOutput, error) {
	nsName := utils.CoderConfigOptional(ctx, utils.CoderNamespace)
	if nsName == "" {
		nsName = "coder"
	}

	externalIP := GetServiceIP(kubeconfigOut, coderRelease.ResourceNames, nsName, "coder")
	ctx.Export("externalIP", externalIP)
	return externalIP, nil
}

// GetServiceIP returns the LoadBalancer IP of a Kubernetes service by calling the
// Kubernetes API directly, bypassing Pulumi's provider await machinery.
// kubeconfigOut is the kubeconfig content as a StringOutput.
// trigger is any Pulumi output whose resolution gates this lookup, ensuring the
// service exists before the read (e.g. a Helm release's ResourceNames output).
func GetServiceIP(kubeconfigOut pulumi.StringOutput, trigger interface{}, namespace, name string) pulumi.StringOutput {
	return pulumi.All(kubeconfigOut, trigger).ApplyT(func(args []interface{}) (string, error) {
		kubeconfig, _ := args[0].(string)
		if kubeconfig == "" {
			return "", fmt.Errorf("kubeconfig is empty")
		}
		return fetchServiceIP(kubeconfig, namespace, name)
	}).(pulumi.StringOutput)
}

// kubeconfigYAML is a minimal struct for parsing the fields we need from a kubeconfig file.
type kubeconfigYAML struct {
	Clusters []struct {
		Cluster struct {
			Server                   string `yaml:"server"`
			CertificateAuthorityData string `yaml:"certificate-authority-data"`
		} `yaml:"cluster"`
		Name string `yaml:"name"`
	} `yaml:"clusters"`
	Users []struct {
		User struct {
			ClientCertificateData string `yaml:"client-certificate-data"`
			ClientKeyData         string `yaml:"client-key-data"`
			Token                 string `yaml:"token"`
		} `yaml:"user"`
		Name string `yaml:"name"`
	} `yaml:"users"`
	Contexts []struct {
		Context struct {
			Cluster string `yaml:"cluster"`
			User    string `yaml:"user"`
		} `yaml:"context"`
		Name string `yaml:"name"`
	} `yaml:"contexts"`
	CurrentContext string `yaml:"current-context"`
}

// fetchServiceIP reads a service's LoadBalancer IP via a direct HTTPS call to the
// Kubernetes API, using client-certificate credentials from the kubeconfig content.
// It retries for up to 10 minutes to handle the race condition where the cloud
// provider (e.g. OVHcloud) has not yet assigned the LoadBalancer IP.
func fetchServiceIP(kubeconfigContent, namespace, serviceName string) (string, error) {
	var kc kubeconfigYAML
	if err := yaml.Unmarshal([]byte(kubeconfigContent), &kc); err != nil {
		return "", fmt.Errorf("failed to parse kubeconfig: %w", err)
	}

	var clusterName, userName string
	for _, c := range kc.Contexts {
		if c.Name == kc.CurrentContext {
			clusterName = c.Context.Cluster
			userName = c.Context.User
			break
		}
	}

	var serverURL, caCertB64 string
	for _, c := range kc.Clusters {
		if c.Name == clusterName {
			serverURL = c.Cluster.Server
			caCertB64 = c.Cluster.CertificateAuthorityData
			break
		}
	}

	var clientCertB64, clientKeyB64, token string
	for _, u := range kc.Users {
		if u.Name == userName {
			clientCertB64 = u.User.ClientCertificateData
			clientKeyB64 = u.User.ClientKeyData
			token = u.User.Token
			break
		}
	}

	caCertPEM, err := base64.StdEncoding.DecodeString(caCertB64)
	if err != nil {
		return "", fmt.Errorf("failed to decode CA cert: %w", err)
	}

	caCertPool := x509.NewCertPool()
	if !caCertPool.AppendCertsFromPEM(caCertPEM) {
		return "", fmt.Errorf("failed to parse CA certificate")
	}

	tlsCfg := &tls.Config{RootCAs: caCertPool}

	// Client certificate auth (OVH, AKS local accounts).
	// Token auth (AKS with Azure AD / managed identity): skip cert setup.
	if clientCertB64 != "" && clientKeyB64 != "" {
		clientCertPEM, decErr := base64.StdEncoding.DecodeString(clientCertB64)
		if decErr != nil {
			return "", fmt.Errorf("failed to decode client cert: %w", decErr)
		}
		clientKeyPEM, decErr := base64.StdEncoding.DecodeString(clientKeyB64)
		if decErr != nil {
			return "", fmt.Errorf("failed to decode client key: %w", decErr)
		}
		cert, keyErr := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
		if keyErr != nil {
			return "", fmt.Errorf("failed to build TLS key pair: %w", keyErr)
		}
		tlsCfg.Certificates = []tls.Certificate{cert}
	} else if token == "" {
		return "", fmt.Errorf("kubeconfig for user %q has no client certificate and no token", userName)
	}

	transport := &http.Transport{TLSClientConfig: tlsCfg}
	client := &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}

	apiURL := fmt.Sprintf("%s/api/v1/namespaces/%s/services/%s", serverURL, namespace, serviceName)

	// Retry for up to 10 minutes (40 × 15 s). This handles two cases:
	//   1. The cloud provider hasn't assigned the LoadBalancer IP yet.
	//   2. The API server is temporarily unreachable (transient network blip).
	var lastErr error
	for attempt := 0; attempt < 40; attempt++ {
		if attempt > 0 {
			time.Sleep(15 * time.Second)
		}

		req, reqErr := http.NewRequest(http.MethodGet, apiURL, nil)
		if reqErr != nil {
			return "", fmt.Errorf("failed to build API request: %w", reqErr)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, reqErr := client.Do(req)
		if reqErr != nil {
			lastErr = fmt.Errorf("attempt %d: failed to call Kubernetes API: %w", attempt+1, reqErr)
			continue
		}

		if resp.StatusCode == http.StatusNotFound {
			resp.Body.Close()
			return "", fmt.Errorf("service %s/%s not found in cluster", namespace, serviceName)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return "", fmt.Errorf("Kubernetes API returned status %d for service %s/%s", resp.StatusCode, namespace, serviceName)
		}

		var svcStatus struct {
			Status struct {
				LoadBalancer struct {
					Ingress []struct {
						IP string `json:"ip"`
					} `json:"ingress"`
				} `json:"loadBalancer"`
			} `json:"status"`
		}
		if decodeErr := json.NewDecoder(resp.Body).Decode(&svcStatus); decodeErr != nil {
			resp.Body.Close()
			return "", fmt.Errorf("failed to decode Kubernetes API response: %w", decodeErr)
		}
		resp.Body.Close()

		if len(svcStatus.Status.LoadBalancer.Ingress) > 0 && svcStatus.Status.LoadBalancer.Ingress[0].IP != "" {
			return svcStatus.Status.LoadBalancer.Ingress[0].IP, nil
		}
		lastErr = fmt.Errorf("attempt %d: service %s/%s has no LoadBalancer IP yet", attempt+1, namespace, serviceName)
	}
	return "", fmt.Errorf("timed out waiting 10 min for LoadBalancer IP of service %s/%s: %w", namespace, serviceName, lastErr)
}
