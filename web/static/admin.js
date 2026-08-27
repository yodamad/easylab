// Wizard functionality
const wizard = {
    currentStep: 1,
    useExistingCluster: false,
    clusterModeSelected: false,
    allSteps: [1, 2, 3, 4, 5, 6, 7],
    byokSteps: [1, 4, 5, 6, 7],

    getActiveSteps() {
        return this.useExistingCluster ? this.byokSteps : this.allSteps;
    },

    getActiveStepIndex() {
        return this.getActiveSteps().indexOf(this.currentStep);
    },

    isLastStep() {
        const steps = this.getActiveSteps();
        return this.currentStep === steps[steps.length - 1];
    },

    isFirstStep() {
        return this.currentStep === this.getActiveSteps()[0];
    },

    init() {
        this.bindEvents();
        this.bindClusterModeEvents();
        this.bindIngressCertManagerEvents();
        this.bindDomainModeEvents();
        this.bindDNSRecordModeEvents();
        this.bindDNSAlreadyConfiguredEvents();
        this.updateUI();
    },

    bindEvents() {
        document.getElementById('btn-next').addEventListener('click', () => this.nextStep());
        document.getElementById('btn-prev').addEventListener('click', () => this.prevStep());

        const dryRunBtn = document.getElementById('btn-dry-run');
        if (dryRunBtn) {
            dryRunBtn.addEventListener('click', () => {
                if (this.validateCurrentStep()) {
                    submitDryRun();
                }
            });
        }

        document.addEventListener('keydown', (e) => {
            if (e.key === 'Enter' && e.target.tagName !== 'TEXTAREA') {
                e.preventDefault();
                if (!this.isLastStep()) {
                    this.nextStep();
                }
            }
        });

        document.querySelectorAll('.progress-step').forEach(step => {
            step.addEventListener('click', () => {
                const stepNum = parseInt(step.dataset.step);
                if (this.getActiveSteps().includes(stepNum) && this.canGoToStep(stepNum)) {
                    this.goToStep(stepNum);
                }
            });
        });
    },

    bindClusterModeEvents() {
        const newBtn = document.getElementById('cluster-mode-new');
        const existingBtn = document.getElementById('cluster-mode-existing');
        if (!newBtn || !existingBtn) return;

        newBtn.addEventListener('click', () => this.setClusterMode(false));
        existingBtn.addEventListener('click', () => this.setClusterMode(true));
    },

    bindIngressCertManagerEvents() {
        const ingressInstall = document.getElementById('ingress-install-btn');
        const ingressExisting = document.getElementById('ingress-existing-btn');
        if (ingressInstall && ingressExisting) {
            ingressInstall.addEventListener('click', () => this.setIngressMode('install'));
            ingressExisting.addEventListener('click', () => this.setIngressMode('existing'));
        }

        const certInstall = document.getElementById('certmanager-install-btn');
        const certExisting = document.getElementById('certmanager-existing-btn');
        if (certInstall && certExisting) {
            certInstall.addEventListener('click', () => this.setCertManagerMode('install'));
            certExisting.addEventListener('click', () => this.setCertManagerMode('existing'));
        }

        const githubEnable = document.getElementById('github-login-enable-btn');
        const githubDisable = document.getElementById('github-login-disable-btn');
        if (githubEnable && githubDisable) {
            githubEnable.addEventListener('click', () => this.setGithubLoginEnabled(true));
            githubDisable.addEventListener('click', () => this.setGithubLoginEnabled(false));
        }
    },

    setIngressMode(mode) {
        const installBtn = document.getElementById('ingress-install-btn');
        const existingBtn = document.getElementById('ingress-existing-btn');
        const fields = document.getElementById('ingress-existing-fields');
        const hidden = document.getElementById('install_nginx_ingress');

        installBtn.classList.toggle('selected', mode === 'install');
        existingBtn.classList.toggle('selected', mode === 'existing');
        fields.style.display = mode === 'existing' ? '' : 'none';
        hidden.value = mode === 'install' ? 'true' : 'false';
    },

    setCertManagerMode(mode) {
        const installBtn = document.getElementById('certmanager-install-btn');
        const existingBtn = document.getElementById('certmanager-existing-btn');
        const fields = document.getElementById('certmanager-existing-fields');
        const hidden = document.getElementById('install_cert_manager');

        installBtn.classList.toggle('selected', mode === 'install');
        existingBtn.classList.toggle('selected', mode === 'existing');
        fields.style.display = mode === 'existing' ? '' : 'none';
        hidden.value = mode === 'install' ? 'true' : 'false';
        this.updateDNSAlreadyConfiguredVisibility();
    },

    bindDomainModeEvents() {
        const quickstartBtn = document.getElementById('domain-mode-quickstart-btn');
        const autoBtn = document.getElementById('domain-mode-auto-btn');
        const manualBtn = document.getElementById('domain-mode-manual-btn');
        if (!quickstartBtn || !autoBtn || !manualBtn) return;

        quickstartBtn.addEventListener('click', () => this.setDomainMode('quickstart'));
        autoBtn.addEventListener('click', () => this.setDomainMode('auto'));
        manualBtn.addEventListener('click', () => this.setDomainMode('manual'));

        // Quick start is the form's default (domain fields start empty).
        this.setDomainMode('quickstart');
    },

    setDomainMode(mode) {
        const quickstartBtn = document.getElementById('domain-mode-quickstart-btn');
        const autoBtn = document.getElementById('domain-mode-auto-btn');
        const manualBtn = document.getElementById('domain-mode-manual-btn');
        const domainFields = document.getElementById('domain-fields-block');
        const dnsProviderBlock = document.getElementById('dns-provider-block');
        const manualGuidance = document.getElementById('dns-manual-warning');
        const certManagerGroup = document.getElementById('certmanager-toggle-group');
        if (!quickstartBtn || !autoBtn || !manualBtn) return;

        quickstartBtn.classList.toggle('selected', mode === 'quickstart');
        autoBtn.classList.toggle('selected', mode === 'auto');
        manualBtn.classList.toggle('selected', mode === 'manual');

        if (domainFields) domainFields.style.display = mode === 'quickstart' ? 'none' : '';
        if (dnsProviderBlock) dnsProviderBlock.style.display = mode === 'auto' ? '' : 'none';
        if (manualGuidance) manualGuidance.style.display = mode === 'manual' ? '' : 'none';
        // cert-manager is a no-op with no domain (coder/https.go skips it entirely),
        // so hide the toggle rather than leave a dead control on screen.
        if (certManagerGroup) certManagerGroup.style.display = mode === 'quickstart' ? 'none' : '';

        // "Quick start" and "manual DNS" both mean no DNS provider is configured;
        // clearing it here keeps handleDNSProviderChange()'s field visibility and
        // updateDNSManualWarning() in sync with the chosen mode.
        const dnsProviderSelect = document.getElementById('dns_provider');
        if (mode !== 'auto' && dnsProviderSelect && dnsProviderSelect.value !== '') {
            dnsProviderSelect.value = '';
            dnsProviderSelect.dispatchEvent(new Event('change'));
        }

        if (mode === 'quickstart') {
            const domainInput = document.getElementById('domain');
            const acmeEmailInput = document.getElementById('acme_email');
            const wildcardInput = document.getElementById('wildcard_domain');
            if (domainInput) domainInput.value = '';
            if (acmeEmailInput) acmeEmailInput.value = '';
            if (wildcardInput) wildcardInput.value = '';
        }

        if (typeof updateDNSManualWarning === 'function') updateDNSManualWarning();
        this.updateWildcardOverrideVisibility();
        this.updateDNSAlreadyConfiguredVisibility();
    },

    // The Wildcard Domain override only ever takes effect for "Custom domain —
    // automatic" with the "Wildcard record" DNS strategy: coder/https.go reads
    // coder:wildcardDomain solely inside that branch. It is a silent no-op in
    // every other mode, so show it only where it can actually do something.
    updateWildcardOverrideVisibility() {
        const advanced = document.getElementById('dns-advanced-options');
        const autoBtn = document.getElementById('domain-mode-auto-btn');
        const externalBtn = document.getElementById('dns-record-externaldns-btn');
        if (!advanced || !autoBtn) return;

        const isAuto = autoBtn.classList.contains('selected');
        const isExternalDNS = !!externalBtn && externalBtn.classList.contains('selected');
        advanced.style.display = isAuto && !isExternalDNS ? '' : 'none';
    },

    // Only relevant when reusing an existing cert-manager with a DNS provider
    // selected — a freshly-installed cert-manager can't already have a DNS-01
    // issuer configured on it.
    updateDNSAlreadyConfiguredVisibility() {
        const group = document.getElementById('dns-already-configured-group');
        const certExistingBtn = document.getElementById('certmanager-existing-btn');
        const autoBtn = document.getElementById('domain-mode-auto-btn');
        if (!group || !certExistingBtn || !autoBtn) return;

        const certExisting = certExistingBtn.classList.contains('selected');
        const isAuto = autoBtn.classList.contains('selected');
        const relevant = certExisting && isAuto;
        group.style.display = relevant ? '' : 'none';
        if (!relevant) this.setDNSAlreadyConfigured(false);
    },

    bindDNSAlreadyConfiguredEvents() {
        const noBtn = document.getElementById('dns-already-configured-no-btn');
        const yesBtn = document.getElementById('dns-already-configured-yes-btn');
        if (!noBtn || !yesBtn) return;

        noBtn.addEventListener('click', () => this.setDNSAlreadyConfigured(false));
        yesBtn.addEventListener('click', () => this.setDNSAlreadyConfigured(true));
    },

    setDNSAlreadyConfigured(isConfigured) {
        const noBtn = document.getElementById('dns-already-configured-no-btn');
        const yesBtn = document.getElementById('dns-already-configured-yes-btn');
        const hidden = document.getElementById('dns_already_configured');
        if (!noBtn || !yesBtn || !hidden) return;

        noBtn.classList.toggle('selected', !isConfigured);
        yesBtn.classList.toggle('selected', isConfigured);
        hidden.value = isConfigured ? 'true' : 'false';
    },

    bindDNSRecordModeEvents() {
        const wildcardBtn = document.getElementById('dns-record-wildcard-btn');
        const externalBtn = document.getElementById('dns-record-externaldns-btn');
        if (!wildcardBtn || !externalBtn) return;

        wildcardBtn.addEventListener('click', () => this.setDNSRecordMode('wildcard'));
        externalBtn.addEventListener('click', () => this.setDNSRecordMode('externaldns'));
    },

    setDNSRecordMode(mode) {
        const wildcardBtn = document.getElementById('dns-record-wildcard-btn');
        const externalBtn = document.getElementById('dns-record-externaldns-btn');
        const hidden = document.getElementById('use_external_dns');
        if (!wildcardBtn || !externalBtn || !hidden) return;

        wildcardBtn.classList.toggle('selected', mode === 'wildcard');
        externalBtn.classList.toggle('selected', mode === 'externaldns');
        hidden.value = mode === 'externaldns' ? 'true' : 'false';
        this.updateWildcardOverrideVisibility();
    },

    setGithubLoginEnabled(enabled) {
        const enableBtn = document.getElementById('github-login-enable-btn');
        const disableBtn = document.getElementById('github-login-disable-btn');
        const hidden = document.getElementById('coder_github_login_enabled');
        if (!enableBtn || !disableBtn || !hidden) return;

        enableBtn.classList.toggle('selected', enabled);
        disableBtn.classList.toggle('selected', !enabled);
        hidden.value = enabled ? 'true' : 'false';
    },

    setClusterMode(useExisting) {
        this.useExistingCluster = useExisting;
        this.clusterModeSelected = true;

        document.getElementById('use_existing_cluster').value = useExisting ? 'true' : 'false';

        const newBtn = document.getElementById('cluster-mode-new');
        const existingBtn = document.getElementById('cluster-mode-existing');
        newBtn.classList.toggle('selected', !useExisting);
        existingBtn.classList.toggle('selected', useExisting);

        document.getElementById('provider-section').style.display = useExisting ? 'none' : '';
        document.getElementById('kubeconfig-section').style.display = useExisting ? '' : 'none';

        // Toggle required on infrastructure-only fields (steps 3, 4, 5)
        // These steps are skipped in BYOK mode, so validation would block submission
        const infraFieldIds = [
            'network_gateway_name', 'network_gateway_model', 'network_private_network_name',
            'network_id', 'network_region', 'network_mask',
            'k8s_cluster_name',
            'nodepool_name', 'nodepool_flavor',
            'nodepool_desired_node_count', 'nodepool_min_node_count', 'nodepool_max_node_count'
        ];
        infraFieldIds.forEach(id => {
            const el = document.getElementById(id);
            if (el) {
                if (useExisting) {
                    el.removeAttribute('required');
                } else {
                    el.setAttribute('required', 'required');
                }
            }
        });

        // Handle azure_location required state (not in infraFieldIds)
        const azureLocEl = document.getElementById('azure_location');
        if (azureLocEl && useExisting) {
            azureLocEl.removeAttribute('required');
        }

        // When creating new infra, adjust required attrs for OVH vs Azure fields
        if (!useExisting) {
            syncRequiredAttrsForProvider();
        }

        // Reset to step 1 when switching modes
        this.currentStep = 1;
        this.updateProgressBar();
        this.updateUI();

        // Update credentials notice visibility
        if (useExisting) {
            const notice = document.getElementById('provider-credentials-notice');
            if (notice) notice.style.display = 'none';
        } else {
            checkCredentialsStatus();
        }
    },

    updateProgressBar() {
        const activeSteps = this.getActiveSteps();
        const progressSteps = document.querySelectorAll('.progress-step');
        progressSteps.forEach(step => {
            const stepNum = parseInt(step.dataset.step);
            step.style.display = activeSteps.includes(stepNum) ? '' : 'none';
            // Relabel step numbers for BYOK mode
            const stepNumberEl = step.querySelector('.step-number');
            if (stepNumberEl) {
                const idx = activeSteps.indexOf(stepNum);
                stepNumberEl.textContent = idx >= 0 ? idx + 1 : stepNum;
            }
        });
        document.getElementById('total-steps').textContent = activeSteps.length;
    },

    validateCurrentStep() {
        const currentStepEl = document.querySelector(`.wizard-step[data-step="${this.currentStep}"]`);
        if (!currentStepEl) return true;

        // On step 1, require a cluster mode selection
        if (this.currentStep === 1 && !this.clusterModeSelected) {
            alert('Please select a cluster mode: Create New Infrastructure or Use Existing Cluster.');
            return false;
        }

        // In BYOK mode on step 1, validate kubeconfig is provided
        if (this.useExistingCluster && this.currentStep === 1) {
            const uploadBtn = document.getElementById('kubeconfig-mode-upload');
            const pasteBtn = document.getElementById('kubeconfig-mode-paste');
            const modeSelected = (uploadBtn && uploadBtn.classList.contains('selected')) ||
                                 (pasteBtn && pasteBtn.classList.contains('selected'));
            if (!modeSelected) {
                alert('Please choose how to provide your kubeconfig: Upload File or Paste Content.');
                return false;
            }
            const kubeconfigFile = document.getElementById('kubeconfig_file');
            const kubeconfigContent = document.getElementById('kubeconfig_content');
            const hasFile = kubeconfigFile && kubeconfigFile.files && kubeconfigFile.files.length > 0;
            const hasContent = kubeconfigContent && kubeconfigContent.value.trim() !== '';
            // Editing a failed lab's config before retrying it: a blank kubeconfig
            // here means "keep the lab's existing one" (the backend falls back to
            // it), so it is not required in that one case.
            if (!hasFile && !hasContent && !this.retryEditKeepsKubeconfig) {
                alert('Please provide your kubeconfig content.');
                return false;
            }
            // Also validate stack name (now on step 1 for all modes)
            const stackNameInput = document.getElementById('stack_name');
            if (stackNameInput && !stackNameInput.checkValidity()) {
                stackNameInput.reportValidity();
                return false;
            }
            return true;
        }

        const inputs = currentStepEl.querySelectorAll('input[required], select[required]');
        let valid = true;
        inputs.forEach(input => {
            // Skip hidden/invisible required fields from the inactive section
            if (input.offsetParent === null) return;
            if (!input.checkValidity()) {
                input.reportValidity();
                valid = false;
            }
        });
        if (!valid) return false;

        // In devcontainer mode the admin must run the import before continuing:
        // it is what generates the workspace YAML the lab is created from. The
        // template name is the only required field, so without this gate the
        // wizard would advance — and the lab be created — with no workspace.
        if (this.currentStep === 6) {
            const devcontainerBtn = document.getElementById('templates-mode-devcontainer');
            const yamlTextarea = document.getElementById('templates_yaml');
            if (devcontainerBtn && devcontainerBtn.classList.contains('selected') &&
                (!yamlTextarea || !yamlTextarea.value.trim())) {
                alert('Click "Import" to read the devcontainer before you continue — it generates the workspace the lab is created from.');
                return false;
            }
        }

        return true;
    },

    canGoToStep(stepNum) {
        if (!this.getActiveSteps().includes(stepNum)) return false;
        const currentIdx = this.getActiveStepIndex();
        const targetIdx = this.getActiveSteps().indexOf(stepNum);
        if (targetIdx < currentIdx) return true;
        if (targetIdx === currentIdx + 1) return this.validateCurrentStep();
        if (targetIdx === currentIdx) return true;
        return false;
    },

    nextStep() {
        const steps = this.getActiveSteps();
        const idx = this.getActiveStepIndex();
        if (idx < steps.length - 1 && this.validateCurrentStep()) {
            this.currentStep = steps[idx + 1];
            this.updateUI();
        }
    },

    prevStep() {
        const steps = this.getActiveSteps();
        const idx = this.getActiveStepIndex();
        if (idx > 0) {
            this.currentStep = steps[idx - 1];
            this.updateUI();
        }
    },

    goToStep(stepNum) {
        if (this.getActiveSteps().includes(stepNum)) {
            this.currentStep = stepNum;
            this.updateUI();
        }
    },

    updateUI() {
        const activeSteps = this.getActiveSteps();
        const currentIdx = this.getActiveStepIndex();

        // Update step visibility
        document.querySelectorAll('.wizard-step').forEach(step => {
            const stepNum = parseInt(step.dataset.step);
            step.classList.toggle('active', stepNum === this.currentStep);
        });

        // Update progress indicator
        document.querySelectorAll('.progress-step').forEach(step => {
            const stepNum = parseInt(step.dataset.step);
            if (!activeSteps.includes(stepNum)) {
                step.style.display = 'none';
                return;
            }
            step.style.display = '';
            const stepIdx = activeSteps.indexOf(stepNum);
            step.classList.remove('active', 'completed');
            if (stepNum === this.currentStep) {
                step.classList.add('active');
            } else if (stepIdx < currentIdx) {
                step.classList.add('completed');
            }
        });

        // Update step counter
        document.getElementById('current-step-num').textContent = currentIdx + 1;
        document.getElementById('total-steps').textContent = activeSteps.length;

        // Update buttons
        const prevBtn = document.getElementById('btn-prev');
        const nextBtn = document.getElementById('btn-next');
        const submitBtn = document.getElementById('btn-submit');
        const dryRunBtn = document.getElementById('btn-dry-run');

        prevBtn.disabled = this.isFirstStep();

        if (this.isLastStep()) {
            nextBtn.style.display = 'none';
            submitBtn.style.display = 'inline-flex';
            // Editing a lab's config before retrying it submits straight to the
            // retry endpoint — there is no separate "preview" concept for that.
            dryRunBtn.style.display = this.hideDryRunButton ? 'none' : 'inline-flex';
        } else {
            nextBtn.style.display = 'inline-flex';
            submitBtn.style.display = 'none';
            dryRunBtn.style.display = 'none';
        }

        const progressEl = document.querySelector('.wizard-progress');
        if (progressEl) progressEl.scrollIntoView({ behavior: 'smooth', block: 'start' });

        // Fetch cloud regions when entering step 2 (Network) for the first time.
        if (this.currentStep === 2) {
            const providerSelect = document.getElementById('provider');
            const isAzure = providerSelect && providerSelect.value === 'azure';
            if (isAzure) {
                if (!this._azureLocationsLoaded) {
                    this._azureLocationsLoaded = true;
                    loadAzureLocations();
                }
            } else if (!this._ovhRegionsLoaded) {
                this._ovhRegionsLoaded = true;
                loadOVHRegions();
            }
        }
        // Node pool sizes: OVH flavors vs Azure VM sizes (step 3: Compute)
        if (this.currentStep === 3) {
            const providerSelect = document.getElementById('provider');
            const isAzure = providerSelect && providerSelect.value === 'azure';
            if (isAzure) {
                const loc = document.getElementById('azure_location');
                if (loc && loc.value) {
                    loadAzureVMSizes();
                }
            } else {
                loadOVHFlavors();
            }
        }
    }
};

