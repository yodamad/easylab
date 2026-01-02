import { Page, Route, Locator, BrowserContext } from '@playwright/test';

/**
 * Chaos Testing Utilities
 * 
 * A collection of helper functions for simulating network failures,
 * server errors, and UI race conditions in Playwright tests.
 */

// ============================================================================
// Network Chaos Helpers
// ============================================================================

/**
 * Execute a function while simulating network failure for matching requests
 * @param page - Playwright page instance
 * @param urlPattern - URL pattern to intercept (string or RegExp)
 * @param fn - Function to execute while network is failing
 * @param errorType - Type of network error to simulate
 */
export async function withNetworkFailure(
  page: Page,
  urlPattern: string | RegExp,
  fn: () => Promise<void>,
  errorType: 'failed' | 'timedout' | 'aborted' | 'connectionrefused' = 'failed'
): Promise<void> {
  await page.route(urlPattern, route => route.abort(errorType));
  try {
    await fn();
  } finally {
    await page.unroute(urlPattern);
  }
}

/**
 * Execute a function while simulating slow network conditions
 * @param page - Playwright page instance
 * @param delayMs - Delay in milliseconds to add to each request
 * @param fn - Function to execute under slow conditions
 * @param urlPattern - Optional URL pattern to slow down (defaults to all)
 */
export async function withSlowNetwork(
  page: Page,
  delayMs: number,
  fn: () => Promise<void>,
  urlPattern: string | RegExp = '**/*'
): Promise<void> {
  await page.route(urlPattern, async route => {
    await new Promise(resolve => setTimeout(resolve, delayMs));
    await route.continue();
  });
  try {
    await fn();
  } finally {
    await page.unroute(urlPattern);
  }
}

/**
 * Execute a function while simulating intermittent network failures
 * @param page - Playwright page instance
 * @param failureProbability - Probability of failure (0-1)
 * @param fn - Function to execute
 * @param urlPattern - URL pattern to affect
 */
export async function withIntermittentNetwork(
  page: Page,
  failureProbability: number,
  fn: () => Promise<void>,
  urlPattern: string | RegExp = '**/*'
): Promise<void> {
  await page.route(urlPattern, async route => {
    if (Math.random() < failureProbability) {
      await route.abort('failed');
    } else {
      await route.continue();
    }
  });
  try {
    await fn();
  } finally {
    await page.unroute(urlPattern);
  }
}

/**
 * Simulate offline mode for the page
 * @param page - Playwright page instance
 * @param fn - Function to execute while offline
 */
export async function withOfflineMode(
  page: Page,
  fn: () => Promise<void>
): Promise<void> {
  const context = page.context();
  await context.setOffline(true);
  try {
    await fn();
  } finally {
    await context.setOffline(false);
  }
}

// ============================================================================
// Server Chaos Helpers
// ============================================================================

/**
 * Execute a function while simulating server errors for matching requests
 * @param page - Playwright page instance
 * @param urlPattern - URL pattern to intercept
 * @param statusCode - HTTP status code to return
 * @param fn - Function to execute
 * @param body - Optional response body
 */
export async function withServerError(
  page: Page,
  urlPattern: string | RegExp,
  statusCode: number,
  fn: () => Promise<void>,
  body: string = ''
): Promise<void> {
  await page.route(urlPattern, route => {
    route.fulfill({
      status: statusCode,
      contentType: 'text/html',
      body: body || `<html><body><h1>Error ${statusCode}</h1></body></html>`,
    });
  });
  try {
    await fn();
  } finally {
    await page.unroute(urlPattern);
  }
}

/**
 * Execute a function while returning malformed JSON for API requests
 * @param page - Playwright page instance
 * @param urlPattern - URL pattern to intercept
 * @param fn - Function to execute
 */
export async function withMalformedJSON(
  page: Page,
  urlPattern: string | RegExp,
  fn: () => Promise<void>
): Promise<void> {
  await page.route(urlPattern, route => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: '{ invalid json without closing brace',
    });
  });
  try {
    await fn();
  } finally {
    await page.unroute(urlPattern);
  }
}

/**
 * Execute a function while returning empty response
 * @param page - Playwright page instance
 * @param urlPattern - URL pattern to intercept
 * @param fn - Function to execute
 */
