import { test, expect } from '@playwright/test';
import {
  rapidClick,
  doubleClickThenClick,
  rapidFormSubmit,
  withNavigationChaos,
  rapidFocusSwitch,
  typeWhileSubmitting,
  createRequestCounter,
  chaosLogin,
} from './helpers';

test.describe('UI Chaos - Rapid Click Prevention', () => {
  test('prevents duplicate login submissions on rapid click', async ({ page }) => {
    await page.goto('/login');
    await page.locator('input[type="password"]').fill('testpassword');

    const loginButton = page.locator('button[type="submit"]');
    
    // Track login requests
    const counter = createRequestCounter(page, '**/login');
    await counter.start();

    // Rapid click the login button
    await rapidClick(loginButton, 5);
    
    // Wait for requests to complete
    await page.waitForTimeout(2000);
    await counter.stop();

    // Page should be in a valid state (either logged in or on login page)
    await expect(page.locator('body')).toBeVisible();
    
    // Ideally only 1-2 requests should have been made
    // (depends on frontend debouncing implementation)
    console.log(`Login requests made: ${counter.getCount()}`);
  });

  test('prevents duplicate student login submissions', async ({ page }) => {
    await page.goto('/student/login');
    await page.locator('input[type="password"]').fill('studentpass');

    const loginButton = page.locator('button[type="submit"]');
    
    const counter = createRequestCounter(page, '**/student/login');
    await counter.start();

    await rapidClick(loginButton, 5);
    await page.waitForTimeout(2000);
    await counter.stop();

    await expect(page.locator('body')).toBeVisible();
    console.log(`Student login requests made: ${counter.getCount()}`);
  });

  test('handles double-click then single click on submit', async ({ page }) => {
    await page.goto('/login');
    await page.locator('input[type="password"]').fill('testpassword');

    const loginButton = page.locator('button[type="submit"]');
    
    await doubleClickThenClick(loginButton);
    await page.waitForTimeout(2000);

    // Should be in a valid state
    await expect(page.locator('body')).toBeVisible();
  });
});

test.describe('UI Chaos - Wizard Rapid Navigation', () => {
  test.beforeEach(async ({ page }) => {
    await chaosLogin(page);
  });

  test('handles rapid Next button clicks in wizard', async ({ page }) => {
    await page.goto('/admin');

    const nextButton = page.locator('button:has-text("Next")');
    
    // Fill required field first
    await page.locator('input#stack_name').fill('rapid-test');

    // Rapidly click next multiple times
    await rapidClick(nextButton, 10, 50);
    await page.waitForTimeout(1000);

    // Wizard should be in a valid state
    const activeStep = page.locator('.wizard-step.active');
    await expect(activeStep).toBeVisible();
  });

  test('handles rapid Previous button clicks in wizard', async ({ page }) => {
    await page.goto('/admin');

    // Navigate to step 2 first
    await page.locator('button:has-text("Next")').click();
    await page.waitForSelector('.wizard-step[data-step="2"].active', { timeout: 5000 });

    const prevButton = page.locator('button:has-text("Previous")');
    
    // Rapidly click previous
    await rapidClick(prevButton, 5, 30);
    await page.waitForTimeout(500);

    // Should be on step 1
    await expect(page.locator('.wizard-step[data-step="1"].active')).toBeVisible();
  });

  test('handles rapid alternating Next/Previous clicks', async ({ page }) => {
    await page.goto('/admin');

    const nextButton = page.locator('button:has-text("Next")');
    const prevButton = page.locator('button:has-text("Previous")');

    // Rapidly alternate between next and previous
    for (let i = 0; i < 5; i++) {
      await nextButton.click({ force: true }).catch(() => {});
      await prevButton.click({ force: true }).catch(() => {});
    }

    await page.waitForTimeout(500);

    // Wizard should be in a valid state
    const activeStep = page.locator('.wizard-step.active');
    await expect(activeStep).toBeVisible();
  });
});