// Expose wizard globally so inline onclick handlers can reach it
window.wizard = wizard;

// Function to hide wizard and show only job status
function hideWizardShowStatus() {
    // Hide wizard elements
    document.querySelector('.wizard-progress').style.display = 'none';
    document.getElementById('lab-form').style.display = 'none';
    document.querySelector('.wizard-footer').style.display = 'none';
    
    // Make sure job status container is visible
    const container = document.getElementById('job-status-container');
    container.style.display = 'block';
    
    // Remove bottom padding from body since footer is hidden
    document.body.style.paddingBottom = '1rem';
}

// Extract base name from prefixed value (removes stack prefix)
function extractBaseName(prefixedValue, stackName) {
    if (!prefixedValue || !stackName) return prefixedValue || '';
    const prefix = `${stackName}-`;
    if (prefixedValue.startsWith(prefix)) {
        return prefixedValue.substring(prefix.length);
    }
    return prefixedValue;
}

// Update resource name inputs with stack prefix
function updateResourceNames(skipIfEditing = false) {
    const stackName = document.getElementById('stack_name').value || 'dev';
    const resourceInputs = [
        { id: 'network_gateway_name', baseAttr: 'data-base-name' },
        { id: 'network_private_network_name', baseAttr: 'data-base-name' },
        { id: 'k8s_cluster_name', baseAttr: 'data-base-name' },
        { id: 'nodepool_name', baseAttr: 'data-base-name' }
    ];
    
    resourceInputs.forEach(({ id, baseAttr }) => {
        const input = document.getElementById(id);
        if (!input) return;
        
        // Skip if user is currently editing this field
        if (skipIfEditing && document.activeElement === input) {
            return;
        }
        
        // Get current value and determine base name
        let currentValue = input.value || '';
        let baseName = input.getAttribute(baseAttr);
        
        // Extract base name from current value if it has prefix, otherwise use current value as base
        if (currentValue.startsWith(`${stackName}-`)) {
            baseName = extractBaseName(currentValue, stackName);
            input.setAttribute(baseAttr, baseName);
        } else if (currentValue && !baseName) {
            // If no prefix and no stored base name, use current value as base
            baseName = currentValue;
            input.setAttribute(baseAttr, baseName);
        }
        
        // Update input value to show prefixed name
        if (baseName) {
            const prefixedValue = `${stackName}-${baseName}`;
            if (input.value !== prefixedValue) {
                input.value = prefixedValue;
            }
        }
    });
}

// Extract base names from form before submission
function extractBaseNamesBeforeSubmit(form) {
    const stackName = document.getElementById('stack_name').value || 'dev';
    const resourceFields = [
        'network_gateway_name',
        'network_private_network_name',
        'k8s_cluster_name',
        'nodepool_name'
    ];
    
    resourceFields.forEach(fieldName => {
        const input = form.querySelector(`[name="${fieldName}"]`);
        if (input) {
            const currentValue = input.value || '';
            const baseName = extractBaseName(currentValue, stackName);
            // Temporarily set the base name for submission
            input.value = baseName;
        }
    });
}

