import { test, expect } from '@playwright/test';

/**
 * GitHub student login, as configured from the admin area (/admin/github).
 *
 * These tests cover what an admin controls: turning the sign-in button on,
 * restricting it, and turning it off again. The OAuth round trip itself needs
 * github.com, so it is covered server-side against a fake GitHub in
 * internal/server/github_auth_test.go.
 *
 * The settings are persisted by the server, so the tests run in order and the
 * last one leaves GitHub login off for the other specs.
 */
test.describe.configure({ mode: 'serial' });

test.describe('GitHub login settings', () => {
  const githubButton = 'a[href="/student/auth/github/login"]';

  test.beforeEach(async ({ page }) => {
    await page.goto('/login');
    await page.locator('input[type="password"]').fill('testpassword');
    await page.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');
    await page.goto('/admin/github');
  });

  test('is off by default and shows the callback URL to register', async ({ page, context }) => {
    await expect(page.locator('#github-auth-status')).toContainText('GitHub login is off');
    await expect(page.locator('#github_callback_url')).toHaveValue(/\/student\/auth\/github\/callback$/);
    await expect(page.locator('#github-auth-turn-off')).toHaveCount(0);

    const student = await context.newPage();
    await student.goto('/student/login');
    await expect(student.locator(githubButton)).toHaveCount(0);
  });

  test('asks for the client secret before turning on', async ({ page }) => {
    await page.locator('#github_client_id').fill('test-client-id');
    await page.locator('button[type="submit"]').click();
    await expect(page.locator('#save-response .error-message')).toContainText('Enter the client secret');
  });

  test('turning it on adds the sign-in button and warns that anyone can sign in', async ({ page, context }) => {
    await page.locator('#github_client_id').fill('test-client-id');
    await page.locator('#github_client_secret').fill('test-client-secret');
    await page.locator('button[type="submit"]').click();

    await expect(page.locator('#github-auth-status')).toContainText('any GitHub account');
    await expect(page.locator('#github-auth-status')).toHaveClass(/warning-message/);
    // The saved secret is never sent back to the browser.
    await expect(page.locator('#github_client_secret')).toHaveValue('');
    expect(await page.content()).not.toContain('test-client-secret');

    const student = await context.newPage();
    await student.goto('/student/login');
    await expect(student.locator(githubButton)).toContainText('Sign in with GitHub');
    await expect(student.locator('#login-form')).toBeVisible();
  });

  test('restricting to an organization and turning off password sign-in', async ({ page, context }) => {
    await page.locator('#github_allowed_orgs').fill('My-Org');
    await page.locator('#github_disable_classic_login').check();
    await page.locator('button[type="submit"]').click();

    await expect(page.locator('#github-auth-status')).toContainText('Only members of my-org');
    await expect(page.locator('#github_disable_classic_login')).toBeChecked();

    const student = await context.newPage();
    await student.goto('/student/login?error=GitHub+sign-in+was+cancelled.');
    await expect(student.locator(githubButton)).toBeVisible();
    await expect(student.locator('#login-form')).toHaveCount(0);
    // Sign-in errors must stay visible when the password form is hidden.
    await expect(student.locator('.error-alert')).toContainText('GitHub sign-in was cancelled.');
  });

  test('rejects an invalid organization name', async ({ page }) => {
    await page.locator('#github_allowed_orgs').fill('my-org/team');
    await page.locator('button[type="submit"]').click();
    await expect(page.locator('#save-response .error-message')).toContainText('not valid');
  });

  test('turning it off removes the button and restores the password form', async ({ page, context }) => {
    await page.locator('#github-auth-turn-off').click();
    await expect(page.locator('#github-auth-status')).toContainText('GitHub login is off');
    await expect(page.locator('#github_client_id')).toHaveValue('');

    const student = await context.newPage();
    await student.goto('/student/login');
    await expect(student.locator(githubButton)).toHaveCount(0);
    await expect(student.locator('#login-form')).toBeVisible();
  });
});
