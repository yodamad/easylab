package server

import (
	"fmt"
	"html"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"

	"github.com/ovh/go-ovh/ovh"
)

type ovhFlavor struct {
	Name  string `json:"name"`
	VCPUs int    `json:"vcpus"`
	RAM   int    `json:"ram"`
}

func (h *Handler) newOVHClient() (*ovh.Client, string, error) {
	creds, err := h.credentialsManager.GetOVHCredentials()
	if err != nil {
		return nil, "", fmt.Errorf("OVH credentials not configured: %w", err)
	}

	client, err := ovh.NewClient(creds.Endpoint, creds.ApplicationKey, creds.ApplicationSecret, creds.ConsumerKey)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create OVH client: %w", err)
	}

	return client, creds.ServiceName, nil
}

// GetOVHRegions returns HTML <option> elements for regions where Managed Kubernetes is available.
// Uses the OVHOptionsManager cache when available; falls back to a live OVH API call.
func (h *Handler) GetOVHRegions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.ovhOptionsManager != nil && h.ovhOptionsManager.HasCache() {
		regions, defaultRegion := h.ovhOptionsManager.GetRegionsForForm()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		for _, region := range regions {
			selected := ""
			if region == defaultRegion || (defaultRegion == "" && region == regions[0]) {
				selected = " selected"
			}
			fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, escapeHTML(region), selected, escapeHTML(region))
		}
		return
	}

	client, serviceName, err := h.newOVHClient()
	if err != nil {
		log.Printf("GetOVHRegions: %v", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<option value="" disabled selected>Error: %s</option>`, escapeHTML(err.Error()))
		return
	}

	var regions []string
	endpoint := fmt.Sprintf("/cloud/project/%s/capabilities/kube/regions", serviceName)
	if err := client.Get(endpoint, &regions); err != nil {
		log.Printf("GetOVHRegions: OVH API error: %v", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<option value="" disabled selected>Failed to load regions</option>`)
		return
	}

	sort.Strings(regions)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	for i, region := range regions {
		if i == 0 {
			fmt.Fprintf(w, `<option value="%s" selected>%s</option>`, escapeHTML(region), escapeHTML(region))
		} else {
			fmt.Fprintf(w, `<option value="%s">%s</option>`, escapeHTML(region), escapeHTML(region))
		}
	}
}

// GetOVHFlavors returns HTML <option> elements for flavors available in a given region.
// Query param: region (required).
// Uses the OVHOptionsManager cache when available; falls back to a live OVH API call.
func (h *Handler) GetOVHFlavors(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	region := r.URL.Query().Get("region")
	if region == "" {
		http.Error(w, "Region is required", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	minVcpus, _ := strconv.Atoi(q.Get("min_vcpus"))
	maxVcpus, _ := strconv.Atoi(q.Get("max_vcpus"))
	minRam, _ := strconv.Atoi(q.Get("min_ram"))
	maxRam, _ := strconv.Atoi(q.Get("max_ram"))

	if h.ovhOptionsManager != nil && h.ovhOptionsManager.HasCache() {
		flavors, defaultFlavor := h.ovhOptionsManager.GetFlavorsForForm(region)
		flavors = filterFlavorsByCPURAM(flavors, minVcpus, maxVcpus, minRam, maxRam)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if len(flavors) == 0 {
			fmt.Fprint(w, `<option value="" disabled selected>No flavors match — adjust filters or use 0 for no limit</option>`)
			return
		}
		for i, f := range flavors {
			label := fmt.Sprintf("%s (%d vCPU, %d GB RAM)", f.Name, f.VCPUs, f.RAM)
			selected := ""
			if f.Name == defaultFlavor || (defaultFlavor == "" && i == 0) {
				selected = " selected"
			}
			fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, escapeHTML(f.Name), selected, escapeHTML(label))
		}
		return
	}

	client, serviceName, err := h.newOVHClient()
	if err != nil {
		log.Printf("GetOVHFlavors: %v", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<option value="" disabled selected>Error: %s</option>`, escapeHTML(err.Error()))
		return
	}

	var flavors []ovhFlavor
	endpoint := fmt.Sprintf("/cloud/project/%s/capabilities/kube/flavors?region=%s", serviceName, url.QueryEscape(region))
	if err := client.Get(endpoint, &flavors); err != nil {
		log.Printf("GetOVHFlavors: OVH API error: %v", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, `<option value="" disabled selected>Failed to load flavors</option>`)
		return
	}

	sort.Slice(flavors, func(i, j int) bool {
		return flavors[i].Name < flavors[j].Name
	})
	flavors = filterFlavorsByCPURAM(flavors, minVcpus, maxVcpus, minRam, maxRam)

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if len(flavors) == 0 {
		fmt.Fprint(w, `<option value="" disabled selected>No flavors match — adjust filters or use 0 for no limit</option>`)
		return
	}
	for i, f := range flavors {
		label := fmt.Sprintf("%s (%d vCPU, %d GB RAM)", f.Name, f.VCPUs, f.RAM)
		if i == 0 {
			fmt.Fprintf(w, `<option value="%s" selected>%s</option>`, escapeHTML(f.Name), escapeHTML(label))
		} else {
			fmt.Fprintf(w, `<option value="%s">%s</option>`, escapeHTML(f.Name), escapeHTML(label))
		}
	}
}

func escapeHTML(s string) string {
	return html.EscapeString(s)
}
