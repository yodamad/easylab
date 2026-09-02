package kube

import (
	"context"
	"crypto/sha1"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"

	"easylab/internal/providers/workspace"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	labelBakeLabID    = "easylab.io/bake-lab-id"
	labelBakeTemplate = "easylab.io/bake-template"

	// bakeTTLSecondsAfterFinished cleans up a finished bake Job automatically, so a
	// forgotten one (or a missed delete-before-recreate) doesn't linger forever.
	bakeTTLSecondsAfterFinished = int32(3600)
)

// bakeJobName is deterministic per (labID, template) so EnsureBakeJob/BakeJobStatus
// agree on which Job they mean without persisting a separate name anywhere.
func bakeJobName(labID, template string) string {
	sum := sha1.Sum([]byte(sanitizeDNS(labID) + "\x00" + sanitizeDNS(template)))
	return "bake-" + hex.EncodeToString(sum[:])[:16]
}

func bakeLabels(labID, template string) map[string]string {
	// Deliberately not registryCacheLabels()/Backend.labels()'s shape: this must
	// never carry easylab.io/lab-id, which is exactly what ListWorkspaces/
	// GetWorkspace select workspaces by — a bake Job must never show up as one.
	return map[string]string{
		labelManagedBy:    managedByValue,
		labelBakeLabID:    sanitizeDNS(labID),
		labelBakeTemplate: sanitizeDNS(template),
	}
}

// EnsureBakeJob (re)starts a bake for req.LabID/req.Template, pushing the built image
// to the internal (in-cluster, plain-HTTP) address of BakedImageRepo — envbuilder is a
// userspace HTTP client, so it needs no TLS trust the way a kubelet pull would.
//
// A Job's pod template is immutable, so unlike every other Ensure* in this package
// this is delete-then-create rather than idempotent create-and-swallow-AlreadyExists.
func (b *Backend) EnsureBakeJob(ctx context.Context, req workspace.BakeRequest) error {
	if req.Devcontainer == nil {
		return fmt.Errorf("bake request missing devcontainer config")
	}
	name := bakeJobName(req.LabID, req.Template)
	labels := bakeLabels(req.LabID, req.Template)

	propagation := metav1.DeletePropagationBackground
	if err := b.client.BatchV1().Jobs(b.namespace).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &propagation}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to remove previous bake job: %w", err)
	}

	dc := req.Devcontainer
	dockerConfig, err := b.dockerConfigFromSecret(ctx, dc.RegistryAuthSecret)
	if err != nil {
		return err
	}
	internalRepo, _ := b.BakedImageRepo(req.LabID, req.Template, "")

	container := corev1.Container{
		Name:            "bake",
		Image:           envbuilderImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Env:             bakeEnv(req, internalRepo, dockerConfig),
		Resources:       buildResources(req.CPU, req.Memory, req.CPULimit, req.MemoryLimit, true),
	}

	var initContainers []corev1.Container
	var volumes []corev1.Volume
	if repo := strings.TrimSpace(dc.ConfigRepo); repo != "" {
		initContainers = append(initContainers, devcontainerConfigCloneInit(repo, dc.ConfigBranch, dc.ConfigAuthSecret))
		volumes = append(volumes, corev1.Volume{
			Name:         devcontainerConfigVolumeName,
			VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
		})
		container.VolumeMounts = []corev1.VolumeMount{{Name: devcontainerConfigVolumeName, MountPath: devcontainerConfigMountPath}}
	}

	backoffLimit := int32(0)
	ttl := bakeTTLSecondsAfterFinished
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: b.namespace, Labels: labels},
		Spec: batchv1.JobSpec{
			BackoffLimit:            &backoffLimit,
			TTLSecondsAfterFinished: &ttl,
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					RestartPolicy:  corev1.RestartPolicyNever,
					InitContainers: initContainers,
					Containers:     []corev1.Container{container},
					Volumes:        volumes,
				},
			},
		},
	}
	if _, err := b.client.BatchV1().Jobs(b.namespace).Create(ctx, job, metav1.CreateOptions{}); err != nil {
		return fmt.Errorf("failed to create bake job: %w", err)
	}
	return nil
}

