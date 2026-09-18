package kube

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/bcrypt"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestEnsureBuildCache(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()

	repo, err := b.EnsureBuildCache(ctx)
	require.NoError(t, err)
	assert.Equal(t, "easylab-registry-cache.workshops.svc.cluster.local:5000/cache", repo)

	dep, err := cs.AppsV1().Deployments("workshops").Get(ctx, registryCacheName, metav1.GetOptions{})
	require.NoError(t, err)
	require.Len(t, dep.Spec.Template.Spec.Containers, 1)
	assert.Equal(t, registryCacheImage, dep.Spec.Template.Spec.Containers[0].Image)

	_, err = cs.CoreV1().Services("workshops").Get(ctx, registryCacheName, metav1.GetOptions{})
	require.NoError(t, err)

	_, err = cs.CoreV1().PersistentVolumeClaims("workshops").Get(ctx, registryCacheName, metav1.GetOptions{})
	require.NoError(t, err)

	// The registry must never carry the label workspace listing selects on, or
	// it would show up as a workspace in a lab's list.
	labels := dep.Labels
	if _, ok := labels[labelLabID]; ok {
		t.Fatalf("registry cache deployment must not carry %s, it would be listed as a workspace", labelLabID)
	}
}

func TestEnsureBuildCache_Idempotent(t *testing.T) {
	b, _ := newTestBackend()
	ctx := context.Background()

	repo1, err := b.EnsureBuildCache(ctx)
	require.NoError(t, err)
	repo2, err := b.EnsureBuildCache(ctx)
	require.NoError(t, err)

	assert.Equal(t, repo1, repo2)
}

// TestBakedImageRepo_InternalIsBare guards a real bug found in production: an
// explicit :latest suffix on the internal (ENVBUILDER_CACHE_REPO push) repo broke
// envbuilder's own tag resolution for the whole-image push ("repository can only
// contain the characters ...", from a real bake's logs) — the push failed
// silently while the Job still exited 0, leaving the registry with nothing ever
// pushed to it. envbuilder appends its own tag; the internal repo must be bare.
func TestBakedImageRepo_InternalIsBare(t *testing.T) {
	b, _ := newTestBackend()

	internal, external := b.BakedImageRepo("job-1", "go-workshop", "lab.example.com")

	assert.NotContains(t, internal, ":latest", "ENVBUILDER_CACHE_REPO must be a bare repository, no tag")
	assert.Equal(t, "easylab-registry-cache.workshops.svc.cluster.local:5000/baked/job-1/go-workshop", internal)
	// The external (pull) reference is unaffected — kubelet pulls/manifest
	// lookups expect an explicit tag, which is where this bug did not occur.
	assert.Equal(t, "easylab-registry-cache.lab.example.com/baked/job-1/go-workshop:latest", external)
}

// EnsureRegistryIngress is documented as idempotent but was create-only: a registry
// first exposed without a certificate source kept a TLS config nothing issues, the
// kubelet then refused to pull over it, and every bake ended in "image was built, but
// never became pullable".
func TestEnsureRegistryIngress_ReconcilesExisting(t *testing.T) {
	const domain = "lab.example.com"
	host := registryCacheName + "." + domain

	tests := []struct {
		name                       string
		firstSecret, firstIssuer   string
		secondSecret, secondIssuer string
		wantSecret, wantIssuer     string
	}{
		{
			name:         "gains the wildcard secret once the lab has one",
			wantSecret:   "easylab-wildcard-tls",
			secondSecret: "easylab-wildcard-tls",
		},
		{
			name:         "gains a per-host certificate request",
			secondIssuer: "letsencrypt-prod",
			wantSecret:   registryCacheName + "-tls",
			wantIssuer:   "letsencrypt-prod",
		},
		{
			name:         "follows a renamed cluster issuer",
			firstIssuer:  "letsencrypt-prod",
			secondIssuer: "easylab-staging",
			wantSecret:   registryCacheName + "-tls",
			wantIssuer:   "easylab-staging",
		},
		{
			name:         "switches from per-host to wildcard",
			firstIssuer:  "letsencrypt-prod",
			secondSecret: "easylab-wildcard-tls",
			wantSecret:   "easylab-wildcard-tls",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, cs := newTestBackend()
			ctx := context.Background()

			_, err := b.EnsureRegistryIngress(ctx, domain, tt.firstSecret, tt.firstIssuer)
			require.NoError(t, err)

			gotHost, err := b.EnsureRegistryIngress(ctx, domain, tt.secondSecret, tt.secondIssuer)
			require.NoError(t, err)
			assert.Equal(t, host, gotHost)

			ing, err := cs.NetworkingV1().Ingresses("workshops").Get(ctx, registryCacheName, metav1.GetOptions{})
			require.NoError(t, err)
			require.Len(t, ing.Spec.TLS, 1)
			assert.Equal(t, tt.wantSecret, ing.Spec.TLS[0].SecretName)
			assert.Equal(t, tt.wantIssuer, ing.Annotations[clusterIssuerAnnotation])
			assert.Equal(t, host, ing.Spec.Rules[0].Host)
			require.NotNil(t, ing.Spec.IngressClassName)
			assert.Equal(t, workspaceIngressClass, *ing.Spec.IngressClassName)
		})
	}
}

