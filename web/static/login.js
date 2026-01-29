// Hash password with SHA-256 before submission
async function hashPassword(password) {
    const encoder = new TextEncoder();
    const data = encoder.encode(password);
    const hashBuffer = await crypto.subtle.digest('SHA-256', data);
    const hashArray = Array.from(new Uint8Array(hashBuffer));
    const hashHex = hashArray.map(b => b.toString(16).padStart(2, '0')).join('');
    return hashHex;
}

// Initialize login form handler
function initLoginForm(formId) {
    const form = document.getElementById(formId);
    if (!form) return;

    form.addEventListener('submit', async function(e) {
        e.preventDefault(); // Prevent default form submission
        
        const passwordInput = document.getElementById('password');
        const passwordHashInput = document.getElementById('password_hash');
        const submitBtn = document.getElementById('submit-btn');
        
        const password = passwordInput.value;
        if (!password) {
            return; // Let HTML5 validation handle empty password
        }
        
        // Disable submit button
        submitBtn.disabled = true;
        submitBtn.textContent = 'Logging in...';
        
        try {
            // Hash password with SHA-256
            const hash = await hashPassword(password);
            
            // Set the hash in hidden field
            passwordHashInput.value = hash;
            
            // Clear the plaintext password field
            passwordInput.value = '';
            passwordInput.name = ''; // Remove name to prevent submission
            
            // Submit the form
            this.submit();
        } catch (error) {
            console.error('Error hashing password:', error);
            submitBtn.disabled = false;
            submitBtn.textContent = 'Login';
            alert('An error occurred. Please try again.');
        }
    });
}

// Initialize on page load
document.addEventListener('DOMContentLoaded', function() {
    initLoginForm('login-form');
});
