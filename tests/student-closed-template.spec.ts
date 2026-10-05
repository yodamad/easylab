import { test, expect, Page } from '@playwright/test';

/**
 * Closed labs and templates in the student picker.
 *
 * A lab or template closed to new students is only listed for a student who
 * already has a workspace on it; the server decides that (see
 * internal/server/availability_test.go). What is tested here is how the picker
 * presents such an item, so the two student endpoints are mocked: reaching the
 * real thing needs a lab that has finished provisioning against a cluster.
 */
async function mockPicker(page: Page, labs: unknown[], templates: unknown[]) {
  await page.route('**/api/student/labs', route => route.fulfill({ json: labs }));
  await page.route('**/api/student/labs/templates?*', route => route.fulfill({ json: templates }));
}

test.describe('Student picker: closed labs and templates', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/student/login');
    await page.locator('input[type="email"]').fill('alice@example.com');
    await page.locator('input[type="password"]').fill('studentpass');
    await page.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');
  });

  test('marks a closed template and offers to open the existing workspace', async ({ page }) => {
    await mockPicker(
      page,
      [{ id: 'lab-1', config: { stack_name: 'devoxx' } }],
      [
        { id: 'go', name: 'go' },
        { id: 'python', name: 'python', closed: true },
      ],
    );
    await page.goto('/student/dashboard');

    const tiles = page.locator('#template-tiles .template-tile');
    await expect(tiles).toHaveCount(2);

    const open = tiles.filter({ hasText: 'go' });
    const closed = tiles.filter({ hasText: 'python' });
    await expect(open).not.toHaveClass(/is-closed/);
    await expect(closed).toHaveClass(/is-closed/);
    await expect(closed.locator('.template-tile-closed-note')).toHaveText(
      'Closed to new students. Your workspace is still here.',
    );

    const submit = page.locator('#submit-btn');
    await closed.click();
    await expect(submit).toBeEnabled();
    await expect(submit).toHaveText('Open my workspace');

    await open.click();
    await expect(submit).toHaveText('Request Workspace');
  });

  test('marks a closed lab the student still has a workspace on', async ({ page }) => {
    await mockPicker(
      page,
      [{ id: 'lab-1', config: { stack_name: 'devoxx', disabled: true } }],
      [{ id: 'go', name: 'go', closed: true }],
    );
    await page.goto('/student/dashboard');

    await expect(page.locator('#lab-tiles .template-tile')).toHaveClass(/is-closed/);
    // A lone lab and a lone template are both pre-selected.
    await expect(page.locator('#template-tiles .template-tile')).toHaveClass(/is-closed/);
    await expect(page.locator('#submit-btn')).toHaveText('Open my workspace');
  });

  test('an open lab with open templates is unchanged', async ({ page }) => {
    await mockPicker(page, [{ id: 'lab-1', config: { stack_name: 'devoxx' } }], [{ id: 'go', name: 'go' }]);
    await page.goto('/student/dashboard');

    await expect(page.locator('#template-tiles .template-tile')).toHaveCount(1);
    await expect(page.locator('.is-closed')).toHaveCount(0);
    await expect(page.locator('.template-tile-closed-note')).toHaveCount(0);
    await expect(page.locator('#submit-btn')).toHaveText('Request Workspace');
  });
});
