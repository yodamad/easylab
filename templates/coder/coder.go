package coder

import (
	"bytes"
	"context"
	"fmt"
	"io"
	internalK8s "easylab/k8s"
	"easylab/utils"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/coder/coder/v2/codersdk"
	"github.com/google/uuid"
	k8s "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes"
	k8score "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/core/v1"
	helmv3 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/helm/v3"
	metav1 "github.com/pulumi/pulumi-kubernetes/sdk/v4/go/kubernetes/meta/v1"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

type CoderConfig struct {
	pulumi.CustomResourceState
	ServerURL      url.URL
	SessionToken   string
	OrganizationID uuid.UUID
}

// CoderConfigOutput is a Pulumi-compatible output struct for CoderConfig
type CoderConfigOutput struct {
	ServerURL      pulumi.StringOutput
	SessionToken   pulumi.StringOutput
	OrganizationID pulumi.StringOutput
}

// coderConfigValues is a helper struct to avoid lock issues when passing by value
type coderConfigValues struct {
	ServerURL      string
	SessionToken   string
	OrganizationID string
}

// SetupDB installs PostgreSQL via Helm and returns the release for dependency tracking
func SetupDB(ctx *pulumi.Context, k8sProvider *k8s.Provider, ns *k8score.Namespace) (*helmv3.Release, error) {

	dbUser := utils.CoderConfig(ctx, utils.CoderDbUser)
	dbPassword := utils.CoderConfig(ctx, utils.CoderDbPassword)
	dbName := utils.CoderConfig(ctx, utils.CoderDbName)

	helmValues := internalK8s.HelmChartInfo{
		Name:        "postgresql",
		ChartName:   "postgresql",
		Version:     "",
		Url:         "https://charts.bitnami.com/bitnami",
		ReleaseName: "postgresql",
		Values: pulumi.Map{
			"auth": pulumi.Map{
				"username": pulumi.String(dbUser),
				"password": pulumi.String(dbPassword),
				"database": pulumi.String(dbName),
			},
			"primary": pulumi.Map{
				"persistence": pulumi.Map{
					"size": pulumi.String("8Gi"),
				},
			},
		},
	}

	dbRelease, err := internalK8s.InitHelm(ctx, k8sProvider, helmValues, ns)
	if err != nil {
		return nil, fmt.Errorf("failed to setup PostgreSQL: %w", err)
	}

	return dbRelease, nil

}

// SetupDBSecret creates the database URL secret for Coder
// It depends on the namespace and optionally on the DB release for proper ordering
func SetupDBSecret(ctx *pulumi.Context, provider pulumi.ProviderResource, ns *k8score.Namespace, dbRelease ...*helmv3.Release) error {

	dbUser := utils.CoderConfig(ctx, utils.CoderDbUser)
	dbPassword := utils.CoderConfig(ctx, utils.CoderDbPassword)
	dbName := utils.CoderConfig(ctx, utils.CoderDbName)
	dbUrl := fmt.Sprintf("postgres://%s:%s@postgresql.coder.svc.cluster.local:5432/%s?sslmode=disable", dbUser, dbPassword, dbName)

	// Build dependencies list
	deps := []pulumi.Resource{ns}
	for _, rel := range dbRelease {
		if rel != nil {
			deps = append(deps, rel)
		}
	}

	_, err := k8score.NewSecret(ctx, "coder-db-url", &k8score.SecretArgs{
		Type: pulumi.String("Opaque"),
		Metadata: &metav1.ObjectMetaArgs{
			Namespace: ns.Metadata.Name(),
		},
		StringData: pulumi.StringMap{
			"url": pulumi.String(dbUrl),
		},
	}, pulumi.Provider(provider), pulumi.DependsOn(deps))
	if err != nil {
		return fmt.Errorf("failed to create coder-db-url secret: %w", err)
	}
	return nil
}

// InfrastructureResult holds the results of parallel infrastructure setup
type InfrastructureResult struct {
	DBRelease    *helmv3.Release
	CoderRelease *helmv3.Release
}

// SetupInfrastructureParallel sets up PostgreSQL and Coder Helm charts in parallel
// Both only depend on the namespace, so they can be installed concurrently
// This significantly reduces deployment time compared to sequential installation
func SetupInfrastructureParallel(ctx *pulumi.Context, k8sProvider *k8s.Provider, ns *k8score.Namespace) (*InfrastructureResult, error) {
	utils.LogInfo(ctx, "Setting up PostgreSQL and Coder in parallel...")

	// Setup PostgreSQL (doesn't block on Coder)
	dbRelease, dbErr := SetupDB(ctx, k8sProvider, ns)
	if dbErr != nil {
		return nil, fmt.Errorf("failed to setup PostgreSQL: %w", dbErr)
	}

	// Setup DB Secret (depends on namespace, will wait for DB at runtime)
	if err := SetupDBSecret(ctx, k8sProvider, ns, dbRelease); err != nil {
		return nil, fmt.Errorf("failed to setup DB secret: %w", err)
	}

	// Setup Coder (doesn't block on PostgreSQL installation, but will wait at runtime for DB)
	coderRelease, coderErr := SetupCoder(ctx, k8sProvider, ns)
	if coderErr != nil {
		return nil, fmt.Errorf("failed to setup Coder: %w", coderErr)
	}

	utils.LogInfo(ctx, "PostgreSQL and Coder Helm charts configured for parallel installation")

	return &InfrastructureResult{
		DBRelease:    dbRelease,
		CoderRelease: coderRelease,
	}, nil
}

func SetupCoder(ctx *pulumi.Context, k8sProvider *k8s.Provider, ns *k8score.Namespace) (*helmv3.Release, error) {
	helmValues := internalK8s.HelmChartInfo{
		Name:        "coder",
		ChartName:   "coder",
		Version:     utils.CoderConfig(ctx, utils.CoderVersion),
		Url:         "https://helm.coder.com/v2",
		ReleaseName: "coder", // Explicit name to avoid Pulumi-generated suffixes and Helm ownership conflicts
	}

	helmRelease, err := internalK8s.InitHelm(ctx, k8sProvider, helmValues, ns)
	if err != nil {
		return nil, err
	}
	return helmRelease, nil
}

func InitCoder(ctx *pulumi.Context, externalIp string) (CoderConfig, error) {
	serverURL := &url.URL{
		Scheme: "http",
		Host:   externalIp,
	}

	client := codersdk.New(serverURL)

	email := utils.CoderConfig(ctx, utils.CoderAdminEmail)
	password := utils.CoderConfig(ctx, utils.CoderAdminPassword)

	// Try to login first to check if first user already exists
	loginRes, err := client.LoginWithPassword(context.Background(), codersdk.LoginWithPasswordRequest{
		Email:    email,
		Password: password,
	})

	var organizationID uuid.UUID

	if err != nil {
		// Login failed, try to create the first user
		utils.LogInfo(ctx, "First user not found, creating first user...")
		createFirstUserRes, createErr := client.CreateFirstUser(context.Background(), codersdk.CreateFirstUserRequest{
			Email:    email,
			Username: "admin",
			Password: password,
		})
		if createErr != nil {
			utils.LogError(ctx, "failed to create first user: "+createErr.Error())
			return CoderConfig{}, fmt.Errorf("failed to create first user: %w", createErr)
		}

		utils.LogInfo(ctx, "First user created: "+createFirstUserRes.UserID.String())
		utils.LogInfo(ctx, "Organization ID: "+createFirstUserRes.OrganizationID.String())
		organizationID = createFirstUserRes.OrganizationID

		// Login with the newly created user
		loginRes, err = client.LoginWithPassword(context.Background(), codersdk.LoginWithPasswordRequest{
			Email:    email,
			Password: password,
		})
		if err != nil {
			utils.LogError(ctx, "failed to login with new user: "+err.Error())
			return CoderConfig{}, fmt.Errorf("failed to login with new user: %w", err)
		}
	} else {
		// Login succeeded, first user already exists
		utils.LogInfo(ctx, "First user already exists, skipping creation")

		// Get the authenticated user to retrieve organization ID
		authClient := codersdk.New(serverURL)
		authClient.SetSessionToken(loginRes.SessionToken)
		authUser, err := authClient.User(context.Background(), codersdk.Me)
		if err != nil {
			utils.LogError(ctx, "failed to get authenticated user: "+err.Error())
			return CoderConfig{}, fmt.Errorf("failed to get authenticated user: %w", err)
		}

		// Get organization ID from user's organization memberships
		if len(authUser.OrganizationIDs) > 0 {
			organizationID = authUser.OrganizationIDs[0]
			utils.LogInfo(ctx, "Using existing organization ID: "+organizationID.String())
		} else {
			// Fallback: try to get organizations
			orgs, err := authClient.OrganizationsByUser(context.Background(), authUser.ID.String())
			if err != nil || len(orgs) == 0 {
				utils.LogError(ctx, "failed to get organization: "+err.Error())
				return CoderConfig{}, fmt.Errorf("failed to get organization: %w", err)
			}
			organizationID = orgs[0].ID
			utils.LogInfo(ctx, "Using existing organization ID: "+organizationID.String())
		}
	}

	return CoderConfig{
		ServerURL:      *serverURL,
		SessionToken:   loginRes.SessionToken,
		OrganizationID: organizationID,
	}, nil
}

// InitCoderOutput initializes Coder and returns a Pulumi-compatible output struct
func InitCoderOutput(ctx *pulumi.Context, externalIp pulumi.StringOutput) *CoderConfigOutput {
	// Use ApplyT to transform the IP string into config values
	configOutput := externalIp.ApplyT(func(ip string) (coderConfigValues, error) {
		config, err := InitCoder(ctx, ip)
		if err != nil {
			return coderConfigValues{}, err
		}
		return coderConfigValues{
			ServerURL:      config.ServerURL.String(),
			SessionToken:   config.SessionToken,
			OrganizationID: config.OrganizationID.String(),
		}, nil
	})

	// Extract individual fields as outputs
	serverURLOutput := configOutput.ApplyT(func(c interface{}) string {
		return c.(coderConfigValues).ServerURL
	}).(pulumi.StringOutput)

	sessionTokenOutput := configOutput.ApplyT(func(c interface{}) string {
		return c.(coderConfigValues).SessionToken
	}).(pulumi.StringOutput)

	organizationIDOutput := configOutput.ApplyT(func(c interface{}) string {
		return c.(coderConfigValues).OrganizationID
	}).(pulumi.StringOutput)

	return &CoderConfigOutput{
		ServerURL:      serverURLOutput,
		SessionToken:   sessionTokenOutput,
		OrganizationID: organizationIDOutput,
	}
}

// CreateTemplateFromZip creates a new Coder template from an external zip file URL
// It accepts CoderConfigOutput and returns a pulumi.Output that resolves when the template is created
func CreateTemplateFromZip(ctx *pulumi.Context, coderInfos *CoderConfigOutput, templateName string, zipURL string) pulumi.Output {
	// Combine all outputs and extract actual values
	return pulumi.All(coderInfos.ServerURL, coderInfos.SessionToken, coderInfos.OrganizationID).ApplyT(func(args []interface{}) (interface{}, error) {
		serverURLStr := args[0].(string)
		sessionToken := args[1].(string)
		organizationIDStr := args[2].(string)

		// Parse the server URL
		serverURL, err := url.Parse(serverURLStr)
		if err != nil {
			utils.LogError(ctx, "failed to parse server URL: "+err.Error())
			return nil, fmt.Errorf("failed to parse server URL: %w", err)
		}

		// Parse the organization ID
		organizationID, err := uuid.Parse(organizationIDStr)
		if err != nil {
			utils.LogError(ctx, "failed to parse organization ID: "+err.Error())
			return nil, fmt.Errorf("failed to parse organization ID: %w", err)
		}

		// Create client with actual values
		client := codersdk.New(serverURL)
		client.SetSessionToken(sessionToken)

		// Read the zip file - support both file:// and http/https protocols
		var zipContent []byte

		if strings.HasPrefix(zipURL, "file://") {
			// Handle file:// protocol
			utils.LogInfo(ctx, "Reading template zip from file: "+zipURL)

			// Parse the file:// URL properly
			fileURL, parseErr := url.Parse(zipURL)
			if parseErr != nil {
				utils.LogError(ctx, "failed to parse file URL: "+parseErr.Error())
				return nil, fmt.Errorf("failed to parse file URL: %w", parseErr)
			}

			var filePath string

			// Handle different file:// URL formats:
			// - file:///absolute/path (three slashes - standard absolute path)
			// - file://C:/path (Windows drive letter in host)
			// - file://Users/... (incorrect format, should be file:///Users/...)
			if fileURL.Host != "" {
				// Check if it's a Windows drive letter (single character)
				if len(fileURL.Host) == 1 && fileURL.Path != "" && strings.HasPrefix(fileURL.Path, "/") {
					// Windows format: file://C:/path
					filePath = fileURL.Host + ":" + fileURL.Path
				} else {
					// Likely incorrect format like file://Users/... - treat host as part of path
					filePath = "/" + fileURL.Host + fileURL.Path
				}
			} else {
				// Standard format: file:///path
				filePath = fileURL.Path
			}

			// Ensure absolute path - if not absolute, make it absolute
			if !filepath.IsAbs(filePath) {
				var absPathErr error
				filePath, absPathErr = filepath.Abs(filePath)
				if absPathErr != nil {
					utils.LogError(ctx, "failed to resolve file path: "+absPathErr.Error())
					return nil, fmt.Errorf("failed to resolve file path: %w", absPathErr)
				}
			}
			var readErr error
			zipContent, readErr = os.ReadFile(filePath)
			if readErr != nil {
				utils.LogError(ctx, "failed to read zip file: "+readErr.Error())
				return nil, fmt.Errorf("failed to read zip file: %w", readErr)
			}
		} else {
			// Handle http/https protocol
			utils.LogInfo(ctx, "Downloading template zip from: "+zipURL)
			resp, httpErr := http.Get(zipURL)
			if httpErr != nil {
				utils.LogError(ctx, "failed to download zip file: "+httpErr.Error())
				return nil, fmt.Errorf("failed to download zip file: %w", httpErr)
			}
			defer resp.Body.Close()

			if resp.StatusCode != http.StatusOK {
				return nil, fmt.Errorf("failed to download zip file: HTTP %d", resp.StatusCode)
			}

			var readErr error
			zipContent, readErr = io.ReadAll(resp.Body)
			if readErr != nil {
				utils.LogError(ctx, "failed to read zip file content: "+readErr.Error())
				return nil, fmt.Errorf("failed to read zip file content: %w", readErr)
			}
		}

		// Upload the zip file to Coder
		utils.LogInfo(ctx, "Uploading template archive to Coder...")
		uploadResp, err := client.Upload(context.Background(), codersdk.ContentTypeZip, bytes.NewReader(zipContent))
		if err != nil {
			utils.LogError(ctx, "failed to upload template archive: "+err.Error())
			return nil, fmt.Errorf("failed to upload template archive: %w", err)
		}
		utils.LogInfo(ctx, "Template archive uploaded with hash: "+uploadResp.ID.String())

		// Create a template version from the uploaded file
		utils.LogInfo(ctx, "Creating template version...")
		templateVersion, err := client.CreateTemplateVersion(context.Background(), organizationID, codersdk.CreateTemplateVersionRequest{
			Name:          utils.CoderConfig(ctx, utils.CoderTemplateName),
			FileID:        uploadResp.ID,
			StorageMethod: codersdk.ProvisionerStorageMethodFile,
			Provisioner:   codersdk.ProvisionerTypeTerraform,
		})
		if err != nil {
			utils.LogError(ctx, "failed to create template version: "+err.Error())
			return nil, fmt.Errorf("failed to create template version: %w", err)
		}
		utils.LogInfo(ctx, "Template version created: "+templateVersion.ID.String())

		// Wait for the template version to be ready
		utils.LogInfo(ctx, "Waiting for template version to be processed...")
		err = waitForTemplateVersion(client, templateVersion.ID)
		if err != nil {
			utils.LogError(ctx, "template version failed: "+err.Error())
			return nil, fmt.Errorf("template version failed: %w", err)
		}

		// Create the template using the version
		utils.LogInfo(ctx, "Creating template: "+templateName)
		template, err := client.CreateTemplate(context.Background(), organizationID, codersdk.CreateTemplateRequest{
			Name:      templateName,
			VersionID: templateVersion.ID,
		})
		if err != nil {
			utils.LogError(ctx, "failed to create template: "+err.Error())
			return nil, fmt.Errorf("failed to create template: %w", err)
		}

		utils.LogInfo(ctx, "Template created successfully: "+template.Name+" (ID: "+template.ID.String()+")")
		return nil, nil
	})
}

// waitForTemplateVersion polls the template version status until it's ready or fails
func waitForTemplateVersion(client *codersdk.Client, versionID uuid.UUID) error {
	for {
		version, err := client.TemplateVersion(context.Background(), versionID)
		if err != nil {
			return fmt.Errorf("failed to get template version status: %w", err)
		}

		switch version.Job.Status {
		case codersdk.ProvisionerJobSucceeded:
			return nil
		case codersdk.ProvisionerJobFailed:
			return fmt.Errorf("template version job failed: %s", version.Job.Error)
		case codersdk.ProvisionerJobCanceled:
			return fmt.Errorf("template version job was canceled")
		}
		// Still pending/running, continue polling
		time.Sleep(2 * time.Second)
	}
}

// CoderClientConfig holds configuration for connecting to a Coder instance
type CoderClientConfig struct {
	ServerURL      string
	SessionToken   string
	OrganizationID string
}

// GetTemplates retrieves all available templates from a Coder instance
func GetTemplates(config CoderClientConfig) ([]codersdk.Template, error) {
	serverURL, err := url.Parse(config.ServerURL)
	if err != nil {
		return nil, fmt.Errorf("failed to parse server URL: %w", err)
	}

	organizationID, err := uuid.Parse(config.OrganizationID)
	if err != nil {
		return nil, fmt.Errorf("failed to parse organization ID: %w", err)
	}

	client := codersdk.New(serverURL)
	client.SetSessionToken(config.SessionToken)

	templates, err := client.TemplatesByOrganization(context.Background(), organizationID)
	if err != nil {
		return nil, fmt.Errorf("failed to get templates: %w", err)
	}

	return templates, nil
}

// CreateUser creates a new user in Coder with the given email and password
func CreateUser(config CoderClientConfig, email, username, password string) (codersdk.User, error) {
	serverURL, err := url.Parse(config.ServerURL)
	if err != nil {
		return codersdk.User{}, fmt.Errorf("failed to parse server URL: %w", err)
	}

	organizationID, err := uuid.Parse(config.OrganizationID)
	if err != nil {
		return codersdk.User{}, fmt.Errorf("failed to parse organization ID: %w", err)
	}

	client := codersdk.New(serverURL)
	client.SetSessionToken(config.SessionToken)

	// Create user request
	createReq := codersdk.CreateUserRequest{
		Email:          email,
		Username:       username,
		Password:       password,
		OrganizationID: organizationID,
	}

	user, err := client.CreateUser(context.Background(), createReq)
	if err != nil {
		return codersdk.User{}, fmt.Errorf("failed to create user: %w", err)
	}

	return user, nil
}

// CreateWorkspace creates a workspace for a user based on a template
func CreateWorkspace(config CoderClientConfig, userID uuid.UUID, templateID uuid.UUID, workspaceName string) (codersdk.Workspace, error) {
	serverURL, err := url.Parse(config.ServerURL)
	if err != nil {
		return codersdk.Workspace{}, fmt.Errorf("failed to parse server URL: %w", err)
	}

	organizationID, err := uuid.Parse(config.OrganizationID)
	if err != nil {
		return codersdk.Workspace{}, fmt.Errorf("failed to parse organization ID: %w", err)
	}

	client := codersdk.New(serverURL)
	client.SetSessionToken(config.SessionToken)

	// Create workspace request
	createReq := codersdk.CreateWorkspaceRequest{
		TemplateID: templateID,
		Name:       workspaceName,
	}

	workspace, err := client.CreateWorkspace(context.Background(), organizationID, userID.String(), createReq)
	if err != nil {
		return codersdk.Workspace{}, fmt.Errorf("failed to create workspace: %w", err)
	}

	return workspace, nil
}

// RefreshToken attempts to refresh the session token using admin credentials
// Returns a new CoderClientConfig with refreshed token, or error if refresh fails
func RefreshToken(config CoderClientConfig, adminEmail, adminPassword string) (CoderClientConfig, error) {
	serverURL, err := url.Parse(config.ServerURL)
	if err != nil {
		return CoderClientConfig{}, fmt.Errorf("failed to parse server URL: %w", err)
	}

	client := codersdk.New(serverURL)

	// Try to login with admin credentials
	loginRes, err := client.LoginWithPassword(context.Background(), codersdk.LoginWithPasswordRequest{
		Email:    adminEmail,
		Password: adminPassword,
	})
	if err != nil {
		return CoderClientConfig{}, fmt.Errorf("failed to refresh token: %w", err)
	}

	// Return updated config with new token
	return CoderClientConfig{
		ServerURL:      config.ServerURL,
		SessionToken:   loginRes.SessionToken,
		OrganizationID: config.OrganizationID,
	}, nil
}

// GetTemplatesWithRetry gets templates with automatic token refresh on failure
func GetTemplatesWithRetry(config CoderClientConfig, adminEmail, adminPassword string) ([]codersdk.Template, error) {
	templates, err := GetTemplates(config)
	if err != nil {
		// If it fails, try to refresh token and retry
		log.Printf("Template retrieval failed, attempting token refresh...")
		refreshedConfig, refreshErr := RefreshToken(config, adminEmail, adminPassword)
		if refreshErr != nil {
			return nil, fmt.Errorf("failed to refresh token: %w", refreshErr)
		}

		// Retry with refreshed token
		templates, err = GetTemplates(refreshedConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to get templates even after token refresh: %w", err)
		}

		// Return refreshed config as well (though not used in this context)
		log.Printf("Token refreshed successfully")
	}
	return templates, nil
}

// CreateUserWithRetry creates a user with automatic token refresh on failure
func CreateUserWithRetry(config CoderClientConfig, adminEmail, adminPassword string, email, username, password string) (codersdk.User, CoderClientConfig, error) {
	user, err := CreateUser(config, email, username, password)
	if err != nil {
		// If it fails, try to refresh token and retry
		log.Printf("User creation failed, attempting token refresh...")
		refreshedConfig, refreshErr := RefreshToken(config, adminEmail, adminPassword)
		if refreshErr != nil {
			return codersdk.User{}, config, fmt.Errorf("failed to refresh token: %w", refreshErr)
		}

		// Retry with refreshed token
		user, err = CreateUser(refreshedConfig, email, username, password)
		if err != nil {
			return codersdk.User{}, refreshedConfig, fmt.Errorf("failed to create user even after token refresh: %w", err)
		}

		log.Printf("Token refreshed successfully")
		return user, refreshedConfig, nil
	}
	return user, config, nil
}

// CreateWorkspaceWithRetry creates a workspace with automatic token refresh on failure
func CreateWorkspaceWithRetry(config CoderClientConfig, adminEmail, adminPassword string, userID uuid.UUID, templateID uuid.UUID, workspaceName string) (codersdk.Workspace, CoderClientConfig, error) {
	workspace, err := CreateWorkspace(config, userID, templateID, workspaceName)
	if err != nil {
		// If it fails, try to refresh token and retry
		log.Printf("Workspace creation failed, attempting token refresh...")
		refreshedConfig, refreshErr := RefreshToken(config, adminEmail, adminPassword)
		if refreshErr != nil {
			return codersdk.Workspace{}, config, fmt.Errorf("failed to refresh token: %w", refreshErr)
		}

		// Retry with refreshed token
		workspace, err = CreateWorkspace(refreshedConfig, userID, templateID, workspaceName)
		if err != nil {
			return codersdk.Workspace{}, refreshedConfig, fmt.Errorf("failed to create workspace even after token refresh: %w", err)
		}

		log.Printf("Token refreshed successfully")
		return workspace, refreshedConfig, nil
	}
	return workspace, config, nil
}

// AsyncTemplateConfig holds configuration for async template creation
type AsyncTemplateConfig struct {
	ServerURL      string
	SessionToken   string
	OrganizationID string
	TemplateName   string
	ZipFilePath    string
	LogCallback    func(message string)
}

// CreateTemplateAsync creates a Coder template asynchronously (non-blocking for Pulumi)
// This function is designed to be called after Pulumi completes infrastructure setup
// Returns immediately with a channel that will receive the result
func CreateTemplateAsync(config AsyncTemplateConfig) <-chan error {
	resultChan := make(chan error, 1)

	go func() {
		defer close(resultChan)

		logFunc := config.LogCallback
		if logFunc == nil {
			logFunc = func(msg string) { log.Println(msg) }
		}

		logFunc("[ASYNC_TEMPLATE] Starting async template creation...")

		// Parse server URL
		serverURL, err := url.Parse(config.ServerURL)
		if err != nil {
			logFunc(fmt.Sprintf("[ASYNC_TEMPLATE] Failed to parse server URL: %v", err))
			resultChan <- fmt.Errorf("failed to parse server URL: %w", err)
			return
		}

		// Parse organization ID
		organizationID, err := uuid.Parse(config.OrganizationID)
		if err != nil {
			logFunc(fmt.Sprintf("[ASYNC_TEMPLATE] Failed to parse organization ID: %v", err))
			resultChan <- fmt.Errorf("failed to parse organization ID: %w", err)
			return
		}

		// Create Coder client
		client := codersdk.New(serverURL)
		client.SetSessionToken(config.SessionToken)

		// Read the zip file
		logFunc(fmt.Sprintf("[ASYNC_TEMPLATE] Reading template zip from: %s", config.ZipFilePath))
		zipContent, err := os.ReadFile(config.ZipFilePath)
		if err != nil {
			logFunc(fmt.Sprintf("[ASYNC_TEMPLATE] Failed to read zip file: %v", err))
			resultChan <- fmt.Errorf("failed to read zip file: %w", err)
			return
		}

		// Upload the zip file to Coder
		logFunc("[ASYNC_TEMPLATE] Uploading template archive to Coder...")
		uploadResp, err := client.Upload(context.Background(), codersdk.ContentTypeZip, bytes.NewReader(zipContent))
		if err != nil {
			logFunc(fmt.Sprintf("[ASYNC_TEMPLATE] Failed to upload template archive: %v", err))
			resultChan <- fmt.Errorf("failed to upload template archive: %w", err)
			return
		}
		logFunc(fmt.Sprintf("[ASYNC_TEMPLATE] Template archive uploaded with hash: %s", uploadResp.ID.String()))

		// Create template version
		logFunc("[ASYNC_TEMPLATE] Creating template version...")
		templateVersion, err := client.CreateTemplateVersion(context.Background(), organizationID, codersdk.CreateTemplateVersionRequest{
			Name:          config.TemplateName,
			FileID:        uploadResp.ID,
			StorageMethod: codersdk.ProvisionerStorageMethodFile,
			Provisioner:   codersdk.ProvisionerTypeTerraform,
		})
		if err != nil {
			logFunc(fmt.Sprintf("[ASYNC_TEMPLATE] Failed to create template version: %v", err))
			resultChan <- fmt.Errorf("failed to create template version: %w", err)
			return
		}
		logFunc(fmt.Sprintf("[ASYNC_TEMPLATE] Template version created: %s", templateVersion.ID.String()))

		// Wait for template version to be ready
		logFunc("[ASYNC_TEMPLATE] Waiting for template version to be processed...")
		err = waitForTemplateVersionAsync(client, templateVersion.ID, logFunc)
		if err != nil {
			logFunc(fmt.Sprintf("[ASYNC_TEMPLATE] Template version failed: %v", err))
			resultChan <- fmt.Errorf("template version failed: %w", err)
			return
		}

		// Create the template
		logFunc(fmt.Sprintf("[ASYNC_TEMPLATE] Creating template: %s", config.TemplateName))
		template, err := client.CreateTemplate(context.Background(), organizationID, codersdk.CreateTemplateRequest{
			Name:      config.TemplateName,
			VersionID: templateVersion.ID,
		})
		if err != nil {
			logFunc(fmt.Sprintf("[ASYNC_TEMPLATE] Failed to create template: %v", err))
			resultChan <- fmt.Errorf("failed to create template: %w", err)
			return
		}

		logFunc(fmt.Sprintf("[ASYNC_TEMPLATE] Template created successfully: %s (ID: %s)", template.Name, template.ID.String()))
		resultChan <- nil
	}()

	return resultChan
}

// waitForTemplateVersionAsync polls the template version status with logging
func waitForTemplateVersionAsync(client *codersdk.Client, versionID uuid.UUID, logFunc func(string)) error {
	pollCount := 0
	for {
		version, err := client.TemplateVersion(context.Background(), versionID)
		if err != nil {
			return fmt.Errorf("failed to get template version status: %w", err)
		}

		switch version.Job.Status {
		case codersdk.ProvisionerJobSucceeded:
			return nil
		case codersdk.ProvisionerJobFailed:
			return fmt.Errorf("template version job failed: %s", version.Job.Error)
		case codersdk.ProvisionerJobCanceled:
			return fmt.Errorf("template version job was canceled")
		}

		// Log progress every 5 polls (10 seconds)
		pollCount++
		if pollCount%5 == 0 {
			logFunc(fmt.Sprintf("[ASYNC_TEMPLATE] Still processing template version (status: %s)...", version.Job.Status))
		}
		time.Sleep(2 * time.Second)
	}
}