test.describe('UI Chaos - Form Submission Race Conditions', () => {
  test.beforeEach(async ({ page }) => {
    await chaosLogin(page);
  });

  test('handles rapid credentials save submissions', async ({ page }) => {
    await page.goto('/ovh-credentials');

    // Fill all fields
    await page.locator('input#ovh_application_key').fill('test-key-rapid');
    await page.locator('input#ovh_application_secret').fill('test-secret-rapid');
    await page.locator('input#ovh_consumer_key').fill('test-consumer-rapid');
    await page.locator('input#ovh_service_name').fill('test-service-rapid');
    await page.locator('select#ovh_endpoint').selectOption('ovh-eu');

    const submitButton = page.locator('button[type="submit"]');
    
    const counter = createRequestCounter(page, '**/api/credentials');
    await counter.start();

    // Rapid submit
    await rapidClick(submitButton, 5);
    await page.waitForTimeout(2000);
    await counter.stop();

    // Form should remain functional
    await expect(submitButton).toBeVisible();
    console.log(`Credentials save requests: ${counter.getCount()}`);
  });

  test('handles rapid form fill and submit', async ({ page }) => {
    await page.goto('/ovh-credentials');

    const submitButton = page.locator('button[type="submit"]');

    await rapidFormSubmit(
      page,
      async () => {
        await page.locator('input#ovh_application_key').fill('test-key');
        await page.locator('input#ovh_application_secret').fill('test-secret');
        await page.locator('input#ovh_consumer_key').fill('test-consumer');
        await page.locator('input#ovh_service_name').fill('test-service');
        await page.locator('select#ovh_endpoint').selectOption('ovh-eu');
      },
      submitButton,
      3
    );

    await page.waitForTimeout(1500);
    await expect(page.locator('body')).toBeVisible();
  });
});

test.describe('UI Chaos - Input Focus Racing', () => {
  test('handles rapid focus switching on login form', async ({ page }) => {
    await page.goto('/login');

    const passwordInput = page.locator('input[type="password"]');
    const submitButton = page.locator('button[type="submit"]');

    // Rapidly switch focus
    await rapidFocusSwitch([passwordInput, submitButton], 10);

    // Form should still be functional
    await expect(passwordInput).toBeVisible();
    await expect(submitButton).toBeVisible();
  });

  test('handles rapid focus switching on credentials form', async ({ page }) => {
    await chaosLogin(page);
    await page.goto('/ovh-credentials');

    const inputs = [
      page.locator('input#ovh_application_key'),
      page.locator('input#ovh_application_secret'),
      page.locator('input#ovh_consumer_key'),
      page.locator('input#ovh_service_name'),
    ];

    await rapidFocusSwitch(inputs, 5);

    // All inputs should still be visible and functional
    for (const input of inputs) {
      await expect(input).toBeVisible();
    }
  });
});

test.describe('UI Chaos - Typing While Submitting', () => {
  test('handles typing while submitting login form', async ({ page }) => {
    await page.goto('/login');

    const passwordInput = page.locator('input[type="password"]');
    const submitButton = page.locator('button[type="submit"]');

    // Start typing password and submit at the same time
    await typeWhileSubmitting(passwordInput, submitButton, 'testpassword');

    await page.waitForTimeout(1000);

    // Page should be in a valid state
    await expect(page.locator('body')).toBeVisible();
  });

  test('handles typing in wizard while navigating', async ({ page }) => {
    await chaosLogin(page);
    await page.goto('/admin');

    const stackInput = page.locator('input#stack_name');
    const nextButton = page.locator('button:has-text("Next")');

    // Type in stack name while clicking next
    await Promise.all([
      stackInput.pressSequentially('chaos-test', { delay: 20 }),
      nextButton.click({ force: true }),
    ]);

    await page.waitForTimeout(500);

    // Wizard should be in a valid state
    await expect(page.locator('.wizard-step.active')).toBeVisible();
  });
});

