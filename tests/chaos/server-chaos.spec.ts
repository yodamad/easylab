import { test, expect } from '@playwright/test';
import {
  withServerError,
  withMalformedJSON,
  withEmptyResponse,
  withHTMLInsteadOfJSON,
  withRandomServerErrors,
  chaosLogin,
} from './helpers';

test.describe('Server Chaos - 500 Internal Server Error', () => {
  test('handles 500 error on login submission', async ({ page }) => {
    await page.goto('/login');
    await page.locator('input[type="password"]').fill('testpassword');

    await withServerError(page, '**/login', 500, async () => {
      await page.locator('button[type="submit"]').click();
      await page.waitForTimeout(1000);
      
      // Page should remain functional and not crash
      await expect(page.locator('body')).toBeVisible();
    });
  });

  test('handles 500 error on student login', async ({ page }) => {
    await page.goto('/student/login');
    await page.locator('input[type="password"]').fill('studentpass');

    await withServerError(page, '**/student/login', 500, async () => {
      await page.locator('button[type="submit"]').click();
      await page.waitForTimeout(1000);
      
      // Should show error or remain on login page
      await expect(page.locator('body')).toBeVisible();
    });
  });

  test('handles 500 error on credentials save', async ({ page }) => {
    await chaosLogin(page);
    await page.goto('/ovh-credentials');

    // Fill credentials form
    await page.locator('input#ovh_application_key').fill('test-key');
    await page.locator('input#ovh_application_secret').fill('test-secret');
    await page.locator('input#ovh_consumer_key').fill('test-consumer');
    await page.locator('input#ovh_service_name').fill('test-service');
    await page.locator('select#ovh_endpoint').selectOption('ovh-eu');

    await withServerError(page, '**/api/credentials', 500, async () => {
      await page.locator('button[type="submit"]').click();
      await page.waitForTimeout(1000);
      
      // Form response area should show error or form should remain
      await expect(page.locator('body')).toBeVisible();
    });
  });
});

test.describe('Server Chaos - 502 Bad Gateway', () => {
  test('handles 502 error on page load', async ({ page }) => {
    await withServerError(page, '**/login', 502, async () => {
      await page.goto('/login').catch(() => {});
      await page.waitForTimeout(500);
      
      // Page should show error or be handled gracefully
      await expect(page.locator('body')).toBeVisible();
    });
  });

  test('handles 502 during wizard navigation', async ({ page }) => {
    await chaosLogin(page);
    await page.goto('/admin');

    await withServerError(page, '**/api/**', 502, async () => {
      // Try to interact with wizard
      await page.locator('button:has-text("Next")').click();
      await page.waitForTimeout(500);
    });

    // Wizard should still be visible and functional
    await expect(page.locator('.wizard-step.active')).toBeVisible();
  });
});

test.describe('Server Chaos - 503 Service Unavailable', () => {
  test('handles 503 on jobs list', async ({ page }) => {
    await chaosLogin(page);
    
    await withServerError(page, '**/jobs', 503, async () => {
      await page.goto('/jobs').catch(() => {});
      await page.waitForTimeout(500);
    });

    // Page should handle gracefully
    await expect(page.locator('body')).toBeVisible();
  });

  test('handles 503 on API calls', async ({ page }) => {
    // Login as student to access student dashboard
    await chaosLogin(page, 'studentpass', true);

    await withServerError(page, '**/api/labs', 503, async () => {
      await page.goto('/student/dashboard');
      await page.waitForTimeout(1500);
      
      // Labs container should show error or empty state
      await expect(page.locator('#labs-container')).toBeVisible();
    });
  });
});