// bakeEnv builds the bake Job's envbuilder configuration — a narrower subset of
// devcontainerEnv's: there is no IDE to protect and no live workspace folder, and the
// init script is a trivial no-op rather than an IDE exec (see BakeJobStatus).
func bakeEnv(req workspace.BakeRequest, internalRepo, dockerConfig string) []corev1.EnvVar {
	dc := req.Devcontainer

	env := []corev1.EnvVar{
		{Name: "ENVBUILDER_GIT_URL", Value: gitURLWithRef(req.GitRepo, req.GitBranch)},
		{Name: "ENVBUILDER_CACHE_REPO", Value: internalRepo},
		{Name: "ENVBUILDER_PUSH_IMAGE", Value: "true"},
		// envbuilder execs the init script (process replacement) once the build is
		// done; a trivial, immediately-exiting script is what lets the Job's pod
		// reach Complete on its own — the push has already happened by this point,
		// so Job Complete is a sufficient, black-box completion signal.
		{Name: "ENVBUILDER_INIT_SCRIPT", Value: "true"},
		{Name: "ENVBUILDER_EXIT_ON_BUILD_FAILURE", Value: "true"},
	}

	configRepo := strings.TrimSpace(dc.ConfigRepo)
	if configRepo != "" {
		dir := strings.TrimSpace(dc.Dir)
		if dir == "" {
			dir = defaultDevcontainerDirName
		}
		env = append(env, corev1.EnvVar{Name: "ENVBUILDER_DEVCONTAINER_DIR", Value: path.Join(devcontainerConfigMountPath, dir)})
	} else if v := strings.TrimSpace(dc.Dir); v != "" {
		env = append(env, corev1.EnvVar{Name: "ENVBUILDER_DEVCONTAINER_DIR", Value: v})
	}
	if v := strings.TrimSpace(dc.FallbackImage); v != "" {
		env = append(env, corev1.EnvVar{Name: "ENVBUILDER_FALLBACK_IMAGE", Value: v})
	}
	if dockerConfig != "" {
		env = append(env, corev1.EnvVar{Name: "ENVBUILDER_DOCKER_CONFIG_BASE64", Value: dockerConfig})
	}
	if dc.Insecure {
		env = append(env, corev1.EnvVar{Name: "ENVBUILDER_INSECURE", Value: "true"})
	}
	if s := strings.TrimSpace(req.GitAuthSecret); s != "" {
		env = append(env, basicAuthEnv(s, "ENVBUILDER_GIT_USERNAME", "ENVBUILDER_GIT_PASSWORD")...)
	}
	return env
}

// BakeJobStatus reports the current state of the most recent bake Job for
// labID/template, driven entirely by the Job's own status conditions — no registry
// polling needed, since the push happens before the (trivial) init-script step that
// gates the Job reaching Complete.
func (b *Backend) BakeJobStatus(ctx context.Context, labID, template string) (workspace.BakeState, error) {
	job, err := b.client.BatchV1().Jobs(b.namespace).Get(ctx, bakeJobName(labID, template), metav1.GetOptions{})
	if err != nil {
		if apierrors.IsNotFound(err) {
			return workspace.BakeStateNone, nil
		}
		return workspace.BakeStateNone, fmt.Errorf("failed to look up bake job: %w", err)
	}
	for _, c := range job.Status.Conditions {
		if c.Status != corev1.ConditionTrue {
			continue
		}
		switch c.Type {
		case batchv1.JobComplete:
			return workspace.BakeStateComplete, nil
		case batchv1.JobFailed:
			return workspace.BakeStateFailed, nil
		}
	}
	return workspace.BakeStateBuilding, nil
}

// devcontainerMetadataLabel is the label envbuilder bakes into a built image,
// carrying the devcontainer/feature config it merged — including remoteUser, if the
// devcontainer declared one. Confirmed present by baking a real devcontainer locally.
const devcontainerMetadataLabel = "devcontainer.metadata"

// BakeRemoteUser fetches repoRef's devcontainer.metadata label via the registry's
// ordinary v2 API (manifest -> config digest -> config blob -> Labels) and returns the
// declared remoteUser, if any. repoRef must be reachable from wherever this process
// runs — the EXTERNAL reference from BakedImageRepo, or an external cache_repo — not
// the in-cluster address envbuilder pushed to, since the EasyLab server is not
// necessarily inside the lab's own cluster network. registryAuthSecret names the same
// dockerconfigjson Secret used to push: many registries require auth for reads too
// (e.g. a private internal registry), so this must authenticate the same way the push
// did rather than assuming an anonymous GET works wherever an anonymous push doesn't.
func (b *Backend) BakeRemoteUser(ctx context.Context, repoRef string, insecure bool, registryAuthSecret string) (string, error) {
	host, repo, tag := splitImageRef(repoRef)
	if host == "" || repo == "" {
		return "", fmt.Errorf("invalid image reference %q", repoRef)
	}
	scheme := "https"
	if insecure {
		scheme = "http"
	}
	client := &http.Client{Timeout: 15 * time.Second}

	authHeader, err := b.registryPullAuthHeader(ctx, registryAuthSecret, host)
	if err != nil {
		return "", err
	}

	digest, err := fetchConfigDigest(ctx, client, scheme, host, repo, tag, authHeader)
	if err != nil {
		return "", err
	}
	labels, err := fetchImageConfigLabels(ctx, client, scheme, host, repo, digest, authHeader)
	if err != nil {
		return "", err
	}

	raw := labels[devcontainerMetadataLabel]
	if raw == "" {
		return "", nil
	}
	var entries []struct {
		RemoteUser string `json:"remoteUser"`
	}
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return "", fmt.Errorf("failed to parse %s label: %w", devcontainerMetadataLabel, err)
	}
	// Devcontainer/feature config merges later entries over earlier ones — the last
	// remoteUser set anywhere in the merge is the one that applies.
	for i := len(entries) - 1; i >= 0; i-- {
		if entries[i].RemoteUser != "" {
			return entries[i].RemoteUser, nil
		}
	}
	return "", nil
}

