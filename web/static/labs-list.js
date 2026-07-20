function destroyStack(labId) {
    // Send POST request to destroy endpoint
    fetch('/api/stacks/destroy', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/x-www-form-urlencoded',
        },
        body: 'job_id=' + encodeURIComponent(labId)
    })
    .then(response => {
        if (response.redirected) {
            // Follow the redirect
            window.location.href = response.url;
        } else {
            // Handle error
            console.error('Destroy failed:', response.status);
        }
    })
    .catch(error => {
        console.error('Destroy error:', error);
    });
}

function submitRecreate(body) {
    fetch('/api/labs/recreate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
        body: body
    })
    .then(response => {
        if (response.redirected) {
            window.location.href = response.url;
        } else {
            console.error('Recreate failed:', response.status);
        }
    })
    .catch(error => console.error('Recreate error:', error));
}

function recreateLab(labId) {
    // Recreation lands on a new cluster, so any credentials the lab's templates
    // reference must be supplied again. Ask the server which ones; prompt only if
    // there are any, otherwise recreate straight away.
    fetch('/api/labs/' + encodeURIComponent(labId) + '/recreate-credentials')
        .then(response => response.ok ? response.text() : '')
        .then(rows => {
            if (rows.trim() === '') {
                submitRecreate('job_id=' + encodeURIComponent(labId));
                return;
            }
            document.getElementById('recreate-job-id').value = labId;
            document.getElementById('recreate-credentials-rows').innerHTML = rows;
            openRecreateCredentialsModal();
        })
        .catch(error => {
            console.error('Could not load credentials for recreate:', error);
            // Fall back to recreating without the prompt rather than blocking.
            submitRecreate('job_id=' + encodeURIComponent(labId));
        });
}

function openRecreateCredentialsModal() {
    const overlay = document.getElementById('recreate-credentials-overlay');
    if (overlay) {
        overlay.classList.add('visible');
        overlay.setAttribute('aria-hidden', 'false');
    }
}

function closeRecreateCredentialsModal() {
    const overlay = document.getElementById('recreate-credentials-overlay');
    if (overlay) {
        overlay.classList.remove('visible');
        overlay.setAttribute('aria-hidden', 'true');
    }
}

document.addEventListener('DOMContentLoaded', function() {
    const form = document.getElementById('recreate-credentials-form');
    if (form) {
        form.addEventListener('submit', function(event) {
            event.preventDefault();
            submitRecreate(new URLSearchParams(new FormData(form)).toString());
        });
    }
});

function removeLab(labId) {
    if (!confirm('Remove this lab from the list? This action cannot be undone.')) return;
    fetch('/api/labs/' + encodeURIComponent(labId) + '/delete', {
        method: 'POST',
    })
    .then(response => {
        if (response.redirected) {
            window.location.href = response.url;
        } else if (response.ok) {
            window.location.reload();
        } else {
            response.text().then(text => {
                alert('Failed to remove lab: ' + text);
            });
        }
    })
    .catch(error => {
        alert('Error removing lab: ' + error.message);
    });
}

function retryLab(labId) {
    // Send POST request to retry endpoint
    fetch('/api/labs/' + encodeURIComponent(labId) + '/retry', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/x-www-form-urlencoded',
        }
    })
    .then(response => {
        if (response.ok) {
            // Redirect to admin page to view retry progress
            window.location.href = '/admin?job=' + encodeURIComponent(labId);
        } else {
            // Handle error
            response.text().then(text => {
                console.error('Retry failed:', response.status, text);
                alert('Failed to retry lab: ' + response.status);
            });
        }
    })
    .catch(error => {
        console.error('Retry error:', error);
        alert('Error retrying lab: ' + error.message);
    });
}

