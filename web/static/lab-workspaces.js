// Checkboxes of the rows currently on screen. Filtering and paging hide rows
// with .is-hidden and uncheck them, so a bulk delete never reaches a workspace
// the admin cannot see.
const VISIBLE_WORKSPACE_CHECKBOXES = '.workspace-row:not(.is-hidden) .workspace-checkbox';

// Delete a single workspace
function deleteWorkspace(workspaceId, workspaceName) {
    if (!confirm(`Are you sure you want to delete workspace "${workspaceName}"? This action cannot be undone.`)) {
        return;
    }

    const formData = new FormData();
    formData.append('workspace_id', workspaceId);
    formData.append('lab_id', LAB_ID);

    fetch(`/api/labs/${LAB_ID}/workspaces/${workspaceId}/delete`, {
        method: 'POST',
        body: formData
    })
    .then(response => {
        if (response.ok) {
            return response.json();
        } else {
            return response.json().then(err => {
                throw new Error(err.message || 'Failed to delete workspace');
            });
        }
    })
    .then(data => {
        showMessage('success', data.message || 'Workspace deleted successfully');
        // Refresh the page after a short delay
        setTimeout(() => {
            window.location.reload();
        }, 1000);
    })
    .catch(error => {
        console.error('Delete error:', error);
        showMessage('error', 'Failed to delete workspace: ' + error.message);
    });
}

// Delete selected workspaces (bulk delete)
function deleteSelected() {
    const checkboxes = document.querySelectorAll('.workspace-checkbox:checked');
    if (checkboxes.length === 0) {
        alert('Please select at least one workspace to delete.');
        return;
    }

    const workspaceIds = Array.from(checkboxes).map(cb => cb.value);
    const workspaceNames = Array.from(checkboxes).map(cb => {
        const row = cb.closest('.workspace-row');
        return row ? row.querySelector('.workspace-name').textContent : '';
    }).filter(name => name);

    if (!confirm(`Are you sure you want to delete ${workspaceIds.length} workspace(s)?\n\n${workspaceNames.join('\n')}\n\nThis action cannot be undone.`)) {
        return;
    }

    const params = new URLSearchParams();
    params.append('workspace_ids', JSON.stringify(workspaceIds));
    params.append('lab_id', LAB_ID);

    fetch(`/api/labs/${LAB_ID}/workspaces/bulk-delete`, {
        method: 'POST',
        body: params
    })
    .then(response => {
        if (response.ok || response.status === 207) { // 207 is Partial Content
            return response.json();
        } else {
            return response.json().then(err => {
                throw new Error(err.message || 'Failed to delete workspaces');
            });
        }
    })
    .then(data => {
        if (data.success) {
            showMessage('success', data.message || `Successfully deleted ${workspaceIds.length} workspace(s)`);
        } else {
            showMessage('error', 'Some workspaces could not be deleted: ' + (data.errors || []).join(', '));
        }
        // Refresh the page after a short delay
        setTimeout(() => {
            window.location.reload();
        }, 2000);
    })
    .catch(error => {
        console.error('Bulk delete error:', error);
        showMessage('error', 'Failed to delete workspaces: ' + error.message);
    });
}

// Toggle select all checkboxes
function toggleSelectAll() {
    const selectAllCheckbox = document.getElementById('select-all-checkbox');
    const checkboxes = document.querySelectorAll(VISIBLE_WORKSPACE_CHECKBOXES);
    const selectAllIcon = document.getElementById('select-all-icon');
    
    if (checkboxes.length === 0) {
        return;
    }
    
    // Determine if we should select all or deselect all
    const checkedCount = Array.from(checkboxes).filter(cb => cb.checked).length;
    const isChecked = checkedCount < checkboxes.length;
    
    checkboxes.forEach(checkbox => {
        checkbox.checked = isChecked;
    });
    
    if (selectAllCheckbox) {
        selectAllCheckbox.checked = isChecked;
    }
    
    // Update icon to show checked/unchecked state
    if (selectAllIcon) {
        if (isChecked) {
            selectAllIcon.innerHTML = '<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z" />';
        } else {
            selectAllIcon.innerHTML = '<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />';
        }
    }

    updateDeleteButton();
}