// Calculate start and end IPs from network mask
function calculateNetworkIPs() {
    const maskInput = document.getElementById('network_mask');
    const startIpDisplay = document.getElementById('calculated-start-ip');
    const endIpDisplay = document.getElementById('calculated-end-ip');
    const startIpHidden = document.getElementById('network_start_ip');
    const endIpHidden = document.getElementById('network_end_ip');
    
    if (!maskInput || !startIpDisplay || !endIpDisplay || !startIpHidden || !endIpHidden) {
        return;
    }
    
    const maskValue = maskInput.value.trim();
    
    // Parse CIDR notation (e.g., "10.0.0.0/24")
    const cidrMatch = maskValue.match(/^(\d+\.\d+\.\d+\.\d+)\/(\d+)$/);
    
    if (!cidrMatch) {
        // Invalid format, keep default values
        return;
    }
    
    const networkAddress = cidrMatch[1];
    const parts = networkAddress.split('.');
    
    if (parts.length !== 4) {
        return;
    }
    
    // Calculate start IP (last octet = 100)
    const startIP = `${parts[0]}.${parts[1]}.${parts[2]}.100`;
    
    // Calculate end IP (last octet = 254)
    const endIP = `${parts[0]}.${parts[1]}.${parts[2]}.254`;
    
    // Update display
    startIpDisplay.textContent = startIP;
    endIpDisplay.textContent = endIP;
    
    // Update hidden fields for form submission
    startIpHidden.value = startIP;
    endIpHidden.value = endIP;
}

// Generate secure password using crypto.getRandomValues (mirrors backend GenerateSecurePassword)
function generateSecurePassword(length) {
    const lowercase = 'abcdefghijklmnopqrstuvwxyz';
    const uppercase = 'ABCDEFGHIJKLMNOPQRSTUVWXYZ';
    const numbers = '0123456789';
    const symbols = '!@#$%^&*';
    const allChars = lowercase + uppercase + numbers + symbols;
    const minLength = 16;

    if (!length || length < minLength) {
        length = minLength;
    }

    const getRandomInt = (max) => {
        const arr = new Uint32Array(1);
        crypto.getRandomValues(arr);
        return arr[0] % max;
    };

    const password = new Array(length);

    password[0] = lowercase[getRandomInt(lowercase.length)];
    password[1] = uppercase[getRandomInt(uppercase.length)];
    password[2] = numbers[getRandomInt(numbers.length)];
    password[3] = symbols[getRandomInt(symbols.length)];

    for (let i = 4; i < length; i++) {
        password[i] = allChars[getRandomInt(allChars.length)];
    }

    for (let i = length - 1; i > 0; i--) {
        const j = getRandomInt(i + 1);
        [password[i], password[j]] = [password[j], password[i]];
    }

    return password.join('');
}

// Fetch available Azure locations and populate the region select (wizard step 3).
function loadAzureLocations() {
    const locationSelect = document.getElementById('azure_location');
    if (!locationSelect) return;

    locationSelect.innerHTML = '<option value="">Loading regions…</option>';

    return fetch('/api/azure/locations')
        .then(response => {
            if (!response.ok) throw new Error('Failed to load regions');
            return response.text();
        })
        .then(html => {
            locationSelect.innerHTML = html;
            wizard._azureLocationsLoaded = true;
        })
        .catch(err => {
            console.error('Error loading Azure regions:', err);
            locationSelect.innerHTML = '<option value="" disabled selected>Failed to load regions</option>';
        });
}

// Fetch available OVH regions and populate the region select
function loadOVHRegions() {
    const regionSelect = document.getElementById('network_region');
    if (!regionSelect) return;

    regionSelect.innerHTML = '<option value="">Loading regions…</option>';

    return fetch('/api/ovh/regions')
        .then(response => {
            if (!response.ok) throw new Error('Failed to load regions');
            return response.text();
        })
        .then(html => {
            regionSelect.innerHTML = html;
            wizard._ovhRegionsLoaded = true;
            // Trigger flavor load for the initially selected region
            loadOVHFlavors();
        })
        .catch(err => {
            console.error('Error loading OVH regions:', err);
            regionSelect.innerHTML = '<option value="" disabled selected>Failed to load regions</option>';
        });
}

// Fetch available OVH flavors for the selected region and populate the flavor select
function loadOVHFlavors() {
    const regionSelect = document.getElementById('network_region');
    const flavorSelect = document.getElementById('nodepool_flavor');
    if (!regionSelect || !flavorSelect) return;

    const region = regionSelect.value;
    if (!region) {
        flavorSelect.innerHTML = '<option value="" disabled selected>Select a region first</option>';
        return;
    }

    let url = '/api/ovh/flavors?region=' + encodeURIComponent(region);
    const minVcpus = document.getElementById('flavor_filter_min_vcpus');
    const maxVcpus = document.getElementById('flavor_filter_max_vcpus');
    const minRam = document.getElementById('flavor_filter_min_ram');
    const maxRam = document.getElementById('flavor_filter_max_ram');
    if (minVcpus && minVcpus.value) url += '&min_vcpus=' + encodeURIComponent(minVcpus.value);
    if (maxVcpus && maxVcpus.value) url += '&max_vcpus=' + encodeURIComponent(maxVcpus.value);
    if (minRam && minRam.value) url += '&min_ram=' + encodeURIComponent(minRam.value);
    if (maxRam && maxRam.value) url += '&max_ram=' + encodeURIComponent(maxRam.value);

    flavorSelect.innerHTML = '<option value="">Loading flavors…</option>';

    return fetch(url)
        .then(response => {
            if (!response.ok) throw new Error('Failed to load flavors');
            return response.text();
        })
        .then(html => {
            flavorSelect.innerHTML = html;
            const hint = document.getElementById('flavor-filter-hint');
            if (hint) {
                const opts = flavorSelect.options;
                const noMatch = opts.length === 1 && opts[0].disabled && opts[0].textContent.indexOf('No flavors match') !== -1;
                hint.style.display = noMatch ? 'block' : 'none';
            }
        })
        .catch(err => {
            console.error('Error loading OVH flavors:', err);
            flavorSelect.innerHTML = '<option value="" disabled selected>Failed to load flavors</option>';
            const hint = document.getElementById('flavor-filter-hint');
            if (hint) hint.style.display = 'none';
        });
}

// Toggle flavor filters section visibility (lab creation form)
function toggleFlavorFiltersSection() {
    var body = document.getElementById('flavor-filters-body');
    var toggle = document.getElementById('flavor-filters-toggle');
    if (!body || !toggle) return;
    if (body.style.display === 'none') {
        body.style.display = 'block';
        toggle.style.transform = 'rotate(180deg)';
    } else {
        body.style.display = 'none';
        toggle.style.transform = 'rotate(0deg)';
    }
}

// Clear flavor filter inputs and reload the flavor list
function clearFlavorFilters() {
    const ids = ['flavor_filter_min_vcpus', 'flavor_filter_max_vcpus', 'flavor_filter_min_ram', 'flavor_filter_max_ram'];
    ids.forEach(function (id) {
        const el = document.getElementById(id);
        if (el) el.value = '0';
    });
    const providerSelect = document.getElementById('provider');
    if (providerSelect && providerSelect.value === 'azure') {
        loadAzureVMSizes();
    } else {
        loadOVHFlavors();
    }
}

// Sync required attributes on OVH-only vs Azure-only network fields based on current provider.
// Must be called after setClusterMode sets the base required state on infra fields.
function syncRequiredAttrsForProvider() {
    const providerSelect = document.getElementById('provider');
    const isAzure = providerSelect && providerSelect.value === 'azure';
    // Fields only relevant to OVH (inside #ovh-network-fields, hidden when Azure is selected)
    const ovhOnlyIds = [
        'network_gateway_name', 'network_gateway_model', 'network_private_network_name',
        'network_id', 'network_region', 'network_mask'
    ];
    ovhOnlyIds.forEach(id => {
        const el = document.getElementById(id);
        if (!el) return;
        if (isAzure) {
            el.removeAttribute('required');
        } else {
            el.setAttribute('required', 'required');
        }
    });
    const azureLocEl = document.getElementById('azure_location');
    if (azureLocEl) {
        if (isAzure) {
            azureLocEl.setAttribute('required', 'required');
        } else {
            azureLocEl.removeAttribute('required');
        }
    }
}

// Switch between OVH and Azure network/flavor UI based on selected provider
function handleProviderChange() {
    const providerSelect = document.getElementById('provider');
    if (!providerSelect) return;
    const provider = providerSelect.value;
    const isAzure = provider === 'azure';

    const ovhFields = document.getElementById('ovh-network-fields');
    const azureFields = document.getElementById('azure-network-fields');
    if (ovhFields) ovhFields.style.display = isAzure ? 'none' : '';
    if (azureFields) azureFields.style.display = isAzure ? '' : 'none';

    // Update step 3 header
    const stepTitle = document.getElementById('network-step-title');
    const stepDesc = document.getElementById('network-step-desc');
    if (stepTitle) stepTitle.textContent = isAzure ? 'Azure Region' : 'Network Configuration';
    if (stepDesc) stepDesc.textContent = isAzure
        ? 'Select the Azure region for your AKS cluster'
        : 'Configure private network and gateway settings';

    // Update flavor label
    const flavorLabel = document.getElementById('nodepool-flavor-label');
    if (flavorLabel) flavorLabel.textContent = isAzure ? 'VM Size *' : 'Flavor *';

    // Sync required attributes when switching provider (cluster mode already chosen)
    if (wizard.clusterModeSelected && !wizard.useExistingCluster) {
        syncRequiredAttrsForProvider();
    }

    // For Azure: load regions when switching provider (e.g. step 3 with OVH → Azure).
    // For OVH: repopulate regions if we're on network step and left Azure.
    const azureLocationSelect = document.getElementById('azure_location');
    if (isAzure && azureLocationSelect) {
        wizard._azureLocationsLoaded = false;
        loadAzureLocations();
        azureLocationSelect.removeEventListener('change', loadAzureVMSizes);
        azureLocationSelect.addEventListener('change', loadAzureVMSizes);
        if (azureLocationSelect.value) loadAzureVMSizes();
        else {
            const flavorSelect = document.getElementById('nodepool_flavor');
            if (flavorSelect) flavorSelect.innerHTML = '<option value="">Select a region first…</option>';
        }
    } else if (!isAzure && wizard.currentStep === 2) {
        wizard._ovhRegionsLoaded = false;
        loadOVHRegions();
    }

    checkCredentialsStatus();
    if (typeof syncEasylabHeaderProviderDropdown === 'function') {
        syncEasylabHeaderProviderDropdown(provider);
    }
    if (typeof setEasylabHeaderProviderPreference === 'function') {
        setEasylabHeaderProviderPreference(provider);
    }
}

// Show/hide DNS provider-specific credential fields when dns_provider changes.
function handleDNSProviderChange() {
    const select = document.getElementById('dns_provider');
    if (!select) return;
    const provider = select.value;

    const zoneGroup = document.getElementById('dns_zone_group');
    const zoneInput = document.getElementById('dns_zone');
    const ovhFields = document.getElementById('dns-ovh-fields');
    const azureFields = document.getElementById('dns-azure-fields');

    if (zoneGroup) zoneGroup.style.display = provider ? '' : 'none';
    // Zone is mandatory once a provider is chosen: without it the A record fails
    // deep inside pulumi up. Keep required in lockstep with visibility.
    if (zoneInput) zoneInput.required = !!provider;
    if (ovhFields) ovhFields.style.display = provider === 'ovh' ? '' : 'none';
    if (azureFields) azureFields.style.display = provider === 'azure' ? '' : 'none';

    // ExternalDNS replaces the wildcard record, so it only means anything once a
    // provider is there to create records with.
    const externalDNSGroup = document.getElementById('external_dns_group');
    if (externalDNSGroup) externalDNSGroup.style.display = provider ? '' : 'none';
    if (!provider && typeof wizard !== 'undefined') {
        wizard.setDNSRecordMode('wildcard');
    }

    updateDNSManualWarning();
}

// Keeps the manual-DNS guidance panel's example record in sync as the domain
// input changes. Visibility of the panel itself is owned by wizard.setDomainMode().
function updateDNSManualWarning() {
    const record = document.getElementById('dns-warning-record');
    const domainInput = document.getElementById('domain');
    if (!record || !domainInput) return;

    const domain = domainInput.value.trim();
    record.textContent = domain !== '' ? '*.' + domain : '*.your-domain';
}

