// The restriction of a new token has no default: the user picks a role or
// "no restriction" explicitly.
import { test, expect } from '@playwright/test';
import { ADMIN, signIn } from './env';

test('new token needs an explicit restriction', async ({ page }) => {
  await signIn(page, ADMIN);
  await page.goto('/tokens');
  await page.getByTestId('token-new').click();
  const form = page.locator('#tok-create-form');
  await form.getByPlaceholder('github actions · acme-api').fill(`e2e-restriction-${Date.now().toString(36)}`);

  const generate = page.getByTestId('token-generate');
  await expect(generate).toBeDisabled();
  await expect(form.locator('.tok-scope-active')).toHaveCount(0);

  await form.getByTestId('token-unrestricted').click();
  await expect(form.getByTestId('token-unrestricted-warning')).toBeVisible();
  await expect(generate).toBeEnabled();

  const res = page.waitForResponse((r) => r.url().endsWith('/api/v1/settings/api-tokens') && r.request().method() === 'POST');
  await generate.click();
  const post = await res;
  expect(post.status()).toBe(201);
  expect(post.request().postDataJSON().role).toBeUndefined();
});
