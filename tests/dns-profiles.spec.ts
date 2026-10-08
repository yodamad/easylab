import { test, expect } from '@playwright/test';

/**
 * DNS profiles and the credential storage behind them (/admin/dns).
 *
 * A profile saves a set of DNS provider credentials under a name, so the lab
 * wizard can offer it instead of asking for the credentials again. Profiles are
 * only kept across restarts once credential storage has a key that is not on
 * disk — here, a passphrase — and are unavailable while it is locked.
 *
 * One serial flow, because every step builds on the state the previous one left:
 * the storage is server-side and shared by the whole run. It resets the storage
 * first and last so it neither depends on nor leaves anything behind.
 */
test.describe.configure({ mode: 'serial' });

test.describe('DNS profiles', () => {
  const passphrase = 'playwright storage passphrase';
  const profileName = 'Playwright zone';

  test.beforeEach(async ({ page }) => {
    await page.goto('/login');
    await page.locator('input[type="password"]').fill('testpassword');
    await page.locator('button[type="submit"]').click();
    await page.waitForLoadState('networkidle');
  });

  const storage = (page: any) => page.locator('#credential-storage');

  // Wipes credential storage when there is anything to wipe.
  async function resetStorage(page: any) {
    await page.goto('/admin/dns');
    const confirm = page.locator('#vault_reset_confirm');
    if ((await confirm.count()) === 0) return;
    await page.locator('summary', { hasText: 'Reset credential storage' }).click();
    await confirm.fill('RESET');
    await page.getByRole('button', { name: 'Reset credential storage' }).click();
    await expect(storage(page)).toHaveAttribute('data-state', 'uninitialized');
  }

  async function goToWizardDNSStep(page: any) {
    await page.goto('/admin');
    await page.evaluate(() => {
      (window as any).wizard.currentStep = 4;
      (window as any).wizard.updateUI();
    });
    await page.locator('#domain-mode-auto-btn').click();
    await expect(page.locator('#dns_profile')).toBeVisible();
  }

  test('starts in memory only once reset', async ({ page }) => {
    await resetStorage(page);
    await expect(storage(page)).toContainText('in memory only');
    await expect(page.locator('#dns-profiles-list')).toContainText('No DNS profile yet');
  });

  test('adds a profile from the provider\'s own fields', async ({ page }) => {
    await page.goto('/admin/dns');
    await page.locator('#profile_name').fill(profileName);
    await page.locator('#profile_provider').selectOption('ovh');

    // The inputs come from the DNS provider registry, secrets as password fields.
    await expect(page.locator('#profile_cred_ovhAppSecret')).toHaveAttribute('type', 'password');
    await page.locator('#profile_zone').fill('example.com');
    await page.locator('#profile_cred_ovhAppKey').fill('pw-app-key');
    await page.locator('#profile_cred_ovhAppSecret').fill('pw-app-secret');
    await page.locator('#profile_cred_ovhConsumerKey').fill('pw-consumer-key');
    await page.getByRole('button', { name: 'Add profile' }).click();

    const row = page.locator('#dns-profiles-list tbody tr', { hasText: profileName });
    await expect(row).toContainText('OVH DNS');
    await expect(row).toContainText('example.com');
    // No secret ever comes back to the page.
    await expect(page.locator('body')).not.toContainText('pw-app-secret');
  });

  test('the wizard offers the profile in place of the credential fields', async ({ page }) => {
    await goToWizardDNSStep(page);

    await expect(page.locator('#dns_cred_ovhAppKey')).toBeHidden();
    await page.locator('#dns_profile').selectOption({ label: `${profileName} (OVH DNS)` });

    await expect(page.locator('#dns_provider')).toHaveValue('ovh');
    await expect(page.locator('#dns_zone')).toHaveValue('example.com');
    await expect(page.locator('#dns-ovh-fields')).toBeHidden();

    // Back to manual: the fields return. Another provider drops the profile.
    await page.locator('#dns_profile').selectOption('');
    await expect(page.locator('#dns-ovh-fields')).toBeVisible();
    await page.locator('#dns_profile').selectOption({ label: `${profileName} (OVH DNS)` });
    await page.locator('#dns_provider').selectOption('azure');
    await expect(page.locator('#dns_profile')).toHaveValue('');
    await expect(page.locator('#dns-azure-fields')).toBeVisible();
  });

  test('a passphrase saves the profile, and locking hides it from the wizard', async ({ page }) => {
    await page.goto('/admin/dns');
    await page.locator('#vault_passphrase').fill(passphrase);
    await page.locator('#vault_passphrase_confirm').fill(passphrase);
    await page.getByRole('button', { name: 'Save credentials encrypted' }).click();
    await expect(storage(page)).toHaveAttribute('data-state', 'unlocked');
    await expect(page.locator('#dns-profiles-list')).toContainText(profileName);

    await page.locator('summary', { hasText: 'Change passphrase or lock' }).click();
    await page.getByRole('button', { name: 'Lock now' }).click();
    await expect(storage(page)).toHaveAttribute('data-state', 'locked');

    // Still listed by name, but not editable and not offered to the wizard.
    await expect(page.locator('#dns-profiles-list')).toContainText(profileName);
    await expect(page.locator('#dns-profile-form')).toHaveCount(0);

    await goToWizardDNSStep(page);
    await expect(page.locator('#dns_profile_group')).toContainText('Saved DNS profiles are locked');
    await expect(page.locator('#dns_profile option')).toHaveCount(1);
  });

  test('a wrong passphrase is refused, the right one unlocks', async ({ page }) => {
    await page.goto('/admin/dns');
    await page.locator('#vault_unlock_passphrase').fill('not the passphrase');
    await page.getByRole('button', { name: 'Unlock' }).click();
    await expect(page.locator('#credential-storage-response')).toContainText('not correct');
    await expect(storage(page)).toHaveAttribute('data-state', 'locked');

    await page.locator('#vault_unlock_passphrase').fill(passphrase);
    await page.getByRole('button', { name: 'Unlock' }).click();
    await expect(storage(page)).toHaveAttribute('data-state', 'unlocked');
    await expect(page.locator('#dns-profile-form')).toBeVisible();
  });

  test('reset deletes the saved profile', async ({ page }) => {
    await resetStorage(page);
    await expect(page.locator('#dns-profiles-list')).toContainText('No DNS profile yet');
  });
});
