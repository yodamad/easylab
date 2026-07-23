// Shared helpers used by both student pages (Request a workspace and My Workspaces):
// cookie access, the client-side password encryption, HTML escaping, and clipboard
// copy. Keeping these in one place means the encrypt/decrypt logic can never drift
// between the two pages — a workspace encrypted on one is always readable on the other.

// Cookie utility functions
function getCookie(name) {
    const value = `; ${document.cookie}`;
    const parts = value.split(`; ${name}=`);
    if (parts.length === 2) return parts.pop().split(';').shift();
    return null;
}

function setCookie(name, value, days) {
    const expires = new Date();
    expires.setTime(expires.getTime() + (days * 24 * 60 * 60 * 1000));
    document.cookie = `${name}=${value};expires=${expires.toUTCString()};path=/`;
}

function deleteCookie(name) {
    document.cookie = `${name}=;expires=Thu, 01 Jan 1970 00:00:00 UTC;path=/;`;
}

function getAllWorkspaceCookies() {
    const cookies = document.cookie.split(';');
    const workspaces = [];
    for (const cookie of cookies) {
        const trimmed = cookie.trim();
        const match = trimmed.match(/^workspace_info_(.+?)=(.+)$/);
        if (match) {
            try {
                const info = JSON.parse(decodeURIComponent(match[2]));
                workspaces.push({ cookieName: `workspace_info_${match[1]}`, labId: info.lab_id || match[1], uniqueId: match[1], info });
            } catch (e) {
                console.error('Failed to parse workspace cookie:', trimmed, e);
            }
        }
    }
    return workspaces;
}

function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

// The inline clipboard glyph shared by every copy button.
const copyIconSvg = `<svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" class="copy-icon">
    <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z" />
</svg>`;

// Encryption/Decryption functions using Web Crypto API
async function deriveKey(email, studentPassword) {
    const encoder = new TextEncoder();
    const keyMaterial = encoder.encode(email + ':' + studentPassword);

    const key = await crypto.subtle.importKey(
        'raw',
        keyMaterial,
        { name: 'PBKDF2' },
        false,
        ['deriveBits', 'deriveKey']
    );

    const derivedKey = await crypto.subtle.deriveKey(
        {
            name: 'PBKDF2',
            salt: encoder.encode('lab-as-code-salt'),
            iterations: 100000,
            hash: 'SHA-256'
        },
        key,
        { name: 'AES-GCM', length: 256 },
        false,
        ['encrypt', 'decrypt']
    );

    return derivedKey;
}

async function encryptPassword(plaintext, email, studentPassword) {
    try {
        const key = await deriveKey(email, studentPassword);
        const encoder = new TextEncoder();
        const data = encoder.encode(plaintext);

        const iv = crypto.getRandomValues(new Uint8Array(12));

        const encrypted = await crypto.subtle.encrypt(
            { name: 'AES-GCM', iv: iv },
            key,
            data
        );

        const combined = new Uint8Array(iv.length + encrypted.byteLength);
        combined.set(iv);
        combined.set(new Uint8Array(encrypted), iv.length);

        return btoa(String.fromCharCode(...combined));
    } catch (error) {
        console.error('Encryption error:', error);
        throw error;
    }
}

async function decryptPassword(encryptedBase64, email, studentPassword) {
    try {
        const key = await deriveKey(email, studentPassword);

        const combined = Uint8Array.from(atob(encryptedBase64), c => c.charCodeAt(0));

        const iv = combined.slice(0, 12);
        const encrypted = combined.slice(12);

        const decrypted = await crypto.subtle.decrypt(
            { name: 'AES-GCM', iv: iv },
            key,
            encrypted
        );

        const decoder = new TextDecoder();
        return decoder.decode(decrypted);
    } catch (error) {
        console.error('Decryption error:', error);
        throw error;
    }
}

function promptStudentPassword(message = 'Enter your student password to decrypt workspace information:') {
    return new Promise((resolve, reject) => {
        const password = prompt(message);
        if (password === null) {
            reject(new Error('Password entry cancelled'));
        } else if (password === '') {
            reject(new Error('Password cannot be empty'));
        } else {
            resolve(password);
        }
    });
}

async function saveWorkspaceInfoWithEncryption(workspaceInfo) {
    try {
        const studentPassword = await promptStudentPassword('Enter your student password to encrypt and save workspace information:');

        const encryptedPassword = await encryptPassword(workspaceInfo.password, workspaceInfo.email, studentPassword);

        const encryptedInfo = {
            email: workspaceInfo.email,
            workspace_url: workspaceInfo.workspace_url,
            encrypted_password: encryptedPassword,
            workspace_name: workspaceInfo.workspace_name,
            lab_id: workspaceInfo.lab_id,
            lab_name: workspaceInfo.lab_name,
            template: workspaceInfo.template,
            created_at: workspaceInfo.created_at,
            deletion_at: workspaceInfo.deletion_at
        };

        const cookieName = `workspace_info_${workspaceInfo.lab_id}_${workspaceInfo.workspace_name}`;
        const cookieValue = encodeURIComponent(JSON.stringify(encryptedInfo));
        setCookie(cookieName, cookieValue, 1);

        alert('Workspace information encrypted and saved successfully!');
        // The workspaces list only exists on the My Workspaces page; refresh it there.
        if (typeof loadAllWorkspaceInfos === 'function') {
            setTimeout(loadAllWorkspaceInfos, 100);
        }

        return true;
    } catch (error) {
        console.error('Failed to encrypt and save workspace info:', error);
        if (error.message.includes('cancelled')) return false;
        alert('Failed to encrypt workspace information: ' + error.message);
        return false;
    }
}

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
