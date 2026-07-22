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

// Show a recreate failure inside the modal so the admin can fix their input
// instead of the request failing silently. `html` is a complete error fragment
// (the server returns an already-escaped `.error-message` div; wrap plain
// client-side messages with recreateErrorBox).
function showRecreateError(html) {
    const box = document.getElementById('recreate-credentials-error');
    if (!box) return;
    box.innerHTML = html;
    box.style.display = 'block';
    openRecreateCredentialsModal();
}

// Wrap a plain message in the same error-message box the server returns, so
// client-side and server-side errors render identically.
function recreateErrorBox(message) {
    const p = document.createElement('p');
    p.textContent = message;
    return '<div class="error-message">' + p.outerHTML + '</div>';
}

function clearRecreateError() {
    const box = document.getElementById('recreate-credentials-error');
    if (!box) return;
    box.innerHTML = '';
    box.style.display = 'none';
}

function submitRecreate(body) {
    clearRecreateError();
    fetch('/api/labs/recreate', {
        method: 'POST',
        headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
        body: body
    })
    .then(response => {
        // Success is always a redirect to the new lab; anything else is an error
        // whose body carries the message to display.
        if (response.redirected) {
            window.location.href = response.url;
            return;
        }
        return response.text().then(text => {
            showRecreateError(text.trim() || recreateErrorBox('Could not recreate the lab.'));
        });
    })
    .catch(error => {
        console.error('Recreate error:', error);
        showRecreateError(recreateErrorBox('Could not recreate the lab. Please try again.'));
    });
}

function recreateLab(labId) {
    // Recreation lands on a new cluster, so any credentials the lab's templates
    // reference must be supplied again, and a lab that had a scheduled deletion
    // date needs a fresh one (the old date is in the past). The server returns a
    // prompt fragment for whatever applies; an empty fragment means recreate
    // straight away.
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
                return Promise.reject('Could not load endpoint info (' + response.status + ').');
            }
            return response.json();
        })
        .then(function (data) {
            loading.style.display = 'none';
            var urlEl = document.getElementById('coder-cred-url');
            // An empty base URL means workspaces are only reachable in-cluster.
            urlEl.value = data.url || '';
            urlEl.placeholder = 'Not exposed (in-cluster only)';
            var nsEl = document.getElementById('coder-cred-email');
            if (nsEl) nsEl.value = data.namespace || '';
            fields.style.display = 'block';
        })
        .catch(function (err) {
            loading.style.display = 'none';
            errorEl.textContent = typeof err === 'string' ? err : 'Failed to load endpoint info.';
            errorEl.style.display = 'block';
        });
}

