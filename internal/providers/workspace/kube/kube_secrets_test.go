package kube

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"testing"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestEnsureRegistrySecret(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()

	require.NoError(t, b.EnsureRegistrySecret(ctx, "regcred", "registry.example.com", "bob", "tok123"))

	sec, err := cs.CoreV1().Secrets("workshops").Get(ctx, "regcred", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, corev1.SecretTypeDockerConfigJson, sec.Type)

	var cfg dockerConfig
	require.NoError(t, json.Unmarshal(sec.Data[corev1.DockerConfigJsonKey], &cfg))
	entry, ok := cfg.Auths["registry.example.com"]
	require.True(t, ok, "the registry server is missing from the docker config")
	assert.Equal(t, "bob", entry.Username)
	assert.Equal(t, "tok123", entry.Password)
	// The auth blob is what the registry authenticates with; kubelet requires it.
	assert.Equal(t, base64.StdEncoding.EncodeToString([]byte("bob:tok123")), entry.Auth)
}

func TestEnsureGitAuthSecret(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()

	require.NoError(t, b.EnsureGitAuthSecret(ctx, "gitcred", "oauth2", "glpat-x"))

	sec, err := cs.CoreV1().Secrets("workshops").Get(ctx, "gitcred", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, corev1.SecretTypeBasicAuth, sec.Type)
	assert.Equal(t, "oauth2", string(sec.Data[corev1.BasicAuthUsernameKey]))
	assert.Equal(t, "glpat-x", string(sec.Data[corev1.BasicAuthPasswordKey]))
}

func TestEnsureSecret_RequiredFields(t *testing.T) {
	b, _ := newTestBackend()
	ctx := context.Background()

	tests := []struct {
		name string
		call func() error
	}{
		{"registry without name", func() error { return b.EnsureRegistrySecret(ctx, "", "s", "u", "t") }},
		{"registry without server", func() error { return b.EnsureRegistrySecret(ctx, "n", "", "u", "t") }},
		{"registry without username", func() error { return b.EnsureRegistrySecret(ctx, "n", "s", "", "t") }},
		{"registry without token", func() error { return b.EnsureRegistrySecret(ctx, "n", "s", "u", "") }},
		{"git without name", func() error { return b.EnsureGitAuthSecret(ctx, "", "u", "t") }},
		{"git without username", func() error { return b.EnsureGitAuthSecret(ctx, "n", "", "t") }},
		{"git without token", func() error { return b.EnsureGitAuthSecret(ctx, "n", "u", "") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Error(t, tt.call())
		})
	}
}

// Rotation is the reason applySecret updates rather than ignoring an existing
// Secret: an admin replacing an expired token must not be told it worked while
// the old one stays in place.
func TestEnsureSecret_RotatesInsteadOfIgnoring(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()

	require.NoError(t, b.EnsureGitAuthSecret(ctx, "gitcred", "oauth2", "old-token"))
	require.NoError(t, b.EnsureGitAuthSecret(ctx, "gitcred", "oauth2", "new-token"))

	sec, err := cs.CoreV1().Secrets("workshops").Get(ctx, "gitcred", metav1.GetOptions{})
	require.NoError(t, err)
	assert.Equal(t, "new-token", string(sec.Data[corev1.BasicAuthPasswordKey]))

	require.NoError(t, b.EnsureRegistrySecret(ctx, "regcred", "reg.example.com", "bob", "old"))
	require.NoError(t, b.EnsureRegistrySecret(ctx, "regcred", "reg.example.com", "bob", "new"))

	reg, err := cs.CoreV1().Secrets("workshops").Get(ctx, "regcred", metav1.GetOptions{})
	require.NoError(t, err)
	var cfg dockerConfig
	require.NoError(t, json.Unmarshal(reg.Data[corev1.DockerConfigJsonKey], &cfg))
	assert.Equal(t, "new", cfg.Auths["reg.example.com"].Password)
}

