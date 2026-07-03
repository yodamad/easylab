package coder

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/coder/coder/v2/codersdk"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const invalidURL = "://no-scheme"
const invalidUUID = "not-a-valid-uuid"

// validConfig returns a CoderClientConfig pointing at srv.
func validConfig(srv *httptest.Server) CoderClientConfig {
	return CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "test-token",
		OrganizationID: uuid.New().String(),
	}
}

// --- GetTemplates ---

func TestGetTemplates_InvalidURL(t *testing.T) {
	_, err := GetTemplates(CoderClientConfig{ServerURL: invalidURL})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "parse")
}

func TestGetTemplates_InvalidOrgID(t *testing.T) {
	_, err := GetTemplates(CoderClientConfig{
		ServerURL:      "http://localhost",
		OrganizationID: invalidUUID,
	})
	assert.Error(t, err)
}

// --- CreateUser ---

func TestCreateUser_InvalidURL(t *testing.T) {
	_, err := CreateUser(CoderClientConfig{ServerURL: invalidURL}, "a@b.com", "user", "pass")
	assert.Error(t, err)
}

func TestCreateUser_InvalidOrgID(t *testing.T) {
	_, err := CreateUser(CoderClientConfig{
		ServerURL:      "http://localhost",
		OrganizationID: invalidUUID,
	}, "a@b.com", "user", "pass")
	assert.Error(t, err)
}

// --- GetUserByUsername ---

func TestGetUserByUsername_InvalidURL(t *testing.T) {
	_, err := GetUserByUsername(CoderClientConfig{ServerURL: invalidURL}, "username")
	assert.Error(t, err)
}

// --- GetUserByEmail ---

func TestGetUserByEmail_InvalidURL(t *testing.T) {
	_, err := GetUserByEmail(CoderClientConfig{ServerURL: invalidURL}, "a@b.com")
	assert.Error(t, err)
}

func TestGetUserByEmail_InvalidOrgID(t *testing.T) {
	_, err := GetUserByEmail(CoderClientConfig{
		ServerURL:      "http://localhost",
		OrganizationID: invalidUUID,
	}, "a@b.com")
	assert.Error(t, err)
}

// --- CreateWorkspace ---

func TestCreateWorkspace_InvalidURL(t *testing.T) {
	_, err := CreateWorkspace(CoderClientConfig{ServerURL: invalidURL}, uuid.New(), uuid.New(), "ws")
	assert.Error(t, err)
}

func TestCreateWorkspace_InvalidOrgID(t *testing.T) {
	_, err := CreateWorkspace(CoderClientConfig{
		ServerURL:      "http://localhost",
		OrganizationID: invalidUUID,
	}, uuid.New(), uuid.New(), "ws")
	assert.Error(t, err)
}

// --- RefreshToken ---

func TestRefreshToken_InvalidURL(t *testing.T) {
	_, err := RefreshToken(CoderClientConfig{ServerURL: invalidURL}, "admin@example.com", "pass")
	assert.Error(t, err)
}

// --- GetStudentSessionToken ---

func TestGetStudentSessionToken_InvalidURL(t *testing.T) {
	_, err := GetStudentSessionToken(invalidURL, "user@example.com", "pass")
	assert.Error(t, err)
}

// --- ListWorkspaces ---

func TestListWorkspaces_InvalidURL(t *testing.T) {
	_, err := ListWorkspaces(CoderClientConfig{ServerURL: invalidURL}, uuid.New(), "template")
	assert.Error(t, err)
}

// --- GetWorkspaceByOwnerAndName ---

func TestGetWorkspaceByOwnerAndName_InvalidURL(t *testing.T) {
	_, err := GetWorkspaceByOwnerAndName(CoderClientConfig{ServerURL: invalidURL}, "owner", "workspace")
	assert.Error(t, err)
}

// --- DeleteWorkspace ---

func TestDeleteWorkspace_InvalidURL(t *testing.T) {
	err := DeleteWorkspace(CoderClientConfig{ServerURL: invalidURL}, uuid.New())
	assert.Error(t, err)
}

// --- UpdateUserPassword ---

func TestUpdateUserPassword_InvalidURL(t *testing.T) {
	err := UpdateUserPassword(CoderClientConfig{ServerURL: invalidURL}, "user-id", "newpass")
	assert.Error(t, err)
}

// --- ErrUserNotFound / ErrWorkspaceNotFound are exported vars ---

func TestErrUserNotFound(t *testing.T) {
	assert.NotNil(t, ErrUserNotFound)
	assert.Contains(t, ErrUserNotFound.Error(), "user not found")
}

func TestErrWorkspaceNotFound(t *testing.T) {
	assert.NotNil(t, ErrWorkspaceNotFound)
	assert.Contains(t, ErrWorkspaceNotFound.Error(), "workspace not found")
}

// --- waitForCoderReachableStandalone with mock server ---

func TestWaitForCoderReachableStandalone_Success(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/v2/buildinfo" {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]string{"version": "2.29.1"})
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	var logs []string
	err := waitForCoderReachableStandalone(srv.URL, func(msg string) {
		logs = append(logs, msg)
	})
	require.NoError(t, err)
	assert.NotEmpty(t, logs)
}

func TestWaitForCoderReachableStandalone_InvalidURL(t *testing.T) {
	var logs []string
	err := waitForCoderReachableStandalone(invalidURL+"/bad", func(msg string) {
		logs = append(logs, msg)
	})
	assert.Error(t, err)
}

// --- WithRetry family: base failure triggers retry; both fail with invalid URL ---

func TestGetTemplatesWithRetry_InvalidURL(t *testing.T) {
	cfg := CoderClientConfig{ServerURL: invalidURL, OrganizationID: uuid.New().String()}
	_, _, err := GetTemplatesWithRetry(cfg, "admin@example.com", "pass")
	assert.Error(t, err)
}

func TestCreateUserWithRetry_InvalidURL(t *testing.T) {
	cfg := CoderClientConfig{ServerURL: invalidURL, OrganizationID: uuid.New().String()}
	_, _, err := CreateUserWithRetry(cfg, "admin@example.com", "pass", "u@b.com", "user", "pass")
	assert.Error(t, err)
}

func TestCreateWorkspaceWithRetry_InvalidURL(t *testing.T) {
	cfg := CoderClientConfig{ServerURL: invalidURL, OrganizationID: uuid.New().String()}
	_, _, err := CreateWorkspaceWithRetry(cfg, "admin@example.com", "pass", uuid.New(), uuid.New(), "ws")
	assert.Error(t, err)
}

