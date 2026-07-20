// Package devcontainer parses devcontainer.json files from workshop
// repositories and translates them into the pieces EasyLab needs to build a
// workspace template.
//
// It reads the subset of the Dev Container specification that matters here:
// what envbuilder will build (image / Dockerfile / features), and the keys
// envbuilder does not honour but EasyLab can (extensions, sizing, workspace
// folder). Keys nothing can honour are reported as warnings rather than
// silently dropped — see translate.go.
package devcontainer

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// ErrNotFound reports that a directory or archive contains no devcontainer.json.
var ErrNotFound = errors.New("no devcontainer.json found")

// ErrParse reports a devcontainer.json that is not valid JSON. The wrapped
// detail describes the admin's own file (offsets, unexpected tokens), so callers
// may safely surface it rather than replacing it with a generic message.
var ErrParse = errors.New("failed to parse devcontainer.json")

// Config is the subset of devcontainer.json EasyLab reads. Fields it neither
// translates nor warns about are omitted: encoding/json ignores unknown keys, so
// a workshop's devcontainer may contain anything else without failing the parse.
type Config struct {
	Name string `json:"name,omitempty"`

	// Base image selection. envbuilder builds all of these.
	Image string `json:"image,omitempty"`
	Build *Build `json:"build,omitempty"`
	// LegacyDockerfile is the pre-build.dockerfile spelling ("dockerFile" at the
	// document root), still found in older workshop repos.
	LegacyDockerfile string                     `json:"dockerFile,omitempty"`
	Features         map[string]json.RawMessage `json:"features,omitempty"`

	// Translated by EasyLab — envbuilder ignores or only partially honours these.
	Customizations   *Customizations   `json:"customizations,omitempty"`
	HostRequirements *HostRequirements `json:"hostRequirements,omitempty"`
	WorkspaceFolder  string            `json:"workspaceFolder,omitempty"`

	// Unsupported by envbuilder and by EasyLab — presence drives warnings.
	WorkspaceMount string            `json:"workspaceMount,omitempty"`
	Mounts         []json.RawMessage `json:"mounts,omitempty"`
	ForwardPorts   []json.RawMessage `json:"forwardPorts,omitempty"`
	RemoteUser     string            `json:"remoteUser,omitempty"`
	ContainerUser  string            `json:"containerUser,omitempty"`
	Privileged     bool              `json:"privileged,omitempty"`
	CapAdd         []string          `json:"capAdd,omitempty"`
	Init           bool              `json:"init,omitempty"`
	// PostStartCommand is read for presence only. envbuilder exposes it through
	// ENVBUILDER_POST_START_SCRIPT rather than running it, and EasyLab does not
	// wire that up yet, so a declared postStartCommand does not run — which the
	// admin is warned about instead of finding out mid-workshop.
	PostStartCommand json.RawMessage `json:"postStartCommand,omitempty"`

	// Compose-based devcontainers are rejected outright: envbuilder builds an
	// image rather than orchestrating containers, so it cannot run them.
	DockerComposeFile StringOrSlice `json:"dockerComposeFile,omitempty"`
	Service           string        `json:"service,omitempty"`
	RunServices       []string      `json:"runServices,omitempty"`
}

// Build is the devcontainer "build" object.
type Build struct {
	Dockerfile string         `json:"dockerfile,omitempty"`
	Context    string         `json:"context,omitempty"`
	Target     string         `json:"target,omitempty"`
	Args       map[string]any `json:"args,omitempty"`
}

// Customizations carries tool-specific settings; EasyLab reads the VS Code
// extension list, which envbuilder only partially supports.
type Customizations struct {
	VSCode *VSCodeCustomizations `json:"vscode,omitempty"`
}

// VSCodeCustomizations is the customizations.vscode object.
type VSCodeCustomizations struct {
	Extensions []string `json:"extensions,omitempty"`
}

// HostRequirements is the devcontainer sizing hint. envbuilder ignores it; it
// maps onto the workspace template's cpu/memory/disk_size.
type HostRequirements struct {
	CPUs    FlexString `json:"cpus,omitempty"`
	Memory  string     `json:"memory,omitempty"`
	Storage string     `json:"storage,omitempty"`
}

// FlexString is a string that also accepts a JSON number, for keys the spec
// types loosely (hostRequirements.cpus is a number, but string is seen in the wild).
type FlexString string

