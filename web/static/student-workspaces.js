// My Workspaces page: renders the workspaces a student has saved locally (in cookies),
// each with its credentials, auto-deletion time, and per-workspace actions. Shared
// helpers (cookies, crypto, copy, escaping) live in student-common.js, loaded first.

// lab_id in a saved workspace is the internal job id (e.g. "job-1a2b…"), which is
// not meaningful to a student. We resolve it to the lab's real name from the live
// labs list so even workspaces saved before the name was recorded read correctly.
let _labNames = {};

function fetchLabNames() {
    return fetch('/api/student/labs', { credentials: 'same-origin' })
        .then(r => r.ok ? r.json() : [])
        .then(labs => {
            (labs || []).forEach(lab => {
                _labNames[lab.id] = (lab.config && lab.config.stack_name) || lab.id;
            });
        })
        .catch(() => {});
}

document.addEventListener('DOMContentLoaded', function() {
    // Render once the lab names are in (or the lookup has failed), so cards show a
    // readable lab name from the first paint rather than flashing the job id.
    fetchLabNames().finally(loadAllWorkspaceInfos);
});

// formatWorkspaceDate turns an ISO timestamp into a compact "Mon DD, YYYY · HH:MM"
// label in the viewer's locale. Returns '' when the value is missing or unparseable.
function formatWorkspaceDate(iso) {
    if (!iso) return '';
    const d = new Date(iso);
    if (isNaN(d.getTime())) return '';
    const date = d.toLocaleDateString(undefined, { month: 'short', day: '2-digit', year: 'numeric' });
    const time = d.toLocaleTimeString(undefined, { hour: '2-digit', minute: '2-digit', hour12: false });
    return `${date} · ${time}`;
}

function renderEmptyState() {
    return `
        <div class="student-empty-state">
            <svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke-width="1.5" stroke="currentColor" class="student-empty-icon" aria-hidden="true">
                <path stroke-linecap="round" stroke-linejoin="round" d="M6 6.878V6a2.25 2.25 0 0 1 2.25-2.25h7.5A2.25 2.25 0 0 1 18 6v.878m-12 0c.235-.083.487-.128.75-.128h10.5c.263 0 .515.045.75.128m-12 0A2.25 2.25 0 0 0 4.5 9v.878m13.5-3A2.25 2.25 0 0 1 19.5 9v.878m0 0a2.246 2.246 0 0 0-.75-.128H5.25c-.263 0-.515.045-.75.128m15 0A2.25 2.25 0 0 1 21 12v6a2.25 2.25 0 0 1-2.25 2.25H5.25A2.25 2.25 0 0 1 3 18v-6c0-.98.626-1.813 1.5-2.122" />
            </svg>
            <h3>No workspaces yet</h3>
            <p>Request a workspace to get your development environment. It shows up here so you can reconnect anytime.</p>
            <a href="/student/dashboard" class="student-btn">Request a workspace</a>
        </div>
    `;
}

function loadAllWorkspaceInfos() {
    const currentEmail = document.querySelector('.student-dashboard-container')?.dataset.currentEmail || '';
    const all = getAllWorkspaceCookies();
    const workspaces = currentEmail ? all.filter(ws => ws.info.email === currentEmail) : all;
    const container = document.getElementById('workspaces-list-container');
    const clearAllBtn = document.getElementById('clear-all-btn');
    const countEl = document.getElementById('workspaces-count');
    if (!container) return;

    if (workspaces.length === 0) {
        container.innerHTML = renderEmptyState();
        if (clearAllBtn) clearAllBtn.style.display = 'none';
        if (countEl) countEl.textContent = '';
        return;
    }

    container.innerHTML = workspaces.map(ws => renderWorkspaceCard(ws.info, ws.uniqueId)).join('');
    if (clearAllBtn) clearAllBtn.style.display = '';
    if (countEl) countEl.textContent = workspaces.length === 1 ? '1 workspace' : `${workspaces.length} workspaces`;

    setTimeout(() => attachCopyButtonListeners(), 10);
}

// Toggle a single workspace card between collapsed (name + lab/template + expiry only)
// and expanded (full credentials). Triggered from the card header and chevron.
function toggleWorkspaceCard(labId) {
    const card = document.getElementById(`workspace-card-${labId}`);
    if (!card) return;
    card.classList.toggle('collapsed');
    const toggle = card.querySelector('.collapsible-toggle');
    if (toggle) toggle.setAttribute('aria-expanded', String(!card.classList.contains('collapsed')));
}

