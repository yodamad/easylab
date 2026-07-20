package devcontainer

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// Base kinds: what envbuilder will build the workspace image from.
const (
	BaseImage      = "image"
	BaseDockerfile = "dockerfile"
	// BaseNone is a devcontainer that names neither an image nor a Dockerfile —
	// it relies on the caller's fallback image (features may still apply).
	BaseNone = "none"
)

// ErrComposeUnsupported reports a docker-compose based devcontainer. envbuilder
// builds a single workspace image rather than orchestrating containers, so it
// cannot run these at all — this is a hard stop at import rather than a warning,
// because there is no degraded mode to fall back to.
var ErrComposeUnsupported = errors.New("docker-compose based devcontainer is not supported")

// Base describes the image envbuilder will produce.
type Base struct {
	Kind       string `json:"kind"`
	Image      string `json:"image,omitempty"`
	Dockerfile string `json:"dockerfile,omitempty"`
}

// Warning is a devcontainer key that neither envbuilder nor EasyLab honours.
// Warnings are surfaced to the admin at import so a workshop's expectations are
// corrected in the editor rather than on workshop day.
type Warning struct {
	Key     string `json:"key"`
	Message string `json:"message"`
}

// Result is the outcome of translating a devcontainer.json.
//
// It is deliberately not a server.WorkspaceTemplate: this package stays
// standalone (like internal/tfparse), and the server maps Result onto its own
// template type. Base and Features are informational — envbuilder reads them
// straight from the repo at build time, so they are reported to the admin but
// not copied into the template.
type Result struct {
	Name       string   `json:"name,omitempty"`
	Base       Base     `json:"base"`
	Features   []string `json:"features,omitempty"`
	Extensions []string `json:"extensions,omitempty"`
	CPU        string   `json:"cpu,omitempty"`
	Memory     string   `json:"memory,omitempty"`
	DiskSize   string   `json:"disk_size,omitempty"`
	// GitFolder is the subfolder the IDE should open, relative to the repo root.
	// It is only set when the devcontainer's workspaceFolder is itself relative —
	// see translateWorkspaceFolder.
	GitFolder string    `json:"git_folder,omitempty"`
	Warnings  []Warning `json:"warnings,omitempty"`
}

// Translate maps a parsed devcontainer.json onto the values EasyLab needs.
//
// The split is dictated by what envbuilder supports: it honours image, build.*,
// features, containerEnv/remoteEnv and the lifecycle commands itself, straight
// from the repo — so those are reported, not copied. What it ignores or only
// partially supports (sizing, extensions, workspace folder) is translated here.
// What nobody supports becomes a Warning.
func Translate(cfg *Config) (Result, error) {
	if len(cfg.DockerComposeFile) > 0 {
		return Result{}, fmt.Errorf("%w: dockerComposeFile references %s",
			ErrComposeUnsupported, strings.Join(cfg.DockerComposeFile, ", "))
	}

	res := Result{
		Name:     slugify(cfg.Name),
		Base:     baseOf(cfg),
		Features: featureNames(cfg),
	}

	folder, folderWarning := translateWorkspaceFolder(cfg.WorkspaceFolder)
	res.GitFolder = folder
	if folderWarning != nil {
		res.Warnings = append(res.Warnings, *folderWarning)
	}

	if cfg.Customizations != nil && cfg.Customizations.VSCode != nil {
		res.Extensions = append(res.Extensions, cfg.Customizations.VSCode.Extensions...)
	}

	if hr := cfg.HostRequirements; hr != nil {
		res.CPU = strings.TrimSpace(string(hr.CPUs))
		res.Memory = toK8sQuantity(hr.Memory)
		res.DiskSize = toK8sQuantity(hr.Storage)

		if hr.Memory != "" && res.Memory == "" {
			res.Warnings = append(res.Warnings, Warning{
				Key:     "hostRequirements.memory",
				Message: fmt.Sprintf("could not read %q as a size — set memory on the template by hand", hr.Memory),
			})
		}
		if hr.Storage != "" && res.DiskSize == "" {
			res.Warnings = append(res.Warnings, Warning{
				Key:     "hostRequirements.storage",
				Message: fmt.Sprintf("could not read %q as a size — set disk_size on the template by hand", hr.Storage),
			})
		}
	}

	res.Warnings = append(res.Warnings, unsupportedWarnings(cfg)...)
	return res, nil
}

