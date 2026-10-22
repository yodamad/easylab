package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"

	"easylab/internal/clusterscan"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// testKubeconfig is a kubeconfig clientcmd accepts, with recognisable secret
// material so tests can assert it never reaches disk or a response body.
func testKubeconfig(token string) string {
	return `apiVersion: v1
kind: Config
clusters:
- cluster:
    server: https://cluster.example.com:6443
    insecure-skip-tls-verify: true
  name: test
contexts:
- context:
    cluster: test
    user: test
  name: test
current-context: test
users:
- name: test
  user:
    token: ` + token + "\n"
}

func clusterProfileForm(name, token string) url.Values {
	return url.Values{
		"profile_name":        {name},
		"profile_description": {"Shared workshop cluster"},
		"kubeconfig_content":  {testKubeconfig(token)},
	}
}

func TestCredentialVault_ClusterProfiles(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	v := openTestVault(t, dataDir, testEnvKey(1))

	created, err := v.SaveClusterProfile(ClusterProfile{Name: "Workshops", Description: "eu-west", Kubeconfig: testKubeconfig("secret-token-one")})
	require.NoError(t, err)
	require.NotEmpty(t, created.ID)
	assert.Empty(t, created.Kubeconfig, "a saved profile is returned without its kubeconfig")

	// Names are unique whatever their case.
	_, err = v.SaveClusterProfile(ClusterProfile{Name: "workshops", Kubeconfig: testKubeconfig("x")})
	assert.ErrorIs(t, err, errClusterProfileNameTaken)

	_, err = v.SaveClusterProfile(ClusterProfile{ID: "no-such-id", Name: "Other", Kubeconfig: testKubeconfig("x")})
	assert.ErrorIs(t, err, errClusterProfileNotFound)

	second, err := v.SaveClusterProfile(ClusterProfile{Name: "Alpha", Kubeconfig: testKubeconfig("secret-token-two")})
	require.NoError(t, err)

	listed := v.ListClusterProfiles()
	require.Len(t, listed, 2)
	assert.Equal(t, []string{"Alpha", "Workshops"}, []string{listed[0].Name, listed[1].Name}, "listed by name")
	for _, p := range listed {
		assert.Empty(t, p.Kubeconfig, "listings never carry the kubeconfig")
	}

	raw, err := os.ReadFile(vaultFilePath(dataDir))
	require.NoError(t, err)
	assert.Contains(t, string(raw), "Workshops", "names stay readable so a locked vault can list them")
	assert.Contains(t, string(raw), "eu-west", "descriptions stay readable too")
	for _, secret := range []string{"secret-token-one", "secret-token-two", "cluster.example.com", "current-context"} {
		assert.NotContains(t, string(raw), secret, "no fragment of a kubeconfig is stored in the clear")
	}

	// Survives a restart, kubeconfig included.
	reopened := openTestVault(t, dataDir, testEnvKey(1))
	got, err := reopened.GetClusterProfile(created.ID)
	require.NoError(t, err)
	assert.Equal(t, "Workshops", got.Name)
	assert.Equal(t, "eu-west", got.Description)
	assert.Equal(t, testKubeconfig("secret-token-one"), got.Kubeconfig)

	// Renaming to its own name is not a clash.
	updated := got
	updated.Description = "eu-west-1"
	_, err = reopened.SaveClusterProfile(updated)
	require.NoError(t, err)
	got, err = reopened.GetClusterProfile(created.ID)
	require.NoError(t, err)
	assert.Equal(t, "eu-west-1", got.Description)
	assert.Equal(t, testKubeconfig("secret-token-one"), got.Kubeconfig, "an update that does not touch it keeps the kubeconfig")

	require.NoError(t, reopened.DeleteClusterProfile(second.ID))
	assert.ErrorIs(t, reopened.DeleteClusterProfile(second.ID), errClusterProfileNotFound)
	_, err = reopened.GetClusterProfile(second.ID)
	assert.ErrorIs(t, err, errClusterProfileNotFound)
	assert.Len(t, reopened.ListClusterProfiles(), 1)
}

