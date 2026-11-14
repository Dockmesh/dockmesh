// Paths tab: add, edit and delete path routes; the table shows them in
// the order Caddy applies them, before and after saving.
import { test, expect, type Page } from '@playwright/test';
import { ADMIN, password, signIn } from './env';

async function token(page: Page): Promise<string> {
  const login = await page.request.post('/api/v1/auth/login', { data: { username: ADMIN, password: password() } });
  return (await login.json()).access_token;
}

async function storedPaths(page: Page, host: string) {
  const res = await page.request.get('/api/v1/proxy/routes', { headers: { Authorization: `Bearer ${await token(page)}` } });
  const site = (await res.json()).find((r: { host: string }) => r.host === host);
  return (site.paths as { match: { type: string; path: string }; action: string; to?: string }[]).map((p) => `${p.match.type} ${p.match.path} ${p.action}`);
}

async function addPath(page: Page, type: 'prefix' | 'exact', path: string) {
  await page.getByTestId('paths-add').first().click();
  const ed = page.getByTestId('path-editor');
  if (type === 'exact') await ed.locator('input[type=radio][value=exact]').check();
  await ed.locator('input[id$="-path"]').fill(path);
  await ed.locator('input[id$="-path"]').blur();
}

const rowPaths = (page: Page) => page.getByTestId('path-row').locator('[data-testid=path-match]').allInnerTexts();

test('paths are added, edited, deleted and shown in Caddy order', async ({ page }, info) => {
  const host = `paths-${info.project.name}-${Date.now()}.example.com`;
  await signIn(page, ADMIN);
  const res = await page.request.post('/api/v1/proxy/routes', {
    headers: { Authorization: `Bearer ${await token(page)}` },
    data: { host, upstream: '10.0.0.5:8080', tls_mode: 'auto' }
  });
  expect(res.ok(), await res.text()).toBeTruthy();

  await page.goto(`/proxy/${host}?tab=paths`);
  await expect(page.getByTestId('paths-empty')).toBeVisible();
  await expect(page.getByTestId('path-fallback')).toContainText('10.0.0.5:8080');

  await addPath(page, 'prefix', '/ords');
  await addPath(page, 'prefix', 'ords/r/');
  await addPath(page, 'exact', '/health');
  await expect.poll(() => rowPaths(page)).toEqual(['/health', '/ords/r', '/ords']);

  await page.getByTestId('paths-save').click();
  await expect.poll(() => storedPaths(page, host)).toEqual(['exact /health proxy', 'prefix /ords/r proxy', 'prefix /ords proxy']);
  await expect.poll(() => rowPaths(page)).toEqual(['/health', '/ords/r', '/ords']);

  const ordsR = page.getByTestId('path-row').filter({ hasText: '/ords/r' });
  await ordsR.getByTestId('path-edit').click();
  const ed = page.getByTestId('path-editor');
  await ed.locator('input[type=radio][value=redirect]').check();
  await ed.locator('input[id$="-to"]').fill('/ords/r/app/home');
  await page.getByTestId('path-row').filter({ hasText: '/health' }).getByTestId('path-delete').click();
  await page.getByTestId('paths-save').click();
  await expect.poll(() => storedPaths(page, host)).toEqual(['prefix /ords/r redirect', 'prefix /ords proxy']);
  await expect(page.getByTestId('path-row').filter({ hasText: '/ords/r' })).toContainText('/ords/r/app/home');

  await addPath(page, 'prefix', '/ords');
  await page.getByTestId('paths-save').click();
  await expect(page.getByTestId('path-editor').locator('.ferr')).toBeVisible();
  await expect.poll(() => storedPaths(page, host)).toEqual(['prefix /ords/r redirect', 'prefix /ords proxy']);
});