export async function withEmptyResponse(
  page: Page,
  urlPattern: string | RegExp,
  fn: () => Promise<void>
): Promise<void> {
  await page.route(urlPattern, route => {
    route.fulfill({
      status: 200,
      contentType: 'application/json',
      body: '',
    });
  });
  try {
    await fn();
  } finally {
    await page.unroute(urlPattern);
  }
}

/**
 * Execute a function while returning HTML instead of JSON
 * @param page - Playwright page instance
 * @param urlPattern - URL pattern to intercept
 * @param fn - Function to execute
 */
export async function withHTMLInsteadOfJSON(
  page: Page,
  urlPattern: string | RegExp,
  fn: () => Promise<void>
): Promise<void> {
  await page.route(urlPattern, route => {
    route.fulfill({
      status: 200,
      contentType: 'text/html',
      body: '<html><body>Unexpected HTML response</body></html>',
    });
  });
  try {
    await fn();
  } finally {
    await page.unroute(urlPattern);
  }
}

/**
 * Execute a function while simulating random server errors
 * @param page - Playwright page instance
 * @param urlPattern - URL pattern to intercept
 * @param fn - Function to execute
 * @param errorProbability - Probability of error (0-1)
 */
export async function withRandomServerErrors(
  page: Page,
  urlPattern: string | RegExp,
  fn: () => Promise<void>,
  errorProbability: number = 0.5
): Promise<void> {
  const errorCodes = [500, 502, 503, 504];
  await page.route(urlPattern, async route => {
    if (Math.random() < errorProbability) {
      const statusCode = errorCodes[Math.floor(Math.random() * errorCodes.length)];
      await route.fulfill({
        status: statusCode,
        contentType: 'text/html',
        body: `<html><body><h1>Error ${statusCode}</h1></body></html>`,
      });
    } else {
      await route.continue();
    }
  });
  try {
    await fn();
  } finally {
    await page.unroute(urlPattern);
  }
}

/**
 * Execute a function while simulating request timeout (very slow response)
 * @param page - Playwright page instance
 * @param urlPattern - URL pattern to intercept
 * @param fn - Function to execute
 * @param timeoutMs - Time to wait before responding (simulating timeout)
 */
export async function withRequestTimeout(
  page: Page,
  urlPattern: string | RegExp,
  fn: () => Promise<void>,
  timeoutMs: number = 30000
): Promise<void> {
  await page.route(urlPattern, async route => {
    // Wait for the specified time, then abort
    await new Promise(resolve => setTimeout(resolve, timeoutMs));
    await route.abort('timedout');
  });
  try {
    await fn();
  } finally {
    await page.unroute(urlPattern);
  }
}

// ============================================================================
// UI Chaos Helpers
// ============================================================================

/**
 * Perform rapid clicks on an element
 * @param locator - Playwright locator for the element
 * @param times - Number of clicks to perform
 * @param delayBetweenClicks - Optional delay between clicks in ms
 */
export async function rapidClick(
  locator: Locator,
  times: number,
  delayBetweenClicks: number = 0
): Promise<void> {
  const clicks = [];
  for (let i = 0; i < times; i++) {
    if (delayBetweenClicks > 0) {
      clicks.push(
        new Promise<void>(async resolve => {
          await new Promise(r => setTimeout(r, i * delayBetweenClicks));
          await locator.click({ force: true }).catch(() => {});
          resolve();
        })
      );
    } else {
      clicks.push(locator.click({ force: true }).catch(() => {}));
    }
  }
  await Promise.all(clicks);
}

/**
 * Perform double click followed by single click (common user mistake)
 * @param locator - Playwright locator for the element
 */
export async function doubleClickThenClick(locator: Locator): Promise<void> {
  await locator.dblclick({ force: true }).catch(() => {});
  await locator.click({ force: true }).catch(() => {});
}

/**
 * Fill form and submit multiple times rapidly
 * @param page - Playwright page instance
 * @param formFillFn - Function to fill the form
 * @param submitLocator - Locator for submit button
 * @param times - Number of submissions
 */
export async function rapidFormSubmit(
  page: Page,
  formFillFn: () => Promise<void>,
  submitLocator: Locator,
  times: number = 3
): Promise<void> {
  await formFillFn();
  await rapidClick(submitLocator, times);
}

