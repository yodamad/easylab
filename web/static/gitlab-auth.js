// GitLab login admin page: copy the callback URL, and turn GitLab login off.

(function () {
    var copyBtn = document.getElementById('gitlab-callback-copy');
    var callbackInput = document.getElementById('gitlab_callback_url');

    function showCopied() {
        var orig = copyBtn.textContent;
        copyBtn.textContent = 'Copied';
        setTimeout(function () { copyBtn.textContent = orig; }, 2000);
    }

    function fallbackCopy() {
        callbackInput.select();
        try {
            document.execCommand('copy');
            showCopied();
        } catch (e) {}
    }

    if (copyBtn && callbackInput) {
        copyBtn.addEventListener('click', function () {
            if (navigator.clipboard && navigator.clipboard.writeText) {
                navigator.clipboard.writeText(callbackInput.value).then(showCopied).catch(fallbackCopy);
            } else {
                fallbackCopy();
            }
        });
    }

    // Turning GitLab login off is saving the form with an empty application ID.
    var turnOffBtn = document.getElementById('gitlab-auth-turn-off');
    if (turnOffBtn) {
        turnOffBtn.addEventListener('click', function () {
            document.getElementById('gitlab_client_id').value = '';
            document.getElementById('gitlab_client_secret').value = '';
            htmx.trigger('#gitlab-auth-form', 'submit');
        });
    }
})();
