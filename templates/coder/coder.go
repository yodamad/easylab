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

func SetupDB(ctx *pulumi.Context, k8sProvider *k8s.Provider, ns *k8score.Namespace) internalK8s.HelmChartInfo {

	dbUser := utils.CoderConfig(ctx, utils.CoderDbUser)
	dbPassword := utils.CoderConfig(ctx, utils.CoderDbPassword)
	dbName := utils.CoderConfig(ctx, utils.CoderDbName)

	helmValues := internalK8s.HelmChartInfo{
		Name:      "postgresql",
		ChartName: "postgresql",
		Version:   "",
		Url:       "https://charts.bitnami.com/bitnami",
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

	_, err := internalK8s.InitHelm(ctx, k8sProvider, helmValues, ns)
	if err != nil {
		return internalK8s.HelmChartInfo{}
	}

	return helmValues

}

func SetupDBSecret(ctx *pulumi.Context, provider pulumi.ProviderResource, ns *k8score.Namespace) {

	dbUser := utils.CoderConfig(ctx, utils.CoderDbUser)
	dbPassword := utils.CoderConfig(ctx, utils.CoderDbPassword)
	dbName := utils.CoderConfig(ctx, utils.CoderDbName)
	dbUrl := fmt.Sprintf("postgres://%s:%s@postgresql.coder.svc.cluster.local:5432/%s?sslmode=disable", dbUser, dbPassword, dbName)

	_, err := k8score.NewSecret(ctx, "coder-db-url", &k8score.SecretArgs{
		Type: pulumi.String("Opaque"),
		Metadata: &metav1.ObjectMetaArgs{
			Namespace: ns.Metadata.Name(),
		},
		StringData: pulumi.StringMap{
			"url": pulumi.String(dbUrl),
		},
	}, pulumi.Provider(provider), pulumi.DependsOn([]pulumi.Resource{ns}))
	if err != nil {
		_ = ctx.Log.Error("failed to create coder-db-url secret: "+err.Error(), nil)
	}
}

func SetupCoder(ctx *pulumi.Context, k8sProvider *k8s.Provider, ns *k8score.Namespace) (*helmv3.Release, error) {
	helmValues := internalK8s.HelmChartInfo{
		Name:      "coder",
		ChartName: "coder",
		Version:   utils.CoderConfig(ctx, utils.CoderVersion),
		Url:       "https://helm.coder.com/v2",
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
