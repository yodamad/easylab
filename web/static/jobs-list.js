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
