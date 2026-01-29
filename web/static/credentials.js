// Provider configuration - extensible for future providers
const providerConfig = {
    ovh: {
        name: "OVHcloud",
        enabled: true,
        statusFields: ['service_name', 'endpoint']
    },
    aws: {
        name: "Amazon Web Services",
        enabled: false,
        statusFields: ['region']
    },
    azure: {
        name: "Microsoft Azure",
        enabled: false,
        statusFields: []
    },
    gcp: {
        name: "Google Cloud Platform",
        enabled: false,
        statusFields: []
    }
};

// Current selected provider
let currentProvider = 'ovh';

// Switch provider section visibility
function switchProvider() {
    const providerSelect = document.getElementById('provider-select');
    const newProvider = providerSelect.value;
    
    // Hide all provider sections
    document.querySelectorAll('.provider-section').forEach(section => {
        section.style.display = 'none';
    });
    
    // Show selected provider section
    const selectedSection = document.getElementById(`provider-section-${newProvider}`);
    if (selectedSection) {
        selectedSection.style.display = 'block';
    }
    
    currentProvider = newProvider;
    
    // Check credentials for the selected provider
    checkCredentials(newProvider);
}

// Check credentials status for a specific provider
function checkCredentials(provider) {
    const statusContainer = document.getElementById(`status-container-${provider}`);
    const statusContent = document.getElementById(`status-content-${provider}`);
    
    if (!statusContainer || !statusContent) return;
    
    statusContainer.style.display = 'block';
    statusContent.innerHTML = '<p>Checking...</p>';
    
    fetch(`/api/credentials?provider=${provider}`)
        .then(response => {
            if (response.ok) {
                return response.json();
            } else if (response.status === 404) {
                return { configured: false };
            }
            throw new Error('Failed to check credentials');
        })
        .then(data => {
            if (data.configured) {
                let statusHTML = `
                    <div class="success-message">
                        <h4>✅ Credentials Configured</h4>
                        <p>${providerConfig[provider]?.name || provider} credentials are stored in memory and ready to use.</p>
                        <ul>`;
                
                // Display provider-specific status fields
                const fields = providerConfig[provider]?.statusFields || [];
                fields.forEach(field => {
                    if (data[field]) {
                        const label = field.replace(/_/g, ' ').replace(/\b\w/g, l => l.toUpperCase());
                        statusHTML += `<li>${label}: <strong>${data[field]}</strong></li>`;
                    }
                });
                
                statusHTML += `
                        </ul>
                        <p><small>Note: Credentials are stored in memory only and will be cleared on application restart.</small></p>
                    </div>`;
                statusContent.innerHTML = statusHTML;
            } else {
                statusContent.innerHTML = `
                    <div class="error-message">
                        <h4>⚠️ No Credentials Configured</h4>
                        <p>Please configure your ${providerConfig[provider]?.name || provider} credentials above before creating labs.</p>
                    </div>
                `;
            }
        })
        .catch(error => {
            statusContent.innerHTML = `
                <div class="error-message">
                    <h4>❌ Error</h4>
                    <p>${error.message}</p>
                </div>
            `;
        });
}

// Load current credentials into form if they exist
function loadCurrentCredentials(provider) {
    fetch(`/api/credentials?provider=${provider}`)
        .then(response => {
            if (response.ok) {
                return response.json();
            }
            return { configured: false };
        })
        .then(data => {
            if (data.configured) {
                // Pre-fill non-sensitive fields based on provider
                if (provider === 'ovh') {
                    const serviceNameInput = document.getElementById('ovh_service_name');
                    const endpointSelect = document.getElementById('ovh_endpoint');
                    
                    if (serviceNameInput && data.service_name) {
                        serviceNameInput.value = data.service_name;
                    }
                    
                    if (endpointSelect && data.endpoint) {
                        endpointSelect.value = data.endpoint;
                    }
                }
                // Future: Add handling for other providers here
                
                // Show info that credentials exist
                const formSection = document.querySelector(`#provider-section-${provider} .form-section`);
                if (formSection && !formSection.querySelector('.existing-creds-info')) {
                    const existingInfo = document.createElement('div');
                    existingInfo.className = 'info-box existing-creds-info';
                    existingInfo.style.marginBottom = '1.5rem';
                    
                    let infoHTML = `<h3>📝 Update Existing Credentials</h3>
                        <p>Credentials are currently configured. Fill in the fields below to update them.</p>`;
                    
                    if (provider === 'ovh') {
                        infoHTML += `<p><strong>Service Name:</strong> ${data.service_name || 'N/A'}</p>
                            <p><strong>Endpoint:</strong> ${data.endpoint || 'N/A'}</p>`;
                    }
                    
                    existingInfo.innerHTML = infoHTML;
                    formSection.insertBefore(existingInfo, formSection.firstChild.nextSibling);
                }
            }
        })
        .catch(error => {
            console.error('Error loading credentials:', error);
        });
}

