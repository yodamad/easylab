// Attaches the CSRF token issued at login (mirrored into a JS-readable
// cookie by the server) to every state-changing request, so the server-side
// csrfProtect check in auth.go can validate it. Loaded once from base.html
// so every page gets this without per-template changes.
(function () {
    function getCookie(name) {
        const match = document.cookie.match(new RegExp('(?:^|; )' + name + '=([^;]*)'));
        return match ? decodeURIComponent(match[1]) : '';
    }

    function csrfToken() {
        // Only one of these is set at a time (admin session vs student session).
        return getCookie('csrf_token') || getCookie('student_csrf_token');
    }

    // HTMX requests: attach the header HTMX will send along with the request.
    document.body.addEventListener('htmx:configRequest', function (event) {
        const token = csrfToken();
        if (token) {
            event.detail.headers['X-CSRF-Token'] = token;
        }
    });

    // Plain (non-HTMX) form POSTs: inject a hidden field before submit.
    document.addEventListener('submit', function (event) {
        const form = event.target;
        if (!(form instanceof HTMLFormElement)) return;
        if (form.hasAttribute('hx-post') || form.hasAttribute('hx-put') || form.hasAttribute('hx-patch') || form.hasAttribute('hx-delete')) {
            // HTMX intercepts this submit and issues its own request instead of a
            // native form submit; the header above already covers it.
            return;
        }
        if ((form.getAttribute('method') || 'get').toLowerCase() !== 'post') return;

        const token = csrfToken();
        if (!token) return;

        let input = form.querySelector('input[name="csrf_token"]');
        if (!input) {
            input = document.createElement('input');
            input.type = 'hidden';
            input.name = 'csrf_token';
            form.appendChild(input);
        }
        input.value = token;
    }, true);

    // Page JS calling fetch() directly (outside HTMX and native form submit)
    // otherwise never attaches the token, so the same csrfProtect check in
    // auth.go rejects it with 403. Wrap fetch once here so every state-changing,
    // same-origin call gets the header without each call site having to know about it.
    const SAFE_METHODS = ['GET', 'HEAD', 'OPTIONS', 'TRACE'];
    const originalFetch = window.fetch.bind(window);
    window.fetch = function (input, init) {
        const method = ((init && init.method) || (input instanceof Request ? input.method : 'GET') || 'GET').toUpperCase();
        if (SAFE_METHODS.includes(method)) return originalFetch(input, init);

        const url = input instanceof Request ? input.url : input;
        if (new URL(url, window.location.href).origin !== window.location.origin) {
            return originalFetch(input, init);
        }

        const token = csrfToken();
        if (!token) return originalFetch(input, init);

        const headers = new Headers((init && init.headers) || (input instanceof Request ? input.headers : undefined));
        headers.set('X-CSRF-Token', token);
        return originalFetch(input, Object.assign({}, init, { headers: headers }));
    };
})();