func TestCredentialVault_ClusterProfilesWhileLocked(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	v := openTestVault(t, dataDir, nil)
	require.NoError(t, v.SetPassphrase(testVaultPassphrase))
	created, err := v.SaveClusterProfile(ClusterProfile{Name: "Workshops", Kubeconfig: testKubeconfig("locked-token")})
	require.NoError(t, err)

	locked := openTestVault(t, dataDir, nil)
	require.Len(t, locked.ListClusterProfiles(), 1, "a locked vault still says what it holds")

	_, err = locked.GetClusterProfile(created.ID)
	assert.ErrorIs(t, err, errVaultLocked)
	_, err = locked.SaveClusterProfile(ClusterProfile{Name: "New", Kubeconfig: testKubeconfig("x")})
	assert.ErrorIs(t, err, errVaultLocked)
	assert.ErrorIs(t, locked.DeleteClusterProfile(created.ID), errVaultLocked)

	require.NoError(t, locked.Unlock(testVaultPassphrase))
	got, err := locked.GetClusterProfile(created.ID)
	require.NoError(t, err)
	assert.Equal(t, testKubeconfig("locked-token"), got.Kubeconfig)
}

// A kubeconfig is bound to its own profile, so a sealed value cannot be moved
// into another slot — another profile's, or a DNS field's.
func TestCredentialVault_ClusterProfileAADBinding(t *testing.T) {
	t.Parallel()
	v := openTestVault(t, t.TempDir(), testEnvKey(1))

	sealed, err := sealWithAEAD(v.aead, testKubeconfig("bound-token"), vaultClusterKubeconfigAAD("profile-a"))
	require.NoError(t, err)

	_, err = openWithAEAD(v.aead, sealed, vaultClusterKubeconfigAAD("profile-a"))
	require.NoError(t, err, "it opens in the slot it was sealed for")

	for _, aad := range []string{
		vaultClusterKubeconfigAAD("profile-b"),
		vaultDNSFieldAAD("profile-a", "kubeconfig"),
		vaultProviderAAD("ovh"),
	} {
		_, err = openWithAEAD(v.aead, sealed, aad)
		assert.Error(t, err, "a kubeconfig must not open under %q", aad)
	}
}

// A key change has to carry the cluster profiles with it, or every saved
// kubeconfig is silently lost on the next passphrase change.
func TestCredentialVault_ClusterProfilesSurviveRekey(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	v := openTestVault(t, dataDir, nil)
	require.NoError(t, v.SetPassphrase(testVaultPassphrase))
	created, err := v.SaveClusterProfile(ClusterProfile{Name: "Workshops", Kubeconfig: testKubeconfig("rekey-token")})
	require.NoError(t, err)

	const nextPassphrase = "another good passphrase"
	require.NoError(t, v.ChangePassphrase(testVaultPassphrase, nextPassphrase))

	reopened := openTestVault(t, dataDir, nil)
	require.NoError(t, reopened.Unlock(nextPassphrase))
	got, err := reopened.GetClusterProfile(created.ID)
	require.NoError(t, err)
	assert.Equal(t, testKubeconfig("rekey-token"), got.Kubeconfig)
}

func TestSaveClusterProfile_Validation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(url.Values)
		want   string
	}{
		{name: "missing name", mutate: func(f url.Values) { f.Set("profile_name", "  ") }, want: "Give the profile a name"},
		{
			name:   "name too long",
			mutate: func(f url.Values) { f.Set("profile_name", strings.Repeat("n", maxClusterProfileNameLength+1)) },
			want:   "Give the profile a name",
		},
		{
			name: "description too long",
			mutate: func(f url.Values) {
				f.Set("profile_description", strings.Repeat("d", maxClusterProfileDescriptionLength+1))
			},
			want: "Keep the description under",
		},
		{name: "missing kubeconfig", mutate: func(f url.Values) { f.Del("kubeconfig_content") }, want: "Provide a kubeconfig"},
		{name: "invalid kubeconfig", mutate: func(f url.Values) { f.Set("kubeconfig_content", "not a kubeconfig") }, want: "does not look like a valid kubeconfig"},
		{name: "unknown profile id", mutate: func(f url.Values) { f.Set("profile_id", "no-such-id") }, want: "no longer exists"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			h, v := vaultTestHandler(t, t.TempDir(), testEnvKey(1))
			form := clusterProfileForm("Workshops", "secret-token")
			tt.mutate(form)

			rec := postVaultForm(h.SaveClusterProfile, "/api/cluster-profiles", form)
			assert.Contains(t, rec.Body.String(), "error-message")
			assert.Contains(t, rec.Body.String(), tt.want)
			assert.Empty(t, rec.Header().Get("HX-Redirect"))
			assert.Empty(t, v.ListClusterProfiles())
		})
	}
}

