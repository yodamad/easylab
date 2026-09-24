package kube

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"easylab/internal/providers/workspace"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	// prepullNamePrefix names a lab's pre-pull DaemonSet: one per lab, so labs
	// sharing a namespace on a BYO cluster each keep their own image set.
	prepullNamePrefix = "easylab-prepull-"

	// prepullBinImage supplies the one binary every pre-pull init container runs.
	// The -musl variant is statically linked, so the copy works inside any image —
	// including distroless sidecars that have no shell of their own to run "true".
	prepullBinImage  = "busybox:stable-musl"
	prepullBinDir    = "/prepull"
	prepullBinVolume = "prepull-bin"

	// prepullPauseImage keeps the pod alive once every image has been pulled, so
	// the DaemonSet does not restart it (and re-run the pulls) in a loop.
	prepullPauseImage = "registry.k8s.io/pause:3.10"

	// annotationPrepullHash records what the DaemonSet was built from. Comparing
	// it, rather than the pod template itself, avoids a spurious update on every
	// reconcile: the API server fills in defaults the desired template lacks.
	annotationPrepullHash = "easylab.io/prepull-hash"
)

// prepullName returns the lab's DaemonSet name, kept within the 63-character
// DNS-1123 label limit its pods' names derive from.
func prepullName(labID string) string {
	name := prepullNamePrefix + sanitizeDNS(labID)
	if len(name) > 63 {
		name = strings.TrimRight(name[:63], "-")
	}
	return name
}

// sortedUnique trims, de-duplicates and sorts values, dropping blanks, so the same
// set always yields the same pod spec (and hash) whatever order it came in.
func sortedUnique(values []string) []string {
	seen := make(map[string]bool, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

// prepullPullSecrets is the requested pull secrets plus, when any image lives on
// this backend's own in-cluster registry, that registry's credentials — the same
// rule workspacePullSecrets applies to a single workspace.
func prepullPullSecrets(images, secrets []string) []string {
	out := append([]string{}, secrets...)
	for _, img := range images {
		if host, _, _ := splitImageRef(img); isOwnRegistryHost(host) {
			out = append(out, registryAuthSecretName)
			break
		}
	}
	return sortedUnique(out)
}

// prepullResources keeps the pre-pull pod from reserving node capacity: none of
// its containers does more than copy one file or sleep.
func prepullResources() corev1.ResourceRequirements {
	return corev1.ResourceRequirements{
		Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1m"),
			corev1.ResourceMemory: resource.MustParse("8Mi"),
		},
		Limits: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("100m"),
			corev1.ResourceMemory: resource.MustParse("32Mi"),
		},
	}
}

