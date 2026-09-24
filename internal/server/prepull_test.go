package server

import (
	"testing"

	"easylab/internal/providers/workspace/kube"

	"github.com/stretchr/testify/assert"
)

func TestPrepullRequestFor(t *testing.T) {
	t.Parallel()
	pool := map[string]string{"pool": "workspaces"}
	tests := []struct {
		name             string
		templates        []WorkspaceTemplate
		baked            map[string]BakedImage
		expectedImages   []string
		expectedSecrets  []string
		expectedSelector map[string]string
	}{
		{
			name:           "no templates",
			expectedImages: nil,
		},
		{
			name:           "default image",
			templates:      []WorkspaceTemplate{{Name: "t"}},
			expectedImages: []string{kube.DefaultImage},
		},
		{
			name: "custom image, git repo and sidecars",
			templates: []WorkspaceTemplate{{
				Name:             "t",
				Image:            "my/ide:1",
				GitRepo:          "https://gitlab.com/org/repo.git",
				Sidecars:         []WorkspaceSidecar{{Name: "db", Image: "postgres:16"}},
				ImagePullSecrets: []string{"regcred"},
			}},
			expectedImages:  []string{"my/ide:1", kube.GitCloneImage, "postgres:16"},
			expectedSecrets: []string{"regcred"},
		},
		{
			name: "unbaked devcontainer runs envbuilder",
			templates: []WorkspaceTemplate{{
				Name: "dc", GitRepo: "https://gitlab.com/org/repo.git",
				Devcontainer: &DevcontainerConfig{Enabled: true},
			}},
			expectedImages: []string{kube.DefaultImage, kube.EnvbuilderImage, kube.GitCloneImage},
		},
		{
			name: "baked devcontainer runs its baked image",
			templates: []WorkspaceTemplate{{
				Name: "dc", GitRepo: "https://gitlab.com/org/repo.git",
				Devcontainer: &DevcontainerConfig{Enabled: true},
			}},
			baked:          map[string]BakedImage{"dc": {Image: "registry.example.com/baked/dc@sha256:abc"}},
			expectedImages: []string{kube.DefaultImage, "registry.example.com/baked/dc@sha256:abc", kube.GitCloneImage},
		},
		{
			name: "disabled devcontainer is a plain template",
			templates: []WorkspaceTemplate{{
				Name: "t", Devcontainer: &DevcontainerConfig{Enabled: false},
			}},
			expectedImages: []string{kube.DefaultImage},
		},
		{
			name: "shared node selector is kept",
			templates: []WorkspaceTemplate{
				{Name: "a", NodeSelector: pool},
				{Name: "b", NodeSelector: map[string]string{"pool": "workspaces"}},
			},
			expectedImages:   []string{kube.DefaultImage, kube.DefaultImage},
			expectedSelector: pool,
		},
		{
			name: "differing node selectors pull everywhere",
			templates: []WorkspaceTemplate{
				{Name: "a", NodeSelector: pool},
				{Name: "b"},
			},
			expectedImages:   []string{kube.DefaultImage, kube.DefaultImage},
			expectedSelector: nil,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := prepullRequestFor(tt.templates, tt.baked)
			// Duplicates are left for the backend to collapse; order is what matters here.
			assert.Equal(t, tt.expectedImages, req.Images)
			assert.Equal(t, tt.expectedSecrets, req.PullSecrets)
			if tt.expectedSelector == nil {
				assert.Empty(t, req.NodeSelector)
			} else {
				assert.Equal(t, tt.expectedSelector, req.NodeSelector)
			}
		})
	}
}
