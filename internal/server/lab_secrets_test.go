package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"easylab/internal/providers/workspace"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeSecretBackend is a fakeBackend that can also manage secrets. It is separate
// from fakeBackend on purpose: SecretManager is optional, and the plain fake
// staying unable to manage secrets is what proves handlers cope with a backend
// that does not implement it.
type fakeSecretBackend struct {
	fakeBackend

	secrets []workspace.AuthSecret

	listErrSecrets error
	ensureErr      error
	deleteErr      error

	RegistryCalls []registryCall
	GitCalls      []gitCall
	DeleteCalls   []string
}

type registryCall struct{ Name, Server, Username, Token string }
type gitCall struct{ Name, Username, Token string }

func (f *fakeSecretBackend) EnsureRegistrySecret(_ context.Context, name, server, username, token string) error {
	f.RegistryCalls = append(f.RegistryCalls, registryCall{name, server, username, token})
	if f.ensureErr != nil {
		return f.ensureErr
	}
	f.secrets = append(f.secrets, workspace.AuthSecret{
		Name: name, Type: workspace.AuthSecretRegistry, Servers: []string{server}, Username: username,
	})
	return nil
}

func (f *fakeSecretBackend) EnsureGitAuthSecret(_ context.Context, name, username, token string) error {
	f.GitCalls = append(f.GitCalls, gitCall{name, username, token})
	if f.ensureErr != nil {
		return f.ensureErr
	}
	f.secrets = append(f.secrets, workspace.AuthSecret{
		Name: name, Type: workspace.AuthSecretGit, Username: username,
	})
	return nil
}

func (f *fakeSecretBackend) ListAuthSecrets(context.Context) ([]workspace.AuthSecret, error) {
	if f.listErrSecrets != nil {
		return nil, f.listErrSecrets
	}
	return f.secrets, nil
}

func (f *fakeSecretBackend) DeleteAuthSecret(_ context.Context, name string) error {
	f.DeleteCalls = append(f.DeleteCalls, name)
	return f.deleteErr
}

// newSecretsTestHandler wires a handler to a lab that is up, with a backend that
// can manage secrets.
func newSecretsTestHandler(t *testing.T) (*Handler, *fakeSecretBackend, string) {
	t.Helper()
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	fb := &fakeSecretBackend{}
	h.newWorkspaceBackend = func(_, _ string) (workspace.Backend, error) { return fb, nil }
	return h, fb, completedLabWithKubeconfig(jm, 0)
}

func TestServeLabSecrets_Gates(t *testing.T) {
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	h.newWorkspaceBackend = func(_, _ string) (workspace.Backend, error) { return &fakeSecretBackend{}, nil }

	pendingLab := jm.CreateJob(&LabConfig{StackName: "pending"})

	noKubeconfig := jm.CreateJob(&LabConfig{StackName: "nokube"})
	jm.UpdateJobStatus(noKubeconfig, JobStatusCompleted)

	tests := []struct {
		name string
		path string
		want int
	}{
		{name: "unknown lab", path: "/api/labs/does-not-exist/secrets", want: http.StatusNotFound},
		{name: "lab still provisioning", path: "/api/labs/" + pendingLab + "/secrets", want: http.StatusBadRequest},
		{name: "lab without a kubeconfig", path: "/api/labs/" + noKubeconfig + "/secrets", want: http.StatusInternalServerError},
		{name: "malformed path", path: "/api/labs/secrets", want: http.StatusBadRequest},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			h.ServeLabSecrets(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))
			assert.Equal(t, tt.want, rec.Code)
		})
	}
}

// A backend that cannot manage secrets must be reported, not panicked on. The
// plain fakeBackend is exactly that backend.
func TestServeLabSecrets_BackendWithoutSecretManager(t *testing.T) {
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	useFakeBackend(h, &fakeBackend{})
	labID := completedLabWithKubeconfig(jm, 0)

	rec := httptest.NewRecorder()
	h.ServeLabSecrets(rec, httptest.NewRequest(http.MethodGet, "/api/labs/"+labID+"/secrets", nil))
	assert.Equal(t, http.StatusNotImplemented, rec.Code)
}

