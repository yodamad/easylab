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
        const card = cb.closest('.workspace-card');
        return card ? card.querySelector('.workspace-name').textContent : '';
    }).filter(name => name);

    if (!confirm(`Are you sure you want to delete ${workspaceIds.length} workspace(s)?\n\n${workspaceNames.join('\n')}\n\nThis action cannot be undone.`)) {
        return;
    }

    const formData = new FormData();
    formData.append('workspace_ids', JSON.stringify(workspaceIds));
    formData.append('lab_id', LAB_ID);

    fetch(`/api/labs/${LAB_ID}/workspaces/bulk-delete`, {
        method: 'POST',
        body: formData
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
    const checkboxes = document.querySelectorAll('.workspace-checkbox');
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
    const allCheckboxes = document.querySelectorAll('.workspace-checkbox');
    
    if (checkboxes.length > 0) {
        deleteBtn.style.display = 'inline-flex';
        // Update tooltip text
        const tooltip = deleteBtn.querySelector('.tooltip');
        if (tooltip) {
            tooltip.textContent = `Delete Selected (${checkboxes.length})`;
        }
    } else {
        deleteBtn.style.display = 'none';
    }

    // Update select all checkbox and icon state
    if (selectAllCheckbox && allCheckboxes.length > 0) {
        const allChecked = checkboxes.length === allCheckboxes.length;
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
});