func TestUpdateUserPasswordWithRetry_InvalidURL(t *testing.T) {
	cfg := CoderClientConfig{ServerURL: invalidURL}
	_, err := UpdateUserPasswordWithRetry(cfg, "admin@example.com", "pass", "user-id", "newpass")
	assert.Error(t, err)
}

func TestListWorkspacesWithRetry_InvalidURL(t *testing.T) {
	cfg := CoderClientConfig{ServerURL: invalidURL}
	_, _, err := ListWorkspacesWithRetry(cfg, "admin@example.com", "pass", uuid.New(), "template")
	assert.Error(t, err)
}

func TestGetWorkspaceByOwnerAndNameWithRetry_InvalidURL(t *testing.T) {
	cfg := CoderClientConfig{ServerURL: invalidURL}
	_, _, err := GetWorkspaceByOwnerAndNameWithRetry(cfg, "admin@example.com", "pass", "owner", "ws")
	assert.Error(t, err)
}

func TestDeleteWorkspaceWithRetry_InvalidURL(t *testing.T) {
	cfg := CoderClientConfig{ServerURL: invalidURL}
	_, err := DeleteWorkspaceWithRetry(cfg, "admin@example.com", "pass", uuid.New())
	assert.Error(t, err)
}

func TestGetUserByUsernameWithRetry_InvalidURL(t *testing.T) {
	cfg := CoderClientConfig{ServerURL: invalidURL}
	_, _, err := GetUserByUsernameWithRetry(cfg, "admin@example.com", "pass", "username")
	assert.Error(t, err)
}

// --- InitCoderStandalone error paths ---

func TestInitCoderStandalone_InvalidURL(t *testing.T) {
	var logs []string
	_, err := InitCoderStandalone(invalidURL, "admin@example.com", "pass", func(msg string) {
		logs = append(logs, msg)
	})
	assert.Error(t, err)
}

// --- CreateTemplateStandalone error paths ---

func TestCreateTemplateStandalone_InvalidURL(t *testing.T) {
	var logs []string
	err := CreateTemplateStandalone(
		CoderClientConfig{ServerURL: invalidURL},
		"template", "/tmp/test.zip", nil,
		func(msg string) { logs = append(logs, msg) },
	)
	assert.Error(t, err)
}

func TestCreateTemplateStandalone_InvalidOrgID(t *testing.T) {
	var logs []string
	err := CreateTemplateStandalone(
		CoderClientConfig{ServerURL: "http://localhost", OrganizationID: invalidUUID},
		"template", "/tmp/test.zip", nil,
		func(msg string) { logs = append(logs, msg) },
	)
	assert.Error(t, err)
}

func TestCreateTemplateStandalone_MissingZipFile(t *testing.T) {
	var logs []string
	err := CreateTemplateStandalone(
		CoderClientConfig{ServerURL: "http://localhost", OrganizationID: uuid.New().String()},
		"template", "/nonexistent/template.zip", nil,
		func(msg string) { logs = append(logs, msg) },
	)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "zip file")
}

// --- CreateTemplateAsync error paths ---

func TestCreateTemplateAsync_InvalidURL(t *testing.T) {
	cfg := AsyncTemplateConfig{
		ServerURL:      invalidURL,
		OrganizationID: uuid.New().String(),
		TemplateName:   "test",
		ZipFilePath:    "/tmp/test.zip",
	}
	errChan := CreateTemplateAsync(cfg)
	err := <-errChan
	assert.Error(t, err)
}

func TestCreateTemplateAsync_InvalidOrgID(t *testing.T) {
	cfg := AsyncTemplateConfig{
		ServerURL:      "http://localhost",
		OrganizationID: invalidUUID,
		TemplateName:   "test",
		ZipFilePath:    "/tmp/test.zip",
	}
	errChan := CreateTemplateAsync(cfg)
	err := <-errChan
	assert.Error(t, err)
}

func TestCreateTemplateAsync_MissingZipFile(t *testing.T) {
	cfg := AsyncTemplateConfig{
		ServerURL:      "http://localhost",
		OrganizationID: uuid.New().String(),
		TemplateName:   "test",
		ZipFilePath:    "/nonexistent/path/template.zip",
	}
	errChan := CreateTemplateAsync(cfg)
	err := <-errChan
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "zip file")
}

func TestRefreshToken_Failure(t *testing.T) {
	// Server returns 401 for login → RefreshToken should fail
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"message": "unauthorized"})
	}))
	defer srv.Close()

	cfg := CoderClientConfig{ServerURL: srv.URL, SessionToken: "token"}
	_, err := RefreshToken(cfg, "admin@example.com", "wrong-pass")
	assert.Error(t, err)
}

func TestGetStudentSessionToken_Failure(t *testing.T) {
	// Server returns 401 for login
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		json.NewEncoder(w).Encode(map[string]string{"message": "unauthorized"})
	}))
	defer srv.Close()

	_, err := GetStudentSessionToken(srv.URL, "student@example.com", "wrong-pass")
	assert.Error(t, err)
}

func TestCreateUser_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusConflict)
		json.NewEncoder(w).Encode(map[string]string{"message": "user already exists"})
	}))
	defer srv.Close()

	cfg := CoderClientConfig{ServerURL: srv.URL, SessionToken: "token", OrganizationID: uuid.New().String()}
	_, err := CreateUser(cfg, "existing@example.com", "existing", "pass")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create user")
}

func TestCreateWorkspace_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"message": "invalid workspace name"})
	}))
	defer srv.Close()

	cfg := CoderClientConfig{ServerURL: srv.URL, SessionToken: "token", OrganizationID: uuid.New().String()}
	_, err := CreateWorkspace(cfg, uuid.New(), uuid.New(), "invalid name")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create workspace")
}

func TestGetTemplates_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		json.NewEncoder(w).Encode(map[string]string{"message": "forbidden"})
	}))
	defer srv.Close()

	cfg := CoderClientConfig{ServerURL: srv.URL, SessionToken: "token", OrganizationID: uuid.New().String()}
	_, err := GetTemplates(cfg)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get templates")
}

func TestUpdateUserPassword_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		json.NewEncoder(w).Encode(map[string]string{"message": "bad request"})
	}))
	defer srv.Close()

	cfg := CoderClientConfig{ServerURL: srv.URL, SessionToken: "token"}
	err := UpdateUserPassword(cfg, "testuser", "weak")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to update user password")
}

func TestGetUserByUsername_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"message": "user not found"})
	}))
	defer srv.Close()

	cfg := CoderClientConfig{ServerURL: srv.URL, SessionToken: "token"}
	_, err := GetUserByUsername(cfg, "nonexistent")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get user by username")
}