func TestServeLabSecrets_RendersSecrets(t *testing.T) {
	h, fb, labID := newSecretsTestHandler(t)
	fb.secrets = []workspace.AuthSecret{
		{Name: "regcred", Type: workspace.AuthSecretRegistry, Servers: []string{"registry.example.com"}, Username: "bob"},
		{Name: "gitcred", Type: workspace.AuthSecretGit, Username: "oauth2"},
	}

	rec := httptest.NewRecorder()
	h.ServeLabSecrets(rec, httptest.NewRequest(http.MethodGet, "/api/labs/"+labID+"/secrets", nil))
	require.Equal(t, http.StatusOK, rec.Code)

	body := rec.Body.String()
	assert.Contains(t, body, "regcred")
	assert.Contains(t, body, "registry.example.com")
	assert.Contains(t, body, "gitcred")
	// The panel tells the admin what to write in their template.
	assert.Contains(t, body, "image_pull_secrets: regcred")
	assert.Contains(t, body, "git_auth_secret: gitcred")
}

func TestServeLabSecrets_EmptyState(t *testing.T) {
	h, _, labID := newSecretsTestHandler(t)

	rec := httptest.NewRecorder()
	h.ServeLabSecrets(rec, httptest.NewRequest(http.MethodGet, "/api/labs/"+labID+"/secrets", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "No credentials yet")
}

func TestSaveLabSecret(t *testing.T) {
	tests := []struct {
		name       string
		form       url.Values
		wantRegist []registryCall
		wantGit    []gitCall
	}{
		{
			name: "registry credential",
			form: url.Values{
				"kind": {"registry"}, "name": {"regcred"},
				"server": {"registry.example.com"}, "username": {"bob"}, "token": {"tok123"},
			},
			wantRegist: []registryCall{{"regcred", "registry.example.com", "bob", "tok123"}},
		},
		{
			name: "git credential",
			form: url.Values{
				"kind": {"git"}, "name": {"gitcred"}, "username": {"bob"}, "token": {"glpat-x"},
			},
			wantGit: []gitCall{{"gitcred", "bob", "glpat-x"}},
		},
		{
			// An admin who pastes only a token gets the username GitLab expects.
			name:    "git credential without a username defaults to oauth2",
			form:    url.Values{"kind": {"git"}, "name": {"gitcred"}, "token": {"glpat-x"}},
			wantGit: []gitCall{{"gitcred", "oauth2", "glpat-x"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, fb, labID := newSecretsTestHandler(t)
			rec := httptest.NewRecorder()
			h.SaveLabSecret(rec, postForm(t, "/api/labs/"+labID+"/secrets", tt.form))

			require.Equal(t, http.StatusOK, rec.Code)
			assert.Equal(t, tt.wantRegist, fb.RegistryCalls)
			assert.Equal(t, tt.wantGit, fb.GitCalls)
		})
	}
}

// The response re-renders the panel, which must never echo what was just posted.
func TestSaveLabSecret_ResponseNeverEchoesTheToken(t *testing.T) {
	const token = "glpat-do-not-echo"
	h, _, labID := newSecretsTestHandler(t)

	rec := httptest.NewRecorder()
	h.SaveLabSecret(rec, postForm(t, "/api/labs/"+labID+"/secrets", url.Values{
		"kind": {"git"}, "name": {"gitcred"}, "token": {token},
	}))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), token)
}

func TestSaveLabSecret_InvalidInput(t *testing.T) {
	tests := []struct {
		name    string
		form    url.Values
		wantMsg string
	}{
		{
			name:    "no name",
			form:    url.Values{"kind": {"git"}, "token": {"t"}},
			wantMsg: "name is required",
		},
		{
			name:    "name kubernetes would reject",
			form:    url.Values{"kind": {"git"}, "name": {"Not Valid!"}, "token": {"t"}},
			wantMsg: "is not valid",
		},
		{
			name:    "no token",
			form:    url.Values{"kind": {"git"}, "name": {"gitcred"}},
			wantMsg: "token is required",
		},
		{
			name:    "registry without a server",
			form:    url.Values{"kind": {"registry"}, "name": {"regcred"}, "token": {"t"}},
			wantMsg: "registry server is required",
		},
		{
			name:    "unknown kind",
			form:    url.Values{"kind": {"wat"}, "name": {"x"}, "token": {"t"}},
			wantMsg: "Unknown credential type",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h, fb, labID := newSecretsTestHandler(t)
			rec := httptest.NewRecorder()
			h.SaveLabSecret(rec, postForm(t, "/api/labs/"+labID+"/secrets", tt.form))

			body := rec.Body.String()
			assert.Contains(t, body, "toast-error")
			assert.Contains(t, body, tt.wantMsg)
			assert.Empty(t, fb.GitCalls, "nothing should have been written")
			assert.Empty(t, fb.RegistryCalls, "nothing should have been written")
		})
	}
}

