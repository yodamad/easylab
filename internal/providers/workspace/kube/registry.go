package kube

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	// registryCacheName is the fixed name of the in-cluster registry used as the
	// devcontainer build cache when a template opts in instead of naming an
	// external one. One instance is shared by every devcontainer template in
	// the lab that opts in.
	registryCacheName = "easylab-registry-cache"
	// registryCacheImage is the standard, minimal open-source OCI registry.
	registryCacheImage = "registry:2"
	registryCachePort  = 5000
	// registryCacheDiskSize is generous enough to hold several devcontainer
	// images' worth of cached layers without needing to be configurable.
	registryCacheDiskSize = "10Gi"
)

// registryCacheLabels intentionally does not reuse Backend.labels: that helper
// stamps easylab.io/lab-id, which is also what ListWorkspaces/GetWorkspace
// select workspaces by — this resource must never carry that label, or it
// would show up in a student's or admin's workspace list.
func registryCacheLabels() map[string]string {
	return map[string]string{
		labelManagedBy: managedByValue,
		labelName:      registryCacheName,
	}
}

// EnsureBuildCache provisions (idempotently) an in-cluster registry used as the
// devcontainer build cache and returns its repo address. The registry has no
// TLS or authentication: it is reachable only from inside the cluster, over
// plain HTTP — envbuilder is a userspace HTTP client, not the kubelet, so
// DevcontainerSpec.Insecure (rather than node-level trust configuration) is
// what makes this work despite the missing TLS.
func (b *Backend) EnsureBuildCache(ctx context.Context) (string, error) {
	labels := registryCacheLabels()

	if err := b.createRegistryCachePVC(ctx, labels); err != nil {
		return "", err
	}
	if err := b.createRegistryCacheDeployment(ctx, labels); err != nil {
		return "", err
	}
	if err := b.createRegistryCacheService(ctx, labels); err != nil {
		return "", err
	}

	return fmt.Sprintf("%s.%s.svc.cluster.local:%d/cache", registryCacheName, b.namespace, registryCachePort), nil
}

func (b *Backend) createRegistryCachePVC(ctx context.Context, labels map[string]string) error {
	pvc := &corev1.PersistentVolumeClaim{
		ObjectMeta: metav1.ObjectMeta{Name: registryCacheName, Namespace: b.namespace, Labels: labels},
		Spec: corev1.PersistentVolumeClaimSpec{
			AccessModes: []corev1.PersistentVolumeAccessMode{corev1.ReadWriteOnce},
			Resources: corev1.VolumeResourceRequirements{
				Requests: corev1.ResourceList{corev1.ResourceStorage: resource.MustParse(registryCacheDiskSize)},
			},
		},
	}
	if _, err := b.client.CoreV1().PersistentVolumeClaims(b.namespace).Create(ctx, pvc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create registry cache PVC: %w", err)
	}
	return nil
}

func (b *Backend) createRegistryCacheDeployment(ctx context.Context, labels map[string]string) error {
	replicas := int32(1)
	dep := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: registryCacheName, Namespace: b.namespace, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Replicas: &replicas,
			Selector: &metav1.LabelSelector{MatchLabels: map[string]string{labelName: registryCacheName}},
			Template: corev1.PodTemplateSpec{
				ObjectMeta: metav1.ObjectMeta{Labels: labels},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "registry",
						Image: registryCacheImage,
						// The registry image itself changes rarely enough that reusing
						// whatever a node already has is the right default here too.
						ImagePullPolicy: corev1.PullIfNotPresent,
						Ports:           []corev1.ContainerPort{{ContainerPort: registryCachePort, Name: "http"}},
						VolumeMounts:    []corev1.VolumeMount{{Name: "data", MountPath: "/var/lib/registry"}},
						ReadinessProbe: &corev1.Probe{
							ProbeHandler:        corev1.ProbeHandler{TCPSocket: &corev1.TCPSocketAction{Port: intstr.FromInt(registryCachePort)}},
							InitialDelaySeconds: 5,
							PeriodSeconds:       10,
						},
					}},
					Volumes: []corev1.Volume{{
						Name: "data",
						VolumeSource: corev1.VolumeSource{
							PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: registryCacheName},
						},
					}},
				},
			},
		},
	}
	if _, err := b.client.AppsV1().Deployments(b.namespace).Create(ctx, dep, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create registry cache deployment: %w", err)
	}
	return nil
}

func (b *Backend) createRegistryCacheService(ctx context.Context, labels map[string]string) error {
	svc := &corev1.Service{
		ObjectMeta: metav1.ObjectMeta{Name: registryCacheName, Namespace: b.namespace, Labels: labels},
		Spec: corev1.ServiceSpec{
			Selector: map[string]string{labelName: registryCacheName},
			Ports: []corev1.ServicePort{{
				Port:       registryCachePort,
				TargetPort: intstr.FromInt(registryCachePort),
				Protocol:   corev1.ProtocolTCP,
			}},
		},
	}
	if _, err := b.client.CoreV1().Services(b.namespace).Create(ctx, svc, metav1.CreateOptions{}); err != nil && !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create registry cache service: %w", err)
	}
	return nil
}