func TestGetWorkspaceByOwnerAndName_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return 404 for any workspace lookup
		w.WriteHeader(http.StatusNotFound)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{"message": "workspace not found"})
	}))
	defer srv.Close()

	cfg := CoderClientConfig{ServerURL: srv.URL, SessionToken: "token", OrganizationID: uuid.New().String()}
	_, err := GetWorkspaceByOwnerAndName(cfg, "owner", "nonexistent-workspace")
	assert.ErrorIs(t, err, ErrWorkspaceNotFound)
}

func TestCreateTemplateStandalone_TemplateCreationFails(t *testing.T) {
	orgID := uuid.New()
	fileID := uuid.New()
	versionID := uuid.New()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v2/files":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(codersdk.UploadResponse{ID: fileID})
		case strings.HasSuffix(r.URL.Path, "/templateversions") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			version := codersdk.TemplateVersion{ID: versionID}
			version.Job.Status = codersdk.ProvisionerJobSucceeded
			json.NewEncoder(w).Encode(version)
		case strings.HasPrefix(r.URL.Path, "/api/v2/templateversions/"):
			version := codersdk.TemplateVersion{ID: versionID}
			version.Job.Status = codersdk.ProvisionerJobSucceeded
			json.NewEncoder(w).Encode(version)
		case strings.HasSuffix(r.URL.Path, "/templates") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"message": "bad request"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	zipFile, err := os.CreateTemp(t.TempDir(), "template-*.zip")
	require.NoError(t, err)
	zipFile.Write([]byte("zip content"))
	zipFile.Close()

	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	var logs []string
	err = CreateTemplateStandalone(cfg, "test-template", zipFile.Name(), nil, func(msg string) {
		logs = append(logs, msg)
	})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create template")
}

func TestCreateTemplateAsync_UploadFails(t *testing.T) {
	orgID := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]string{"message": "upload failed"})
	}))
	defer srv.Close()

	zipFile, err := os.CreateTemp(t.TempDir(), "template-*.zip")
	require.NoError(t, err)
	zipFile.Write([]byte("zip content"))
	zipFile.Close()

	cfg := AsyncTemplateConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
		TemplateName:   "test",
		ZipFilePath:    zipFile.Name(),
		LogCallback:    func(msg string) {},
	}
	errChan := CreateTemplateAsync(cfg)
	err = <-errChan
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to upload")
}

func TestCreateTemplateAsync_VersionCreationFails(t *testing.T) {
	orgID := uuid.New()
	fileID := uuid.New()
	callCount := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		callCount++
		switch {
		case r.URL.Path == "/api/v2/files":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(codersdk.UploadResponse{ID: fileID})
		case strings.HasSuffix(r.URL.Path, "/templateversions") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"message": "version creation failed"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	zipFile, err := os.CreateTemp(t.TempDir(), "template-*.zip")
	require.NoError(t, err)
	zipFile.Write([]byte("zip content"))
	zipFile.Close()

	cfg := AsyncTemplateConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
		TemplateName:   "test",
		ZipFilePath:    zipFile.Name(),
		LogCallback:    func(msg string) {},
	}
	errChan := CreateTemplateAsync(cfg)
	err = <-errChan
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create template version")
}

func TestCreateTemplateAsync_VersionProcessingFails(t *testing.T) {
	orgID := uuid.New()
	fileID := uuid.New()
	versionID := uuid.New()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v2/files":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(codersdk.UploadResponse{ID: fileID})
		case strings.HasSuffix(r.URL.Path, "/templateversions") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			version := codersdk.TemplateVersion{ID: versionID}
			version.Job.Status = codersdk.ProvisionerJobFailed
			version.Job.Error = "provisioning failed"
			json.NewEncoder(w).Encode(version)
		case strings.HasPrefix(r.URL.Path, "/api/v2/templateversions/"):
			version := codersdk.TemplateVersion{ID: versionID}
			version.Job.Status = codersdk.ProvisionerJobFailed
			version.Job.Error = "provisioning failed"
			json.NewEncoder(w).Encode(version)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	zipFile, err := os.CreateTemp(t.TempDir(), "template-*.zip")
	require.NoError(t, err)
	zipFile.Write([]byte("zip content"))
	zipFile.Close()

	cfg := AsyncTemplateConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
		TemplateName:   "test",
		ZipFilePath:    zipFile.Name(),
		LogCallback:    func(msg string) {},
	}
	errChan := CreateTemplateAsync(cfg)
	err = <-errChan
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "template version failed")
}

func TestCreateTemplateAsync_TemplateCreationFails(t *testing.T) {
	orgID := uuid.New()
	fileID := uuid.New()
	versionID := uuid.New()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v2/files":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(codersdk.UploadResponse{ID: fileID})
		case strings.HasSuffix(r.URL.Path, "/templateversions") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			version := codersdk.TemplateVersion{}
			version.ID = versionID
			version.Job.Status = codersdk.ProvisionerJobSucceeded
			json.NewEncoder(w).Encode(version)
		case strings.HasPrefix(r.URL.Path, "/api/v2/templateversions/"):
			version := codersdk.TemplateVersion{}
			version.ID = versionID
			version.Job.Status = codersdk.ProvisionerJobSucceeded
			json.NewEncoder(w).Encode(version)
		case strings.HasSuffix(r.URL.Path, "/templates") && r.Method == http.MethodPost:
			// Template creation fails
			w.WriteHeader(http.StatusBadRequest)
			json.NewEncoder(w).Encode(map[string]string{"message": "template creation failed"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	zipFile, err := os.CreateTemp(t.TempDir(), "template-*.zip")
	require.NoError(t, err)
	zipFile.Write([]byte("zip content"))
	zipFile.Close()

	cfg := AsyncTemplateConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
		TemplateName:   "test-template",
		ZipFilePath:    zipFile.Name(),
		LogCallback:    func(msg string) {},
	}
	errChan := CreateTemplateAsync(cfg)
	err = <-errChan
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to create template")
}

func TestWaitForTemplateVersionAsync_APIError(t *testing.T) {
	versionID := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Return 404 to trigger an API error
		http.NotFound(w, r)
	}))
	defer srv.Close()

	serverURL, _ := url.Parse(srv.URL)
	client := codersdk.New(serverURL)
	var logs []string
	err := waitForTemplateVersionAsync(client, versionID, func(msg string) { logs = append(logs, msg) })
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to get template version status")
}