// The cluster's error can name the secret and the cluster; the admin gets a
// generic message and the detail goes to the log.
func TestSaveLabSecret_BackendErrorIsSanitized(t *testing.T) {
	h, fb, labID := newSecretsTestHandler(t)
	fb.ensureErr = fmt.Errorf("secrets is forbidden: user cannot create secrets in namespace %q", "workshops")

	rec := httptest.NewRecorder()
	h.SaveLabSecret(rec, postForm(t, "/api/labs/"+labID+"/secrets", url.Values{
		"kind": {"git"}, "name": {"gitcred"}, "token": {"glpat-x"},
	}))

	body := rec.Body.String()
	assert.Contains(t, body, "toast-error")
	assert.Contains(t, body, "Could not save the credential")
	assert.NotContains(t, body, "forbidden")
	assert.NotContains(t, body, "namespace")
}

func TestSaveLabSecret_RejectsGET(t *testing.T) {
	h, _, labID := newSecretsTestHandler(t)
	rec := httptest.NewRecorder()
	h.SaveLabSecret(rec, httptest.NewRequest(http.MethodGet, "/api/labs/"+labID+"/secrets", nil))
	assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)
}

func TestDeleteLabSecret(t *testing.T) {
	h, fb, labID := newSecretsTestHandler(t)

	rec := httptest.NewRecorder()
	h.DeleteLabSecret(rec, postForm(t, "/api/labs/"+labID+"/secrets/delete", url.Values{
		"name": {"gitcred"},
	}))

	require.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, []string{"gitcred"}, fb.DeleteCalls)
}

func TestDeleteLabSecret_RequiresName(t *testing.T) {
	h, fb, labID := newSecretsTestHandler(t)

	rec := httptest.NewRecorder()
	h.DeleteLabSecret(rec, postForm(t, "/api/labs/"+labID+"/secrets/delete", url.Values{}))

	assert.Contains(t, rec.Body.String(), "toast-error")
	assert.Empty(t, fb.DeleteCalls)
}

func TestLabIDFromPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "labs secrets", path: "/api/labs/job-1/secrets", want: "job-1"},
		{name: "labs secrets delete", path: "/api/labs/job-1/secrets/delete", want: "job-1"},
		// The /api/jobs/ prefix is kept for backward compatibility.
		{name: "jobs prefix", path: "/api/jobs/job-1/secrets", want: "job-1"},
		{name: "too short", path: "/api/labs/secrets", want: ""},
		{name: "not an api path", path: "/labs/job-1/secrets", want: ""},
		{name: "empty", path: "/", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, labIDFromPath(tt.path))
		})
	}
}

func TestValidateSecretName(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   string
		wantErr bool
	}{
		{name: "simple", input: "regcred"},
		{name: "with dashes", input: "my-reg-cred"},
		{name: "with dots", input: "reg.example.com"},
		{name: "with digits", input: "cred2"},
		{name: "empty", input: "", wantErr: true},
		{name: "uppercase", input: "RegCred", wantErr: true},
		{name: "spaces", input: "reg cred", wantErr: true},
		{name: "underscore", input: "reg_cred", wantErr: true},
		{name: "slash would escape the namespace", input: "../other", wantErr: true},
		{name: "leading dash", input: "-regcred", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := validateSecretName(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
		})
	}
}