function closeCoderCredentialsModal() {
    var overlay = document.getElementById('coder-credentials-overlay');
    overlay.classList.remove('visible');
    overlay.setAttribute('aria-hidden', 'true');
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

// ---------------------------------------------------------------------------
// Add Template drawer
//
// The drawer mirrors the create-lab wizard's Step 6: three ways to define a
// workspace — build with a form, import a devcontainer, or paste YAML — that all
// resolve to one or more templates appended to the lab via
// POST /api/labs/{id}/templates/upload.
// ---------------------------------------------------------------------------

var _utLabId = null;
var _utDcSource = 'git';

function utEl(id) { return document.getElementById(id); }

function utEscape(text) {
    var div = document.createElement('div');
    div.textContent = text == null ? '' : text;
    return div.innerHTML;
}

// showToast renders the app's floating toast (top-right, auto-dismissed by CSS).
function showToast(message, kind) {
    var toast = document.createElement('div');
    toast.className = 'toast toast-' + (kind === 'error' ? 'error' : 'success');
    toast.innerHTML = '<span class="toast-icon">' + (kind === 'error' ? '⚠️' : '✅') +
        '</span><span>' + utEscape(message) + '</span>';
    document.body.appendChild(toast);
    setTimeout(function () { toast.remove(); }, 4500);
}

function showUploadTemplateModal(labId, labName, existingCsv) {
    _utLabId = labId;
    var overlay = utEl('upload-template-overlay');
    var form = utEl('upload-template-form');
    var errorEl = utEl('upload-template-error');

    // Context bar: which lab, and the templates it already has (so a duplicate
    // name is visible up front rather than arriving as a 409).
    var contextBar = utEl('ut-drawer-lab-context');
    if (contextBar) {
        contextBar.style.display = 'flex';
        var html = 'Targeting lab <span class="ut-drawer-lab-chip">' + utEscape(labName || labId) + '</span>';
        var existing = (existingCsv || '').trim();
        if (existing) {
            html += '<span class="ut-drawer-existing">Already on this lab: ' + utEscape(existing) + '</span>';
        }
        contextBar.innerHTML = html;
    }

    if (form) form.reset();
    if (errorEl) { errorEl.style.display = 'none'; errorEl.textContent = ''; }

    utClearFormContainers();
    var dcResult = utEl('ut-dc-result'); if (dcResult) dcResult.innerHTML = '';
    var yamlVal = utEl('ut-templates-yaml-validation'); if (yamlVal) yamlVal.innerHTML = '';
    var reviewBtn = utEl('ut-dc-review-yaml-btn'); if (reviewBtn) reviewBtn.style.display = 'none';
    utResetSubmitBtn();

    // Always open on the form mode with a git devcontainer source.
    utSetTemplatesMode('form');
    utSetDevcontainerSource('git');

    overlay.classList.add('visible');
    overlay.setAttribute('aria-hidden', 'false');
    document.body.style.overflow = 'hidden';
}

function closeUploadTemplateModal() {
    var overlay = utEl('upload-template-overlay');
    overlay.classList.remove('visible');
    overlay.setAttribute('aria-hidden', 'true');
    document.body.style.overflow = '';
    _utLabId = null;
    var contextBar = utEl('ut-drawer-lab-context');
    if (contextBar) contextBar.style.display = 'none';
}

function utClearFormContainers() {
    ['.template-variables-container', '.template-sidecars-container', '.template-mounts-container'].forEach(function (sel) {
        var c = document.querySelector('#ut-templates-form-mode ' + sel);
        if (c) c.innerHTML = '';
    });
}

function utResetSubmitBtn() {
    var btn = utEl('upload-template-submit-btn');
    if (!btn) return;
    btn.disabled = false;
    btn.innerHTML = '<svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" aria-hidden="true"><path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 4v16m8-8H4" /></svg>Add Template';
}

function utShowError(msg) {
    var errorEl = utEl('upload-template-error');
    if (errorEl) { errorEl.textContent = msg; errorEl.style.display = 'flex'; }
    utResetSubmitBtn();
}

// utSetTemplatesMode toggles the three panels. The server only distinguishes form
// from yaml; the devcontainer path resolves to yaml (it fills the editor with a
// generated document the admin reviews), so templates_mode only holds those two.
function utSetTemplatesMode(mode) {
    var useForm = mode === 'form';
    var modeInput = utEl('ut-templates-mode');
    if (modeInput) modeInput.value = useForm ? 'form' : 'yaml';

    [['form', 'ut-templates-mode-form'], ['devcontainer', 'ut-templates-mode-devcontainer'], ['yaml', 'ut-templates-mode-yaml']].forEach(function (pair) {
        var b = utEl(pair[1]);
        if (b) b.classList.toggle('selected', mode === pair[0]);
    });

    var formPanel = utEl('ut-templates-form-mode');
    var dcPanel = utEl('ut-templates-devcontainer-mode');
    var yamlPanel = utEl('ut-templates-yaml-mode');
    if (formPanel) formPanel.style.display = useForm ? '' : 'none';
    if (dcPanel) dcPanel.style.display = mode === 'devcontainer' ? '' : 'none';
    if (yamlPanel) yamlPanel.style.display = mode === 'yaml' ? '' : 'none';

    // Keep the inactive panel's fields out of the submitted payload.
    if (formPanel) formPanel.querySelectorAll('input, select, textarea').forEach(function (el) { el.disabled = !useForm; });
    var yamlArea = utEl('ut-templates-yaml');
    if (yamlArea) yamlArea.disabled = useForm;
}

function utSetDevcontainerSource(source) {
    _utDcSource = source === 'upload' ? 'upload' : 'git';
    var gitBtn = utEl('ut-dc-source-git');
    var upBtn = utEl('ut-dc-source-upload');
    if (gitBtn) gitBtn.classList.toggle('selected', _utDcSource === 'git');
    if (upBtn) upBtn.classList.toggle('selected', _utDcSource === 'upload');
    var upRow = utEl('ut-dc-upload-row');
    var authRow = utEl('ut-dc-git-auth-row');
    if (upRow) upRow.style.display = _utDcSource === 'upload' ? '' : 'none';
    // An upload is read straight from the file — there is no clone to authenticate.
    if (authRow) authRow.style.display = _utDcSource === 'git' ? '' : 'none';
}

// ---- Form-mode advanced rows (env vars, sidecars, mounts) ------------------

function utMakeTextInput(name, placeholder) {
    var input = document.createElement('input');
    input.type = 'text';
    input.name = name;
    input.placeholder = placeholder;
    return input;
}

function utMakeRemoveButton(div, title) {
    var btn = document.createElement('button');
    btn.type = 'button';
    btn.className = 'btn btn-secondary btn-remove-variable';
    btn.textContent = 'x';
    btn.title = title;
    btn.addEventListener('click', function () { div.remove(); });
    return btn;
}

// A hidden input carries the value so an unchecked box still submits, keeping the
// sidecar arrays index-aligned on the server.
function utMakePrivilegedToggle(name) {
    var wrap = document.createElement('label');
    wrap.className = 'sidecar-privileged';
    var hidden = document.createElement('input');
    hidden.type = 'hidden';
    hidden.name = name;
    hidden.value = 'false';
    var cb = document.createElement('input');
    cb.type = 'checkbox';
    cb.addEventListener('change', function () { hidden.value = cb.checked ? 'true' : 'false'; });
    var span = document.createElement('span');
    span.textContent = 'privileged';
    wrap.appendChild(hidden);
    wrap.appendChild(cb);
    wrap.appendChild(span);
    return wrap;
}

function utCreateVariableRow() {
    var div = document.createElement('div');
    div.className = 'template-variable-row';
    div.appendChild(utMakeTextInput('template_0_env_name', 'Env variable name'));
    div.appendChild(utMakeTextInput('template_0_env_value', 'Value'));
    div.appendChild(utMakeRemoveButton(div, 'Remove variable'));
    return div;
}

function utCreateSidecarRow() {
    var div = document.createElement('div');
    div.className = 'template-variable-row';
    div.appendChild(utMakeTextInput('template_0_sidecar_name', 'name'));
    div.appendChild(utMakeTextInput('template_0_sidecar_image', 'image (e.g. postgres:16)'));
    div.appendChild(utMakeTextInput('template_0_sidecar_ports', 'ports (5432,6379)'));
    div.appendChild(utMakeTextInput('template_0_sidecar_env', 'env (KEY=VAL,KEY2=VAL2)'));
    div.appendChild(utMakeTextInput('template_0_sidecar_capabilities', 'capabilities (SYS_ADMIN,…)'));
    div.appendChild(utMakePrivilegedToggle('template_0_sidecar_privileged'));
    div.appendChild(utMakeRemoveButton(div, 'Remove sidecar'));
    return div;
}

function utCreateMountRow() {
    var div = document.createElement('div');
    div.className = 'template-variable-row';
    var typeSelect = document.createElement('select');
    typeSelect.name = 'template_0_mount_type';
    ['configmap', 'secret'].forEach(function (t) {
        var opt = document.createElement('option');
        opt.value = t;
        opt.textContent = t;
        typeSelect.appendChild(opt);
    });
    div.appendChild(typeSelect);
    div.appendChild(utMakeTextInput('template_0_mount_name', 'ConfigMap/Secret name'));
    div.appendChild(utMakeTextInput('template_0_mount_path', 'mount path (/etc/config)'));
    div.appendChild(utMakeRemoveButton(div, 'Remove mount'));
    return div;
}

function utAppendRow(sel, factory) {
    var c = document.querySelector('#ut-templates-form-mode ' + sel);
    if (c) c.appendChild(factory());
}

// ---- Devcontainer import ---------------------------------------------------

function utDcMessage(kind, text) {
    var el = utEl('ut-dc-result');
    if (!el) return;
    el.innerHTML = '<div class="toast toast--inline toast-' + kind + '"><span>' + utEscape(text) + '</span></div>';
}

// Reports what the builder will produce and every key it will ignore.
function utRenderDevcontainerImport(data) {
    var el = utEl('ut-dc-result');
    if (!el) return;
    var base = data.base || {};
    var summary = 'no image or Dockerfile — the fallback image is used';
    if (base.kind === 'image') summary = 'image <code>' + utEscape(base.image) + '</code>';
    else if (base.kind === 'dockerfile') summary = 'built from <code>' + utEscape(base.dockerfile) + '</code>';

    var html = '<div class="toast toast--inline toast-success"><span>Imported <code>' +
        utEscape(data.path || 'devcontainer.json') + '</code> — ' + summary + '.</span></div>';

    if (data.features && data.features.length) {
        html += '<p class="devcontainer-import-note">Features built into the workspace: ' +
            data.features.map(function (f) { return '<code>' + utEscape(f) + '</code>'; }).join(', ') + '</p>';
    }
    if (data.warnings && data.warnings.length) {
        html += '<p class="devcontainer-import-note">These parts of the devcontainer will not take effect:</p><ul class="devcontainer-import-warnings">';
        data.warnings.forEach(function (w) {
            html += '<li><code>' + utEscape(w.key) + '</code> — ' + utEscape(w.message) + '</li>';
        });
        html += '</ul>';
    }
    el.innerHTML = html;
}

function utRunDevcontainerImport() {
    var importBtn = utEl('ut-dc-import-btn');
    var yamlArea = utEl('ut-templates-yaml');
    var reviewBtn = utEl('ut-dc-review-yaml-btn');

    var body = new FormData();
    body.append('source', _utDcSource);
    body.append('git_repo', (utEl('ut-dc-git-repo') || {}).value || '');
    body.append('git_branch', (utEl('ut-dc-git-branch') || {}).value || '');
    body.append('devcontainer_dir', (utEl('ut-dc-dir') || {}).value || '');
    body.append('cache_repo', (utEl('ut-dc-cache-repo') || {}).value || '');
    // Credentials for students are configured at lab creation; nothing to pick here.
    body.append('registry_auth_secret', '');
    body.append('git_auth_secret', '');
    // Request-scoped: authenticates this read only, and is never persisted.
    body.append('git_username', (utEl('ut-dc-git-username') || {}).value || '');
    body.append('git_token', (utEl('ut-dc-git-token') || {}).value || '');

    if (_utDcSource === 'upload') {
        var fileInput = utEl('ut-dc-file');
        var file = fileInput && fileInput.files[0];
        if (!file) { utDcMessage('error', 'Choose a devcontainer.json or a repository .zip to upload.'); return; }
        body.append('devcontainer_file', file);
    }

    utDcMessage('success', 'Reading the devcontainer…');
    if (importBtn) importBtn.disabled = true;

    fetch('/api/templates/detect-devcontainer', { method: 'POST', body: body })
        .then(function (response) { return response.json().then(function (data) { return { ok: response.ok, data: data }; }); })
        .then(function (r) {
            if (!r.ok) { utDcMessage('error', r.data.message || 'Could not read the devcontainer.'); return; }
            if (yamlArea) yamlArea.value = r.data.templates_yaml || '';
            utRenderDevcontainerImport(r.data);
            if (reviewBtn) reviewBtn.style.display = '';
        })
        .catch(function () { utDcMessage('error', 'Could not reach the server.'); })
        .finally(function () { if (importBtn) importBtn.disabled = false; });
}

// ---- Paste YAML ------------------------------------------------------------

function utValidateYaml() {
    var yamlArea = utEl('ut-templates-yaml');
    var out = utEl('ut-templates-yaml-validation');
    var body = new FormData();
    body.append('templates_yaml', yamlArea ? yamlArea.value : '');
    fetch('/api/labs/templates/yaml/validate', { method: 'POST', body: body })
        .then(function (r) { return r.text(); })
        .then(function (html) { if (out) out.innerHTML = html; })
        .catch(function () { if (out) out.textContent = 'Could not validate.'; });
}

function utInsertYamlSkeleton() {
    var yamlArea = utEl('ut-templates-yaml');
    if (yamlArea && yamlArea.value.trim() && !confirm('Replace the current YAML with the commented skeleton?')) return;
    // Posting no template fields makes the server return the skeleton.
    fetch('/api/labs/templates/yaml', { method: 'POST', body: new FormData() })
        .then(function (r) { return r.ok ? r.text() : Promise.reject(new Error('skeleton failed')); })
        .then(function (text) { if (yamlArea) yamlArea.value = text; })
        .catch(function () { /* leave the editor as-is */ });
}

// ---- Submit ----------------------------------------------------------------

function submitUploadTemplate(event) {
    event.preventDefault();
    if (!_utLabId) return;

    var errorEl = utEl('upload-template-error');
    if (errorEl) { errorEl.style.display = 'none'; errorEl.textContent = ''; }

    var modeInput = utEl('ut-templates-mode');
    var mode = modeInput ? modeInput.value : 'form';

    var body = new FormData();
    body.append('templates_mode', mode);

    if (mode === 'yaml') {
        var yamlArea = utEl('ut-templates-yaml');
        if (!yamlArea || !yamlArea.value.trim()) {
            utShowError('Add some YAML, or switch to “Build with a form”.');
            return;
        }
        body.append('templates_yaml', yamlArea.value);
    } else {
        var nameEl = document.querySelector('#ut-templates-form-mode [name="template_0_name"]');
        if (!nameEl || !nameEl.value.trim()) {
            utShowError('Template name is required.');
            return;
        }
        var formPanel = utEl('ut-templates-form-mode');
        formPanel.querySelectorAll('[name^="template_"]').forEach(function (el) {
            if (el.type === 'file' || el.disabled) return;
            if ((el.type === 'checkbox' || el.type === 'radio') && !el.checked) return;
            body.append(el.getAttribute('name'), el.value);
        });
    }

    var submitBtn = utEl('upload-template-submit-btn');
    submitBtn.disabled = true;
    submitBtn.textContent = 'Adding…';

    fetch('/api/labs/' + encodeURIComponent(_utLabId) + '/templates/upload', { method: 'POST', body: body })
        .then(function (response) {
            if (!response.ok) {
                return response.text().then(function (text) { return Promise.reject(text || 'Add failed (' + response.status + ').'); });
            }
            return response.json();
        })
        .then(function (data) {
            var names = (data && data.templates) || [];
            var label = names.length > 1
                ? (names.length + ' templates added.')
                : ('Template “' + (names[0] || '') + '” added.');
            // Queue the toast to survive the reload that refreshes the list.
            try { sessionStorage.setItem('ut-flash', JSON.stringify({ msg: label, kind: 'success' })); } catch (e) { /* ignore */ }
            closeUploadTemplateModal();
            window.location.reload();
        })
        .catch(function (err) {
            utShowError(typeof err === 'string' ? err : 'Failed to add template.');
        });
}

function utInitDrawer() {
    var modeForm = utEl('ut-templates-mode-form');
    if (modeForm) modeForm.addEventListener('click', function () { utSetTemplatesMode('form'); });
    var modeDc = utEl('ut-templates-mode-devcontainer');
    if (modeDc) modeDc.addEventListener('click', function () {
        // The form's git repo usually already points at the workshop repo.
        var dcRepo = utEl('ut-dc-git-repo');
        var formRepo = document.querySelector('#ut-templates-form-mode [name="template_0_git_repo"]');
        if (dcRepo && !dcRepo.value && formRepo && formRepo.value) dcRepo.value = formRepo.value;
        utSetTemplatesMode('devcontainer');
    });
    var modeYaml = utEl('ut-templates-mode-yaml');
    if (modeYaml) modeYaml.addEventListener('click', function () { utSetTemplatesMode('yaml'); });

    var dcGit = utEl('ut-dc-source-git');
    if (dcGit) dcGit.addEventListener('click', function () { utSetDevcontainerSource('git'); });
    var dcUp = utEl('ut-dc-source-upload');
    if (dcUp) dcUp.addEventListener('click', function () { utSetDevcontainerSource('upload'); });
    var importBtn = utEl('ut-dc-import-btn');
    if (importBtn) importBtn.addEventListener('click', utRunDevcontainerImport);
    var reviewBtn = utEl('ut-dc-review-yaml-btn');
    if (reviewBtn) reviewBtn.addEventListener('click', function () { utSetTemplatesMode('yaml'); });

    var validateBtn = utEl('ut-yaml-validate-btn');
    if (validateBtn) validateBtn.addEventListener('click', utValidateYaml);
    var skeletonBtn = utEl('ut-yaml-skeleton-btn');
    if (skeletonBtn) skeletonBtn.addEventListener('click', utInsertYamlSkeleton);
    var uploadBtn = utEl('ut-yaml-upload-btn');
    var uploadInput = utEl('ut-yaml-file');
    if (uploadBtn && uploadInput) {
        uploadBtn.addEventListener('click', function () { uploadInput.click(); });
        uploadInput.addEventListener('change', function (e) {
            var file = e.target.files[0];
            if (!file) return;
            var reader = new FileReader();
            reader.onload = function (evt) { var t = utEl('ut-templates-yaml'); if (t) t.value = evt.target.result; };
            reader.readAsText(file);
            e.target.value = '';
        });
    }

    var addVar = document.querySelector('#ut-templates-form-mode .btn-add-variable');
    if (addVar) addVar.addEventListener('click', function () { utAppendRow('.template-variables-container', utCreateVariableRow); });
    var addSide = document.querySelector('#ut-templates-form-mode .btn-add-sidecar');
    if (addSide) addSide.addEventListener('click', function () { utAppendRow('.template-sidecars-container', utCreateSidecarRow); });
    var addMount = document.querySelector('#ut-templates-form-mode .btn-add-mount');
    if (addMount) addMount.addEventListener('click', function () { utAppendRow('.template-mounts-container', utCreateMountRow); });

    // Show a toast queued before a reload (e.g. after a template was added).
    try {
        var flash = sessionStorage.getItem('ut-flash');
        if (flash) {
            sessionStorage.removeItem('ut-flash');
            var f = JSON.parse(flash);
            showToast(f.msg, f.kind);
        }
    } catch (e) { /* ignore */ }
}

document.addEventListener('DOMContentLoaded', utInitDrawer);

document.addEventListener('DOMContentLoaded', function () {
    if (typeof syncEasylabHeaderProviderDropdown === 'function') {
        var pref =
            typeof getEasylabHeaderProviderPreference === 'function'
                ? getEasylabHeaderProviderPreference()
                : 'ovh';
        syncEasylabHeaderProviderDropdown(pref);
    }
});