// Fetch Azure VM sizes for the selected location
function loadAzureVMSizes() {
    const locationSelect = document.getElementById('azure_location');
    const flavorSelect = document.getElementById('nodepool_flavor');
    if (!locationSelect || !flavorSelect) return;

    const location = locationSelect.value;
    if (!location) {
        flavorSelect.innerHTML = '<option value="" disabled selected>Select a region first</option>';
        return;
    }

    let url = '/api/azure/vm-sizes?location=' + encodeURIComponent(location);
    const minVcpus = document.getElementById('flavor_filter_min_vcpus');
    const maxVcpus = document.getElementById('flavor_filter_max_vcpus');
    const minRam = document.getElementById('flavor_filter_min_ram');
    const maxRam = document.getElementById('flavor_filter_max_ram');
    if (minVcpus && minVcpus.value) url += '&min_vcpus=' + encodeURIComponent(minVcpus.value);
    if (maxVcpus && maxVcpus.value) url += '&max_vcpus=' + encodeURIComponent(maxVcpus.value);
    if (minRam && minRam.value) url += '&min_ram=' + encodeURIComponent(minRam.value);
    if (maxRam && maxRam.value) url += '&max_ram=' + encodeURIComponent(maxRam.value);

    flavorSelect.innerHTML = '<option value="">Loading VM sizes…</option>';

    return fetch(url)
        .then(response => {
            if (!response.ok) throw new Error('Failed to load VM sizes');
            return response.text();
        })
        .then(html => {
            flavorSelect.innerHTML = html;
        })
        .catch(err => {
            console.error('Error loading Azure VM sizes:', err);
            flavorSelect.innerHTML = '<option value="" disabled selected>Failed to load VM sizes</option>';
        });
}

// Check credentials status and show/hide disclaimer
function checkCredentialsStatus() {
    // Skip credentials check in BYOK mode
    if (wizard.useExistingCluster) {
        const notice = document.getElementById('provider-credentials-notice');
        if (notice) notice.style.display = 'none';
        return;
    }

    const providerSelect = document.getElementById('provider');
    const provider = providerSelect ? providerSelect.value : 'ovh';
    
    fetch(`/api/credentials?provider=${provider}`)
        .then(response => {
            if (response.ok) {
                return response.json();
            } else if (response.status === 404) {
                return { configured: false };
            }
            return { configured: false };
        })
        .then(data => {
            const notice = document.getElementById('provider-credentials-notice');
            if (notice) {
                notice.style.display = data.configured ? 'none' : 'block';
            }
        })
        .catch(error => {
            console.error('Error checking credentials:', error);
            const notice = document.getElementById('provider-credentials-notice');
            if (notice) {
                notice.style.display = 'block';
            }
        });
}

// Submit dry run
function submitDryRun() {
    const form = document.getElementById('lab-form');
    if (!form) return;
    
    // Extract base names before submission
    extractBaseNamesBeforeSubmit(form);
    
    const formData = new FormData(form);
    const responseDiv = document.getElementById('form-response');
    const loadingDiv = document.getElementById('loading');
    
    // Show loading indicator
    if (loadingDiv) {
        loadingDiv.style.display = 'block';
    }
    
    // Submit to dry-run endpoint
    fetch('/api/labs/dry-run', {
        method: 'POST',
        body: formData
    })
    .then(response => response.text())
    .then(html => {
        if (responseDiv) {
            responseDiv.innerHTML = html;
        }
        if (loadingDiv) {
            loadingDiv.style.display = 'none';
        }
        
        // Hide wizard and show status
        hideWizardShowStatus();
        
        // Start polling for status updates
        const container = document.getElementById('job-status-container');
        if (container) {
            const jobStatusDiv = responseDiv.querySelector('[id^="job-status"]');
            if (jobStatusDiv) {
                container.innerHTML = '';
                container.appendChild(jobStatusDiv);
                if (typeof htmx !== 'undefined') {
                    htmx.process(container);
                } else {
                    // Fallback polling
                    const hxGet = jobStatusDiv.getAttribute('hx-get');
                    if (hxGet) {
                        const match = hxGet.match(/\/api\/jobs\/([^/]+)/);
                        if (match) {
                            pollJobStatus(match[1], container);
                        }
                    }
                }
            }
        }
    })
    .catch(error => {
        console.error('Error submitting dry run:', error);
        if (responseDiv) {
            responseDiv.innerHTML = `<div class="error-message">Error: ${error.message}</div>`;
        }
        if (loadingDiv) {
            loadingDiv.style.display = 'none';
        }
    });
}

// Scroll output to bottom
function scrollOutputToBottom() {
    const outputEl = document.querySelector('.output');
    if (outputEl) {
        outputEl.scrollTop = outputEl.scrollHeight;
    }
}

// Fallback polling function
function pollJobStatus(jobId, container) {
    fetch('/api/jobs/' + jobId + '/status')
        .then(response => response.text())
        .then(html => {
            container.innerHTML = html;
            scrollOutputToBottom();
            if (html.includes('status-pending') || html.includes('status-running')) {
                setTimeout(() => pollJobStatus(jobId, container), 10000);
            }
        })
        .catch(error => {
            container.innerHTML = '<p class="error-message">Error polling status: ' + error.message + '</p>';
        });
}

function retryJob(jobId) {
    // Send POST request to retry endpoint
    fetch('/api/jobs/' + encodeURIComponent(jobId) + '/retry', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/x-www-form-urlencoded',
        }
    })
    .then(response => {
        if (response.ok) {
            // Redirect to admin page to view retry progress
            window.location.href = '/admin?job=' + encodeURIComponent(jobId);
        } else {
            // Handle error
            response.text().then(text => {
                console.error('Retry failed:', response.status, text);
                alert('Failed to retry job: ' + response.status);
            });
        }
    })
    .catch(error => {
        console.error('Retry error:', error);
        alert('Error retrying job: ' + error.message);
    });
}

function startPolling() {
    const responseDiv = document.getElementById('form-response');
    const jobStatusDiv = responseDiv.querySelector('[id^="job-status"]');
    if (jobStatusDiv) {
        // Hide wizard and show only status
        hideWizardShowStatus();
        
        const container = document.getElementById('job-status-container');
        container.innerHTML = '';
        container.appendChild(jobStatusDiv);
        
        const hxGet = jobStatusDiv.getAttribute('hx-get');
        if (hxGet) {
            const match = hxGet.match(/\/api\/jobs\/([^/]+)/);
            if (match) {
                pollJobStatus(match[1], container);
            }
        }
    }
}

// Initialize wizard
document.addEventListener('DOMContentLoaded', function() {
    wizard.init();

    // Auto-generate DB password on load so users don't need to touch the Advanced section
    const dbPasswordInput = document.getElementById('coder_db_password');
    if (dbPasswordInput && !dbPasswordInput.value) {
        dbPasswordInput.value = generateSecurePassword(16);
    }

    // Live stack name preview
    const stackNameInput = document.getElementById('stack_name');
    const stackNamePreview = document.getElementById('stack-name-preview');
    if (stackNameInput && stackNamePreview) {
        const updatePreview = function() {
            stackNamePreview.textContent = stackNameInput.value || 'dev';
        };
        stackNameInput.addEventListener('input', updatePreview);
        updatePreview();
    }

    // Bind Generate password buttons
    document.querySelectorAll('.btn-generate-password-db').forEach(function(btn) {
        btn.addEventListener('click', function() {
            const targetId = this.getAttribute('data-target');
            const input = document.getElementById(targetId);
            if (input) {
                input.value = generateSecurePassword(12);
            }
        });
    });

    document.querySelectorAll('.btn-generate-password-coder').forEach(function(btn) {
        btn.addEventListener('click', function() {
            const targetId = this.getAttribute('data-target');
            const input = document.getElementById(targetId);
            if (input) {
                input.value = generateSecurePassword(20);
            }
        });
    });

    // Handle provider selection change (OVH vs Azure)
    const providerSelect = document.getElementById('provider');
    if (providerSelect) {
        providerSelect.addEventListener('change', handleProviderChange);
        // Apply initial state
        handleProviderChange();
    }

    // Handle DNS provider selection change
    const dnsProviderSelect = document.getElementById('dns_provider');
    if (dnsProviderSelect) {
        dnsProviderSelect.addEventListener('change', handleDNSProviderChange);
        // Apply initial state (all DNS fields hidden by default)
        handleDNSProviderChange();
    }

    // The domain drives the same warning, and it sits in an earlier wizard step
    // than the DNS provider select.
    const domainInput = document.getElementById('domain');
    if (domainInput) {
        domainInput.addEventListener('input', updateDNSManualWarning);
    }

    // Reload flavors when region selection changes (OVH)
    const regionSelect = document.getElementById('network_region');
    if (regionSelect) {
        regionSelect.addEventListener('change', loadOVHFlavors);
    }

    // Reload flavors when flavor filter inputs change (provider-aware)
    ['flavor_filter_min_vcpus', 'flavor_filter_max_vcpus', 'flavor_filter_min_ram', 'flavor_filter_max_ram'].forEach(function (id) {
        const el = document.getElementById(id);
        if (el) {
            el.addEventListener('change', function () {
                const ps = document.getElementById('provider');
                if (ps && ps.value === 'azure') {
                    loadAzureVMSizes();
                } else {
                    loadOVHFlavors();
                }
            });
        }
    });

    // Clear flavor filters button
    const flavorFilterClearBtn = document.getElementById('flavor_filter_clear_btn');
    if (flavorFilterClearBtn) {
        flavorFilterClearBtn.addEventListener('click', clearFlavorFilters);
    }

    // Set up network mask calculation
    const maskInput = document.getElementById('network_mask');
    if (maskInput) {
        // Calculate on page load
        calculateNetworkIPs();
        
        // Calculate on input change
        maskInput.addEventListener('input', calculateNetworkIPs);
        maskInput.addEventListener('change', calculateNetworkIPs);
    }
    
    const form = document.getElementById('lab-form');
    
    // Set up resource name updates
    const resourceInputs = [
        document.getElementById('network_gateway_name'),
        document.getElementById('network_private_network_name'),
        document.getElementById('k8s_cluster_name'),
        document.getElementById('nodepool_name')
    ];
    
    // Update resource names when stack name changes
    if (stackNameInput) {
        stackNameInput.addEventListener('input', function() {
            updateResourceNames(true); // Skip fields being edited
        });
        stackNameInput.addEventListener('change', function() {
            updateResourceNames(false); // Update all when stack name is finalized
        });
    }
    
    // Update resource names when user finishes editing (on blur)
    resourceInputs.forEach(input => {
        if (input) {
            input.addEventListener('blur', function() {
                updateResourceNames(false);
            });
        }
    });
    
    // Initial update
    updateResourceNames();
    
    // Extract base names before form submission
    form.addEventListener('submit', function(e) {
        extractBaseNamesBeforeSubmit(form);
    });
    
    // Also handle HTMX form submission
    if (typeof htmx !== 'undefined') {
        document.body.addEventListener('htmx:configRequest', function(event) {
            if (event.detail.target === form || event.detail.elt === form) {
                // Editing a failed lab's config before retrying it submits to a
                // different endpoint than plain lab creation. htmx caches an
                // already-processed element's request config, so changing the
                // form's hx-post attribute after the fact and reprocessing does
                // not reliably take effect — rewriting the request path here,
                // which htmx explicitly supports, does.
                if (window.retryWithConfigTarget) {
                    event.detail.path = window.retryWithConfigTarget;
                }
                extractBaseNamesBeforeSubmit(form);
            }
        });
    }
    
    // Check if HTMX is loaded
    if (typeof htmx === 'undefined') {
        console.warn('HTMX not loaded, using fallback form submission');
        form.addEventListener('submit', function(e) {
            e.preventDefault();
            const formData = new FormData(form);
            const responseDiv = document.getElementById('form-response');
            responseDiv.innerHTML = '<p>Submitting...</p>';

            fetch(window.retryWithConfigTarget || '/api/labs', {
                method: 'POST',
                body: formData
            })
            .then(response => response.text())
            .then(html => {
                responseDiv.innerHTML = html;
                startPolling();
            })
            .catch(error => {
                responseDiv.innerHTML = '<p class="error-message">Error: ' + error.message + '</p>';
            });
        });
    }
    
    // Kubeconfig mode toggle (upload vs paste)
    const kubeconfigUploadBtn = document.getElementById('kubeconfig-mode-upload');
    const kubeconfigPasteBtn = document.getElementById('kubeconfig-mode-paste');
    const kubeconfigUploadSection = document.getElementById('kubeconfig-upload-section');
    const kubeconfigPasteSection = document.getElementById('kubeconfig-paste-section');

    function setKubeconfigMode(mode) {
        if (kubeconfigUploadBtn && kubeconfigPasteBtn) {
            kubeconfigUploadBtn.classList.toggle('selected', mode === 'upload');
            kubeconfigPasteBtn.classList.toggle('selected', mode === 'paste');
        }
        if (kubeconfigUploadSection) {
            kubeconfigUploadSection.style.display = mode === 'upload' ? '' : 'none';
        }
        if (kubeconfigPasteSection) {
            kubeconfigPasteSection.style.display = mode === 'paste' ? '' : 'none';
        }
    }

    if (kubeconfigUploadBtn) {
        kubeconfigUploadBtn.addEventListener('click', () => setKubeconfigMode('upload'));
    }
    if (kubeconfigPasteBtn) {
        kubeconfigPasteBtn.addEventListener('click', () => setKubeconfigMode('paste'));
    }
    setKubeconfigMode('upload');

    // Kubeconfig file upload: read contents into textarea
    const kubeconfigFileInput = document.getElementById('kubeconfig_file');
    const kubeconfigFileNameDisplay = document.getElementById('kubeconfig-file-name-display');
    if (kubeconfigFileInput) {
        kubeconfigFileInput.addEventListener('change', function(e) {
            const file = e.target.files[0];
            if (file) {
                if (kubeconfigFileNameDisplay) {
                    kubeconfigFileNameDisplay.textContent = `Selected: ${file.name}`;
                    kubeconfigFileNameDisplay.style.display = 'block';
                }
                const reader = new FileReader();
                reader.onload = function(evt) {
                    const textarea = document.getElementById('kubeconfig_content');
                    if (textarea) {
                        textarea.value = evt.target.result;
                    }
                };
                reader.readAsText(file);
            } else if (kubeconfigFileNameDisplay) {
                kubeconfigFileNameDisplay.style.display = 'none';
            }
        });
    }

    // Handle job status display on page load if job ID is in URL
    const urlParams = new URLSearchParams(window.location.search);
    const jobId = urlParams.get('job');
    if (jobId) {
        hideWizardShowStatus();

        const container = document.getElementById('job-status-container');
        container.innerHTML = `<div id="job-status" hx-get="/api/jobs/${jobId}/status" hx-trigger="load, every 10s" hx-swap="innerHTML"></div>`;
        if (typeof htmx !== 'undefined') {
            htmx.process(container);
        } else {
            pollJobStatus(jobId, container);
        }
    }

    // Editing a failed/destroyed lab's configuration before retrying or
    // recreating it: the wizard is pre-filled from the source lab's stored
    // config, seeded via the retry/recreate choice modal on the Labs and Jobs
    // History pages.
    const prefillJobId = urlParams.get('prefill_job');
    const prefillAction = urlParams.get('prefill_action'); // 'retry' | 'recreate'
    if (prefillJobId && prefillAction) {
        Promise.all([
            // GetJobStatusJSON only recognizes the /api/jobs/ prefix, not /api/labs/.
            fetch('/api/jobs/' + encodeURIComponent(prefillJobId) + '?format=json').then(r => r.json()),
            fetch('/api/labs/templates/yaml?lab_id=' + encodeURIComponent(prefillJobId)).then(r => r.text())
        ]).then(([job, templatesYaml]) => applyPrefill(job.config || {}, templatesYaml, prefillJobId, prefillAction))
          .catch(err => {
              console.error('Could not load lab configuration to edit:', err);
          });
    }
});

