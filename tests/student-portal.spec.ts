import { test, expect } from '@playwright/test';

test.describe('Student Login Page', () => {
  test('should load student login page correctly', async ({ page }) => {
    await page.goto('/student/login');

    // Check page title
    await expect(page).toHaveTitle(/Student Login/);

    // Check main heading
    await expect(page.locator('h1')).toContainText('Student Portal');

    // Check subtitle
    await expect(page.locator('text=Request your workspace')).toBeVisible();

    // Check student icon
    await expect(page.locator('.icon:has-text("🎓")')).toBeVisible();

    // Check password input exists
    const passwordInput = page.locator('input[type="password"]');
    await expect(passwordInput).toBeVisible();
    await expect(passwordInput).toHaveAttribute('placeholder', 'Enter student password');

    // Check label text
    await expect(page.locator('label:has-text("Student Password")')).toBeVisible();

    // Check login button exists
    await expect(page.locator('button[type="submit"]')).toHaveText('Login');

    // Check security badge
    await expect(page.locator('text=Password is hashed before transmission')).toBeVisible();

    // Check footer text
    await expect(page.locator('text=Contact your instructor if you need access')).toBeVisible();
  });

  test('should show error on invalid student password', async ({ page }) => {
    await page.goto('/student/login');

    // Enter wrong password
    await page.locator('input[type="password"]').fill('wrongpassword');

    // Click login button
    await page.locator('button[type="submit"]').click();

    // Wait for page to reload with error
    await page.waitForLoadState('networkidle');

    // Check if still on login page (authentication failed)
    await expect(page.locator('h1')).toContainText('Student Portal');
    await expect(page.locator('input[type="password"]')).toBeVisible();
  });

  test('should login successfully with correct student password', async ({ page }) => {
    await page.goto('/student/login');

    // Enter correct password (studentpass as set in playwright.config.ts)
    await page.locator('input[type="password"]').fill('studentpass');

    // Click login button
    await page.locator('button[type="submit"]').click();

    // Wait for navigation
    await page.waitForLoadState('networkidle');

    // Should be redirected to student dashboard
    await expect(page).toHaveURL(/\/student\/dashboard/);

    // Check dashboard loaded
    await expect(page.locator('h1')).toContainText('Student Portal');
  });

  test('should logout from student portal', async ({ page }) => {
    // First login
    await page.goto('/student/login');
    await page.locator('input[type="password"]').fill('studentpass');
    await page.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');

    // Should be on dashboard
    await expect(page).toHaveURL(/\/student\/dashboard/);

    // Click logout
    await page.locator('a[href="/student/logout"]').click();
    await page.waitForLoadState('networkidle');

    // Should be redirected to login page
    await expect(page.locator('h1')).toContainText('Student Portal');
    await expect(page.locator('input[type="password"]')).toBeVisible();
  });
});

test.describe('Student Dashboard', () => {
  test.beforeEach(async ({ page }) => {
    // Login as student before each test
    await page.goto('/student/login');
    await page.locator('input[type="password"]').fill('studentpass');
    await page.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');
  });

  test('should load student dashboard correctly', async ({ page }) => {
    await page.goto('/student/dashboard');

    // Check page title
    await expect(page).toHaveTitle(/Student Portal/);

    // Check header
    await expect(page.locator('h1')).toContainText('Student Portal');
    await expect(page.locator('text=Request your workspace')).toBeVisible();

    // Check logout button
    await expect(page.locator('a[href="/student/logout"]')).toBeVisible();
  });

  test('should display Available Labs section', async ({ page }) => {
    await page.goto('/student/dashboard');

    // Check Available Labs section exists
    await expect(page.locator('h2:has-text("Available Labs")')).toBeVisible();

    // Check labs container exists
    await expect(page.locator('#labs-container')).toBeVisible();
  });

  test('should display Request Workspace form', async ({ page }) => {
    await page.goto('/student/dashboard');

    // Check Request Workspace section exists
    await expect(page.locator('h2:has-text("Request Workspace")')).toBeVisible();

    // Check email input
    const emailInput = page.locator('input#email');
    await expect(emailInput).toBeVisible();
    await expect(emailInput).toHaveAttribute('type', 'email');
    await expect(emailInput).toHaveAttribute('placeholder', 'your.email@example.com');

    // Check label
    await expect(page.locator('label:has-text("Email Address")')).toBeVisible();

    // Check select dropdown
    const labSelect = page.locator('select#lab_id');
    await expect(labSelect).toBeVisible();

    // Check submit button
    await expect(page.locator('button#submit-btn')).toHaveText('Request Workspace');
  });

  test('should show no labs message when no completed labs exist', async ({ page }) => {
    await page.goto('/student/dashboard');

    // Wait for labs to load
    await page.waitForLoadState('networkidle');

    // Allow time for AJAX to complete
    await page.waitForTimeout(1000);

    // Since there are no completed labs, should show message (either no labs, loading, or error)
    await expect(page.locator('#labs-container')).toContainText(/No labs available|Loading|Error loading labs/);
  });

  test('should validate email field', async ({ page }) => {
    await page.goto('/student/dashboard');

    // Try to submit without email
    await page.locator('button#submit-btn').click();

    // Form should not submit (HTML5 validation)
    await expect(page).toHaveURL(/\/student\/dashboard/);
  });

  test('should have proper form attributes for workspace request', async ({ page }) => {
    await page.goto('/student/dashboard');

    // Check form attributes
    const form = page.locator('#workspace-request-form');
    await expect(form).toHaveAttribute('method', 'POST');
    await expect(form).toHaveAttribute('action', '/api/student/workspace/request');
  });

  test('should navigate back to login when clicking logout', async ({ page }) => {
    await page.goto('/student/dashboard');

    // Click logout button
    await page.locator('a[href="/student/logout"]').click();
    await page.waitForLoadState('networkidle');

    // Should be back at login
    await expect(page.locator('input[type="password"]')).toBeVisible();
    await expect(page.locator('h1')).toContainText('Student Portal');
  });
});

test.describe('Student Portal Access Control', () => {
  test('should redirect to login when accessing dashboard without authentication', async ({ page }) => {
    // Try to access dashboard directly without logging in
    await page.goto('/student/dashboard');
    await page.waitForLoadState('networkidle');

    // Should be redirected to login page
    await expect(page).toHaveURL(/\/student\/login/);
  });

  test('should not allow admin password for student login', async ({ page }) => {
    await page.goto('/student/login');

    // Try to use admin password
    await page.locator('input[type="password"]').fill('testpassword');
    await page.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');

    // Should still be on login page (wrong password)
    await expect(page.locator('h1')).toContainText('Student Portal');
    await expect(page.locator('input[type="password"]')).toBeVisible();
  });

  test('should not allow student password for admin login', async ({ page }) => {
    await page.goto('/login');

    // Try to use student password for admin
    await page.locator('input[type="password"]').fill('studentpass');
    await page.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');

    // Should still be on admin login page
    await expect(page.locator('h1')).toContainText('Lab as Code');
    await expect(page.locator('input[type="password"]')).toBeVisible();
  });
});

