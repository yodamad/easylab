// Actions for the lab detail page (web/lab-detail.html). Also included by
// labs-list.html so its retained "Retry"/"Recreate" quick-action buttons can
// open the same choice modals and call the same network functions, without
// duplicating this logic across the two pages.

// ---------------------------------------------------------------------------
// Page-level tabs (Overview / Progress / Workspaces & Templates / Feedback /
// Lifecycle & Cleanup / Activity / Danger Zone)
// ---------------------------------------------------------------------------

function switchLabDetailTab(name) {
    document.querySelectorAll('.lab-detail-tab').forEach(function (tab) {
        var isSelected = tab.getAttribute('aria-controls') === name;
        tab.classList.toggle('is-active', isSelected);
        tab.setAttribute('aria-selected', isSelected ? 'true' : 'false');
    });
    document.querySelectorAll('.lab-detail-section').forEach(function (section) {
        section.classList.toggle('is-hidden', section.id !== name);
    });
}

document.addEventListener('DOMContentLoaded', function () {
    // Only runs on the lab detail page (labs-list.html has no .lab-detail-tab).
    if (!document.querySelector('.lab-detail-tab')) return;
    var requested = (window.location.hash || '').slice(1);
    // Land on the requested tab if it exists (e.g. the old /labs/{id}/workspaces
    // URL redirects here with #workspaces) — a lab that isn't completed yet
    // has no "workspaces" tab, so this falls back to the default Overview tab.
    if (requested && document.getElementById(requested)) {
        switchLabDetailTab(requested);
    }
});

// ---------------------------------------------------------------------------
// Progress tab (the shared job-status fragment rendered by GetJobStatus)
// ---------------------------------------------------------------------------

// The fragment's "Retry Job" button calls retryJob(); on this page retrying
// goes through the same as-is/edit choice modal as the Danger Zone button
// (admin.js defines its own retryJob for the creation wizard, and the two pages
// never load each other's script).
function retryJob(labId) {
    openRetryChoiceModal(labId);
}

(function () {
    // The fragment re-renders in full on every poll, so the deployment log would
    // snap back to its first line every 10s. Remember the reading position
    // before each swap and restore it after, following the tail when the admin
    // was already at the bottom.
    var STICK_THRESHOLD_PX = 40;
    var savedScrollTop = 0;
    var stuckToBottom = true;
    var wasInFlight = false;

    function progressLog() {
        return document.querySelector('#progress .output');
    }

    function progressStatus() {
        var badge = document.querySelector('#progress .status-badge');
        return badge ? badge.textContent.trim() : '';
    }

    // htmx reports the swapped element on evt.target, except for outerHTML swaps
    // where the detached element is replaced and the event carries the parent —
    // check both so the panel's self-refresh is recognized either way.
    function touchesProgress(evt) {
        var el = evt.target;
        if (el && el.closest && el.closest('#progress')) return true;
        var detailTarget = evt.detail && evt.detail.target;
        return !!(detailTarget && detailTarget.closest && detailTarget.closest('#progress'));
    }

    document.body.addEventListener('htmx:beforeSwap', function (evt) {
        if (!touchesProgress(evt)) return;
        var log = progressLog();
        if (!log) return;
        savedScrollTop = log.scrollTop;
        stuckToBottom = log.scrollHeight - log.scrollTop - log.clientHeight < STICK_THRESHOLD_PX;
        var status = progressStatus();
        wasInFlight = status === 'pending' || status === 'running';
    });

    document.body.addEventListener('htmx:afterSwap', function (evt) {
        if (!touchesProgress(evt)) return;
        var log = progressLog();
        if (log) {
            log.scrollTop = stuckToBottom ? log.scrollHeight : savedScrollTop;
        }
        // Tabs are rendered server-side from the lab's status, so Workspaces &
        // Templates only shows up after a reload. Reload for the admin watching
        // the deployment finish, but never under one working in another tab.
        var progress = document.getElementById('progress');
        var watching = progress && !progress.classList.contains('is-hidden');
        if (wasInFlight && watching && progressStatus() === 'completed') {
            wasInFlight = false;
            window.location.reload();
        }
    });
})();

