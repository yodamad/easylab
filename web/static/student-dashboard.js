// Request a workspace page: lets a student pick a lab + template, request a workspace,
// watch it start, and optionally encrypt & save its credentials locally. The saved
// workspaces themselves are shown on the separate My Workspaces page. Shared helpers
// (cookies, crypto, copy, escaping) live in student-common.js, loaded first.

document.addEventListener('DOMContentLoaded', function() {
    loadLabs();
    setupLabTemplateHandlers();

    document.addEventListener('htmx:afterRequest', function(evt) {
        if (evt.detail.elt && evt.detail.elt.id === 'workspace-request-form' && evt.detail.successful) {
            advanceStep(3);
        }
    });
});

function advanceStep(stepNum) {
    for (var i = 1; i <= 3; i++) {
        var step = document.getElementById('step-' + i);
        var divider = document.getElementById('step-divider-' + i);
        if (!step) continue;
        step.classList.remove('active', 'completed');
        if (i < stepNum) {
            step.classList.add('completed');
            if (divider) divider.classList.add('completed');
        } else if (i === stepNum) {
            step.classList.add('active');
        } else {
            if (divider) divider.classList.remove('completed');
        }
    }
}

async function encryptAndSaveWorkspaceInfo(button) {
    const responseDiv = document.getElementById('workspace-response');
    if (!responseDiv) {
        alert('Workspace information not found');
        return;
    }

    const dataElement = responseDiv.querySelector('[data-workspace-info]');
    if (!dataElement) {
        alert('Workspace information data not found');
        return;
    }

    try {
        const workspaceInfo = JSON.parse(dataElement.getAttribute('data-workspace-info'));
        await saveWorkspaceInfoWithEncryption(workspaceInfo);
    } catch (error) {
        console.error('Failed to parse workspace info:', error);
        alert('Failed to parse workspace information');
    }
}

function setupLabTemplateHandlers() {
    const labSelect = document.getElementById('lab_id');
    const tilesContainer = document.getElementById('template-tiles');
    const templateGroup = document.getElementById('template-select-group');
    const countEl = document.getElementById('template-picker-count');
    const submitBtn = document.getElementById('submit-btn');

    if (!labSelect || !tilesContainer || !templateGroup) return;

    function setSubmitEnabled(enabled) {
        if (submitBtn) submitBtn.disabled = !enabled;
    }

    function showStatus(message, isError) {
        tilesContainer.innerHTML = '';
        const p = document.createElement('p');
        p.className = 'template-tiles-status' + (isError ? ' template-tiles-status--error' : '');
        p.textContent = message;
        tilesContainer.appendChild(p);
        if (countEl) countEl.textContent = '';
    }

    // Nothing can be requested until a template is chosen — the picker starts hidden.
    setSubmitEnabled(false);

    labSelect.addEventListener('change', function() {
        const labId = this.value;

        if (!labId) {
            templateGroup.classList.add('template-group-hidden');
            tilesContainer.innerHTML = '';
            if (countEl) countEl.textContent = '';
            setSubmitEnabled(false);
            return;
        }

        templateGroup.classList.remove('template-group-hidden');
        showStatus('Loading templates…', false);
        setSubmitEnabled(false);

        fetch('/api/student/labs/templates?lab_id=' + encodeURIComponent(labId))
            .then(response => {
                if (!response.ok) {
                    throw new Error(response.statusText || 'Failed to load templates');
                }
                return response.json();
            })
            .then(templates => {
                renderTemplates(templates);
            })
            .catch(error => {
                console.error('Error loading templates:', error);
                showStatus("Couldn't load templates. Choose the lab again to retry.", true);
                setSubmitEnabled(false);
            });
    });

    function renderTemplates(templates) {
        if (!Array.isArray(templates) || templates.length === 0) {
            showStatus('No templates available for this lab.', false);
            setSubmitEnabled(false);
            return;
        }

        const single = templates.length === 1;
        tilesContainer.innerHTML = '';
        templates.forEach((t, i) => tilesContainer.appendChild(renderTile(t, single && i === 0)));
        if (countEl) countEl.textContent = single ? '1 available' : templates.length + ' available';

        // A lone template is pre-selected, so the request is ready right away;
        // otherwise the student must pick one first.
        setSubmitEnabled(single);
        tilesContainer.querySelectorAll('input[name="template_id"]').forEach(radio => {
            radio.addEventListener('change', function() {
                // Mirror the checked state as a class so the selected styling holds up
                // even where :has() isn't supported.
                tilesContainer.querySelectorAll('.template-tile').forEach(tile => tile.classList.remove('is-selected'));
                this.closest('.template-tile').classList.add('is-selected');
                setSubmitEnabled(true);
            });
        });

        advanceStep(2);
    }
}

