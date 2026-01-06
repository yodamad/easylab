import { defineConfig, devices } from '@playwright/test';

/**
 * Lab as Code - Playwright Configuration
 * @see https://playwright.dev/docs/test-configuration
 */
export default defineConfig({
  testDir: './tests',
  /* Run tests in files in parallel */
  fullyParallel: true,
  /* Fail the build on CI if you accidentally left test.only in the source code */
  forbidOnly: !!process.env.CI,
  /* Retry on CI only */
  retries: process.env.CI ? 2 : 0,
  /* Opt out of parallel tests on CI */
  workers: process.env.CI ? 1 : undefined,
  /* Reporter to use */
  reporter: [['html', { outputFolder: 'playwright-report' }]],
  /* Global setup and teardown for coverage collection */
  globalSetup: require.resolve('./tests/global-setup.ts'),
  globalTeardown: require.resolve('./tests/global-teardown.ts'),
  /* Shared settings for all the projects below */
  use: {
    /* Base URL to use in actions like `await page.goto('/')` */
    baseURL: 'http://localhost:8080',

    /* Collect trace when retrying the failed test */
    trace: 'on-first-retry',

    /* Take screenshot on failure */
    screenshot: 'only-on-failure',
  },

  /* Configure projects for major browsers */
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
    },
  ],

  /* Run your local dev server before starting the tests */
  webServer: {
    command: 'rm -rf test-results/ && go build -o test-results/build/lab-as-code-server cmd/server/main.go && ./test-results/build/lab-as-code-server -env-file=env.test.sh',
    url: 'http://localhost:8080/health',
    reuseExistingServer: !process.env.CI,
    timeout: 120000, // 2 minutes for Go build + server start
  },

  /* Output directory for test artifacts */
  outputDir: 'test-results',
});

