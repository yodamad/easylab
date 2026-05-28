// Persists the last-used provider in sessionStorage (used by the lab creation wizard).

(function (global) {
    var STORAGE_KEY = 'easylab_header_provider';

    function normalizeProvider(p) {
        return p === 'azure' ? 'azure' : 'ovh';
    }

    global.getEasylabHeaderProviderPreference = function () {
        try {
            var stored = sessionStorage.getItem(STORAGE_KEY);
            if (stored === 'azure' || stored === 'ovh') {
                return stored;
            }
        } catch (_) { /* ignore */ }
        return 'ovh';
    };

    global.setEasylabHeaderProviderPreference = function (provider) {
        try {
            sessionStorage.setItem(STORAGE_KEY, normalizeProvider(provider));
        } catch (_) { /* ignore */ }
    };

    document.addEventListener('DOMContentLoaded', function () {
        document.querySelectorAll('.header-dropdown').forEach(function (dropdown) {
            var toggle = dropdown.querySelector('.header-dropdown-toggle');
            if (!toggle) return;

            toggle.addEventListener('click', function (e) {
                e.stopPropagation();
                dropdown.classList.toggle('open');
            });
        });

        document.addEventListener('click', function () {
            document.querySelectorAll('.header-dropdown.open').forEach(function (d) {
                d.classList.remove('open');
            });
        });
    });
})(typeof window !== 'undefined' ? window : this);