function destroyStack(labId) {
    fetch('/api/stacks/destroy', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/x-www-form-urlencoded',
        },
        body: 'job_id=' + encodeURIComponent(labId)
    })
    .then(response => {
        if (response.redirected) {
            // The server redirects to the wizard's progress view; send the admin
            // back to this lab's own detail page instead, where Progress covers it.
            window.location.href = '/labs/' + encodeURIComponent(labId);
        } else {
            console.error('Destroy failed:', response.status);
        }
    })
    .catch(error => {
        console.error('Destroy error:', error);
    });
}

// Show a recreate failure inline so the admin can fix their input instead of
// the request failing silently. `html` is a complete error fragment (the
// server returns an already-escaped `.error-message` div; wrap plain
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
        // Success is always a redirect to the new lab's wizard progress view;
        // send the admin to that lab's own detail page instead.
        if (response.redirected) {
            let newJobId = null;
            try {
                newJobId = new URL(response.url, window.location.origin).searchParams.get('job');
            } catch (e) { /* ignore */ }
            window.location.href = newJobId ? ('/labs/' + encodeURIComponent(newJobId)) : response.url;
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

let recreateChoiceLabId = null;

// openRecreateChoiceModal presents the "rerun as-is or edit configuration
// first" choice before recreating a destroyed lab.
function openRecreateChoiceModal(labId) {
    recreateChoiceLabId = labId;
    const overlay = document.getElementById('recreate-choice-overlay');
    if (overlay) {
        overlay.classList.add('visible');
        overlay.setAttribute('aria-hidden', 'false');
    }
}

function closeRecreateChoiceModal() {
    const overlay = document.getElementById('recreate-choice-overlay');
    if (overlay) {
        overlay.classList.remove('visible');
        overlay.setAttribute('aria-hidden', 'true');
    }
}

function confirmRecreateAsIs() {
    const labId = recreateChoiceLabId;
    closeRecreateChoiceModal();
    if (labId) recreateLab(labId);
}

function confirmRecreateEdit() {
    const labId = recreateChoiceLabId;
    closeRecreateChoiceModal();
    if (labId) {
        window.location.href = '/admin?prefill_job=' + encodeURIComponent(labId) + '&prefill_action=recreate';
    }
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

// ---------------------------------------------------------------------------
// Lifecycle & Cleanup inline form (was the "Edit Lifecycle" modal)
// ---------------------------------------------------------------------------

function showLifecycleFormError(message) {
    const box = document.getElementById('lifecycle-form-error');
    if (!box) return;
    box.textContent = message;
    box.className = 'error-message';
    box.style.display = 'block';
}

function clearLifecycleFormError() {
    const box = document.getElementById('lifecycle-form-error');
    if (!box) return;
    box.textContent = '';
    box.style.display = 'none';
}

document.addEventListener('DOMContentLoaded', function() {
    const form = document.getElementById('lifecycle-form');
    if (!form) return;
    form.addEventListener('submit', function(event) {
        event.preventDefault();
        clearLifecycleFormError();
        const labId = document.getElementById('lifecycle-job-id').value;
        fetch('/api/labs/' + encodeURIComponent(labId) + '/lifecycle', {
            method: 'POST',
            headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
            body: new URLSearchParams(new FormData(form)).toString()
        })
        .then(response => response.json().then(data => ({ ok: response.ok, data: data })))
        .then(result => {
            if (result.ok) {
                showToast('Lifecycle settings saved.', 'success');
                setTimeout(function() { window.location.reload(); }, 1000);
            } else {
                showLifecycleFormError(result.data.message || 'Could not update the lab.');
            }
        })
        .catch(error => {
            console.error('Edit lifecycle error:', error);
            showLifecycleFormError('Could not update the lab. Please try again.');
        });
    });
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
            window.location.href = '/labs';
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

let retryChoiceLabId = null;

// openRetryChoiceModal presents the "rerun as-is or edit configuration first"
// choice before retrying a failed lab.
function openRetryChoiceModal(labId) {
    retryChoiceLabId = labId;
    const overlay = document.getElementById('retry-choice-overlay');
    if (overlay) {
        overlay.classList.add('visible');
        overlay.setAttribute('aria-hidden', 'false');
    }
}

function closeRetryChoiceModal() {
    const overlay = document.getElementById('retry-choice-overlay');
    if (overlay) {
        overlay.classList.remove('visible');
        overlay.setAttribute('aria-hidden', 'true');
    }
}

function confirmRetryAsIs() {
    const labId = retryChoiceLabId;
    closeRetryChoiceModal();
    if (labId) retryLab(labId);
}

function confirmRetryEdit() {
    const labId = retryChoiceLabId;
    closeRetryChoiceModal();
    if (labId) {
        window.location.href = '/admin?prefill_job=' + encodeURIComponent(labId) + '&prefill_action=retry';
    }
}

function retryLab(labId) {
    fetch('/api/labs/' + encodeURIComponent(labId) + '/retry', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/x-www-form-urlencoded',
        }
    })
    .then(response => {
        if (response.ok) {
            window.location.href = '/labs/' + encodeURIComponent(labId);
        } else {
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

// ---------------------------------------------------------------------------
// Lab Endpoint Info (was the "coder-credentials" modal, now an inline
// <details> panel in the Overview section)
// ---------------------------------------------------------------------------

const _loadedCoderCredentials = {};

function loadCoderCredentials(labId) {
    if (_loadedCoderCredentials[labId]) return;

    const loading = document.getElementById('coder-credentials-loading');
    const errorEl = document.getElementById('coder-credentials-error');
    const fields = document.getElementById('coder-credentials-fields');
    if (!loading || !errorEl || !fields) return;

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
            _loadedCoderCredentials[labId] = true;
            loading.style.display = 'none';
            const urlEl = document.getElementById('coder-cred-url');
            // An empty base URL means workspaces are only reachable in-cluster.
            urlEl.value = data.url || '';
            urlEl.placeholder = 'Not exposed (in-cluster only)';
            const nsEl = document.getElementById('coder-cred-namespace');
            if (nsEl) nsEl.value = data.namespace || '';
            fields.style.display = 'block';
        })
        .catch(function (err) {
            loading.style.display = 'none';
            errorEl.textContent = typeof err === 'string' ? err : 'Failed to load endpoint info.';
            errorEl.style.display = 'block';
        });
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
// The drawer renders the same editor as the create-lab wizard's Step 6
// (web/partials/template-editor.html) and its behaviour comes from the same
// module (web/static/template-editor.js). Only the drawer's own chrome — opening
// it against a lab, submitting to that lab, and the post-reload toast — lives
// here.
//
// The one real difference from the wizard is credentials. The wizard's tokens are
// still in the page, so it can send them. This lab's credentials are already in
// its cluster and must stay there, so the drawer offers their names and lets the
// server resolve one for the devcontainer import's clone (hence lab_id below).
// ---------------------------------------------------------------------------

var _utLabId = null;
// Credential names for this lab, refreshed from the cluster each time the drawer
// opens. Names only — the tokens never leave the server.
var _utGitCredentials = [];
var _utRegistryCredentials = [];

function utEl(id) { return document.getElementById(id); }

// showToast renders the app's floating toast (top-right, auto-dismissed by CSS).
function showToast(message, kind) {
    var toast = document.createElement('div');
    toast.className = 'toast toast-' + (kind === 'error' ? 'error' : 'success');
    toast.innerHTML = '<span class="toast-icon">' + (kind === 'error' ? '⚠️' : '✅') +
        '</span><span>' + TemplateEditor.escapeHtml(message) + '</span>';
    document.body.appendChild(toast);
    setTimeout(function () { toast.remove(); }, 4500);
}

// utLoadCredentials fills the editor's credential pickers from the lab's own
// cluster Secrets, so a template added here can reference a private repo or
// registry. Without this the pickers would offer nothing but "None".
function utLoadCredentials(labId) {
    return fetch('/api/labs/' + encodeURIComponent(labId) + '/secrets?format=json')
        .then(function (r) { return r.ok ? r.json() : Promise.reject(new Error('secrets failed')); })
        .then(function (secrets) {
            _utGitCredentials = [];
            _utRegistryCredentials = [];
            (secrets || []).forEach(function (s) {
                if (s.type === 'git') _utGitCredentials.push(s.name);
                else _utRegistryCredentials.push(s.name);
            });
        })
        .catch(function () {
            // An unreachable cluster leaves the pickers empty rather than blocking
            // the drawer: the other two modes still work, and YAML can name a
            // credential directly.
            _utGitCredentials = [];
            _utRegistryCredentials = [];
        })
        .finally(function () { TemplateEditor.refreshCredentialOptions(); });
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
        var html = 'Targeting lab <span class="ut-drawer-lab-chip">' +
            TemplateEditor.escapeHtml(labName || labId) + '</span>';
        var existing = (existingCsv || '').trim();
        if (existing) {
            html += '<span class="ut-drawer-existing">Already on this lab: ' +
                TemplateEditor.escapeHtml(existing) + '</span>';
        }
        contextBar.innerHTML = html;
    }

    if (form) form.reset();
    if (errorEl) { errorEl.style.display = 'none'; errorEl.textContent = ''; }

    utClearFormContainers();
    var dcResult = utEl('devcontainer-import-result'); if (dcResult) dcResult.innerHTML = '';
    var yamlVal = utEl('templates-yaml-validation'); if (yamlVal) yamlVal.innerHTML = '';
    var reviewBtn = utEl('btn-devcontainer-review-yaml'); if (reviewBtn) reviewBtn.style.display = 'none';
    utResetSubmitBtn();

    // Always open on the form mode with a git devcontainer source.
    TemplateEditor.setTemplatesMode('form');
    TemplateEditor.setDevcontainerSource('git');
    TemplateEditor.setDevcontainerCacheMode('external');

    utLoadCredentials(labId);

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
    ['.template-variables-container', '.template-sidecars-container',
     '.template-mounts-container', '.template-nodeselector-container'].forEach(function (sel) {
        var c = document.querySelector('#templates-form-mode ' + sel);
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

function submitUploadTemplate(event) {
    event.preventDefault();
    if (!_utLabId) return;

    var errorEl = utEl('upload-template-error');
    if (errorEl) { errorEl.style.display = 'none'; errorEl.textContent = ''; }

    var modeInput = utEl('templates_mode');
    var mode = modeInput ? modeInput.value : 'form';

    var body = new FormData();
    body.append('templates_mode', mode);

    if (mode === 'yaml') {
        var yamlArea = utEl('templates_yaml');
        if (!yamlArea || !yamlArea.value.trim()) {
            utShowError('Add some YAML, or switch to “Build with a form”.');
            return;
        }
        body.append('templates_yaml', yamlArea.value);
    } else {
        var nameEl = document.querySelector('#templates-form-mode [name="template_0_name"]');
        if (!nameEl || !nameEl.value.trim()) {
            utShowError('Template name is required.');
            return;
        }
        var formPanel = utEl('templates-form-mode');
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
            // Queue the toast to survive the reload that refreshes the page.
            try { sessionStorage.setItem('ut-flash', JSON.stringify({ msg: label, kind: 'success' })); } catch (e) { /* ignore */ }
            closeUploadTemplateModal();
            window.location.reload();
        })
        .catch(function (err) {
            utShowError(typeof err === 'string' ? err : 'Failed to add template.');
        });
}

document.addEventListener('DOMContentLoaded', function () {
    TemplateEditor.init({
        multi: false,
        gitCredentialNames: function () { return _utGitCredentials; },
        registryCredentialNames: function () { return _utRegistryCredentials; },
        // No token to send: this lab's credentials live in its cluster. The name
        // goes up instead and the server reads the Secret to do the import clone.
        gitCredentialAuth: function () { return { username: '', token: '' }; },
        decorateImport: function (body) {
            if (_utLabId) body.append('lab_id', _utLabId);
        },
    });

    // Show a toast queued before a reload (e.g. after a template was added).
    try {
        var flash = sessionStorage.getItem('ut-flash');
        if (flash) {
            sessionStorage.removeItem('ut-flash');
            var f = JSON.parse(flash);
            showToast(f.msg, f.kind);
        }
    } catch (e) { /* ignore */ }
});
