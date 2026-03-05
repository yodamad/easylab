document.addEventListener('DOMContentLoaded', function() {
    loadLabs();
    loadAllWorkspaceInfos();
});

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
                workspaces.push({ cookieName: `workspace_info_${match[1]}`, labId: match[1], info });
            } catch (e) {
                console.error('Failed to parse workspace cookie:', trimmed, e);
            }
        }
    }
    return workspaces;
}

function loadAllWorkspaceInfos() {
    const workspaces = getAllWorkspaceCookies();
    const section = document.getElementById('workspaces-list-section');
    const container = document.getElementById('workspaces-list-container');
    if (!section || !container) return;

    if (workspaces.length === 0) {
        section.style.display = 'none';
        container.innerHTML = '';
        return;
    }

    section.style.display = 'block';
    container.innerHTML = workspaces.map(ws => renderWorkspaceCard(ws.info, ws.labId)).join('');

    const card = document.querySelector('#workspaces-list-section .collapsible-card');
    const content = document.getElementById('workspaces-list-content');
    if (card && content) {
        card.classList.add('collapsed');
        content.style.maxHeight = '0';
    }

    setTimeout(() => attachCopyButtonListeners(), 10);
}

function toggleWorkspacesPanel() {
    const card = document.querySelector('#workspaces-list-section .collapsible-card');
    const content = document.getElementById('workspaces-list-content');
    if (!card || !content) return;

    if (card.classList.contains('collapsed')) {
        card.classList.remove('collapsed');
        content.style.maxHeight = content.scrollHeight + 'px';
        // After the opening transition completes, remove the fixed height constraint
        // so that expanding inner workspace cards can grow the panel freely
        setTimeout(() => { content.style.maxHeight = 'none'; }, 350);
    } else {
        // Snap from 'none' to an explicit pixel value so the CSS transition can animate
        content.style.maxHeight = content.scrollHeight + 'px';
        requestAnimationFrame(() => { content.style.maxHeight = '0'; });
        card.classList.add('collapsed');
    }
}

function toggleWorkspaceCard(labId) {
    const card = document.getElementById(`workspace-card-${labId}`);
    const body = document.getElementById(`workspace-body-${labId}`);
    if (!card || !body) return;

    if (card.classList.contains('collapsed')) {
        card.classList.remove('collapsed');
        body.style.maxHeight = body.scrollHeight + 'px';
    } else {
        body.style.maxHeight = body.scrollHeight + 'px';
        requestAnimationFrame(() => { body.style.maxHeight = '0'; });
        card.classList.add('collapsed');
    }
}