// registryCreds reads back the generated credentials and htpasswd.
func registryCreds(t *testing.T, cs *fake.Clientset) (dockerConfig, string) {
	t.Helper()
	ctx := context.Background()
	sec, err := cs.CoreV1().Secrets("workshops").Get(ctx, registryAuthSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, corev1.SecretTypeDockerConfigJson, sec.Type, "the kubelet only takes a dockerconfigjson Secret as an imagePullSecret")
	var cfg dockerConfig
	require.NoError(t, json.Unmarshal(sec.Data[corev1.DockerConfigJsonKey], &cfg))

	ht, err := cs.CoreV1().Secrets("workshops").Get(ctx, registryHtpasswdSecretName, metav1.GetOptions{})
	require.NoError(t, err)
	return cfg, string(ht.Data[registryHtpasswdKey])
}

func registryDeployment(t *testing.T, cs *fake.Clientset) *appsv1.Deployment {
	t.Helper()
	dep, err := cs.AppsV1().Deployments("workshops").Get(context.Background(), registryCacheName, metav1.GetOptions{})
	require.NoError(t, err)
	return dep
}

func TestEnsureBuildCache_RequiresAuthentication(t *testing.T) {
	b, cs := newTestBackend()
	_, err := b.EnsureBuildCache(context.Background())
	require.NoError(t, err)

	cfg, htpasswd := registryCreds(t, cs)
	cred, ok := cfg.Auths[b.registryInternalHost()]
	require.True(t, ok, "the internal host is what envbuilder pushes and pulls through")
	assert.Equal(t, registryAuthUsername, cred.Username)
	assert.Len(t, cred.Password, 64, "256 random bits, hex-encoded")
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte(cred.Username+":"+cred.Password)), cred.Auth)

	user, hash, found := strings.Cut(strings.TrimSpace(htpasswd), ":")
	require.True(t, found)
	assert.Equal(t, registryAuthUsername, user)
	assert.NoError(t, bcrypt.CompareHashAndPassword([]byte(hash), []byte(cred.Password)), "the registry must accept the generated password")

	dep := registryDeployment(t, cs)
	assert.Equal(t, appsv1.RecreateDeploymentStrategyType, dep.Spec.Strategy.Type, "RollingUpdate deadlocks on the ReadWriteOnce data volume")
	assert.Equal(t, htpasswdSum(htpasswd), dep.Spec.Template.Annotations[annotationHtpasswdSum])

	c := dep.Spec.Template.Spec.Containers[0]
	env := envOf(c)
	assert.Equal(t, "htpasswd", env["REGISTRY_AUTH"])
	assert.Equal(t, "/auth/htpasswd", env["REGISTRY_AUTH_HTPASSWD_PATH"])
	assert.NotEmpty(t, env["REGISTRY_AUTH_HTPASSWD_REALM"])
	assert.Contains(t, c.VolumeMounts, corev1.VolumeMount{Name: registryAuthVolumeName, MountPath: registryAuthMountPath, ReadOnly: true})
	assert.Contains(t, dep.Spec.Template.Spec.Volumes, registryAuthVolume())
}

func TestEnsureBuildCache_ReusesCredentials(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()

	_, err := b.EnsureBuildCache(ctx)
	require.NoError(t, err)
	first, firstHtpasswd := registryCreds(t, cs)
	cs.ClearActions()

	_, err = b.EnsureBuildCache(ctx)
	require.NoError(t, err)
	second, secondHtpasswd := registryCreds(t, cs)

	assert.Equal(t, first, second, "a second call must not rotate the password out from under running clients")
	assert.Equal(t, firstHtpasswd, secondHtpasswd)
	for _, a := range cs.Actions() {
		assert.NotEqual(t, "update", a.GetVerb(), "an already-authenticated registry must not be rewritten (and restarted)")
	}
}