// registryPullAuthHeader reads the same kubernetes.io/dockerconfigjson Secret used to
// push (DevcontainerSpec.RegistryAuthSecret) and returns the "Authorization" header
// value for host, if the secret carries credentials for it. An empty secret name, or
// no entry for host, both return "", nil — an anonymous request, which is correct for
// the in-cluster build cache (no auth configured at all).
func (b *Backend) registryPullAuthHeader(ctx context.Context, secretName, host string) (string, error) {
	secretName = strings.TrimSpace(secretName)
	if secretName == "" {
		return "", nil
	}
	sec, err := b.client.CoreV1().Secrets(b.namespace).Get(ctx, secretName, metav1.GetOptions{})
	if err != nil {
		return "", fmt.Errorf("failed to read registry auth secret %q: %w", secretName, err)
	}
	raw := sec.Data[corev1.DockerConfigJsonKey]
	if len(raw) == 0 {
		raw = sec.Data["config.json"]
	}
	if len(raw) == 0 {
		return "", fmt.Errorf("registry auth secret %q has no %q key", secretName, corev1.DockerConfigJsonKey)
	}
	var cfg dockerConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return "", fmt.Errorf("failed to parse registry auth secret %q: %w", secretName, err)
	}
	auth, ok := cfg.Auths[host]
	if !ok {
		return "", nil
	}
	if auth.Auth != "" {
		return "Basic " + auth.Auth, nil
	}
	if auth.Username != "" || auth.Password != "" {
		return "Basic " + base64.StdEncoding.EncodeToString([]byte(auth.Username+":"+auth.Password)), nil
	}
	return "", nil
}

// splitImageRef splits host[:port]/path/to/repo:tag into its host, repo path and tag,
// defaulting tag to "latest" when absent. Not a general-purpose image reference
// parser — it only needs to handle the shapes this package itself produces.
func splitImageRef(ref string) (host, repo, tag string) {
	slash := strings.Index(ref, "/")
	if slash < 0 {
		return "", "", ""
	}
	host = ref[:slash]
	rest := ref[slash+1:]
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		return host, rest[:i], rest[i+1:]
	}
	return host, rest, "latest"
}

func fetchConfigDigest(ctx context.Context, client *http.Client, scheme, host, repo, tag, authHeader string) (string, error) {
	url := fmt.Sprintf("%s://%s/v2/%s/manifests/%s", scheme, host, repo, tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.docker.distribution.manifest.v2+json, application/vnd.oci.image.manifest.v1+json")
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to fetch image manifest: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("failed to fetch image manifest: unexpected status %s", resp.Status)
	}
	var manifest struct {
		Config struct {
			Digest string `json:"digest"`
		} `json:"config"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&manifest); err != nil {
		return "", fmt.Errorf("failed to decode image manifest: %w", err)
	}
	if manifest.Config.Digest == "" {
		return "", fmt.Errorf("image manifest has no config digest")
	}
	return manifest.Config.Digest, nil
}

func fetchImageConfigLabels(ctx context.Context, client *http.Client, scheme, host, repo, digest, authHeader string) (map[string]string, error) {
	url := fmt.Sprintf("%s://%s/v2/%s/blobs/%s", scheme, host, repo, digest)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if authHeader != "" {
		req.Header.Set("Authorization", authHeader)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch image config: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch image config: unexpected status %s", resp.Status)
	}
	var config struct {
		Config struct {
			Labels map[string]string `json:"Labels"`
		} `json:"config"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&config); err != nil {
		return nil, fmt.Errorf("failed to decode image config: %w", err)
	}
	return config.Config.Labels, nil
}
