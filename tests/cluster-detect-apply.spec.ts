import { test, expect } from '@playwright/test';

/**
 * "Detect from cluster" reports; "Apply to this lab" writes. These tests pin the
 * writing half, which has no server-side equivalent to test: the handler returns
 * a `suggested` object keyed by form field names, and everything after that is
 * admin.js deciding which wizard controls to move.
 *
 * The case that matters most is the ClusterIssuer. A suggested issuer is always
 * one that is Ready with a DNS-01 solver, and such an issuer can only serve a
 * domain the admin owns — so applying it has to select "Custom domain —
 * automatic" too. The wizard opens on Quick start, where both the "Already
 * configured" toggle and the issuer-name field are hidden, so an apply that does
 * not move the domain mode silently drops both values.
 *
 * The cluster is stubbed: these are front-end mapping rules, and the scan itself
 * is covered by internal/clusterscan and internal/server/cluster_detect_test.go.
 */
test.describe('Detect from cluster — Apply', () => {
  const baseReport = {
    cluster: { status: 'ok', version: 'v1.31.4', platform: 'linux/amd64' },
    ingress: {
      status: 'ok', class: 'traefik', controller: 'traefik.io/ingress-controller',
      flavour: 'traefik', namespace: 'traefik', service: 'traefik',
      service_type: 'LoadBalancer', external_ip: '51.210.44.12',
      classes_probe: { status: 'ok' },
    },
    cert_manager: { status: 'ok', installed: true, namespace: 'cert-manager' },
    storage: { status: 'ok', default: 'csi-cinder-high-speed', node_local: false },
    nodes: { status: 'ok', count: 3, labels: [{ key: 'nodepool', values: ['workshop'], nodes: 3 }] },
    namespaces: { status: 'ok', workspace_namespace: 'workshops', workspace_namespace_exists: true },
    permissions: { status: 'ok', checks: [], missing: [] },
  };

  // What suggestedFromReport returns for that cluster, with and without an issuer
  // the lab could actually use.
  const reusable = {
    install_nginx_ingress: false,
    nginx_ingress_namespace: 'traefik',
    nginx_ingress_service_name: 'traefik',
    ingress_class_name: 'traefik',
    install_cert_manager: false,
    cert_manager_namespace: 'cert-manager',
    workspace_namespace: 'workshops',
    node_selector_key: 'nodepool',
    node_selector_value: 'workshop',
  };

  async function stubDetect(page: any, suggested: Record<string, unknown>, issuers: unknown[]) {
    await page.route('**/api/cluster/detect', (route: any) => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: JSON.stringify({ report: { ...baseReport, issuers: { status: 'ok', items: issuers } }, suggested }),
    }));
  }

  test.beforeEach(async ({ page }) => {
    await page.goto('/login');
    await page.locator('input[type="password"]').fill('testpassword');
    await page.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');
    await page.goto('/admin');
    await page.waitForSelector('#lab-form');

    // A BYO lab on Step 4, with a kubeconfig present so the button has something
    // to post. The wizard opens on Quick start, which is the point of the test.
    await page.evaluate(() => {
      const w = (window as any).wizard;
      w.setClusterMode(true);
      (document.getElementById('kubeconfig_content') as HTMLTextAreaElement).value = 'apiVersion: v1';
      w.goToStep(4);
    });
    await expect(page.locator('#domain-mode-quickstart-btn')).toHaveClass(/selected/);
  });

  test('applies a usable issuer by switching to a custom domain', async ({ page }) => {
    await stubDetect(page, {
      ...reusable,
      cluster_issuer_name: 'letsencrypt-prod',
      dns_already_configured: true,
    }, [{ name: 'letsencrypt-prod', kind: 'ClusterIssuer', has_dns01: true, has_http01: false, ready: true, staging: false }]);

    await page.locator('#btn-detect-cluster').click();
    const apply = page.locator('#btn-apply-cluster-detect');
    await expect(apply).toBeVisible();

    // The note promises the mode change before the admin commits to it.
    const note = page.locator('.cluster-detect-actions small');
    await expect(note).toContainText('Custom domain — automatic');
    await expect(note).toContainText('letsencrypt-prod');

    await apply.click();

    await expect(page.locator('#domain-mode-auto-btn')).toHaveClass(/selected/);
    await expect(page.locator('#ingress-existing-btn')).toHaveClass(/selected/);
    await expect(page.locator('#certmanager-existing-btn')).toHaveClass(/selected/);
    await expect(page.locator('#dns-already-configured-yes-btn')).toHaveClass(/selected/);
    await expect(page.locator('#cluster-issuer-name-group')).toBeVisible();

    // The hidden inputs are what handler.go actually reads off the form.
    await expect(page.locator('#dns_already_configured')).toHaveValue('true');
    await expect(page.locator('#cluster_issuer_name')).toHaveValue('letsencrypt-prod');
    await expect(page.locator('#install_cert_manager')).toHaveValue('false');
    await expect(page.locator('#install_nginx_ingress')).toHaveValue('false');
    await expect(page.locator('#ingress_class_name')).toHaveValue('traefik');
    await expect(page.locator('#cert_manager_namespace')).toHaveValue('cert-manager');

    // Switching to a custom domain leaves the admin two fields to fill, so the
    // confirmation says so rather than only reporting what was done.
    await expect(page.locator('#cluster-detect-applied')).toContainText('Add the domain');
  });

  test('leaves the domain mode alone when no issuer can serve the lab', async ({ page }) => {
    // Reported, but never applied: an HTTP-01-only issuer that is not Ready is
    // exactly the one that stalls a CertificateRequest forever.
    await stubDetect(page, reusable,
      [{ name: 'letsencrypt-http', kind: 'ClusterIssuer', has_dns01: false, has_http01: true, ready: false, staging: false }]);

    await page.locator('#btn-detect-cluster').click();
    const apply = page.locator('#btn-apply-cluster-detect');
    await expect(apply).toBeVisible();
    await expect(page.locator('.cluster-detect-actions small')).not.toContainText('ClusterIssuer');

    await apply.click();

    await expect(page.locator('#domain-mode-quickstart-btn')).toHaveClass(/selected/);
    await expect(page.locator('#dns_already_configured')).toHaveValue('false');
    await expect(page.locator('#cluster_issuer_name')).toHaveValue('');
    // The rest of the scan still applies.
    await expect(page.locator('#install_nginx_ingress')).toHaveValue('false');
    await expect(page.locator('#cert_manager_namespace')).toHaveValue('cert-manager');
  });

  test('does not promise node selectors when both components are reused', async ({ page }) => {
    await stubDetect(page, {
      ...reusable,
      cluster_issuer_name: 'letsencrypt-prod',
      dns_already_configured: true,
    }, [{ name: 'letsencrypt-prod', kind: 'ClusterIssuer', has_dns01: true, has_http01: false, ready: true, staging: false }]);

    await page.locator('#btn-detect-cluster').click();
    await expect(page.locator('#btn-apply-cluster-detect')).toBeVisible();
    // createLabConfigFromForm only reads the node-selector fields for a component
    // EasyLab installs, so reusing both leaves nothing for Apply to seed.
    await expect(page.locator('.cluster-detect-actions small')).not.toContainText('node selectors');

    await page.locator('#btn-apply-cluster-detect').click();
    await expect(page.locator('#traefik-nodeselector-container')).toBeEmpty();
    await expect(page.locator('#certmanager-nodeselector-container')).toBeEmpty();
  });
});