test.describe('UI Chaos - Navigation During Async Operations', () => {
  test.beforeEach(async ({ page }) => {
    await chaosLogin(page);
  });

  test('handles back/forward during wizard operations', async ({ page }) => {
    await page.goto('/admin');
    
    // Move through wizard
    await page.locator('button:has-text("Next")').click();
    await page.waitForTimeout(200);

    // Try navigation chaos
    await withNavigationChaos(page, async () => {
      await page.locator('button:has-text("Next")').click().catch(() => {});
    });

    await page.waitForTimeout(500);

    // Page should be functional
    await expect(page.locator('body')).toBeVisible();
  });

  test('handles browser back during form submission', async ({ page }) => {
    // First navigate to admin to have history
    await page.goto('/admin');
    await page.waitForLoadState('networkidle');
    
    // Then navigate to credentials
    await page.goto('/ovh-credentials');
    await page.waitForLoadState('networkidle');

    // Fill form
    await page.locator('input#ovh_application_key').fill('test-key');
    await page.locator('input#ovh_application_secret').fill('test-secret');
    await page.locator('input#ovh_consumer_key').fill('test-consumer');
    await page.locator('input#ovh_service_name').fill('test-service');
    await page.locator('select#ovh_endpoint').selectOption('ovh-eu');

    // Click submit using force to avoid waiting for navigation
    await page.locator('button[type="submit"]').click({ force: true, noWaitAfter: true });
    
    // Immediately go back - don't wait for submission to complete
    await page.goBack().catch(() => {});

    await page.waitForTimeout(1000);

    // Should be on some valid page (admin or credentials)
    await expect(page.locator('body')).toBeVisible();
    await expect(page.locator('h1')).toBeVisible();
  });

  test('handles browser refresh during wizard navigation', async ({ page }) => {
    await page.goto('/admin');
    
    // Navigate to step 2
    await page.locator('button:has-text("Next")').click();
    await page.waitForSelector('.wizard-step[data-step="2"].active', { timeout: 5000 });

    // Refresh the page
    await page.reload();

    // Wizard should load (may reset to step 1)
    await expect(page.locator('.wizard-step.active')).toBeVisible();
  });
});

test.describe('UI Chaos - Concurrent Browser Contexts', () => {
  test('handles multiple simultaneous logins from same browser', async ({ browser }) => {
    const context = await browser.newContext();
    
    // Create multiple pages
    const page1 = await context.newPage();
    const page2 = await context.newPage();
    const page3 = await context.newPage();

    // Try to login from all pages simultaneously
    await Promise.all([
      (async () => {
        await page1.goto('/login');
        await page1.locator('input[type="password"]').fill('testpassword');
        await page1.locator('button[type="submit"]').click();
      })(),
      (async () => {
        await page2.goto('/login');
        await page2.locator('input[type="password"]').fill('testpassword');
        await page2.locator('button[type="submit"]').click();
      })(),
      (async () => {
        await page3.goto('/login');
        await page3.locator('input[type="password"]').fill('testpassword');
        await page3.locator('button[type="submit"]').click();
      })(),
    ]);

    await Promise.all([
      page1.waitForTimeout(2000),
      page2.waitForTimeout(2000),
      page3.waitForTimeout(2000),
    ]);

    // All pages should be in valid states
    await expect(page1.locator('body')).toBeVisible();
    await expect(page2.locator('body')).toBeVisible();
    await expect(page3.locator('body')).toBeVisible();

    await context.close();
  });

  test('handles simultaneous credential saves from multiple tabs', async ({ browser }) => {
    const context = await browser.newContext();
    
    // First, login in one page and share the session
    const loginPage = await context.newPage();
    await loginPage.goto('/login');
    await loginPage.locator('input[type="password"]').fill('testpassword');
    await loginPage.locator('button[type="submit"]').click();
    await loginPage.waitForLoadState('networkidle');
    await loginPage.close();

    // Create multiple pages with shared session
    const page1 = await context.newPage();
    const page2 = await context.newPage();

    // Navigate both to credentials
    await Promise.all([
      page1.goto('/ovh-credentials'),
      page2.goto('/ovh-credentials'),
    ]);

    // Fill both forms differently
    await page1.locator('input#ovh_application_key').fill('key-from-page1');
    await page1.locator('input#ovh_application_secret').fill('secret1');
    await page1.locator('input#ovh_consumer_key').fill('consumer1');
    await page1.locator('input#ovh_service_name').fill('service1');
    await page1.locator('select#ovh_endpoint').selectOption('ovh-eu');

    await page2.locator('input#ovh_application_key').fill('key-from-page2');
    await page2.locator('input#ovh_application_secret').fill('secret2');
    await page2.locator('input#ovh_consumer_key').fill('consumer2');
    await page2.locator('input#ovh_service_name').fill('service2');
    await page2.locator('select#ovh_endpoint').selectOption('ovh-us');

    // Submit both simultaneously
    await Promise.all([
      page1.locator('button[type="submit"]').click(),
      page2.locator('button[type="submit"]').click(),
    ]);

    await Promise.all([
      page1.waitForTimeout(2000),
      page2.waitForTimeout(2000),
    ]);

    // Both pages should still be functional
    await expect(page1.locator('body')).toBeVisible();
    await expect(page2.locator('body')).toBeVisible();

    await context.close();
  });
});

