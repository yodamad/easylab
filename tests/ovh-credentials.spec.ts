import { test, expect } from '@playwright/test';

test.describe('OVH Credentials Page', () => {
  test.beforeEach(async ({ page }) => {
    // Login as admin before each test
    await page.goto('/login');
    await page.locator('input[type="password"]').fill('testpassword');
    await page.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');
  });

  test('should load credentials page correctly', async ({ page }) => {
    await page.goto('/ovh-credentials');

    // Check page title
    await expect(page).toHaveTitle(/OVH Credentials/);

    // Check header
    await expect(page.locator('h1')).toContainText('OVH Credentials Configuration');
    await expect(page.locator('text=Configure your OVHcloud API credentials')).toBeVisible();

    // Check info box
    await expect(page.locator('text=About OVH Credentials')).toBeVisible();
    await expect(page.locator('text=Update Existing Credentials')).toBeVisible();
    await expect(page.locator('text=Credentials are currently configured')).toBeVisible();

    // Check form section header
    await expect(page.locator('h2:has-text("OVH API Credentials")')).toBeVisible();
  });

  test('should display all form fields', async ({ page }) => {
    await page.goto('/ovh-credentials');

    // Check Application Key field
    await expect(page.locator('label:has-text("Application Key")')).toBeVisible();
    await expect(page.locator('input#ovh_application_key')).toBeVisible();
    await expect(page.locator('input#ovh_application_key')).toHaveAttribute('type', 'password');

    // Check Application Secret field
    await expect(page.locator('label:has-text("Application Secret")')).toBeVisible();
    await expect(page.locator('input#ovh_application_secret')).toBeVisible();

    // Check Consumer Key field
    await expect(page.locator('label:has-text("Consumer Key")')).toBeVisible();
    await expect(page.locator('input#ovh_consumer_key')).toBeVisible();

    // Check Service Name field
    await expect(page.locator('label:has-text("Service Name")')).toBeVisible();
    await expect(page.locator('input#ovh_service_name')).toBeVisible();
    await expect(page.locator('input#ovh_service_name')).toHaveAttribute('type', 'text');

    // Check Endpoint selector
    await expect(page.locator('label:has-text("OVH Endpoint")')).toBeVisible();
    await expect(page.locator('select#ovh_endpoint')).toBeVisible();
  });

  test('should have endpoint options', async ({ page }) => {
    await page.goto('/ovh-credentials');

    const endpointSelect = page.locator('select#ovh_endpoint');
    
    // Check that endpoint options exist (use toHaveCount instead of toBeVisible for options)
    await expect(endpointSelect.locator('option[value="ovh-eu"]')).toHaveCount(1);
    await expect(endpointSelect.locator('option[value="ovh-us"]')).toHaveCount(1);
    await expect(endpointSelect.locator('option[value="ovh-ca"]')).toHaveCount(1);
  });

  test('should display navigation links', async ({ page }) => {
    await page.goto('/ovh-credentials');

    // Check navigation links
    await expect(page.locator('a[href="/jobs"]')).toBeVisible();
    await expect(page.locator('a[href="/admin"]')).toBeVisible();
    await expect(page.locator('a[href="/logout"]')).toBeVisible();
  });

  test('should navigate back to admin page', async ({ page }) => {
    await page.goto('/ovh-credentials');

    // Click "Back to Labs" button
    await page.locator('a[href="/admin"]:has-text("Back to Labs")').click();
    await page.waitForLoadState('networkidle');

    // Should be on admin page
    await expect(page).toHaveURL(/\/admin/);
  });

  test('should navigate to jobs page', async ({ page }) => {
    await page.goto('/ovh-credentials');

    // Click Jobs link
    await page.locator('a[href="/jobs"]').click();
    await page.waitForLoadState('networkidle');

    // Should be on jobs page
    await expect(page).toHaveURL(/\/jobs/);
  });

  test('should have Save Credentials and Check Status buttons', async ({ page }) => {
    await page.goto('/ovh-credentials');

    // Check Save Credentials button
    await expect(page.locator('button[type="submit"]:has-text("Save Credentials")')).toBeVisible();

    // Check Check Status button
    await expect(page.locator('button#btn-check:has-text("Check Status")')).toBeVisible();
  });

  test('should show status when Check Status is clicked', async ({ page }) => {
    await page.goto('/ovh-credentials');

    // Click Check Status button
    await page.locator('button#btn-check').click();

    // Wait for status container to appear
    await expect(page.locator('#status-container')).toBeVisible();

    // Should show either configured or not configured status
    await expect(page.locator('#status-content')).toContainText(/Credentials Configured|No Credentials Configured/);
  });

  test('should validate required fields on form submission', async ({ page }) => {
    await page.goto('/ovh-credentials');

    // Try to submit empty form (click submit)
    await page.locator('button[type="submit"]').click();

    // Should still be on credentials page (HTML5 validation prevents submission)
    await expect(page).toHaveURL(/\/ovh-credentials/);
  });

  test('should save credentials successfully with all fields', async ({ page }) => {
    await page.goto('/ovh-credentials');

    // Fill in all required fields
    await page.locator('input#ovh_application_key').fill('test-app-key-12345');
    await page.locator('input#ovh_application_secret').fill('test-app-secret-12345');
    await page.locator('input#ovh_consumer_key').fill('test-consumer-key-12345');
    await page.locator('input#ovh_service_name').fill('test-service-name');
    await page.locator('select#ovh_endpoint').selectOption('ovh-eu');

    // Submit the form
    await page.locator('button[type="submit"]').click();

    // Wait for response
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(1000);

    // Should show success message
    await expect(page.locator('#form-response')).toContainText(/success|saved/i);
  });

  test('should show configured status after saving credentials', async ({ page }) => {
    await page.goto('/ovh-credentials');

    // First save credentials
    await page.locator('input#ovh_application_key').fill('test-app-key-check');
    await page.locator('input#ovh_application_secret').fill('test-app-secret-check');
    await page.locator('input#ovh_consumer_key').fill('test-consumer-key-check');
    await page.locator('input#ovh_service_name').fill('test-service-check');
    await page.locator('select#ovh_endpoint').selectOption('ovh-eu');
    await page.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');
    await page.waitForTimeout(500);

    // Click Check Status
    await page.locator('button#btn-check').click();
    await page.waitForTimeout(500);

    // Should show configured status with service name
    await expect(page.locator('#status-content')).toContainText(/Credentials Configured|test-service-check/);
  });
});

