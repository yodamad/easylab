import { test, expect, Page } from '@playwright/test';

/**
 * A lab's optional "resource link" (slides, notes) on the student pages.
 *
 * Only http(s) links are rendered; anything else (javascript:) is dropped
 * client-side. The student endpoints are mocked: reaching the real thing needs
 * a lab that has finished provisioning against a cluster.
 */
const EMAIL = 'alice@example.com';

async function login(page: Page) {
  await page.goto('/student/login');
  await page.locator('input[type="email"]').fill(EMAIL);
  await page.locator('input[type="password"]').fill('studentpass');
  await page.locator('button[type="submit"]').click();
  await page.waitForLoadState('networkidle');
}

async function mockLabs(page: Page, labs: unknown[]) {
  await page.route('**/api/student/labs', route => route.fulfill({ json: labs }));
  await page.route('**/api/student/labs/templates?*', route => route.fulfill({ json: [] }));
}

test.describe('Lab resource link', () => {
  test.beforeEach(async ({ page }) => {
    await login(page);
  });

  test('picker tile shows an https link that opens safely in a new tab', async ({ page }) => {
    await mockLabs(page, [
      { id: 'lab-1', config: { stack_name: 'devoxx', resource_link: 'https://slides.example.com/devoxx' } },
      { id: 'lab-2', config: { stack_name: 'plain' } },
    ]);
    await page.goto('/student/dashboard');

    await expect(page.locator('#lab-tiles .template-tile')).toHaveCount(2);
    const link = page.locator('a.picker-tile-link');
    await expect(link).toHaveCount(1);
    await expect(link).toHaveAttribute('href', 'https://slides.example.com/devoxx');
    await expect(link).toHaveAttribute('target', '_blank');
    await expect(link).toHaveAttribute('rel', /noopener/);
  });

  test('picker tile drops a javascript: resource link', async ({ page }) => {
    await mockLabs(page, [{ id: 'lab-1', config: { stack_name: 'devoxx', resource_link: 'javascript:alert(1)' } }]);
    await page.goto('/student/dashboard');

    await expect(page.locator('#lab-tiles .template-tile')).toHaveCount(1);
    await expect(page.locator('a.picker-tile-link')).toHaveCount(0);
  });

  test('workspace card shows the lab link only when it is http(s)', async ({ page }) => {
    const ws = (name: string, link: string) => ({
      lab_id: name, lab_name: name, workspace_name: `ws-${name}`,
      workspace_url: `https://ws-${name}.example.com/`, email: EMAIL, template: 'go',
      created_at: '2026-10-06T09:00:00Z', deletion_at: '', ready: true, teacher_accessed_at: '',
      lab_resource_link: link,
    });
    await page.route('**/api/student/workspaces', route =>
      route.fulfill({ json: { workspaces: [ws('good', 'https://slides.example.com/x'), ws('bad', 'javascript:alert(1)')], incomplete: false } }),
    );
    await page.goto('/student/workspaces');

    const cards = page.locator('.workspace-card');
    await expect(cards).toHaveCount(2);
    const link = cards.nth(0).locator('a.workspace-card-link');
    await expect(link).toHaveAttribute('href', 'https://slides.example.com/x');
    await expect(link).toHaveAttribute('target', '_blank');
    await expect(link).toHaveAttribute('rel', /noopener/);
    await expect(cards.nth(1).locator('a.workspace-card-link')).toHaveCount(0);
  });
});
