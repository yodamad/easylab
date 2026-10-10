import { test, expect, Page } from '@playwright/test';

/**
 * Idle workspaces are paused, not deleted, and their owner resumes them.
 *
 * When a workspace is paused and what protects a student who is working is
 * decided by the server (see internal/server/idle_test.go). What is tested here
 * is the browser's side: the admin can set the timeout, and a student who finds
 * their workspace paused is told so and can bring it back. The student endpoints
 * are mocked: reaching the real thing needs a lab that has finished provisioning
 * against a cluster.
 */
const EMAIL = 'alice@example.com';

const PAUSED_WORKSPACE = {
  lab_id: 'lab-1',
  lab_name: 'devoxx',
  workspace_name: 'ws-alice-go',
  workspace_url: 'https://ws-alice-go.example.com/',
  email: EMAIL,
  template: 'go',
  created_at: '2026-10-06T09:00:00Z',
  deletion_at: '',
  ready: false,
  paused: true,
  teacher_accessed_at: '',
};

const RESUME_URL = '/api/student/workspace/resume?lab_id=lab-1&workspace_name=ws-alice-go';
const PAUSED_STATUS = `<div class="workspace-ready-status workspace-ready-status--paused" data-resume-url="${RESUME_URL.replace('&', '&amp;')}"><span>⏸ Workspace paused after inactivity. Your files are kept.</span><button type="button" class="btn-workspace-connect btn-workspace-resume">Resume</button></div>`;

async function studentLogin(page: Page) {
  await page.goto('/student/login');
  await page.locator('input[type="email"]').fill(EMAIL);
  await page.locator('input[type="password"]').fill('studentpass');
  await page.locator('button[type="submit"]').click();
  await page.waitForLoadState('networkidle');
}

test.describe('Paused workspaces: student side', () => {
  test('a paused workspace is flagged on its card', async ({ page }) => {
    await studentLogin(page);
    await page.route('**/api/student/workspaces', route =>
      route.fulfill({ json: { workspaces: [PAUSED_WORKSPACE, { ...PAUSED_WORKSPACE, lab_id: 'lab-2', workspace_name: 'ws-alice-python', paused: false, ready: true }], incomplete: false } }),
    );
    await page.goto('/student/workspaces');

    const cards = page.locator('.workspace-card');
    await expect(cards).toHaveCount(2);
    await expect(cards.nth(0).locator('.workspace-card-chip--paused')).toContainText('Paused');
    await expect(cards.nth(1).locator('.workspace-card-chip--paused')).toHaveCount(0);
  });

  test('opening a paused workspace resumes it instead of failing', async ({ page }) => {
    await studentLogin(page);
    await page.route('**/api/student/workspaces', route =>
      route.fulfill({ json: { workspaces: [PAUSED_WORKSPACE], incomplete: false } }),
    );
    await page.route('**/api/student/workspace/status**', route =>
      route.fulfill({ contentType: 'text/html', body: PAUSED_STATUS }),
    );
    let resumed: { method: string; url: string } | null = null;
    await page.route('**/api/student/workspace/resume**', route => {
      resumed = { method: route.request().method(), url: route.request().url() };
      return route.fulfill({
        contentType: 'text/html',
        body: '<div class="workspace-ready-status workspace-ready-status--starting"><span>Workspace is resuming, this may take a moment...</span></div>',
      });
    });
    const messages: string[] = [];
    page.on('dialog', dialog => {
      messages.push(dialog.message());
      return dialog.accept();
    });
    await page.goto('/student/workspaces');

    await page.locator('.workspace-card-open').first().click();

    await expect.poll(() => resumed).not.toBeNull();
    expect(resumed!.method).toBe('POST');
    expect(resumed!.url).toContain('workspace_name=ws-alice-go');
    await expect.poll(() => messages).toHaveLength(1);
    expect(messages[0]).toContain('paused after a period of inactivity');
    expect(messages[0]).toContain('all your files');
  });
});

test.describe('Paused workspaces: admin setting', () => {
  test('the wizard lifecycle step offers the idle timeout', async ({ page }) => {
    await page.goto('/login');
    await page.locator('input[type="password"]').fill('testpassword');
    await page.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');
    await page.goto('/admin');

    await page.evaluate(() => {
      (window as any).wizard.currentStep = 7;
      (window as any).wizard.updateUI();
    });

    const step = page.locator('.wizard-step[data-step="7"]');
    await expect(step).toBeVisible();
    await expect(step).toContainText('Workspace Lifetime');
    await expect(step).toContainText('Pause Idle Workspaces');
    const field = step.locator('#workspace_idle_timeout_minutes');
    await expect(field).toBeVisible();
    await expect(field).toHaveAttribute('name', 'workspace_idle_timeout_minutes');
    await field.fill('60');
    await expect(field).toHaveValue('60');
  });
});
