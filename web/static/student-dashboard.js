// Load labs on page load
document.addEventListener('DOMContentLoaded', function() {
    loadLabs();
    loadAndDecryptWorkspaceInfo();
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

// Legacy function - kept for compatibility but now uses decryption
function loadWorkspaceInfo() {
    loadAndDecryptWorkspaceInfo();
}

// Display workspace info in the UI
function displayWorkspaceInfo(info) {
    const container = document.getElementById('workspace-info-container');
    const card = document.getElementById('workspace-info-card');
    if (!container || !card) {
        return; // Container doesn't exist yet
    }

    const createdAt = new Date(info.created_at).toLocaleString();
    
    container.innerHTML = `
        <div class="student-credentials-box">
            <div class="student-credentials-box-header">
                <h3>Your Saved Workspace Information</h3>
                <button onclick="clearWorkspaceInfo()" class="student-btn student-btn-danger">
                    Clear Info
                </button>
            </div>
            <div class="student-credential-item">
                <label>Workspace URL:</label>
                <div class="student-credential-item-value">
                    <a href="${info.workspace_url}" target="_blank">${info.workspace_url}</a>
                </div>
            </div>
            <div class="student-credential-item">
                <label>Email:</label>
                <div class="student-credential-item-value">${escapeHtml(info.email)}</div>
            </div>
            <div class="student-credential-item">
                <label>Password:</label>
                <div class="student-credential-item-value" id="workspace-password-display">
                    ${info.password ? escapeHtml(info.password) : '<em>Encrypted - click to decrypt</em>'}
                </div>
                ${info.encrypted_password && !info.password ? 
                    '<button onclick="decryptAndShowPassword()" class="student-btn student-btn-small">Decrypt Password</button>' : 
                    ''}
            </div>
            <div class="student-credential-item">
                <label>Workspace Name:</label>
                <div class="student-credential-item-value">${escapeHtml(info.workspace_name)}</div>
            </div>
            <div class="student-credential-item">
                <label>Lab ID:</label>
                <div class="student-credential-item-value">${escapeHtml(info.lab_id)}</div>
            </div>
            <div class="student-credential-item">
                <label>Created At:</label>
                <div class="student-credential-item-value">${createdAt}</div>
            </div>
        </div>
    `;
    
    // Show the card
    card.style.display = 'block';
}

// Decrypt and show password
async function decryptAndShowPassword() {
    const cookieValue = getCookie('workspace_info');
    if (!cookieValue) {
        alert('No workspace information found');
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
        
        // Update display
        const passwordDisplay = document.getElementById('workspace-password-display');
        if (passwordDisplay) {
            passwordDisplay.innerHTML = escapeHtml(decryptedPassword);
        }
    } catch (error) {
        console.error('Failed to decrypt password:', error);
        if (error.message.includes('cancelled')) {
            return;
        }
        alert('Failed to decrypt password. Please check your password and try again.');
    }
}

// Clear workspace info cookie
function clearWorkspaceInfo() {
    if (confirm('Are you sure you want to clear your saved workspace information? You will need to request a new workspace to see this information again.')) {
        deleteCookie('workspace_info');
        const container = document.getElementById('workspace-info-container');
        const card = document.getElementById('workspace-info-card');
        if (container) {
            container.innerHTML = '';
        }
        if (card) {
            card.style.display = 'none';
        }
    }
}

// Simple HTML escape function
function escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
}

// Encryption/Decryption functions using Web Crypto API
// Derive encryption key from email + student password
async function deriveKey(email, studentPassword) {
    const encoder = new TextEncoder();
    // Combine email and password to create unique key per student
    const keyMaterial = encoder.encode(email + ':' + studentPassword);
    
    // Import key material
    const key = await crypto.subtle.importKey(
        'raw',
        keyMaterial,
        { name: 'PBKDF2' },
        false,
        ['deriveBits', 'deriveKey']
    );
    
    // Derive key using PBKDF2
    const derivedKey = await crypto.subtle.deriveKey(
        {
            name: 'PBKDF2',
            salt: encoder.encode('lab-as-code-salt'), // Fixed salt (email makes it unique per student)
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

// Encrypt workspace password
async function encryptPassword(plaintext, email, studentPassword) {
    try {
        const key = await deriveKey(email, studentPassword);
        const encoder = new TextEncoder();
        const data = encoder.encode(plaintext);
        
        // Generate random IV
        const iv = crypto.getRandomValues(new Uint8Array(12));
        
        // Encrypt
        const encrypted = await crypto.subtle.encrypt(
            { name: 'AES-GCM', iv: iv },
            key,
            data
        );
        
        // Combine IV and encrypted data, then base64 encode
        const combined = new Uint8Array(iv.length + encrypted.byteLength);
        combined.set(iv);
        combined.set(new Uint8Array(encrypted), iv.length);
        
        return btoa(String.fromCharCode(...combined));
    } catch (error) {
        console.error('Encryption error:', error);
        throw error;
    }
}

// Decrypt workspace password
async function decryptPassword(encryptedBase64, email, studentPassword) {
    try {
        const key = await deriveKey(email, studentPassword);
        
        // Decode base64
        const combined = Uint8Array.from(atob(encryptedBase64), c => c.charCodeAt(0));
        
        // Extract IV and encrypted data
        const iv = combined.slice(0, 12);
        const encrypted = combined.slice(12);
        
        // Decrypt
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

// Prompt for student password
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

// Save workspace info with encrypted password
async function saveWorkspaceInfoWithEncryption(workspaceInfo) {
    try {
        const studentPassword = await promptStudentPassword('Enter your student password to encrypt and save workspace information:');
        
        // Encrypt the password
        const encryptedPassword = await encryptPassword(workspaceInfo.password, workspaceInfo.email, studentPassword);
        
        // Create new workspace info with encrypted password (remove plaintext)
        const encryptedInfo = {
            email: workspaceInfo.email,
            workspace_url: workspaceInfo.workspace_url,
            encrypted_password: encryptedPassword,
            workspace_name: workspaceInfo.workspace_name,
            lab_id: workspaceInfo.lab_id,
            created_at: workspaceInfo.created_at
        };
        
        // Save to cookie
        const cookieValue = encodeURIComponent(JSON.stringify(encryptedInfo));
        setCookie('workspace_info', cookieValue, 1);
        
        alert('Workspace information encrypted and saved successfully!');
        // Reload to show encrypted version
        setTimeout(loadAndDecryptWorkspaceInfo, 100);
        
        return true;
    } catch (error) {
        console.error('Failed to encrypt and save workspace info:', error);
        if (error.message.includes('cancelled')) {
            return false;
        }
        alert('Failed to encrypt workspace information: ' + error.message);
        return false;
    }
}

// Encrypt and save workspace info from button click
async function encryptAndSaveWorkspaceInfo(button) {
    // Find the data attribute with workspace info
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

// Load and decrypt workspace info
async function loadAndDecryptWorkspaceInfo() {
    const cookieValue = getCookie('workspace_info');
    if (!cookieValue) {
        return; // No workspace info cookie
    }

    try {
        const workspaceInfo = JSON.parse(decodeURIComponent(cookieValue));
        
        // Check if password is encrypted
        if (workspaceInfo.encrypted_password && !workspaceInfo.password) {
            // Need to decrypt
            const studentPassword = await promptStudentPassword('Enter your student password to view workspace information:');
            workspaceInfo.password = await decryptPassword(workspaceInfo.encrypted_password, workspaceInfo.email, studentPassword);
        }
        
        displayWorkspaceInfo(workspaceInfo);
    } catch (error) {
        console.error('Failed to decrypt workspace info:', error);
        if (error.message.includes('cancelled')) {
            return; // User cancelled
        }
        alert('Failed to decrypt workspace information. Please check your password and try again.');
    }
}

function loadLabs() {
    fetch('/api/student/labs')
        .then(response => response.json())
        .then(data => {
            const container = document.getElementById('labs-container');
            const select = document.getElementById('lab_id');
            
            if (data.length === 0) {
                container.innerHTML = '<p>No labs available. Please contact your instructor.</p>';
                select.innerHTML = '<option value="">No labs available</option>';
                return;
            }

            // Populate labs list
            const labsList = document.createElement('ul');
            labsList.className = 'labs-list';
            data.forEach(lab => {
                const item = document.createElement('li');
                item.className = 'lab-item';
                const date = new Date(lab.created_at).toLocaleString();
                item.innerHTML = `
                    <strong>${lab.config.stack_name || lab.id}</strong>
                    <div class="lab-meta">
                        Created: ${date} | Status: ${lab.status}
                    </div>
                `;
                labsList.appendChild(item);
            });
            container.innerHTML = '';
            container.appendChild(labsList);

            // Populate select dropdown
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
            document.getElementById('labs-container').innerHTML = 
                '<p class="error-message">Error loading labs. Please refresh the page.</p>';
        });
}

// Handle form submission
const workspaceForm = document.getElementById('workspace-request-form');

// HTMX event handlers
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
    });
} else {
    // Fallback for when HTMX is not available
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
        })
        .catch(error => {
            responseDiv.innerHTML = `<div class="error-message">Error: ${error.message}</div>`;
            btn.disabled = false;
            btn.textContent = 'Request Workspace';
        });
    });
}