// Update delete button visibility based on selected checkboxes
function updateDeleteButton() {
    const checkboxes = document.querySelectorAll('.workspace-checkbox:checked');
    const deleteBtn = document.getElementById('delete-selected-btn');
    const selectAllCheckbox = document.getElementById('select-all-checkbox');
    const selectAllIcon = document.getElementById('select-all-icon');
    const allCheckboxes = document.querySelectorAll(VISIBLE_WORKSPACE_CHECKBOXES);

    if (!deleteBtn) {
        return;
    }

    if (checkboxes.length > 0) {
        deleteBtn.classList.remove('is-hidden');
        const tooltip = deleteBtn.querySelector('.tooltip');
        if (tooltip) {
            tooltip.textContent = `Delete Selected (${checkboxes.length})`;
        }
    } else {
        deleteBtn.classList.add('is-hidden');
    }

    // Update select all checkbox and icon state
    if (selectAllCheckbox) {
        const allChecked = allCheckboxes.length > 0 && checkboxes.length === allCheckboxes.length;
        selectAllCheckbox.checked = allChecked;
        
        if (selectAllIcon) {
            if (allChecked) {
                selectAllIcon.innerHTML = '<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M9 12l2 2 4-4m6 2a9 9 0 11-18 0 9 9 0 0118 0z" />';
            } else {
                selectAllIcon.innerHTML = '<path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M6 18L18 6M6 6l12 12" />';
            }
        }
    }
}

// Refresh workspaces list
function refreshWorkspaces() {
    window.location.reload();
}

// Switch between the Active Workspaces and History tabs. Both panels are
// already rendered server-side, so this only toggles visibility — no data to fetch.
function switchWorkspacesTab(name) {
    ['active', 'history'].forEach(key => {
        const tab = document.getElementById(`tab-${key}`);
        const panel = document.getElementById(`panel-${key}`);
        if (!tab || !panel) {
            return;
        }
        const isSelected = key === name;
        tab.classList.toggle('is-active', isSelected);
        tab.setAttribute('aria-selected', isSelected ? 'true' : 'false');
        panel.classList.toggle('is-hidden', !isSelected);
    });
}

// Filtering and pagination for the Active Workspaces and History lists. Both
// are fully rendered server-side, so this only shows and hides rows: a filter
// control is matched against the row's data-* attribute of the same name
// (data-ws-filter="status" against data-status), "search" as a substring and
// the others exactly.
const DEFAULT_WORKSPACE_PAGE_SIZE = 25;
const workspaceLists = {};

function initWorkspaceList(key, rowSelector) {
    const panel = document.getElementById(`panel-${key}`);
    if (!panel) {
        return;
    }
    const rows = Array.from(panel.querySelectorAll(rowSelector));
    if (rows.length === 0) {
        return;
    }

    const list = { panel, rows, page: 1 };
    workspaceLists[key] = list;

    const resetAndRender = () => {
        list.page = 1;
        renderWorkspaceList(key);
    };
    panel.querySelectorAll('[data-ws-filter], [data-ws-page-size]').forEach(control => {
        control.addEventListener(control.tagName === 'INPUT' ? 'input' : 'change', resetAndRender);
    });
    panel.querySelectorAll('[data-ws-page]').forEach(button => {
        button.addEventListener('click', () => {
            list.page += button.dataset.wsPage === 'next' ? 1 : -1;
            renderWorkspaceList(key);
        });
    });

    renderWorkspaceList(key);
}

function renderWorkspaceList(key) {
    const list = workspaceLists[key];
    if (!list) {
        return;
    }
    const panel = list.panel;

    const filters = Array.from(panel.querySelectorAll('[data-ws-filter]'))
        .map(control => ({ name: control.dataset.wsFilter, value: control.value.trim().toLowerCase() }))
        .filter(filter => filter.value);
    const matching = list.rows.filter(row => filters.every(filter => {
        const value = (row.dataset[filter.name] || '').toLowerCase();
        return filter.name === 'search' ? value.includes(filter.value) : value === filter.value;
    }));

    const sizeControl = panel.querySelector('[data-ws-page-size]');
    const size = (sizeControl && parseInt(sizeControl.value, 10)) || DEFAULT_WORKSPACE_PAGE_SIZE;
    const totalPages = Math.max(1, Math.ceil(matching.length / size));
    list.page = Math.min(Math.max(1, list.page), totalPages);
    const start = (list.page - 1) * size;
    const visible = new Set(matching.slice(start, start + size));

    list.rows.forEach(row => {
        const isVisible = visible.has(row);
        row.classList.toggle('is-hidden', !isVisible);
        if (!isVisible) {
            const checkbox = row.querySelector('.workspace-checkbox');
            if (checkbox) {
                checkbox.checked = false;
            }
        }
    });

    const results = panel.querySelector('[data-ws-results]');
    if (results) {
        results.classList.toggle('is-hidden', matching.length === 0);
    }
    const noMatch = panel.querySelector('[data-ws-no-match]');
    if (noMatch) {
        noMatch.classList.toggle('is-hidden', matching.length > 0);
    }
    const summary = panel.querySelector('[data-ws-summary]');
    if (summary) {
        const filtered = matching.length !== list.rows.length ? ` (filtered from ${list.rows.length})` : '';
        summary.textContent = matching.length === 0
            ? `0 of ${list.rows.length}`
            : `${start + 1}–${start + visible.size} of ${matching.length}${filtered}`;
    }
    const pageInfo = panel.querySelector('[data-ws-page-info]');
    if (pageInfo) {
        pageInfo.textContent = `Page ${list.page} of ${totalPages}`;
    }
    panel.querySelectorAll('[data-ws-page]').forEach(button => {
        button.disabled = button.dataset.wsPage === 'next' ? list.page >= totalPages : list.page <= 1;
    });

    if (key === 'active') {
        updateDeleteButton();
    }
}

