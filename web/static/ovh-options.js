function switchProviderTab(tabName) {
    document.querySelectorAll('.provider-tab').forEach(function(t) {
        t.classList.toggle('active', t.dataset.tab === tabName);
    });
    document.querySelectorAll('.provider-tab-panel').forEach(function(p) {
        p.classList.toggle('active', p.id === 'tab-' + tabName);
    });
    if (history.replaceState) {
        history.replaceState(null, '', '#' + tabName);
    }
}

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
    var hidden = body.classList.contains('is-hidden') || body.style.display === 'none';
    if (hidden) {
        body.style.display = '';
        body.classList.remove('is-hidden');
        toggle.classList.add('rotate-up');
    } else {
        body.classList.add('is-hidden');
        toggle.classList.remove('rotate-up');
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

    var hidden = body.classList.contains('is-hidden') || body.style.display === 'none';
    if (hidden) {
        body.style.display = '';
        body.classList.remove('is-hidden');
        toggle.classList.add('rotate-up');
    } else {
        body.classList.add('is-hidden');
        toggle.classList.remove('rotate-up');
    }
}

document.addEventListener('DOMContentLoaded', function () {
    // Restore tab from URL hash
    var hash = window.location.hash.replace('#', '');
    if (hash === 'options' || hash === 'credentials') {
        switchProviderTab(hash);
    }

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
