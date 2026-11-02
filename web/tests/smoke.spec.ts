// Every page as every role: no console errors and no failing API calls,
// in each language project.
import { test, expect, type Page } from '@playwright/test';
import { ADMIN, ROLE_USERS, password } from './env';

const OVERVIEW = { en: 'Overview', de: 'Übersicht' } as const;

interface RoleSpec {
  user: string;
  pages: string[];
}

const ALL_PAGES = [
  '/', '/activity', '/stacks', '/containers', '/templates', '/hosts', '/resources', '/topology',
  '/updates', '/backups', '/proxy', '/alerts', '/users', '/audit', '/account', '/tokens'
];

const ROLES: RoleSpec[] = [
  { user: ADMIN, pages: [...ALL_PAGES, '/settings', '/settings/updates', '/settings/encryption', '/authentication', '/registries', '/environment'] },
  { user: ROLE_USERS.operator, pages: ['/', '/activity', '/stacks', '/containers', '/hosts', '/resources', '/account', '/tokens'] },
  { user: ROLE_USERS.viewer, pages: ['/', '/activity', '/stacks', '/containers', '/account'] }
];

async function signIn(page: Page, user: string) {
  await page.goto('/login');
  await page.locator('#login-username').fill(user);
  await page.locator('#login-password').fill(password());
  await page.locator('form button[type="submit"]').click();
  await page.waitForURL((u) => !u.pathname.startsWith('/login'));
}

function watch(page: Page): string[] {
  const problems: string[] = [];
  page.on('console', (msg) => {
    if (msg.type() === 'error') problems.push(`console: ${msg.text()}`);
  });
  page.on('pageerror', (err) => problems.push(`page error: ${err.message}`));
  page.on('response', (res) => {
    const url = new URL(res.url());
    // Rapid page loads can hit the reachability rate limit; that is not a page bug.
    if (url.pathname.startsWith('/api/v1/') && res.status() >= 400 && res.status() !== 429) {
      problems.push(`${res.request().method()} ${url.pathname} → ${res.status()}`);
    }
  });
  return problems;
}

for (const role of ROLES) {
  test(`pages as ${role.user}`, async ({ page }, info) => {
    const lang = info.project.name as keyof typeof OVERVIEW;
    const problems = watch(page);
    await signIn(page, role.user);
    await expect(page.getByRole('navigation').getByText(OVERVIEW[lang], { exact: true }).first()).toBeVisible();
    for (const path of role.pages) {
      await page.goto(path);
      await page.waitForLoadState('networkidle');
      await expect(page.locator('body')).not.toContainText('undefined');
    }
    expect(problems).toEqual([]);
  });
}

test('the saved language wins over the browser language', async ({ page }, info) => {
  test.skip(info.project.name !== 'en', 'switches from the English browser language');
  await signIn(page, ADMIN);
  try {
    await page.goto('/account');
    await Promise.all([page.waitForEvent('load'), page.getByTestId('language-select').selectOption('de')]);
    await expect(page.getByRole('navigation').getByText('Übersicht', { exact: true }).first()).toBeVisible();
    await page.goto('/stacks');
    await expect(page.locator('html')).toHaveAttribute('lang', 'de');
  } finally {
    const token = await page.evaluate(() => JSON.parse(localStorage.getItem('dockmesh_auth') ?? '{}').accessToken);
    await page.request.put('/api/v1/me/language', {
      headers: { Authorization: `Bearer ${token}` },
      data: { language: '' }
    });
  }
});
