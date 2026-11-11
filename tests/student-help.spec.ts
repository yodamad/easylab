import { test, expect, Page } from '@playwright/test';

/**
 * The "Ask for help" button in the student portal header.
 *
 * What a request does on the server — who may raise one, how it reaches the
 * admin, when it expires — is decided there and tested there (see
 * internal/server/help_test.go). What is tested here is how the header button
 * and its dialog present it, so the endpoints are mocked: reaching the real ones
 * needs a lab that has finished provisioning against a cluster.
 */
const EMAIL = 'alice@example.com';
const LAB_ID = 'job-11111111-2222-3333-4444-555555555555';

type HelpState = { status: string; message?: string } | null;

/** mockHelp serves the student's own request state, and records what is posted. */
async function mockHelp(page: Page, initial: HelpState) {
  const posted: Array<Record<string, string>> = [];
  let state = initial;

  await page.route('**/api/student/labs', route =>
    route.fulfill({
      json: [
        { id: LAB_ID, config: { stack_name: 'Devoxx 2026' } },
      ],
    }),
  );
  await page.route('**/api/student/workspaces', route =>
    route.fulfill({ json: { workspaces: [], incomplete: false } }),
  );

  await page.route('**/api/student/help', async route => {
    const request = route.request();
    if (request.method() === 'POST') {
      const fields = Object.fromEntries(new URLSearchParams(request.postData() ?? ''));
      posted.push(fields);
      state = { status: 'waiting', message: fields.message };
      await route.fulfill({
        json: {
          success: true,
          request: { lab_id: LAB_ID, lab_name: 'Devoxx 2026', status: 'waiting', message: fields.message, at: new Date().toISOString() },
        },
      });
      return;
    }
    await route.fulfill({
      json: {
        requests: state
          ? [{ lab_id: LAB_ID, lab_name: 'Devoxx 2026', status: state.status, message: state.message ?? '', at: new Date().toISOString() }]
          : [],
      },
    });
  });

  await page.route('**/api/student/help/cancel', async route => {
    state = null;
    await route.fulfill({ json: { success: true, cancelled: true } });
  });

  return posted;
}

test.describe('Student portal: ask for help', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/student/login');
    await page.locator('input[type="email"]').fill(EMAIL);
    await page.locator('input[type="password"]').fill('studentpass');
    await page.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');
  });

  test('raises a request from the header and reflects it on the button', async ({ page }) => {
    const posted = await mockHelp(page, null);
    await page.goto('/student/workspaces');

    const button = page.locator('#student-help-btn');
    await expect(button).toBeVisible();
    await expect(button).toHaveText(/Ask for help/);

    await button.click();
    const dialog = page.locator('#student-help-dialog');
    await expect(dialog).toBeVisible();
    // One lab, so there is nothing to choose.
    await expect(page.locator('#student-help-lab-row')).toBeHidden();

    await page.locator('#student-help-message').fill('My pod will not start');
    await page.locator('#student-help-submit').click();

    await expect(dialog).toBeHidden();
    await expect(button).toHaveText(/Help requested/);
    await expect(button).toHaveClass(/student-help-btn--active/);

    expect(posted).toHaveLength(1);
    expect(posted[0].lab_id).toBe(LAB_ID);
    expect(posted[0].message).toBe('My pod will not start');
  });

  test('shows that an organiser picked the request up', async ({ page }) => {
    await mockHelp(page, { status: 'handling', message: 'My pod will not start' });
    await page.goto('/student/workspaces');

    const button = page.locator('#student-help-btn');
    await expect(button).toHaveText(/Help on the way/);
    await expect(button).toHaveClass(/student-help-btn--handling/);
  });

  test('withdraws an open request', async ({ page }) => {
    await mockHelp(page, { status: 'waiting', message: 'My pod will not start' });
    await page.goto('/student/workspaces');

    const button = page.locator('#student-help-btn');
    await expect(button).toHaveText(/Help requested/);

    await button.click();
    const dialog = page.locator('#student-help-dialog');
    await expect(dialog).toBeVisible();
    // Reopening shows what was asked, and offers to take it back.
    await expect(page.locator('#student-help-message')).toHaveValue('My pod will not start');

    await page.locator('#student-help-cancel').click();
    await expect(dialog).toBeHidden();
    await expect(button).toHaveText(/Ask for help/);
    await expect(button).not.toHaveClass(/student-help-btn--active/);
  });

  test('is offered on the request-a-workspace page too', async ({ page }) => {
    await mockHelp(page, null);
    await page.goto('/student/dashboard');
    await expect(page.locator('#student-help-btn')).toBeVisible();
  });
});
