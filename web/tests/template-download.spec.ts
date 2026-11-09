// The YAML download needs the session token, which a plain link can't send.
import { test, expect } from '@playwright/test';
import { readFile } from 'node:fs/promises';
import { ADMIN, signIn } from './env';

test('a template downloads as YAML with the session', async ({ page }) => {
  await signIn(page, ADMIN);

  await page.goto('/templates');
  await page.locator('.tp-actions [aria-haspopup="menu"]').first().click();
  const [download] = await Promise.all([
    page.waitForEvent('download'),
    page.getByRole('menuitem', { name: /YAML/ }).click()
  ]);
  expect(download.suggestedFilename()).toMatch(/\.yaml$/);
  const body = await readFile(await download.path(), 'utf8');
  expect(body).toContain('compose');
});
