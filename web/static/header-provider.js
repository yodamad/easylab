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
})(typeof window !== 'undefined' ? window : this);