// Handle form submission response (for HTMX)
document.body.addEventListener('htmx:afterSwap', function(event) {
    if (event.detail.target.id === 'form-response') {
        const response = event.detail.target;
        const jobStatusDiv = response.querySelector('[id^="job-status"]');
        if (jobStatusDiv) {
            // Hide wizard and show only status
            hideWizardShowStatus();
            
            const container = document.getElementById('job-status-container');
            container.innerHTML = '';
            container.appendChild(jobStatusDiv);
            if (typeof htmx !== 'undefined') {
                htmx.process(container);
            }
        }
    }
    
    // Scroll output to bottom after any HTMX swap
    scrollOutputToBottom();
});

// Also handle HTMX afterSettle for job status updates
document.body.addEventListener('htmx:afterSettle', function(event) {
    scrollOutputToBottom();
});

// Wizard credentials: repeatable registry/git token rows, applied to the lab's
// cluster once it is provisioned. A git credential has no server field — basic
// auth goes to whatever host the template's git_repo names.
const credentialsContainer = document.getElementById('wizard-credentials-container');
const credentialRowTmpl = document.getElementById('credential-row-tmpl');
const btnAddCredential = document.getElementById('btn-add-credential');

// The names of the git credentials currently defined, in row order. A template's
// "Git credential" picker offers these so a private repo can name the token that
// unlocks it without the admin retyping the name (and risking a typo).
function gitCredentialNames() {
    const names = [];
    if (!credentialsContainer) return names;
    credentialsContainer.querySelectorAll('.credential-row').forEach(row => {
        const kind = row.querySelector('.credential-kind');
        const nameInput = row.querySelector('[name="secret_name"]');
        const name = nameInput ? nameInput.value.trim() : '';
        if (kind && kind.value === 'git' && name) names.push(name);
    });
    return names;
}

// Rebuild every template's git-credential picker from the defined credentials,
// keeping any explicit choice that still exists. With exactly one git credential
// the empty option is labelled to match the server's auto-link, so the common case
// reads as "already handled".
function refreshGitCredentialOptions() {
    const names = gitCredentialNames();
    document.querySelectorAll('.template-git-cred-select').forEach(select => {
        const current = select.value;
        select.innerHTML = '';
        const auto = document.createElement('option');
        auto.value = '';
        auto.textContent = names.length === 1 ? 'Auto — use ' + names[0] : 'None';
        select.appendChild(auto);
        names.forEach(name => {
            const opt = document.createElement('option');
            opt.value = name;
            opt.textContent = name;
            select.appendChild(opt);
        });
        if (current && names.includes(current)) select.value = current;
    });
}

// The names of the registry credentials currently defined, in row order. The
// devcontainer picker offers these so a private base image or cache registry can
// name the dockerconfigjson Secret that unlocks it without the admin retyping it.
function registryCredentialNames() {
    const names = [];
    if (!credentialsContainer) return names;
    credentialsContainer.querySelectorAll('.credential-row').forEach(row => {
        const kind = row.querySelector('.credential-kind');
        const nameInput = row.querySelector('[name="secret_name"]');
        const name = nameInput ? nameInput.value.trim() : '';
        if (kind && kind.value === 'registry' && name) names.push(name);
    });
    return names;
}

// Rebuild every registry-credential picker from the defined credentials, keeping
// any explicit choice that still exists. With exactly one registry credential the
// empty option is labelled to match the devcontainer import's auto-resolve, so the
// common case reads as "already handled".
function refreshRegistryCredentialOptions() {
    const names = registryCredentialNames();
    document.querySelectorAll('.template-registry-cred-select').forEach(select => {
        const current = select.value;
        select.innerHTML = '';
        const auto = document.createElement('option');
        auto.value = '';
        auto.textContent = names.length === 1 ? 'Auto — use ' + names[0] : 'None';
        select.appendChild(auto);
        names.forEach(name => {
            const opt = document.createElement('option');
            opt.value = name;
            opt.textContent = name;
            select.appendChild(opt);
        });
        if (current && names.includes(current)) select.value = current;
    });
}

// The username/token of a defined git credential, looked up by name. The devcontainer
// import reuses it to read a private repo, so the admin enters the token once (in the
// Credentials section) rather than retyping it just to read the devcontainer. Empty
// when the name is unset or unmatched, so a public repo still clones anonymously.
function gitCredentialAuthByName(name) {
    const empty = { username: '', token: '' };
    if (!name || !credentialsContainer) return empty;
    let found = empty;
    credentialsContainer.querySelectorAll('.credential-row').forEach(row => {
        const kind = row.querySelector('.credential-kind');
        const nameInput = row.querySelector('[name="secret_name"]');
        if (!kind || kind.value !== 'git' || !nameInput || nameInput.value.trim() !== name) return;
        const userInput = row.querySelector('[name="secret_username"]');
        const tokenInput = row.querySelector('[name="secret_token"]');
        found = {
            username: userInput ? userInput.value : '',
            token: tokenInput ? tokenInput.value : '',
        };
    });
    return found;
}

function toggleCredentialServer(row) {
    const kind = row.querySelector('.credential-kind');
    const serverGroup = row.querySelector('.credential-server');
    if (!kind || !serverGroup) {
        return;
    }
    serverGroup.style.display = kind.value === 'registry' ? '' : 'none';
}

function wireCredentialRow(row) {
    const kind = row.querySelector('.credential-kind');
    if (kind) {
        kind.addEventListener('change', () => {
            toggleCredentialServer(row);
            refreshGitCredentialOptions();
            refreshRegistryCredentialOptions();
        });
    }
    const nameInput = row.querySelector('[name="secret_name"]');
    if (nameInput) {
        nameInput.addEventListener('input', () => {
            refreshGitCredentialOptions();
            refreshRegistryCredentialOptions();
        });
    }
    const removeBtn = row.querySelector('.btn-remove-credential');
    if (removeBtn) {
        removeBtn.addEventListener('click', () => {
            row.remove();
            refreshGitCredentialOptions();
            refreshRegistryCredentialOptions();
        });
    }
    toggleCredentialServer(row);
}

if (btnAddCredential && credentialRowTmpl && credentialsContainer) {
    btnAddCredential.addEventListener('click', function() {
        const row = credentialRowTmpl.content.firstElementChild.cloneNode(true);
        credentialsContainer.appendChild(row);
        wireCredentialRow(row);
        refreshGitCredentialOptions();
        refreshRegistryCredentialOptions();
    });
}

// Multi-template: Add/Remove rows and per-row source selection
const templatesContainer = document.getElementById('workspace-templates-container');
const templateRowTmpl = document.getElementById('template-row-tmpl');
const btnAddTemplate = document.getElementById('btn-add-template');
const templateCountInput = document.getElementById('template_count');

function reindexTemplateRows() {
    const rows = templatesContainer.querySelectorAll('.template-row');
    templateCountInput.value = rows.length;
    rows.forEach((row, idx) => {
        row.setAttribute('data-template-index', idx);
        row.querySelector('.template-index').textContent = idx + 1;
        row.querySelectorAll('[name^="template_"]').forEach(el => {
            const name = el.getAttribute('name');
            if (!name) return;
            const match = name.match(/^template_(\d+)_(.+)$/);
            if (match) {
                el.setAttribute('name', 'template_' + idx + '_' + match[2]);
            }
        });
        row.querySelectorAll('input[data-field="file"]').forEach(el => {
            el.setAttribute('name', 'template_file_' + idx);
            el.setAttribute('id', 'template_file_' + idx);
        });
        row.querySelectorAll('label[for^="template_file_"]').forEach(el => {
            el.setAttribute('for', 'template_file_' + idx);
        });
    });
    initTemplateRowHandlers();
}

function createVariableRow(templateIdx, varName, varValue, description, required) {
    const div = document.createElement('div');
    div.className = 'template-variable-row';
    const nameInput = document.createElement('input');
    nameInput.type = 'text';
    nameInput.name = 'template_' + templateIdx + '_env_name';
    nameInput.placeholder = 'Env variable name';
    nameInput.value = varName || '';
    if (required) nameInput.setAttribute('data-required', 'true');

    const valueInput = document.createElement('input');
    valueInput.type = 'text';
    valueInput.name = 'template_' + templateIdx + '_env_value';
    valueInput.placeholder = description || 'Value';
    valueInput.value = varValue || '';

    const removeBtn = document.createElement('button');
    removeBtn.type = 'button';
    removeBtn.className = 'btn btn-secondary btn-remove-variable';
    removeBtn.textContent = 'x';
    removeBtn.title = 'Remove variable';
    removeBtn.addEventListener('click', function() {
        div.remove();
    });

    div.appendChild(nameInput);
    div.appendChild(valueInput);
    div.appendChild(removeBtn);
    return div;
}

function addVariableRow(row) {
    const idx = parseInt(row.getAttribute('data-template-index'), 10);
    const container = row.querySelector('.template-variables-container');
    if (container) {
        container.appendChild(createVariableRow(idx, '', '', '', false));
    }
}

