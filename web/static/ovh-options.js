// Invert all checkboxes with the given name attribute
function invertCheckboxes(name) {
    document.querySelectorAll('input[type="checkbox"][name="' + name + '"]').forEach(function (cb) {
        cb.checked = !cb.checked;
    });
}

// Toggle flavor filters section visibility (OVH options page)
function toggleFlavorFiltersSection() {
    var body = document.getElementById('ovh-flavor-filters-body');
    var toggle = document.getElementById('ovh-flavor-filters-toggle');
    if (!body || !toggle) return;
    if (body.style.display === 'none') {
        body.style.display = 'block';
        toggle.style.transform = 'rotate(180deg)';
    } else {
        body.style.display = 'none';
        toggle.style.transform = 'rotate(0deg)';
    }
}

// Clear flavor filter inputs (set all to 0)
function clearFlavorFilters() {
    var ids = ['flavor_filter_min_vcpus', 'flavor_filter_max_vcpus', 'flavor_filter_min_ram', 'flavor_filter_max_ram'];
    ids.forEach(function (id) {
        var el = document.getElementById(id);
        if (el) el.value = '0';
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
    // Clear flavor filters button
    var clearBtn = document.getElementById('ovh_flavor_filter_clear_btn');
    if (clearBtn) {
        clearBtn.addEventListener('click', clearFlavorFilters);
    }

    // Auto-dismiss success/error messages after 5 seconds
    document.body.addEventListener('htmx:afterSwap', function (evt) {
        if (evt.detail.target && evt.detail.target.id === 'save-response') {
            setTimeout(function () {
                var el = document.getElementById('save-response');
                if (el) el.innerHTML = '';
            }, 5000);
        }
    });
});
