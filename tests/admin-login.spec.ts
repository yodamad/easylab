import { test, expect } from '@playwright/test';

test.describe('Login Page', () => {
  test('should load login page correctly', async ({ page }) => {
    await page.goto('/login');

    // Check page title
    await expect(page).toHaveTitle(/EasyLab/);

    // Check main heading
    await expect(page.locator('h1')).toContainText('EasyLab');

    // Check subtitle
    await expect(page.locator('text=Enter password to continue')).toBeVisible();

    // Check password input exists
    const passwordInput = page.locator('input[type="password"]');
    await expect(passwordInput).toBeVisible();
    await expect(passwordInput).toHaveAttribute('placeholder', 'Enter admin password');

    // Check login button exists
    await expect(page.locator('button[type="submit"]')).toHaveText('Login');

    // Check security badge
    await expect(page.locator('text=Password is hashed before transmission')).toBeVisible();

    // Check footer text
    await expect(page.locator('text=Contact your administrator if you need access')).toBeVisible();
  });

  test('should show error on invalid password', async ({ page }) => {
    await page.goto('/login');

    // Enter wrong password
    await page.locator('input[type="password"]').fill('wrongpassword');

    // Click login button
    await page.locator('button[type="submit"]').click();

    // Wait for page to reload with error
    await page.waitForLoadState('networkidle');

    // Check if still on login page (authentication failed)
    await expect(page.locator('h1')).toContainText('EasyLab');
    await expect(page.locator('input[type="password"]')).toBeVisible();
  });

  test('should login successfully with correct password', async ({ page }) => {
    await page.goto('/login');

    // Enter correct password (testpassword as set in playwright.config.ts)
    await page.locator('input[type="password"]').fill('testpassword');

    // Click login button
    await page.locator('button[type="submit"]').click();

    // Wait for navigation
    await page.waitForLoadState('networkidle');

    // Should be redirected to admin page
    await expect(page).toHaveURL(/\/admin/);

    // Check admin page loaded
    await expect(page.locator('h1')).toContainText('EasyLab');
  });

  test('should logout successfully', async ({ page }) => {
    // First login
    await page.goto('/login');
    await page.locator('input[type="password"]').fill('testpassword');
    await page.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');

    // Should be on admin page
    await expect(page).toHaveURL(/\/admin/);

    // Click logout
    await page.locator('a[href="/logout"]').click();
    await page.waitForLoadState('networkidle');

    // Should be redirected to home page
    await expect(page).toHaveURL('/');
    await expect(page.locator('h1')).toContainText('EasyLab');
    // Check for the home page options (student and admin cards)
    await expect(page.locator('text=Student Space')).toBeVisible();
    await expect(page.locator('text=Admin Space')).toBeVisible();
  });
});

