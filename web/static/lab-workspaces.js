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
        const row = cb.closest('tr');
        return row.querySelector('.workspace-name').textContent;
    });

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
    const isChecked = selectAllCheckbox.checked;

    checkboxes.forEach(checkbox => {
        checkbox.checked = isChecked;
    });

    updateDeleteButton();
}

// Update delete button visibility based on selected checkboxes
function updateDeleteButton() {
    const checkboxes = document.querySelectorAll('.workspace-checkbox:checked');
    const deleteBtn = document.getElementById('delete-selected-btn');
    
    if (checkboxes.length > 0) {
        deleteBtn.style.display = 'inline-block';
        deleteBtn.textContent = `Delete Selected (${checkboxes.length})`;
    } else {
        deleteBtn.style.display = 'none';
    }

    // Update select all checkbox state
    const allCheckboxes = document.querySelectorAll('.workspace-checkbox');
    const selectAllCheckbox = document.getElementById('select-all-checkbox');
    if (allCheckboxes.length > 0) {
        selectAllCheckbox.checked = checkboxes.length === allCheckboxes.length;
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