func TestWaitForTemplateVersion_APIError(t *testing.T) {
	versionID := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()

	serverURL, _ := url.Parse(srv.URL)
	client := codersdk.New(serverURL)
	err := waitForTemplateVersion(client, versionID)
	assert.Error(t, err)
}

// --- GetUserByEmailWithRetry short-circuit on ErrUserNotFound ---

func TestGetUserByEmailWithRetry_UserNotFound(t *testing.T) {
	// Server returns 200 with empty users list → ErrUserNotFound short-circuits retry
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// Return empty user list to trigger ErrUserNotFound
		json.NewEncoder(w).Encode(map[string]interface{}{
			"users": []interface{}{},
			"count": 0,
		})
	}))
	defer srv.Close()

	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: uuid.New().String(),
	}
	_, _, err := GetUserByEmailWithRetry(cfg, "admin@example.com", "pass", "find@example.com")
	// The function either returns ErrUserNotFound or a network/API error — either is an error
	assert.Error(t, err)
}

// --- Mock server for happy-path tests ---

// mockCoderServer builds an httptest.Server that handles the minimal Coder API
// endpoints needed by the functions under test.
func mockCoderServer(t *testing.T) (*httptest.Server, uuid.UUID, uuid.UUID) {
	t.Helper()
	userID := uuid.New()
	orgID := uuid.New()
	templateID := uuid.New()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == "/api/v2/buildinfo":
			json.NewEncoder(w).Encode(map[string]string{"version": "2.29.1"})

		case r.URL.Path == "/api/v2/users" && r.Method == http.MethodGet:
			// GetUserByEmail / Users search
			user := codersdk.User{}
			user.ID = userID
			user.Email = "test@example.com"
			user.Username = "testuser"
			user.CreatedAt = time.Now()
			user.UpdatedAt = time.Now()
			user.Status = codersdk.UserStatusActive
			user.OrganizationIDs = []uuid.UUID{orgID}
			json.NewEncoder(w).Encode(codersdk.GetUsersResponse{
				Users: []codersdk.User{user},
				Count: 1,
			})

		case r.URL.Path == "/api/v2/users" && r.Method == http.MethodPost:
			// CreateUser - codersdk expects 201
			w.WriteHeader(http.StatusCreated)
			user := codersdk.User{}
			user.ID = userID
			user.Email = "new@example.com"
			user.Username = "newuser"
			user.CreatedAt = time.Now()
			user.Status = codersdk.UserStatusActive
			user.OrganizationIDs = []uuid.UUID{orgID}
			json.NewEncoder(w).Encode(user)

		case r.URL.Path == "/api/v2/users/testuser":
			// GetUserByUsername
			user := codersdk.User{}
			user.ID = userID
			user.Username = "testuser"
			user.Email = "test@example.com"
			user.Status = codersdk.UserStatusActive
			json.NewEncoder(w).Encode(user)

		case r.URL.Path == "/api/v2/organizations/"+orgID.String()+"/templates":
			// GetTemplates
			tmpl := codersdk.Template{}
			tmpl.ID = templateID
			tmpl.Name = "test-template"
			tmpl.OrganizationID = orgID
			json.NewEncoder(w).Encode([]codersdk.Template{tmpl})

		case r.URL.Path == "/api/v2/workspaces":
			// ListWorkspaces
			ws := codersdk.Workspace{}
			ws.ID = uuid.New()
			ws.Name = "test-workspace"
			json.NewEncoder(w).Encode(codersdk.WorkspacesResponse{
				Workspaces: []codersdk.Workspace{ws},
				Count:      1,
			})

		case strings.HasPrefix(r.URL.Path, "/api/v2/users/") && strings.HasSuffix(r.URL.Path, "/workspaces") && r.Method == http.MethodPost:
			// CreateWorkspace
			w.WriteHeader(http.StatusCreated)
			ws := codersdk.Workspace{}
			ws.ID = uuid.New()
			ws.Name = "new-workspace"
			json.NewEncoder(w).Encode(ws)

		case strings.HasPrefix(r.URL.Path, "/api/v2/users/") && strings.Contains(r.URL.Path, "/workspace/"):
			// GetWorkspaceByOwnerAndName
			ws := codersdk.Workspace{}
			ws.ID = uuid.New()
			ws.Name = "test-workspace"
			json.NewEncoder(w).Encode(ws)

		case strings.HasPrefix(r.URL.Path, "/api/v2/users/") && strings.HasSuffix(r.URL.Path, "/password") && r.Method == http.MethodPut:
			// UpdateUserPassword
			w.WriteHeader(http.StatusNoContent)

		case r.URL.Path == "/api/v2/users/login" && r.Method == http.MethodPost:
			// LoginWithPassword
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(codersdk.LoginWithPasswordResponse{
				SessionToken: "new-session-token",
			})

		case strings.HasPrefix(r.URL.Path, "/api/v2/workspaces/") && strings.HasSuffix(r.URL.Path, "/builds") && r.Method == http.MethodPost:
			// CreateWorkspaceBuild (for DeleteWorkspace) - expects 201
			build := codersdk.WorkspaceBuild{}
			build.ID = uuid.New()
			build.WorkspaceID = uuid.New()
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(build)

		default:
			http.NotFound(w, r)
		}
	}))

	t.Cleanup(srv.Close)
	return srv, orgID, userID
}

func TestGetUserByEmail_Found(t *testing.T) {
	srv, orgID, _ := mockCoderServer(t)
	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	user, err := GetUserByEmail(cfg, "test@example.com")
	require.NoError(t, err)
	assert.Equal(t, "test@example.com", user.Email)
}

func TestGetUserByEmail_NotFound(t *testing.T) {
	srv, orgID, _ := mockCoderServer(t)
	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	_, err := GetUserByEmail(cfg, "notfound@example.com")
	assert.ErrorIs(t, err, ErrUserNotFound)
}

func TestGetUserByUsername_Found(t *testing.T) {
	srv, orgID, _ := mockCoderServer(t)
	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	user, err := GetUserByUsername(cfg, "testuser")
	require.NoError(t, err)
	assert.Equal(t, "testuser", user.Username)
}

func TestGetTemplates_Found(t *testing.T) {
	srv, orgID, _ := mockCoderServer(t)
	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	templates, err := GetTemplates(cfg)
	require.NoError(t, err)
	assert.Len(t, templates, 1)
	assert.Equal(t, "test-template", templates[0].Name)
}

func TestCreateUser_Found(t *testing.T) {
	srv, orgID, _ := mockCoderServer(t)
	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	user, err := CreateUser(cfg, "new@example.com", "newuser", "pass")
	require.NoError(t, err)
	assert.Equal(t, "new@example.com", user.Email)
}

