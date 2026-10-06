import { test, expect, Page } from '@playwright/test';

/**
 * My Workspaces follows the student's login, not the browser.
 *
 * The list comes from the server (GET /api/student/workspaces), so a student who
 * signs in on another laptop finds their workspaces there. Which workspaces a
 * student gets is decided by the server (see
 * internal/server/student_workspaces_test.go). What is tested here is that the
 * page needs nothing stored in the browser to show them, so the endpoint is
 * mocked: reaching the real thing needs a lab that has finished provisioning
 * against a cluster.
 */
const EMAIL = 'alice@example.com';

const WORKSPACES = [
  {
    lab_id: 'lab-1',
    lab_name: 'devoxx',
    workspace_name: 'ws-alice-go',
    workspace_url: 'https://ws-alice-go.example.com/',
    email: EMAIL,
    template: 'go',
    created_at: '2026-10-06T09:00:00Z',
    deletion_at: '2026-10-06T13:00:00Z',
    ready: true,
    teacher_accessed_at: '',
  },
  {
    lab_id: 'lab-2',
    lab_name: 'snowcamp',
    workspace_name: 'ws-alice-python',
    workspace_url: 'https://ws-alice-python.example.com/',
    email: EMAIL,
    template: 'python',
    created_at: '2026-10-05T09:00:00Z',
    deletion_at: '',
    ready: true,
    teacher_accessed_at: '',
  },
];

async function login(page: Page) {
  await page.goto('/student/login');
  await page.locator('input[type="email"]').fill(EMAIL);
  await page.locator('input[type="password"]').fill('studentpass');
  await page.locator('button[type="submit"]').click();
  await page.waitForLoadState('networkidle');
}

async function mockWorkspaces(page: Page, workspaces: object[], incomplete = false) {
  await page.route('**/api/student/workspaces', route =>
    route.fulfill({ json: { workspaces, incomplete } }),
  );
}

test.describe('My Workspaces: listed from the server', () => {
  test('lists the workspaces in a browser that never saved them', async ({ browser }) => {
    // A fresh context stands in for another laptop: no cookie from a previous visit.
    const context = await browser.newContext();
    const page = await context.newPage();
    await login(page);
    await mockWorkspaces(page, WORKSPACES);
    await page.goto('/student/workspaces');

    const cards = page.locator('.workspace-card');
    await expect(cards).toHaveCount(2);
    await expect(cards.nth(0)).toContainText('ws-alice-go');
    await expect(cards.nth(0)).toContainText('devoxx');
    await expect(cards.nth(0)).toContainText('Auto-deletes');
    await expect(cards.nth(1)).toContainText('ws-alice-python');
    await expect(cards.nth(1)).toContainText('snowcamp');
    await expect(page.locator('#workspaces-count')).toHaveText('2 workspaces');
    await expect(cards.nth(0).locator('.workspace-card-open')).toBeVisible();

    const saved = (await context.cookies()).filter(c => c.name.startsWith('workspace_info_'));
    expect(saved).toHaveLength(0);
    await context.close();
  });

  test('does not show a workspace password', async ({ page }) => {
    await login(page);
    await mockWorkspaces(page, WORKSPACES);
    await page.goto('/student/workspaces');

    const card = page.locator('.workspace-card').first();
    await card.locator('.workspace-card-header').click();
    await expect(card.locator('.workspace-card-details')).toContainText('Workspace URL');
    await expect(card).not.toContainText('Password');
    await expect(card).not.toContainText('Encrypt');
  });

  test('invites to request a workspace when there is none', async ({ page }) => {
    await login(page);
    await mockWorkspaces(page, []);
    await page.goto('/student/workspaces');

    await expect(page.locator('.student-empty-state')).toContainText('No workspaces yet');
    await expect(page.locator('#clear-all-btn')).toBeHidden();
  });

  test('says so when a lab could not be reached', async ({ page }) => {
    await login(page);
    await mockWorkspaces(page, [WORKSPACES[0]], true);
    await page.goto('/student/workspaces');

    await expect(page.locator('.workspace-card')).toHaveCount(1);
    await expect(page.locator('#workspaces-list-container .warning-message')).toContainText('could not be reached');
  });

  test('clearing a workspace deletes it and reloads the list', async ({ page }) => {
    await login(page);
    let remaining = [...WORKSPACES];
    await page.route('**/api/student/workspaces', route =>
      route.fulfill({ json: { workspaces: remaining, incomplete: false } }),
    );
    let deleted = '';
    await page.route('**/api/student/workspace/delete', route => {
      deleted = new URLSearchParams(route.request().postData() || '').get('workspace_name') || '';
      remaining = remaining.filter(ws => ws.workspace_name !== deleted);
      return route.fulfill({ json: { success: true, deleted: true } });
    });
    page.on('dialog', dialog => dialog.accept());
    await page.goto('/student/workspaces');

    const card = page.locator('.workspace-card').first();
    await card.locator('.workspace-card-header').click();
    await card.getByRole('button', { name: 'Clear' }).click();

    await expect(page.locator('.workspace-card')).toHaveCount(1);
    expect(deleted).toBe('ws-alice-go');
    await expect(page.locator('.workspace-card')).toContainText('ws-alice-python');
  });
});