function renderWorkspaceCard(info, labId) {
    const createdAt = formatWorkspaceDate(info.created_at) || new Date(info.created_at).toLocaleString();
    const deletionAt = formatWorkspaceDate(info.deletion_at);
    const isEncrypted = info.encrypted_password && !info.password;
    const safeLab = escapeHtml(labId);
    const safeUrl = escapeHtml(info.workspace_url);
    const safeEmail = escapeHtml(info.email);
    const safeName = escapeHtml(info.workspace_name);
    const labDisplay = escapeHtml(_labNames[info.lab_id] || info.lab_name || info.lab_id || '');

    const ownerID = info.email.split('@')[0].toLowerCase().replace(/\./g, '-');

    const templateChip = info.template
        ? `<span class="workspace-card-chip"><span class="workspace-card-chip-key">Template</span>${escapeHtml(info.template)}</span>`
        : '';
    const labChip = labDisplay
        ? `<span class="workspace-card-chip"><span class="workspace-card-chip-key">Lab</span>${labDisplay}</span>`
        : '';

    const expiryPill = deletionAt ? `
                <div class="workspace-expiry-pill" title="This workspace is deleted automatically">
                    <svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" class="workspace-expiry-icon" aria-hidden="true">
                        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M12 6v6h4.5m4.5 0a9 9 0 11-18 0 9 9 0 0118 0z" />
                    </svg>
                    <span class="workspace-expiry-label">Auto-deletes</span>
                    <span class="workspace-expiry-date">${escapeHtml(deletionAt)}</span>
                </div>` : '';

    return `
        <div class="workspace-card collapsed" id="workspace-card-${safeLab}">
            <div class="workspace-card-header" onclick="toggleWorkspaceCard('${safeLab}')">
                <div class="workspace-card-heading">
                    <h3 class="workspace-card-title">${safeName}</h3>
                    <div class="workspace-card-subtitle">${labChip}${templateChip}</div>
                </div>
                <div class="workspace-card-header-actions">
                    <button onclick="event.stopPropagation(); openCodeServer('${escapeHtml(info.lab_id)}', '${escapeHtml(info.workspace_name)}', '${escapeHtml(ownerID)}')" class="student-btn student-btn-small workspace-card-open" title="Open code-server">Open Code Server</button>
                    <button class="collapsible-toggle" type="button" aria-label="Toggle workspace details" aria-expanded="false">
                        <svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" class="chevron-icon">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7" />
                        </svg>
                    </button>
                </div>
            </div>
            ${expiryPill}
            <div class="workspace-card-collapsible">
                <div class="workspace-card-collapsible-inner">
                    <div class="workspace-card-details">
                        <div class="student-credential-item">
                            <label>Workspace URL:</label>
                            <div class="student-credential-item-value credential-with-copy">
                                <a href="${info.workspace_url}" target="_blank">${safeUrl}</a>
                                <button class="copy-btn" data-copy-text="${safeUrl}" title="Copy URL">${copyIconSvg}</button>
                            </div>
                        </div>
                        <div class="student-credential-item">
                            <label>Email:</label>
                            <div class="student-credential-item-value credential-with-copy">
                                <span>${safeEmail}</span>
                                <button class="copy-btn" data-copy-text="${safeEmail}" title="Copy Email">${copyIconSvg}</button>
                            </div>
                        </div>
                        <div class="student-credential-item">
                            <label>Password:</label>
                            <div class="student-credential-item-value credential-with-copy ${isEncrypted ? 'encrypted-state' : ''}" id="workspace-password-display-${safeLab}">
                                ${info.password ?
                                    `<span class="password-value">${escapeHtml(info.password)}</span>
                                     <button class="copy-btn" data-copy-text="${escapeHtml(info.password)}" title="Copy Password">${copyIconSvg}</button>` :
                                    `<em class="encrypted-indicator">Encrypted - click to decrypt</em>`}
                            </div>
                            ${isEncrypted ?
                                `<button onclick="decryptAndShowPassword('${safeLab}')" class="student-btn student-btn-small decrypt-btn">Decrypt Password</button>` :
                                ''}
                        </div>
                        <div class="student-credential-item">
                            <label>Created At:</label>
                            <div class="student-credential-item-value">${createdAt}</div>
                        </div>
                    </div>
                    <div class="workspace-card-footer-actions">
                        ${isEncrypted ? '' : `<button onclick="encryptSingleWorkspace('${safeLab}')" class="student-btn student-btn-small" title="Encrypt password">Encrypt</button>`}
                        <button onclick="clearWorkspaceInfo('${safeLab}')" class="student-btn student-btn-danger student-btn-small" title="Remove">Clear</button>
                    </div>
                </div>
            </div>
        </div>
    `;
}

async function openCodeServer(labId, workspaceName, ownerID) {
    const statusUrl = `/api/student/workspace/status?lab_id=${encodeURIComponent(labId)}&workspace_name=${encodeURIComponent(workspaceName)}&owner_id=${encodeURIComponent(ownerID)}`;
    try {
        const resp = await fetch(statusUrl, { credentials: 'same-origin' });
        if (!resp.ok) throw new Error('Failed to fetch workspace status');
        const html = await resp.text();
        const tmp = document.createElement('div');
        tmp.innerHTML = html;
        const link = tmp.querySelector('a.btn-workspace-connect');
        if (link) {
            window.open(link.href, '_blank');
        } else {
            alert('Workspace is not ready yet. Please wait for it to start and try again.');
        }
    } catch (e) {
        console.error('Failed to open code-server:', e);
        alert('Failed to check workspace status. Please try again.');
    }
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
                <button class="copy-btn" data-copy-text="${escapeHtml(decryptedPassword)}" title="Copy Password">${copyIconSvg}</button>
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
        loadAllWorkspaceInfos();
    }
}

function clearAllWorkspaceInfos() {
    if (!confirm('Are you sure you want to clear all saved workspace information?')) return;
    const workspaces = getAllWorkspaceCookies();
    workspaces.forEach(ws => deleteCookie(ws.cookieName));
    loadAllWorkspaceInfos();
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
