package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ownedTemplatesTimeout bounds the cluster lookup that decides whether a student
// may still see a closed lab or template. It runs while a student loads the
// portal, so an unreachable cluster must not hang the page.
const ownedTemplatesTimeout = 5 * time.Second

// withTemplateDisabled returns a copy of disabled with name set or cleared. It
// never mutates its argument — see LabConfig.DisabledTemplates — and returns nil
// rather than an empty map so the field stays out of the persisted JSON.
func withTemplateDisabled(disabled map[string]bool, name string, value bool) map[string]bool {
	next := make(map[string]bool, len(disabled)+1)
	for k, v := range disabled {
		if v && k != name {
			next[k] = true
		}
	}
	if value {
		next[name] = true
	}
	if len(next) == 0 {
		return nil
	}
	return next
}

// parseDisabledForm reads the "disabled" form field of an availability request.
func parseDisabledForm(r *http.Request) (bool, error) {
	if err := r.ParseForm(); err != nil {
		return false, fmt.Errorf("failed to parse form: %w", err)
	}
	disabled, err := strconv.ParseBool(strings.TrimSpace(r.FormValue("disabled")))
	if err != nil {
		return false, fmt.Errorf("failed to parse disabled flag: %w", err)
	}
	return disabled, nil
}

// writeAvailability writes the JSON success body shared by both availability
// endpoints.
func writeAvailability(w http.ResponseWriter, disabled bool) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "disabled": disabled})
}

// SetLabAvailability closes a lab to new students, or reopens it
// (POST api/labs/{id}/availability, disabled=true|false). Closing deletes
// nothing: existing workspaces keep running and their owners keep access.
func (h *Handler) SetLabAvailability(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}

	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	// Expect: api/labs/{id}/availability or api/jobs/{id}/availability
	if len(pathParts) != 4 || (pathParts[1] != "labs" && pathParts[1] != "jobs") {
		writeJSONError(w, http.StatusBadRequest, "Invalid path")
		return
	}
	jobID := pathParts[2]

	if _, exists := h.jobManager.GetJob(jobID); !exists {
		writeJSONError(w, http.StatusNotFound, "Lab not found")
		return
	}

	disabled, err := parseDisabledForm(r)
	if err != nil {
		log.Printf("SetLabAvailability: invalid request for lab %s: %v", jobID, err)
		writeJSONError(w, http.StatusBadRequest, "disabled must be true or false")
		return
	}

	updated := false
	h.updateJobConfig(jobID, func(config *LabConfig) {
		config.Disabled = disabled
		updated = true
	})
	if !updated {
		writeJSONError(w, http.StatusConflict, "Lab has no configuration")
		return
	}
	// Off the response's critical path — see the comment on the SaveJob call in
	// UploadTemplateToLab for why this is safe to run async.
	go func() {
		if err := h.jobManager.SaveJob(jobID); err != nil {
			log.Printf("Failed to persist availability change for lab %s: %v", jobID, err)
		}
	}()

	action := "lab.enable"
	if disabled {
		action = "lab.disable"
	}
	h.recordAudit(adminActor(r), "admin", action, jobID, "")

	writeAvailability(w, disabled)
}

// SetTemplateAvailability closes one template of a lab to new students, or
// reopens it (POST api/labs/{id}/templates/{name}/availability,
// disabled=true|false). Unlike RemoveTemplateFromLab it deletes no workspace.
func (h *Handler) SetTemplateAvailability(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "Method not allowed")
		return
	}
	jobID, templateName, ok := bakePathParts(w, r)
	if !ok {
		return
	}

	if _, exists := h.jobManager.GetJob(jobID); !exists {
		writeJSONError(w, http.StatusNotFound, "Lab not found")
		return
	}

	disabled, err := parseDisabledForm(r)
	if err != nil {
		log.Printf("SetTemplateAvailability: invalid request for lab %s: %v", jobID, err)
		writeJSONError(w, http.StatusBadRequest, "disabled must be true or false")
		return
	}

	found := false
	h.updateJobConfig(jobID, func(config *LabConfig) {
		for _, t := range config.GetWorkspaceTemplates() {
			if t.Name == templateName {
				found = true
				break
			}
		}
		if !found {
			return
		}
		config.DisabledTemplates = withTemplateDisabled(config.DisabledTemplates, templateName, disabled)
	})
	if !found {
		writeJSONError(w, http.StatusNotFound, "Template not found")
		return
	}
	// Off the response's critical path — see the comment on the SaveJob call in
	// UploadTemplateToLab for why this is safe to run async.
	go func() {
		if err := h.jobManager.SaveJob(jobID); err != nil {
			log.Printf("Failed to persist template availability change for lab %s: %v", jobID, err)
		}
	}()

	action := "lab.template_enable"
	if disabled {
		action = "lab.template_disable"
	}
	h.recordAudit(adminActor(r), "admin", action, jobID, templateName)

	writeAvailability(w, disabled)
}

// ownedTemplates returns the names of the templates the student already has a
// workspace on in this lab. It is what lets a student keep reaching a workspace
// on a lab or template that has since been closed to new students.
//
// It asks the lab's cluster, so callers only invoke it when something is
// actually closed. A lab without a kubeconfig has no workspaces to own.
func (h *Handler) ownedTemplates(ctx context.Context, labID, kubeconfig, namespace, email string) (map[string]bool, error) {
	owned := make(map[string]bool)
	if kubeconfig == "" {
		return owned, nil
	}
	backend, err := h.workspaceBackendFor(labID, kubeconfig, namespace)
	if err != nil {
		return nil, fmt.Errorf("failed to build workspace backend: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, ownedTemplatesTimeout)
	defer cancel()
	workspaces, err := backend.ListWorkspaces(ctx, labID)
	if err != nil {
		return nil, fmt.Errorf("failed to list workspaces: %w", err)
	}

	owner := usernameFromEmail(email)
	for _, ws := range workspaces {
		// A workspace created before template attribution has no template to match.
		if ws.Owner == owner && ws.Template != "" {
			owned[ws.Template] = true
		}
	}
	return owned, nil
}
