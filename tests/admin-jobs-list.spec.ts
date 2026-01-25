import { test, expect } from '@playwright/test';

test.describe('Jobs List Page', () => {
  test.beforeEach(async ({ page }) => {
    // Login before each test
    await page.goto('/login');
    await page.locator('input[type="password"]').fill('testpassword');
    await page.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');
  });

  test('should load jobs list page', async ({ page }) => {
    await page.goto('/jobs');

    // Check page title
    await expect(page).toHaveTitle(/Jobs List/);

    // Check header
    await expect(page.locator('h1')).toContainText('EasyLab');
    await expect(page.locator('text=Jobs History')).toBeVisible();

    // Check navigation links in header
    await expect(page.locator('.header-actions a[href="/admin"]')).toBeVisible();
    await expect(page.locator('.header-actions a[href="/ovh-credentials"]')).toBeVisible();
    await expect(page.locator('.header-actions a[href="/logout"]')).toBeVisible();
  });

  test('should show empty state when no jobs', async ({ page }) => {
    await page.goto('/jobs');

    // Check empty state
    await expect(page.locator('h2:has-text("No Jobs Yet")')).toBeVisible();
    await expect(page.locator('text=You haven\'t created any labs yet')).toBeVisible();

    // Check create new lab button
    const createButton = page.locator('a[href="/admin"]:has-text("Create New Lab")');
    await expect(createButton).toBeVisible();
  });

  test('should navigate to admin page from empty state', async ({ page }) => {
    await page.goto('/jobs');

    // Click create new lab button
    await page.locator('a[href="/admin"]:has-text("Create New Lab")').click();
    await page.waitForLoadState('networkidle');

    // Should be on admin page
    await expect(page).toHaveURL(/\/admin/);
    await expect(page.locator('h1')).toContainText('EasyLab');
  });

  test('should navigate to new lab from header', async ({ page }) => {
    await page.goto('/jobs');

    // Click "New Lab" in header
    await page.locator('.header-actions a[href="/admin"]').click();
    await page.waitForLoadState('networkidle');

    // Should be on admin page
    await expect(page).toHaveURL(/\/admin/);
  });

  test('should navigate to credentials page', async ({ page }) => {
    await page.goto('/jobs');

    // Click credentials link
    await page.locator('a[href="/ovh-credentials"]').click();
    await page.waitForLoadState('networkidle');

    // Should be on credentials page
    await expect(page).toHaveURL(/\/ovh-credentials/);
  });
});

