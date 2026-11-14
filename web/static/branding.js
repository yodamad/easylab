// Live preview for the admin Branding page.
document.addEventListener('DOMContentLoaded', function () {
    var form = document.getElementById('branding-form');
    var preview = document.getElementById('branding-preview');
    if (!form || !preview) return;

    var DEFAULT_ACCENT = '#2563eb';
    var DEFAULT_LOGO = '/static/logo.png';
    var HEX = /^#[0-9a-fA-F]{6}$/;

    var defaultName = form.getAttribute('data-default-name') || '';
    var defaultTagline = form.getAttribute('data-default-tagline') || '';

    var nameInput = document.getElementById('branding-name');
    var taglineInput = document.getElementById('branding-tagline');
    var accentInput = document.getElementById('branding-accent');
    var fileInput = document.getElementById('branding-logo');
    var removeBox = document.getElementById('branding-logo-remove');
    var nameEl = document.getElementById('branding-preview-name');
    var taglineEl = document.getElementById('branding-preview-tagline');
    var logoEl = document.getElementById('branding-preview-logo');

    var savedLogo = logoEl ? logoEl.getAttribute('src') : '';
    var objectUrl = null;

    var swatches = form.querySelectorAll('.branding-swatch');
    var i;
    for (i = 0; i < swatches.length; i++) {
        var hex = swatches[i].getAttribute('data-accent');
        if (hex) swatches[i].style.setProperty('--branding-swatch-color', hex);
    }

    var hasSavedLogo = form.getAttribute('data-has-custom-logo') === 'true';

    function val(input) {
        return input ? input.value.trim() : '';
    }

    // Mirrors BrandingSettings.View() in internal/server/branding.go, so the mock
    // shows what the sign-in page will actually print rather than every field at
    // once. Two rules there are easy to get wrong here:
    //   - an uploaded logo with no name given is the whole lockup, because most
    //     logos already contain their wordmark;
    //   - EasyLab's own tagline is a default only while nothing is branded. Once
    //     something is, an empty tagline says nothing instead of lending our
    //     slogan to someone else's logo.
    function updateLockup() {
        var logoChosen = !!(fileInput && fileInput.files && fileInput.files[0]);
        var logoRemoved = !!(removeBox && removeBox.checked);
        var hasLogo = logoChosen || (hasSavedLogo && !logoRemoved);
        var name = val(nameInput);
        var tagline = val(taglineInput);
        var branded = hasLogo || name !== '' || tagline !== '' || val(accentInput) !== '';

        if (nameEl) {
            nameEl.textContent = name !== '' ? name : defaultName;
            nameEl.hidden = hasLogo && name === '';
        }
        if (taglineEl) {
            var shown = tagline !== '' ? tagline : (branded ? '' : defaultTagline);
            taglineEl.textContent = shown;
            taglineEl.hidden = shown === '';
        }
    }

    function updateAccent() {
        if (!accentInput) return;
        var v = accentInput.value.trim();
        if (v === '') {
            preview.style.setProperty('--branding-preview-accent', DEFAULT_ACCENT);
        } else if (HEX.test(v)) {
            preview.style.setProperty('--branding-preview-accent', v);
        }
    }

    function revokeObjectUrl() {
        if (objectUrl) {
            URL.revokeObjectURL(objectUrl);
            objectUrl = null;
        }
    }

    function updateLogo() {
        if (!logoEl) return;
        if (removeBox && removeBox.checked) {
            logoEl.src = DEFAULT_LOGO;
            return;
        }
        var file = fileInput && fileInput.files ? fileInput.files[0] : null;
        if (file) {
            revokeObjectUrl();
            objectUrl = URL.createObjectURL(file);
            logoEl.src = objectUrl;
        } else {
            revokeObjectUrl();
            logoEl.src = savedLogo;
        }
    }

    if (nameInput) {
        nameInput.addEventListener('input', updateLockup);
    }
    if (taglineInput) {
        taglineInput.addEventListener('input', updateLockup);
    }
    if (accentInput) {
        accentInput.addEventListener('input', function () {
            updateAccent();
            updateLockup();
        });
    }
    for (i = 0; i < swatches.length; i++) {
        swatches[i].addEventListener('click', function (e) {
            if (!accentInput) return;
            accentInput.value = e.currentTarget.getAttribute('data-accent') || '';
            updateAccent();
            updateLockup();
        });
    }
    // A logo arriving or leaving changes the lockup as well as the image: it is
    // what decides whether the name is printed beside it.
    function updateLogoAndLockup() {
        updateLogo();
        updateLockup();
    }
    if (fileInput) fileInput.addEventListener('change', updateLogoAndLockup);
    if (removeBox) removeBox.addEventListener('change', updateLogoAndLockup);
});