// UnmarshalJSON accepts either a JSON string or a JSON number.
func (f *FlexString) UnmarshalJSON(b []byte) error {
	b = trimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*f = ""
		return nil
	}
	if b[0] == '"' {
		var s string
		if err := json.Unmarshal(b, &s); err != nil {
			return err
		}
		*f = FlexString(s)
		return nil
	}
	// A bare number: keep its literal form so "2" stays "2" and not "2.000000".
	var n json.Number
	if err := json.Unmarshal(b, &n); err != nil {
		return fmt.Errorf("expected a string or number, got %s", string(b))
	}
	*f = FlexString(n.String())
	return nil
}

// StringOrSlice is a value the spec allows to be either a single string or a
// list of strings (e.g. dockerComposeFile).
type StringOrSlice []string

// UnmarshalJSON accepts either a JSON string or a JSON array of strings.
func (s *StringOrSlice) UnmarshalJSON(b []byte) error {
	b = trimSpace(b)
	if len(b) == 0 || string(b) == "null" {
		*s = nil
		return nil
	}
	if b[0] == '"' {
		var one string
		if err := json.Unmarshal(b, &one); err != nil {
			return err
		}
		*s = StringOrSlice{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return fmt.Errorf("expected a string or list of strings, got %s", string(b))
	}
	*s = StringOrSlice(many)
	return nil
}

func trimSpace(b []byte) []byte {
	return []byte(strings.TrimSpace(string(b)))
}

// Parse decodes a devcontainer.json document. The file format is JSONC —
// comments and trailing commas are permitted and common in the wild — so the
// document is normalised before encoding/json sees it.
func Parse(content []byte) (*Config, error) {
	var cfg Config
	if err := json.Unmarshal(StripJSONC(content), &cfg); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrParse, err)
	}
	return &cfg, nil
}

// ParseFromFile reads and parses a devcontainer.json file.
func ParseFromFile(path string) (*Config, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("failed to read %s: %w", filepath.Base(path), err)
	}
	return Parse(content)
}

// ParseFromZip finds and parses the devcontainer.json inside a .zip archive of
// a repository, applying the same precedence as FindInDir, and returns the
// archive-relative path it used. Archives produced by "git archive" or a forge's
// "download zip" wrap everything in a single top level directory, so entries are
// matched on their path suffix.
func ParseFromZip(zipPath string) (*Config, string, error) {
	r, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, "", fmt.Errorf("failed to open zip: %w", err)
	}
	defer r.Close()

	names := make([]string, 0, len(r.File))
	byName := make(map[string]*zip.File, len(r.File))
	for _, f := range r.File {
		if f.FileInfo().IsDir() {
			continue
		}
		name := path.Clean(filepath.ToSlash(f.Name))
		names = append(names, name)
		byName[name] = f
	}

	match := pickCandidate(names)
	if match == "" {
		return nil, "", ErrNotFound
	}

	rc, err := byName[match].Open()
	if err != nil {
		return nil, "", fmt.Errorf("failed to open %s in zip: %w", match, err)
	}
	defer rc.Close()

	content, err := io.ReadAll(rc)
	if err != nil {
		return nil, "", fmt.Errorf("failed to read %s in zip: %w", match, err)
	}

	cfg, err := Parse(content)
	if err != nil {
		return nil, "", err
	}
	return cfg, match, nil
}

// pickCandidate chooses the devcontainer.json to use from a set of slash
// separated paths, in the spec's precedence order: the canonical
// .devcontainer/devcontainer.json, then the root .devcontainer.json, then the
// first .devcontainer/<subfolder>/devcontainer.json in lexical order.
func pickCandidate(names []string) string {
	sort.Strings(names)

	var nested string
	var rootLevel string
	for _, n := range names {
		switch {
		case hasSuffixPath(n, ".devcontainer/devcontainer.json"):
			return n
		case hasSuffixPath(n, ".devcontainer.json") && !strings.Contains(n, "/.devcontainer/"):
			if rootLevel == "" {
				rootLevel = n
			}
		case strings.Contains(n, "/.devcontainer/") && strings.HasSuffix(n, "/devcontainer.json"),
			strings.HasPrefix(n, ".devcontainer/") && strings.HasSuffix(n, "/devcontainer.json"):
			if nested == "" {
				nested = n
			}
		}
	}
	if rootLevel != "" {
		return rootLevel
	}
	return nested
}

