import { test, expect } from '@playwright/test';

test.describe('Admin End-to-End Flow', () => {
  test.beforeEach(async ({ page }) => {
    // Login before each test
    await page.goto('/login');
    await page.locator('input[type="password"]').fill('testpassword');
    await page.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');
  });

  test('should load admin wizard page', async ({ page }) => {
    await page.goto('/admin');

    // Check page title
    await expect(page).toHaveTitle(/Create New Lab/);

    // Check header
    await expect(page.locator('h1')).toContainText('EasyLab');
    await expect(page.locator('text=Create a new lab environment')).toBeVisible();

    // Check wizard progress steps
    await expect(page.locator('.progress-step .step-label:has-text("Pulumi")')).toBeVisible();
    await expect(page.locator('.progress-step .step-label:has-text("Network")')).toBeVisible();
    await expect(page.locator('.progress-step .step-label:has-text("Kubernetes")')).toBeVisible();
    await expect(page.locator('.progress-step .step-label:has-text("Node Pool")')).toBeVisible();
    await expect(page.locator('.progress-step .step-label:has-text("Coder")')).toBeVisible();
  });

  test('should show step 1 - Pulumi Configuration', async ({ page }) => {
    await page.goto('/admin');

    // Check step 1 is active
    await expect(page.locator('h2:has-text("Pulumi Configuration")')).toBeVisible();
    await expect(page.locator('text=Configure your Pulumi stack settings')).toBeVisible();

    // Check stack name field
    await expect(page.locator('label:has-text("Stack Name")')).toBeVisible();
    const stackInput = page.locator('input#stack_name');
    await expect(stackInput).toBeVisible();
    await expect(stackInput).toHaveValue('dev');

    // Check navigation
    await expect(page.locator('button:has-text("Previous")')).toBeDisabled();
    await expect(page.locator('button:has-text("Next")')).toBeVisible();
  });

  test('should navigate through wizard steps', async ({ page }) => {
    await page.goto('/admin');

    // Step 1 - Pulumi
    await expect(page.locator('h2:has-text("Pulumi Configuration")')).toBeVisible();
    await page.locator('button:has-text("Next")').click();

    // Step 2 - Network (fill required network_id field)
    await expect(page.locator('h2:has-text("Network Configuration")')).toBeVisible();
    await expect(page.locator('label:has-text("Gateway Name")')).toBeVisible();
    await page.locator('input#network_id').fill('test-network-id');
    await page.locator('button:has-text("Next")').click();

    // Step 3 - Kubernetes (wait for step to become visible)
    await page.waitForSelector('.wizard-step[data-step="3"].active', { timeout: 5000 });
    await expect(page.locator('h2:has-text("Kubernetes Configuration")')).toBeVisible();
    await expect(page.locator('label:has-text("Cluster Name")')).toBeVisible();
    await page.locator('button:has-text("Next")').click();

    // Step 4 - Node Pool
    await page.waitForSelector('.wizard-step[data-step="4"].active', { timeout: 5000 });
    await expect(page.locator('h2:has-text("Node Pool Configuration")')).toBeVisible();
    await expect(page.locator('label:has-text("Node Pool Name")')).toBeVisible();
    await page.locator('button:has-text("Next")').click();

    // Step 5 - Coder
    await page.waitForSelector('.wizard-step[data-step="5"].active', { timeout: 5000 });
    await expect(page.locator('h2:has-text("Coder Configuration")')).toBeVisible();
    await expect(page.locator('label:has-text("Admin Email")')).toBeVisible();
    await expect(page.locator('label:has-text("Admin Password")')).toBeVisible();
  });

  test('should go back through wizard steps', async ({ page }) => {
    await page.goto('/admin');

    // Go to step 2
    await page.locator('button:has-text("Next")').click();
    await expect(page.locator('h2:has-text("Network Configuration")')).toBeVisible();

    // Go back to step 1
    await page.locator('button:has-text("Previous")').click();
    await expect(page.locator('h2:has-text("Pulumi Configuration")')).toBeVisible();
  });

  test('should validate required fields in wizard', async ({ page }) => {
    await page.goto('/admin');

    // Clear the stack name (required field)
    const stackInput = page.locator('input#stack_name');
    await stackInput.clear();

    // Try to go to next step
    await page.locator('button:has-text("Next")').click();

    // Should still be on step 1 due to validation
    await expect(page.locator('h2:has-text("Pulumi Configuration")')).toBeVisible();
  });

  test('should maintain form validation across wizard steps', async ({ page }) => {
    await page.goto('/admin');

    // Fill step 1
    await page.locator('input#stack_name').fill('test-stack');
    await page.locator('button:has-text("Next")').click();

    // Fill step 2 (need to fill required network_id)
    await expect(page.locator('h2:has-text("Network Configuration")')).toBeVisible();
    await page.locator('input#network_id').fill('test-network-id');
    await page.locator('button:has-text("Next")').click();

    // Fill step 3
    await page.waitForSelector('.wizard-step[data-step="3"].active', { timeout: 5000 });
    await expect(page.locator('h2:has-text("Kubernetes Configuration")')).toBeVisible();
    await page.locator('button:has-text("Next")').click();

    // Fill step 4
    await page.waitForSelector('.wizard-step[data-step="4"].active', { timeout: 5000 });
    await expect(page.locator('h2:has-text("Node Pool Configuration")')).toBeVisible();
    await page.locator('button:has-text("Next")').click();

    // Step 5 - should show Dry Run and Create Lab buttons
    await page.waitForSelector('.wizard-step[data-step="5"].active', { timeout: 5000 });
    await expect(page.locator('h2:has-text("Coder Configuration")')).toBeVisible();
    await expect(page.locator('button:has-text("Dry Run")')).toBeVisible();
    await expect(page.locator('button:has-text("Create Lab")')).toBeVisible();
  });

  test('should maintain session across page navigations', async ({ page }) => {
    await page.goto('/admin');
    await expect(page.locator('h1')).toContainText('EasyLab');

    // Navigate to jobs list
    await page.locator('a[href="/jobs"]').click();
    await page.waitForLoadState('networkidle');
    await expect(page).toHaveURL(/\/jobs/);

    // Navigate back to admin
    await page.locator('a[href="/admin"]').first().click();
    await page.waitForLoadState('networkidle');

    // Should still be logged in
    await expect(page).toHaveURL(/\/admin/);
    await expect(page.locator('h1')).toContainText('EasyLab');
  });

  test('should update resource names based on stack name', async ({ page }) => {
    await page.goto('/admin');

    // Change stack name
    const stackInput = page.locator('input#stack_name');
    await stackInput.clear();
    await stackInput.fill('mystack');

    // Go to step 2 - Network
    await page.locator('button:has-text("Next")').click();

    // Check that gateway name was updated with prefix
    const gatewayInput = page.locator('input#network_gateway_name');
    await expect(gatewayInput).toHaveValue(/mystack-/);

    // Check that network name was updated with prefix
    const networkInput = page.locator('input#network_private_network_name');
    await expect(networkInput).toHaveValue(/mystack-/);
  });

  test('should calculate network IPs from mask', async ({ page }) => {
    await page.goto('/admin');

    // Go to step 2 - Network
    await page.locator('button:has-text("Next")').click();
    await expect(page.locator('h2:has-text("Network Configuration")')).toBeVisible();

    // Check default mask value
    const maskInput = page.locator('input#network_mask');
    await expect(maskInput).toHaveValue('10.0.0.0/24');

    // Check calculated IPs are displayed
    await expect(page.locator('text=10.0.0.100')).toBeVisible();
    await expect(page.locator('text=10.0.0.254')).toBeVisible();

    // Change mask and check IPs update
    await maskInput.clear();
    await maskInput.fill('192.168.1.0/24');

    // Trigger calculation
    await maskInput.blur();

    // Check new calculated IPs
    await expect(page.locator('#calculated-start-ip')).toContainText('192.168.1.100');
    await expect(page.locator('#calculated-end-ip')).toContainText('192.168.1.254');
  });
});

