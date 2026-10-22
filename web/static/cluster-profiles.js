// Cluster profiles page: show which kubeconfig file was picked. The file input
// itself is visually hidden behind a styled label (.file-input), so without this
// there is no feedback that a file was chosen. HTMX cannot read a File object,
// so this is the one thing the page needs script for.
document.addEventListener('DOMContentLoaded', function () {
    const input = document.getElementById('kubeconfig_file');
    const display = document.getElementById('kubeconfig-file-name-display');
    if (!input || !display) return;

    input.addEventListener('change', function (e) {
        const file = e.target.files && e.target.files[0];
        if (file) {
            display.textContent = 'Selected: ' + file.name;
            display.style.display = 'block';
        } else {
            display.style.display = 'none';
        }
    });
});