function makeTextInput(name, placeholder) {
    const input = document.createElement('input');
    input.type = 'text';
    input.name = name;
    input.placeholder = placeholder;
    return input;
}

function makeRemoveButton(div, title) {
    const btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'btn btn-secondary btn-remove-variable';
    btn.textContent = 'x';
    btn.title = title;
    btn.addEventListener('click', function() { div.remove(); });
    return btn;
}

// makePrivilegedToggle returns a checkbox that drives a hidden input carrying the
// value, so an unchecked box still submits (keeping the sidecar arrays aligned).
function makePrivilegedToggle(name) {
    const wrap = document.createElement('label');
    wrap.className = 'sidecar-privileged';
    const hidden = document.createElement('input');
    hidden.type = 'hidden';
    hidden.name = name;
    hidden.value = 'false';
    const cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.addEventListener('change', function() { hidden.value = cb.checked ? 'true' : 'false'; });
    const span = document.createElement('span');
    span.textContent = 'privileged';
    wrap.appendChild(hidden);
    wrap.appendChild(cb);
    wrap.appendChild(span);
    return wrap;
}

function createSidecarRow(templateIdx) {
    const div = document.createElement('div');
    div.className = 'template-variable-row';
    div.appendChild(makeTextInput('template_' + templateIdx + '_sidecar_name', 'name'));
    div.appendChild(makeTextInput('template_' + templateIdx + '_sidecar_image', 'image (e.g. postgres:16)'));
    div.appendChild(makeTextInput('template_' + templateIdx + '_sidecar_ports', 'ports (5432,6379)'));
    div.appendChild(makeTextInput('template_' + templateIdx + '_sidecar_env', 'env (KEY=VAL,KEY2=VAL2)'));
    div.appendChild(makeTextInput('template_' + templateIdx + '_sidecar_capabilities', 'capabilities (SYS_ADMIN,…)'));
    div.appendChild(makePrivilegedToggle('template_' + templateIdx + '_sidecar_privileged'));
    div.appendChild(makeRemoveButton(div, 'Remove sidecar'));
    return div;
}

function addSidecarRow(row) {
    const idx = parseInt(row.getAttribute('data-template-index'), 10);
    const container = row.querySelector('.template-sidecars-container');
    if (container) container.appendChild(createSidecarRow(idx));
}

function createMountRow(templateIdx) {
    const div = document.createElement('div');
    div.className = 'template-variable-row';
    const typeSelect = document.createElement('select');
    typeSelect.name = 'template_' + templateIdx + '_mount_type';
    ['configmap', 'secret'].forEach(function(t) {
        const opt = document.createElement('option');
        opt.value = t;
        opt.textContent = t;
        typeSelect.appendChild(opt);
    });
    div.appendChild(typeSelect);
    div.appendChild(makeTextInput('template_' + templateIdx + '_mount_name', 'ConfigMap/Secret name'));
    div.appendChild(makeTextInput('template_' + templateIdx + '_mount_path', 'mount path (/etc/config)'));
    div.appendChild(makeRemoveButton(div, 'Remove mount'));
    return div;
}

function addMountRow(row) {
    const idx = parseInt(row.getAttribute('data-template-index'), 10);
    const container = row.querySelector('.template-mounts-container');
    if (container) container.appendChild(createMountRow(idx));
}

function detectVariables(row) {
    const idx = parseInt(row.getAttribute('data-template-index'), 10);
    const sourceRadio = row.querySelector('input[data-field="source"]:checked');
    const source = sourceRadio ? sourceRadio.value : 'git';
    const container = row.querySelector('.template-variables-container');
    const detectBtn = row.querySelector('.btn-detect-variables');
    if (!container || !detectBtn) return;

    detectBtn.disabled = true;
    detectBtn.textContent = 'Detecting...';

    const formData = new FormData();
    formData.append('source', source);

    if (source === 'upload') {
        const fileInput = row.querySelector('input[data-field="file"]');
        if (!fileInput || !fileInput.files || !fileInput.files[0]) {
            detectBtn.disabled = false;
            detectBtn.textContent = 'Detect Variables';
            alert('Please select a template file first.');
            return;
        }
        formData.append('template_file', fileInput.files[0]);
    } else if (source === 'git') {
        const repoInput = row.querySelector('input[data-field="git_repo"]');
        const folderInput = row.querySelector('input[data-field="git_folder"]');
        const branchInput = row.querySelector('input[data-field="git_branch"]');
        if (!repoInput || !repoInput.value) {
            detectBtn.disabled = false;
            detectBtn.textContent = 'Detect Variables';
            alert('Please enter a Git repository URL first.');
            return;
        }
        formData.append('git_repo', repoInput.value);
        if (folderInput && folderInput.value) formData.append('git_folder', folderInput.value);
        if (branchInput && branchInput.value) formData.append('git_branch', branchInput.value);
    }

    fetch('/api/templates/detect-variables', {
        method: 'POST',
        body: formData
    })
    .then(resp => {
        if (!resp.ok) {
            return resp.json().then(data => { throw new Error(data.message || 'Detection failed'); });
        }
        return resp.json();
    })
    .then(variables => {
        container.innerHTML = '';
        if (!variables || variables.length === 0) {
            const empty = document.createElement('div');
            empty.className = 'detect-variables-status';
            empty.textContent = 'No Terraform variables found in this template.';
            container.appendChild(empty);
        } else {
            variables.forEach(v => {
                container.appendChild(createVariableRow(idx, v.name, v.default || '', v.description || '', v.required));
            });
        }
    })
    .catch(err => {
        const errDiv = document.createElement('div');
        errDiv.className = 'detect-variables-status error-message';
        errDiv.textContent = 'Detection failed: ' + err.message;
        container.appendChild(errDiv);
    })
    .finally(() => {
        detectBtn.disabled = false;
        detectBtn.textContent = 'Detect Variables';
    });
}

function updateRowSourceVisibility(row) {
    const sourceRadios = row.querySelectorAll('input[data-field="source"]');
    const selectedSource = Array.from(sourceRadios).find(r => r.checked)?.value;
    const uploadSection = row.querySelector('.template-upload-section');
    const gitSection = row.querySelector('.template-git-section');
    const fileInput = row.querySelector('input[data-field="file"]');
    const gitRepoInput = row.querySelector('input[data-field="git_repo"]');
    const sourceButtons = row.querySelectorAll('.source-button[data-source]');

    sourceButtons.forEach(btn => {
        if (btn.getAttribute('data-source') === selectedSource) {
            btn.classList.add('selected');
        } else {
            btn.classList.remove('selected');
        }
    });

    if (selectedSource === 'upload') {
        if (uploadSection) uploadSection.style.display = 'block';
        if (gitSection) gitSection.style.display = 'none';
        if (fileInput) fileInput.setAttribute('required', 'required');
        if (gitRepoInput) gitRepoInput.removeAttribute('required');
    } else if (selectedSource === 'git') {
        if (uploadSection) uploadSection.style.display = 'none';
        if (gitSection) gitSection.style.display = 'block';
        if (fileInput) fileInput.removeAttribute('required');
        if (gitRepoInput) gitRepoInput.setAttribute('required', 'required');
    } else {
        if (uploadSection) uploadSection.style.display = 'none';
        if (gitSection) gitSection.style.display = 'none';
        if (fileInput) fileInput.removeAttribute('required');
        if (gitRepoInput) gitRepoInput.removeAttribute('required');
    }
}

function initTemplateRowHandlers() {
    templatesContainer.querySelectorAll('.template-row').forEach(row => {
        const sourceButtons = row.querySelectorAll('.source-button[data-source]');
        sourceButtons.forEach(btn => {
            btn.replaceWith(btn.cloneNode(true));
        });
        row.querySelectorAll('.source-button[data-source]').forEach(btn => {
            btn.addEventListener('click', function() {
                const source = this.getAttribute('data-source');
                const radio = row.querySelector('input[data-field="source"][value="' + source + '"]');
                if (radio) {
                    radio.checked = true;
                    updateRowSourceVisibility(row);
                }
            });
        });

        const removeBtn = row.querySelector('.btn-remove-template');
        if (removeBtn) {
            removeBtn.replaceWith(removeBtn.cloneNode(true));
            row.querySelector('.btn-remove-template').addEventListener('click', function() {
                const rows = templatesContainer.querySelectorAll('.template-row');
                if (rows.length <= 1) return;
                row.remove();
                reindexTemplateRows();
            });
        }

        const fileInput = row.querySelector('input[data-field="file"]');
        const fileDisplay = row.querySelector('.file-name-display');
        const fileLabel = row.querySelector('.file-upload-label');
        if (fileInput && fileDisplay) {
            fileInput.addEventListener('change', function() {
                const file = this.files[0];
                fileDisplay.textContent = file ? 'Selected: ' + file.name : '';
                fileDisplay.style.display = file ? 'block' : 'none';
            });
        }
        if (fileLabel && fileInput) {
            ['dragenter', 'dragover', 'dragleave', 'drop'].forEach(ev => {
                fileLabel.addEventListener(ev, e => { e.preventDefault(); e.stopPropagation(); }, false);
            });
            fileLabel.addEventListener('drop', function(e) {
                const files = e.dataTransfer.files;
                if (files.length > 0) {
                    fileInput.files = files;
                    fileInput.dispatchEvent(new Event('change', { bubbles: true }));
                }
            }, false);
        }

        const detectBtn = row.querySelector('.btn-detect-variables');
        if (detectBtn) {
            detectBtn.replaceWith(detectBtn.cloneNode(true));
            row.querySelector('.btn-detect-variables').addEventListener('click', function() {
                detectVariables(row);
            });
        }

        const addVarBtn = row.querySelector('.btn-add-variable');
        if (addVarBtn) {
            addVarBtn.replaceWith(addVarBtn.cloneNode(true));
            row.querySelector('.btn-add-variable').addEventListener('click', function() {
                addVariableRow(row);
            });
        }

        const addSidecarBtn = row.querySelector('.btn-add-sidecar');
        if (addSidecarBtn) {
            addSidecarBtn.replaceWith(addSidecarBtn.cloneNode(true));
            row.querySelector('.btn-add-sidecar').addEventListener('click', function() {
                addSidecarRow(row);
            });
        }

        const addMountBtn = row.querySelector('.btn-add-mount');
        if (addMountBtn) {
            addMountBtn.replaceWith(addMountBtn.cloneNode(true));
            row.querySelector('.btn-add-mount').addEventListener('click', function() {
                addMountRow(row);
            });
        }
    });
}

if (btnAddTemplate && templateRowTmpl) {
    btnAddTemplate.addEventListener('click', function() {
        const clone = templateRowTmpl.content.cloneNode(true);
        const newRow = clone.querySelector('.template-row');
        const nextIdx = templatesContainer.querySelectorAll('.template-row').length;
        newRow.querySelectorAll('[name]').forEach(el => {
            let n = el.getAttribute('name');
            if (n) {
                n = n.replace('template_file_0', 'template_file_' + nextIdx).replace('template_0_', 'template_' + nextIdx + '_');
                el.setAttribute('name', n);
            }
        });
        newRow.querySelectorAll('input[data-field="file"]').forEach(el => {
            el.setAttribute('id', 'template_file_' + nextIdx);
        });
        newRow.querySelectorAll('label[for="template_file_0"]').forEach(el => {
            el.setAttribute('for', 'template_file_' + nextIdx);
        });
        newRow.querySelector('input[data-field="name"]').value = '';
        newRow.querySelector('input[data-field="git_branch"]').value = 'main';
        templatesContainer.appendChild(newRow);
        reindexTemplateRows();
        updateRowSourceVisibility(newRow);
        // The clone ships with a bare picker; fill it from the defined credentials.
        refreshGitCredentialOptions();
    });
}

document.addEventListener('DOMContentLoaded', function() {
    initTemplateRowHandlers();
    templatesContainer.querySelectorAll('.template-row').forEach(row => updateRowSourceVisibility(row));
    refreshGitCredentialOptions();
    refreshRegistryCredentialOptions();
});

