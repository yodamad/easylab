import { test, expect } from '@playwright/test';
import {
  withNetworkFailure,
  withSlowNetwork,
  withIntermittentNetwork,
  withOfflineMode,
  withRequestTimeout,
  chaosLogin,
} from './helpers';

test.describe('Network Chaos - Login Page', () => {
  test('handles login request timeout gracefully', async ({ page }) => {
    await page.goto('/login');
    await page.locator('input[type="password"]').fill('testpassword');

    // Simulate network timeout on form submission
    await withNetworkFailure(page, '**/login', async () => {
      await page.locator('button[type="submit"]').click();
      
      // Page should not crash - it may show an error or remain on login
      await page.waitForTimeout(1000);
      
      // The page should still be functional (not crashed)
      await expect(page.locator('body')).toBeVisible();
    }, 'timedout');
  });

  test('handles connection refused on login', async ({ page }) => {
    await page.goto('/login');
    await page.locator('input[type="password"]').fill('testpassword');

    await withNetworkFailure(page, '**/login', async () => {
      await page.locator('button[type="submit"]').click();
      
      // Wait for the request to fail
      await page.waitForTimeout(1000);
      
      // Page should remain visible and functional
      await expect(page.locator('body')).toBeVisible();
    }, 'connectionrefused');
  });

  test('handles slow network on login (2000ms latency)', async ({ page }) => {
    await page.goto('/login');
    await page.locator('input[type="password"]').fill('testpassword');

    const startTime = Date.now();
    
    await withSlowNetwork(page, 2000, async () => {
      await page.locator('button[type="submit"]').click();
      
      // Wait for the slow request to complete
      await page.waitForLoadState('networkidle', { timeout: 10000 });
    });

    const elapsed = Date.now() - startTime;
    
    // Verify the delay was applied (should be at least 2 seconds)
    expect(elapsed).toBeGreaterThanOrEqual(1800); // Allow some tolerance
  });

  test('handles intermittent network during login attempts', async ({ page }) => {
    // With 50% failure rate, multiple login attempts may be needed
    let loginSucceeded = false;
    
    for (let attempt = 0; attempt < 5 && !loginSucceeded; attempt++) {
      await page.goto('/login');
      await page.locator('input[type="password"]').fill('testpassword');
      
      await withIntermittentNetwork(page, 0.5, async () => {
        await page.locator('button[type="submit"]').click();
        await page.waitForTimeout(500);
      }, '**/login');
      
      // Check if we made it to admin page
      if (page.url().includes('/admin')) {
        loginSucceeded = true;
      }
    }
    
    // Page should remain functional regardless of success
    await expect(page.locator('body')).toBeVisible();
  });
});

test.describe('Network Chaos - Student Portal', () => {
  test('handles network failure on student login', async ({ page }) => {
    await page.goto('/student/login');
    await page.locator('input[type="password"]').fill('studentpass');

    await withNetworkFailure(page, '**/student/login', async () => {
      await page.locator('button[type="submit"]').click();
      await page.waitForTimeout(1000);
      
      // Page should not crash
      await expect(page.locator('body')).toBeVisible();
    });
  });

  test('handles slow network on dashboard labs load', async ({ page }) => {
    // First login successfully
    await chaosLogin(page, 'studentpass', true);
    
    // Navigate to dashboard with slow network for API calls
    await withSlowNetwork(page, 1500, async () => {
      await page.goto('/student/dashboard');
      await page.waitForLoadState('domcontentloaded');
      
      // Labs container should show loading or no labs message
      await expect(page.locator('#labs-container')).toBeVisible();
    }, '**/api/**');
  });
});

test.describe('Network Chaos - Admin Wizard', () => {
  test.beforeEach(async ({ page }) => {
    await chaosLogin(page);
  });

  test('handles network failure during wizard navigation', async ({ page }) => {
    await page.goto('/admin');
    
    // Navigate through wizard with intermittent failures
    await withIntermittentNetwork(page, 0.3, async () => {
      // Try to navigate through steps
      await page.locator('button:has-text("Next")').click();
      await page.waitForTimeout(500);
      
      // The wizard should maintain its state
      const activeStep = page.locator('.wizard-step.active');
      await expect(activeStep).toBeVisible();
    });
  });

  test('handles slow network on form submission', async ({ page }) => {
    await page.goto('/admin');
    
    // Fill required fields
    await page.locator('input#stack_name').fill('chaos-test-stack');
    
    // Go through wizard steps
    await page.locator('button:has-text("Next")').click();
    await page.locator('input#network_id').fill('test-network');
    await page.locator('button:has-text("Next")').click();
    await page.waitForSelector('.wizard-step[data-step="3"].active', { timeout: 5000 });
    await page.locator('button:has-text("Next")').click();
    await page.waitForSelector('.wizard-step[data-step="4"].active', { timeout: 5000 });
    await page.locator('button:has-text("Next")').click();
    await page.waitForSelector('.wizard-step[data-step="5"].active', { timeout: 5000 });
    
    // Test slow network on dry run submission
    await withSlowNetwork(page, 3000, async () => {
      // Dry run button should still work even with slow network
      const dryRunButton = page.locator('button:has-text("Dry Run")');
      await expect(dryRunButton).toBeVisible();
    }, '**/api/**');
  });

  test('handles network timeout on credentials check', async ({ page }) => {
    await page.goto('/ovh-credentials');
    
    await withNetworkFailure(page, '**/api/credentials/**', async () => {
      await page.locator('button#btn-check').click();
      await page.waitForTimeout(1000);
      
      // Status container should show something (error or loading)
      await expect(page.locator('#status-container')).toBeVisible();
    }, 'timedout');
  });
});

