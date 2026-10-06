import { test, expect, Page } from '@playwright/test';

/**
 * Teacher-access note on the My Workspaces page.
 *
 * When an admin opens a student's workspace, the student is told on the
 * workspace's card. Whether an access is reported, and to whom, is decided by the
 * server (see internal/server/student_workspaces_test.go). What is tested here is
 * how the card presents it, so the workspaces endpoint is mocked: reaching the
 * real thing needs a lab that has finished provisioning against a cluster.
 */
const EMAIL = 'alice@example.com';
const WORKSPACE = 'ws-alice-1a2b3c4d';

async function mockWorkspaces(page: Page, teacherAccessedAt: string) {
  await page.route('**/api/student/workspaces', route =>
    route.fulfill({
      json: {
        workspaces: [
          {
            lab_id: 'lab-1',
            lab_name: 'devoxx',
            workspace_name: WORKSPACE,
            workspace_url: `https://${WORKSPACE}.example.com/`,
            email: EMAIL,
            created_at: '2026-10-06T09:00:00Z',
            deletion_at: '',
            ready: true,
            teacher_accessed_at: teacherAccessedAt,
          },
        ],
        incomplete: false,
      },
    }),
  );
}

test.describe('My Workspaces: teacher access note', () => {
  test.beforeEach(async ({ page }) => {
    await page.goto('/student/login');
    await page.locator('input[type="email"]').fill(EMAIL);
    await page.locator('input[type="password"]').fill('studentpass');
    await page.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');
  });

  test('tells the student when a teacher opened their workspace', async ({ page }) => {
    await mockWorkspaces(page, '2026-10-06T14:32:00Z');
    await page.goto('/student/workspaces');

    const note = page.locator('.workspace-card .workspace-teacher-access-note');
    await expect(note).toBeVisible();
    await expect(note).toContainText('A teacher opened this workspace on');
    await expect(note).toContainText('2026');
  });

  test('shows nothing for a workspace no teacher opened', async ({ page }) => {
    await mockWorkspaces(page, '');
    await page.goto('/student/workspaces');

    await expect(page.locator('.workspace-card')).toHaveCount(1);
    await expect(page.locator('.workspace-teacher-access-note')).toHaveCount(0);
  });
});