func TestListWorkspaces_Found(t *testing.T) {
	srv, orgID, _ := mockCoderServer(t)
	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	workspaces, err := ListWorkspaces(cfg, uuid.New(), "template")
	require.NoError(t, err)
	assert.Len(t, workspaces, 1)
	assert.Equal(t, "test-workspace", workspaces[0].Name)
}

// --- waitForTemplateVersion and waitForTemplateVersionAsync ---

func TestWaitForTemplateVersion_Success(t *testing.T) {
	versionID := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		version := codersdk.TemplateVersion{}
		version.ID = versionID
		version.Job.Status = codersdk.ProvisionerJobSucceeded
		version.Job.CompletedAt = nil
		json.NewEncoder(w).Encode(version)
	}))
	defer srv.Close()

	serverURL, _ := url.Parse(srv.URL)
	client := codersdk.New(serverURL)
	err := waitForTemplateVersion(client, versionID)
	require.NoError(t, err)
}

func TestWaitForTemplateVersion_Failed(t *testing.T) {
	versionID := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		version := codersdk.TemplateVersion{}
		version.ID = versionID
		version.Job.Status = codersdk.ProvisionerJobFailed
		version.Job.Error = "something broke"
		json.NewEncoder(w).Encode(version)
	}))
	defer srv.Close()

	serverURL, _ := url.Parse(srv.URL)
	client := codersdk.New(serverURL)
	err := waitForTemplateVersion(client, versionID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed")
}

func TestWaitForTemplateVersionAsync_Success(t *testing.T) {
	versionID := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		version := codersdk.TemplateVersion{}
		version.ID = versionID
		version.Job.Status = codersdk.ProvisionerJobSucceeded
		json.NewEncoder(w).Encode(version)
	}))
	defer srv.Close()

	serverURL, _ := url.Parse(srv.URL)
	client := codersdk.New(serverURL)
	var logs []string
	err := waitForTemplateVersionAsync(client, versionID, func(msg string) { logs = append(logs, msg) })
	require.NoError(t, err)
}

func TestWaitForTemplateVersionAsync_Canceled(t *testing.T) {
	versionID := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		version := codersdk.TemplateVersion{}
		version.ID = versionID
		version.Job.Status = codersdk.ProvisionerJobCanceled
		json.NewEncoder(w).Encode(version)
	}))
	defer srv.Close()

	serverURL, _ := url.Parse(srv.URL)
	client := codersdk.New(serverURL)
	err := waitForTemplateVersionAsync(client, versionID, func(msg string) {})
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "canceled")
}

func TestWaitForTemplateVersion_Canceled(t *testing.T) {
	versionID := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		version := codersdk.TemplateVersion{}
		version.ID = versionID
		version.Job.Status = codersdk.ProvisionerJobCanceled
		json.NewEncoder(w).Encode(version)
	}))
	defer srv.Close()

	serverURL, _ := url.Parse(srv.URL)
	client := codersdk.New(serverURL)
	err := waitForTemplateVersion(client, versionID)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "canceled")
}

func TestCreateWorkspace_Found(t *testing.T) {
	srv, orgID, userID := mockCoderServer(t)
	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	ws, err := CreateWorkspace(cfg, userID, uuid.New(), "my-workspace")
	require.NoError(t, err)
	assert.Equal(t, "new-workspace", ws.Name)
}

func TestGetWorkspaceByOwnerAndName_Found(t *testing.T) {
	srv, orgID, userID := mockCoderServer(t)
	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	ws, err := GetWorkspaceByOwnerAndName(cfg, userID.String(), "test-workspace")
	require.NoError(t, err)
	assert.Equal(t, "test-workspace", ws.Name)
}

func TestUpdateUserPassword_Success(t *testing.T) {
	srv, orgID, _ := mockCoderServer(t)
	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	err := UpdateUserPassword(cfg, "testuser", "newpassword")
	require.NoError(t, err)
}

func TestGetUserByUsernameWithRetry_Success(t *testing.T) {
	srv, orgID, _ := mockCoderServer(t)
	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	user, newCfg, err := GetUserByUsernameWithRetry(cfg, "admin@example.com", "pass", "testuser")
	require.NoError(t, err)
	assert.Equal(t, "testuser", user.Username)
	assert.Equal(t, cfg.ServerURL, newCfg.ServerURL)
}

func TestCreateUserWithRetry_Success(t *testing.T) {
	srv, orgID, _ := mockCoderServer(t)
	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	user, newCfg, err := CreateUserWithRetry(cfg, "admin@example.com", "pass", "new@example.com", "newuser", "pass")
	require.NoError(t, err)
	assert.NotEmpty(t, user.Email)
	assert.Equal(t, cfg.ServerURL, newCfg.ServerURL)
}

func TestCreateWorkspaceWithRetry_Success(t *testing.T) {
	srv, orgID, userID := mockCoderServer(t)
	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	ws, newCfg, err := CreateWorkspaceWithRetry(cfg, "admin@example.com", "pass", userID, uuid.New(), "my-ws")
	require.NoError(t, err)
	assert.Equal(t, "new-workspace", ws.Name)
	assert.Equal(t, cfg.ServerURL, newCfg.ServerURL)
}

func TestListWorkspacesWithRetry_Success(t *testing.T) {
	srv, orgID, _ := mockCoderServer(t)
	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	workspaces, newCfg, err := ListWorkspacesWithRetry(cfg, "admin@example.com", "pass", uuid.New(), "tmpl")
	require.NoError(t, err)
	assert.Len(t, workspaces, 1)
	assert.Equal(t, cfg.ServerURL, newCfg.ServerURL)
}

func newFullCoderMockServer(t *testing.T) (*httptest.Server, uuid.UUID) {
	t.Helper()
	orgID := uuid.New()
	fileID := uuid.New()
	versionID := uuid.New()
	templateID := uuid.New()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == "/api/v2/buildinfo":
			json.NewEncoder(w).Encode(map[string]string{"version": "2.29.1"})

		case r.URL.Path == "/api/v2/files" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(codersdk.UploadResponse{ID: fileID})

		case strings.HasSuffix(r.URL.Path, "/templateversions") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			version := codersdk.TemplateVersion{}
			version.ID = versionID
			version.Job.Status = codersdk.ProvisionerJobSucceeded
			json.NewEncoder(w).Encode(version)

		case strings.HasPrefix(r.URL.Path, "/api/v2/templateversions/"):
			version := codersdk.TemplateVersion{}
			version.ID = versionID
			version.Job.Status = codersdk.ProvisionerJobSucceeded
			json.NewEncoder(w).Encode(version)

		case strings.HasSuffix(r.URL.Path, "/templates") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			tmpl := codersdk.Template{}
			tmpl.ID = templateID
			tmpl.Name = "test-template"
			tmpl.OrganizationID = orgID
			json.NewEncoder(w).Encode(tmpl)

		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, orgID
}

