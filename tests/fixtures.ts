import { test as base } from '@playwright/test';
import { coverageCollector } from './coverage-helper';

/**
 * Extended test fixture with coverage collection
 */
export const test = base.extend({
  page: async ({ page }, use, testInfo) => {
    // Start coverage collection before test
    if (process.env.COLLECT_COVERAGE === 'true') {
      await coverageCollector.startCoverage(page);
    }

    // Use the page
    await use(page);

    // Stop coverage collection after test
    if (process.env.COLLECT_COVERAGE === 'true') {
      await coverageCollector.stopCoverage(page, testInfo.title);
    }
  },
});

export { expect } from '@playwright/test';

