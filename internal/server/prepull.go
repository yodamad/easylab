package server

import (
	"context"
	"log"
	"maps"
	"strings"

	"easylab/internal/providers/workspace"
	"easylab/internal/providers/workspace/kube"
)

// prepullRequestFor lists every image a workspace pod of a lab can run, so the
// lab's nodes can pull them before the first student asks for a workspace. It
// mirrors what the kube backend puts in a workspace pod for each template: the
// IDE image, sidecars, the git-clone init container, and for devcontainer
// templates the IDE bundle plus either the baked image or envbuilder.
func prepullRequestFor(templates []WorkspaceTemplate, baked map[string]BakedImage) workspace.PrepullRequest {
	var req workspace.PrepullRequest
	for i, t := range templates {
		image := strings.TrimSpace(t.Image)
		if t.Devcontainer != nil && t.Devcontainer.Enabled {
			// The workspace container runs the baked image, or envbuilder when the
			// template has not been baked; the IDE image still runs as ide-inject.
			if b, ok := baked[t.Name]; ok && b.Image != "" {
				image = b.Image
			} else {
				image = kube.EnvbuilderImage
			}
			req.Images = append(req.Images, kube.DefaultImage)
		} else if image == "" {
			image = kube.DefaultImage
		}
		req.Images = append(req.Images, image)

		if strings.TrimSpace(t.GitRepo) != "" {
			req.Images = append(req.Images, kube.GitCloneImage)
		}
		for _, sc := range t.Sidecars {
			req.Images = append(req.Images, sc.Image)
		}
		req.PullSecrets = append(req.PullSecrets, t.ImagePullSecrets...)

		// A node selector only narrows the pre-pull when every template agrees on
		// it; otherwise workspaces can land anywhere, so every node pulls.
		if i == 0 {
			req.NodeSelector = maps.Clone(t.NodeSelector)
		} else if !maps.Equal(req.NodeSelector, t.NodeSelector) {
			req.NodeSelector = nil
		}
	}
	return req
}

// reconcilePrepull brings a lab's image pre-pull in line with its current
// templates. Best-effort: a failure only means the first students on a node wait
// for the pull, as they would without it, so it is logged and otherwise ignored.
func (h *Handler) reconcilePrepull(labID string) {
	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		return
	}
	job.mu.RLock()
	kubeconfig := extractStringFromConfigValue(job.Kubeconfig)
	namespace := job.workspaceNamespace()
	var req workspace.PrepullRequest
	if job.Config != nil {
		req = prepullRequestFor(job.Config.WorkspaceTemplates, job.Config.BakedImages)
	}
	job.mu.RUnlock()
	if kubeconfig == "" {
		return
	}

	backend, err := h.workspaceBackendFor(labID, kubeconfig, namespace)
	if err != nil {
		log.Printf("[prepull] skipping lab %s: failed to build backend: %v", labID, err)
		return
	}
	pp, ok := backend.(workspace.ImagePrepuller)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), clusterOperationTimeout)
	defer cancel()
	if err := pp.EnsurePrepull(ctx, labID, req); err != nil {
		log.Printf("[prepull] failed to reconcile image pre-pull for lab %s: %v", labID, err)
	}
}

// reconcilePrepulls runs reconcilePrepull for every completed lab whose cluster
// answers. It rides the cleanup ticker so templates set through any path (the
// creation wizard, a YAML import, an edit) and nodes added since are covered
// without a hook at every place a template can change.
func (h *Handler) reconcilePrepulls() {
	for _, job := range h.jobManager.GetAllJobs() {
		job.mu.RLock()
		completed := job.Status == JobStatusCompleted
		kubeconfig := extractStringFromConfigValue(job.Kubeconfig)
		namespace := job.workspaceNamespace()
		job.mu.RUnlock()
		if !completed || kubeconfig == "" {
			continue
		}
		backend, err := h.workspaceBackendFor(job.ID, kubeconfig, namespace)
		if err != nil {
			continue
		}
		reachCtx, cancel := context.WithTimeout(context.Background(), clusterReachabilityTimeout)
		reachable := backend.Reachable(reachCtx)
		cancel()
		if !reachable {
			continue
		}
		h.reconcilePrepull(job.ID)
	}
}

// removePrepull deletes a lab's image pre-pull, best-effort. Called before a lab is
// destroyed so a BYO cluster, which outlives the lab, is not left pulling for it.
func (h *Handler) removePrepull(labID string) {
	job, exists := h.jobManager.GetJob(labID)
	if !exists {
		return
	}
	job.mu.RLock()
	kubeconfig := extractStringFromConfigValue(job.Kubeconfig)
	namespace := job.workspaceNamespace()
	job.mu.RUnlock()
	if kubeconfig == "" {
		return
	}

	backend, err := h.workspaceBackendFor(labID, kubeconfig, namespace)
	if err != nil {
		log.Printf("[prepull] cannot remove image pre-pull for lab %s: %v", labID, err)
		return
	}
	pp, ok := backend.(workspace.ImagePrepuller)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), clusterOperationTimeout)
	defer cancel()
	if err := pp.RemovePrepull(ctx, labID); err != nil {
		log.Printf("[prepull] failed to remove image pre-pull for lab %s: %v", labID, err)
	}
}