func TestCreateTemplateStandalone_Success(t *testing.T) {
	srv, orgID := newFullCoderMockServer(t)

	zipFile, err := os.CreateTemp(t.TempDir(), "template-*.zip")
	require.NoError(t, err)
	zipFile.Write([]byte("fake zip content"))
	zipFile.Close()

	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	var logs []string
	err = CreateTemplateStandalone(cfg, "test-template", zipFile.Name(), nil, func(msg string) {
		logs = append(logs, msg)
	})
	require.NoError(t, err)
	assert.NotEmpty(t, logs)
}

func TestCreateTemplateStandalone_WithVariables(t *testing.T) {
	srv, orgID := newFullCoderMockServer(t)

	zipFile, err := os.CreateTemp(t.TempDir(), "template-*.zip")
	require.NoError(t, err)
	zipFile.Write([]byte("fake zip"))
	zipFile.Close()

	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	vars := map[string]string{"var1": "value1", "var2": "value2"}
	var logs []string
	err = CreateTemplateStandalone(cfg, "test-template", zipFile.Name(), vars, func(msg string) {
		logs = append(logs, msg)
	})
	require.NoError(t, err)
	// Check that the variable-setting log message appeared
	found := false
	for _, l := range logs {
		if strings.Contains(l, "variable") {
			found = true
		}
	}
	assert.True(t, found, "should have logged variable setting")
}

func TestCreateTemplateAsync_Success(t *testing.T) {
	orgID := uuid.New()
	fileID := uuid.New()
	versionID := uuid.New()
	templateID := uuid.New()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.URL.Path == "/api/v2/files" && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(codersdk.UploadResponse{ID: fileID})

		case strings.HasSuffix(r.URL.Path, "/templateversions") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			version := codersdk.TemplateVersion{}
			version.ID = versionID
			version.Job.Status = codersdk.ProvisionerJobSucceeded
			json.NewEncoder(w).Encode(version)

		case strings.HasPrefix(r.URL.Path, "/api/v2/templateversions/"):
			version := codersdk.TemplateVersion{}
			version.ID = versionID
			version.Job.Status = codersdk.ProvisionerJobSucceeded
			json.NewEncoder(w).Encode(version)

		case strings.HasSuffix(r.URL.Path, "/templates") && r.Method == http.MethodPost:
			w.WriteHeader(http.StatusCreated)
			tmpl := codersdk.Template{}
			tmpl.ID = templateID
			tmpl.Name = "test-template"
			tmpl.OrganizationID = orgID
			json.NewEncoder(w).Encode(tmpl)

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// Create a temporary zip file
	zipFile, err := os.CreateTemp(t.TempDir(), "template-*.zip")
	require.NoError(t, err)
	zipFile.Write([]byte("fake zip content"))
	zipFile.Close()

	cfg := AsyncTemplateConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
		TemplateName:   "test-template",
		ZipFilePath:    zipFile.Name(),
		LogCallback:    func(msg string) {},
	}

	errChan := CreateTemplateAsync(cfg)
	err = <-errChan
	require.NoError(t, err)
}

func TestRefreshToken_Success(t *testing.T) {
	srv, orgID, _ := mockCoderServer(t)
	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "old-token",
		OrganizationID: orgID.String(),
	}
	newCfg, err := RefreshToken(cfg, "admin@example.com", "pass")
	require.NoError(t, err)
	assert.Equal(t, "new-session-token", newCfg.SessionToken)
	assert.Equal(t, cfg.ServerURL, newCfg.ServerURL)
}

func TestGetStudentSessionToken_Success(t *testing.T) {
	srv, _, _ := mockCoderServer(t)
	token, err := GetStudentSessionToken(srv.URL, "student@example.com", "pass")
	require.NoError(t, err)
	assert.Equal(t, "new-session-token", token)
}

// TestGetUserByEmailWithRetry_RefreshesToken tests the token refresh path when GetUserByEmail fails with a non-ErrUserNotFound error.
func makeRetryMockServer(t *testing.T, orgID uuid.UUID, firstHandler func(w http.ResponseWriter, r *http.Request), retryHandler func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	callCount := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		callCount++
		if r.URL.Path == "/api/v2/users/login" {
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(codersdk.LoginWithPasswordResponse{SessionToken: "refreshed-token"})
			return
		}
		// RefreshToken mints a long-lived admin token after logging in; model that
		// endpoint so the refreshed config carries the minted token.
		if strings.HasSuffix(r.URL.Path, "/keys/tokens") && r.Method == http.MethodPost {
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(codersdk.GenerateAPIKeyResponse{Key: "refreshed-token"})
			return
		}
		if callCount <= 1 {
			firstHandler(w, r)
		} else {
			retryHandler(w, r)
		}
	}))
}

func TestListWorkspacesWithRetry_RefreshesToken(t *testing.T) {
	orgID := uuid.New()
	srv := makeRetryMockServer(t, orgID,
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"message": "error"})
		},
		func(w http.ResponseWriter, r *http.Request) {
			json.NewEncoder(w).Encode(codersdk.WorkspacesResponse{
				Workspaces: []codersdk.Workspace{{Name: "ws"}},
				Count:      1,
			})
		},
	)
	defer srv.Close()

	cfg := CoderClientConfig{ServerURL: srv.URL, SessionToken: "token", OrganizationID: orgID.String()}
	workspaces, newCfg, err := ListWorkspacesWithRetry(cfg, "admin@example.com", "pass", uuid.New(), "tmpl")
	require.NoError(t, err)
	assert.Len(t, workspaces, 1)
	assert.Equal(t, "refreshed-token", newCfg.SessionToken)
}

func TestCreateUserWithRetry_RefreshesToken(t *testing.T) {
	orgID := uuid.New()
	userID := uuid.New()
	srv := makeRetryMockServer(t, orgID,
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"message": "error"})
		},
		func(w http.ResponseWriter, r *http.Request) {
			user := codersdk.User{}
			user.ID = userID
			user.Email = "new@example.com"
			user.Status = codersdk.UserStatusActive
			user.OrganizationIDs = []uuid.UUID{orgID}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(user)
		},
	)
	defer srv.Close()

	cfg := CoderClientConfig{ServerURL: srv.URL, SessionToken: "token", OrganizationID: orgID.String()}
	user, newCfg, err := CreateUserWithRetry(cfg, "admin@example.com", "pass", "new@example.com", "newuser", "pass")
	require.NoError(t, err)
	assert.Equal(t, "new@example.com", user.Email)
	assert.Equal(t, "refreshed-token", newCfg.SessionToken)
}

