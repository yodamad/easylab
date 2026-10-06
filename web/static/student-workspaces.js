// My Workspaces page: renders the logged-in student's workspaces as reported by the
// server (GET /api/student/workspaces), each with its details, auto-deletion time, and
// per-workspace actions. The list follows the student's login, not the browser, so it
// is the same on every device. Shared helpers (copy, escaping) live in
// student-common.js, loaded first.

// The workspaces currently listed, keyed by the id their card is rendered under.
let _workspaces = {};

document.addEventListener('DOMContentLoaded', loadAllWorkspaceInfos);

// workspaceKey identifies a workspace across labs; it names the card's DOM ids.
function workspaceKey(info) {
    return `${info.lab_id}_${info.workspace_name}`;
}

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
            <p>Request a workspace to get your development environment. It shows up here so you can reconnect anytime, from any device.</p>
            <a href="/student/dashboard" class="student-btn">Request a workspace</a>
        </div>
    `;
}

// The server skips a lab whose cluster it cannot reach rather than failing the whole
// list, so say that some workspaces may be missing.
function renderIncompleteNotice() {
    return `<div class="warning-message">Some labs could not be reached, so workspaces may be missing from this list. Please try again in a moment.</div>`;
}

async function loadAllWorkspaceInfos() {
    const container = document.getElementById('workspaces-list-container');
    const clearAllBtn = document.getElementById('clear-all-btn');
    const countEl = document.getElementById('workspaces-count');
    if (!container) return;

    let data;
    try {
        const resp = await fetch('/api/student/workspaces', { credentials: 'same-origin' });
        if (!resp.ok) throw new Error('Failed to fetch workspaces');
        data = await resp.json();
    } catch (e) {
        console.error('Failed to load workspaces:', e);
        _workspaces = {};
        container.innerHTML = `<div class="error-message">Your workspaces could not be loaded. Please refresh the page.</div>`;
        if (clearAllBtn) clearAllBtn.style.display = 'none';
        if (countEl) countEl.textContent = '';
        return;
    }

    const workspaces = data.workspaces || [];
    const notice = data.incomplete ? renderIncompleteNotice() : '';
    _workspaces = {};
    workspaces.forEach(info => { _workspaces[workspaceKey(info)] = info; });

    if (workspaces.length === 0) {
        container.innerHTML = notice + renderEmptyState();
        if (clearAllBtn) clearAllBtn.style.display = 'none';
        if (countEl) countEl.textContent = '';
        return;
    }

    container.innerHTML = notice + workspaces.map(info => renderWorkspaceCard(info, workspaceKey(info))).join('');
    if (clearAllBtn) clearAllBtn.style.display = '';
    if (countEl) countEl.textContent = workspaces.length === 1 ? '1 workspace' : `${workspaces.length} workspaces`;

    setTimeout(() => attachCopyButtonListeners(), 10);
}

// Toggle a single workspace card between collapsed (name + lab/template + expiry only)
// and expanded (full details). Triggered from the card header and chevron.
function toggleWorkspaceCard(labId) {
    const card = document.getElementById(`workspace-card-${labId}`);
    if (!card) return;
    card.classList.toggle('collapsed');
    const toggle = card.querySelector('.collapsible-toggle');
    if (toggle) toggle.setAttribute('aria-expanded', String(!card.classList.contains('collapsed')));
}

function renderWorkspaceCard(info, labId) {
    const createdAt = formatWorkspaceDate(info.created_at);
    const deletionAt = formatWorkspaceDate(info.deletion_at);
    const teacherAccessedAt = formatWorkspaceDate(info.teacher_accessed_at);
    // safeLab is embedded inside single-quoted onclick="fn('...')" handlers
    // below, so it needs quote-safe escaping, not just HTML-text escaping.
    const safeLab = escapeHtmlAttr(labId);
    const safeUrl = escapeHtml(info.workspace_url || '');
    // Only render as a clickable link when the scheme is http(s) — otherwise
    // (e.g. a javascript: URL) show it as inert text instead of a link.
    const workspaceLinkHref = /^https?:\/\//i.test(info.workspace_url || '') ? escapeHtmlAttr(info.workspace_url) : null;
    const safeEmail = escapeHtml(info.email || '');
    const safeName = escapeHtml(info.workspace_name);
    const labDisplay = escapeHtml(info.lab_name || info.lab_id || '');

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

    const teacherNote = teacherAccessedAt
        ? `<div class="workspace-teacher-access-note">A teacher opened this workspace on ${escapeHtml(teacherAccessedAt)}</div>`
        : '';

    return `
        <div class="workspace-card collapsed" id="workspace-card-${safeLab}">
            <div class="workspace-card-header" onclick="toggleWorkspaceCard('${safeLab}')">
                <div class="workspace-card-heading">
                    <h3 class="workspace-card-title">${safeName}</h3>
                    <div class="workspace-card-subtitle">${labChip}${templateChip}</div>
                </div>
                <div class="workspace-card-header-actions">
                    <button onclick="event.stopPropagation(); openCodeServer('${escapeHtmlAttr(info.lab_id)}', '${escapeHtmlAttr(info.workspace_name)}')" class="student-btn student-btn-small workspace-card-open" title="Open code-server">Open Code Server</button>
                    <button class="collapsible-toggle" type="button" aria-label="Toggle workspace details" aria-expanded="false">
                        <svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" class="chevron-icon">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M19 9l-7 7-7-7" />
                        </svg>
                    </button>
                </div>
            </div>
            ${expiryPill}
            ${teacherNote}
            <div class="workspace-card-collapsible">
                <div class="workspace-card-collapsible-inner">
                    <div class="workspace-card-details">
                        <div class="student-credential-item">
                            <label>Workspace URL:</label>
                            <div class="student-credential-item-value credential-with-copy">
                                ${workspaceLinkHref
                                    ? `<a href="${workspaceLinkHref}" target="_blank" rel="noopener noreferrer">${safeUrl}</a>`
                                    : `<span>${safeUrl}</span>`}
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
                            <label>Created At:</label>
                            <div class="student-credential-item-value">${escapeHtml(createdAt)}</div>
                        </div>
                    </div>
                    <div class="workspace-card-footer-actions">
                        <button onclick="clearWorkspaceInfo('${safeLab}')" class="student-btn student-btn-danger student-btn-small" title="Delete this workspace">Clear</button>
                    </div>
                </div>
            </div>
        </div>
    `;
}

async function openCodeServer(labId, workspaceName) {
    const statusUrl = `/api/student/workspace/status?lab_id=${encodeURIComponent(labId)}&workspace_name=${encodeURIComponent(workspaceName)}`;
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

// deleteWorkspaceFromCluster asks the server to delete the workspace itself (its pod
// and storage). Resolves to true when the workspace is gone from the cluster —
// including when it had already been deleted.
async function deleteWorkspaceFromCluster(info) {
    const body = new URLSearchParams({
        lab_id: info.lab_id || '',
        workspace_name: info.workspace_name || ''
    });
    try {
        const resp = await fetch('/api/student/workspace/delete', { method: 'POST', credentials: 'same-origin', body });
        return resp.ok;
    } catch (e) {
        console.error('Failed to delete workspace:', e);
        return false;
    }
}

async function clearWorkspaceInfo(labId) {
    const info = _workspaces[labId];
    if (!info) return;

    if (!confirm('Are you sure you want to clear this workspace? It will be deleted from the lab, along with everything saved in it.')) return;

    const deleted = await deleteWorkspaceFromCluster(info);
    await loadAllWorkspaceInfos();
    if (!deleted) {
        alert('The workspace could not be deleted from the lab. Please try again.');
    }
}

async function clearAllWorkspaceInfos() {
    if (!confirm('Are you sure you want to clear all your workspaces? They will be deleted from their labs, along with everything saved in them.')) return;
    const clearAllBtn = document.getElementById('clear-all-btn');
    if (clearAllBtn) clearAllBtn.disabled = true;

    const results = await Promise.all(Object.values(_workspaces).map(deleteWorkspaceFromCluster));

    if (clearAllBtn) clearAllBtn.disabled = false;
    await loadAllWorkspaceInfos();
    const failed = results.filter(ok => !ok).length;
    if (failed > 0) {
        alert(`${failed} workspace(s) could not be deleted from their lab and are still in your list. Please try again.`);
    }
}
