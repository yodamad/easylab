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

function recreateLab(labId) {
    // Send POST request to recreate endpoint
    fetch('/api/labs/recreate', {
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
            console.error('Recreate failed:', response.status);
        }
    })
    .catch(error => {
        console.error('Recreate error:', error);
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
            document.getElementById('coder-cred-email').value = data.email || '';
            document.getElementById('coder-cred-password').value = data.password || '';
            var pwdInput = document.getElementById('coder-cred-password');
            pwdInput.type = 'password';
            var toggleBtn = document.querySelector('.credentials-toggle-password');
            if (toggleBtn) toggleBtn.textContent = 'Show';
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
