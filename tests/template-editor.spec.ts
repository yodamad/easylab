import { test, expect } from '@playwright/test';

/**
 * The workspace template editor — the three-mode form shared by the create-lab
 * wizard's Step 6 and the "Add Template" drawer on a lab's detail page.
 *
 * The two surfaces render one partial (web/partials/template-editor.html) and run
 * one module (web/static/template-editor.js), which is what keeps them from
 * drifting the way they had. These tests drive the wizard, because reaching the
 * drawer needs a lab that has finished provisioning against a real cluster.
 * Markup parity between the two is pinned server-side instead, by
 * TestTemplateEditorFieldsOnBothSurfaces in internal/server/template_partials_test.go.
 */
test.describe('Workspace template editor', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/login');
    await page.locator('input[type="password"]').fill('testpassword');
    await page.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');

    await page.goto('/admin');
    // Jumping straight to Step 6 keeps these tests about the editor rather than
    // about the infrastructure steps in front of it, which have their own specs.
    await page.evaluate(() => {
      (window as any).wizard.currentStep = 6;
      (window as any).wizard.updateUI();
    });
    await expect(page.locator('.wizard-step[data-step="6"]')).toBeVisible();
  });

  test('offers every field the form builder submits', async ({ page }) => {
    const fields = [
      'name', 'description', 'git_repo', 'git_branch', 'git_folder',
      'git_auth_secret', 'image', 'cpu', 'cpu_limit', 'memory',
      'memory_limit', 'disk_size', 'startup_script', 'dotfiles_repo', 'extensions',
    ];
    for (const field of fields) {
      await expect(page.locator(`#templates-form-mode [name="template_0_${field}"]`)).toHaveCount(1);
    }

    // The four kinds of repeating row.
    for (const container of [
      '.template-variables-container', '.template-sidecars-container',
      '.template-mounts-container', '.template-nodeselector-container',
    ]) {
      await expect(page.locator(`#templates-form-mode ${container}`)).toHaveCount(1);
    }
  });

  test('switches between the three modes, disabling the ones not in use', async ({ page }) => {
    const mode = page.locator('#templates_mode');
    const templateName = page.locator('#templates-form-mode [name="template_0_name"]');
    const devcontainerName = page.locator('#devcontainer_template_name');

    await expect(mode).toHaveValue('form');
    await expect(page.locator('#templates-form-mode')).toBeVisible();

    await page.locator('#templates-mode-devcontainer').click();
    await expect(page.locator('#templates-devcontainer-mode')).toBeVisible();
    // Hiding is not enough: a hidden-but-required field blocks submission with an
    // error the admin cannot see, so the inactive panel is disabled outright.
    await expect(templateName).toBeDisabled();
    await expect(devcontainerName).toBeEnabled();
    // The server only knows form vs yaml; the devcontainer path resolves to yaml.
    await expect(mode).toHaveValue('yaml');

    await page.locator('#templates-mode-form').click();
    await expect(templateName).toBeEnabled();
    await expect(devcontainerName).toBeDisabled();
    await expect(mode).toHaveValue('form');
  });

  test('carries the repo and name into the devcontainer importer', async ({ page }) => {
    await page.locator('#templates-form-mode [name="template_0_name"]').fill('go-basics');
    await page.locator('#templates-form-mode [name="template_0_git_repo"]').fill('https://gitlab.com/o/r.git');

    await page.locator('#templates-mode-devcontainer').click();

    await expect(page.locator('#devcontainer_template_name')).toHaveValue('go-basics');
    await expect(page.locator('#devcontainer_git_repo')).toHaveValue('https://gitlab.com/o/r.git');
  });

  test('seeds the YAML editor from the form', async ({ page }) => {
    await page.locator('#templates-form-mode [name="template_0_name"]').fill('go-basics');
    await page.locator('#templates-form-mode [name="template_0_git_repo"]').fill('https://gitlab.com/o/r.git');

    await page.locator('#templates-mode-yaml').click();
    await expect(page.locator('#templates_yaml')).toHaveValue(/name: go-basics/);

    await page.locator('#btn-validate-templates-yaml').click();
    await expect(page.locator('#templates-yaml-validation')).toContainText('go-basics');
  });

  test('devcontainer cache choice hides what it makes irrelevant', async ({ page }) => {
    await page.locator('#templates-mode-devcontainer').click();

    const address = page.locator('#devcontainer-cache-external-fields');
    const registryCred = page.locator('#devcontainer-registry-cred-group');
    await expect(address).toBeVisible();

    // An in-cluster registry is provisioned by EasyLab, so there is no address to
    // give and no credential to pull it with.
    await page.locator('#devcontainer-cache-incluster-btn').click();
    await expect(address).toBeHidden();
    await expect(registryCred).toBeHidden();
    await expect(page.locator('#devcontainer_use_in_cluster_cache')).toHaveValue('true');

    await page.locator('#devcontainer-cache-external-btn').click();
    await expect(address).toBeVisible();
    await expect(page.locator('#devcontainer_use_in_cluster_cache')).toHaveValue('false');
  });

  test('devcontainer config can live in a separate repository', async ({ page }) => {
    await page.locator('#templates-mode-devcontainer').click();

    const configRow = page.locator('#devcontainer-config-repo-row');
    await expect(configRow).toBeHidden();

    await page.locator('#devcontainer-config-source-separate').click();
    await expect(configRow).toBeVisible();
    await expect(page.locator('#devcontainer_config_repo')).toBeVisible();
    await expect(page.locator('#devcontainer_config_auth_secret')).toBeVisible();

    // An upload already *is* the devcontainer.json, so there is nothing to clone
    // and the choice goes away.
    await page.locator('#devcontainer-source-upload').click();
    await expect(page.locator('#devcontainer-config-source-row')).toBeHidden();
    await expect(page.locator('#devcontainer-upload-row')).toBeVisible();
  });

  test('adds a repeating row of each kind, named for its template index', async ({ page }) => {
    const row = page.locator('#workspace-templates-container .template-row').first();
    await row.locator('details.template-advanced > summary').click();

    await row.locator('.btn-add-variable').click();
    await row.locator('.btn-add-sidecar').click();
    await row.locator('.btn-add-mount').click();
    await row.locator('.btn-add-nodeselector').click();

    await expect(row.locator('[name="template_0_env_name"]')).toBeVisible();
    await expect(row.locator('[name="template_0_sidecar_name"]')).toBeVisible();
    await expect(row.locator('[name="template_0_mount_type"]')).toBeVisible();
    await expect(row.locator('[name="template_0_nodeselector_key"]')).toBeVisible();
  });

  test('a second template reindexes, and its rows follow', async ({ page }) => {
    await page.locator('#btn-add-template').click();

    const rows = page.locator('#workspace-templates-container .template-row');
    await expect(rows).toHaveCount(2);
    await expect(page.locator('#template_count')).toHaveValue('2');

    // The clone ships with index-0 names; without reindexing the second template
    // would overwrite the first on the server.
    const second = rows.nth(1);
    await expect(second.locator('[name="template_1_name"]')).toBeVisible();

    await second.locator('details.template-advanced > summary').click();
    await second.locator('.btn-add-nodeselector').click();
    await expect(second.locator('[name="template_1_nodeselector_key"]')).toBeVisible();
  });

  test('credentials defined in the wizard reach every picker', async ({ page }) => {
    await page.locator('details.wizard-credentials > summary').click();
    await page.locator('#btn-add-credential').click();

    const credential = page.locator('#wizard-credentials-container .credential-row').first();
    await credential.locator('.credential-kind').selectOption('git');
    await credential.locator('[name="secret_name"]').fill('gitlab-token');

    // A single git credential is applied automatically, which the empty option says.
    await page.locator('#workspace-templates-container details.template-advanced > summary').first().click();
    const picker = page.locator('#templates-form-mode .template-git-cred-select').first();
    await expect(picker.locator('option')).toHaveCount(2);
    await expect(picker.locator('option').first()).toHaveText('Auto — use gitlab-token');
    await expect(picker.locator('option').nth(1)).toHaveText('gitlab-token');
  });
});
