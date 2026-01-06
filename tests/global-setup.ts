import { FullConfig } from '@playwright/test';

/**
 * Global setup for Playwright tests
 * Initializes coverage collection if enabled
 */
async function globalSetup(config: FullConfig) {
  if (process.env.COLLECT_COVERAGE === 'true') {
    console.log('Coverage collection enabled');
  }
}

export default globalSetup;