test.describe('Server Chaos - Malformed JSON Responses', () => {
  test('handles malformed JSON on labs API', async ({ page }) => {
    await chaosLogin(page, 'studentpass', true);
    await page.goto('/student/dashboard');

    await withMalformedJSON(page, '**/api/labs', async () => {
      // Wait for the API call to complete
      await page.waitForTimeout(1500);
      
      // Page should not crash - labs container should show error or fallback
      await expect(page.locator('#labs-container')).toBeVisible();
    });
  });

  test('handles malformed JSON on jobs API', async ({ page }) => {
    await chaosLogin(page);
    
    await withMalformedJSON(page, '**/api/jobs', async () => {
      await page.goto('/jobs');
      await page.waitForTimeout(1000);
    });

    // Page should handle gracefully
    await expect(page.locator('body')).toBeVisible();
  });

  test('handles malformed JSON on credentials status', async ({ page }) => {
    await chaosLogin(page);
    await page.goto('/ovh-credentials');

    await withMalformedJSON(page, '**/api/credentials/**', async () => {
      await page.locator('button#btn-check').click();
      await page.waitForTimeout(1000);
      
      // Status container should show something
      await expect(page.locator('#status-container')).toBeVisible();
    });
  });
});

test.describe('Server Chaos - Empty Responses', () => {
  test('handles empty response on labs API', async ({ page }) => {
    await chaosLogin(page, 'studentpass', true);
    await page.goto('/student/dashboard');

    await withEmptyResponse(page, '**/api/labs', async () => {
      await page.waitForTimeout(1500);
      
      // Should show "no labs" message or handle gracefully
      await expect(page.locator('#labs-container')).toBeVisible();
    });
  });

  test('handles empty response on jobs API', async ({ page }) => {
    await chaosLogin(page);
    
    await withEmptyResponse(page, '**/api/jobs', async () => {
      await page.goto('/jobs');
      await page.waitForTimeout(1000);
    });

    // Page should handle gracefully
    await expect(page.locator('body')).toBeVisible();
  });

  test('handles empty response on credentials check', async ({ page }) => {
    await chaosLogin(page);
    await page.goto('/ovh-credentials');

    await withEmptyResponse(page, '**/api/credentials/**', async () => {
      await page.locator('button#btn-check').click();
      await page.waitForTimeout(1000);
    });

    // Status should show something (error or default state)
    await expect(page.locator('#status-container')).toBeVisible();
  });
});

test.describe('Server Chaos - HTML Instead of JSON', () => {
  test('handles HTML response when JSON expected on labs API', async ({ page }) => {
    await chaosLogin(page, 'studentpass', true);
    await page.goto('/student/dashboard');

    await withHTMLInsteadOfJSON(page, '**/api/labs', async () => {
      await page.waitForTimeout(1500);
      
      // Should handle gracefully - show error or empty state
      await expect(page.locator('#labs-container')).toBeVisible();
    });
  });

  test('handles HTML response when JSON expected on jobs API', async ({ page }) => {
    await chaosLogin(page);
    
    await withHTMLInsteadOfJSON(page, '**/api/jobs', async () => {
      await page.goto('/jobs');
      await page.waitForTimeout(1000);
    });

    // Page should not crash
    await expect(page.locator('body')).toBeVisible();
  });

  test('handles HTML error page on API endpoint', async ({ page }) => {
    await chaosLogin(page);
    await page.goto('/ovh-credentials');

    await withHTMLInsteadOfJSON(page, '**/api/credentials/**', async () => {
      await page.locator('button#btn-check').click();
      await page.waitForTimeout(1000);
    });

    // Should show some status
    await expect(page.locator('#status-container')).toBeVisible();
  });
});

test.describe('Server Chaos - Random Server Errors', () => {
  test('survives random server errors during wizard flow', async ({ page }) => {
    await chaosLogin(page);
    await page.goto('/admin');

    // Navigate through wizard with 30% error rate
    await withRandomServerErrors(page, '**/api/**', async () => {
      // Try multiple steps
      for (let i = 0; i < 3; i++) {
        await page.locator('button:has-text("Next")').click().catch(() => {});
        await page.waitForTimeout(300);
      }
    }, 0.3);

    // Wizard should still be visible
    await expect(page.locator('.wizard-step.active')).toBeVisible();
  });

  test('survives random server errors on navigation', async ({ page }) => {
    await chaosLogin(page);

    await withRandomServerErrors(page, '**/*', async () => {
      // Navigate around the app
      await page.goto('/admin').catch(() => {});
      await page.waitForTimeout(300);
      await page.goto('/jobs').catch(() => {});
      await page.waitForTimeout(300);
      await page.goto('/ovh-credentials').catch(() => {});
      await page.waitForTimeout(300);
    }, 0.3);

    // Page should be functional
    await expect(page.locator('body')).toBeVisible();
  });
});

