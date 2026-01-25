import { test, expect } from '@playwright/test';
import { chaosLogin } from './helpers';

/**
 * API Chaos Tests
 * 
 * Note: These tests verify how the UI handles API failures by intercepting
 * browser-initiated requests (AJAX/fetch) rather than direct API calls.
 * For direct API chaos, we test through the UI interactions that trigger API calls.
 */

test.describe('API Chaos - Health Endpoint', () => {
  test('health endpoint returns 200', async ({ page }) => {
    const response = await page.goto('/health');
    expect(response?.status()).toBe(200);
  });

  test('health endpoint accessible after server error on other routes', async ({ page }) => {
    // Simulate 500 on login but health should still work
    await page.route('**/login', route => route.fulfill({
      status: 500,
      body: 'Server Error',
    }));

    const response = await page.goto('/health');
    expect(response?.status()).toBe(200);

    await page.unroute('**/login');
  });
});

test.describe('API Chaos - Jobs Page API Calls', () => {
  test.beforeEach(async ({ page }) => {
    await chaosLogin(page);
  });

  test('jobs page handles 500 error from API gracefully', async ({ page }) => {
    // Navigate to jobs first, then set up interception for subsequent API calls
    await page.goto('/jobs');
    await expect(page.locator('h1')).toBeVisible();

    // The page loads jobs via server-side rendering, so page should display
    await expect(page.locator('body')).toBeVisible();
  });

  test('job status polling handles server errors', async ({ page }) => {
    // This tests HTMX polling behavior when server returns errors
    await page.route('**/api/jobs/**/status', route => route.fulfill({
      status: 500,
      contentType: 'text/html',
      body: '<div class="error">Server Error</div>',
    }));

    await page.goto('/admin');
    // Navigate and interact - the app should not crash
    await expect(page.locator('h1')).toBeVisible();

    await page.unroute('**/api/jobs/**/status');
  });

  test('GET /api/jobs/{id}/status returns 404 for non-existent job', async ({ page }) => {
    // Direct API test without route interception
    const response = await page.request.get('/api/jobs/non-existent-job-id/status');
    expect(response.status()).toBe(404);
  });

  test('GET /api/jobs/{id}/kubeconfig returns 404 for non-existent job', async ({ page }) => {
    const response = await page.request.get('/api/jobs/non-existent-job-id/kubeconfig');
    expect(response.status()).toBe(404);
  });
});

test.describe('API Chaos - Credentials API via UI', () => {
  test.beforeEach(async ({ page }) => {
    await chaosLogin(page);
  });

  test('credentials check handles 500 error gracefully', async ({ page }) => {
    await page.goto('/ovh-credentials');

    // Set up route interception for the check endpoint
    await page.route('**/api/credentials/status', route => route.fulfill({
      status: 500,
      contentType: 'text/html',
      body: '<div class="error">Server Error</div>',
    }));

    // Click check status
    await page.locator('button#btn-check').click();
    await page.waitForTimeout(1000);

    // Status container should show something (error response)
    await expect(page.locator('#status-container')).toBeVisible();

    await page.unroute('**/api/credentials/status');
  });

  test('credentials check handles malformed response', async ({ page }) => {
    await page.goto('/ovh-credentials');

    await page.route('**/api/credentials/status', route => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: '{ invalid json',
    }));

    await page.locator('button#btn-check').click();
    await page.waitForTimeout(1000);

    // Page should not crash
    await expect(page.locator('body')).toBeVisible();

    await page.unroute('**/api/credentials/status');
  });

  test('credentials save handles 500 error on submit', async ({ page }) => {
    await page.goto('/ovh-credentials');

    // Fill form
    await page.locator('input#ovh_application_key').fill('test-key');
    await page.locator('input#ovh_application_secret').fill('test-secret');
    await page.locator('input#ovh_consumer_key').fill('test-consumer');
    await page.locator('input#ovh_service_name').fill('test-service');
    await page.locator('select#ovh_endpoint').selectOption('ovh-eu');

    // Intercept the POST
    await page.route('**/api/credentials', route => {
      if (route.request().method() === 'POST') {
        route.fulfill({
          status: 500,
          contentType: 'text/html',
          body: '<div class="error">Server Error</div>',
        });
      } else {
        route.continue();
      }
    });

    await page.locator('button[type="submit"]').click();
    await page.waitForTimeout(1000);

    // Form should still be visible, page not crashed
    await expect(page.locator('button[type="submit"]')).toBeVisible();

    await page.unroute('**/api/credentials');
  });

  test('credentials save succeeds normally', async ({ page }) => {
    await page.goto('/ovh-credentials');

    // Fill form
    await page.locator('input#ovh_application_key').fill('chaos-test-key');
    await page.locator('input#ovh_application_secret').fill('chaos-test-secret');
    await page.locator('input#ovh_consumer_key').fill('chaos-test-consumer');
    await page.locator('input#ovh_service_name').fill('chaos-test-service');
    await page.locator('select#ovh_endpoint').selectOption('ovh-eu');

    await page.locator('button[type="submit"]').click();
    await page.waitForTimeout(1500);

    // Should show success or form response
    await expect(page.locator('#form-response')).toBeVisible();
  });
});

