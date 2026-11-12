// Shared helpers used by the student pages (Request a workspace and My Workspaces):
// HTML escaping and clipboard copy.

function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

// escapeHtml() doesn't escape single quotes, which is fine for element text
// content but unsafe when the result is embedded inside a single-quoted JS
// string literal in an inline attribute, e.g. onclick="fn('${...}')" — use
// this instead at those specific call sites.
function escapeHtmlAttr(text) {
    return escapeHtml(text).replace(/'/g, '&#39;');
}

// safeHttpURL returns the normalized href for raw only when it parses as an
// absolute http: or https: URL, and '' otherwise (blocks javascript:, data:, ...).
// The WHATWG parser leaves characters like " ' < > ` unencoded in a host, so a
// href still containing any of them (or whitespace) is refused: the result is
// also embedded in HTML attribute strings, where a " would end the attribute.
function safeHttpURL(raw) {
    if (!raw) return '';
    try {
        const u = new URL(raw);
        if (u.protocol !== 'http:' && u.protocol !== 'https:') return '';
        return /["'<>`\\\s]/.test(u.href) ? '' : u.href;
    } catch (e) {
        return '';
    }
}

// resourceHostname returns the host of an http(s) URL ('' when it does not parse).
function resourceHostname(href) {
    try {
        return new URL(href).hostname;
    } catch (e) {
        return '';
    }
}

// The inline "slides" glyph shared by every link to a template's resource link.
const slidesIconSvg = `<svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" class="slides-icon" aria-hidden="true" focusable="false"><rect x="3" y="4" width="18" height="12" rx="2"/><path d="M12 16v4M8 20h8"/></svg>`;

// The inline clipboard glyph shared by every copy button.
const copyIconSvg = `<svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" class="copy-icon">
    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z" />
</svg>`;

async function copyToClipboard(text, button) {
    try {
        await navigator.clipboard.writeText(text);

        const originalHTML = button.innerHTML;
        button.innerHTML = `
            <svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" class="copy-icon">
                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M5 13l4 4L19 7" />
            </svg>
        `;
        button.classList.add('copied');

        setTimeout(() => {
            button.innerHTML = originalHTML;
            button.classList.remove('copied');
        }, 2000);
    } catch (error) {
        console.error('Failed to copy:', error);
        const textArea = document.createElement('textarea');
        textArea.value = text;
        textArea.style.position = 'fixed';
        textArea.style.opacity = '0';
        document.body.appendChild(textArea);
        textArea.select();
        try {
            document.execCommand('copy');
            button.classList.add('copied');
            setTimeout(() => button.classList.remove('copied'), 2000);
        } catch (err) {
            alert('Failed to copy to clipboard');
        }
        document.body.removeChild(textArea);
    }
}

function attachCopyButtonListeners() {
    document.querySelectorAll('.copy-btn[data-copy-text]').forEach(button => {
        if (!button.hasAttribute('data-listener-attached')) {
            button.setAttribute('data-listener-attached', 'true');
            button.addEventListener('click', function() {
                const text = this.getAttribute('data-copy-text');
                copyToClipboard(text, this);
            });
        }
    });
}

// resumeWorkspace asks the server to start a workspace that was paused for
// inactivity. resumeUrl comes from the data-resume-url attribute of the status
// partial. Resolves with the new status partial's HTML, or null when the
// workspace could not be resumed.
async function resumeWorkspace(resumeUrl) {
    try {
        const resp = await fetch(resumeUrl, { method: 'POST', credentials: 'same-origin' });
        if (!resp.ok) return null;
        return await resp.text();
    } catch (e) {
        console.error('Failed to resume workspace:', e);
        return null;
    }
}
