import { test, expect } from '@playwright/test';

/**
 * GitLab student login, as configured from the admin area (/admin/gitlab).
 *
 * These tests cover what an admin controls: the GitLab instance URL, turning
 * the sign-in button on, restricting it, and turning it off again. The OAuth
 * round trip itself needs a GitLab instance, so it is covered server-side
 * against a fake one in internal/server/gitlab_auth_test.go.
 *
 * The settings are persisted by the server, so the tests run in order and the
 * last one leaves GitLab login off for the other specs.
 */
test.describe.configure({ mode: 'serial' });

test.describe('GitLab login settings', () => {
  const gitlabButton = 'a[href="/student/auth/gitlab/login"]';

  test.beforeEach(async ({ page }) => {
    await page.goto('/login');
    await page.locator('input[type="password"]').fill('testpassword');
    await page.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');
    await page.goto('/admin/gitlab');
  });

  test('is off by default, on gitlab.com, and shows the callback URL to register', async ({ page, context }) => {
    await expect(page.locator('#gitlab-auth-status')).toContainText('GitLab login is off');
    await expect(page.locator('#gitlab_url')).toHaveValue('https://gitlab.com');
    await expect(page.locator('#gitlab_callback_url')).toHaveValue(/\/student\/auth\/gitlab\/callback$/);
    await expect(page.locator('#gitlab-auth-turn-off')).toHaveCount(0);

    const student = await context.newPage();
    await student.goto('/student/login');
    await expect(student.locator(gitlabButton)).toHaveCount(0);
  });

  test('asks for the secret before turning on', async ({ page }) => {
    await page.locator('#gitlab_client_id').fill('test-application-id');
    await page.locator('button[type="submit"]').click();
    await expect(page.locator('#save-response .error-message')).toContainText('Enter the secret');
  });

  test('turning it on for gitlab.com adds the sign-in button and warns that anyone can sign in', async ({ page, context }) => {
    await page.locator('#gitlab_client_id').fill('test-application-id');
    await page.locator('#gitlab_client_secret').fill('test-application-secret');
    await page.locator('button[type="submit"]').click();

    await expect(page.locator('#gitlab-auth-status')).toContainText('any account on gitlab.com');
    await expect(page.locator('#gitlab-auth-status')).toHaveClass(/warning-message/);
    // The saved secret is never sent back to the browser.
    await expect(page.locator('#gitlab_client_secret')).toHaveValue('');
    expect(await page.content()).not.toContain('test-application-secret');

    const student = await context.newPage();
    await student.goto('/student/login');
    await expect(student.locator(gitlabButton)).toContainText('Sign in with GitLab');
    await expect(student.locator('#login-form')).toBeVisible();
  });

  test('pointing at a self-managed instance, restricted to a group', async ({ page, context }) => {
    await page.locator('#gitlab_url').fill('https://GitLab.example.org/');
    await page.locator('#gitlab_allowed_groups').fill('My-Group/Workshop');
    await page.locator('#gitlab_disable_classic_login').check();
    await page.locator('button[type="submit"]').click();

    await expect(page.locator('#gitlab-auth-status')).toContainText('on for gitlab.example.org');
    await expect(page.locator('#gitlab-auth-status')).toContainText('Only members of my-group/workshop');
    await expect(page.locator('#gitlab_url')).toHaveValue('https://gitlab.example.org');

    // The sign-in button now sends students to that instance.
    const response = await context.request.get('/student/auth/gitlab/login', { maxRedirects: 0 });
    expect(response.status()).toBe(302);
    expect(response.headers()['location']).toMatch(/^https:\/\/gitlab\.example\.org\/oauth\/authorize\?/);

    const student = await context.newPage();
    await student.goto('/student/login?error=GitLab+sign-in+was+cancelled.');
    await expect(student.locator(gitlabButton)).toBeVisible();
    await expect(student.locator('#login-form')).toHaveCount(0);
    await expect(student.locator('.error-alert')).toContainText('GitLab sign-in was cancelled.');
  });

  test('rejects a URL that is not an http(s) address, keeping the saved one', async ({ page }) => {
    await page.locator('#gitlab_url').fill('ftp://gitlab.example.org');
    await page.locator('button[type="submit"]').click();
    await expect(page.locator('#save-response .error-message')).toContainText('GitLab URL is not valid');

    await page.reload();
    await expect(page.locator('#gitlab_url')).toHaveValue('https://gitlab.example.org');
  });

  test('warns when the instance is reached over http', async ({ page }) => {
    await page.locator('#gitlab_url').fill('http://gitlab.internal');
    await page.locator('button[type="submit"]').click();
    await expect(page.locator('#gitlab-auth-insecure')).toContainText('not encrypted');
  });

  test('turning it off removes the button and restores the password form', async ({ page, context }) => {
    await page.locator('#gitlab-auth-turn-off').click();
    await expect(page.locator('#gitlab-auth-status')).toContainText('GitLab login is off');
    await expect(page.locator('#gitlab_client_id')).toHaveValue('');
    await expect(page.locator('#gitlab_url')).toHaveValue('https://gitlab.com');

    const student = await context.newPage();
    await student.goto('/student/login');
    await expect(student.locator(gitlabButton)).toHaveCount(0);
    await expect(student.locator('#login-form')).toBeVisible();
  });
});
