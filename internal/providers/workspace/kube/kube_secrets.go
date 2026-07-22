package kube

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"easylab/internal/providers/workspace"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// This file implements workspace.SecretManager: the credential Secrets that
// workspace templates reference by name (image_pull_secrets, git_auth_secret,
// devcontainer.registry_auth_secret).
//
// The Secrets live in the cluster and nowhere else. EasyLab writes them and then
// forgets the token — it is never held in memory between requests, never written
// to the lab config, and so never reaches the job file or the templates export.

// SecretManager is optional and reached by type assertion, so nothing else would
// notice if this backend stopped implementing it.
var _ workspace.SecretManager = (*Backend)(nil)

// dockerConfig mirrors the parts of a ~/.docker/config.json that a
// kubernetes.io/dockerconfigjson Secret carries.
type dockerConfig struct {
	Auths map[string]dockerAuth `json:"auths"`
}

// dockerAuth is one registry's entry. Auth is base64(username:password) — the
// field the registry actually authenticates with, and the reason a
// dockerconfigjson Secret must never be echoed back to a client.
type dockerAuth struct {
	Username string `json:"username,omitempty"`
	Password string `json:"password,omitempty"`
	Auth     string `json:"auth,omitempty"`
}

// EnsureRegistrySecret writes a dockerconfigjson Secret granting access to a
// single registry server.
func (b *Backend) EnsureRegistrySecret(ctx context.Context, name, server, username, token string) error {
	name, server = strings.TrimSpace(name), strings.TrimSpace(server)
	username, token = strings.TrimSpace(username), strings.TrimSpace(token)
	if name == "" || server == "" || username == "" || token == "" {
		return fmt.Errorf("registry secret needs a name, server, username and token")
	}

	cfg := dockerConfig{Auths: map[string]dockerAuth{
		server: {
			Username: username,
			Password: token,
			Auth:     base64.StdEncoding.EncodeToString([]byte(username + ":" + token)),
		},
	}}
	encoded, err := json.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to encode docker config for secret %q: %w", name, err)
	}

	return b.applySecret(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: b.namespace,
			Labels:    map[string]string{labelManagedBy: managedByValue},
		},
		Type: corev1.SecretTypeDockerConfigJson,
		Data: map[string][]byte{corev1.DockerConfigJsonKey: encoded},
	})
}

// EnsureGitAuthSecret writes a basic-auth Secret for cloning a private repo. The
// keys are the ones the clone step reads (see basicAuthEnv); they are fixed by
// the kubernetes.io/basic-auth type rather than chosen here.
func (b *Backend) EnsureGitAuthSecret(ctx context.Context, name, username, token string) error {
	name = strings.TrimSpace(name)
	username, token = strings.TrimSpace(username), strings.TrimSpace(token)
	if name == "" || username == "" || token == "" {
		return fmt.Errorf("git auth secret needs a name, username and token")
	}

	return b.applySecret(ctx, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: b.namespace,
			Labels:    map[string]string{labelManagedBy: managedByValue},
		},
		Type: corev1.SecretTypeBasicAuth,
		Data: map[string][]byte{
			corev1.BasicAuthUsernameKey: []byte(username),
			corev1.BasicAuthPasswordKey: []byte(token),
		},
	})
}

// applySecret creates the Secret, falling back to an update when it is already
// there. Unlike the workspace resources — where an existing object means the
// workspace is already up and must be left alone — an existing credential Secret
// is the rotation case: the admin is replacing an expired token, and ignoring the
// write would leave the old one in place while reporting success.
func (b *Backend) applySecret(ctx context.Context, sec *corev1.Secret) error {
	_, err := b.client.CoreV1().Secrets(b.namespace).Create(ctx, sec, metav1.CreateOptions{})
	if err == nil {
		return nil
	}
	if !apierrors.IsAlreadyExists(err) {
		return fmt.Errorf("failed to create secret %q: %w", sec.Name, err)
	}
	if _, err := b.client.CoreV1().Secrets(b.namespace).Update(ctx, sec, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("failed to update secret %q: %w", sec.Name, err)
	}
	return nil
}

// ListAuthSecrets reports the credential Secrets in the namespace a template can
// reference.
//
// It selects on the Secret's type rather than on an easylab label, so a Secret an
// admin created out of band with `kubectl create secret docker-registry` is
// listed too — the admin needs to know which names are valid to reference,
// whoever made them. Filtering is client-side because the fake clientset used in
// tests does not honour field selectors, so a fieldSelector would pass the tests
// and drop nothing in production.
func (b *Backend) ListAuthSecrets(ctx context.Context) ([]workspace.AuthSecret, error) {
	list, err := b.client.CoreV1().Secrets(b.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to list secrets in namespace %q: %w", b.namespace, err)
	}

	out := make([]workspace.AuthSecret, 0, len(list.Items))
	for _, sec := range list.Items {
		switch sec.Type {
		case corev1.SecretTypeDockerConfigJson:
			out = append(out, registryAuthSecret(sec))
		case corev1.SecretTypeBasicAuth:
			out = append(out, workspace.AuthSecret{
				Name:     sec.Name,
				Type:     workspace.AuthSecretGit,
				Username: string(sec.Data[corev1.BasicAuthUsernameKey]),
			})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// registryAuthSecret summarises a dockerconfigjson Secret without revealing what
// it holds: the servers it covers and the username, never the password and never
// the "auth" blob (which is only base64 and would hand over the token verbatim).
// A Secret that cannot be parsed is still listed by name — it exists, and hiding
// it would make a template that references it look like a typo.
func registryAuthSecret(sec corev1.Secret) workspace.AuthSecret {
	out := workspace.AuthSecret{Name: sec.Name, Type: workspace.AuthSecretRegistry}

	raw := sec.Data[corev1.DockerConfigJsonKey]
	if len(raw) == 0 {
		raw = sec.Data["config.json"]
	}
	var cfg dockerConfig
	if len(raw) == 0 || json.Unmarshal(raw, &cfg) != nil {
		return out
	}

	for server, auth := range cfg.Auths {
		out.Servers = append(out.Servers, server)
		if out.Username == "" {
			out.Username = registryUsername(auth)
		}
	}
	sort.Strings(out.Servers)
	return out
}

// registryUsername recovers the username for display. `kubectl create secret
// docker-registry` writes both username and auth, but a hand-written config.json
// often carries only auth, so fall back to decoding it — taking the part before
// the first colon and discarding the rest, which is the password.
func registryUsername(auth dockerAuth) string {
	if auth.Username != "" {
		return auth.Username
	}
	decoded, err := base64.StdEncoding.DecodeString(auth.Auth)
	if err != nil {
		return ""
	}
	user, _, found := strings.Cut(string(decoded), ":")
	if !found {
		return ""
	}
	return user
}

// DeleteAuthSecret removes a credential Secret. Deleting one that is not there is
// not an error: the caller wanted it gone, and it is.
func (b *Backend) DeleteAuthSecret(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("secret name is required")
	}
	if err := b.client.CoreV1().Secrets(b.namespace).Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("failed to delete secret %q: %w", name, err)
	}
	return nil
}
