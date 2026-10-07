import { test, expect } from '@playwright/test';

const DARK_BG = 'rgb(8, 13, 20)';
const LIGHT_BG = 'rgb(237, 242, 241)';

test.describe('Home Page Theme', () => {
  test('should follow a dark OS preference by default', async ({ page }) => {
    await page.emulateMedia({ colorScheme: 'dark' });
    await page.goto('/');

    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
    await expect(page.locator('body')).toHaveCSS('background-color', DARK_BG);
    await expect(page.locator('#homeThemeToggle')).toHaveAttribute('aria-label', 'Switch to light theme');
  });

  test('should follow a light OS preference by default', async ({ page }) => {
    await page.emulateMedia({ colorScheme: 'light' });
    await page.goto('/');

    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
    await expect(page.locator('body')).toHaveCSS('background-color', LIGHT_BG);
    await expect(page.locator('#homeThemeToggle')).toHaveAttribute('aria-label', 'Switch to dark theme');
  });

  test('should switch theme with the toggle and remember the choice', async ({ page }) => {
    await page.emulateMedia({ colorScheme: 'dark' });
    await page.goto('/');

    await page.locator('#homeThemeToggle').click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
    await expect(page.locator('body')).toHaveCSS('background-color', LIGHT_BG);
    await expect(page.locator('text=Student Space')).toBeVisible();
    await expect(page.locator('text=Admin Space')).toBeVisible();

    // The stored choice wins over the OS preference after a reload
    await page.reload();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');

    await page.locator('#homeThemeToggle').click();
    await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
    await expect(page.locator('body')).toHaveCSS('background-color', DARK_BG);
  });
});

const LOGIN_PAGES = [
  { name: 'admin login', path: '/login' },
  { name: 'student login', path: '/student/login' },
];

for (const loginPage of LOGIN_PAGES) {
  test.describe(`Login Page Theme (${loginPage.name})`, () => {
    test('should follow a dark OS preference by default', async ({ page }) => {
      await page.emulateMedia({ colorScheme: 'dark' });
      await page.goto(loginPage.path);

      await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
      await expect(page.locator('body')).toHaveCSS('background-color', DARK_BG);
      await expect(page.locator('#homeThemeToggle')).toHaveAttribute('aria-label', 'Switch to light theme');
    });

    test('should follow a light OS preference by default', async ({ page }) => {
      await page.emulateMedia({ colorScheme: 'light' });
      await page.goto(loginPage.path);

      await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
      await expect(page.locator('body')).toHaveCSS('background-color', LIGHT_BG);
      await expect(page.locator('#homeThemeToggle')).toHaveAttribute('aria-label', 'Switch to dark theme');
    });

    test('should switch theme with the toggle and keep the form usable', async ({ page }) => {
      await page.emulateMedia({ colorScheme: 'dark' });
      await page.goto(loginPage.path);

      await page.locator('#homeThemeToggle').click();
      await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
      await expect(page.locator('body')).toHaveCSS('background-color', LIGHT_BG);
      await expect(page.locator('#password')).toBeVisible();
      await expect(page.locator('#password')).toHaveCSS('color', 'rgb(12, 37, 48)');

      await page.locator('#homeThemeToggle').click();
      await expect(page.locator('html')).toHaveAttribute('data-theme', 'dark');
      await expect(page.locator('body')).toHaveCSS('background-color', DARK_BG);
    });

    test('should keep the theme chosen on the homepage', async ({ page }) => {
      await page.emulateMedia({ colorScheme: 'dark' });
      await page.goto('/');
      await page.locator('#homeThemeToggle').click();
      await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');

      await page.goto(loginPage.path);
      await expect(page.locator('html')).toHaveAttribute('data-theme', 'light');
      await expect(page.locator('body')).toHaveCSS('background-color', LIGHT_BG);
    });
  });
}
