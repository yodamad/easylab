package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
)

// --- Azure REST helpers (management.azure.com) --------------------------------

func azureAcquireToken(tenantID, clientID, clientSecret string) (string, error) {
	form := url.Values{}
	form.Set("grant_type", "client_credentials")
	form.Set("client_id", clientID)
	form.Set("client_secret", clientSecret)
	form.Set("scope", "https://management.azure.com/.default")
	tokenURL := fmt.Sprintf("https://login.microsoftonline.com/%s/oauth2/v2.0/token", url.PathEscape(tenantID))

	resp, err := http.Post(tokenURL, "application/x-www-form-urlencoded", strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("token request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("failed to read token response: %w", err)
	}

	var tr struct {
		AccessToken      string `json:"access_token"`
		Error            string `json:"error"`
		ErrorDescription string `json:"error_description"`
	}
	if err := json.Unmarshal(body, &tr); err != nil {
		return "", fmt.Errorf("failed to decode token response: %w", err)
	}
	if resp.StatusCode != http.StatusOK || tr.AccessToken == "" {
		msg := strings.TrimSpace(tr.ErrorDescription)
		if msg == "" {
			msg = tr.Error
		}
		if msg == "" {
			msg = fmt.Sprintf("HTTP %d", resp.StatusCode)
		}
		return "", fmt.Errorf("failed to acquire token: %s", msg)
	}
	return tr.AccessToken, nil
}

func azureListSubscriptionLocations(token, subscriptionID string) (names []string, display map[string]string, err error) {
	u := fmt.Sprintf("https://management.azure.com/subscriptions/%s/locations?api-version=2020-01-01", url.PathEscape(subscriptionID))
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("locations request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to read locations body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("locations API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var decoded struct {
		Value []struct {
			Name        string `json:"name"`
			DisplayName string `json:"displayName"`
			Metadata    struct {
				RegionType string `json:"regionType"`
			} `json:"metadata"`
		} `json:"value"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return nil, nil, fmt.Errorf("failed to decode locations: %w", err)
	}

	display = make(map[string]string, len(decoded.Value))
	for _, v := range decoded.Value {
		if v.Name == "" {
			continue
		}
		// Skip logical groupings (e.g. "france") — only Physical regions support Compute.
		if v.Metadata.RegionType != "" && v.Metadata.RegionType != "Physical" {
			continue
		}
		names = append(names, v.Name)
		display[v.Name] = v.DisplayName
	}
	return names, display, nil
}

func azureListVMSizes(token, subscriptionID, location string) ([]azureVMSize, error) {
	u := fmt.Sprintf(
		"https://management.azure.com/subscriptions/%s/providers/Microsoft.Compute/locations/%s/vmSizes?api-version=2022-11-01",
		url.PathEscape(subscriptionID),
		url.PathEscape(location),
	)
	req, err := http.NewRequest(http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("vmSizes request failed: %w", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("failed to read vmSizes body: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("vmSizes API HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var body struct {
		Value []struct {
			Name          string `json:"name"`
			NumberOfCores int    `json:"numberOfCores"`
			MemoryInMB    int    `json:"memoryInMB"`
		} `json:"value"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, fmt.Errorf("failed to decode vmSizes: %w", err)
	}

	out := make([]azureVMSize, 0, len(body.Value))
	for _, v := range body.Value {
		if v.Name == "" {
			continue
		}
		ramGB := v.MemoryInMB / 1024
		if ramGB == 0 && v.MemoryInMB > 0 {
			ramGB = 1
		}
		out = append(out, azureVMSize{Name: v.Name, VCPUs: v.NumberOfCores, RAMGB: ramGB})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

func (h *Handler) azureWriteLocationOptionsLive(w http.ResponseWriter) error {
	creds, err := h.credentialsManager.GetAzureCredentials()
	if err != nil {
		return err
	}
	token, err := azureAcquireToken(creds.TenantID, creds.ClientID, creds.ClientSecret)
	if err != nil {
		return err
	}
	names, _, err := azureListSubscriptionLocations(token, creds.SubscriptionID)
	if err != nil {
		return err
	}
	sort.Strings(names)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	for i, region := range names {
		selected := ""
		if i == 0 {
			selected = " selected"
		}
		fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, escapeHTML(region), selected, escapeHTML(region))
	}
	return nil
}

func (h *Handler) azureWriteVMSizesOptionsLive(w http.ResponseWriter, location string, minVcpus, maxVcpus, minRam, maxRam int) error {
	creds, err := h.credentialsManager.GetAzureCredentials()
	if err != nil {
		return err
	}
	token, err := azureAcquireToken(creds.TenantID, creds.ClientID, creds.ClientSecret)
	if err != nil {
		return err
	}
	sizes, err := azureListVMSizes(token, creds.SubscriptionID, location)
	if err != nil {
		return err
	}
	sizes = filterAzureVMSizesByCPURAM(sizes, minVcpus, maxVcpus, minRam, maxRam)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if len(sizes) == 0 {
		fmt.Fprint(w, `<option value="" disabled selected>No VM sizes match — adjust filters or use 0 for no limit</option>`)
		return nil
	}
	for i, s := range sizes {
		label := fmt.Sprintf("%s (%d vCPU, %d GB RAM)", s.Name, s.VCPUs, s.RAMGB)
		if i == 0 {
			fmt.Fprintf(w, `<option value="%s" selected>%s</option>`, escapeHTML(s.Name), escapeHTML(label))
		} else {
			fmt.Fprintf(w, `<option value="%s">%s</option>`, escapeHTML(s.Name), escapeHTML(label))
		}
	}
	return nil
}

func filterAzureVMSizesByCPURAM(sizes []azureVMSize, minVcpus, maxVcpus, minRam, maxRam int) []azureVMSize {
	if minVcpus == 0 && maxVcpus == 0 && minRam == 0 && maxRam == 0 {
		return sizes
	}
	var out []azureVMSize
	for _, s := range sizes {
		if minVcpus > 0 && s.VCPUs < minVcpus {
			continue
		}
		if maxVcpus > 0 && s.VCPUs > maxVcpus {
			continue
		}
		if minRam > 0 && s.RAMGB < minRam {
			continue
		}
		if maxRam > 0 && s.RAMGB > maxRam {
			continue
		}
		out = append(out, s)
	}
	return out
}

// GetAzureLocations returns HTML option elements for Azure regions (subscription locations).
func (h *Handler) GetAzureLocations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if h.azureOptionsManager != nil && h.azureOptionsManager.HasCache() {
		regions, defaultRegion := h.azureOptionsManager.GetRegionsForForm()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		for _, region := range regions {
			selected := ""
			if region == defaultRegion || (defaultRegion == "" && len(regions) > 0 && region == regions[0]) {
				selected = " selected"
			}
			disp := h.azureOptionsManager.RegionDisplayName(region)
			label := region
			if disp != "" && disp != region {
				label = fmt.Sprintf("%s (%s)", disp, region)
			}
			fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, escapeHTML(region), selected, escapeHTML(label))
		}
		return
	}

	if err := h.azureWriteLocationOptionsLive(w); err != nil {
		log.Printf("GetAzureLocations: %v", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<option value="" disabled selected>Error: %s</option>`, escapeHTML(err.Error()))
	}
}

// GetAzureVMSizes returns HTML option elements for VM sizes in a region.
func (h *Handler) GetAzureVMSizes(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	location := r.URL.Query().Get("location")
	if location == "" {
		http.Error(w, "Location is required", http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	minVcpus, _ := strconv.Atoi(q.Get("min_vcpus"))
	maxVcpus, _ := strconv.Atoi(q.Get("max_vcpus"))
	minRam, _ := strconv.Atoi(q.Get("min_ram"))
	maxRam, _ := strconv.Atoi(q.Get("max_ram"))

	if h.azureOptionsManager != nil && h.azureOptionsManager.HasCache() {
		if err := h.azureOptionsManager.EnsureVMSizesCached(location); err != nil {
			log.Printf("GetAzureVMSizes: %v", err)
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<option value="" disabled selected>Error: %s</option>`, escapeHTML(err.Error()))
			return
		}
		sizes, defaultSize := h.azureOptionsManager.GetVMSizesForForm(location)
		sizes = filterAzureVMSizesByCPURAM(sizes, minVcpus, maxVcpus, minRam, maxRam)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if len(sizes) == 0 {
			fmt.Fprint(w, `<option value="" disabled selected>No VM sizes match — adjust filters or use 0 for no limit</option>`)
			return
		}
		for i, s := range sizes {
			label := fmt.Sprintf("%s (%d vCPU, %d GB RAM)", s.Name, s.VCPUs, s.RAMGB)
			selected := ""
			if s.Name == defaultSize || (defaultSize == "" && i == 0) {
				selected = " selected"
			}
			fmt.Fprintf(w, `<option value="%s"%s>%s</option>`, escapeHTML(s.Name), selected, escapeHTML(label))
		}
		return
	}

	if err := h.azureWriteVMSizesOptionsLive(w, location, minVcpus, maxVcpus, minRam, maxRam); err != nil {
		log.Printf("GetAzureVMSizes: %v", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<option value="" disabled selected>Error: %s</option>`, escapeHTML(err.Error()))
	}
}

// GetAzureOptionsRegionVMSizeHTML returns the VM size table HTML for the Azure options admin page (HTMX fragment).
func (h *Handler) GetAzureOptionsRegionVMSizeHTML(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if h.azureOptionsManager == nil {
		http.Error(w, "Azure options not available", http.StatusServiceUnavailable)
		return
	}
	region := r.URL.Query().Get("region")
	if region == "" {
		http.Error(w, "region is required", http.StatusBadRequest)
		return
	}

	if err := h.azureOptionsManager.EnsureVMSizesCached(region); err != nil {
		log.Printf("GetAzureOptionsRegionVMSizeHTML: %v", err)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<p class="section-description">Failed to load VM sizes: %s</p>`, escapeHTML(err.Error()))
		return
	}

	cfg := h.azureOptionsManager.GetConfig()
	sizes := h.azureOptionsManager.GetCachedVMSizes(region)
	vmCfg := cfg.VMSizes[region]
	enabledSet := toSet(vmCfg.Enabled)
	showAll := len(enabledSet) == 0

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if len(sizes) == 0 {
		fmt.Fprint(w, `<p class="section-description">No VM sizes returned for this region.</p>`)
		return
	}

	var buf bytes.Buffer
	fmt.Fprintf(&buf, `<table class="options-table" style="width: 100%%; border-collapse: collapse;">`)
	fmt.Fprintf(&buf, `<thead><tr>`)
	fmt.Fprintf(&buf, `<th style="text-align: left; padding: 0.5rem; border-bottom: 1px solid var(--border); font-size: 0.875rem;">VM size</th>`)
	fmt.Fprintf(&buf, `<th style="text-align: center; padding: 0.5rem; border-bottom: 1px solid var(--border); font-size: 0.875rem; width: 80px;">vCPUs</th>`)
	fmt.Fprintf(&buf, `<th style="text-align: center; padding: 0.5rem; border-bottom: 1px solid var(--border); font-size: 0.875rem; width: 80px;">RAM (GB)</th>`)
	fmt.Fprintf(&buf, `<th style="text-align: center; padding: 0.5rem; border-bottom: 1px solid var(--border); font-size: 0.875rem; width: 80px;">Enabled</th>`)
	fmt.Fprintf(&buf, `<th style="text-align: center; padding: 0.5rem; border-bottom: 1px solid var(--border); font-size: 0.875rem; width: 80px;">Default</th>`)
	fmt.Fprintf(&buf, `</tr></thead><tbody>`)

	escRegion := escapeHTML(region)
	for _, s := range sizes {
		enabled := showAll || enabledSet[s.Name]
		isDef := s.Name == vmCfg.Default
		checked := ""
		if enabled {
			checked = " checked"
		}
		defChecked := ""
		if isDef {
			defChecked = " checked"
		}
		fmt.Fprintf(&buf, `<tr style="border-bottom: 1px solid var(--border);">`)
		fmt.Fprintf(&buf, `<td style="padding: 0.5rem; font-family: monospace; font-weight: 700;">%s</td>`, escapeHTML(s.Name))
		fmt.Fprintf(&buf, `<td style="text-align: center; padding: 0.5rem;">%d</td>`, s.VCPUs)
		fmt.Fprintf(&buf, `<td style="text-align: center; padding: 0.5rem;">%d</td>`, s.RAMGB)
		fmt.Fprintf(&buf, `<td style="text-align: center; padding: 0.5rem;"><input type="checkbox" name="vmsize_enabled_%s" value="%s"%s></td>`, escRegion, escapeHTML(s.Name), checked)
		fmt.Fprintf(&buf, `<td style="text-align: center; padding: 0.5rem;"><input type="radio" name="vmsize_default_%s" value="%s"%s></td>`, escRegion, escapeHTML(s.Name), defChecked)
		fmt.Fprintf(&buf, `</tr>`)
	}
	fmt.Fprintf(&buf, `</tbody></table>`)
	_, _ = w.Write(buf.Bytes())
}