// prepullDaemonSet builds the desired DaemonSet for a lab's image set. images and
// pullSecrets must already be normalized (see sortedUnique).
func (b *Backend) prepullDaemonSet(labID string, images, pullSecrets []string, nodeSelector map[string]string) (*appsv1.DaemonSet, error) {
	name := prepullName(labID)
	labels := map[string]string{
		labelManagedBy: managedByValue,
		labelLabID:     sanitizeDNS(labID),
		labelName:      name,
	}

	hashInput, err := json.Marshal(struct {
		Images       []string          `json:"images"`
		PullSecrets  []string          `json:"pull_secrets"`
		NodeSelector map[string]string `json:"node_selector"`
	}{images, pullSecrets, nodeSelector})
	if err != nil {
		return nil, fmt.Errorf("failed to hash pre-pull spec: %w", err)
	}
	sum := sha1.Sum(hashInput)

	binMount := corev1.VolumeMount{Name: prepullBinVolume, MountPath: prepullBinDir}
	initContainers := []corev1.Container{{
		Name:            "prepull-bin",
		Image:           prepullBinImage,
		ImagePullPolicy: corev1.PullIfNotPresent,
		Command:         []string{"cp", "/bin/busybox", prepullBinDir + "/busybox"},
		Resources:       prepullResources(),
		VolumeMounts:    []corev1.VolumeMount{binMount},
	}}
	for i, img := range images {
		// Running the image is what makes the kubelet pull it; IfNotPresent is the
		// same policy the workspace pods use, so the cached copy is the one they
		// will reuse.
		initContainers = append(initContainers, corev1.Container{
			Name:            fmt.Sprintf("pull-%d", i),
			Image:           img,
			ImagePullPolicy: corev1.PullIfNotPresent,
			Command:         []string{prepullBinDir + "/busybox", "true"},
			Resources:       prepullResources(),
			VolumeMounts:    []corev1.VolumeMount{binMount},
		})
	}

	// Every node pulls at once after a template change rather than one at a time:
	// the pod does no work that needs availability preserving.
	maxUnavailable := intstr.FromString("100%")
	automount := false
	return &appsv1.DaemonSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   b.namespace,
			Labels:      labels,
			Annotations: map[string]string{annotationPrepullHash: hex.EncodeToString(sum[:])},
		},
		Spec: appsv1.DaemonSetSpec{
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{labelName: name}},
			UpdateStrategy: appsv1.DaemonSetUpdateStrategy{
				Type:          appsv1.RollingUpdateDaemonSetStrategyType,
				RollingUpdate: &appsv1.RollingUpdateDaemonSet{MaxUnavailable: &maxUnavailable},
			},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					AutomountServiceAccountToken: &automount,
					ImagePullSecrets:             imagePullSecrets(pullSecrets),
					NodeSelector:                 nodeSelector,
					InitContainers:               initContainers,
					Containers: []corev1.Container{{
						Name:            "pause",
						Image:           prepullPauseImage,
						ImagePullPolicy: corev1.PullIfNotPresent,
						Resources:       prepullResources(),
					}},
					Volumes: []corev1.Volume{{
						Name:         prepullBinVolume,
						VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}},
					}},
				},
			},
		},
	}, nil
}

// EnsurePrepull creates or updates the lab's pre-pull DaemonSet so every matching
// node already holds the images its workspaces start from — otherwise the first
// student on each node (and every node the autoscaler adds) waits for the pull.
// It is idempotent and only updates when the image set, pull secrets or node
// selector actually changed. An empty image list removes the DaemonSet.
func (b *Backend) EnsurePrepull(ctx context.Context, labID string, req workspace.PrepullRequest) error {
	images := sortedUnique(req.Images)
	if len(images) == 0 {
		return b.RemovePrepull(ctx, labID)
	}
	nodeSelector := req.NodeSelector
	if len(nodeSelector) == 0 {
		nodeSelector = nil
	}
	desired, err := b.prepullDaemonSet(labID, images, prepullPullSecrets(images, req.PullSecrets), nodeSelector)
	if err != nil {
		return err
	}

	dsClient := b.client.AppsV1().DaemonSets(b.namespace)
	existing, err := dsClient.Get(ctx, desired.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		if _, err := dsClient.Create(ctx, desired, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
			return fmt.Errorf("failed to create pre-pull daemonset %s: %w", desired.Name, err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("failed to look up pre-pull daemonset %s: %w", desired.Name, err)
	}
	if existing.Annotations[annotationPrepullHash] == desired.Annotations[annotationPrepullHash] {
		return nil
	}

	if existing.Annotations == nil {
		existing.Annotations = map[string]string{}
	}
	existing.Annotations[annotationPrepullHash] = desired.Annotations[annotationPrepullHash]
	existing.Spec.UpdateStrategy = desired.Spec.UpdateStrategy
	existing.Spec.Template = desired.Spec.Template
	if _, err := dsClient.Update(ctx, existing, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update pre-pull daemonset %s: %w", desired.Name, err)
	}
	return nil
}

// RemovePrepull deletes the lab's pre-pull DaemonSet. An absent one is not an error.
func (b *Backend) RemovePrepull(ctx context.Context, labID string) error {
	name := prepullName(labID)
	if err := b.client.AppsV1().DaemonSets(b.namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete pre-pull daemonset %s: %w", name, err)
	}
	return nil
}
