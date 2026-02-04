// Load labs on page load
document.addEventListener('DOMContentLoaded', function() {
    loadLabs();
    loadWorkspaceInfo();
    // Collapsible state will be initialized when workspace info is displayed
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

// Load workspace info without decrypting (on-demand decryption)
function loadWorkspaceInfo() {
    const cookieValue = getCookie('workspace_info');
    if (!cookieValue) {
        return; // No workspace info cookie
    }

    try {
        const workspaceInfo = JSON.parse(decodeURIComponent(cookieValue));
        // Don't decrypt automatically - just display encrypted state
        displayWorkspaceInfo(workspaceInfo);
    } catch (error) {
        console.error('Failed to load workspace info:', error);
    }
}

// Display workspace info in the UI
function displayWorkspaceInfo(info) {
    const container = document.getElementById('workspace-info-container');
    const card = document.getElementById('workspace-info-card');
    if (!container || !card) {
        return; // Container doesn't exist yet
    }

    const createdAt = new Date(info.created_at).toLocaleString();
    const isEncrypted = info.encrypted_password && !info.password;
    
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
                <div class="student-credential-item-value credential-with-copy">
                    <a href="${info.workspace_url}" target="_blank">${info.workspace_url}</a>
                    <button class="copy-btn" data-copy-text="${escapeHtml(info.workspace_url)}" title="Copy URL">
                        <svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" class="copy-icon">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z" />
                        </svg>
                    </button>
                </div>
            </div>
            <div class="student-credential-item">
                <label>Email:</label>
                <div class="student-credential-item-value credential-with-copy">
                    <span>${escapeHtml(info.email)}</span>
                    <button class="copy-btn" data-copy-text="${escapeHtml(info.email)}" title="Copy Email">
                        <svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" class="copy-icon">
                            <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z" />
                        </svg>
                    </button>
                </div>
            </div>
            <div class="student-credential-item">
                <label>Password:</label>
                <div class="student-credential-item-value credential-with-copy ${isEncrypted ? 'encrypted-state' : ''}" id="workspace-password-display">
                    ${info.password ? 
                        `<span class="password-value">${escapeHtml(info.password)}</span>
                         <button class="copy-btn" data-copy-text="${escapeHtml(info.password)}" title="Copy Password">
                             <svg xmlns="http://www.w3.org/2000/svg" fill="none" viewBox="0 0 24 24" stroke="currentColor" class="copy-icon">
                                 <path stroke-linecap="round" stroke-linejoin="round" stroke-width="2" d="M8 16H6a2 2 0 01-2-2V6a2 2 0 012-2h8a2 2 0 012 2v2m-6 12h8a2 2 0 002-2v-8a2 2 0 00-2-2h-8a2 2 0 00-2 2v8a2 2 0 002 2z" />
                             </svg>
                         </button>` : 
                        `<em class="encrypted-indicator">🔒 Encrypted - click to decrypt</em>`}
                </div>
                ${isEncrypted ? 
                    '<button onclick="decryptAndShowPassword()" class="student-btn student-btn-small decrypt-btn">Decrypt Password</button>' : 
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
    
    // Initialize collapsible state and attach copy button listeners after content is rendered
    setTimeout(() => {
        initializeCollapsibleState();
        attachCopyButtonListeners();
    }, 10);
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
        
        // Update display with decrypted password and copy button
        const passwordDisplay = document.getElementById('workspace-password-display');
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
            
            // Attach event listener to copy button
            const copyBtn = passwordDisplay.querySelector('.copy-btn');
            if (copyBtn) {
                copyBtn.addEventListener('click', function() {
                    copyToClipboard(decryptedPassword, this);
                });
            }
            
            // Remove decrypt button
            const decryptBtn = passwordDisplay.parentElement.querySelector('.decrypt-btn');
            if (decryptBtn) {
                decryptBtn.remove();
            }
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
        setTimeout(loadWorkspaceInfo, 100);
        
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

// Toggle collapsible workspace info
function toggleWorkspaceInfo() {
    const card = document.getElementById('workspace-info-card');
    const content = document.getElementById('workspace-info-content');
    const toggle = card.querySelector('.collapsible-toggle');
    const chevron = card.querySelector('.chevron-icon');
    
    if (!card || !content) return;
    
    const isCollapsed = card.classList.contains('collapsed');
    
    if (isCollapsed) {
        card.classList.remove('collapsed');
        content.style.maxHeight = content.scrollHeight + 'px';
        if (chevron) chevron.style.transform = 'rotate(180deg)';
        localStorage.setItem('workspaceInfoExpanded', 'true');
    } else {
        card.classList.add('collapsed');
        content.style.maxHeight = '0';
        if (chevron) chevron.style.transform = 'rotate(0deg)';
        localStorage.setItem('workspaceInfoExpanded', 'false');
    }
}

// Initialize collapsible state from localStorage
function initializeCollapsibleState() {
    const card = document.getElementById('workspace-info-card');
    const content = document.getElementById('workspace-info-content');
    const chevron = card?.querySelector('.chevron-icon');
    
    if (!card || !content) return;
    
    // Default to collapsed
    const isExpanded = localStorage.getItem('workspaceInfoExpanded') === 'true';
    
    if (isExpanded) {
        card.classList.remove('collapsed');
        // Set max-height after a brief delay to allow content to render
        setTimeout(() => {
            content.style.maxHeight = content.scrollHeight + 'px';
        }, 10);
        if (chevron) chevron.style.transform = 'rotate(180deg)';
    } else {
        card.classList.add('collapsed');
        content.style.maxHeight = '0';
        if (chevron) chevron.style.transform = 'rotate(0deg)';
    }
}

// Copy to clipboard functionality
async function copyToClipboard(text, button) {
    try {
        await navigator.clipboard.writeText(text);
        
        // Visual feedback
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
        // Fallback for older browsers
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

// Attach copy button event listeners after content is rendered
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

            // Populate select dropdown only
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