// translateWorkspaceFolder maps the devcontainer's workspaceFolder onto the
// template's git_folder.
//
// The two mean different things: workspaceFolder is an absolute path inside the
// container ("/workspaces/repo/src"), while git_folder is a subfolder relative
// to the repo root. Only a relative workspaceFolder carries across cleanly. An
// absolute one is left alone rather than guessed at — EasyLab clones the repo to
// the IDE's own workspace directory, so the prefix a devcontainer assumes need
// not exist, and a wrong guess opens the IDE on an empty folder.
func translateWorkspaceFolder(workspaceFolder string) (string, *Warning) {
	wf := strings.TrimSpace(workspaceFolder)
	if wf == "" {
		return "", nil
	}
	if strings.HasPrefix(wf, "/") {
		return "", &Warning{
			Key: "workspaceFolder",
			Message: fmt.Sprintf(
				"%q is an absolute container path; the repo is cloned to the IDE's workspace directory instead — set git_folder if the IDE should open a subfolder",
				wf),
		}
	}
	return strings.TrimPrefix(wf, "./"), nil
}

// unsupportedWarnings reports every key present in the devcontainer that will
// not take effect. Only keys actually set produce a warning — an import of a
// clean devcontainer should be silent.
func unsupportedWarnings(cfg *Config) []Warning {
	var out []Warning
	add := func(key, msg string) { out = append(out, Warning{Key: key, Message: msg}) }

	if cfg.WorkspaceMount != "" {
		add("workspaceMount", "envbuilder does not support workspaceMount; the workspace lives on the template's disk_size volume instead")
	}
	if len(cfg.Mounts) > 0 {
		add("mounts", "envbuilder does not support devcontainer mounts; use the template's mounts: block to mount a ConfigMap or Secret")
	}
	if len(cfg.ForwardPorts) > 0 {
		add("forwardPorts", "forwarded ports are not exposed — only the IDE port is routed through the ingress")
	}
	if cfg.Privileged {
		add("privileged", "privileged is not applied to the workspace container; a privileged sidecar on the template is the supported route")
	}
	if len(cfg.CapAdd) > 0 {
		add("capAdd", "capAdd is not applied to the workspace container; use a sidecar with capabilities: on the template")
	}
	if cfg.Init {
		add("init", "init is not applied to the workspace container")
	}
	if cfg.RemoteUser != "" || cfg.ContainerUser != "" {
		add("remoteUser", "envbuilder's user handling diverges from the spec — check the IDE starts as the expected user")
	}
	if len(cfg.PostStartCommand) > 0 {
		add("postStartCommand", "postStartCommand does not run — move it to postCreateCommand, or to the template's startup_script, if it must happen before the IDE starts")
	}
	return out
}

// baseOf reports what envbuilder will build from, honouring the legacy
// root-level "dockerFile" spelling alongside build.dockerfile.
func baseOf(cfg *Config) Base {
	if cfg.Build != nil && strings.TrimSpace(cfg.Build.Dockerfile) != "" {
		return Base{Kind: BaseDockerfile, Dockerfile: strings.TrimSpace(cfg.Build.Dockerfile)}
	}
	if strings.TrimSpace(cfg.LegacyDockerfile) != "" {
		return Base{Kind: BaseDockerfile, Dockerfile: strings.TrimSpace(cfg.LegacyDockerfile)}
	}
	if strings.TrimSpace(cfg.Image) != "" {
		return Base{Kind: BaseImage, Image: strings.TrimSpace(cfg.Image)}
	}
	return Base{Kind: BaseNone}
}

// featureNames returns the devcontainer's feature refs in a stable order.
func featureNames(cfg *Config) []string {
	if len(cfg.Features) == 0 {
		return nil
	}
	names := make([]string, 0, len(cfg.Features))
	for name := range cfg.Features {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

var (
	slugInvalid = regexp.MustCompile(`[^a-z0-9]+`)
	slugTrim    = regexp.MustCompile(`^-+|-+$`)
)

// slugify turns a devcontainer's display name into a template name. Template
// names reach Kubernetes resource names via the workspace backend, so the result
// is restricted to what a DNS-1123 label allows.
func slugify(s string) string {
	out := slugInvalid.ReplaceAllString(strings.ToLower(strings.TrimSpace(s)), "-")
	out = slugTrim.ReplaceAllString(out, "")
	if len(out) > 40 {
		out = slugTrim.ReplaceAllString(out[:40], "")
	}
	return out
}