function renderWorkspaceCard(info, labId) {
    const createdAt = new Date(info.created_at).toLocaleString();
    const isEncrypted = info.encrypted_password && !info.password;
    const safeLab = escapeHtml(labId);
    const safeUrl = escapeHtml(info.workspace_url);
    const safeEmail = escapeHtml(info.email);
    const safeName = escapeHtml(info.workspace_name);

    return `
        <div class="workspace-card collapsible-card collapsed" id="workspace-card-${safeLab}">
            <div class="workspace-card-header" onclick="toggleWorkspaceCard('${safeLab}')">
                <h3>${safeName}</h3>
                <div class="workspace-card-actions">
                    ${isEncrypted ? '' : `<button onclick="event.stopPropagation(); encryptSingleWorkspace('${safeLab}')" class="student-btn student-btn-small" title="Encrypt password">Encrypt</button>`}
                    <button onclick="event.stopPropagation(); clearWorkspaceInfo('${safeLab}')" class="student-btn student-btn-danger student-btn-small" title="Remove">Clear</button>
                    <button class="collapsible-toggle" type="button" aria-label="Toggle workspace details">
                        <svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" class="chevron-icon">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7" />
                        </svg>
                    </button>
                </div>
            </div>
            <div class="workspace-card-body collapsible-content" style="max-height: 0;" id="workspace-body-${safeLab}">
                <div class="student-credential-item">
                    <label>Workspace URL:</label>
                    <div class="student-credential-item-value credential-with-copy">
                        <a href="${info.workspace_url}" target="_blank">${safeUrl}</a>
                        <button class="copy-btn" data-copy-text="${safeUrl}" title="Copy URL">
                            <svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" class="copy-icon">
                                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z" />
                            </svg>
                        </button>
                    </div>
                </div>
                <div class="student-credential-item">
                    <label>Email:</label>
                    <div class="student-credential-item-value credential-with-copy">
                        <span>${safeEmail}</span>
                        <button class="copy-btn" data-copy-text="${safeEmail}" title="Copy Email">
                            <svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" class="copy-icon">
                                <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z" />
                            </svg>
                        </button>
                    </div>
                </div>
                <div class="student-credential-item">
                    <label>Password:</label>
                    <div class="student-credential-item-value credential-with-copy ${isEncrypted ? 'encrypted-state' : ''}" id="workspace-password-display-${safeLab}">
                        ${info.password ?
                            `<span class="password-value">${escapeHtml(info.password)}</span>
                             <button class="copy-btn" data-copy-text="${escapeHtml(info.password)}" title="Copy Password">
                                 <svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" class="copy-icon">
                                     <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z" />
                                 </svg>
                             </button>` :
                            `<em class="encrypted-indicator">Encrypted - click to decrypt</em>`}
                    </div>
                    ${isEncrypted ?
                        `<button onclick="decryptAndShowPassword('${safeLab}')" class="student-btn student-btn-small decrypt-btn">Decrypt Password</button>` :
                        ''}
                </div>
                <div class="student-credential-item">
                    <label>Lab ID:</label>
                    <div class="student-credential-item-value">${safeLab}</div>
                </div>
                <div class="student-credential-item">
                    <label>Created At:</label>
                    <div class="student-credential-item-value">${createdAt}</div>
                </div>
            </div>
        </div>
    `;
}

async function decryptAndShowPassword(labId) {
    const cookieName = `workspace_info_${labId}`;
    const cookieValue = getCookie(cookieName);
    if (!cookieValue) {
        alert('No workspace information found for this lab');
        return;
    }

    try {
        const workspaceInfo = JSON.parse(decodeURIComponent(cookieValue));
        if (!workspaceInfo.encrypted_password) {
            alert('Password is not encrypted');
            return;
        }

        const studentPassword = await promptStudentPassword('Enter your student password to decrypt the workspace password:');
        const decryptedPassword = await decryptPassword(workspaceInfo.encrypted_password, workspaceInfo.email, studentPassword);

        const passwordDisplay = document.getElementById(`workspace-password-display-${labId}`);
        if (passwordDisplay) {
            passwordDisplay.className = 'student-credential-item-value credential-with-copy';
            passwordDisplay.innerHTML = `
                <span class="password-value">${escapeHtml(decryptedPassword)}</span>
                <button class="copy-btn" data-copy-text="${escapeHtml(decryptedPassword)}" title="Copy Password">
                    <svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" class="copy-icon">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z" />
                    </svg>
                </button>
            `;

            const copyBtn = passwordDisplay.querySelector('.copy-btn');
            if (copyBtn) {
                copyBtn.addEventListener('click', function() {
                    copyToClipboard(decryptedPassword, this);
                });
            }

            const decryptBtn = passwordDisplay.parentElement.querySelector('.decrypt-btn');
            if (decryptBtn) decryptBtn.remove();
        }
    } catch (error) {
        console.error('Failed to decrypt password:', error);
        if (error.message.includes('cancelled')) return;
        alert('Failed to decrypt password. Please check your password and try again.');
    }
}

function clearWorkspaceInfo(labId) {
    if (confirm('Are you sure you want to clear workspace information for this lab?')) {
        deleteCookie(`workspace_info_${labId}`);
        const card = document.getElementById(`workspace-card-${labId}`);
        if (card) card.remove();

        if (getAllWorkspaceCookies().length === 0) {
            const section = document.getElementById('workspaces-list-section');
            if (section) section.style.display = 'none';
        }
    }
}

function clearAllWorkspaceInfos() {
    if (!confirm('Are you sure you want to clear all saved workspace information?')) return;
    const workspaces = getAllWorkspaceCookies();
    workspaces.forEach(ws => deleteCookie(ws.cookieName));

    const container = document.getElementById('workspaces-list-container');
    if (container) container.innerHTML = '';
    const section = document.getElementById('workspaces-list-section');
    if (section) section.style.display = 'none';
}

