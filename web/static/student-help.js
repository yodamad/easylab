// The "Ask for help" button in the student portal header: raising a request,
// following it until an organiser picks it up, and withdrawing it.
//
// This is JS rather than HTMX on purpose: My Workspaces does not load HTMX at
// all, so one fetch-based path serves both pages that carry the button. The poll
// is jittered like the one in student-notifications.js — a room full of students
// on a fixed interval would all hit the endpoint together.
//
// csrf.js patches window.fetch and picks the student token for /api/student/*,
// so nothing here has to deal with CSRF.

const HELP_POLL_BASE_MS = 20000;
const HELP_POLL_JITTER_MS = 5000;

// The request currently open, as the server last described it, or null.
let _studentHelpRequest = null;
let _studentHelpPollTimer = null;
let _studentHelpBusy = false;

function studentHelpEls() {
    return {
        button: document.getElementById('student-help-btn'),
        label: document.getElementById('student-help-btn-label'),
        dialog: document.getElementById('student-help-dialog'),
        form: document.getElementById('student-help-form'),
        lab: document.getElementById('student-help-lab'),
        labRow: document.getElementById('student-help-lab-row'),
        message: document.getElementById('student-help-message'),
        error: document.getElementById('student-help-error'),
        submit: document.getElementById('student-help-submit'),
        cancel: document.getElementById('student-help-cancel'),
    };
}

// renderStudentHelpButton puts the request's state on the button itself, which
// is the only place a student looks: there is no panel to open and nothing to
// dismiss.
function renderStudentHelpButton() {
    const els = studentHelpEls();
    if (!els.button || !els.label) return;

    if (!_studentHelpRequest) {
        els.button.classList.remove('student-help-btn--active', 'student-help-btn--handling');
        els.label.textContent = 'Ask for help';
        els.button.title = 'Ask your organiser for help';
        els.button.setAttribute('aria-label', els.button.title);
        return;
    }

    els.button.classList.add('student-help-btn--active');
    if (_studentHelpRequest.status === 'handling') {
        els.button.classList.add('student-help-btn--handling');
        // Kept as short as the header's other labels: anything longer wraps to a
        // third line and makes this button taller than its neighbours.
        els.label.textContent = 'Help on the way';
        els.button.title = 'An organiser has picked up your request. Click to see it or withdraw it.';
    } else {
        els.button.classList.remove('student-help-btn--handling');
        els.label.textContent = 'Help requested';
        els.button.title = 'Your request is waiting. Click to change it or withdraw it.';
    }
    els.button.setAttribute('aria-label', els.button.title);
}

async function loadStudentHelpState() {
    try {
        const resp = await fetch('/api/student/help', { credentials: 'same-origin' });
        if (!resp.ok) return;
        const payload = await resp.json();
        const items = Array.isArray(payload.requests) ? payload.requests : [];
        _studentHelpRequest = items.length ? items[0] : null;
        renderStudentHelpButton();
        renderStudentHelpDialogState();
    } catch (e) {
        // A failed poll is not worth telling the student about.
        console.error('Failed to load your help request:', e);
    }
}

// loadStudentHelpLabs fills the lab picker from the labs this student can see.
// With a single lab — the usual case, and always the case on a lab's own portal
// — the row is hidden: there is nothing to choose.
async function loadStudentHelpLabs() {
    const els = studentHelpEls();
    if (!els.lab) return;
    try {
        const resp = await fetch('/api/student/labs', { credentials: 'same-origin' });
        if (!resp.ok) return;
        const labs = await resp.json();
        const entries = Array.isArray(labs) ? labs : [];
        // replaceChildren, not innerHTML: the option text is a lab name an admin
        // typed, and it only ever goes in through textContent below.
        els.lab.replaceChildren();
        entries.forEach(lab => {
            const option = document.createElement('option');
            option.value = lab.id;
            option.textContent = (lab.config && lab.config.stack_name) ? lab.config.stack_name : lab.id;
            els.lab.appendChild(option);
        });
        if (els.labRow) {
            els.labRow.style.display = entries.length > 1 ? '' : 'none';
        }
    } catch (e) {
        console.error('Failed to load your labs:', e);
    }
}

// renderStudentHelpDialogState reflects an open request inside the dialog, so
// reopening it shows what was asked and offers to withdraw it.
function renderStudentHelpDialogState() {
    const els = studentHelpEls();
    if (!els.cancel || !els.submit) return;

    if (_studentHelpRequest) {
        els.cancel.style.display = '';
        els.submit.textContent = 'Update';
        if (els.lab && _studentHelpRequest.lab_id) {
            els.lab.value = _studentHelpRequest.lab_id;
        }
    } else {
        els.cancel.style.display = 'none';
        els.submit.textContent = 'Ask for help';
    }
}