func TestListAuthSecrets(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()

	require.NoError(t, b.EnsureRegistrySecret(ctx, "regcred", "registry.example.com", "bob", "tok123"))
	require.NoError(t, b.EnsureGitAuthSecret(ctx, "gitcred", "oauth2", "glpat-x"))

	// Unrelated secrets in the namespace must not be reported as credentials.
	for _, s := range []*corev1.Secret{
		{ObjectMeta: metav1.ObjectMeta{Name: "tls-cert", Namespace: "workshops"}, Type: corev1.SecretTypeTLS},
		{ObjectMeta: metav1.ObjectMeta{Name: "random", Namespace: "workshops"}, Type: corev1.SecretTypeOpaque},
	} {
		_, err := cs.CoreV1().Secrets("workshops").Create(ctx, s, metav1.CreateOptions{})
		require.NoError(t, err)
	}

	got, err := b.ListAuthSecrets(ctx)
	require.NoError(t, err)
	require.Len(t, got, 2, "expected only the credential secrets")

	// Sorted by name: gitcred, regcred.
	assert.Equal(t, workspace.AuthSecret{
		Name: "gitcred", Type: workspace.AuthSecretGit, Username: "oauth2",
	}, got[0])
	assert.Equal(t, workspace.AuthSecret{
		Name: "regcred", Type: workspace.AuthSecretRegistry,
		Servers: []string{"registry.example.com"}, Username: "bob",
	}, got[1])
}

// The listing is rendered into an admin page, so it must not carry the token —
// not as a password, and not as the base64 "auth" blob either.
func TestListAuthSecrets_NeverSurfacesSecretMaterial(t *testing.T) {
	b, _ := newTestBackend()
	ctx := context.Background()

	const token = "glpat-do-not-leak-me"
	require.NoError(t, b.EnsureRegistrySecret(ctx, "regcred", "registry.example.com", "bob", token))
	require.NoError(t, b.EnsureGitAuthSecret(ctx, "gitcred", "oauth2", token))

	got, err := b.ListAuthSecrets(ctx)
	require.NoError(t, err)

	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), token, "the token is exposed by the listing")
	assert.NotContains(t, string(encoded), base64.StdEncoding.EncodeToString([]byte("bob:"+token)),
		"the base64 auth blob is exposed by the listing, which is the token in all but name")
}

// A Secret made out of band with `kubectl create secret docker-registry`, or a
// hand-written config.json, often carries only the auth blob. The username is
// still worth showing; the password never is.
func TestListAuthSecrets_UsernameRecoveredFromAuthBlob(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()

	const token = "tok-secret"
	raw, err := json.Marshal(dockerConfig{Auths: map[string]dockerAuth{
		"registry.example.com": {Auth: base64.StdEncoding.EncodeToString([]byte("alice:" + token))},
	}})
	require.NoError(t, err)

	_, err = cs.CoreV1().Secrets("workshops").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "handmade", Namespace: "workshops"},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: raw},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	got, err := b.ListAuthSecrets(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "alice", got[0].Username)

	encoded, err := json.Marshal(got)
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), token)
}

// An unparseable Secret still exists, and a template referencing it still works.
// Hiding it would make that template look like a typo.
func TestListAuthSecrets_UnparseableSecretStillListed(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()

	_, err := cs.CoreV1().Secrets("workshops").Create(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "broken", Namespace: "workshops"},
		Type:       corev1.SecretTypeDockerConfigJson,
		Data:       map[string][]byte{corev1.DockerConfigJsonKey: []byte("not json")},
	}, metav1.CreateOptions{})
	require.NoError(t, err)

	got, err := b.ListAuthSecrets(ctx)
	require.NoError(t, err)
	require.Len(t, got, 1)
	assert.Equal(t, "broken", got[0].Name)
	assert.Empty(t, got[0].Username)
}

func TestDeleteAuthSecret(t *testing.T) {
	b, cs := newTestBackend()
	ctx := context.Background()

	require.NoError(t, b.EnsureGitAuthSecret(ctx, "gitcred", "oauth2", "glpat-x"))
	require.NoError(t, b.DeleteAuthSecret(ctx, "gitcred"))

	_, err := cs.CoreV1().Secrets("workshops").Get(ctx, "gitcred", metav1.GetOptions{})
	assert.Error(t, err, "secret should be gone")

	// Idempotent: the caller wanted it gone, and it is.
	assert.NoError(t, b.DeleteAuthSecret(ctx, "gitcred"))
	assert.Error(t, b.DeleteAuthSecret(ctx, ""))
}

// The two halves of the feature must agree on the key names: EnsureGitAuthSecret
// writes them, and the clone step reads them by reference. Nothing else would
// catch a divergence — each side's own tests would still pass.
func TestEnsureWorkspace_AcceptsSecretFromEnsureGitAuthSecret(t *testing.T) {
	b, _ := newTestBackend()
	ctx := context.Background()

	require.NoError(t, b.EnsureGitAuthSecret(ctx, "gitcred", "oauth2", "glpat-x"))

	spec := plainGitSpec()
	spec.GitAuthSecret = "gitcred"
	_, err := b.EnsureWorkspace(ctx, spec)
	assert.NoError(t, err, "a secret written by EnsureGitAuthSecret must satisfy verifyGitAuthSecret")
}