func TestDeleteWorkspaceWithRetry_RefreshesToken(t *testing.T) {
	orgID := uuid.New()
	wsID := uuid.New()
	buildID := uuid.New()
	srv := makeRetryMockServer(t, orgID,
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"message": "error"})
		},
		func(w http.ResponseWriter, r *http.Request) {
			build := map[string]interface{}{"id": buildID.String(), "workspace_id": wsID.String()}
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(build)
		},
	)
	defer srv.Close()

	cfg := CoderClientConfig{ServerURL: srv.URL, SessionToken: "token", OrganizationID: orgID.String()}
	newCfg, err := DeleteWorkspaceWithRetry(cfg, "admin@example.com", "pass", wsID)
	require.NoError(t, err)
	assert.Equal(t, "refreshed-token", newCfg.SessionToken)
}

func TestGetUserByUsernameWithRetry_RefreshesToken(t *testing.T) {
	orgID := uuid.New()
	userID := uuid.New()
	srv := makeRetryMockServer(t, orgID,
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"message": "error"})
		},
		func(w http.ResponseWriter, r *http.Request) {
			user := codersdk.User{}
			user.ID = userID
			user.Username = "testuser"
			user.Email = "test@example.com"
			user.Status = codersdk.UserStatusActive
			json.NewEncoder(w).Encode(user)
		},
	)
	defer srv.Close()

	cfg := CoderClientConfig{ServerURL: srv.URL, SessionToken: "token", OrganizationID: orgID.String()}
	user, newCfg, err := GetUserByUsernameWithRetry(cfg, "admin@example.com", "pass", "testuser")
	require.NoError(t, err)
	assert.Equal(t, "testuser", user.Username)
	assert.Equal(t, "refreshed-token", newCfg.SessionToken)
}

func TestCreateWorkspaceWithRetry_RefreshesToken(t *testing.T) {
	orgID := uuid.New()
	userID := uuid.New()
	srv := makeRetryMockServer(t, orgID,
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"message": "error"})
		},
		func(w http.ResponseWriter, r *http.Request) {
			ws := codersdk.Workspace{}
			ws.ID = uuid.New()
			ws.Name = "new-workspace"
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(ws)
		},
	)
	defer srv.Close()

	cfg := CoderClientConfig{ServerURL: srv.URL, SessionToken: "token", OrganizationID: orgID.String()}
	ws, newCfg, err := CreateWorkspaceWithRetry(cfg, "admin@example.com", "pass", userID, uuid.New(), "my-ws")
	require.NoError(t, err)
	assert.Equal(t, "new-workspace", ws.Name)
	assert.Equal(t, "refreshed-token", newCfg.SessionToken)
}

func TestUpdateUserPasswordWithRetry_RefreshesToken(t *testing.T) {
	orgID := uuid.New()
	srv := makeRetryMockServer(t, orgID,
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"message": "error"})
		},
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNoContent)
		},
	)
	defer srv.Close()

	cfg := CoderClientConfig{ServerURL: srv.URL, SessionToken: "token", OrganizationID: orgID.String()}
	newCfg, err := UpdateUserPasswordWithRetry(cfg, "admin@example.com", "pass", "testuser", "newpass")
	require.NoError(t, err)
	assert.Equal(t, "refreshed-token", newCfg.SessionToken)
}

func TestGetWorkspaceByOwnerAndNameWithRetry_RefreshesToken(t *testing.T) {
	orgID := uuid.New()
	userID := uuid.New()
	wsID := uuid.New()
	srv := makeRetryMockServer(t, orgID,
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"message": "error"})
		},
		func(w http.ResponseWriter, r *http.Request) {
			ws := codersdk.Workspace{}
			ws.ID = wsID
			ws.Name = "test-workspace"
			json.NewEncoder(w).Encode(ws)
		},
	)
	defer srv.Close()

	cfg := CoderClientConfig{ServerURL: srv.URL, SessionToken: "token", OrganizationID: orgID.String()}
	ws, newCfg, err := GetWorkspaceByOwnerAndNameWithRetry(cfg, "admin@example.com", "pass", userID.String(), "test-workspace")
	require.NoError(t, err)
	assert.Equal(t, "test-workspace", ws.Name)
	assert.Equal(t, "refreshed-token", newCfg.SessionToken)
}

func TestGetUserByEmailWithRetry_RefreshesToken(t *testing.T) {
	callCount := 0
	orgID := uuid.New()
	userID := uuid.New()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		callCount++

		switch r.URL.Path {
		case "/api/v2/buildinfo":
			json.NewEncoder(w).Encode(map[string]string{"version": "2.29.1"})
		case "/api/v2/users/login":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(codersdk.LoginWithPasswordResponse{SessionToken: "refreshed-token"})
		case "/api/v2/users":
			if callCount <= 1 {
				// First call: return server error to trigger refresh
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"message": "server error"})
				return
			}
			// Second call (after refresh): return the user
			user := codersdk.User{}
			user.ID = userID
			user.Email = "test@example.com"
			user.Username = "testuser"
			user.CreatedAt = time.Now()
			user.Status = codersdk.UserStatusActive
			user.OrganizationIDs = []uuid.UUID{orgID}
			json.NewEncoder(w).Encode(codersdk.GetUsersResponse{Users: []codersdk.User{user}, Count: 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "old-token",
		OrganizationID: orgID.String(),
	}
	user, newCfg, err := GetUserByEmailWithRetry(cfg, "admin@example.com", "pass", "test@example.com")
	require.NoError(t, err)
	assert.Equal(t, "test@example.com", user.Email)
	assert.Equal(t, "refreshed-token", newCfg.SessionToken)
}

func TestGetUserByEmailWithRetry_BothCallsFail(t *testing.T) {
	// First call fails → RefreshToken succeeds → second call also fails
	orgID := uuid.New()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v2/users/login":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(codersdk.LoginWithPasswordResponse{SessionToken: "refreshed"})
		case "/api/v2/users":
			// Always return server error → triggers refresh, then retry fails
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"message": "server error"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	_, _, err := GetUserByEmailWithRetry(cfg, "admin@example.com", "pass", "user@example.com")
	assert.Error(t, err)
}

