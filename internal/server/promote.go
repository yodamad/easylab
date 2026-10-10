package server

import (
	"encoding/json"
	"fmt"
	"html/template"
	"log"
	"net/http"
	"strings"
)

// PromoteTarget is a lab a template can be promoted to: any other lab that is
// up. Listed on each template card of the lab detail page.
type PromoteTarget struct {
	ID        string
	StackName string
}

// promoteTargets lists the labs a template of sourceID can be promoted to —
// every other completed lab, in the labs list's order.
func (h *Handler) promoteTargets(sourceID string) []PromoteTarget {
	var targets []PromoteTarget
	for _, job := range h.jobManager.GetAllJobs() {
		job.mu.RLock()
		id := job.ID
		status := job.Status
		stackName := ""
		if job.Config != nil {
			stackName = job.Config.StackName
		}
		job.mu.RUnlock()
		if id == sourceID || status != JobStatusCompleted {
			continue
		}
		targets = append(targets, PromoteTarget{ID: id, StackName: stackName})
	}
	return targets
}

// cloneWorkspaceTemplate returns a copy of t that shares no map, slice or pointer
// with it, so a template promoted to another lab can never be changed through
// the lab it came from.
func cloneWorkspaceTemplate(t WorkspaceTemplate) (WorkspaceTemplate, error) {
	raw, err := json.Marshal(t)
	if err != nil {
		return WorkspaceTemplate{}, fmt.Errorf("failed to encode template %q: %w", t.Name, err)
	}
	var clone WorkspaceTemplate
	if err := json.Unmarshal(raw, &clone); err != nil {
		return WorkspaceTemplate{}, fmt.Errorf("failed to decode template %q: %w", t.Name, err)
	}
	return clone, nil
}

// PromoteTemplate handles POST /api/labs/{id}/templates/{name}/promote
// (target_lab_id=…, target_name=…, bake=true|false). It copies the template's
// definition from lab {id} to another lab that is up, under target_name when it
// is given — a name the target already uses is refused, never overwritten.
//
// Only the definition travels. Credentials live in each lab's own cluster, a
// baked image is per lab, and whether the template is closed to new students is
// the source lab's business: the copy arrives open and unbaked. With bake=true a
// devcontainer template is baked on the target straight away.
//
// The source lab is only read, so it does not have to be up. The answer is an
// HTML fragment for the card's promote panel.
func (h *Handler) PromoteTemplate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	sourceID, templateName, ok := bakePathParts(w, r)
	if !ok {
		return
	}

	source, exists := h.jobManager.GetJob(sourceID)
	if !exists {
		http.Error(w, "Lab not found", http.StatusNotFound)
		return
	}

	source.mu.RLock()
	var original *WorkspaceTemplate
	if source.Config != nil {
		for _, t := range source.Config.GetWorkspaceTemplates() {
			if t.Name == templateName {
				original = &t
				break
			}
		}
	}
	var promoted WorkspaceTemplate
	var cloneErr error
	if original != nil {
		// Cloned under the lock: the template's maps and slices belong to the source.
		promoted, cloneErr = cloneWorkspaceTemplate(*original)
	}
	source.mu.RUnlock()
	if original == nil {
		http.Error(w, "Template not found", http.StatusNotFound)
		return
	}
	if cloneErr != nil {
		log.Printf("PromoteTemplate: lab %s: %v", sourceID, cloneErr)
		h.renderHTMLError(w, "Cannot Promote", "This template could not be copied.")
		return
	}

	targetID := strings.TrimSpace(getFormValue(r, "target_lab_id"))
	if targetID == "" {
		h.renderHTMLError(w, "Cannot Promote", "Choose the lab to promote this template to.")
		return
	}
	if targetID == sourceID {
		h.renderHTMLError(w, "Cannot Promote", "Choose another lab — this template is already on this one.")
		return
	}
	target, exists := h.jobManager.GetJob(targetID)
	if !exists {
		h.renderHTMLError(w, "Cannot Promote", "The target lab no longer exists.")
		return
	}
	target.mu.RLock()
	targetStatus := target.Status
	targetStack := ""
	if target.Config != nil {
		targetStack = target.Config.StackName
	}
	target.mu.RUnlock()
	if targetStatus != JobStatusCompleted {
		h.renderHTMLError(w, "Cannot Promote", "The target lab is not ready yet.")
		return
	}

	if name := strings.TrimSpace(getFormValue(r, "target_name")); name != "" {
		promoted.Name = name
	}
	if err := validateWorkspaceTemplates([]WorkspaceTemplate{promoted}); err != nil {
		// A validation message about the admin's own template — safe to show.
		h.renderHTMLError(w, "Cannot Promote", err.Error())
		return
	}

	clash, added := false, false
	h.updateJobConfig(targetID, func(config *LabConfig) {
		for _, t := range config.WorkspaceTemplates {
			if t.Name == promoted.Name {
				clash = true
				return
			}
		}
		config.WorkspaceTemplates = append(config.WorkspaceTemplates, promoted)
		added = true
	})
	if clash {
		h.renderHTMLError(w, "Cannot Promote", fmt.Sprintf("The target lab already has a template named %q. Promote it under another name.", promoted.Name))
		return
	}
	if !added {
		h.renderHTMLError(w, "Cannot Promote", "The target lab has no configuration to add a template to.")
		return
	}
	// Off the response's critical path — see the comment on the SaveJob call in
	// UploadTemplateToLab for why this is safe to run async.
	go func() {
		if err := h.jobManager.SaveJob(targetID); err != nil {
			log.Printf("Failed to persist promoted template for lab %s: %v", targetID, err)
		}
	}()
	// Start pulling the promoted template's images onto the target's nodes now.
	go h.reconcilePrepull(targetID)

	detail := fmt.Sprintf("%s from lab %s", promoted.Name, sourceID)
	if promoted.Name != templateName {
		detail = fmt.Sprintf("%s (was %s) from lab %s", promoted.Name, templateName, sourceID)
	}
	h.recordAudit(adminActor(r), "admin", "lab.template_promote", targetID, detail)

	baking := false
	if getFormValue(r, "bake") == "true" {
		if names := devcontainerTemplateNames([]WorkspaceTemplate{promoted}); len(names) > 0 {
			baking = true
			// Before the response — see markBakesStarting.
			h.markBakesStarting(targetID, names)
			go h.autoBakeTemplates(targetID, names, adminActor(r), "admin")
		}
	}

	w.Header().Set("Content-Type", "text/html")
	fmt.Fprint(w, renderPromoteResult(templateName, promoted.Name, targetID, targetStack, baking, h.promotedCredentialWarning(r, targetID, promoted)))
}