// The registry reads its htpasswd once at startup, so every way the htpasswd it
// should run with can change must roll the Deployment — including a registry created
// before authentication existed at all.
func TestEnsureBuildCache_ReconcilesRegistryDeployment(t *testing.T) {
	tests := []struct {
		name  string
		setup func(t *testing.T, b *Backend, cs *fake.Clientset)
	}{
		{
			name: "registry created before authentication",
			setup: func(t *testing.T, b *Backend, cs *fake.Clientset) {
				// The pre-authentication shape: no auth env, volume, annotation, and
				// the default RollingUpdate strategy.
				replicas := int32(1)
				_, err := cs.AppsV1().Deployments("workshops").Create(context.Background(), &appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{Name: registryCacheName, Namespace: "workshops", Labels: registryCacheLabels()},
					Spec: appsv1.DeploymentSpec{
						Replicas: &replicas,
						Strategy: appsv1.DeploymentStrategy{Type: appsv1.RollingUpdateDeploymentStrategyType, RollingUpdate: &appsv1.RollingUpdateDeployment{}},
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{Labels: registryCacheLabels()},
							Spec: corev1.PodSpec{
								Containers: []corev1.Container{{
									Name:         "registry",
									Image:        registryCacheImage,
									VolumeMounts: []corev1.VolumeMount{{Name: "data", MountPath: "/var/lib/registry"}},
								}},
								Volumes: []corev1.Volume{{Name: "data"}},
							},
						},
					},
				}, metav1.CreateOptions{})
				require.NoError(t, err)
			},
		},
		{
			name: "htpasswd secret deleted",
			setup: func(t *testing.T, b *Backend, cs *fake.Clientset) {
				_, err := b.EnsureBuildCache(context.Background())
				require.NoError(t, err)
				require.NoError(t, cs.CoreV1().Secrets("workshops").Delete(context.Background(), registryHtpasswdSecretName, metav1.DeleteOptions{}))
			},
		},
		{
			name: "credentials secret deleted",
			setup: func(t *testing.T, b *Backend, cs *fake.Clientset) {
				_, err := b.EnsureBuildCache(context.Background())
				require.NoError(t, err)
				require.NoError(t, cs.CoreV1().Secrets("workshops").Delete(context.Background(), registryAuthSecretName, metav1.DeleteOptions{}))
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			b, cs := newTestBackend()
			tt.setup(t, b, cs)

			_, err := b.EnsureBuildCache(context.Background())
			require.NoError(t, err)

			cfg, htpasswd := registryCreds(t, cs)
			assert.True(t, htpasswdMatches(htpasswd, registryAuthUsername, cfg.Auths[b.registryInternalHost()].Password), "htpasswd must match the current password")

			dep := registryDeployment(t, cs)
			assert.Equal(t, htpasswdSum(htpasswd), dep.Spec.Template.Annotations[annotationHtpasswdSum], "the rollout is what makes the registry load the new htpasswd")
			assert.Equal(t, appsv1.RecreateDeploymentStrategyType, dep.Spec.Strategy.Type)
			assert.Nil(t, dep.Spec.Strategy.RollingUpdate, "Recreate rejects a leftover rollingUpdate block")

			c := dep.Spec.Template.Spec.Containers[0]
			assert.Equal(t, "htpasswd", envOf(c)["REGISTRY_AUTH"])
			assert.Contains(t, c.VolumeMounts, corev1.VolumeMount{Name: "data", MountPath: "/var/lib/registry"}, "the data volume must survive the reconcile")
			assert.Contains(t, c.VolumeMounts, registryAuthVolumeMount())
			authVolumes := 0
			for _, v := range dep.Spec.Template.Spec.Volumes {
				if v.Name == registryAuthVolumeName {
					authVolumes++
				}
			}
			assert.Equal(t, 1, authVolumes, "reconciling must not duplicate the auth volume")
		})
	}
}

// The kubelet and the server's pull verification match credentials on the exact
// registry host, so exposing the registry must add its Ingress host — with the same
// credentials, and without dropping the internal one envbuilder uses.
func TestEnsureRegistryIngress_AddsExternalHostToCredentials(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()

	_, err := b.EnsureBuildCache(ctx)
	require.NoError(t, err)
	before, beforeHtpasswd := registryCreds(t, cs)

	host, err := b.EnsureRegistryIngress(ctx, "lab.example.com", "easylab-wildcard-tls", "")
	require.NoError(t, err)

	after, afterHtpasswd := registryCreds(t, cs)
	internal := before.Auths[b.registryInternalHost()]
	assert.Equal(t, internal, after.Auths[b.registryInternalHost()])
	assert.Equal(t, internal, after.Auths[host])
	assert.Equal(t, beforeHtpasswd, afterHtpasswd, "adding a host must not change the password")

	// And a later EnsureBuildCache (which knows no domain) must not drop it again.
	_, err = b.EnsureBuildCache(ctx)
	require.NoError(t, err)
	again, _ := registryCreds(t, cs)
	assert.Contains(t, again.Auths, host)
}

func TestIsOwnRegistryHost(t *testing.T) {
	tests := []struct {
		name     string
		host     string
		expected bool
	}{
		{name: "internal service address", host: "easylab-registry-cache.workshops.svc.cluster.local:5000", expected: true},
		{name: "ingress host", host: "easylab-registry-cache.lab.example.com", expected: true},
		{name: "external registry", host: "registry.example.com", expected: false},
		{name: "look-alike without the dot", host: "easylab-registry-cache-evil.example.com", expected: false},
		{name: "empty", host: "", expected: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.expected, isOwnRegistryHost(tt.host))
		})
	}
}

func TestEnsureRegistryIngress_NoWriteWhenUnchanged(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()

	_, err := b.EnsureRegistryIngress(ctx, "lab.example.com", "easylab-wildcard-tls", "")
	require.NoError(t, err)
	cs.ClearActions()

	_, err = b.EnsureRegistryIngress(ctx, "lab.example.com", "easylab-wildcard-tls", "")
	require.NoError(t, err)

	for _, a := range cs.Actions() {
		assert.NotEqual(t, "update", a.GetVerb(), "an already-correct registry ingress must not be rewritten")
	}
}
