// Home page theme: follows the OS preference until the visitor picks one.
// Loaded in <head> so the theme is set before the first paint.
(function() {
    var STORAGE_KEY = 'easylab-home-theme';
    var root = document.documentElement;

    function storedTheme() {
        try {
            var value = localStorage.getItem(STORAGE_KEY);
            return value === 'light' || value === 'dark' ? value : null;
        } catch (e) {
            return null;
        }
    }

    function preferredTheme() {
        return window.matchMedia && window.matchMedia('(prefers-color-scheme: light)').matches ? 'light' : 'dark';
    }

    function applyTheme(theme) {
        root.dataset.theme = theme;
        var toggle = document.getElementById('homeThemeToggle');
        if (toggle) {
            var label = theme === 'light' ? 'Switch to dark theme' : 'Switch to light theme';
            toggle.setAttribute('aria-label', label);
            toggle.setAttribute('title', label);
        }
    }

    applyTheme(storedTheme() || preferredTheme());

    document.addEventListener('DOMContentLoaded', function() {
        applyTheme(root.dataset.theme);
        var toggle = document.getElementById('homeThemeToggle');
        if (!toggle) return;
        toggle.addEventListener('click', function() {
            var next = root.dataset.theme === 'light' ? 'dark' : 'light';
            try {
                localStorage.setItem(STORAGE_KEY, next);
            } catch (e) { /* storage unavailable: theme still applies for this visit */ }
            applyTheme(next);
        });
    });

    if (window.matchMedia) {
        var query = window.matchMedia('(prefers-color-scheme: light)');
        if (query.addEventListener) {
            query.addEventListener('change', function() {
                if (!storedTheme()) applyTheme(preferredTheme());
            });
        }
    }
})();