// hasSuffixPath reports whether name ends with suffix on a path-segment boundary,
// so "x/.devcontainer/devcontainer.json" matches but "my.devcontainer.json" does not.
func hasSuffixPath(name, suffix string) bool {
	return name == suffix || strings.HasSuffix(name, "/"+suffix)
}

// FindInDir locates the devcontainer.json inside a checked-out repository,
// in the spec's precedence order. It returns ErrNotFound when there is none.
func FindInDir(root string) (string, error) {
	for _, rel := range []string{
		filepath.Join(".devcontainer", "devcontainer.json"),
		".devcontainer.json",
	} {
		p := filepath.Join(root, rel)
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	// .devcontainer/<subfolder>/devcontainer.json — a repo offering several
	// variants. Lexically first wins; the admin can edit the result.
	matches, err := filepath.Glob(filepath.Join(root, ".devcontainer", "*", "devcontainer.json"))
	if err == nil && len(matches) > 0 {
		sort.Strings(matches)
		return matches[0], nil
	}
	return "", ErrNotFound
}

// StripJSONC normalises a JSONC document into JSON that encoding/json accepts:
// it removes // and /* */ comments and commas that trail the last element of an
// object or array. Comment markers and commas inside string literals are left
// untouched, so a value like "https://example.com" survives intact.
func StripJSONC(src []byte) []byte {
	return stripTrailingCommas(stripComments(src))
}

func stripComments(src []byte) []byte {
	out := make([]byte, 0, len(src))
	inString, escaped := false, false

	for i := 0; i < len(src); i++ {
		c := src[i]

		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}

		switch {
		case c == '"':
			inString = true
			out = append(out, c)
		case c == '/' && i+1 < len(src) && src[i+1] == '/':
			// Line comment: drop through end of line, keeping the newline so
			// json's error offsets stay on the right line.
			for i < len(src) && src[i] != '\n' {
				i++
			}
			if i < len(src) {
				out = append(out, '\n')
			}
		case c == '/' && i+1 < len(src) && src[i+1] == '*':
			i += 2
			for i+1 < len(src) && !(src[i] == '*' && src[i+1] == '/') {
				i++
			}
			i++ // land on the closing '/', the loop's i++ steps past it
		default:
			out = append(out, c)
		}
	}
	return out
}

func stripTrailingCommas(src []byte) []byte {
	out := make([]byte, 0, len(src))
	inString, escaped := false, false

	for i, c := range src {
		if inString {
			out = append(out, c)
			switch {
			case escaped:
				escaped = false
			case c == '\\':
				escaped = true
			case c == '"':
				inString = false
			}
			continue
		}

		if c == '"' {
			inString = true
			out = append(out, c)
			continue
		}

		if c == ',' && closesNext(src, i+1) {
			continue
		}
		out = append(out, c)
	}
	return out
}

// closesNext reports whether the next non-whitespace byte from i closes an
// object or array, which makes a comma at i-1 a trailing comma.
func closesNext(src []byte, i int) bool {
	for ; i < len(src); i++ {
		switch src[i] {
		case ' ', '\t', '\r', '\n':
			continue
		case '}', ']':
			return true
		default:
			return false
		}
	}
	return false
}

// memoryPattern matches the devcontainer sizing spelling ("4gb", "512mb", "1.5tb").
var memoryPattern = regexp.MustCompile(`^(\d+(?:\.\d+)?)\s*([kmgt]b)?$`)

// toK8sQuantity converts a devcontainer hostRequirements size ("4gb") into a
// Kubernetes quantity ("4Gi"). The binary suffix is a deliberate round up: it
// grants slightly more than asked, which is the safe direction for a workshop.
// It returns "" for anything unparseable, so a malformed hint is skipped rather
// than producing a Deployment the API server rejects.
func toK8sQuantity(s string) string {
	m := memoryPattern.FindStringSubmatch(strings.ToLower(strings.TrimSpace(s)))
	if m == nil {
		return ""
	}
	switch m[2] {
	case "kb":
		return m[1] + "Ki"
	case "mb":
		return m[1] + "Mi"
	case "gb":
		return m[1] + "Gi"
	case "tb":
		return m[1] + "Ti"
	default:
		return m[1]
	}
}