// promotedCredentialWarning says which credentials the promoted template names
// that the target lab's cluster does not have, as an HTML fragment — "" when it
// names none, or the target has them all. Credentials never travel with a
// template, so this is what tells the admin the promotion is not finished yet.
//
// When the target's cluster cannot be asked, every credential the template names
// is listed instead: a warning that may be unnecessary beats a silent gap.
func (h *Handler) promotedCredentialWarning(r *http.Request, targetID string, promoted WorkspaceTemplate) string {
	templates := []WorkspaceTemplate{promoted}
	referenced := referencedCredentialNames(templates)
	if len(referenced) == 0 {
		return ""
	}

	names, lead := referenced, "This template references"
	sm, err := h.labSecretManagerFor(targetID)
	if err == nil {
		secrets, listErr := sm.ListAuthSecrets(r.Context())
		if listErr == nil {
			names, lead = pendingCredentialNames(secrets, templates), "The target lab is missing"
		}
		err = listErr
	}
	if err != nil {
		log.Printf("PromoteTemplate: could not check credentials on lab %s: %v", targetID, err)
	}
	if len(names) == 0 {
		return ""
	}

	escaped := make([]string, len(names))
	for i, name := range names {
		escaped[i] = template.HTMLEscapeString(name)
	}
	return fmt.Sprintf(`<div class="warning-message">%s the credential(s) <strong>%s</strong>. Add them in the target lab's Credentials panel — workspaces needing them fail until then.</div>`,
		lead, strings.Join(escaped, ", "))
}

// renderPromoteResult builds the promote panel's success fragment.
func renderPromoteResult(sourceName, promotedName, targetID, targetStack string, baking bool, credentialWarning string) string {
	label := targetStack
	if label == "" {
		label = targetID
	}

	var b strings.Builder
	fmt.Fprintf(&b, `<div class="success-message">Promoted <strong>%s</strong> to <a href="/labs/%s#workspaces">%s</a>`,
		template.HTMLEscapeString(sourceName), template.HTMLEscapeString(targetID), template.HTMLEscapeString(label))
	if promotedName != sourceName {
		fmt.Fprintf(&b, ` as <strong>%s</strong>`, template.HTMLEscapeString(promotedName))
	}
	b.WriteString(".")
	if baking {
		b.WriteString(" Baking its image there…")
	}
	b.WriteString(`</div>`)
	b.WriteString(credentialWarning)
	return b.String()
}
