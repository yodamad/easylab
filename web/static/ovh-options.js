// Invert all checkboxes with the given name attribute
function invertCheckboxes(name) {
    document.querySelectorAll('input[type="checkbox"][name="' + name + '"]').forEach(function (cb) {
        cb.checked = !cb.checked;
    });
}

// Toggle flavor section visibility for a region
function toggleFlavors(region) {
    const body = document.getElementById('flavors-' + region);
    const toggle = document.getElementById('toggle-' + region);
    if (!body || !toggle) return;

    if (body.style.display === 'none') {
        body.style.display = 'block';
        toggle.style.transform = 'rotate(180deg)';
    } else {
        body.style.display = 'none';
        toggle.style.transform = 'rotate(0deg)';
    }
}

document.addEventListener('DOMContentLoaded', function () {
    // Auto-dismiss success/error messages after 5 seconds
    document.body.addEventListener('htmx:afterSwap', function (evt) {
        if (evt.detail.target && evt.detail.target.id === 'save-response') {
            setTimeout(function () {
                const el = document.getElementById('save-response');
                if (el) el.innerHTML = '';
            }, 5000);
        }
    });
});