/**
 * Simulate browser back/forward navigation during an operation
 * @param page - Playwright page instance
 * @param fn - Function to execute during navigation chaos
 */
export async function withNavigationChaos(
  page: Page,
  fn: () => Promise<void>
): Promise<void> {
  const navigationPromise = (async () => {
    await new Promise(resolve => setTimeout(resolve, 100));
    await page.goBack().catch(() => {});
    await new Promise(resolve => setTimeout(resolve, 50));
    await page.goForward().catch(() => {});
  })();
  
  await Promise.race([fn(), navigationPromise]);
}

/**
 * Create multiple browser contexts and perform concurrent operations
 * @param context - Browser context
 * @param operations - Array of operations to perform concurrently
 */
export async function withConcurrentContexts(
  context: BrowserContext,
  operations: Array<(page: Page) => Promise<void>>
): Promise<void[]> {
  const pages = await Promise.all(
    operations.map(() => context.newPage())
  );
  
  try {
    return await Promise.all(
      operations.map((op, index) => op(pages[index]))
    );
  } finally {
    await Promise.all(pages.map(p => p.close().catch(() => {})));
  }
}

/**
 * Rapidly switch focus between elements
 * @param locators - Array of locators to focus
 * @param cycles - Number of focus cycles
 */
export async function rapidFocusSwitch(
  locators: Locator[],
  cycles: number = 5
): Promise<void> {
  for (let i = 0; i < cycles; i++) {
    for (const locator of locators) {
      await locator.focus().catch(() => {});
    }
  }
}

/**
 * Simulate typing while form is being submitted
 * @param inputLocator - Locator for input field
 * @param submitLocator - Locator for submit button
 * @param text - Text to type
 */
export async function typeWhileSubmitting(
  inputLocator: Locator,
  submitLocator: Locator,
  text: string
): Promise<void> {
  await Promise.all([
    inputLocator.pressSequentially(text, { delay: 10 }),
    submitLocator.click({ force: true }),
  ]);
}

// ============================================================================
// Test Utility Helpers
// ============================================================================

/**
 * Count network requests matching a pattern
 * @param page - Playwright page instance
 * @param urlPattern - URL pattern to match
 * @returns Object with start/stop/getCount methods
 */
export function createRequestCounter(page: Page, urlPattern: string | RegExp) {
  let count = 0;
  let handler: ((route: Route) => Promise<void>) | null = null;
  
  return {
    async start() {
      handler = async (route: Route) => {
        count++;
        await route.continue();
      };
      await page.route(urlPattern, handler);
    },
    async stop() {
      if (handler) {
        await page.unroute(urlPattern);
        handler = null;
      }
    },
    getCount() {
      return count;
    },
    reset() {
      count = 0;
    },
  };
}

/**
 * Wait for a specific number of requests to a URL pattern
 * @param page - Playwright page instance
 * @param urlPattern - URL pattern to match
 * @param count - Number of requests to wait for
 * @param timeout - Timeout in milliseconds
 */
export async function waitForRequestCount(
  page: Page,
  urlPattern: string | RegExp,
  count: number,
  timeout: number = 5000
): Promise<void> {
  let requestCount = 0;
  
  return new Promise((resolve, reject) => {
    const timeoutId = setTimeout(() => {
      reject(new Error(`Timeout waiting for ${count} requests, got ${requestCount}`));
    }, timeout);
    
    const handler = async (route: Route) => {
      requestCount++;
      await route.continue();
      if (requestCount >= count) {
        clearTimeout(timeoutId);
        page.unroute(urlPattern).then(resolve);
      }
    };
    
    page.route(urlPattern, handler);
  });
}

/**
 * Login helper that works across chaos tests
 * @param page - Playwright page instance
 * @param password - Password to use
 * @param isStudent - Whether to login as student
 */
export async function chaosLogin(
  page: Page,
  password: string = 'testpassword',
  isStudent: boolean = false
): Promise<void> {
  const loginUrl = isStudent ? '/student/login' : '/login';
  await page.goto(loginUrl);
  await page.locator('input[type="password"]').fill(password);
  await page.locator('button[type="submit"]').click();
  await page.waitForLoadState('networkidle');
}