// Initialize page
document.addEventListener('DOMContentLoaded', function() {
    // Check URL parameters for provider selection
    const urlParams = new URLSearchParams(window.location.search);
    const providerParam = urlParams.get('provider');
    
    if (providerParam && providerConfig[providerParam]) {
        document.getElementById('provider-select').value = providerParam;
        currentProvider = providerParam;
    }
    
    // Initialize provider view
    switchProvider();
    
    // Load current credentials for the selected provider
    loadCurrentCredentials(currentProvider);
});

// Handle form submission success
document.body.addEventListener('htmx:afterSwap', function(event) {
    const targetId = event.detail.target.id;
    if (targetId.startsWith('form-response-')) {
        const provider = targetId.replace('form-response-', '');
        const response = event.detail.target;
        const responseText = response.innerHTML.toLowerCase();
        if (responseText.includes('success') || responseText.includes('saved successfully')) {
            // Refresh status after successful save
            setTimeout(() => checkCredentials(provider), 500);
            // Clear form fields after successful save
            const form = document.getElementById(`credentials-form-${provider}`);
            if (form) {
                // Only clear password fields, keep non-sensitive fields
                form.querySelectorAll('input[type="password"]').forEach(input => {
                    input.value = '';
                });
            }
        }
    }
});

// Handle HTMX errors
document.body.addEventListener('htmx:responseError', function(event) {
    console.error('HTMX Error:', event.detail);
    const targetId = event.detail.target?.id;
    if (targetId && targetId.startsWith('form-response-')) {
        const responseDiv = event.detail.target;
        if (responseDiv) {
            responseDiv.innerHTML = `
                <div class="error-message">
                    <h3>Error</h3>
                    <p>Failed to save credentials. Please check the server logs for details.</p>
                </div>
            `;
        }
    }
    event.preventDefault();
    return false;
});

// Fallback form submission if HTMX is not available
document.addEventListener('DOMContentLoaded', function() {
    document.querySelectorAll('form[id^="credentials-form-"]').forEach(form => {
        form.addEventListener('submit', function(e) {
            // If HTMX is not loaded, use fallback submission
            if (typeof htmx === 'undefined') {
                e.preventDefault();
                
                const provider = form.id.replace('credentials-form-', '');
                const formData = new FormData(form);
                const responseDiv = document.getElementById(`form-response-${provider}`);
                const loadingDiv = document.getElementById(`loading-${provider}`);
                
                // Show loading indicator
                if (loadingDiv) {
                    loadingDiv.style.display = 'block';
                }
                
                // Submit form via fetch
                fetch('/api/credentials', {
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
                    
                    // Check if save was successful
                    const responseText = html.toLowerCase();
                    if (responseText.includes('success') || responseText.includes('saved successfully')) {
                        // Refresh status after successful save
                        setTimeout(() => checkCredentials(provider), 500);
                        // Clear password fields after successful save
                        form.querySelectorAll('input[type="password"]').forEach(input => {
                            input.value = '';
                        });
                    }
                })
                .catch(error => {
                    console.error('Error submitting form:', error);
                    if (responseDiv) {
                        responseDiv.innerHTML = `
                            <div class="error-message">
                                <h3>Error</h3>
                                <p>Failed to save credentials: ${error.message}</p>
                            </div>
                        `;
                    }
                    if (loadingDiv) {
                        loadingDiv.style.display = 'none';
                    }
                });
                
                return false;
            }
            // Let HTMX handle the submission if available
        });
    });
});