func TestSaveClusterProfile_CreateUpdateDelete(t *testing.T) {
	t.Parallel()
	h, v := vaultTestHandler(t, t.TempDir(), testEnvKey(1))

	rec := postVaultForm(h.SaveClusterProfile, "/api/cluster-profiles", clusterProfileForm("Workshops", "secret-token"))
	require.Equal(t, "/admin/clusters", rec.Header().Get("HX-Redirect"), "body: %s", rec.Body.String())
	assert.NotContains(t, rec.Body.String(), "secret-token")

	profiles := v.ListClusterProfiles()
	require.Len(t, profiles, 1)
	assert.Equal(t, "Shared workshop cluster", profiles[0].Description)
	id := profiles[0].ID

	// A second profile cannot take the name.
	rec = postVaultForm(h.SaveClusterProfile, "/api/cluster-profiles", clusterProfileForm("WORKSHOPS", "other-token"))
	assert.Contains(t, rec.Body.String(), "already uses this name")

	// Editing with the kubeconfig left blank keeps it; the rest is replaced.
	edit := url.Values{
		"profile_id":          {id},
		"profile_name":        {"Workshops EU"},
		"profile_description": {""},
	}
	rec = postVaultForm(h.SaveClusterProfile, "/api/cluster-profiles", edit)
	require.Equal(t, "/admin/clusters", rec.Header().Get("HX-Redirect"), "body: %s", rec.Body.String())

	got, err := v.GetClusterProfile(id)
	require.NoError(t, err)
	assert.Equal(t, "Workshops EU", got.Name)
	assert.Empty(t, got.Description)
	// The handler trims the submitted kubeconfig before storing it.
	assert.Equal(t, strings.TrimSpace(testKubeconfig("secret-token")), got.Kubeconfig)

	// Providing a new one replaces it.
	replace := url.Values{"profile_id": {id}, "profile_name": {"Workshops EU"}, "kubeconfig_content": {testKubeconfig("replaced-token")}}
	rec = postVaultForm(h.SaveClusterProfile, "/api/cluster-profiles", replace)
	require.Equal(t, "/admin/clusters", rec.Header().Get("HX-Redirect"), "body: %s", rec.Body.String())
	got, err = v.GetClusterProfile(id)
	require.NoError(t, err)
	assert.Equal(t, strings.TrimSpace(testKubeconfig("replaced-token")), got.Kubeconfig)

	rec = postVaultForm(h.DeleteClusterProfile, "/api/cluster-profiles/delete", url.Values{"profile_id": {id}})
	assert.Equal(t, "/admin/clusters", rec.Header().Get("HX-Redirect"))
	assert.Empty(t, v.ListClusterProfiles())

	rec = postVaultForm(h.DeleteClusterProfile, "/api/cluster-profiles/delete", url.Values{"profile_id": {id}})
	assert.Contains(t, rec.Body.String(), "no longer exists")
}