// Workspace templates: three ways to define the workspace — a field builder, a
// devcontainer importer, or raw YAML. The server only distinguishes form from
// yaml; the devcontainer path resolves to yaml (it generates a YAML document that
// the admin reviews in the editor), so templates_mode only ever holds those two.
const templatesModeInput = document.getElementById('templates_mode');
const templatesFormMode = document.getElementById('templates-form-mode');
const templatesDevcontainerMode = document.getElementById('templates-devcontainer-mode');
const templatesYamlMode = document.getElementById('templates-yaml-mode');
const templatesYamlTextarea = document.getElementById('templates_yaml');
const templatesModeFormBtn = document.getElementById('templates-mode-form');
const templatesModeDevcontainerBtn = document.getElementById('templates-mode-devcontainer');
const templatesModeYamlBtn = document.getElementById('templates-mode-yaml');

// Collects the wizard's template fields to seed the editor. Must run before the
// inputs are disabled: FormData skips disabled fields.
function templatesFormData() {
    const data = new FormData();
    if (!templatesFormMode) {
        return data;
    }
    templatesFormMode.querySelectorAll('[name^="template_"]').forEach(el => {
        if (el.type === 'file' || el.disabled) return;
        if ((el.type === 'checkbox' || el.type === 'radio') && !el.checked) return;
        data.append(el.getAttribute('name'), el.value);
    });
    return data;
}

function seedTemplatesYaml() {
    return fetch('/api/labs/templates/yaml', { method: 'POST', body: templatesFormData() })
        .then(response => response.ok ? response.text() : Promise.reject(new Error('seed failed')))
        .then(text => { templatesYamlTextarea.value = text; })
        .catch(() => { /* Leave the editor empty — "Insert skeleton" is still available. */ });
}

function setTemplatesMode(mode) {
    const useForm = mode === 'form';
    // Only the form builder submits the wizard fields; both other paths submit YAML.
    if (templatesModeInput) templatesModeInput.value = useForm ? 'form' : 'yaml';
    if (templatesModeFormBtn) templatesModeFormBtn.classList.toggle('selected', useForm);
    if (templatesModeDevcontainerBtn) templatesModeDevcontainerBtn.classList.toggle('selected', mode === 'devcontainer');
    if (templatesModeYamlBtn) templatesModeYamlBtn.classList.toggle('selected', mode === 'yaml');
    if (templatesFormMode) templatesFormMode.style.display = useForm ? '' : 'none';
    if (templatesDevcontainerMode) templatesDevcontainerMode.style.display = mode === 'devcontainer' ? '' : 'none';
    if (templatesYamlMode) templatesYamlMode.style.display = mode === 'yaml' ? '' : 'none';
    // Hiding a section is not enough: its inputs would still be submitted, and a
    // `required` field that is hidden but empty blocks submission with an error the
    // admin cannot see (the browser cannot focus a display:none control to report
    // it). Disabling takes the inactive section's inputs out of the form entirely.
    // The devcontainer section carries a required template name, so it must be
    // disabled whenever the admin is in the form or yaml editor.
    if (templatesFormMode) {
        templatesFormMode.querySelectorAll('input, select, textarea').forEach(el => {
            el.disabled = !useForm;
        });
    }
    if (templatesDevcontainerMode) {
        templatesDevcontainerMode.querySelectorAll('input, select, textarea').forEach(el => {
            el.disabled = mode !== 'devcontainer';
        });
    }
    if (templatesYamlTextarea) templatesYamlTextarea.disabled = useForm;
}

if (templatesModeFormBtn) {
    templatesModeFormBtn.addEventListener('click', () => setTemplatesMode('form'));
}
if (templatesModeDevcontainerBtn) {
    templatesModeDevcontainerBtn.addEventListener('click', function() {
        // The form builder's first template usually already points at the workshop repo.
        const repoField = document.getElementById('devcontainer_git_repo');
        const formRepo = document.querySelector('[name="template_0_git_repo"]');
        if (repoField && !repoField.value && formRepo && formRepo.value) repoField.value = formRepo.value;
        setTemplatesMode('devcontainer');
    });
}
if (templatesModeYamlBtn) {
    templatesModeYamlBtn.addEventListener('click', function() {
        const seeded = templatesYamlTextarea && !templatesYamlTextarea.value.trim()
            ? seedTemplatesYaml()
            : Promise.resolve();
        seeded.then(() => setTemplatesMode('yaml'));
    });
}

const btnSkeletonTemplatesYaml = document.getElementById('btn-skeleton-templates-yaml');
if (btnSkeletonTemplatesYaml) {
    btnSkeletonTemplatesYaml.addEventListener('click', function() {
        if (templatesYamlTextarea.value.trim() &&
            !confirm('Replace the current YAML with the commented skeleton?')) {
            return;
        }
        // Posting no template fields makes the server return the skeleton.
        fetch('/api/labs/templates/yaml', { method: 'POST', body: new FormData() })
            .then(response => response.ok ? response.text() : Promise.reject(new Error('skeleton failed')))
            .then(text => { templatesYamlTextarea.value = text; })
            .catch(() => { /* Nothing to insert; leave what the admin has. */ });
    });
}

const btnUploadTemplatesYaml = document.getElementById('btn-upload-templates-yaml');
const templatesYamlFileInput = document.getElementById('templates_yaml_file');
if (btnUploadTemplatesYaml && templatesYamlFileInput) {
    btnUploadTemplatesYaml.addEventListener('click', () => templatesYamlFileInput.click());
    templatesYamlFileInput.addEventListener('change', function(e) {
        const file = e.target.files[0];
        if (!file) return;
        const reader = new FileReader();
        reader.onload = evt => { templatesYamlTextarea.value = evt.target.result; };
        reader.readAsText(file);
        // Let the same file be picked again after an edit.
        e.target.value = '';
    });
}

// Workspace templates: import from a workshop repo's devcontainer.json. The
// translation happens here, at authoring time, so the admin can see and edit what
// a student will get — and so the keys the builder cannot honour are reported
// while there is still something to do about them.
const devcontainerImportResult = document.getElementById('devcontainer-import-result');
const devcontainerUploadRow = document.getElementById('devcontainer-upload-row');
const devcontainerSourceGitBtn = document.getElementById('devcontainer-source-git');
const devcontainerSourceUploadBtn = document.getElementById('devcontainer-source-upload');
let devcontainerSource = 'git';

// Whether the devcontainer is read from the workshop repo (git_repo) or a
// separate, shared config repo — orthogonal to devcontainerSource (git/upload),
// and only meaningful for the "git" source: an upload already supplies the
// devcontainer.json directly, so there is nothing else to clone.
const devcontainerConfigSourceRow = document.getElementById('devcontainer-config-source-row');
const devcontainerConfigRepoRow = document.getElementById('devcontainer-config-repo-row');
const devcontainerConfigSourceSameBtn = document.getElementById('devcontainer-config-source-same');
const devcontainerConfigSourceSeparateBtn = document.getElementById('devcontainer-config-source-separate');
let devcontainerConfigSource = 'same';

function setDevcontainerConfigSource(source) {
    devcontainerConfigSource = source === 'separate' ? 'separate' : 'same';
    if (devcontainerConfigSourceSameBtn) devcontainerConfigSourceSameBtn.classList.toggle('selected', devcontainerConfigSource === 'same');
    if (devcontainerConfigSourceSeparateBtn) devcontainerConfigSourceSeparateBtn.classList.toggle('selected', devcontainerConfigSource === 'separate');
    if (devcontainerConfigRepoRow) devcontainerConfigRepoRow.style.display = devcontainerConfigSource === 'separate' ? '' : 'none';
}

if (devcontainerConfigSourceSameBtn) {
    devcontainerConfigSourceSameBtn.addEventListener('click', () => setDevcontainerConfigSource('same'));
}
if (devcontainerConfigSourceSeparateBtn) {
    devcontainerConfigSourceSeparateBtn.addEventListener('click', () => setDevcontainerConfigSource('separate'));
}

function setDevcontainerSource(source) {
    devcontainerSource = source === 'upload' ? 'upload' : 'git';
    if (devcontainerSourceGitBtn) devcontainerSourceGitBtn.classList.toggle('selected', devcontainerSource === 'git');
    if (devcontainerSourceUploadBtn) devcontainerSourceUploadBtn.classList.toggle('selected', devcontainerSource === 'upload');
    if (devcontainerUploadRow) devcontainerUploadRow.style.display = devcontainerSource === 'upload' ? '' : 'none';
    // The config-repo choice only applies when reading from git; an upload is
    // already the devcontainer.json, so reset back to "same" underneath it.
    if (devcontainerConfigSourceRow) devcontainerConfigSourceRow.style.display = devcontainerSource === 'upload' ? 'none' : '';
    if (devcontainerSource === 'upload') setDevcontainerConfigSource('same');
}

if (devcontainerSourceGitBtn) {
    devcontainerSourceGitBtn.addEventListener('click', () => setDevcontainerSource('git'));
}
if (devcontainerSourceUploadBtn) {
    devcontainerSourceUploadBtn.addEventListener('click', () => setDevcontainerSource('upload'));
}

// Once the import succeeds the generated YAML is waiting in the editor; this button
// takes the admin there to review and tweak it before creating the lab. It stays
// hidden until there is something to review.
const btnDevcontainerReviewYaml = document.getElementById('btn-devcontainer-review-yaml');
if (btnDevcontainerReviewYaml) {
    btnDevcontainerReviewYaml.addEventListener('click', () => setTemplatesMode('yaml'));
}

// The import result renders values that came out of a workshop's own
// devcontainer.json (image names, feature refs, its name), so it is not trusted
// markup.
function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text == null ? '' : text;
    return div.innerHTML;
}

function devcontainerImportMessage(kind, text) {
    if (!devcontainerImportResult) return;
    devcontainerImportResult.innerHTML =
        `<div class="toast toast--inline toast-${kind}"><span>${escapeHtml(text)}</span></div>`;
}

// renderDevcontainerImport reports what the builder will produce, and every key
// it will ignore. The warning list is the point of importing: it is cheaper to
// learn here than from a student halfway through a workshop.
function renderDevcontainerImport(data) {
    if (!devcontainerImportResult) return;

    const base = data.base || {};
    let summary = 'no image or Dockerfile — the fallback image is used';
    if (base.kind === 'image') summary = `image <code>${escapeHtml(base.image)}</code>`;
    else if (base.kind === 'dockerfile') summary = `built from <code>${escapeHtml(base.dockerfile)}</code>`;

    let html = '<div class="toast toast--inline toast-success"><span>' +
        `Imported <code>${escapeHtml(data.path || 'devcontainer.json')}</code> — ${summary}.` +
        '</span></div>';

    if (data.features && data.features.length) {
        html += '<p class="devcontainer-import-note">Features built into the workspace: ' +
            data.features.map(f => `<code>${escapeHtml(f)}</code>`).join(', ') + '</p>';
    }

    if (data.warnings && data.warnings.length) {
        html += '<p class="devcontainer-import-note">These parts of the devcontainer will not take effect:</p><ul class="devcontainer-import-warnings">';
        data.warnings.forEach(w => {
            html += `<li><code>${escapeHtml(w.key)}</code> — ${escapeHtml(w.message)}</li>`;
        });
        html += '</ul>';
    }

    devcontainerImportResult.innerHTML = html;
}

