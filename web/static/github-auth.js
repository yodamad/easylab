// GitHub login admin page: copy the callback URL, and turn GitHub login off.

(function () {
    var copyBtn = document.getElementById('github-callback-copy');
    var callbackInput = document.getElementById('github_callback_url');

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

    // Turning GitHub login off is saving the form with an empty client ID.
    var turnOffBtn = document.getElementById('github-auth-turn-off');
    if (turnOffBtn) {
        turnOffBtn.addEventListener('click', function () {
            document.getElementById('github_client_id').value = '';
            document.getElementById('github_client_secret').value = '';
            htmx.trigger('#github-auth-form', 'submit');
        });
    }
})();
