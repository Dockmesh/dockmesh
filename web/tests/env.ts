import type { Page } from '@playwright/test';

export const ADMIN = process.env.DOCKMESH_E2E_USER ?? 'admin';

export function password(): string {
  const p = process.env.DOCKMESH_E2E_PASSWORD;
  if (!p) throw new Error('set DOCKMESH_E2E_PASSWORD to the admin password of the instance under test');
  return p;
}

// Users the smoke test signs in as; global setup creates them with the
// admin's password.
export const ROLE_USERS = { operator: 'e2e-operator', viewer: 'e2e-viewer' } as const;

export async function signIn(page: Page, user: string) {
  await page.goto('/login');
  await page.locator('#login-username').fill(user);
  await page.locator('#login-password').fill(password());
  await page.locator('form button[type="submit"]').click();
  await page.waitForURL((u) => !u.pathname.startsWith('/login'));
}