// The panel is HTML; a secret name is attacker-influenced the moment anyone else
// can write to the namespace.
func TestRenderLabSecrets_EscapesNames(t *testing.T) {
	t.Parallel()

	out := renderLabSecrets("job-1", []workspace.AuthSecret{{
		Name: `x"><script>alert(1)</script>`,
		Type: workspace.AuthSecretGit,
	}}, nil)
	assert.NotContains(t, out, "<script>")
}

// A lab with no scheduled deletion date must not get a reschedule prompt on
// recreate — an empty fragment is what lets the client recreate without prompting.
func TestRenderRecreateDeletionDate_NoDate(t *testing.T) {
	t.Parallel()
	assert.Empty(t, renderRecreateDeletionDate(nil))
}

// A lab that had a deletion date gets a reschedule section carrying the old date
// (for context) and blank, future-only inputs the admin fills in before recreation.
func TestRenderRecreateDeletionDate_RendersSection(t *testing.T) {
	t.Parallel()

	old := time.Date(2020, time.March, 4, 9, 30, 0, 0, time.Local)
	out := renderRecreateDeletionDate(&old)

	assert.Contains(t, out, `name="lab_deletion_date"`)
	assert.Contains(t, out, `name="lab_deletion_time"`)
	// The old date is shown so the admin knows why they are being asked.
	assert.Contains(t, out, "Mar 04, 2020")
	// The date input rejects past days client-side via a today floor.
	assert.Contains(t, out, fmt.Sprintf(`min="%s"`, time.Now().Format("2006-01-02")))
}

// A missing credential name is attacker-influenced (it comes from the template
// YAML) and appears in the pending banner, so it must be escaped there too.
func TestRenderLabSecrets_EscapesPendingNames(t *testing.T) {
	t.Parallel()

	out := renderLabSecrets("job-1", nil, []string{`<script>alert(1)</script>`})
	assert.Contains(t, out, "secrets-pending")
	assert.NotContains(t, out, "<script>")
}

func TestListAuthSecretsJSON_CarriesNoSecretMaterial(t *testing.T) {
	t.Parallel()

	// AuthSecret is the type that crosses into the admin's browser; it must have
	// no field a token could live in.
	encoded, err := json.Marshal(workspace.AuthSecret{
		Name: "regcred", Type: workspace.AuthSecretRegistry,
		Servers: []string{"registry.example.com"}, Username: "bob",
	})
	require.NoError(t, err)
	assert.NotContains(t, string(encoded), "password")
	assert.NotContains(t, string(encoded), "auth\"")
}

// EnsureWorkspace's error names the lab's credential Secrets — verifyGitAuthSecret
// and dockerConfigFromSecret both quote the Secret name, and the cluster's own
// errors add the namespace. Students must not be shown any of it: they cannot act
// on it, and it describes the lab's internals.
func TestRequestWorkspace_BackendErrorIsNotShownToTheStudent(t *testing.T) {
	jm := NewJobManager("")
	h := NewHandler(jm, &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	fb := &fakeBackend{
		reachable: true,
		ensureErr: fmt.Errorf(`failed to read git auth secret "gitcred": secrets "gitcred" is forbidden: ` +
			`User "system:serviceaccount:workshops:default" cannot get resource "secrets" in namespace "workshops"`),
	}
	useFakeBackend(h, fb)

	labID := completedLabWithKubeconfig(jm, 0)
	job, _ := jm.GetJob(labID)
	job.mu.Lock()
	job.Config.WorkspaceTemplates = []WorkspaceTemplate{{
		Name: "default", GitRepo: "https://gitlab.com/o/r.git", GitAuthSecret: "gitcred",
	}}
	job.mu.Unlock()

	form := url.Values{"lab_id": {labID}}
	req := postForm(t, "/api/workspace/request", form)
	req = req.WithContext(context.WithValue(req.Context(), studentEmailContextKey, "student@example.com"))

	rec := httptest.NewRecorder()
	h.RequestWorkspace(rec, req)

	body := rec.Body.String()
	require.NotEmpty(t, fb.Ensured, "the test must actually reach EnsureWorkspace")

	assert.Contains(t, body, "contact the lab administrator")
	for _, leak := range []string{"gitcred", "workshops", "forbidden", "serviceaccount", "secrets"} {
		assert.NotContains(t, body, leak, "internal detail %q leaked to the student", leak)
	}
}