const btnRunDevcontainerImport = document.getElementById('btn-run-devcontainer-import');
if (btnRunDevcontainerImport) {
    btnRunDevcontainerImport.addEventListener('click', function() {
        const fileInput = document.getElementById('devcontainer_file');
        // Asked for rather than derived from the devcontainer: its "name" is a display
        // string many repos leave at a scaffolded default, which would give every
        // imported template the same name.
        const templateName = ((document.getElementById('devcontainer_template_name') || {}).value || '').trim();
        if (!templateName) {
            devcontainerImportMessage('error', 'Template name is required.');
            return;
        }
        const body = new FormData();
        body.append('template_name', templateName);
        body.append('template_description', (document.getElementById('devcontainer_template_description') || {}).value || '');
        body.append('source', devcontainerSource);
        body.append('git_repo', (document.getElementById('devcontainer_git_repo') || {}).value || '');
        body.append('git_branch', (document.getElementById('devcontainer_git_branch') || {}).value || '');
        body.append('devcontainer_dir', (document.getElementById('devcontainer_dir') || {}).value || '');
        body.append('cache_repo', (document.getElementById('devcontainer_cache_repo') || {}).value || '');
        body.append('cpu', (document.getElementById('devcontainer_cpu') || {}).value || '');
        body.append('cpu_limit', (document.getElementById('devcontainer_cpu_limit') || {}).value || '');
        body.append('memory', (document.getElementById('devcontainer_memory') || {}).value || '');
        body.append('memory_limit', (document.getElementById('devcontainer_memory_limit') || {}).value || '');
        // The registry credential each student's workspace pulls the private base image
        // (and pushes the layer cache) with, baked into the generated template. Empty
        // means "auto": with a single registry credential, resolve it here so the common
        // case needs no choice, mirroring the git credential below.
        let registryAuthSecret = (document.getElementById('devcontainer_registry_auth_secret') || {}).value || '';
        if (!registryAuthSecret) {
            const rnames = registryCredentialNames();
            if (rnames.length === 1) registryAuthSecret = rnames[0];
        }
        body.append('registry_auth_secret', registryAuthSecret);
        // The credential the students' workspaces clone with, baked into the generated
        // template. Empty means "auto": with a single git credential, resolve it here so
        // the common case needs no choice, mirroring the form path's picker.
        let gitAuthSecret = (document.getElementById('devcontainer_git_auth_secret') || {}).value || '';
        if (!gitAuthSecret) {
            const names = gitCredentialNames();
            if (names.length === 1) gitAuthSecret = names[0];
        }
        body.append('git_auth_secret', gitAuthSecret);
        // Read the private repo with the same git credential the students get: look up
        // the resolved credential's username/token from the Credentials section. Empty
        // when none is defined, so a public repo still clones anonymously. Request-scoped
        // on the server — used for this clone only and never persisted.
        const gitAuth = gitCredentialAuthByName(gitAuthSecret);
        body.append('git_username', gitAuth.username);
        body.append('git_token', gitAuth.token);

        // When the devcontainer lives in a separate repo, the import reads from
        // that repo instead of git_repo — git_repo above stays the generated
        // template's content repo either way. Left empty in "same repo" mode, so
        // the server falls back to reading git_repo as it always has.
        if (devcontainerSource === 'git' && devcontainerConfigSource === 'separate') {
            body.append('devcontainer_config_repo', (document.getElementById('devcontainer_config_repo') || {}).value || '');
            body.append('devcontainer_config_branch', (document.getElementById('devcontainer_config_branch') || {}).value || '');
            let configAuthSecret = (document.getElementById('devcontainer_config_auth_secret') || {}).value || '';
            if (!configAuthSecret) {
                const names = gitCredentialNames();
                if (names.length === 1) configAuthSecret = names[0];
            }
            body.append('devcontainer_config_auth_secret', configAuthSecret);
            const configAuth = gitCredentialAuthByName(configAuthSecret);
            body.append('devcontainer_config_username', configAuth.username);
            body.append('devcontainer_config_token', configAuth.token);
        }

        if (devcontainerSource === 'upload') {
            const file = fileInput && fileInput.files[0];
            if (!file) {
                devcontainerImportMessage('error', 'Choose a devcontainer.json or a repository .zip to upload.');
                return;
            }
            body.append('devcontainer_file', file);
        }

        devcontainerImportMessage('success', 'Reading the devcontainer…');
        btnRunDevcontainerImport.disabled = true;

        fetch('/api/templates/detect-devcontainer', { method: 'POST', body: body })
            .then(response => response.json().then(data => ({ ok: response.ok, data: data })))
            .then(({ ok, data }) => {
                if (!ok) {
                    devcontainerImportMessage('error', data.message || 'Could not read the devcontainer.');
                    return;
                }
                if (templatesYamlTextarea) templatesYamlTextarea.value = data.templates_yaml || '';
                renderDevcontainerImport(data);
                // The YAML is now populated — surface the way to go review it.
                if (btnDevcontainerReviewYaml) btnDevcontainerReviewYaml.style.display = '';
            })
            .catch(() => devcontainerImportMessage('error', 'Could not reach the server.'))
            .finally(() => { btnRunDevcontainerImport.disabled = false; });
    });
}

document.addEventListener('DOMContentLoaded', function() {
    setTemplatesMode(templatesModeInput ? templatesModeInput.value : 'form');
    setDevcontainerSource('git');
});

// setFieldValue assigns a form field's value if the element exists and the
// value is not null/undefined, leaving the field's default otherwise.
function setFieldValue(id, value) {
    if (value === undefined || value === null) return;
    const el = document.getElementById(id);
    if (el) el.value = value;
}

// applyPrefillDeletionDate splits a stored RFC3339 deletion timestamp into the
// wizard's separate date/time fields, unless it has already passed — a past
// date would only schedule the (re-)created lab for deletion on the very next
// cleanup tick.
function applyPrefillDeletionDate(isoDate) {
    if (!isoDate) return;
    const d = new Date(isoDate);
    if (isNaN(d.getTime()) || d <= new Date()) return;

    const pad = n => String(n).padStart(2, '0');
    setFieldValue('lab_deletion_date', d.getFullYear() + '-' + pad(d.getMonth() + 1) + '-' + pad(d.getDate()));
    setFieldValue('lab_deletion_time', pad(d.getHours()) + ':' + pad(d.getMinutes()));
}

// showPrefillNotice reveals the "editing a copy of an existing lab" banner.
function showPrefillNotice(jobId, action) {
    const notice = document.getElementById('prefill-notice');
    if (!notice) return;
    const actionEl = document.getElementById('prefill-notice-action');
    const actionEl2 = document.getElementById('prefill-notice-action-2');
    const jobIdEl = document.getElementById('prefill-notice-job-id');
    if (actionEl) actionEl.textContent = action === 'retry' ? 'retry' : 'recreate';
    if (actionEl2) actionEl2.textContent = action === 'retry' ? 'retrying' : 'recreating';
    if (jobIdEl) jobIdEl.textContent = jobId;
    notice.style.display = 'block';
}

// applyPrefill seeds the lab creation wizard from an existing (failed or
// destroyed) lab's stored configuration, so it can be edited before retrying
// or recreating it. Secrets never round-trip here: the sanitized job JSON this
// is seeded from always strips the BYOK kubeconfig and DNS credentials, so
// those fields are left for the admin to fill in again (or leave blank to keep
// what a retry already has — see the hints shown next to each).
async function applyPrefill(config, templatesYaml, jobId, action) {
    // Step 1: Setup
    setFieldValue('stack_name', config.stack_name);
    const stackNamePreview = document.getElementById('stack-name-preview');
    if (stackNamePreview && config.stack_name) stackNamePreview.textContent = config.stack_name;
    setFieldValue('description', config.description);
    wizard.setClusterMode(!!config.use_existing_cluster);

    if (config.use_existing_cluster) {
        wizard.retryEditKeepsKubeconfig = action === 'retry';
        const hint = document.getElementById('kubeconfig-prefill-hint');
        if (hint) {
            hint.textContent = action === 'retry'
                ? "Leave blank to keep the lab's existing kubeconfig."
                : 'The kubeconfig is not carried over when recreating — you must provide one to continue.';
            hint.style.display = '';
        }
    } else {
        // Steps 2 & 3: Network + Compute. Region/flavor selects are populated
        // asynchronously, so their values can only be set once loaded.
        const provider = config.provider || 'ovh';
        setFieldValue('provider', provider);
        handleProviderChange();

        if (provider === 'azure') {
            await loadAzureLocations();
            setFieldValue('azure_location', config.azure_location);
            await loadAzureVMSizes();
            setFieldValue('nodepool_flavor', config.nodepool_flavor);
        } else {
            await loadOVHRegions();
            setFieldValue('network_region', config.network_region);
            await loadOVHFlavors();
            setFieldValue('nodepool_flavor', config.nodepool_flavor);
        }

        setFieldValue('network_gateway_name', config.network_gateway_name);
        setFieldValue('network_gateway_model', config.network_gateway_model);
        setFieldValue('network_private_network_name', config.network_private_network_name);
        setFieldValue('network_id', config.network_id);
        setFieldValue('network_mask', config.network_mask);
        setFieldValue('network_start_ip', config.network_start_ip);
        setFieldValue('network_end_ip', config.network_end_ip);
        setFieldValue('k8s_cluster_name', config.k8s_cluster_name);
        setFieldValue('nodepool_name', config.nodepool_name);
        setFieldValue('nodepool_desired_node_count', config.nodepool_desired_node_count);
        setFieldValue('nodepool_min_node_count', config.nodepool_min_node_count);
        setFieldValue('nodepool_max_node_count', config.nodepool_max_node_count);
    }

    // Step 4: DNS & HTTPS
    wizard.setIngressMode(config.install_nginx_ingress === false ? 'existing' : 'install');
    setFieldValue('nginx_ingress_namespace', config.nginx_ingress_namespace);
    setFieldValue('nginx_ingress_service_name', config.nginx_ingress_service_name);

    wizard.setCertManagerMode(config.install_cert_manager === false ? 'existing' : 'install');
    setFieldValue('cert_manager_namespace', config.cert_manager_namespace);

    const domainMode = !config.domain ? 'quickstart' : (config.dns_provider ? 'auto' : 'manual');
    wizard.setDomainMode(domainMode); // clears domain/acme_email/wildcard_domain when quickstart
    if (domainMode !== 'quickstart') {
        setFieldValue('domain', config.domain);
        setFieldValue('acme_email', config.acme_email);
        setFieldValue('wildcard_domain', config.wildcard_domain);
        updateDNSManualWarning();
    }
    if (domainMode === 'auto') {
        setFieldValue('dns_provider', config.dns_provider);
        handleDNSProviderChange();
        setFieldValue('dns_zone', config.dns_zone);
        wizard.setDNSRecordMode(config.use_external_dns ? 'externaldns' : 'wildcard');

        const dnsHint = document.getElementById('dns-cred-prefill-hint');
        if (dnsHint) {
            dnsHint.textContent = action === 'retry'
                ? "Leave the credential fields below blank to keep the lab's existing DNS credentials."
                : 'DNS credentials are not carried over when recreating — leave the fields below blank for none, or fill them in again.';
            dnsHint.style.display = '';
        }
    }
    // Only meaningful when reusing an existing cert-manager with a DNS provider
    // selected; updateDNSAlreadyConfiguredVisibility() (called from the mode
    // setters above) already forces this false everywhere else.
    if (config.install_cert_manager === false && domainMode === 'auto') {
        wizard.setDNSAlreadyConfigured(!!config.dns_already_configured);
    }

    // Step 5: Workspace
    setFieldValue('workspace_namespace', config.workspace_namespace);

    // Step 6: Templates. The structured config already has everything the
    // form-mode builder would otherwise need reconstructed field by field
    // (including sidecars/mounts/env), so seed the YAML editor instead.
    setFieldValue('templates_yaml', templatesYaml);
    setTemplatesMode('yaml');

    // Step 7: Lifecycle
    setFieldValue('workspace_lifetime_hours', config.workspace_lifetime_hours);
    setFieldValue('workspace_lifetime_unit', 'hours');
    applyPrefillDeletionDate(config.lab_deletion_date);

    // Retry reuses the same Pulumi stack, so its name cannot change here, and
    // the wizard must submit to the edited-retry endpoint instead of plain lab
    // creation. Recreate keeps submitting to plain lab creation (POST
    // /api/labs), same as today's "Recreate as-is" — a new job either way.
    if (action === 'retry') {
        const stackNameInput = document.getElementById('stack_name');
        if (stackNameInput) stackNameInput.readOnly = true;

        const form = document.getElementById('lab-form');
        if (form) {
            const target = '/api/labs/' + encodeURIComponent(jobId) + '/retry-with-config';
            form.setAttribute('action', target); // non-JS fallback only
            window.retryWithConfigTarget = target; // what htmx:configRequest actually uses
        }

        // Dry run always creates a separate preview job through a different
        // endpoint — not what "editing before a retry" means. Hide it (on every
        // step, not just now — see updateUI()), and relabel the submit button
        // for what it actually does here.
        wizard.hideDryRunButton = true;
        const dryRunBtn = document.getElementById('btn-dry-run');
        if (dryRunBtn) dryRunBtn.style.display = 'none';
        const submitBtn = document.getElementById('btn-submit');
        if (submitBtn) submitBtn.textContent = 'Retry Lab';
    }

    showPrefillNotice(jobId, action);
}
