package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestPendingSecretStore(t *testing.T) {
	t.Parallel()

	s := newPendingSecretStore()
	secrets := []pendingSecret{{Kind: workspace.AuthSecretGit, Name: "gitcred", Token: "t"}}

	assert.False(t, s.Has("job-1"))
	s.Put("job-1", secrets)
	assert.True(t, s.Has("job-1"))

	// Put with nothing is a no-op, not a way to clear.
	s.Put("job-1", nil)
	assert.True(t, s.Has("job-1"))

	got := s.Take("job-1")
	assert.Equal(t, secrets, got)
	assert.False(t, s.Has("job-1"), "Take must forget the credentials")
	assert.Nil(t, s.Take("job-1"), "a second Take returns nothing")

	s.Put("job-2", secrets)
	s.Discard("job-2")
	assert.False(t, s.Has("job-2"))
}

func TestParseWizardSecrets(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		form    url.Values
		want    []pendingSecret
		wantErr string
	}{
		{
			name: "a registry and a git credential",
			form: url.Values{
				"secret_kind":     {"registry", "git"},
				"secret_name":     {"regcred", "gitcred"},
				"secret_server":   {"registry.example.com", ""},
				"secret_username": {"bob", ""},
				"secret_token":    {"rtok", "gtok"},
			},
			want: []pendingSecret{
				{Kind: "registry", Name: "regcred", Server: "registry.example.com", Username: "bob", Token: "rtok"},
				// Git username defaults to oauth2 when left blank.
				{Kind: "git", Name: "gitcred", Username: "oauth2", Token: "gtok"},
			},
		},
		{
			name: "a fully blank row is skipped",
			form: url.Values{
				"secret_kind":  {"registry", "git"},
				"secret_name":  {"", "gitcred"},
				"secret_token": {"", "gtok"},
			},
			want: []pendingSecret{{Kind: "git", Name: "gitcred", Username: "oauth2", Token: "gtok"}},
		},
		{
			name:    "a token with no name is an error, not a silent drop",
			form:    url.Values{"secret_kind": {"git"}, "secret_name": {""}, "secret_token": {"gtok"}},
			wantErr: "token but no name",
		},
		{
			name:    "a name with no token",
			form:    url.Values{"secret_kind": {"git"}, "secret_name": {"gitcred"}, "secret_token": {""}},
			wantErr: `credential "gitcred" has no token`,
		},
		{
			name: "duplicate names",
			form: url.Values{
				"secret_kind":  {"git", "git"},
				"secret_name":  {"dup", "dup"},
				"secret_token": {"a", "b"},
			},
			wantErr: `duplicate credential name "dup"`,
		},
		{
			name: "registry without a server",
			form: url.Values{
				"secret_kind": {"registry"}, "secret_name": {"regcred"},
				"secret_username": {"bob"}, "secret_token": {"t"},
			},
			wantErr: "needs a server",
		},
		{
			name: "registry without a username",
			form: url.Values{
				"secret_kind": {"registry"}, "secret_name": {"regcred"},
				"secret_server": {"reg"}, "secret_token": {"t"},
			},
			wantErr: "needs a username",
		},
		{
			name:    "invalid name",
			form:    url.Values{"secret_kind": {"git"}, "secret_name": {"Bad Name"}, "secret_token": {"t"}},
			wantErr: "not valid",
		},
		{
			name:    "unknown kind",
			form:    url.Values{"secret_kind": {"wat"}, "secret_name": {"x"}, "secret_token": {"t"}},
			wantErr: "unknown type",
		},
		{
			name: "no rows at all",
			form: url.Values{},
			want: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tt.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			require.NoError(t, req.ParseForm())

			got, err := parseWizardSecrets(req)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestReferencedCredentialNames(t *testing.T) {
	t.Parallel()

	templates := []WorkspaceTemplate{
		{Name: "a", GitAuthSecret: "gitcred", ImagePullSecrets: []string{"regcred", "shared"}},
		{Name: "b", ImagePullSecrets: []string{"shared"}, Devcontainer: &DevcontainerConfig{RegistryAuthSecret: "cachecred"}},
	}
	// Distinct, in first-seen order, no duplicates.
	assert.Equal(t, []string{"gitcred", "regcred", "shared", "cachecred"}, referencedCredentialNames(templates))
}

func TestPendingCredentialNames(t *testing.T) {
	t.Parallel()

	templates := []WorkspaceTemplate{{Name: "a", GitAuthSecret: "gitcred", ImagePullSecrets: []string{"regcred"}}}

	tests := []struct {
		name     string
		existing []workspace.AuthSecret
		want     []string
	}{
		{
			name:     "all present",
			existing: []workspace.AuthSecret{{Name: "gitcred"}, {Name: "regcred"}},
			want:     nil,
		},
		{
			name:     "one missing",
			existing: []workspace.AuthSecret{{Name: "gitcred"}},
			want:     []string{"regcred"},
		},
		{
			name:     "none present",
			existing: nil,
			want:     []string{"gitcred", "regcred"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, pendingCredentialNames(tt.existing, templates))
		})
	}
}

func TestReferencedCredentials_InfersKind(t *testing.T) {
	t.Parallel()

	templates := []WorkspaceTemplate{
		{Name: "a", GitAuthSecret: "gitcred", ImagePullSecrets: []string{"regcred"},
			Devcontainer: &DevcontainerConfig{RegistryAuthSecret: "cachecred"}},
	}
	assert.Equal(t, []referencedCredential{
		{Name: "gitcred", Kind: workspace.AuthSecretGit},
		{Name: "regcred", Kind: workspace.AuthSecretRegistry},
		{Name: "cachecred", Kind: workspace.AuthSecretRegistry},
	}, referencedCredentials(templates))
}

// applyPendingSecrets writes the captured credentials to the lab's cluster and
// forgets them, so nothing is held past the write.
func TestApplyPendingSecrets(t *testing.T) {
	h, fb, labID := newSecretsTestHandler(t)

	h.pendingSecrets.Put(labID, []pendingSecret{
		{Kind: workspace.AuthSecretRegistry, Name: "regcred", Server: "reg", Username: "bob", Token: "rtok"},
		{Kind: workspace.AuthSecretGit, Name: "gitcred", Username: "oauth2", Token: "gtok"},
	})

	h.applyPendingSecrets(labID)

	assert.Equal(t, []registryCall{{"regcred", "reg", "bob", "rtok"}}, fb.RegistryCalls)
	assert.Equal(t, []gitCall{{"gitcred", "oauth2", "gtok"}}, fb.GitCalls)
	assert.False(t, h.pendingSecrets.Has(labID), "credentials must be forgotten once applied")
}

// A failure applying one credential is logged and does not stop the others, and
// still does not fail the lab.
func TestApplyPendingSecrets_ToleratesFailure(t *testing.T) {
	h, fb, labID := newSecretsTestHandler(t)
	fb.ensureErr = assertAnError

	h.pendingSecrets.Put(labID, []pendingSecret{{Kind: workspace.AuthSecretGit, Name: "gitcred", Username: "oauth2", Token: "t"}})
	h.applyPendingSecrets(labID) // must not panic
	assert.False(t, h.pendingSecrets.Has(labID))
}

var assertAnError = &stubError{"boom"}

type stubError struct{ msg string }

func (e *stubError) Error() string { return e.msg }

func TestServeRecreateCredentials(t *testing.T) {
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	labID := jm.CreateJob(&LabConfig{
		StackName: "s",
		WorkspaceTemplates: []WorkspaceTemplate{
			{Name: "a", GitAuthSecret: "gitcred", ImagePullSecrets: []string{"regcred"}},
		},
	})

	rec := httptest.NewRecorder()
	h.ServeRecreateCredentials(rec, httptest.NewRequest(http.MethodGet, "/api/labs/"+labID+"/recreate-credentials", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, `value="gitcred"`)
	assert.Contains(t, body, `value="regcred"`)
	assert.Contains(t, body, `name="secret_token"`)
	// The git row carries no server field; the registry row does.
	assert.Contains(t, body, `name="secret_server"`)
}

// A lab whose templates reference no credentials yields an empty prompt, so the
// caller recreates without a dialog.
func TestServeRecreateCredentials_EmptyWhenNoneReferenced(t *testing.T) {
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	labID := jm.CreateJob(&LabConfig{StackName: "s", WorkspaceTemplates: []WorkspaceTemplate{{Name: "a"}}})

	rec := httptest.NewRecorder()
	h.ServeRecreateCredentials(rec, httptest.NewRequest(http.MethodGet, "/api/labs/"+labID+"/recreate-credentials", nil))
	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Empty(t, strings.TrimSpace(rec.Body.String()))
}

// A referenced name comes from the admin's template YAML and is echoed into the
// prompt HTML, so it must be escaped.
func TestRenderRecreateCredentials_EscapesNames(t *testing.T) {
	t.Parallel()

	out := renderRecreateCredentials([]referencedCredential{
		{Name: `<script>alert(1)</script>`, Kind: workspace.AuthSecretGit},
	})
	assert.NotContains(t, out, "<script>")
}

func TestRecreateLab_ParsesTokensIntoPendingStore(t *testing.T) {
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	// A destroyed lab that used an existing cluster, so recreate needs no OVH creds.
	labID := jm.CreateJob(&LabConfig{
		StackName:          "s",
		UseExistingCluster: true,
		ExternalKubeconfig: "kc",
		WorkspaceTemplates: []WorkspaceTemplate{{Name: "a", GitAuthSecret: "gitcred"}},
	})
	jm.UpdateJobStatus(labID, JobStatusDestroyed)

	// Prevent the recreated job from actually running Pulumi.
	h.pulumiExec.afterProvision = nil

	form := url.Values{
		"job_id":       {labID},
		"secret_kind":  {"git"},
		"secret_name":  {"gitcred"},
		"secret_token": {"glpat-x"},
	}
	rec := httptest.NewRecorder()
	h.RecreateLab(rec, postForm(t, "/api/labs/recreate", form))

	// The new job ID is not returned to us, but it is the one job that is not the
	// original — find it and assert its credentials were captured.
	var newJobID string
	for _, j := range jm.GetAllJobs() {
		if j.ID != labID {
			newJobID = j.ID
		}
	}
	require.NotEmpty(t, newJobID, "recreate should have created a new job")
	assert.True(t, h.pendingSecrets.Has(newJobID), "the re-entered token should be waiting for the new cluster")
}

func TestAutolinkGitCredential(t *testing.T) {
	tests := []struct {
		name    string
		secrets []pendingSecret
		in      []WorkspaceTemplate
		want    []string // expected GitAuthSecret per template, in order
	}{
		{
			name:    "single git credential fills private-repo templates",
			secrets: []pendingSecret{{Kind: workspace.AuthSecretGit, Name: "gitcred"}},
			in: []WorkspaceTemplate{
				{Name: "a", GitRepo: "https://gitlab.com/o/r.git"},
				{Name: "b"}, // no repo — nothing to authenticate
			},
			want: []string{"gitcred", ""},
		},
		{
			name:    "an explicit choice is preserved",
			secrets: []pendingSecret{{Kind: workspace.AuthSecretGit, Name: "gitcred"}},
			in:      []WorkspaceTemplate{{Name: "a", GitRepo: "https://x", GitAuthSecret: "picked"}},
			want:    []string{"picked"},
		},
		{
			name: "two git credentials are ambiguous — left untouched",
			secrets: []pendingSecret{
				{Kind: workspace.AuthSecretGit, Name: "one"},
				{Kind: workspace.AuthSecretGit, Name: "two"},
			},
			in:   []WorkspaceTemplate{{Name: "a", GitRepo: "https://x"}},
			want: []string{""},
		},
		{
			name:    "a lone registry credential is not a git credential",
			secrets: []pendingSecret{{Kind: workspace.AuthSecretRegistry, Name: "regcred"}},
			in:      []WorkspaceTemplate{{Name: "a", GitRepo: "https://x"}},
			want:    []string{""},
		},
		{
			name:    "no credentials — no change",
			secrets: nil,
			in:      []WorkspaceTemplate{{Name: "a", GitRepo: "https://x"}},
			want:    []string{""},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			autolinkGitCredential(tt.in, tt.secrets)
			got := make([]string, len(tt.in))
			for i, tmpl := range tt.in {
				got[i] = tmpl.GitAuthSecret
			}
			assert.Equal(t, tt.want, got)
		})
	}
}
