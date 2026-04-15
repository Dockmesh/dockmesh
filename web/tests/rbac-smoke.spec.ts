// RBAC smoke test — clicks every route / tab as every role and asserts
// zero console errors and no 4xx/5xx on /api/v1 calls.
//
// Runs against a live Dockmesh server. Defaults to http://192.168.10.164:8080
// (the test VM). Override with BASE_URL=http://localhost:8080.
//
// Required: users `admin`, `bob` (operator), `eve` (viewer) all with
// password `admin123#`. Reset via:
//   sqlite3 /home/dockmesh/dockmesh/data/dockmesh.db \
//     "UPDATE users SET password = '<argon2-hash-of-admin123#>' \
//      WHERE username IN ('admin','bob','eve')"
//
// First-time setup:
//   cd web
//   npm i -D @playwright/test
//   npx playwright install chromium
//
// Run:
//   npx playwright test tests/rbac-smoke.spec.ts
//   BASE_URL=http://localhost:8080 npx playwright test tests/rbac-smoke.spec.ts

import { test, expect, type Page, type ConsoleMessage, type Response } from '@playwright/test';

const BASE = process.env.BASE_URL ?? 'http://192.168.10.164:8080';
const PASSWORD = 'admin123#';

type Role = 'admin' | 'operator' | 'viewer';

interface RoleSpec {
  user: string;
  role: Role;
  // Routes the role is allowed to navigate to without redirect.
  allowed: string[];
  // Routes the role is NOT allowed to see — must redirect to `/`.
  forbidden: string[];
  // Settings tabs the role should see in the UI (filter to visible=true).
  settingsTabs: string[];
  // Container-detail tabs the role should see.
  containerTabs: string[];
}

const ROLES: RoleSpec[] = [
  {
    user: 'admin',
    role: 'admin',
    allowed: ['/', '/stacks', '/containers', '/images', '/networks', '/proxy', '/agents', '/alerts', '/backups', '/settings'],
    forbidden: [],
    settingsTabs: ['Account', 'Users', 'SSO', 'Audit Log'],
    containerTabs: ['Logs', 'Terminal', 'Stats', 'Updates', 'Inspect']
  },
  {
    user: 'bob',
    role: 'operator',
    allowed: ['/', '/stacks', '/containers', '/images', '/networks', '/settings'],
    forbidden: ['/proxy', '/agents', '/alerts', '/backups'],
    settingsTabs: ['Account', 'Audit Log'],
    containerTabs: ['Logs', 'Terminal', 'Stats', 'Updates', 'Inspect']
  },
  {
    user: 'eve',
    role: 'viewer',
    allowed: ['/', '/stacks', '/containers', '/images', '/networks', '/settings'],
    forbidden: ['/proxy', '/agents', '/alerts', '/backups'],
    settingsTabs: ['Account'],
    containerTabs: ['Logs', 'Stats', 'Inspect']
  }
];

// Any console error matching this list is considered acceptable and ignored.
// Keep tight — the whole point of the test is to catch noise.
const IGNORED_ERROR_PATTERNS: RegExp[] = [
  // Favicon misses on some builds — irrelevant to RBAC.
  /favicon\.svg.*404/
];

function installHooks(page: Page): { errors: string[]; apiFailures: string[] } {
  const errors: string[] = [];
  const apiFailures: string[] = [];

  page.on('console', (msg: ConsoleMessage) => {
    if (msg.type() !== 'error') return;
    const text = msg.text();
    if (IGNORED_ERROR_PATTERNS.some((re) => re.test(text))) return;
    errors.push(text);
  });

  page.on('pageerror', (err) => {
    errors.push(`pageerror: ${err.message}`);
  });

  page.on('response', (res: Response) => {
    const url = res.url();
    if (!url.includes('/api/v1/')) return;
    const status = res.status();
    if (status >= 400) {
      apiFailures.push(`${status} ${res.request().method()} ${url.replace(BASE, '')}`);
    }
  });

  return { errors, apiFailures };
}

async function login(page: Page, username: string) {
  await page.goto(`${BASE}/login`);
  await page.getByRole('textbox', { name: 'Username' }).fill(username);
  await page.getByRole('textbox', { name: 'Password' }).fill(PASSWORD);
  await page.getByRole('button', { name: 'Sign in', exact: true }).click();
  await page.waitForURL(`${BASE}/`);
}

async function logout(page: Page) {
  await page.evaluate(() => localStorage.clear());
}