function showCoderCredentials(labId) {
    var overlay = document.getElementById('coder-credentials-overlay');
    var loading = document.getElementById('coder-credentials-loading');
    var errorEl = document.getElementById('coder-credentials-error');
    var fields = document.getElementById('coder-credentials-fields');

    overlay.classList.add('visible');
    overlay.setAttribute('aria-hidden', 'false');
    loading.style.display = 'block';
    errorEl.style.display = 'none';
    errorEl.textContent = '';
    fields.style.display = 'none';

    fetch('/api/labs/' + encodeURIComponent(labId) + '/coder-credentials')
        .then(function (response) {
            if (!response.ok) {
                if (response.status === 404) return Promise.reject('Lab not found.');
                if (response.status === 400) return Promise.reject('Lab is not completed yet.');
                return Promise.reject('Could not load credentials (' + response.status + ').');
            }
            return response.json();
        })
        .then(function (data) {
            loading.style.display = 'none';
            document.getElementById('coder-cred-url').value = data.url || '';
            var emailEl = document.getElementById('coder-cred-email');
            if (emailEl) emailEl.value = data.namespace || '';
            var pwdInput = document.getElementById('coder-cred-password');
            if (pwdInput) {
                pwdInput.value = '';
                pwdInput.type = 'text';
            }
            fields.style.display = 'block';
        })
        .catch(function (err) {
            loading.style.display = 'none';
            errorEl.textContent = typeof err === 'string' ? err : 'Failed to load credentials.';
            errorEl.style.display = 'block';
        });
}

function closeCoderCredentialsModal() {
    var overlay = document.getElementById('coder-credentials-overlay');
    overlay.classList.remove('visible');
    overlay.setAttribute('aria-hidden', 'true');
}

function openCoderUrlInNewTab() {
    var input = document.getElementById('coder-cred-url');
    if (!input || !input.value) return;
    window.open(input.value, '_blank', 'noopener,noreferrer');
}

function copyCoderCredToClipboard(inputId, copyBtn) {
    var input = document.getElementById(inputId);
    if (!input || !input.value) return;
    copyToClipboard(input.value, copyBtn);
}

function copyToClipboard(text, feedbackBtn) {
    if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(function () {
            showCopyFeedback(feedbackBtn);
        }).catch(function () {
            fallbackCopyToClipboard(text, feedbackBtn);
        });
    } else {
        fallbackCopyToClipboard(text, feedbackBtn);
    }
}

function fallbackCopyToClipboard(text, feedbackBtn) {
    var ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.position = 'absolute';
    ta.style.left = '-9999px';
    document.body.appendChild(ta);
    ta.select();
    try {
        document.execCommand('copy');
        showCopyFeedback(feedbackBtn);
    } catch (e) {}
    document.body.removeChild(ta);
}

function showCopyFeedback(btn) {
    if (!btn) return;
    var orig = btn.textContent;
    btn.textContent = 'Copied!';
    setTimeout(function () { btn.textContent = orig; }, 2000);
}

var _uploadTemplatLabId = null;

function _utSetFile(file) {
    var fileText = document.getElementById('upload-template-file-text');
    var dropzone = document.getElementById('ut-dropzone');
    if (fileText) fileText.textContent = file ? file.name : 'Drop file here';
    if (dropzone) {
        dropzone.classList.toggle('ut-dropzone--active', !!file);
    }
}

function showUploadTemplateModal(labId, labName) {
    _uploadTemplatLabId = labId;
    var overlay = document.getElementById('upload-template-overlay');
    var errorEl = document.getElementById('upload-template-error');
    var successEl = document.getElementById('upload-template-success');
    var form = document.getElementById('upload-template-form');
    var submitBtn = document.getElementById('upload-template-submit-btn');
    var dropzone = document.getElementById('ut-dropzone');

    // Populate the lab context bar
    var contextBar = document.getElementById('ut-drawer-lab-context');
    if (contextBar) {
        contextBar.style.display = 'flex';
        contextBar.innerHTML = 'Targeting lab <span class="ut-drawer-lab-chip">' +
            (labName ? labName : labId) + '</span>';
    }

    form.reset();
    errorEl.style.display = 'none';
    errorEl.textContent = '';
    successEl.style.display = 'none';
    successEl.textContent = '';
    submitBtn.disabled = false;
    // Restore inner HTML (icon + text) to original
    submitBtn.innerHTML = '<svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-8l-4-4m0 0L8 8m4-4v12" /></svg>Add Template';
    _utSetFile(null);

    overlay.classList.add('visible');
    overlay.setAttribute('aria-hidden', 'false');
    document.body.style.overflow = 'hidden';

    var fileInput = document.getElementById('upload-template-file');
    if (fileInput) {
        fileInput.onchange = function () {
            _utSetFile(fileInput.files && fileInput.files[0] ? fileInput.files[0] : null);
        };
    }

    // Drag & drop on the dropzone
    if (dropzone) {
        dropzone.ondragover = function (e) {
            e.preventDefault();
            dropzone.classList.add('ut-dropzone--dragging');
        };
        dropzone.ondragleave = function () {
            dropzone.classList.remove('ut-dropzone--dragging');
        };
        dropzone.ondrop = function (e) {
            e.preventDefault();
            dropzone.classList.remove('ut-dropzone--dragging');
            var file = e.dataTransfer && e.dataTransfer.files && e.dataTransfer.files[0];
            if (file && fileInput) {
                var dt = new DataTransfer();
                dt.items.add(file);
                fileInput.files = dt.files;
                _utSetFile(file);
            }
        };
    }
}

