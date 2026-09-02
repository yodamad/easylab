// Page-specific JS for the labs list (web/labs-list.html). The actual
// destroy/retry/recreate/etc. network calls and their modals now live in
// web/static/lab-detail.js (included alongside this file), which both the
// labs list's retained quick-action buttons and the lab detail page call
// into — this file only keeps what's specific to the list page itself.

document.addEventListener('DOMContentLoaded', function () {
    if (typeof syncEasylabHeaderProviderDropdown === 'function') {
        var pref =
            typeof getEasylabHeaderProviderPreference === 'function'
                ? getEasylabHeaderProviderPreference()
                : 'ovh';
        syncEasylabHeaderProviderDropdown(pref);
    }
});

// ---------------------------------------------------------------------------
// Student Portal Login — reveals the shared LAB_STUDENT_PASSWORD value, so an
// admin without shell/deployment access can hand it out to students. Fetched
// fresh every time the modal opens (the value can change across restarts).
// ---------------------------------------------------------------------------

function openStudentPasswordModal() {
    var overlay = document.getElementById('student-password-overlay');
    var loading = document.getElementById('student-password-loading');
    var errorEl = document.getElementById('student-password-error');
    var fields = document.getElementById('student-password-fields');
    var unset = document.getElementById('student-password-unset');
    if (!overlay || !loading || !errorEl || !fields || !unset) return;

    overlay.classList.add('visible');
    overlay.setAttribute('aria-hidden', 'false');
    loading.style.display = 'block';
    errorEl.style.display = 'none';
    errorEl.textContent = '';
    fields.style.display = 'none';
    unset.style.display = 'none';

    fetch('/api/student-portal-password')
        .then(function (response) {
            if (!response.ok) return Promise.reject('Could not load the password (' + response.status + ').');
            return response.json();
        })
        .then(function (data) {
            loading.style.display = 'none';
            if (!data.set) {
                unset.style.display = 'block';
                return;
            }
            var valueEl = document.getElementById('student-password-value');
            if (valueEl) valueEl.value = data.password || '';
            fields.style.display = 'block';
        })
        .catch(function (err) {
            loading.style.display = 'none';
            errorEl.textContent = typeof err === 'string' ? err : 'Could not load the password.';
            errorEl.style.display = 'block';
        });
}

function closeStudentPasswordModal() {
    var overlay = document.getElementById('student-password-overlay');
    if (!overlay) return;
    overlay.classList.remove('visible');
    overlay.setAttribute('aria-hidden', 'true');
    var valueEl = document.getElementById('student-password-value');
    if (valueEl) valueEl.value = '';
}

function copyStudentPasswordToClipboard(copyBtn) {
    var input = document.getElementById('student-password-value');
    if (!input || !input.value) return;
    var text = input.value;

    function showCopied() {
        if (!copyBtn) return;
        var orig = copyBtn.textContent;
        copyBtn.textContent = 'Copied!';
        setTimeout(function () { copyBtn.textContent = orig; }, 2000);
    }

    if (navigator.clipboard && navigator.clipboard.writeText) {
        navigator.clipboard.writeText(text).then(showCopied).catch(function () {
            fallbackCopyStudentPassword(text, showCopied);
        });
    } else {
        fallbackCopyStudentPassword(text, showCopied);
    }
}

function fallbackCopyStudentPassword(text, onDone) {
    var ta = document.createElement('textarea');
    ta.value = text;
    ta.setAttribute('readonly', '');
    ta.style.position = 'absolute';
    ta.style.left = '-9999px';
    document.body.appendChild(ta);
    ta.select();
    try {
        document.execCommand('copy');
        onDone();
    } catch (e) {}
    document.body.removeChild(ta);
}