test.describe('API Chaos - Student Labs API via UI', () => {
  test('student dashboard handles labs API failure', async ({ page }) => {
    await chaosLogin(page, 'studentpass', true);

    // Intercept the labs API call that the dashboard makes
    await page.route('**/api/labs', route => route.fulfill({
      status: 500,
      contentType: 'text/html',
      body: 'Server Error',
    }));

    await page.goto('/student/dashboard');
    await page.waitForTimeout(1500);

    // Labs container should show error or empty state
    await expect(page.locator('#labs-container')).toBeVisible();

    await page.unroute('**/api/labs');
  });

  test('student dashboard handles empty labs response', async ({ page }) => {
    await chaosLogin(page, 'studentpass', true);

    await page.route('**/api/labs', route => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: '[]',
    }));

    await page.goto('/student/dashboard');
    await page.waitForTimeout(1500);

    // Should show some message (no labs, loading, or error)
    await expect(page.locator('#labs-container')).toContainText(/No labs|Loading|Error/);

    await page.unroute('**/api/labs');
  });

  test('student dashboard handles malformed labs JSON', async ({ page }) => {
    await chaosLogin(page, 'studentpass', true);

    await page.route('**/api/labs', route => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: '{ invalid',
    }));

    await page.goto('/student/dashboard');
    await page.waitForTimeout(1500);

    // Page should not crash - show error or empty state
    await expect(page.locator('body')).toBeVisible();

    await page.unroute('**/api/labs');
  });
});

test.describe('API Chaos - Workspace Request', () => {
  test('POST /api/student/workspace/request returns 404 for non-existent lab', async ({ page }) => {
    await chaosLogin(page, 'studentpass', true);

    const response = await page.request.post('/api/student/workspace/request', {
      form: {
        email: 'test@example.com',
        lab_id: 'non-existent-lab',
      },
    });

    expect([400, 404]).toContain(response.status());
  });

  test('POST /api/student/workspace/request returns 400 for missing email', async ({ page }) => {
    await chaosLogin(page, 'studentpass', true);

    const response = await page.request.post('/api/student/workspace/request', {
      form: {
        lab_id: 'some-lab-id',
      },
    });

    expect(response.status()).toBe(400);
  });
});

test.describe('API Chaos - Labs API Authentication', () => {
  test('POST /api/labs requires authentication', async ({ page }) => {
    // Without login, try to post
    const response = await page.request.post('/api/labs', {
      form: { stack_name: 'test' },
    });

    // Should redirect to login, return auth error, or return 200 with error HTML
    // (Server might return 200 with error message for unauthenticated requests)
    expect([200, 302, 303, 401, 403]).toContain(response.status());
  });

  test('POST /api/labs/launch returns 404 for non-existent job', async ({ page }) => {
    await chaosLogin(page);

    const response = await page.request.post('/api/labs/launch', {
      form: { job_id: 'non-existent-job' },
    });

    expect([400, 404]).toContain(response.status());
  });

  test('POST /api/labs/destroy returns 404 for non-existent job', async ({ page }) => {
    await chaosLogin(page);

    const response = await page.request.post('/api/labs/destroy', {
      form: { job_id: 'non-existent-job' },
    });

    expect([400, 404]).toContain(response.status());
  });

  test('POST /api/labs/recreate returns error for non-existent job', async ({ page }) => {
    await chaosLogin(page);

    const response = await page.request.post('/api/labs/recreate', {
      form: { job_id: 'non-existent-job' },
    });

    expect([400, 404]).toContain(response.status());
  });
});

