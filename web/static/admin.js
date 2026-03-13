// Wizard functionality
const wizard = {
    currentStep: 1,
    useExistingCluster: false,
    clusterModeSelected: false,
    allSteps: [1, 2, 3, 4, 5, 6],
    byokSteps: [1, 2, 6],

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
            if (!hasFile && !hasContent) {
                alert('Please provide your kubeconfig content.');
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
        return valid;
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
            dryRunBtn.style.display = 'inline-flex';
        } else {
            nextBtn.style.display = 'inline-flex';
            submitBtn.style.display = 'none';
            dryRunBtn.style.display = 'none';
        }

        document.querySelector('.wizard-progress').scrollIntoView({ behavior: 'smooth', block: 'start' });

        // Fetch OVH regions when entering step 3 for the first time
        if (this.currentStep === 3 && !this._regionsLoaded) {
            this._regionsLoaded = true;
            loadOVHRegions();
        }
        // Fetch OVH flavors when entering step 5
        if (this.currentStep === 5) {
            loadOVHFlavors();
        }
    }
};

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

// Fetch available OVH regions and populate the region select
function loadOVHRegions() {
    const regionSelect = document.getElementById('network_region');
    if (!regionSelect) return;

    regionSelect.innerHTML = '<option value="">Loading regions…</option>';

    fetch('/api/ovh/regions')
        .then(response => {
            if (!response.ok) throw new Error('Failed to load regions');
            return response.text();
        })
        .then(html => {
            regionSelect.innerHTML = html;
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

    fetch(url)
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
    loadOVHFlavors();
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

    // Reload flavors when region selection changes
    const regionSelect = document.getElementById('network_region');
    if (regionSelect) {
        regionSelect.addEventListener('change', loadOVHFlavors);
    }

    // Reload flavors when flavor filter inputs change
    ['flavor_filter_min_vcpus', 'flavor_filter_max_vcpus', 'flavor_filter_min_ram', 'flavor_filter_max_ram'].forEach(function (id) {
        const el = document.getElementById(id);
        if (el) {
            el.addEventListener('change', loadOVHFlavors);
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
    const stackNameInput = document.getElementById('stack_name');
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
            
            fetch('/api/labs', {
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

// Multi-template: Add/Remove rows and per-row source selection
const templatesContainer = document.getElementById('coder-templates-container');
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
        newRow.querySelector('input[data-field="source"][value="git"]').checked = true;
        templatesContainer.appendChild(newRow);
        reindexTemplateRows();
        updateRowSourceVisibility(newRow);
    });
}

document.addEventListener('DOMContentLoaded', function() {
    initTemplateRowHandlers();
    templatesContainer.querySelectorAll('.template-row').forEach(row => updateRowSourceVisibility(row));
});
