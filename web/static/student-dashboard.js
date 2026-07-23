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
    const templateSelect = document.getElementById('template_id');
    const templateGroup = document.getElementById('template-select-group');

    if (!labSelect || !templateSelect || !templateGroup) return;

    labSelect.addEventListener('change', function() {
        const labId = this.value;
        templateSelect.innerHTML = '<option value="">Loading templates...</option>';
        templateGroup.classList.add('template-group-hidden');
        templateSelect.removeAttribute('required');

        if (!labId) {
            templateSelect.innerHTML = '<option value="">Select a lab first...</option>';
            return;
        }

        fetch('/api/student/labs/templates?lab_id=' + encodeURIComponent(labId))
            .then(response => {
                if (!response.ok) {
                    throw new Error(response.statusText || 'Failed to load templates');
                }
                return response.json();
            })
            .then(templates => {
                templateSelect.innerHTML = '';

                if (templates.length === 0) {
                    templateSelect.innerHTML = '<option value="">No templates available</option>';
                    return;
                }

                if (templates.length === 1) {
                    const opt = document.createElement('option');
                    opt.value = templates[0].id;
                    opt.textContent = templates[0].name;
                    opt.selected = true;
                    templateSelect.appendChild(opt);
                } else {
                    templateSelect.innerHTML = '<option value="">Choose a template...</option>';
                    templates.forEach(t => {
                        const opt = document.createElement('option');
                        opt.value = t.id;
                        opt.textContent = t.name;
                        templateSelect.appendChild(opt);
                    });
                }

                templateGroup.classList.remove('template-group-hidden');
                templateSelect.setAttribute('required', 'required');
                advanceStep(2);
            })
            .catch(error => {
                console.error('Error loading templates:', error);
                templateSelect.innerHTML = '<option value="">Error loading templates</option>';
            });
    });
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
