import { test, expect } from '@playwright/test';

/**
 * The create-lab wizard's two paths diverge on Step 1, and Step 4 (DNS & HTTPS)
 * is where that divergence shows: on "Create New Infrastructure" EasyLab builds
 * the cluster itself, so nothing can already be installed on it. Reusing an
 * ingress controller or a cert-manager is not a choice there — picking it would
 * produce a lab that provisions cleanly and then serves nothing — and node
 * selectors have a single node pool to point at.
 *
 * These tests pin that gating, and pin that the hidden inputs behind it still
 * submit the values the backend reads (handler.go reads install_nginx_ingress /
 * install_cert_manager straight off the form).
 */
test.describe('Lab wizard — Create New Infrastructure path', () => {
  const byokOnly = [
    '#ingress-mode-group',
    '#ingress-existing-fields',
    '#traefik-nodeselector-fields',
    '#certmanager-toggle-group',
  ];

  test.beforeEach(async ({ page }) => {
    await page.goto('/login');
    await page.locator('input[type="password"]').fill('testpassword');
    await page.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');
    await page.goto('/admin');
  });

  // The mode cards live on Step 1, and setClusterMode resets the wizard back to
  // it anyway, so return there before switching and jump afterwards.
  async function chooseModeThenGoTo(page: any, mode: 'new' | 'existing', step: number) {
    await page.evaluate(() => {
      (window as any).wizard.currentStep = 1;
      (window as any).wizard.updateUI();
    });
    await page.locator(`#cluster-mode-${mode}`).click();
    await page.evaluate((s: number) => {
      (window as any).wizard.currentStep = s;
      (window as any).wizard.updateUI();
    }, step);
    await expect(page.locator(`.wizard-step[data-step="${step}"]`)).toBeVisible();
  }

  // Per-template node selectors live inside each row's collapsed "Advanced options".
  async function openTemplateAdvanced(page: any) {
    await page.locator('.template-row .template-advanced > summary').first().click();
  }

  test('hides every reuse toggle and node selector on Step 4', async ({ page }) => {
    await chooseModeThenGoTo(page, 'new', 4);

    for (const selector of byokOnly) {
      await expect(page.locator(selector)).toBeHidden();
    }

    // The choice that does belong here is untouched.
    await expect(page.locator('#domain-mode-quickstart-btn')).toBeVisible();
    await expect(page.locator('#domain-mode-auto-btn')).toBeVisible();
  });

  test('states what gets installed, and tracks the domain choice', async ({ page }) => {
    await chooseModeThenGoTo(page, 'new', 4);

    const note = page.locator('#new-infra-install-note');
    await expect(note).toBeVisible();

    // No domain means no certificates to issue, so cert-manager is not installed.
    await page.locator('#domain-mode-quickstart-btn').click();
    await expect(page.locator('#install-note-with-tls')).toBeHidden();
    const ingressOnly = page.locator('#install-note-ingress-only');
    await expect(ingressOnly).toBeVisible();
    await expect(ingressOnly).toContainText('Traefik');
    await expect(ingressOnly).not.toContainText('cert-manager');

    await page.locator('#domain-mode-auto-btn').click();
    await expect(page.locator('#install-note-ingress-only')).toBeHidden();
    const withTLS = page.locator('#install-note-with-tls');
    await expect(withTLS).toBeVisible();
    await expect(withTLS).toContainText('cert-manager');
  });

  test('hides the per-template node selector on Step 6', async ({ page }) => {
    await chooseModeThenGoTo(page, 'new', 6);
    await openTemplateAdvanced(page);
    await expect(page.locator('.template-nodeselector-section').first()).toBeHidden();
    // Its neighbours inside the same <details> are still there, so the section is
    // hidden by the new-infra rule rather than by a collapsed parent.
    await expect(page.locator('.template-row .btn-add-mount').first()).toBeVisible();
  });

  test('leaves the Use Existing Cluster path untouched', async ({ page }) => {
    await chooseModeThenGoTo(page, 'existing', 4);

    await expect(page.locator('#new-infra-install-note')).toBeHidden();
    await expect(page.locator('#ingress-mode-group')).toBeVisible();
    await expect(page.locator('#traefik-nodeselector-fields')).toBeVisible();

    // cert-manager only appears once there is a domain for it to serve.
    await page.locator('#domain-mode-auto-btn').click();
    await expect(page.locator('#certmanager-toggle-group')).toBeVisible();
    await expect(page.locator('#certmanager-existing-btn')).toBeVisible();

    await chooseModeThenGoTo(page, 'existing', 6);
    await openTemplateAdvanced(page);
    await expect(page.locator('.template-nodeselector-section').first()).toBeVisible();
  });

  test('submits install=true after switching back from an existing cluster', async ({ page }) => {
    await chooseModeThenGoTo(page, 'existing', 4);
    await page.locator('#domain-mode-auto-btn').click();
    await page.locator('#ingress-existing-btn').click();
    await page.locator('#certmanager-existing-btn').click();
    await expect(page.locator('#install_nginx_ingress')).toHaveValue('false');

    await page.evaluate(() => {
      (window as any).wizard.currentStep = 1;
      (window as any).wizard.updateUI();
    });
    await page.locator('#cluster-mode-new').click();
    await expect(page.locator('#install_nginx_ingress')).toHaveValue('true');
    await expect(page.locator('#install_cert_manager')).toHaveValue('true');
  });
});