for (const spec of ROLES) {
  test.describe(`RBAC smoke — ${spec.role} (${spec.user})`, () => {
    test.beforeEach(async ({ page }) => {
      await logout(page);
    });

    test('navigation + console + API clean on allowed routes', async ({ page }) => {
      const { errors, apiFailures } = installHooks(page);
      await login(page, spec.user);

      for (const route of spec.allowed) {
        await page.goto(`${BASE}${route}`);
        await page.waitForLoadState('networkidle');
      }

      expect(errors, `console errors seen: ${errors.join(' | ')}`).toEqual([]);
      expect(apiFailures, `API 4xx/5xx seen: ${apiFailures.join(' | ')}`).toEqual([]);
    });

    if (spec.forbidden.length > 0) {
      test('forbidden routes redirect to /', async ({ page }) => {
        const { errors, apiFailures } = installHooks(page);
        await login(page, spec.user);

        for (const route of spec.forbidden) {
          await page.goto(`${BASE}${route}`);
          // Give the $effect guard a beat to fire.
          await page.waitForURL(`${BASE}/`, { timeout: 3000 });
        }

        expect(errors, `console errors seen: ${errors.join(' | ')}`).toEqual([]);
        expect(apiFailures, `API 4xx/5xx seen: ${apiFailures.join(' | ')}`).toEqual([]);
      });
    }

    test('settings tab visibility', async ({ page }) => {
      await login(page, spec.user);
      await page.goto(`${BASE}/settings`);
      await page.waitForLoadState('networkidle');

      // Every expected tab should be present exactly once.
      for (const tab of spec.settingsTabs) {
        await expect(page.getByRole('button', { name: tab, exact: true })).toBeVisible();
      }

      // Tabs that shouldn't be shown.
      const allTabs = ['Account', 'Users', 'SSO', 'Audit Log'];
      const hidden = allTabs.filter((t) => !spec.settingsTabs.includes(t));
      for (const tab of hidden) {
        await expect(page.getByRole('button', { name: tab, exact: true })).toHaveCount(0);
      }
    });

    test('container-detail tab visibility + click-through clean', async ({ page }) => {
      const { errors, apiFailures } = installHooks(page);
      await login(page, spec.user);

      // Pick the first running container from the list page.
      await page.goto(`${BASE}/containers`);
      await page.waitForLoadState('networkidle');

      const containerId = await page.evaluate(() => {
        // Container rows are buttons — aria-label contains name + state.
        // Fall back to scraping the top row's onclick target via a DOM probe.
        const rows = Array.from(document.querySelectorAll('button[aria-label]')).filter((b) =>
          (b as HTMLElement).getAttribute('aria-label')?.includes('Up')
        );
        if (rows.length === 0) return null;
        // Svelte navigates via goto() inside onclick — we can't easily read the
        // target from DOM. Instead, fetch the API list directly via the same
        // session to find a running container ID.
        return null;
      });

      // Simpler: hit the API directly for a running container.
      const apiContainers = await page.evaluate(async (base: string) => {
        const auth = JSON.parse(localStorage.getItem('dockmesh_auth') || '{}');
        const r = await fetch(`${base}/api/v1/containers?all=true`, {
          headers: { Authorization: `Bearer ${auth.accessToken}` }
        });
        if (!r.ok) return [];
        return r.json();
      }, BASE);

      const running = (apiContainers as Array<{ Id: string; State: string }>).find(
        (c) => c.State === 'running'
      );
      if (!running) {
        test.skip(true, 'no running container on test target — start one to run this spec');
        return;
      }

      await page.goto(`${BASE}/containers/${running.Id}`);
      await page.waitForLoadState('networkidle');

      for (const tab of spec.containerTabs) {
        const btn = page.getByRole('button', { name: tab, exact: true });
        await expect(btn).toBeVisible();
        await btn.click();
        await page.waitForLoadState('networkidle');
      }

      // Hidden tabs shouldn't be there.
      const allTabs = ['Logs', 'Terminal', 'Stats', 'Updates', 'Inspect'];
      const hidden = allTabs.filter((t) => !spec.containerTabs.includes(t));
      for (const tab of hidden) {
        await expect(page.getByRole('button', { name: tab, exact: true })).toHaveCount(0);
      }

      expect(errors, `console errors seen: ${errors.join(' | ')}`).toEqual([]);
      expect(apiFailures, `API 4xx/5xx seen: ${apiFailures.join(' | ')}`).toEqual([]);
    });
  });
}
