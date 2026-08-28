package server

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newHandlerWithOptions(t *testing.T) *Handler {
	t.Helper()
	cm := NewCredentialsManager()
	ovhOpts := NewOVHOptionsManager(t.TempDir(), cm)
	azureOpts := NewAzureOptionsManager(t.TempDir(), cm)
	return NewHandler(NewJobManager(""), &PulumiExecutor{}, cm, ovhOpts, azureOpts, nil)
}

// --- ServeOVHOptions ---

func TestHandler_ServeOVHOptions(t *testing.T) {
	h := newHandlerWithOptions(t)
	req := httptest.NewRequest("GET", "/admin/ovh-options", nil)
	w := httptest.NewRecorder()
	h.ServeOVHOptions(w, req)
	// Template load will fail but data-building code runs
}

// --- SaveOVHOptions ---

func TestHandler_SaveOVHOptions_WrongMethod(t *testing.T) {
	h := newHandlerWithOptions(t)
	req := httptest.NewRequest("GET", "/admin/ovh-options", nil)
	w := httptest.NewRecorder()
	h.SaveOVHOptions(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// --- RefreshOVHOptions ---

func TestHandler_RefreshOVHOptions_WrongMethod(t *testing.T) {
	h := newHandlerWithOptions(t)
	req := httptest.NewRequest("GET", "/admin/ovh-options/refresh", nil)
	w := httptest.NewRecorder()
	h.RefreshOVHOptions(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestHandler_RefreshOVHOptions_NoCredentials(t *testing.T) {
	h := newHandlerWithOptions(t)
	req := httptest.NewRequest("POST", "/admin/ovh-options/refresh", nil)
	w := httptest.NewRecorder()
	h.RefreshOVHOptions(w, req)
	// No OVH credentials → should return error HTML
	assert.Contains(t, w.Body.String(), "error")
}

// TestHandler_RefreshOVHOptions_DoesNotLeakInternalError is a regression test
// for sanitizing this handler's error response: it must not echo the wrapped
// manager error text (which can carry internal detail) back to the client.
func TestHandler_RefreshOVHOptions_DoesNotLeakInternalError(t *testing.T) {
	h := newHandlerWithOptions(t)
	req := httptest.NewRequest("POST", "/admin/ovh-options/refresh", nil)
	w := httptest.NewRecorder()
	h.RefreshOVHOptions(w, req)

	body := w.Body.String()
	assert.NotContains(t, body, "credentials not configured", "response must not echo the raw manager error text")
	assert.Contains(t, body, "Failed to refresh from the OVH API", "response should still explain what happened generically")
}

// TestHandler_SaveOVHOptions_DoesNotLeakInternalError forces SaveConfig to
// fail (dataDir points at a file, so os.MkdirAll cannot create it) and
// asserts the response carries a generic message, not the raw filesystem
// error (which would otherwise expose a server-side path).
func TestHandler_SaveOVHOptions_DoesNotLeakInternalError(t *testing.T) {
	blockedPath := filepath.Join(t.TempDir(), "blocked-by-a-file")
	require.NoError(t, os.WriteFile(blockedPath, []byte("x"), 0644))

	cm := NewCredentialsManager()
	ovhOpts := NewOVHOptionsManager(blockedPath, cm)
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, cm, ovhOpts, NewAzureOptionsManager(t.TempDir(), cm), nil)

	req := httptest.NewRequest("POST", "/admin/ovh-options", nil)
	w := httptest.NewRecorder()
	h.SaveOVHOptions(w, req)

	body := w.Body.String()
	assert.NotContains(t, body, blockedPath, "response must not echo the server-side file path")
	assert.NotContains(t, body, "mkdir", "response must not echo the raw os error text")
	assert.Contains(t, body, "Failed to save configuration", "response should still explain what happened generically")
}

// --- ServeAzureOptions ---

func TestHandler_ServeAzureOptions(t *testing.T) {
	h := newHandlerWithOptions(t)
	req := httptest.NewRequest("GET", "/admin/azure-options", nil)
	w := httptest.NewRecorder()
	h.ServeAzureOptions(w, req)
	// Template load will fail but data-building code runs
}

// --- SaveAzureADConfig ---

func TestHandler_SaveAzureADConfig_WrongMethod(t *testing.T) {
	h := newHandlerWithOptions(t)
	req := httptest.NewRequest("GET", "/admin/azure-options/ad-config", nil)
	w := httptest.NewRecorder()
	h.SaveAzureADConfig(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// --- SaveAzureOptions ---

func TestHandler_SaveAzureOptions_WrongMethod(t *testing.T) {
	h := newHandlerWithOptions(t)
	req := httptest.NewRequest("GET", "/admin/azure-options", nil)
	w := httptest.NewRecorder()
	h.SaveAzureOptions(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// --- RefreshAzureOptions ---

func TestHandler_RefreshAzureOptions_WrongMethod(t *testing.T) {
	h := newHandlerWithOptions(t)
	req := httptest.NewRequest("GET", "/admin/azure-options/refresh", nil)
	w := httptest.NewRecorder()
	h.RefreshAzureOptions(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

func TestHandler_RefreshAzureOptions_NoCredentials(t *testing.T) {
	h := newHandlerWithOptions(t)
	req := httptest.NewRequest("POST", "/admin/azure-options/refresh", nil)
	w := httptest.NewRecorder()
	h.RefreshAzureOptions(w, req)
	// No Azure credentials → should return error
	assert.Contains(t, w.Body.String(), "error")
}

// TestHandler_RefreshAzureOptions_DoesNotLeakInternalError mirrors the OVH
// regression test above for the Azure refresh path.
func TestHandler_RefreshAzureOptions_DoesNotLeakInternalError(t *testing.T) {
	h := newHandlerWithOptions(t)
	req := httptest.NewRequest("POST", "/admin/azure-options/refresh", nil)
	w := httptest.NewRecorder()
	h.RefreshAzureOptions(w, req)

	body := w.Body.String()
	assert.NotContains(t, body, "credentials not configured", "response must not echo the raw manager error text")
	assert.Contains(t, body, "Failed to refresh from the Azure API", "response should still explain what happened generically")
}

// TestHandler_SaveAzureOptions_DoesNotLeakInternalError mirrors the OVH
// SaveConfig-failure regression test above for the Azure options path.
func TestHandler_SaveAzureOptions_DoesNotLeakInternalError(t *testing.T) {
	blockedPath := filepath.Join(t.TempDir(), "blocked-by-a-file")
	require.NoError(t, os.WriteFile(blockedPath, []byte("x"), 0644))

	cm := NewCredentialsManager()
	azureOpts := NewAzureOptionsManager(blockedPath, cm)
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, cm, NewOVHOptionsManager(t.TempDir(), cm), azureOpts, nil)

	req := httptest.NewRequest("POST", "/admin/azure-options", nil)
	w := httptest.NewRecorder()
	h.SaveAzureOptions(w, req)

	body := w.Body.String()
	assert.NotContains(t, body, blockedPath, "response must not echo the server-side file path")
	assert.NotContains(t, body, "mkdir", "response must not echo the raw os error text")
	assert.Contains(t, body, "Failed to save configuration", "response should still explain what happened generically")
}

// TestHandler_SaveAzureADConfig_DoesNotLeakInternalError mirrors the OVH
// SaveConfig-failure regression test above for the Azure AD config path,
// which persists through the same AzureOptionsManager.SaveConfig.
func TestHandler_SaveAzureADConfig_DoesNotLeakInternalError(t *testing.T) {
	blockedPath := filepath.Join(t.TempDir(), "blocked-by-a-file")
	require.NoError(t, os.WriteFile(blockedPath, []byte("x"), 0644))

	cm := NewCredentialsManager()
	azureOpts := NewAzureOptionsManager(blockedPath, cm)
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, cm, NewOVHOptionsManager(t.TempDir(), cm), azureOpts, nil)

	form := url.Values{"azure_ad_client_id": {"client-id"}}
	req := httptest.NewRequest("POST", "/admin/azure-options/ad-config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.SaveAzureADConfig(w, req)

	body := w.Body.String()
	assert.NotContains(t, body, blockedPath, "response must not echo the server-side file path")
	assert.NotContains(t, body, "mkdir", "response must not echo the raw os error text")
	assert.Contains(t, body, "Failed to save Azure AD config", "response should still explain what happened generically")
}

// --- GetAzureVMSizes ---

func TestHandler_GetAzureVMSizes_WrongMethod(t *testing.T) {
	h := newHandlerWithOptions(t)
	req := httptest.NewRequest("POST", "/api/azure/vm-sizes", nil)
	w := httptest.NewRecorder()
	h.GetAzureVMSizes(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// --- GetAzureOptionsRegionVMSizeHTML ---

func TestHandler_GetAzureOptionsRegionVMSizeHTML_WrongMethod(t *testing.T) {
	h := newHandlerWithOptions(t)
	req := httptest.NewRequest("POST", "/api/azure/options/region-vmsizes", nil)
	w := httptest.NewRecorder()
	h.GetAzureOptionsRegionVMSizeHTML(w, req)
	assert.Equal(t, http.StatusMethodNotAllowed, w.Code)
}

// --- SaveAzureADConfig with form ---

func TestHandler_SaveAzureADConfig_EmptyClientID(t *testing.T) {
	h := newHandlerWithOptions(t)
	form := url.Values{}
	form.Set("azure_ad_client_id", "")
	form.Set("azure_ad_tenant_id", "tenant")
	req := httptest.NewRequest("POST", "/admin/azure-options/ad-config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h.SaveAzureADConfig(w, req)
	// Should redirect or return success HTML
	assert.NotEqual(t, http.StatusMethodNotAllowed, w.Code)
}

func TestHandler_SaveAzureADConfig_WithClientID_HTMX(t *testing.T) {
	h := newHandlerWithOptions(t)
	form := url.Values{}
	form.Set("azure_ad_client_id", "my-client")
	form.Set("azure_ad_client_secret", "secret")
	form.Set("azure_ad_tenant_id", "tenant")
	req := httptest.NewRequest("POST", "/admin/azure-options/ad-config", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	h.SaveAzureADConfig(w, req)
	assert.Contains(t, w.Body.String(), "saved")
}

// --- SaveAzureOptions nil manager ---

func TestHandler_SaveAzureOptions_NilManager(t *testing.T) {
	h := NewHandler(NewJobManager(""), &PulumiExecutor{}, NewCredentialsManager(), nil, nil, nil)
	req := httptest.NewRequest("POST", "/admin/azure-options", nil)
	w := httptest.NewRecorder()
	h.SaveAzureOptions(w, req)
	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}

// --- SaveOVHOptions with valid form ---

func TestHandler_SaveOVHOptions_EmptyForm(t *testing.T) {
	h := newHandlerWithOptions(t)
	req := httptest.NewRequest("POST", "/admin/ovh-options", nil)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("HX-Request", "true")
	w := httptest.NewRecorder()
	h.SaveOVHOptions(w, req)
	// Should succeed with no regions selected
	assert.NotEqual(t, http.StatusMethodNotAllowed, w.Code)
}
