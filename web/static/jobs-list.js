function destroyStack(jobId) {
    // Send POST request to destroy endpoint
    fetch('/api/stacks/destroy', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/x-www-form-urlencoded',
        },
        body: 'job_id=' + encodeURIComponent(jobId)
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

let recreateChoiceJobId = null;

// openRecreateChoiceModal presents the "rerun as-is or edit configuration
// first" choice before recreating a destroyed lab.
function openRecreateChoiceModal(jobId) {
    recreateChoiceJobId = jobId;
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
    const jobId = recreateChoiceJobId;
    closeRecreateChoiceModal();
    if (jobId) recreateLab(jobId);
}

function confirmRecreateEdit() {
    const jobId = recreateChoiceJobId;
    closeRecreateChoiceModal();
    if (jobId) {
        window.location.href = '/admin?prefill_job=' + encodeURIComponent(jobId) + '&prefill_action=recreate';
    }
}

function recreateLab(jobId) {
    // Send POST request to recreate endpoint
    fetch('/api/labs/recreate', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/x-www-form-urlencoded',
        },
        body: 'job_id=' + encodeURIComponent(jobId)
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

let retryChoiceJobId = null;

// openRetryChoiceModal presents the "rerun as-is or edit configuration first"
// choice before retrying a failed job.
function openRetryChoiceModal(jobId) {
    retryChoiceJobId = jobId;
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
    const jobId = retryChoiceJobId;
    closeRetryChoiceModal();
    if (jobId) retryJob(jobId);
}

function confirmRetryEdit() {
    const jobId = retryChoiceJobId;
    closeRetryChoiceModal();
    if (jobId) {
        window.location.href = '/admin?prefill_job=' + encodeURIComponent(jobId) + '&prefill_action=retry';
    }
}

function retryJob(jobId) {
    // Send POST request to retry endpoint
    fetch('/api/jobs/' + encodeURIComponent(jobId) + '/retry', {
        method: 'POST',
        headers: {
            'Content-Type': 'application/x-www-form-urlencoded',
        }
    })
    .then(response => {
        if (response.ok) {
            // Redirect to admin page to view retry progress
            window.location.href = '/admin?job=' + encodeURIComponent(jobId);
        } else {
            // Handle error
            response.text().then(text => {
                console.error('Retry failed:', response.status, text);
                alert('Failed to retry job: ' + response.status);
            });
        }
    })
    .catch(error => {
        console.error('Retry error:', error);
        alert('Error retrying job: ' + error.message);
    });
}