function closeUploadTemplateModal() {
    var overlay = document.getElementById('upload-template-overlay');
    overlay.classList.remove('visible');
    overlay.setAttribute('aria-hidden', 'true');
    document.body.style.overflow = '';
    _uploadTemplatLabId = null;
    _utSetFile(null);
    var container = document.getElementById('upload-template-variables-container');
    if (container) container.innerHTML = '';
    var contextBar = document.getElementById('ut-drawer-lab-context');
    if (contextBar) contextBar.style.display = 'none';
}

function createUploadTemplateVariableRow(name, value, description, required) {
    var div = document.createElement('div');
    div.className = 'template-variable-row';

    var nameInput = document.createElement('input');
    nameInput.type = 'text';
    nameInput.name = 'template_0_var_name';
    nameInput.placeholder = 'Variable name';
    nameInput.value = name || '';
    if (required) nameInput.setAttribute('data-required', 'true');

    var valueInput = document.createElement('input');
    valueInput.type = 'text';
    valueInput.name = 'template_0_var_value';
    valueInput.placeholder = description || 'Value';
    valueInput.value = value || '';

    var removeBtn = document.createElement('button');
    removeBtn.type = 'button';
    removeBtn.className = 'btn btn-secondary btn-remove-variable';
    removeBtn.textContent = 'x';
    removeBtn.title = 'Remove variable';
    removeBtn.addEventListener('click', function() { div.remove(); });

    div.appendChild(nameInput);
    div.appendChild(valueInput);
    div.appendChild(removeBtn);
    return div;
}

function addUploadTemplateVariableRow() {
    var container = document.getElementById('upload-template-variables-container');
    if (container) container.appendChild(createUploadTemplateVariableRow('', '', '', false));
}

function detectUploadTemplateVariables() {
    var fileInput = document.getElementById('upload-template-file');
    var container = document.getElementById('upload-template-variables-container');
    var detectBtn = document.getElementById('upload-detect-variables-btn');
    if (!fileInput || !fileInput.files || !fileInput.files[0]) {
        alert('Please select a template file first.');
        return;
    }
    detectBtn.disabled = true;
    detectBtn.textContent = 'Detecting...';

    var formData = new FormData();
    formData.append('source', 'upload');
    formData.append('template_file', fileInput.files[0]);

    fetch('/api/templates/detect-variables', { method: 'POST', body: formData })
        .then(function(resp) {
            if (!resp.ok) {
                return resp.json().then(function(d) { throw new Error(d.message || 'Detection failed'); });
            }
            return resp.json();
        })
        .then(function(variables) {
            container.innerHTML = '';
            if (!variables || variables.length === 0) {
                var empty = document.createElement('div');
                empty.className = 'detect-variables-status';
                empty.textContent = 'No Terraform variables found in this template.';
                container.appendChild(empty);
            } else {
                variables.forEach(function(v) {
                    container.appendChild(createUploadTemplateVariableRow(v.name, v.default || '', v.description || '', v.required));
                });
            }
        })
        .catch(function(err) {
            var errDiv = document.createElement('div');
            errDiv.className = 'detect-variables-status error-message';
            errDiv.textContent = 'Detection failed: ' + err.message;
            container.appendChild(errDiv);
        })
        .finally(function() {
            detectBtn.disabled = false;
            detectBtn.textContent = 'Detect Variables';
        });
}