function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

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
            created_at: workspaceInfo.created_at
        };

        const cookieName = `workspace_info_${workspaceInfo.lab_id}`;
        const cookieValue = encodeURIComponent(JSON.stringify(encryptedInfo));
        setCookie(cookieName, cookieValue, 1);

        alert('Workspace information encrypted and saved successfully!');
        setTimeout(loadAllWorkspaceInfos, 100);

        return true;
    } catch (error) {
        console.error('Failed to encrypt and save workspace info:', error);
        if (error.message.includes('cancelled')) return false;
        alert('Failed to encrypt workspace information: ' + error.message);
        return false;
    }
}

async function encryptAndSaveWorkspaceInfo(button) {
    const responseDiv = document.getElementById('workspace-response');
    if (!responseDiv) {
        alert('Workspace information not found');
        return;
    }

    const dataElement = responseDiv.querySelector('[data-workspace-info]');
    if (!dataElement) {
        alert('Workspace information data not found');
        return;
    }

    try {
        const workspaceInfo = JSON.parse(dataElement.getAttribute('data-workspace-info'));
        await saveWorkspaceInfoWithEncryption(workspaceInfo);
    } catch (error) {
        console.error('Failed to parse workspace info:', error);
        alert('Failed to parse workspace information');
    }
}

async function encryptSingleWorkspace(labId) {
    const cookieName = `workspace_info_${labId}`;
    const cookieValue = getCookie(cookieName);
    if (!cookieValue) {
        alert('No workspace information found for this lab');
        return;
    }

    try {
        const workspaceInfo = JSON.parse(decodeURIComponent(cookieValue));
        if (!workspaceInfo.password) {
            alert('Password is already encrypted');
            return;
        }
        await saveWorkspaceInfoWithEncryption(workspaceInfo);
    } catch (error) {
        console.error('Failed to encrypt workspace info:', error);
        alert('Failed to encrypt workspace information');
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

function loadLabs() {
    fetch('/api/student/labs')
        .then(response => response.json())
        .then(data => {
            const select = document.getElementById('lab_id');

            if (data.length === 0) {
                select.innerHTML = '<option value="">No labs available</option>';
                return;
            }

            select.innerHTML = '<option value="">Select a lab...</option>';
            data.forEach(lab => {
                const option = document.createElement('option');
                option.value = lab.id;
                option.textContent = `${lab.config.stack_name || lab.id} (${lab.status})`;
                select.appendChild(option);
            });
        })
        .catch(error => {
            console.error('Error loading labs:', error);
            const select = document.getElementById('lab_id');
            if (select) {
                select.innerHTML = '<option value="">Error loading labs</option>';
            }
        });
}

const workspaceForm = document.getElementById('workspace-request-form');

if (typeof htmx !== 'undefined') {
    workspaceForm.addEventListener('htmx:beforeRequest', function() {
        const btn = document.getElementById('submit-btn');
        btn.disabled = true;
        btn.textContent = 'Requesting...';
    });

    workspaceForm.addEventListener('htmx:afterRequest', function(event) {
        const btn = document.getElementById('submit-btn');
        btn.disabled = false;
        btn.textContent = 'Request Workspace';
        setTimeout(loadAllWorkspaceInfos, 500);
    });
} else {
    workspaceForm.addEventListener('submit', function(e) {
        e.preventDefault();

        const btn = document.getElementById('submit-btn');
        const responseDiv = document.getElementById('workspace-response');

        btn.disabled = true;
        btn.textContent = 'Requesting...';
        responseDiv.innerHTML = '<div class="student-loading">Requesting workspace...</div>';

        const formData = new FormData(workspaceForm);

        fetch('/api/student/workspace/request', {
            method: 'POST',
            body: formData
        })
        .then(response => response.text())
        .then(html => {
            responseDiv.innerHTML = html;
            btn.disabled = false;
            btn.textContent = 'Request Workspace';
            setTimeout(loadAllWorkspaceInfos, 500);
        })
        .catch(error => {
            responseDiv.innerHTML = `<div class="error-message">Error: ${error.message}</div>`;
            btn.disabled = false;
            btn.textContent = 'Request Workspace';
        });
    });
}