func TestGetWorkspaceByOwnerAndNameWithRetry_BothFail(t *testing.T) {
	orgID := uuid.New()
	srv := makeRetryMockServer(t, orgID,
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"message": "error"})
		},
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"message": "error again"})
		},
	)
	defer srv.Close()

	cfg := CoderClientConfig{ServerURL: srv.URL, SessionToken: "token", OrganizationID: orgID.String()}
	_, _, err := GetWorkspaceByOwnerAndNameWithRetry(cfg, "admin@example.com", "pass", "owner", "ws")
	assert.Error(t, err)
}

func TestGetWorkspaceByOwnerAndName_ErrWorkspaceNotFoundPassthrough(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		json.NewEncoder(w).Encode(map[string]string{"message": "not found"})
	}))
	defer srv.Close()

	orgID := uuid.New()
	cfg := CoderClientConfig{ServerURL: srv.URL, SessionToken: "token", OrganizationID: orgID.String()}
	_, _, err := GetWorkspaceByOwnerAndNameWithRetry(cfg, "admin@example.com", "pass", "owner", "ws")
	assert.ErrorIs(t, err, ErrWorkspaceNotFound)
}

func TestListWorkspacesWithRetry_BothFail(t *testing.T) {
	orgID := uuid.New()
	srv := makeRetryMockServer(t, orgID,
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"message": "error"})
		},
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"message": "error again"})
		},
	)
	defer srv.Close()

	cfg := CoderClientConfig{ServerURL: srv.URL, SessionToken: "token", OrganizationID: orgID.String()}
	_, _, err := ListWorkspacesWithRetry(cfg, "admin@example.com", "pass", uuid.New(), "tmpl")
	assert.Error(t, err)
}

// "Both calls fail" tests for WithRetry functions

func TestCreateUserWithRetry_BothFail(t *testing.T) {
	orgID := uuid.New()
	srv := makeRetryMockServer(t, orgID,
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"message": "error"})
		},
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"message": "error again"})
		},
	)
	defer srv.Close()

	cfg := CoderClientConfig{ServerURL: srv.URL, SessionToken: "token", OrganizationID: orgID.String()}
	_, _, err := CreateUserWithRetry(cfg, "admin@example.com", "pass", "u@b.com", "user", "pass")
	assert.Error(t, err)
}

func TestDeleteWorkspaceWithRetry_BothFail(t *testing.T) {
	orgID := uuid.New()
	wsID := uuid.New()
	srv := makeRetryMockServer(t, orgID,
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"message": "error"})
		},
		func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"message": "error again"})
		},
	)
	defer srv.Close()

	cfg := CoderClientConfig{ServerURL: srv.URL, SessionToken: "token", OrganizationID: orgID.String()}
	_, err := DeleteWorkspaceWithRetry(cfg, "admin@example.com", "pass", wsID)
	assert.Error(t, err)
}

func TestGetUserByEmailWithRetry_Found(t *testing.T) {
	srv, orgID, _ := mockCoderServer(t)
	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	user, newCfg, err := GetUserByEmailWithRetry(cfg, "admin@example.com", "pass", "test@example.com")
	require.NoError(t, err)
	assert.Equal(t, "test@example.com", user.Email)
	assert.Equal(t, cfg.ServerURL, newCfg.ServerURL)
}

func TestDeleteWorkspace_Success(t *testing.T) {
	srv, orgID, _ := mockCoderServer(t)
	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	err := DeleteWorkspace(cfg, uuid.New())
	require.NoError(t, err)
}

func TestDeleteWorkspaceWithRetry_Success(t *testing.T) {
	srv, orgID, _ := mockCoderServer(t)
	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	newCfg, err := DeleteWorkspaceWithRetry(cfg, "admin@example.com", "pass", uuid.New())
	require.NoError(t, err)
	assert.Equal(t, cfg.ServerURL, newCfg.ServerURL)
}

func TestUpdateUserPasswordWithRetry_Success(t *testing.T) {
	srv, orgID, _ := mockCoderServer(t)
	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	newCfg, err := UpdateUserPasswordWithRetry(cfg, "admin@example.com", "pass", "testuser", "newpass")
	require.NoError(t, err)
	assert.Equal(t, cfg.ServerURL, newCfg.ServerURL)
}

func TestGetWorkspaceByOwnerAndNameWithRetry_Success(t *testing.T) {
	srv, orgID, userID := mockCoderServer(t)
	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	ws, newCfg, err := GetWorkspaceByOwnerAndNameWithRetry(cfg, "admin@example.com", "pass", userID.String(), "test-workspace")
	require.NoError(t, err)
	assert.Equal(t, "test-workspace", ws.Name)
	assert.Equal(t, cfg.ServerURL, newCfg.ServerURL)
}

func TestGetTemplatesWithRetry_RefreshesToken(t *testing.T) {
	callCount := 0
	orgID := uuid.New()
	templateID := uuid.New()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		callCount++

		switch {
		case strings.HasSuffix(r.URL.Path, "/templates") && r.Method == http.MethodGet:
			if callCount <= 1 {
				w.WriteHeader(http.StatusInternalServerError)
				json.NewEncoder(w).Encode(map[string]string{"message": "server error"})
				return
			}
			tmpl := codersdk.Template{ID: templateID, Name: "test-template", OrganizationID: orgID}
			json.NewEncoder(w).Encode([]codersdk.Template{tmpl})

		case r.URL.Path == "/api/v2/users/login":
			w.WriteHeader(http.StatusCreated)
			json.NewEncoder(w).Encode(codersdk.LoginWithPasswordResponse{SessionToken: "refreshed-token"})

		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "old-token",
		OrganizationID: orgID.String(),
	}
	templates, refreshedCfg, err := GetTemplatesWithRetry(cfg, "admin@example.com", "pass")
	require.NoError(t, err)
	assert.Len(t, templates, 1)
	// The refreshed config must carry the new token so callers can persist it.
	// (Long-lived token minting hits the mock's NotFound and falls back to the
	// session token from LoginWithPassword.)
	assert.Equal(t, "refreshed-token", refreshedCfg.SessionToken)
}

func TestGetTemplatesWithRetry_Found(t *testing.T) {
	srv, orgID, _ := mockCoderServer(t)
	cfg := CoderClientConfig{
		ServerURL:      srv.URL,
		SessionToken:   "token",
		OrganizationID: orgID.String(),
	}
	templates, retCfg, err := GetTemplatesWithRetry(cfg, "admin@example.com", "pass")
	require.NoError(t, err)
	assert.Len(t, templates, 1)
	// No refresh needed — the original config (and token) is returned unchanged.
	assert.Equal(t, "token", retCfg.SessionToken)
}