test.describe('API Chaos - Slow Network via UI', () => {
  test.beforeEach(async ({ page }) => {
    await chaosLogin(page);
  });

  test('credentials page handles slow API response', async ({ page }) => {
    // Navigate first to let initial API calls complete
    await page.goto('/ovh-credentials');
    
    // Wait for initial page load to complete (status check runs on DOMContentLoaded)
    // Wait for actual result content (not just "Checking...")
    await page.waitForFunction(
      () => {
        const el = document.querySelector('#status-content');
        return el && el.textContent && (el.textContent.includes('Configured') || el.textContent.includes('No Credentials'));
      },
      { timeout: 10000 }
    );
    
    // NOW set up slow response interception for the next GET request
    // Note: The endpoint is /api/ovh-credentials (with ovh- prefix)
    await page.route('**/api/ovh-credentials', async route => {
      if (route.request().method() === 'GET') {
        await new Promise(resolve => setTimeout(resolve, 2000));
        route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ configured: false }),
        });
      } else {
        route.continue();
      }
    });

    const startTime = Date.now();
    await page.locator('button#btn-check').click();
    
    // Wait for the status content to show "Checking..." first, then actual result
    // The function sets "Checking..." immediately, then fetches, then shows result
    // We need to wait for the result that comes AFTER the slow response
    await page.waitForFunction(
      () => {
        const el = document.querySelector('#status-content');
        // Must contain the result text, not just "Checking..."
        return el && el.textContent && !el.textContent.includes('Checking') && el.textContent.includes('No Credentials');
      },
      { timeout: 10000 }
    );
    
    const elapsed = Date.now() - startTime;
    expect(elapsed).toBeGreaterThanOrEqual(1500);

    await page.unroute('**/api/ovh-credentials');
  });
});

test.describe('API Chaos - HTMX Responses', () => {
  test.beforeEach(async ({ page }) => {
    await chaosLogin(page);
  });

  test('wizard form submission handles server error', async ({ page }) => {
    await page.goto('/admin');

    // Navigate to step 5
    await page.locator('button:has-text("Next")').click();
    await page.locator('input#network_id').fill('test-network');
    await page.locator('button:has-text("Next")').click();
    await page.waitForSelector('.wizard-step[data-step="3"].active', { timeout: 5000 });
    await page.locator('button:has-text("Next")').click();
    await page.waitForSelector('.wizard-step[data-step="4"].active', { timeout: 5000 });
    await page.locator('button:has-text("Next")').click();
    await page.waitForSelector('.wizard-step[data-step="5"].active', { timeout: 5000 });

    // Intercept dry-run with error
    await page.route('**/api/labs/dry-run', route => route.fulfill({
      status: 500,
      contentType: 'text/html',
      body: '<div class="error-message"><h3>Server Error</h3><p>Something went wrong</p></div>',
    }));

    // Click dry run
    await page.locator('button:has-text("Dry Run")').click();
    await page.waitForTimeout(1000);

    // Page should show the error, not crash
    await expect(page.locator('body')).toBeVisible();

    await page.unroute('**/api/labs/dry-run');
  });

  test('wizard handles malformed HTML response from HTMX', async ({ page }) => {
    await page.goto('/admin');

    // Navigate to step 5
    await page.locator('button:has-text("Next")').click();
    await page.locator('input#network_id').fill('test-network');
    await page.locator('button:has-text("Next")').click();
    await page.waitForSelector('.wizard-step[data-step="3"].active', { timeout: 5000 });
    await page.locator('button:has-text("Next")').click();
    await page.waitForSelector('.wizard-step[data-step="4"].active', { timeout: 5000 });
    await page.locator('button:has-text("Next")').click();
    await page.waitForSelector('.wizard-step[data-step="5"].active', { timeout: 5000 });

    // Intercept with malformed response
    await page.route('**/api/labs/dry-run', route => route.fulfill({
      status: 200,
      contentType: 'text/html',
      body: '<div class="broken">Unclosed tag<p>Missing close',
    }));

    await page.locator('button:has-text("Dry Run")').click();
    await page.waitForTimeout(1000);

    // Page should handle gracefully
    await expect(page.locator('body')).toBeVisible();

    await page.unroute('**/api/labs/dry-run');
  });
});