func TestClusterProfileHandlers_RejectBadRequests(t *testing.T) {
	t.Parallel()
	h, _ := vaultTestHandler(t, t.TempDir(), testEnvKey(1))
	noVault := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	handlers := map[string]struct {
		withVault http.HandlerFunc
		without   http.HandlerFunc
		target    string
	}{
		"save":   {h.SaveClusterProfile, noVault.SaveClusterProfile, "/api/cluster-profiles"},
		"delete": {h.DeleteClusterProfile, noVault.DeleteClusterProfile, "/api/cluster-profiles/delete"},
	}
	for name, tt := range handlers {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			rec := httptest.NewRecorder()
			tt.withVault(rec, httptest.NewRequest(http.MethodGet, tt.target, nil))
			assert.Equal(t, http.StatusMethodNotAllowed, rec.Code)

			rec = httptest.NewRecorder()
			tt.without(rec, httptest.NewRequest(http.MethodPost, tt.target, nil))
			assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		})
	}
}

func TestClusterProfileErrorMessage(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		err  error
		want string
	}{
		{name: "not found", err: errClusterProfileNotFound, want: "That cluster profile no longer exists."},
		{name: "name taken", err: errClusterProfileNameTaken, want: "Another cluster profile already uses this name."},
		{name: "invalid kubeconfig", err: clusterscan.ErrInvalidKubeconfig, want: "That does not look like a valid kubeconfig."},
		{name: "unreadable", err: errKubeconfigUnreadable, want: "Failed to read the kubeconfig, please check the file and try again."},
		{name: "locked vault", err: errVaultLocked, want: "Credential storage is locked. Unlock it with the passphrase first."},
		{name: "anything else", err: assert.AnError, want: "Something went wrong. Check the server logs for details."},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tt.want, clusterProfileErrorMessage(tt.err))
		})
	}
}

func TestKubeconfigFromFormOrProfile(t *testing.T) {
	t.Parallel()
	h, v := vaultTestHandler(t, t.TempDir(), testEnvKey(1))
	profile, err := v.SaveClusterProfile(ClusterProfile{Name: "Workshops", Kubeconfig: testKubeconfig("profile-token")})
	require.NoError(t, err)

	lockedDir := t.TempDir()
	lockedSource := openTestVault(t, lockedDir, nil)
	require.NoError(t, lockedSource.SetPassphrase(testVaultPassphrase))
	lockedProfile, err := lockedSource.SaveClusterProfile(ClusterProfile{Name: "Locked", Kubeconfig: testKubeconfig("locked-token")})
	require.NoError(t, err)
	lockedHandler, _ := vaultTestHandler(t, lockedDir, nil)

	noVault := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)

	tests := []struct {
		name        string
		handler     *Handler
		form        url.Values
		wantErr     error
		wantKube    string
		wantProfile string
	}{
		{
			name:    "nothing provided",
			handler: h,
			form:    url.Values{},
		},
		{
			name:     "pasted kubeconfig, no profile",
			handler:  h,
			form:     url.Values{"kubeconfig_content": {testKubeconfig("pasted-token")}},
			wantKube: testKubeconfig("pasted-token"),
		},
		{
			name:        "profile only",
			handler:     h,
			form:        url.Values{"cluster_profile": {profile.ID}},
			wantKube:    testKubeconfig("profile-token"),
			wantProfile: "Workshops",
		},
		{
			name:     "a pasted kubeconfig wins over the profile",
			handler:  h,
			form:     url.Values{"kubeconfig_content": {testKubeconfig("pasted-token")}, "cluster_profile": {profile.ID}},
			wantKube: testKubeconfig("pasted-token"),
		},
		{
			name:    "unknown profile",
			handler: h,
			form:    url.Values{"cluster_profile": {"no-such-id"}},
			wantErr: errClusterProfileNotFound,
		},
		{
			name:    "locked vault",
			handler: lockedHandler,
			form:    url.Values{"cluster_profile": {lockedProfile.ID}},
			wantErr: errVaultLocked,
		},
		{
			name:    "no vault wired",
			handler: noVault,
			form:    url.Values{"cluster_profile": {profile.ID}},
			wantErr: errClusterProfileNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/api/labs", strings.NewReader(tt.form.Encode()))
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

			kubeconfig, profileName, err := tt.handler.kubeconfigFromFormOrProfile(req)
			if tt.wantErr != nil {
				assert.ErrorIs(t, err, tt.wantErr)
				assert.Empty(t, kubeconfig)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tt.wantKube, kubeconfig)
			assert.Equal(t, tt.wantProfile, profileName)
		})
	}
}

