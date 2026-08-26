// Request a workspace page: lets a student pick a lab + template, request a workspace,
// watch it start, and optionally encrypt & save its credentials locally. The saved
// workspaces themselves are shown on the separate My Workspaces page. Shared helpers
// (cookies, crypto, copy, escaping) live in student-common.js, loaded first.

document.addEventListener('DOMContentLoaded', function() {
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
    const labTilesContainer = document.getElementById('lab-tiles');
    const labCountEl = document.getElementById('lab-picker-count');
    const templateTilesContainer = document.getElementById('template-tiles');
    const templateGroup = document.getElementById('template-select-group');
    const templateCountEl = document.getElementById('template-picker-count');
    const submitBtn = document.getElementById('submit-btn');

    if (!labTilesContainer || !templateTilesContainer || !templateGroup) return;

    function setSubmitEnabled(enabled) {
        if (submitBtn) submitBtn.disabled = !enabled;
    }

    function showTilesStatus(container, countEl, message, isError) {
        container.innerHTML = '';
        const p = document.createElement('p');
        p.className = 'template-tiles-status' + (isError ? ' template-tiles-status--error' : '');
        p.textContent = message;
        container.appendChild(p);
        if (countEl) countEl.textContent = '';
    }

    // Nothing can be requested until a lab and a template are chosen.
    setSubmitEnabled(false);

    function loadTemplatesForLab(labId) {
        templateGroup.classList.remove('template-group-hidden');
        showTilesStatus(templateTilesContainer, templateCountEl, 'Loading templates…', false);
        setSubmitEnabled(false);

        // Picking a different lab before this request resolves must not let its
        // response render over the newer selection's tiles.
        const isStale = () => {
            const checked = labTilesContainer.querySelector('input[name="lab_id"]:checked');
            return !checked || checked.value !== labId;
        };

        fetch('/api/student/labs/templates?lab_id=' + encodeURIComponent(labId))
            .then(response => {
                if (!response.ok) {
                    throw new Error(response.statusText || 'Failed to load templates');
                }
                return response.json();
            })
            .then(templates => {
                if (isStale()) return;
                renderTemplates(templates);
            })
            .catch(error => {
                if (isStale()) return;
                console.error('Error loading templates:', error);
                showTilesStatus(templateTilesContainer, templateCountEl, "Couldn't load templates. Choose the lab again to retry.", true);
                setSubmitEnabled(false);
            });
    }

    function renderTemplates(templates) {
        if (!Array.isArray(templates) || templates.length === 0) {
            showTilesStatus(templateTilesContainer, templateCountEl, 'No templates available for this lab.', false);
            setSubmitEnabled(false);
            return;
        }

        const single = templates.length === 1;
        templateTilesContainer.innerHTML = '';
        templates.forEach((t, i) => templateTilesContainer.appendChild(renderTemplateTile(t, single && i === 0)));
        if (templateCountEl) templateCountEl.textContent = single ? '1 available' : templates.length + ' available';

        // A lone template is pre-selected, so the request is ready right away;
        // otherwise the student must pick one first.
        setSubmitEnabled(single);
        templateTilesContainer.querySelectorAll('input[name="template_id"]').forEach(radio => {
            radio.addEventListener('change', function() {
                // Mirror the checked state as a class so the selected styling holds up
                // even where :has() isn't supported.
                templateTilesContainer.querySelectorAll('.template-tile').forEach(tile => tile.classList.remove('is-selected'));
                this.closest('.template-tile').classList.add('is-selected');
                setSubmitEnabled(true);
            });
        });

        advanceStep(2);
    }

    function wireLabTiles() {
        labTilesContainer.querySelectorAll('input[name="lab_id"]').forEach(radio => {
            radio.addEventListener('change', function() {
                labTilesContainer.querySelectorAll('.template-tile').forEach(tile => tile.classList.remove('is-selected'));
                this.closest('.template-tile').classList.add('is-selected');
                loadTemplatesForLab(this.value);
            });
        });
    }

    function loadLabs() {
        showTilesStatus(labTilesContainer, labCountEl, 'Loading environments…', false);

        fetch('/api/student/labs')
            .then(response => response.json())
            .then(labs => {
                if (!Array.isArray(labs) || labs.length === 0) {
                    showTilesStatus(labTilesContainer, labCountEl, 'No labs available.', false);
                    return;
                }

                const single = labs.length === 1;
                labTilesContainer.innerHTML = '';
                labs.forEach((lab, i) => labTilesContainer.appendChild(renderLabTile(lab, single && i === 0)));
                if (labCountEl) labCountEl.textContent = single ? '1 available' : labs.length + ' available';
                wireLabTiles();

                // A lone lab is pre-selected, so template loading starts right away.
                if (single) loadTemplatesForLab(labs[0].id);
            })
            .catch(error => {
                console.error('Error loading labs:', error);
                showTilesStatus(labTilesContainer, labCountEl, "Couldn't load environments. Reload the page to retry.", true);
            });
    }

    loadLabs();
}

// renderPickerTile builds one selectable card for a radio group from
// {id, name, description, chips} — shared by the lab and template pickers on this
// page. Built with DOM APIs (not innerHTML) so admin-authored names and
// descriptions can never break out into markup.
function renderPickerTile(item, checked, groupName) {
    const label = document.createElement('label');
    label.className = 'template-tile';

    const input = document.createElement('input');
    input.type = 'radio';
    input.name = groupName;
    input.value = item.id;
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
    name.textContent = item.name;
    label.appendChild(name);

    if (item.description) {
        const desc = document.createElement('span');
        desc.className = 'template-tile-desc';
        desc.textContent = item.description;
        label.appendChild(desc);
    }

    if (item.chips && item.chips.length) {
        const meta = document.createElement('span');
        meta.className = 'template-tile-meta';
        item.chips.forEach(pair => {
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

// renderTemplateTile builds one selectable template card from a template option
// returned by /api/student/labs/templates.
function renderTemplateTile(t, checked) {
    const chips = [];
    if (t.resources) chips.push(['cpu', t.resources]);
    return renderPickerTile({ id: t.id, name: t.name, description: t.description, chips: chips }, checked, 'template_id');
}

// renderLabTile builds one selectable lab card from a job returned by
// /api/student/labs.
function renderLabTile(lab, checked) {
    const config = lab.config || {};
    return renderPickerTile({
        id: lab.id,
        name: config.stack_name || lab.id,
        description: config.description,
        chips: []
    }, checked, 'lab_id');
}

let _workspacePollTimer = null;

// Polling backs off from 2s up to 10s the longer a workspace takes to come up,
// instead of a fixed 2s interval forever. A fixed interval means every student in
// a workshop polls in lockstep for as long as their workspace takes to start,
// which adds up fast during a burst of many students provisioning at once.
const WORKSPACE_POLL_MIN_DELAY_MS = 2000;
const WORKSPACE_POLL_MAX_DELAY_MS = 10000;
const WORKSPACE_POLL_BACKOFF_STEP_MS = 1000;

function startWorkspaceStatusPolling() {
    if (_workspacePollTimer) clearTimeout(_workspacePollTimer);
    let delay = WORKSPACE_POLL_MIN_DELAY_MS;

    function scheduleNext() {
        delay = Math.min(delay + WORKSPACE_POLL_BACKOFF_STEP_MS, WORKSPACE_POLL_MAX_DELAY_MS);
        _workspacePollTimer = setTimeout(poll, delay);
    }

    function poll() {
        const pollEl = document.querySelector('[data-poll-url]');
        if (!pollEl) {
            _workspacePollTimer = null;
            return;
        }
        const pollUrl = pollEl.getAttribute('data-poll-url');
        fetch(pollUrl, { credentials: 'same-origin' })
            .then(function(r) { if (r.ok) return r.text(); })
            .then(function(html) {
                if (!html) {
                    scheduleNext();
                    return;
                }
                const tmp = document.createElement('div');
                tmp.innerHTML = html;
                const newEl = tmp.firstElementChild;
                if (!newEl || !newEl.classList.contains('workspace-ready-status')) {
                    scheduleNext();
                    return;
                }
                pollEl.replaceWith(newEl);
                if (newEl.classList.contains('workspace-ready-status--ready')) {
                    _workspacePollTimer = null;
                    return;
                }
                scheduleNext();
            })
            .catch(function() { scheduleNext(); });
    }

    _workspacePollTimer = setTimeout(poll, delay);
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
