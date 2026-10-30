// Behaviour for the shared workspace template editor (web/partials/template-editor.html).
//
// The create-lab wizard's Step 6 and the "Add Template" drawer on a lab's detail
// page render the same markup and post to the same parser, so they run the same
// code here. Everything that genuinely differs between the two is passed to
// init() as a hook:
//
//   gitCredentialNames / registryCredentialNames
//       Where the credential pickers get their options. The wizard reads the
//       credential rows the admin is filling in; the drawer reads the lab's real
//       credentials from the cluster.
//
//   gitCredentialAuth
//       How the devcontainer import authenticates its clone. The wizard has the
//       token in the page and sends it. On an existing lab the token lives in a
//       cluster Secret and must not reach the browser, so the drawer sends
//       nothing here and lets the server resolve the name instead (see
//       decorateImport).
//
//   decorateImport
//       A last chance to add fields to the import request — the drawer adds
//       lab_id, which is what lets the server do that resolution.
//
// Surface-specific concerns stay out: the wizard's multi-row add/remove/reindex
// and step gating live in admin.js, the drawer's open/close/submit in
// lab-detail.js.
var TemplateEditor = (function () {
    'use strict';

    var noNames = function () { return []; };
    var config = {
        multi: false,
        gitCredentialNames: noNames,
        registryCredentialNames: noNames,
        gitCredentialAuth: function () { return { username: '', token: '' }; },
        decorateImport: function () { },
    };

    var devcontainerSource = 'git';
    var devcontainerConfigSource = 'same';

    function el(id) { return document.getElementById(id); }

    // Values rendered here come out of a workshop's own devcontainer.json (image
    // names, feature refs) or a credential name someone else may control, so
    // nothing interpolated below is trusted markup.
    function escapeHtml(text) {
        var div = document.createElement('div');
        div.textContent = text == null ? '' : text;
        return div.innerHTML;
    }

    // ---- Credential pickers -------------------------------------------------

    // Rebuild one group of pickers, keeping an explicit choice that still exists.
    // With exactly one credential the empty option is relabelled to match the
    // auto-resolve below, so the common case reads as "already handled".
    function fillSelects(selector, names) {
        document.querySelectorAll(selector).forEach(function (select) {
            var current = select.value;
            select.innerHTML = '';
            var auto = document.createElement('option');
            auto.value = '';
            auto.textContent = names.length === 1 ? 'Auto — use ' + names[0] : 'None';
            select.appendChild(auto);
            names.forEach(function (name) {
                var opt = document.createElement('option');
                opt.value = name;
                opt.textContent = name;
                select.appendChild(opt);
            });
            if (current && names.indexOf(current) !== -1) select.value = current;
        });
    }

    function refreshCredentialOptions() {
        fillSelects('.template-git-cred-select', config.gitCredentialNames() || []);
        fillSelects('.template-registry-cred-select', config.registryCredentialNames() || []);
    }

    // An empty picker means "auto": with exactly one credential of the right kind,
    // resolve it here so the common case needs no choice.
    function resolveCredential(id, names) {
        var chosen = ((el(id) || {}).value || '').trim();
        if (!chosen && names.length === 1) return names[0];
        return chosen;
    }

    // ---- Repeating rows (env vars, node selectors, sidecars, mounts) --------

    function makeTextInput(name, placeholder, value) {
        var input = document.createElement('input');
        input.type = 'text';
        input.name = name;
        input.placeholder = placeholder;
        input.value = value || '';
        return input;
    }

    function makeRemoveButton(div, title) {
        var btn = document.createElement('button');
        btn.type = 'button';
        btn.className = 'btn btn-secondary btn-remove-variable';
        btn.textContent = 'x';
        btn.title = title;
        btn.addEventListener('click', function () { div.remove(); });
        return btn;
    }

    // A hidden input carries the value so an unchecked box still submits, keeping
    // the sidecar arrays index-aligned on the server.
    function makePrivilegedToggle(name) {
        var wrap = document.createElement('label');
        wrap.className = 'sidecar-privileged';
        var hidden = document.createElement('input');
        hidden.type = 'hidden';
        hidden.name = name;
        hidden.value = 'false';
        var cb = document.createElement('input');
        cb.type = 'checkbox';
        cb.addEventListener('change', function () { hidden.value = cb.checked ? 'true' : 'false'; });
        var span = document.createElement('span');
        span.textContent = 'privileged';
        wrap.appendChild(hidden);
        wrap.appendChild(cb);
        wrap.appendChild(span);
        return wrap;
    }

    function createVariableRow(idx, varName, varValue, description, required) {
        var div = document.createElement('div');
        div.className = 'template-variable-row';
        var nameInput = makeTextInput('template_' + idx + '_env_name', 'Env variable name', varName);
        if (required) nameInput.setAttribute('data-required', 'true');
        div.appendChild(nameInput);
        div.appendChild(makeTextInput('template_' + idx + '_env_value', description || 'Value', varValue));
        div.appendChild(makeRemoveButton(div, 'Remove variable'));
        return div;
    }

    function createNodeSelectorRow(idx, key, value) {
        var div = document.createElement('div');
        div.className = 'template-variable-row';
        div.appendChild(makeTextInput('template_' + idx + '_nodeselector_key', 'Label key (e.g. pool)', key));
        div.appendChild(makeTextInput('template_' + idx + '_nodeselector_value', 'Label value (e.g. workspaces)', value));
        div.appendChild(makeRemoveButton(div, 'Remove node selector'));
        return div;
    }

    function createSidecarRow(idx) {
        var div = document.createElement('div');
        div.className = 'template-variable-row';
        div.appendChild(makeTextInput('template_' + idx + '_sidecar_name', 'name'));
        div.appendChild(makeTextInput('template_' + idx + '_sidecar_image', 'image (e.g. postgres:16)'));
        div.appendChild(makeTextInput('template_' + idx + '_sidecar_ports', 'ports (5432,6379)'));
        div.appendChild(makeTextInput('template_' + idx + '_sidecar_env', 'env (KEY=VAL,KEY2=VAL2)'));
        div.appendChild(makeTextInput('template_' + idx + '_sidecar_capabilities', 'capabilities (SYS_ADMIN,…)'));
        div.appendChild(makePrivilegedToggle('template_' + idx + '_sidecar_privileged'));
        div.appendChild(makeRemoveButton(div, 'Remove sidecar'));
        return div;
    }

    function createMountRow(idx) {
        var div = document.createElement('div');
        div.className = 'template-variable-row';
        var typeSelect = document.createElement('select');
        typeSelect.name = 'template_' + idx + '_mount_type';
        ['configmap', 'secret'].forEach(function (t) {
            var opt = document.createElement('option');
            opt.value = t;
            opt.textContent = t;
            typeSelect.appendChild(opt);
        });
        div.appendChild(typeSelect);
        div.appendChild(makeTextInput('template_' + idx + '_mount_name', 'ConfigMap/Secret name'));
        div.appendChild(makeTextInput('template_' + idx + '_mount_path', 'mount path (/etc/config)'));
        div.appendChild(makeRemoveButton(div, 'Remove mount'));
        return div;
    }

    // wireRowButtons binds a template row's four "+ Add …" buttons. Listeners are
    // rebound by cloning the button first, so calling this again on a row that has
    // already been wired (the wizard does, after every reindex) does not stack
    // duplicate handlers.
    function wireRowButtons(row) {
        [
            ['.btn-add-variable', '.template-variables-container', createVariableRow],
            ['.btn-add-nodeselector', '.template-nodeselector-container', createNodeSelectorRow],
            ['.btn-add-sidecar', '.template-sidecars-container', createSidecarRow],
            ['.btn-add-mount', '.template-mounts-container', createMountRow],
        ].forEach(function (spec) {
            var btn = row.querySelector(spec[0]);
            if (!btn) return;
            btn.replaceWith(btn.cloneNode(true));
            row.querySelector(spec[0]).addEventListener('click', function () {
                var idx = parseInt(row.getAttribute('data-template-index'), 10) || 0;
                var container = row.querySelector(spec[1]);
                if (container) container.appendChild(spec[2](idx, '', ''));
                renderSummary(row);
            });
        });
        wireRowState(row);
    }

    // The row-level listeners, bound once per row element. wireRowButtons is called
    // again after every reindex, and unlike the "+ Add …" buttons these are not
    // re-created by that, so a flag is what keeps them from stacking.
    function wireRowState(row) {
        if (!row.hasAttribute('data-ws-bound')) {
            row.setAttribute('data-ws-bound', 'true');
            row.addEventListener('input', function () {
                forgetPreset(row);
                renderSummary(row);
            });
            row.addEventListener('change', function (e) {
                forgetPreset(row);
                var field = e.target && e.target.getAttribute && e.target.getAttribute('data-field');
                if (field === 'ide') setTemplateIDE(row, rowIDE(row));
                else renderSummary(row);
            });
        }
        setTemplateIDE(row, rowIDE(row));
    }

    // ---- "What students will get" -------------------------------------------

    // The defaults the kube backend applies when a field is left blank
    // (internal/providers/workspace/kube). They are repeated here so the summary
    // can state them rather than leaving the admin to guess what empty means.
    var DEFAULT_IMAGE = 'codercom/code-server:latest';
    var DEFAULT_CPU = '500m';
    var DEFAULT_MEMORY = '1Gi';
    var DEFAULT_DISK = '5Gi';

    function fieldValue(row, field) {
        var node = row.querySelector('[data-field="' + field + '"]');
        return node && node.value ? node.value.trim() : '';
    }

    function fieldChecked(row, field) {
        var node = row.querySelector('input[type="checkbox"][data-field="' + field + '"]');
        return !!(node && node.checked);
    }

    function rowIDE(row) {
        var checked = row.querySelector('input[type="radio"][data-field="ide"]:checked');
        return checked ? checked.value : '';
    }

    // setTemplateIDE applies everything that follows from the editor choice.
    //
    // The conflicting fields are disabled rather than just hidden, because
    // disabled fields are not submitted: validateIDEFeatures rejects extensions,
    // a subfolder or a home_dir that does not belong to the chosen IDE outright,
    // so a value left behind by a change of mind would fail the whole lab.
    function setTemplateIDE(row, ide) {
        var openchamber = ide === 'openchamber';
        row.classList.toggle('is-openchamber', openchamber);
        ['git_folder', 'extensions'].forEach(function (field) {
            var node = row.querySelector('[data-field="' + field + '"]');
            if (!node) return;
            if (openchamber) node.value = '';
            node.disabled = openchamber;
        });
        var home = row.querySelector('[data-field="home_dir"]');
        if (home) {
            if (!openchamber) home.value = '';
            home.disabled = !openchamber;
        }
        renderSummary(row);
    }

    // renderSummary restates the row as the sentence a student would recognise.
    // Every field in the form is optional, so without this a beginner who touches
    // nothing has no way of telling that the lab already hands out a working IDE.
    function renderSummary(row) {
        var list = row.querySelector('.ws-summary-lines');
        if (!list) return;

        var openchamber = rowIDE(row) === 'openchamber';
        var image = fieldValue(row, 'image');
        var lines = [];

        lines.push(openchamber
            ? 'An AI coding agent (OpenChamber)'
            : 'VS Code in the browser (code-server)');

        if (image) lines.push('Runs ' + image);
        else if (openchamber) lines.push('No image set yet — an agent workspace needs one');
        else lines.push('Runs ' + DEFAULT_IMAGE + ' (the default)');

        var repo = fieldValue(row, 'git_repo');
        if (repo) {
            var clone = 'Clones ' + repo;
            var branch = fieldValue(row, 'git_branch');
            if (branch) clone += ' (' + branch + ')';
            var folder = fieldValue(row, 'git_folder');
            if (folder) clone += ', opening ' + folder + '/';
            lines.push(clone);
        } else {
            lines.push('Nothing cloned — an empty workspace');
        }

        lines.push((fieldValue(row, 'cpu') || DEFAULT_CPU) + ' CPU, ' +
            (fieldValue(row, 'memory') || DEFAULT_MEMORY) + ' memory');

        lines.push(fieldChecked(row, 'ephemeral')
            ? 'Ephemeral — the student’s work is lost whenever the pod moves'
            : (fieldValue(row, 'disk_size') || DEFAULT_DISK) + ' volume — the student’s work is kept');

        var extras = [];
        [
            ['.template-sidecars-container .template-variable-row', 'sidecar', 'sidecars'],
            ['.template-mounts-container .template-variable-row', 'mount', 'mounts'],
            ['.template-variables-container .template-variable-row', 'environment variable', 'environment variables'],
        ].forEach(function (spec) {
            var n = row.querySelectorAll(spec[0]).length;
            if (n) extras.push(n + ' ' + (n === 1 ? spec[1] : spec[2]));
        });
        var extensions = fieldValue(row, 'extensions')
            .split(',')
            .map(function (s) { return s.trim(); })
            .filter(function (s) { return s !== ''; });
        if (extensions.length) {
            extras.push(extensions.length + (extensions.length === 1 ? ' extension' : ' extensions'));
        }
        if (fieldValue(row, 'startup_script')) extras.push('a startup script');
        if (extras.length) lines.push('Plus ' + extras.join(', '));

        list.innerHTML = '';
        lines.forEach(function (text) {
            var li = document.createElement('li');
            li.className = 'ws-summary-line';
            li.textContent = text;
            list.appendChild(li);
        });
    }

    // ---- Starting points ----------------------------------------------------

    function setByName(scope, name, value) {
        var node = scope.querySelector('[name="' + name + '"]');
        if (node) node.value = value == null ? '' : String(value);
    }

    // Env maps reach the sidecar form as one "KEY=VAL,KEY2=VAL2" string, which is
    // the shape parseSidecarsFromForm expects.
    function envPairs(env) {
        return Object.keys(env || {}).map(function (k) { return k + '=' + env[k]; }).join(',');
    }

    // clearRow resets a row to its defaults, so a preset describes the whole
    // template rather than merging into whatever was there before.
    function clearRow(row) {
        row.querySelectorAll('[data-field]').forEach(function (node) {
            if (node.type === 'checkbox') node.checked = false;
            else if (node.type === 'radio') node.checked = node.value === '';
            else node.value = '';
        });
        ['.template-variables-container', '.template-sidecars-container',
            '.template-mounts-container', '.template-nodeselector-container'].forEach(function (sel) {
            var container = row.querySelector(sel);
            if (container) container.innerHTML = '';
        });
    }

    // The row remembers which starting point put its values there, so switching
    // between two of them is one click rather than a confirmation each time. The
    // first edit the admin makes drops the claim, and from then on a preset would
    // be overwriting their own work.
    function forgetPreset(row) {
        if (!row.hasAttribute('data-ws-preset')) return;
        row.removeAttribute('data-ws-preset');
        document.querySelectorAll('#templates-presets .ws-preset').forEach(function (card) {
            card.classList.remove('selected');
        });
    }

    // A row nobody has filled in yet can be replaced without asking.
    function rowIsPristine(row) {
        var filled = false;
        row.querySelectorAll('[data-field]').forEach(function (node) {
            if (filled) return;
            if (node.type === 'checkbox') filled = node.checked;
            else if (node.type === 'radio') filled = node.checked && node.value !== '';
            else if (node.getAttribute('data-field') !== 'name') filled = node.value.trim() !== '';
        });
        return !filled && row.querySelectorAll('.template-variable-row').length === 0;
    }

    function applyPreset(row, preset) {
        if (!preset) return;
        var idx = parseInt(row.getAttribute('data-template-index'), 10) || 0;
        clearRow(row);

        var ide = preset.ide || '';
        var ideRadio = row.querySelector('input[type="radio"][data-field="ide"][value="' + ide + '"]');
        if (ideRadio) ideRadio.checked = true;
        // Before the rest: it decides which of the fields below are submittable.
        setTemplateIDE(row, ide);

        ['name', 'description', 'image', 'home_dir', 'git_repo', 'git_branch', 'git_folder',
            'cpu', 'cpu_limit', 'memory', 'memory_limit', 'disk_size', 'storage_class',
            'startup_script', 'dotfiles_repo'].forEach(function (field) {
            var node = row.querySelector('[data-field="' + field + '"]');
            if (node && preset[field] != null) node.value = preset[field];
        });

        var extensions = row.querySelector('[data-field="extensions"]');
        if (extensions && preset.extensions) extensions.value = preset.extensions.join(', ');

        [['git_shallow', 'git_shallow'], ['ephemeral', 'ephemeral']].forEach(function (pair) {
            var node = row.querySelector('input[type="checkbox"][data-field="' + pair[0] + '"]');
            if (node) node.checked = !!preset[pair[1]];
        });

        var envContainer = row.querySelector('.template-variables-container');
        if (envContainer && preset.env) {
            Object.keys(preset.env).forEach(function (key) {
                envContainer.appendChild(createVariableRow(idx, key, preset.env[key]));
            });
        }

        var sidecarContainer = row.querySelector('.template-sidecars-container');
        if (sidecarContainer && preset.sidecars) {
            preset.sidecars.forEach(function (sidecar) {
                var div = createSidecarRow(idx);
                setByName(div, 'template_' + idx + '_sidecar_name', sidecar.name);
                setByName(div, 'template_' + idx + '_sidecar_image', sidecar.image);
                setByName(div, 'template_' + idx + '_sidecar_ports', (sidecar.ports || []).join(','));
                setByName(div, 'template_' + idx + '_sidecar_env', envPairs(sidecar.env));
                setByName(div, 'template_' + idx + '_sidecar_capabilities', (sidecar.capabilities || []).join(','));
                if (sidecar.privileged) {
                    // The checkbox is cosmetic; the hidden input beside it is what posts.
                    var hidden = div.querySelector('input[type="hidden"][name$="_sidecar_privileged"]');
                    var box = div.querySelector('.sidecar-privileged input[type="checkbox"]');
                    if (hidden) hidden.value = 'true';
                    if (box) box.checked = true;
                }
                sidecarContainer.appendChild(div);
            });
        }

        var mountContainer = row.querySelector('.template-mounts-container');
        if (mountContainer && preset.mounts) {
            preset.mounts.forEach(function (mount) {
                var div = createMountRow(idx);
                setByName(div, 'template_' + idx + '_mount_type', mount.type);
                setByName(div, 'template_' + idx + '_mount_name', mount.name);
                setByName(div, 'template_' + idx + '_mount_path', mount.path);
                mountContainer.appendChild(div);
            });
        }

        var nodeSelectorContainer = row.querySelector('.template-nodeselector-container');
        if (nodeSelectorContainer && preset.node_selector) {
            Object.keys(preset.node_selector).forEach(function (key) {
                nodeSelectorContainer.appendChild(
                    createNodeSelectorRow(idx, key, preset.node_selector[key]));
            });
        }

        // Open the groups the preset wrote into, so what it did is visible rather
        // than hidden behind a collapsed disclosure the admin never opens.
        row.querySelectorAll('details.ws-group').forEach(function (group) {
            var touched = false;
            group.querySelectorAll('[data-field]').forEach(function (node) {
                if (node.type === 'checkbox') touched = touched || node.checked;
                else touched = touched || node.value.trim() !== '';
            });
            if (group.querySelectorAll('.template-variable-row').length) touched = true;
            if (touched) group.open = true;
        });

        renderSummary(row);
    }

    // The preset cards fill the first template row — the only one a beginner has,
    // and the one the drawer always has.
    function wirePresets() {
        var cards = document.querySelectorAll('#templates-presets .ws-preset');
        if (!cards.length) return;
        cards.forEach(function (card) {
            card.addEventListener('click', function () {
                var row = document.querySelector('#workspace-templates-container .template-row');
                if (!row) return;
                var title = (card.querySelector('.ws-preset-title') || {}).textContent || 'this preset';
                if (!row.hasAttribute('data-ws-preset') && !rowIsPristine(row) &&
                    !confirm('Replace what you have filled in with the "' + title.trim() + '" starting point?')) {
                    return;
                }
                var preset;
                try {
                    preset = JSON.parse(card.getAttribute('data-preset') || '{}');
                } catch (err) {
                    return;
                }
                applyPreset(row, preset);
                row.setAttribute('data-ws-preset', card.getAttribute('data-preset-id') || '');
                cards.forEach(function (other) { other.classList.toggle('selected', other === card); });
            });
        });
    }

    // ---- Mode switching -----------------------------------------------------

    // Collects the form builder's fields, to seed the YAML editor. Must run before
    // the inputs are disabled: FormData skips disabled fields.
    function templatesFormData() {
        var data = new FormData();
        var formMode = el('templates-form-mode');
        if (!formMode) return data;
        formMode.querySelectorAll('[name^="template_"]').forEach(function (field) {
            if (field.type === 'file' || field.disabled) return;
            if ((field.type === 'checkbox' || field.type === 'radio') && !field.checked) return;
            data.append(field.getAttribute('name'), field.value);
        });
        return data;
    }

    function seedTemplatesYaml() {
        var yaml = el('templates_yaml');
        return fetch('/api/labs/templates/yaml', { method: 'POST', body: templatesFormData() })
            .then(function (r) { return r.ok ? r.text() : Promise.reject(new Error('seed failed')); })
            .then(function (text) { if (yaml) yaml.value = text; })
            .catch(function () { /* Leave the editor empty — "Insert skeleton" is still available. */ });
    }

    function setTemplatesMode(mode) {
        var useForm = mode === 'form';
        // Only the form builder submits the template_N_* fields; both other paths
        // submit YAML.
        var modeInput = el('templates_mode');
        if (modeInput) modeInput.value = useForm ? 'form' : 'yaml';

        [['form', 'templates-mode-form'], ['devcontainer', 'templates-mode-devcontainer'], ['yaml', 'templates-mode-yaml']]
            .forEach(function (pair) {
                var btn = el(pair[1]);
                if (btn) btn.classList.toggle('selected', mode === pair[0]);
            });

        var formMode = el('templates-form-mode');
        var dcMode = el('templates-devcontainer-mode');
        var yamlMode = el('templates-yaml-mode');
        // The starting points fill the form builder's fields, so they belong to it:
        // the other two paths have their own (a real repo, and "Insert skeleton").
        var presets = el('templates-presets');
        if (presets) presets.style.display = useForm ? '' : 'none';
        if (formMode) formMode.style.display = useForm ? '' : 'none';
        if (dcMode) dcMode.style.display = mode === 'devcontainer' ? '' : 'none';
        if (yamlMode) yamlMode.style.display = mode === 'yaml' ? '' : 'none';

        // Hiding a section is not enough: its inputs would still be submitted, and a
        // `required` field that is hidden but empty blocks submission with an error
        // the admin cannot see (the browser cannot focus a display:none control to
        // report it). Disabling takes the inactive section's inputs out of the form
        // entirely. The devcontainer section carries a required template name, so it
        // must be disabled whenever the admin is in the form or yaml editor.
        if (formMode) {
            formMode.querySelectorAll('input, select, textarea').forEach(function (field) {
                field.disabled = !useForm;
            });
            // Re-enabling the section just undid the per-row IDE gating, which
            // also works by disabling fields. Put it back.
            if (useForm) {
                formMode.querySelectorAll('.template-row').forEach(function (row) {
                    setTemplateIDE(row, rowIDE(row));
                });
            }
        }
        if (dcMode) {
            dcMode.querySelectorAll('input, select, textarea').forEach(function (field) {
                field.disabled = mode !== 'devcontainer';
            });
        }
        var yamlArea = el('templates_yaml');
        if (yamlArea) yamlArea.disabled = useForm;
    }

    function setDevcontainerConfigSource(source) {
        devcontainerConfigSource = source === 'separate' ? 'separate' : 'same';
        var sameBtn = el('devcontainer-config-source-same');
        var sepBtn = el('devcontainer-config-source-separate');
        if (sameBtn) sameBtn.classList.toggle('selected', devcontainerConfigSource === 'same');
        if (sepBtn) sepBtn.classList.toggle('selected', devcontainerConfigSource === 'separate');
        var repoRow = el('devcontainer-config-repo-row');
        if (repoRow) repoRow.style.display = devcontainerConfigSource === 'separate' ? '' : 'none';
    }

    function setDevcontainerSource(source) {
        devcontainerSource = source === 'upload' ? 'upload' : 'git';
        var gitBtn = el('devcontainer-source-git');
        var upBtn = el('devcontainer-source-upload');
        if (gitBtn) gitBtn.classList.toggle('selected', devcontainerSource === 'git');
        if (upBtn) upBtn.classList.toggle('selected', devcontainerSource === 'upload');
        var upRow = el('devcontainer-upload-row');
        if (upRow) upRow.style.display = devcontainerSource === 'upload' ? '' : 'none';
        // The config-repo choice only applies when reading from git; an upload is
        // already the devcontainer.json, so reset back to "same" underneath it.
        var cfgRow = el('devcontainer-config-source-row');
        if (cfgRow) cfgRow.style.display = devcontainerSource === 'upload' ? 'none' : '';
        if (devcontainerSource === 'upload') setDevcontainerConfigSource('same');
    }

    function setDevcontainerCacheMode(mode) {
        var externalBtn = el('devcontainer-cache-external-btn');
        var inClusterBtn = el('devcontainer-cache-incluster-btn');
        var hidden = el('devcontainer_use_in_cluster_cache');
        if (!externalBtn || !inClusterBtn || !hidden) return;

        externalBtn.classList.toggle('selected', mode === 'external');
        inClusterBtn.classList.toggle('selected', mode === 'in-cluster');
        // Only the address is mode-dependent. The registry credential stays: an
        // in-cluster cache brings its own credentials, but the devcontainer's base
        // image can still live in a private registry the build has to pull from.
        var externalFields = el('devcontainer-cache-external-fields');
        if (externalFields) externalFields.style.display = mode === 'in-cluster' ? 'none' : '';
        hidden.value = mode === 'in-cluster' ? 'true' : 'false';
    }

    // ---- Devcontainer import ------------------------------------------------

    function importMessage(kind, text) {
        var out = el('devcontainer-import-result');
        if (!out) return;
        out.innerHTML = '<div class="toast toast--inline toast-' + kind + '"><span>' + escapeHtml(text) + '</span></div>';
    }

    // Reports what the builder will produce, and every key it will ignore. The
    // warning list is the point of importing: it is cheaper to learn here than from
    // a student halfway through a workshop.
    function renderImport(data) {
        var out = el('devcontainer-import-result');
        if (!out) return;

        var base = data.base || {};
        var summary = 'no image or Dockerfile — the fallback image is used';
        if (base.kind === 'image') summary = 'image <code>' + escapeHtml(base.image) + '</code>';
        else if (base.kind === 'dockerfile') summary = 'built from <code>' + escapeHtml(base.dockerfile) + '</code>';

        var html = '<div class="toast toast--inline toast-success"><span>Imported <code>' +
            escapeHtml(data.path || 'devcontainer.json') + '</code> — ' + summary + '.</span></div>';

        if (data.features && data.features.length) {
            html += '<p class="devcontainer-import-note">Features built into the workspace: ' +
                data.features.map(function (f) { return '<code>' + escapeHtml(f) + '</code>'; }).join(', ') + '</p>';
        }
        if (data.warnings && data.warnings.length) {
            html += '<p class="devcontainer-import-note">These parts of the devcontainer will not take effect:</p><ul class="devcontainer-import-warnings">';
            data.warnings.forEach(function (w) {
                html += '<li><code>' + escapeHtml(w.key) + '</code> — ' + escapeHtml(w.message) + '</li>';
            });
            html += '</ul>';
        }
        out.innerHTML = html;
    }

    function runImport() {
        var importBtn = el('btn-run-devcontainer-import');
        var reviewBtn = el('btn-devcontainer-review-yaml');
        var yamlArea = el('templates_yaml');

        // Asked for rather than derived from the devcontainer: its "name" is a
        // display string many repos leave at a scaffolded default, which would give
        // every imported template the same name — and names must be unique in a lab.
        var templateName = ((el('devcontainer_template_name') || {}).value || '').trim();
        if (!templateName) { importMessage('error', 'Template name is required.'); return; }

        var gitNames = config.gitCredentialNames() || [];
        var registryNames = config.registryCredentialNames() || [];

        var body = new FormData();
        body.append('template_name', templateName);
        body.append('template_description', (el('devcontainer_template_description') || {}).value || '');
        body.append('source', devcontainerSource);
        body.append('git_repo', (el('devcontainer_git_repo') || {}).value || '');
        body.append('git_branch', (el('devcontainer_git_branch') || {}).value || '');
        body.append('devcontainer_dir', (el('devcontainer_dir') || {}).value || '');
        body.append('cache_repo', (el('devcontainer_cache_repo') || {}).value || '');
        body.append('use_in_cluster_cache', (el('devcontainer_use_in_cluster_cache') || {}).value || 'false');
        body.append('cpu', (el('devcontainer_cpu') || {}).value || '');
        body.append('cpu_limit', (el('devcontainer_cpu_limit') || {}).value || '');
        body.append('memory', (el('devcontainer_memory') || {}).value || '');
        body.append('memory_limit', (el('devcontainer_memory_limit') || {}).value || '');

        // The registry credential each student's workspace pulls the private base
        // image (and pushes the layer cache) with, baked into the generated template.
        var registryAuthSecret = resolveCredential('devcontainer_registry_auth_secret', registryNames);
        body.append('registry_auth_secret', registryAuthSecret);

        // The credential the students' workspaces clone with, baked into the
        // generated template. gitCredentialAuth decides whether this process also
        // sends the token for the import's own clone, or leaves the server to
        // resolve the name (see the hook's doc comment at the top of this file).
        var gitAuthSecret = resolveCredential('devcontainer_git_auth_secret', gitNames);
        body.append('git_auth_secret', gitAuthSecret);
        var gitAuth = config.gitCredentialAuth(gitAuthSecret) || { username: '', token: '' };
        body.append('git_username', gitAuth.username || '');
        body.append('git_token', gitAuth.token || '');

        // When the devcontainer lives in a separate repo, the import reads from that
        // repo instead of git_repo — git_repo stays the generated template's content
        // repo either way. Left empty in "same repo" mode, so the server falls back
        // to reading git_repo as it always has.
        if (devcontainerSource === 'git' && devcontainerConfigSource === 'separate') {
            body.append('devcontainer_config_repo', (el('devcontainer_config_repo') || {}).value || '');
            body.append('devcontainer_config_branch', (el('devcontainer_config_branch') || {}).value || '');
            var configAuthSecret = resolveCredential('devcontainer_config_auth_secret', gitNames);
            body.append('devcontainer_config_auth_secret', configAuthSecret);
            var configAuth = config.gitCredentialAuth(configAuthSecret) || { username: '', token: '' };
            body.append('devcontainer_config_username', configAuth.username || '');
            body.append('devcontainer_config_token', configAuth.token || '');
        }

        if (devcontainerSource === 'upload') {
            var fileInput = el('devcontainer_file');
            var file = fileInput && fileInput.files[0];
            if (!file) { importMessage('error', 'Choose a devcontainer.json or a repository .zip to upload.'); return; }
            body.append('devcontainer_file', file);
        }

        config.decorateImport(body);

        importMessage('success', 'Reading the devcontainer…');
        if (importBtn) importBtn.disabled = true;

        fetch('/api/templates/detect-devcontainer', { method: 'POST', body: body })
            .then(function (response) {
                return response.json().then(function (data) { return { ok: response.ok, data: data }; });
            })
            .then(function (r) {
                if (!r.ok) { importMessage('error', r.data.message || 'Could not read the devcontainer.'); return; }
                if (yamlArea) yamlArea.value = r.data.templates_yaml || '';
                renderImport(r.data);
                // The YAML is now populated — surface the way to go review it.
                if (reviewBtn) reviewBtn.style.display = '';
            })
            .catch(function () { importMessage('error', 'Could not reach the server.'); })
            .finally(function () { if (importBtn) importBtn.disabled = false; });
    }

    // ---- Wiring -------------------------------------------------------------

    function on(id, handler) {
        var node = el(id);
        if (node) node.addEventListener('click', handler);
    }

    function init(options) {
        Object.keys(options || {}).forEach(function (key) { config[key] = options[key]; });

        on('templates-mode-form', function () { setTemplatesMode('form'); });
        on('templates-mode-devcontainer', function () {
            // The form builder's first template usually already points at the
            // workshop repo — carry it across rather than making the admin retype it.
            var repoField = el('devcontainer_git_repo');
            var formRepo = document.querySelector('#templates-form-mode [name="template_0_git_repo"]');
            if (repoField && !repoField.value && formRepo && formRepo.value) repoField.value = formRepo.value;
            var nameField = el('devcontainer_template_name');
            var formName = document.querySelector('#templates-form-mode [name="template_0_name"]');
            if (nameField && !nameField.value && formName && formName.value) nameField.value = formName.value;
            setTemplatesMode('devcontainer');
        });
        on('templates-mode-yaml', function () {
            var yamlArea = el('templates_yaml');
            var seeded = yamlArea && !yamlArea.value.trim() ? seedTemplatesYaml() : Promise.resolve();
            seeded.then(function () { setTemplatesMode('yaml'); });
        });

        on('devcontainer-source-git', function () { setDevcontainerSource('git'); });
        on('devcontainer-source-upload', function () { setDevcontainerSource('upload'); });
        on('devcontainer-config-source-same', function () { setDevcontainerConfigSource('same'); });
        on('devcontainer-config-source-separate', function () { setDevcontainerConfigSource('separate'); });
        on('devcontainer-cache-external-btn', function () { setDevcontainerCacheMode('external'); });
        on('devcontainer-cache-incluster-btn', function () { setDevcontainerCacheMode('in-cluster'); });
        on('btn-run-devcontainer-import', runImport);
        // Once the import succeeds the generated YAML is waiting in the editor; this
        // takes the admin there to review it. Hidden until there is something to see.
        on('btn-devcontainer-review-yaml', function () { setTemplatesMode('yaml'); });

        on('btn-skeleton-templates-yaml', function () {
            var yamlArea = el('templates_yaml');
            if (yamlArea && yamlArea.value.trim() &&
                !confirm('Replace the current YAML with the commented skeleton?')) return;
            // Posting no template fields makes the server return the skeleton.
            fetch('/api/labs/templates/yaml', { method: 'POST', body: new FormData() })
                .then(function (r) { return r.ok ? r.text() : Promise.reject(new Error('skeleton failed')); })
                .then(function (text) { if (yamlArea) yamlArea.value = text; })
                .catch(function () { /* Nothing to insert; leave what the admin has. */ });
        });

        var uploadBtn = el('btn-upload-templates-yaml');
        var fileInput = el('templates_yaml_file');
        if (uploadBtn && fileInput) {
            uploadBtn.addEventListener('click', function () { fileInput.click(); });
            fileInput.addEventListener('change', function (e) {
                var file = e.target.files[0];
                if (!file) return;
                var reader = new FileReader();
                reader.onload = function (evt) {
                    var yamlArea = el('templates_yaml');
                    if (yamlArea) yamlArea.value = evt.target.result;
                };
                reader.readAsText(file);
                // Let the same file be picked again after an edit.
                e.target.value = '';
            });
        }

        document.querySelectorAll('#templates-form-mode .template-row').forEach(wireRowButtons);
        wirePresets();
        refreshCredentialOptions();

        var modeInput = el('templates_mode');
        setTemplatesMode(modeInput ? modeInput.value : 'form');
        setDevcontainerSource('git');
    }

    return {
        init: init,
        escapeHtml: escapeHtml,
        refreshCredentialOptions: refreshCredentialOptions,
        createVariableRow: createVariableRow,
        createNodeSelectorRow: createNodeSelectorRow,
        createSidecarRow: createSidecarRow,
        createMountRow: createMountRow,
        wireRowButtons: wireRowButtons,
        renderSummary: renderSummary,
        applyPreset: applyPreset,
        templatesFormData: templatesFormData,
        setTemplatesMode: setTemplatesMode,
        setDevcontainerSource: setDevcontainerSource,
        setDevcontainerConfigSource: setDevcontainerConfigSource,
        setDevcontainerCacheMode: setDevcontainerCacheMode,
    };
})();