function submitUploadTemplate(event) {
    event.preventDefault();
    if (!_uploadTemplatLabId) return;

    var name = document.getElementById('upload-template-name').value.trim();
    var imageEl = document.getElementById('upload-template-image');
    var gitEl = document.getElementById('upload-template-git-repo');
    var gitBranchEl = document.getElementById('upload-template-git-branch');
    var gitFolderEl = document.getElementById('upload-template-git-folder');
    var startupEl = document.getElementById('upload-template-startup-script');
    var dotfilesEl = document.getElementById('upload-template-dotfiles-repo');
    var extEl = document.getElementById('upload-template-extensions');
    var errorEl = document.getElementById('upload-template-error');
    var successEl = document.getElementById('upload-template-success');
    var submitBtn = document.getElementById('upload-template-submit-btn');

    errorEl.style.display = 'none';
    errorEl.textContent = '';
    successEl.style.display = 'none';

    if (!name) {
        errorEl.textContent = 'Template name is required.';
        errorEl.style.display = 'block';
        return;
    }

    var formData = new FormData();
    formData.append('template_name', name);
    if (imageEl) formData.append('template_image', imageEl.value.trim());
    if (gitEl) formData.append('template_git_repo', gitEl.value.trim());
    if (gitBranchEl) formData.append('template_git_branch', gitBranchEl.value.trim());
    if (gitFolderEl) formData.append('template_git_folder', gitFolderEl.value.trim());
    if (startupEl) formData.append('template_startup_script', startupEl.value);
    if (dotfilesEl) formData.append('template_dotfiles_repo', dotfilesEl.value.trim());
    if (extEl) formData.append('template_extensions', extEl.value.trim());

    submitBtn.disabled = true;
    submitBtn.textContent = 'Adding…';

    fetch('/api/labs/' + encodeURIComponent(_uploadTemplatLabId) + '/templates/upload', {
        method: 'POST',
        body: formData
    })
    .then(function (response) {
        if (!response.ok) {
            return response.text().then(function (text) {
                return Promise.reject(text || 'Upload failed (' + response.status + ').');
            });
        }
        return response.json();
    })
    .then(function () {
        successEl.textContent = 'Template "' + name + '" added successfully.';
        successEl.style.display = 'flex';
        submitBtn.disabled = false;
        submitBtn.innerHTML = '<svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-8l-4-4m0 0L8 8m4-4v12" /></svg>Add Template';
        document.getElementById('upload-template-form').reset();
        _utSetFile(null);
        var container = document.getElementById('upload-template-variables-container');
        if (container) container.innerHTML = '';
    })
    .catch(function (err) {
        errorEl.textContent = typeof err === 'string' ? err : 'Failed to upload template.';
        errorEl.style.display = 'flex';
        submitBtn.disabled = false;
        submitBtn.innerHTML = '<svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M4 16v1a3 3 0 003 3h10a3 3 0 003-3v-1m-4-8l-4-4m0 0L8 8m4-4v12" /></svg>Add Template';
    });
}

document.addEventListener('DOMContentLoaded', function () {
    if (typeof syncEasylabHeaderProviderDropdown === 'function') {
        var pref =
            typeof getEasylabHeaderProviderPreference === 'function'
                ? getEasylabHeaderProviderPreference()
                : 'ovh';
        syncEasylabHeaderProviderDropdown(pref);
    }
});

function toggleCoderPasswordVisibility() {
    var input = document.getElementById('coder-cred-password');
    var btn = document.querySelector('.credentials-toggle-password');
    if (!input || !btn) return;
    if (input.type === 'password') {
        input.type = 'text';
        btn.textContent = 'Hide';
        btn.setAttribute('title', 'Hide password');
        btn.setAttribute('aria-label', 'Hide password');
    } else {
        input.type = 'password';
        btn.textContent = 'Show';
        btn.setAttribute('title', 'Show password');
        btn.setAttribute('aria-label', 'Show password');
    }
}