function showStudentHelpDialog() {
    const els = studentHelpEls();
    if (!els.dialog) return;
    if (els.error) {
        els.error.textContent = '';
        els.error.style.display = 'none';
    }
    if (els.message) {
        els.message.value = _studentHelpRequest ? (_studentHelpRequest.message || '') : '';
    }
    renderStudentHelpDialogState();
    els.dialog.style.display = '';
    els.dialog.setAttribute('aria-hidden', 'false');
    if (els.message) els.message.focus();
}

function hideStudentHelpDialog() {
    const els = studentHelpEls();
    if (!els.dialog) return;
    els.dialog.style.display = 'none';
    els.dialog.setAttribute('aria-hidden', 'true');
}

function showStudentHelpError(message) {
    const els = studentHelpEls();
    if (!els.error) return;
    els.error.textContent = message;
    els.error.style.display = '';
}

async function submitStudentHelp(event) {
    if (event) event.preventDefault();
    if (_studentHelpBusy) return;

    const els = studentHelpEls();
    const labId = els.lab ? els.lab.value : '';
    if (!labId) {
        showStudentHelpError('Pick the lab you need help with.');
        return;
    }

    _studentHelpBusy = true;
    if (els.submit) els.submit.disabled = true;
    try {
        const body = new URLSearchParams();
        body.set('lab_id', labId);
        body.set('message', els.message ? els.message.value : '');
        const resp = await fetch('/api/student/help', {
            method: 'POST',
            credentials: 'same-origin',
            headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
            body: body.toString(),
        });
        const payload = await resp.json().catch(() => ({}));
        if (!resp.ok || !payload.success) {
            showStudentHelpError(payload.error || 'Could not send your request. Try again.');
            return;
        }
        _studentHelpRequest = payload.request || null;
        renderStudentHelpButton();
        hideStudentHelpDialog();
    } catch (e) {
        console.error('Failed to ask for help:', e);
        showStudentHelpError('Could not send your request. Try again.');
    } finally {
        _studentHelpBusy = false;
        if (els.submit) els.submit.disabled = false;
    }
}

async function cancelStudentHelp() {
    if (_studentHelpBusy || !_studentHelpRequest) return;

    const els = studentHelpEls();
    _studentHelpBusy = true;
    if (els.cancel) els.cancel.disabled = true;
    try {
        const body = new URLSearchParams();
        body.set('lab_id', _studentHelpRequest.lab_id);
        const resp = await fetch('/api/student/help/cancel', {
            method: 'POST',
            credentials: 'same-origin',
            headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
            body: body.toString(),
        });
        const payload = await resp.json().catch(() => ({}));
        if (!resp.ok || !payload.success) {
            showStudentHelpError(payload.error || 'Could not withdraw your request. Try again.');
            return;
        }
        _studentHelpRequest = null;
        renderStudentHelpButton();
        hideStudentHelpDialog();
    } catch (e) {
        console.error('Failed to withdraw your help request:', e);
        showStudentHelpError('Could not withdraw your request. Try again.');
    } finally {
        _studentHelpBusy = false;
        if (els.cancel) els.cancel.disabled = false;
    }
}

function scheduleStudentHelpPoll() {
    const delay = HELP_POLL_BASE_MS + Math.random() * HELP_POLL_JITTER_MS;
    _studentHelpPollTimer = setTimeout(async () => {
        await loadStudentHelpState();
        scheduleStudentHelpPoll();
    }, delay);
}

document.addEventListener('DOMContentLoaded', () => {
    const els = studentHelpEls();
    if (!els.button) return;

    els.button.addEventListener('click', showStudentHelpDialog);
    if (els.form) els.form.addEventListener('submit', submitStudentHelp);
    if (els.cancel) els.cancel.addEventListener('click', cancelStudentHelp);

    // Clicking the backdrop or pressing Escape closes the dialog, as the other
    // overlays in the portal do.
    if (els.dialog) {
        els.dialog.addEventListener('click', event => {
            if (event.target === els.dialog) hideStudentHelpDialog();
        });
    }
    document.addEventListener('keydown', event => {
        if (event.key === 'Escape') hideStudentHelpDialog();
    });
    const close = document.getElementById('student-help-close');
    if (close) close.addEventListener('click', hideStudentHelpDialog);

    loadStudentHelpLabs();
    loadStudentHelpState();
    scheduleStudentHelpPoll();
});