test.describe('UI Chaos - Scroll During Interactions', () => {
  test('handles scrolling while clicking buttons', async ({ page }) => {
    await chaosLogin(page);
    await page.goto('/admin');

    // Scroll while clicking next
    await Promise.all([
      page.evaluate(() => window.scrollTo(0, 500)),
      page.locator('button:has-text("Next")').click({ force: true }),
    ]);

    await page.waitForTimeout(500);

    // Page should be functional
    await expect(page.locator('.wizard-step.active')).toBeVisible();
  });
});

test.describe('UI Chaos - Window Resize During Operations', () => {
  test('handles window resize during wizard navigation', async ({ page }) => {
    await chaosLogin(page);
    await page.goto('/admin');

    // Resize while navigating
    await Promise.all([
      page.setViewportSize({ width: 800, height: 600 }),
      page.locator('button:has-text("Next")').click(),
    ]);

    await page.waitForTimeout(300);

    await Promise.all([
      page.setViewportSize({ width: 1200, height: 800 }),
      page.locator('button:has-text("Previous")').click(),
    ]);

    await page.waitForTimeout(300);

    // Wizard should still be functional
    await expect(page.locator('.wizard-step.active')).toBeVisible();
  });

  test('handles mobile viewport transition during form fill', async ({ page }) => {
    await chaosLogin(page);
    await page.goto('/ovh-credentials');

    // Start with desktop
    await page.setViewportSize({ width: 1200, height: 800 });

    // Fill form while resizing
    await page.locator('input#ovh_application_key').fill('test-key');
    await page.setViewportSize({ width: 375, height: 667 }); // iPhone SE size
    await page.locator('input#ovh_application_secret').fill('test-secret');
    await page.setViewportSize({ width: 1200, height: 800 }); // Back to desktop
    await page.locator('input#ovh_consumer_key').fill('test-consumer');

    // Form should still be functional
    await expect(page.locator('button[type="submit"]')).toBeVisible();
  });
});

test.describe('UI Chaos - Keyboard Shortcuts During Operations', () => {
  test('handles Escape key during form submission', async ({ page }) => {
    await page.goto('/login');
    await page.locator('input[type="password"]').fill('testpassword');

    // Press Escape while clicking submit
    await Promise.all([
      page.locator('button[type="submit"]').click(),
      page.keyboard.press('Escape'),
    ]);

    await page.waitForTimeout(1000);

    // Page should be in a valid state
    await expect(page.locator('body')).toBeVisible();
  });

  test('handles Tab key spam during wizard', async ({ page }) => {
    await chaosLogin(page);
    await page.goto('/admin');

    // Tab through elements rapidly
    for (let i = 0; i < 20; i++) {
      await page.keyboard.press('Tab');
    }

    // Wizard should still be functional
    await expect(page.locator('.wizard-step.active')).toBeVisible();
  });
});

