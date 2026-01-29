// Check credentials status
function checkCredentials() {
    const statusContainer = document.getElementById('status-container');
    const statusContent = document.getElementById('status-content');
    
    statusContainer.style.display = 'block';
    statusContent.innerHTML = '<p>Checking...</p>';
    
    fetch('/api/ovh-credentials')
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
                statusContent.innerHTML = `
                    <div class="success-message">
                        <h4>✅ Credentials Configured</h4>
                        <p>OVH credentials are stored in memory and ready to use.</p>
                        <ul>
                            <li>Service Name: <strong>${data.service_name || 'N/A'}</strong></li>
                            <li>Endpoint: <strong>${data.endpoint || 'N/A'}</strong></li>
                        </ul>
                        <p><small>Note: Credentials are stored in memory only and will be cleared on application restart.</small></p>
                    </div>
                `;
            } else {
                statusContent.innerHTML = `
                    <div class="error-message">
                        <h4>⚠️ No Credentials Configured</h4>
                        <p>Please configure your OVH credentials above before creating labs.</p>
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
function loadCurrentCredentials() {
    fetch('/api/ovh-credentials')
        .then(response => {
            if (response.ok) {
                return response.json();
            }
            return { configured: false };
        })
        .then(data => {
            if (data.configured) {
                // Pre-fill non-sensitive fields
                const serviceNameInput = document.getElementById('ovh_service_name');
                const endpointSelect = document.getElementById('ovh_endpoint');
                
                if (serviceNameInput && data.service_name) {
                    serviceNameInput.value = data.service_name;
                }
                
                if (endpointSelect && data.endpoint) {
                    endpointSelect.value = data.endpoint;
                }
                
                // Show info that credentials exist
                const formSection = document.querySelector('.form-section');
                if (formSection) {
                    const existingInfo = document.createElement('div');
                    existingInfo.className = 'info-box';
                    existingInfo.style.marginBottom = '1.5rem';
                    existingInfo.innerHTML = `
                        <h3>📝 Update Existing Credentials</h3>
                        <p>Credentials are currently configured. Fill in the fields below to update them.</p>
                        <p><strong>Service Name:</strong> ${data.service_name || 'N/A'}</p>
                        <p><strong>Endpoint:</strong> ${data.endpoint || 'N/A'}</p>
                    `;
                    formSection.insertBefore(existingInfo, formSection.firstChild);
                }
            }
        })
        .catch(error => {
            console.error('Error loading credentials:', error);
        });
}

// Check status on page load
document.addEventListener('DOMContentLoaded', function() {
    checkCredentials();
    loadCurrentCredentials();
});

// Handle form submission success
document.body.addEventListener('htmx:afterSwap', function(event) {
    if (event.detail.target.id === 'form-response') {
        const response = event.detail.target;
        const responseText = response.innerHTML.toLowerCase();
        if (responseText.includes('success') || responseText.includes('saved successfully')) {
            // Refresh status after successful save
            setTimeout(checkCredentials, 500);
            // Clear form fields after successful save
            document.getElementById('credentials-form').reset();
        }
    }
});

// Also handle HTMX errors to prevent navigation
document.body.addEventListener('htmx:responseError', function(event) {
    console.error('HTMX Error:', event.detail);
    const responseDiv = document.getElementById('form-response');
    if (responseDiv) {
        responseDiv.innerHTML = `
            <div class="error-message">
                <h3>Error</h3>
                <p>Failed to save credentials. Please check the server logs for details.</p>
            </div>
        `;
    }
    // Prevent default error handling that might cause navigation
    event.preventDefault();
    return false;
});

// Fallback form submission if HTMX is not available
document.addEventListener('DOMContentLoaded', function() {
    const form = document.getElementById('credentials-form');
    if (form) {
        form.addEventListener('submit', function(e) {
            // If HTMX is not loaded, use fallback submission
            if (typeof htmx === 'undefined') {
                e.preventDefault();
                
                const formData = new FormData(form);
                const responseDiv = document.getElementById('form-response');
                const loadingDiv = document.getElementById('loading');
                
                // Show loading indicator
                if (loadingDiv) {
                    loadingDiv.style.display = 'block';
                }
                
                // Submit form via fetch
                fetch('/api/ovh-credentials', {
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
                        setTimeout(checkCredentials, 500);
                        // Clear form fields after successful save
                        form.reset();
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
    }
});