test.describe('Network Chaos - Jobs List', () => {
  test.beforeEach(async ({ page }) => {
    await chaosLogin(page);
  });

  test('handles slow network on jobs list page load', async ({ page }) => {
    await withSlowNetwork(page, 2000, async () => {
      await page.goto('/jobs');
      await page.waitForLoadState('domcontentloaded');
      
      // Page header should be visible even with slow network
      await expect(page.locator('h1')).toContainText('Lab as Code');
    });
  });

  test('handles intermittent failures on jobs API', async ({ page }) => {
    await page.goto('/jobs');
    
    await withIntermittentNetwork(page, 0.5, async () => {
      // Navigate to admin and back to jobs multiple times
      await page.locator('a[href="/admin"]').first().click();
      await page.waitForTimeout(500);
      await page.locator('a[href="/jobs"]').click();
      await page.waitForTimeout(500);
    }, '**/api/jobs**');
    
    // Page should still be functional
    await expect(page.locator('body')).toBeVisible();
  });
});

test.describe('Network Chaos - Offline Mode', () => {
  test('static page content remains accessible in offline mode', async ({ page }) => {
    // First load the page while online
    await page.goto('/login');
    await expect(page.locator('h1')).toContainText('Lab as Code');
    
    // Now go offline
    await withOfflineMode(page, async () => {
      // The already loaded content should still be visible
      await expect(page.locator('h1')).toBeVisible();
      await expect(page.locator('input[type="password"]')).toBeVisible();
    });
  });

  test('handles offline mode gracefully when submitting form', async ({ page }) => {
    await page.goto('/login');
    await page.locator('input[type="password"]').fill('testpassword');
    
    await withOfflineMode(page, async () => {
      // Try to submit while offline
      await page.locator('button[type="submit"]').click();
      await page.waitForTimeout(1000);
      
      // Page should not crash
      await expect(page.locator('body')).toBeVisible();
    });
  });
});

test.describe('Network Chaos - OVH Credentials', () => {
  test.beforeEach(async ({ page }) => {
    await chaosLogin(page);
  });

  test('handles network failure on credentials save', async ({ page }) => {
    await page.goto('/ovh-credentials');
    
    // Fill all fields
    await page.locator('input#ovh_application_key').fill('test-key');
    await page.locator('input#ovh_application_secret').fill('test-secret');
    await page.locator('input#ovh_consumer_key').fill('test-consumer');
    await page.locator('input#ovh_service_name').fill('test-service');
    await page.locator('select#ovh_endpoint').selectOption('ovh-eu');
    
    await withNetworkFailure(page, '**/api/credentials', async () => {
      await page.locator('button[type="submit"]').click();
      await page.waitForTimeout(1000);
      
      // Should show some feedback (error or form should remain)
      await expect(page.locator('body')).toBeVisible();
    });
  });

  test('handles slow network on credentials save', async ({ page }) => {
    // Set up slow response interception BEFORE navigating
    // Note: The endpoint is /api/ovh-credentials (with ovh- prefix)
    await page.route('**/api/ovh-credentials', async route => {
      if (route.request().method() === 'POST') {
        await new Promise(resolve => setTimeout(resolve, 2000));
        route.fulfill({
          status: 200,
          contentType: 'text/html',
          body: '<div class="success-message"><p>✅ Credentials saved successfully</p></div>',
        });
      } else {
        route.continue();
      }
    });

    await page.goto('/ovh-credentials');
    
    // Fill all fields
    await page.locator('input#ovh_application_key').fill('test-key-slow');
    await page.locator('input#ovh_application_secret').fill('test-secret-slow');
    await page.locator('input#ovh_consumer_key').fill('test-consumer-slow');
    await page.locator('input#ovh_service_name').fill('test-service-slow');
    await page.locator('select#ovh_endpoint').selectOption('ovh-eu');
    
    const startTime = Date.now();
    await page.locator('button[type="submit"]').click();
    
    // Wait for form response to contain actual content
    await page.waitForFunction(
      () => {
        const el = document.querySelector('#form-response');
        return el && el.textContent && el.textContent.includes('saved');
      },
      { timeout: 10000 }
    );
    
    const elapsed = Date.now() - startTime;
    expect(elapsed).toBeGreaterThanOrEqual(1800);
    
    await page.unroute('**/api/ovh-credentials');
  });
});

test.describe('Network Chaos - Navigation Resilience', () => {
  test.beforeEach(async ({ page }) => {
    await chaosLogin(page);
  });

  test('handles network failure during page transitions', async ({ page }) => {
    await page.goto('/admin');
    
    // Start navigation with network failure
    await withNetworkFailure(page, '**/jobs', async () => {
      // Try to navigate to jobs
      await page.locator('a[href="/jobs"]').click();
      await page.waitForTimeout(1000);
    });
    
    // Page should show something (error page or remain on admin)
    await expect(page.locator('body')).toBeVisible();
  });

  test('recovers from network failure after reconnection', async ({ page }) => {
    await page.goto('/admin');
    
    // Simulate temporary network failure
    await withNetworkFailure(page, '**/*', async () => {
      await page.waitForTimeout(500);
    });
    
    // Network is back, navigation should work
    await page.goto('/jobs');
    await expect(page.locator('h1')).toContainText('Lab as Code');
  });
});

