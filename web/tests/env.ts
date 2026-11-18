import type { APIRequestContext, Page } from '@playwright/test';

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

// trackCreatedHosts records the hosts a test creates through POST /agents
// and returns a function that deletes them again, so later specs (the
// smoke pages) don't see never-connected hosts.
export function trackCreatedHosts(page: Page): (request: APIRequestContext) => Promise<void> {
  const ids: string[] = [];
  page.on('response', async (r) => {
    if (r.request().method() === 'POST' && /\/api\/v1\/agents$/.test(r.url()) && r.status() === 201) {
      ids.push((await r.json()).agent.id);
    }
  });
  return async (request) => {
    if (!ids.length) return;
    const login = await request.post('/api/v1/auth/login', { data: { username: ADMIN, password: password() } });
    const { access_token: token } = await login.json();
    for (const id of ids) await request.delete(`/api/v1/agents/${id}`, { headers: { Authorization: `Bearer ${token}` } });
  };
}