// Quote a CSV field only when it needs it (contains a comma, quote, or newline),
// doubling any embedded quotes per RFC 4180.
function csvField(value) {
    const str = String(value == null ? '' : value);
    if (/[",\n]/.test(str)) {
        return '"' + str.replace(/"/g, '""') + '"';
    }
    return str;
}

// Export the History tab's rows as a CSV file. The data is already rendered
// server-side (via data-* attributes on each row), so this reads the DOM rather
// than making another request.
function exportWorkspaceHistoryCSV() {
    const rows = document.querySelectorAll('#panel-history .workspace-history-row');
    if (rows.length === 0) {
        return;
    }

    const lines = [['Action', 'Name', 'Owner', 'Template', 'Time'].map(csvField).join(',')];
    rows.forEach(row => {
        lines.push([
            row.dataset.action,
            row.dataset.name,
            row.dataset.owner,
            row.dataset.template,
            row.dataset.at,
        ].map(csvField).join(','));
    });

    // A leading BOM makes Excel detect UTF-8 instead of guessing a local codepage.
    const blob = new Blob(['﻿' + lines.join('\r\n')], { type: 'text/csv;charset=utf-8' });
    const url = URL.createObjectURL(blob);

    const slug = (typeof LAB_STACK_NAME !== 'undefined' && LAB_STACK_NAME ? LAB_STACK_NAME : LAB_ID)
        .toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '') || 'lab';

    const link = document.createElement('a');
    link.href = url;
    link.download = `workspace-history-${slug}.csv`;
    document.body.appendChild(link);
    link.click();
    link.remove();
    URL.revokeObjectURL(url);
}

// Show message to user
function showMessage(type, message) {
    const container = document.getElementById('message-container');
    if (!container) {
        return;
    }

    const messageDiv = document.createElement('div');
    messageDiv.className = `message message-${type}`;
    messageDiv.textContent = message;

    container.innerHTML = '';
    container.appendChild(messageDiv);

    // Auto-hide after 5 seconds
    setTimeout(() => {
        messageDiv.remove();
    }, 5000);
}

// Initialize on page load
document.addEventListener('DOMContentLoaded', function() {
    // Add change listeners to all checkboxes
    const checkboxes = document.querySelectorAll('.workspace-checkbox');
    checkboxes.forEach(checkbox => {
        checkbox.addEventListener('change', updateDeleteButton);
    });

    initWorkspaceList('active', '.workspace-row');
    initWorkspaceList('history', '.workspace-history-row');
});

// A git credential has no server field: basic auth is sent to whatever host the
// template's git_repo names, so asking for one would imply a scoping that does
// not exist.
function toggleSecretServerField() {
    const kind = document.getElementById('secret-kind');
    const row = document.getElementById('secret-server-row');
    const server = document.getElementById('secret-server');
    if (!kind || !row || !server) {
        return;
    }
    const isRegistry = kind.value === 'registry';
    row.classList.toggle('is-hidden', !isRegistry);
    // Only the registry form requires a server; leaving it required while hidden
    // would block submission with an error the admin cannot see.
    server.required = isRegistry;
    if (!isRegistry) {
        server.value = '';
    }
}

document.addEventListener('DOMContentLoaded', toggleSecretServerField);