func TestWizardClusterProfiles(t *testing.T) {
	t.Parallel()

	dataDir := t.TempDir()
	h, v := vaultTestHandler(t, dataDir, nil)
	require.NoError(t, v.SetPassphrase(testVaultPassphrase))
	_, err := v.SaveClusterProfile(ClusterProfile{Name: "Workshops", Description: "eu-west", Kubeconfig: testKubeconfig("wizard-token")})
	require.NoError(t, err)

	profiles, locked := h.wizardClusterProfiles()
	require.Len(t, profiles, 1)
	assert.Equal(t, "Workshops", profiles[0].Name)
	assert.Equal(t, "eu-west", profiles[0].Description)
	assert.False(t, locked)

	// After a restart the vault is locked: the profile exists but cannot be used.
	restarted, _ := vaultTestHandler(t, dataDir, nil)
	profiles, locked = restarted.wizardClusterProfiles()
	assert.Empty(t, profiles)
	assert.True(t, locked)

	noVault := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	profiles, locked = noVault.wizardClusterProfiles()
	assert.Empty(t, profiles)
	assert.False(t, locked)
}

// The clusters page lists profiles and never prints a kubeconfig, even when
// editing one.
func TestServeClusterProfiles(t *testing.T) {
	t.Chdir("../..") // getTemplate resolves web/ relative to the working directory

	h, v := vaultTestHandler(t, t.TempDir(), testEnvKey(1))
	profile, err := v.SaveClusterProfile(ClusterProfile{Name: "Workshops <b>", Description: "eu-west <i>", Kubeconfig: testKubeconfig("page-token")})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	h.ServeClusterProfiles(rec, httptest.NewRequest(http.MethodGet, "/admin/clusters", nil))
	require.Equal(t, http.StatusOK, rec.Code, "body: %s", rec.Body.String())
	body := rec.Body.String()
	assert.Contains(t, body, "Workshops &lt;b&gt;")
	assert.NotContains(t, body, "Workshops <b>")
	assert.Contains(t, body, "eu-west &lt;i&gt;")
	assert.Contains(t, body, "Add a cluster profile")

	rec = httptest.NewRecorder()
	h.ServeClusterProfiles(rec, httptest.NewRequest(http.MethodGet, "/admin/clusters?edit="+profile.ID, nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body = rec.Body.String()
	assert.Contains(t, body, "Save changes")
	assert.Contains(t, body, "leave both fields empty to keep it")
	for _, secret := range []string{"page-token", "cluster.example.com"} {
		assert.NotContains(t, body, secret, "the kubeconfig is never rendered back")
	}

	noVault := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	rec = httptest.NewRecorder()
	noVault.ServeClusterProfiles(rec, httptest.NewRequest(http.MethodGet, "/admin/clusters", nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
}

// The wizard offers saved clusters, or explains that they are locked.
func TestServeAdminUI_OffersClusterProfiles(t *testing.T) {
	t.Chdir("../..") // getTemplate resolves web/ relative to the working directory

	dataDir := t.TempDir()
	h, v := vaultTestHandler(t, dataDir, nil)
	require.NoError(t, v.SetPassphrase(testVaultPassphrase))
	profile, err := v.SaveClusterProfile(ClusterProfile{Name: "Workshops", Description: "eu-west", Kubeconfig: testKubeconfig("admin-ui-token")})
	require.NoError(t, err)

	rec := httptest.NewRecorder()
	h.ServeAdminUI(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `<option value="`+profile.ID+`" data-name="Workshops" data-description="eu-west">Workshops</option>`)
	assert.NotContains(t, body, "Saved clusters are locked")
	assert.NotContains(t, body, "admin-ui-token")

	restarted, _ := vaultTestHandler(t, dataDir, nil)
	rec = httptest.NewRecorder()
	restarted.ServeAdminUI(rec, httptest.NewRequest(http.MethodGet, "/admin", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	body = rec.Body.String()
	assert.Contains(t, body, "Saved clusters are locked")
	assert.NotContains(t, body, profile.ID)
}