// renderTile builds one selectable template card from a template option returned by
// /api/student/labs/templates. Built with DOM APIs (not innerHTML) so admin-authored
// names and descriptions can never break out into markup.
function renderTile(t, checked) {
    const label = document.createElement('label');
    label.className = 'template-tile';

    const input = document.createElement('input');
    input.type = 'radio';
    input.name = 'template_id';
    input.value = t.id;
    if (checked) {
        input.checked = true;
        label.classList.add('is-selected');
    }
    label.appendChild(input);

    const check = document.createElement('span');
    check.className = 'template-tile-check';
    check.setAttribute('aria-hidden', 'true');
    check.innerHTML = '<svg viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="3" stroke-linecap="round" stroke-linejoin="round"><path d="M4 10.5l4 4 8-9"/></svg>';
    label.appendChild(check);

    const name = document.createElement('span');
    name.className = 'template-tile-name';
    name.textContent = t.name;
    label.appendChild(name);

    if (t.description) {
        const desc = document.createElement('span');
        desc.className = 'template-tile-desc';
        desc.textContent = t.description;
        label.appendChild(desc);
    }

    const chips = [];
    if (t.ide) chips.push(['', t.ide]);
    if (t.resources) chips.push(['cpu', t.resources]);
    if (t.repo) chips.push(['repo', t.repo]);
    if (chips.length) {
        const meta = document.createElement('span');
        meta.className = 'template-tile-meta';
        chips.forEach(pair => {
            const chip = document.createElement('span');
            chip.className = 'template-tile-chip';
            if (pair[0]) {
                const key = document.createElement('span');
                key.className = 'template-tile-chip-key';
                key.textContent = pair[0];
                chip.appendChild(key);
                chip.appendChild(document.createTextNode(' ' + pair[1]));
            } else {
                chip.textContent = pair[1];
            }
            meta.appendChild(chip);
        });
        label.appendChild(meta);
    }

    return label;
}

function loadLabs() {
    fetch('/api/student/labs')
        .then(response => response.json())
        .then(data => {
            const select = document.getElementById('lab_id');
            select.classList.remove('loading');

            if (data.length === 0) {
                select.innerHTML = '<option value="">No labs available</option>';
                return;
            }

            select.innerHTML = '<option value="">Select a lab...</option>';
            data.forEach(lab => {
                const option = document.createElement('option');
                option.value = lab.id;
                option.textContent = `${lab.config.stack_name || lab.id}`;
                select.appendChild(option);
            });
        })
        .catch(error => {
            console.error('Error loading labs:', error);
            const select = document.getElementById('lab_id');
            if (select) {
                select.classList.remove('loading');
                select.innerHTML = '<option value="">Error loading labs</option>';
            }
        });
}

let _workspacePollTimer = null;

function startWorkspaceStatusPolling() {
    if (_workspacePollTimer) clearInterval(_workspacePollTimer);
    _workspacePollTimer = setInterval(function() {
        const pollEl = document.querySelector('[data-poll-url]');
        if (!pollEl) {
            clearInterval(_workspacePollTimer);
            return;
        }
        const pollUrl = pollEl.getAttribute('data-poll-url');
        fetch(pollUrl, { credentials: 'same-origin' })
            .then(function(r) { if (r.ok) return r.text(); })
            .then(function(html) {
                if (!html) return;
                const tmp = document.createElement('div');
                tmp.innerHTML = html;
                const newEl = tmp.firstElementChild;
                if (!newEl || !newEl.classList.contains('workspace-ready-status')) return;
                pollEl.replaceWith(newEl);
                if (newEl.classList.contains('workspace-ready-status--ready')) {
                    clearInterval(_workspacePollTimer);
                }
            })
            .catch(function() {});
    }, 2000);
}

const workspaceForm = document.getElementById('workspace-request-form');

if (typeof htmx !== 'undefined') {
    workspaceForm.addEventListener('htmx:beforeRequest', function() {
        const btn = document.getElementById('submit-btn');
        btn.disabled = true;
        btn.textContent = 'Requesting...';
        const fields = document.getElementById('workspace-form-fields');
        if (fields) fields.style.display = 'none';
    });

    workspaceForm.addEventListener('htmx:afterRequest', function(event) {
        const btn = document.getElementById('submit-btn');
        btn.disabled = false;
        btn.textContent = 'Request Workspace';
        if (!event.detail.successful) {
            const fields = document.getElementById('workspace-form-fields');
            if (fields) fields.style.display = '';
        }
        setTimeout(startWorkspaceStatusPolling, 100);
    });
} else {
    workspaceForm.addEventListener('submit', function(e) {
        e.preventDefault();

        const btn = document.getElementById('submit-btn');
        const responseDiv = document.getElementById('workspace-response');
        const fields = document.getElementById('workspace-form-fields');

        btn.disabled = true;
        btn.textContent = 'Requesting...';
        if (fields) fields.style.display = 'none';
        responseDiv.innerHTML = '<div class="student-loading">Requesting workspace...</div>';

        const formData = new FormData(workspaceForm);

        fetch('/api/student/workspace/request', {
            method: 'POST',
            body: formData
        })
        .then(response => response.text())
        .then(html => {
            responseDiv.innerHTML = html;
            btn.disabled = false;
            btn.textContent = 'Request Workspace';
            setTimeout(startWorkspaceStatusPolling, 100);
        })
        .catch(error => {
            responseDiv.innerHTML = `<div class="error-message">Error: ${error.message}</div>`;
            btn.disabled = false;
            btn.textContent = 'Request Workspace';
            if (fields) fields.style.display = '';
        });
    });
}
