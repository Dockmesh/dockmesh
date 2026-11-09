// The YAML download needs the session token, which a plain link can't send.
import { test, expect } from '@playwright/test';
import { ADMIN, password } from './env';

test('a template downloads as YAML with the session', async ({ page }) => {
  await page.goto('/login');
  await page.locator('#login-username').fill(ADMIN);
  await page.locator('#login-password').fill(password());
  await page.locator('form button[type="submit"]').click();
  await page.waitForURL((u) => !u.pathname.startsWith('/login'));

  await page.goto('/templates');
  await page.locator('.tp-actions [aria-haspopup="menu"]').first().click();
  const [download] = await Promise.all([
    page.waitForEvent('download'),
    page.getByRole('menuitem', { name: /YAML/ }).click()
  ]);
  expect(download.suggestedFilename()).toMatch(/\.yaml$/);
  const body = await (await import('node:fs/promises')).readFile(await download.path(), 'utf8');
  expect(body).toContain('compose');
});
