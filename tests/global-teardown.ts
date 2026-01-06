import { FullConfig } from '@playwright/test';
import { coverageCollector } from './coverage-helper';

/**
 * Global teardown for Playwright tests
 * Saves coverage summary report
 */
async function globalTeardown(config: FullConfig) {
  if (process.env.COLLECT_COVERAGE === 'true') {
    coverageCollector.saveSummaryReport();
    const stats = coverageCollector.calculateCoverageStats();
    console.log(`\nFrontend Coverage: ${stats.coveragePercentage.toFixed(2)}% (${stats.files} files)`);
  }
}

export default globalTeardown;

