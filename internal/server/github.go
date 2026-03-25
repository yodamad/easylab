package server

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
)

type githubRelease struct {
	TagName    string `json:"tag_name"`
	Prerelease bool   `json:"prerelease"`
	Draft      bool   `json:"draft"`
}

// GetCoderVersions returns HTML <option> elements for the 5 latest stable Coder releases
// fetched from the GitHub API. The first option is selected (latest). A final "custom" option
// allows the user to enter a version manually.
func (h *Handler) GetCoderVersions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet,
		"https://api.github.com/repos/coder/coder/releases?per_page=20", nil)
	if err != nil {
		log.Printf("GetCoderVersions: failed to build request: %v", err)
		writeVersionsError(w)
		return
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", "easylab")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		log.Printf("GetCoderVersions: GitHub API request failed: %v", err)
		writeVersionsError(w)
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("GetCoderVersions: GitHub API returned status %d", resp.StatusCode)
		writeVersionsError(w)
		return
	}

	var releases []githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		log.Printf("GetCoderVersions: failed to decode response: %v", err)
		writeVersionsError(w)
		return
	}

	// Collect up to 5 stable (non-prerelease, non-draft) releases
	var versions []string
	for _, rel := range releases {
		if rel.Prerelease || rel.Draft {
			continue
		}
		// Strip leading "v" for consistency with existing form value
		tag := rel.TagName
		if len(tag) > 0 && tag[0] == 'v' {
			tag = tag[1:]
		}
		versions = append(versions, tag)
		if len(versions) == 5 {
			break
		}
	}

	if len(versions) == 0 {
		writeVersionsError(w)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	for i, v := range versions {
		selected := ""
		if i == 0 {
			selected = " selected"
		}
		fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, escapeHTML(v), selected, escapeHTML(v))
	}
	fmt.Fprint(w, `<option value="custom">Custom version…</option>`)
}

func writeVersionsError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprint(w, `<option value="custom" selected>Custom version…</option>`)
}