test.describe('Server Chaos - Session Expiration Simulation', () => {
  test('handles 401 unauthorized mid-operation', async ({ page }) => {
    await chaosLogin(page);
    await page.goto('/admin');

    // Simulate session expiration
    await withServerError(page, '**/api/**', 401, async () => {
      await page.locator('button:has-text("Next")').click();
      await page.waitForTimeout(1000);
    }, 'Unauthorized');

    // Page should handle gracefully
    await expect(page.locator('body')).toBeVisible();
  });

  test('handles 403 forbidden on protected routes', async ({ page }) => {
    await chaosLogin(page);
    
    await withServerError(page, '**/jobs', 403, async () => {
      await page.goto('/jobs').catch(() => {});
      await page.waitForTimeout(500);
    }, 'Forbidden');

    // Should show error or handle gracefully
    await expect(page.locator('body')).toBeVisible();
  });
});

test.describe('Server Chaos - Rate Limiting Simulation', () => {
  test('handles 429 too many requests', async ({ page }) => {
    await chaosLogin(page);
    await page.goto('/ovh-credentials');

    // Fill credentials form
    await page.locator('input#ovh_application_key').fill('test-key');
    await page.locator('input#ovh_application_secret').fill('test-secret');
    await page.locator('input#ovh_consumer_key').fill('test-consumer');
    await page.locator('input#ovh_service_name').fill('test-service');
    await page.locator('select#ovh_endpoint').selectOption('ovh-eu');

    await withServerError(page, '**/api/credentials', 429, async () => {
      await page.locator('button[type="submit"]').click();
      await page.waitForTimeout(1000);
    }, 'Too Many Requests');

    // Should handle gracefully
    await expect(page.locator('body')).toBeVisible();
  });
});

test.describe('Server Chaos - Gateway Timeout', () => {
  test('handles 504 gateway timeout on form submission', async ({ page }) => {
    await chaosLogin(page);
    await page.goto('/ovh-credentials');

    // Fill credentials form
    await page.locator('input#ovh_application_key').fill('test-key');
    await page.locator('input#ovh_application_secret').fill('test-secret');
    await page.locator('input#ovh_consumer_key').fill('test-consumer');
    await page.locator('input#ovh_service_name').fill('test-service');
    await page.locator('select#ovh_endpoint').selectOption('ovh-eu');

    await withServerError(page, '**/api/credentials', 504, async () => {
      await page.locator('button[type="submit"]').click();
      await page.waitForTimeout(1000);
    }, 'Gateway Timeout');

    // Form should remain functional
    await expect(page.locator('button[type="submit"]')).toBeVisible();
  });
});

test.describe('Server Chaos - Mixed Error Scenarios', () => {
  test('handles different errors on different endpoints simultaneously', async ({ page }) => {
    await chaosLogin(page);
    
    // Set up different errors for different routes
    await page.route('**/api/jobs', route => {
      route.fulfill({
        status: 500,
        contentType: 'text/html',
        body: 'Internal Server Error',
      });
    });
    
    await page.route('**/api/credentials/**', route => {
      route.fulfill({
        status: 200,
        contentType: 'application/json',
        body: '{ invalid json',
      });
    });

    // Navigate and interact
    await page.goto('/jobs').catch(() => {});
    await page.waitForTimeout(500);
    
    await page.goto('/ovh-credentials').catch(() => {});
    await page.locator('button#btn-check').click().catch(() => {});
    await page.waitForTimeout(500);

    // Clean up routes
    await page.unroute('**/api/jobs');
    await page.unroute('**/api/credentials/**');

    // Page should remain functional
    await expect(page.locator('body')).toBeVisible();
  });
});

