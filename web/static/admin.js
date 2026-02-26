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

// Template source selection handler with button group
const templateSourceRadios = document.querySelectorAll('input[name="template_source"]');
const sourceButtons = document.querySelectorAll('.source-button[data-source]');
const templateUploadSection = document.getElementById('template_upload_section');
const templateGitSection = document.getElementById('template_git_section');
const templateFileInput = document.getElementById('template_file');
const templateGitRepoInput = document.getElementById('template_git_repo');

function updateButtonStates(selectedSource) {
    // Update button visual states
    sourceButtons.forEach(button => {
        const buttonSource = button.getAttribute('data-source');
        if (buttonSource === selectedSource) {
            button.classList.add('selected');
        } else {
            button.classList.remove('selected');
        }
    });
}

function updateTemplateSourceVisibility() {
    const selectedSource = document.querySelector('input[name="template_source"]:checked')?.value;
    
    // Update button visual states
    updateButtonStates(selectedSource);
    
    if (selectedSource === 'git') {
        // Show Git section, hide upload section
        templateUploadSection.style.display = 'none';
        templateGitSection.style.display = 'block';
        // Update required attributes
        if (templateFileInput) {
            templateFileInput.removeAttribute('required');
        }
        if (templateGitRepoInput) {
            templateGitRepoInput.setAttribute('required', 'required');
        }
    } else if (selectedSource === 'upload') {
        // Show upload section, hide Git section
        templateUploadSection.style.display = 'block';
        templateGitSection.style.display = 'none';
        // Update required attributes
        if (templateFileInput) {
            templateFileInput.setAttribute('required', 'required');
        }
        if (templateGitRepoInput) {
            templateGitRepoInput.removeAttribute('required');
        }
    } else {
        // No selection - hide both sections
        templateUploadSection.style.display = 'none';
        templateGitSection.style.display = 'none';
        // Remove required attributes
        if (templateFileInput) {
            templateFileInput.removeAttribute('required');
        }
        if (templateGitRepoInput) {
            templateGitRepoInput.removeAttribute('required');
        }
    }
}

// Add event listeners to source buttons
sourceButtons.forEach(button => {
    button.addEventListener('click', function() {
        const source = this.getAttribute('data-source');
        const radio = document.getElementById('template_source_' + source);
        
        if (radio) {
            // Check the corresponding radio button
            radio.checked = true;
            // Trigger change event for any listeners
            radio.dispatchEvent(new Event('change', { bubbles: true }));
            // Update visibility
            updateTemplateSourceVisibility();
        }
    });
});

// Also listen to radio changes (for form reset or programmatic changes)
templateSourceRadios.forEach(radio => {
    radio.addEventListener('change', updateTemplateSourceVisibility);
});

// Initialize visibility on page load (both sections hidden by default)
updateTemplateSourceVisibility();

// File upload display handler with drag and drop support
const fileInput = document.getElementById('template_file');
const fileNameDisplay = document.getElementById('file-name-display');
const fileUploadLabel = document.querySelector('.file-upload-label');

if (fileInput && fileNameDisplay && fileUploadLabel) {
    // Handle file selection
    fileInput.addEventListener('change', function(e) {
        const file = e.target.files[0];
        if (file) {
            fileNameDisplay.textContent = `Selected: ${file.name}`;
            fileNameDisplay.style.display = 'block';
        } else {
            fileNameDisplay.style.display = 'none';
        }
    });

    // Drag and drop support
    ['dragenter', 'dragover', 'dragleave', 'drop'].forEach(eventName => {
        fileUploadLabel.addEventListener(eventName, preventDefaults, false);
    });

    function preventDefaults(e) {
        e.preventDefault();
        e.stopPropagation();
    }

    ['dragenter', 'dragover'].forEach(eventName => {
        fileUploadLabel.addEventListener(eventName, function() {
            fileUploadLabel.style.borderColor = 'var(--primary-color)';
            fileUploadLabel.style.background = 'rgba(37, 99, 235, 0.1)';
        }, false);
    });

    ['dragleave', 'drop'].forEach(eventName => {
        fileUploadLabel.addEventListener(eventName, function() {
            fileUploadLabel.style.borderColor = 'var(--border)';
            fileUploadLabel.style.background = 'var(--surface)';
        }, false);
    });

    fileUploadLabel.addEventListener('drop', function(e) {
        const dt = e.dataTransfer;
        const files = dt.files;
        if (files.length > 0) {
            fileInput.files = files;
            const event = new Event('change', { bubbles: true });
            fileInput.dispatchEvent(event);
        }
    }, false);
}