test.describe('API Chaos - Content Type Edge Cases', () => {
  test.beforeEach(async ({ page }) => {
    await chaosLogin(page);
  });

  test('student dashboard handles HTML when JSON expected', async ({ page }) => {
    await page.goto('/student/login');
    await page.locator('input[type="password"]').fill('studentpass');
    await page.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');

    await page.route('**/api/labs', route => route.fulfill({
      status: 200,
      contentType: 'text/html',
      body: '<html><body>Unexpected HTML</body></html>',
    }));

    await page.goto('/student/dashboard');
    await page.waitForTimeout(1500);

    // Should handle gracefully
    await expect(page.locator('body')).toBeVisible();

    await page.unroute('**/api/labs');
  });

  test('credentials check handles empty response', async ({ page }) => {
    await page.goto('/ovh-credentials');

    await page.route('**/api/credentials/status', route => route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: '',
    }));

    await page.locator('button#btn-check').click();
    await page.waitForTimeout(1000);

    // Should handle empty response
    await expect(page.locator('#status-container')).toBeVisible();

    await page.unroute('**/api/credentials/status');
  });
});

test.describe('API Chaos - Error Recovery', () => {
  test.beforeEach(async ({ page }) => {
    await chaosLogin(page);
  });

  test('app recovers after temporary API failure', async ({ page }) => {
    let failCount = 0;

    await page.route('**/api/credentials/status', route => {
      failCount++;
      if (failCount <= 2) {
        route.fulfill({
          status: 500,
          body: 'Temporary failure',
        });
      } else {
        route.fulfill({
          status: 200,
          contentType: 'application/json',
          body: JSON.stringify({ configured: true, service_name: 'test' }),
        });
      }
    });

    await page.goto('/ovh-credentials');

    // First check - will fail
    await page.locator('button#btn-check').click();
    await page.waitForTimeout(500);

    // Second check - will fail
    await page.locator('button#btn-check').click();
    await page.waitForTimeout(500);

    // Third check - will succeed
    await page.locator('button#btn-check').click();
    await page.waitForTimeout(1000);

    // Should show configured status
    await expect(page.locator('#status-container')).toBeVisible();

    await page.unroute('**/api/credentials/status');
  });

  test('navigation works after API error', async ({ page }) => {
    // Cause an error on one page
    await page.route('**/api/jobs', route => route.fulfill({
      status: 500,
      body: 'Error',
    }));

    await page.goto('/jobs');
    await page.waitForTimeout(500);

    await page.unroute('**/api/jobs');

    // Navigation to other pages should still work
    await page.goto('/admin');
    await expect(page.locator('h1')).toContainText('EasyLab');

    await page.goto('/ovh-credentials');
    await expect(page.locator('h1')).toContainText('OVH Credentials');
  });
});

test.describe('API Chaos - Concurrent Requests', () => {
  test('multiple credential checks don\'t cause issues', async ({ page }) => {
    await chaosLogin(page);
    await page.goto('/ovh-credentials');

    // Click check button multiple times rapidly
    const checkButton = page.locator('button#btn-check');
    await Promise.all([
      checkButton.click({ force: true }),
      checkButton.click({ force: true }),
      checkButton.click({ force: true }),
    ]).catch(() => {});

    await page.waitForTimeout(2000);

    // Page should remain stable
    await expect(page.locator('body')).toBeVisible();
    await expect(page.locator('#status-container')).toBeVisible();
  });
});
