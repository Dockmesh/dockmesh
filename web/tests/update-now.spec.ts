// Settings › Updates: the Update now dialog against a mocked update API,
// one test per outcome the root updater can report, plus the hosts where
// the button is replaced by a hint.
import { test, expect, type Page, type Route } from '@playwright/test';
import { ADMIN, signIn } from './env';

const status = (extra: Record<string, unknown> = {}) => ({
  current_version: 'v0.6.0',
  latest_version: 'v0.6.1',
  latest_prerelease: false,
  channel: 'stable',
  update_available: true,
  is_dev_build: false,
  release_url: '',
  release_notes: '',
  enabled: true,
  ...extra
});

type State = Record<string, unknown>;

// mockUpdate serves the update status and answers the install endpoint
// with the next state of `states` on every poll; a null entry fails the
// request like a server that is restarting.
async function mockUpdate(page: Page, opts: { status?: object; initial: State; states?: (State | null)[] }) {
  const states = [...(opts.states ?? [])];
  let started = false;
  await page.route('**/api/v1/system/update-status', (r) => r.fulfill({ json: opts.status ?? status() }));
  await page.route('**/api/v1/system/update/install', (r: Route) => {
    if (r.request().method() === 'POST') {
      started = true;
      return r.fulfill({ status: 202, json: { phase: 'downloading', version: 'v0.6.1', started: '2026-11-14T19:59:00Z' } });
    }
    if (!started) return r.fulfill({ json: opts.initial });
    const next = states.length > 1 ? states.shift() : states[0];
    if (next === null || next === undefined) return r.fulfill({ status: 502, body: 'bad gateway' });
    return r.fulfill({ json: next });
  });
}

async function openDialog(page: Page) {
  await page.goto('/settings/updates');
  await page.getByTestId('update-now').click();
  await expect(page.getByTestId('update-dialog')).toHaveAttribute('data-step', 'confirm');
  await page.getByTestId('update-start').click();
}

const result = (status: string, reason = '') => ({
  phase: 'idle',
  last_result: { status, from: 'v0.6.0', to: 'v0.6.1', reason, finished: '2026-11-14T20:00:00Z' }
});

test.beforeEach(async ({ page }) => {
  await signIn(page, ADMIN);
});

test('update goes through every step and reports updated', async ({ page }) => {
  await mockUpdate(page, {
    initial: { phase: 'idle' },
    states: [
      { phase: 'waiting_for_stacks', version: 'v0.6.1' },
      { phase: 'applying', version: 'v0.6.1' },
      null,
      { phase: 'idle', pending: true },
      result('updated')
    ]
  });
  await openDialog(page);
  const dlg = page.getByTestId('update-dialog');
  for (const step of ['waiting_for_stacks', 'applying', 'restarting', 'checking']) {
    await expect(dlg).toHaveAttribute('data-step', step, { timeout: 10_000 });
  }
  await expect(page.getByTestId('update-result')).toHaveAttribute('data-status', 'updated', { timeout: 10_000 });
  await expect(page.getByTestId('update-audit')).toHaveAttribute('href', '/audit?category=system');
});

test('a bad build is reported as rolled back with the reason', async ({ page }) => {
  await mockUpdate(page, {
    initial: { phase: 'idle' },
    states: [{ phase: 'applying', version: 'v0.6.1' }, null, { phase: 'idle', pending: true }, result('rolled_back', 'health: no answer within 180s')]
  });
  await openDialog(page);
  const res = page.getByTestId('update-result');
  await expect(res).toHaveAttribute('data-status', 'rolled_back', { timeout: 15_000 });
  await expect(res).toContainText('health: no answer within 180s');
});

test('a refusal by the root updater is shown without a restart', async ({ page }) => {
  await mockUpdate(page, {
    initial: { phase: 'idle' },
    states: [{ phase: 'failed', version: 'v0.6.1', error: 'the update was refused: signature', ...result('refused', 'verify: bad signature') }]
  });
  await openDialog(page);
  const res = page.getByTestId('update-result');
  await expect(res).toHaveAttribute('data-status', 'refused', { timeout: 10_000 });
  await expect(res).toContainText('verify: bad signature');
});

test('a failed download ends the dialog with the error', async ({ page }) => {
  await mockUpdate(page, {
    initial: { phase: 'idle' },
    states: [{ phase: 'failed', version: 'v0.6.1', error: 'download checksums.txt: HTTP 404' }]
  });
  await openDialog(page);
  const res = page.getByTestId('update-result');
  await expect(res).toHaveAttribute('data-status', 'error', { timeout: 10_000 });
  await expect(res).toContainText('HTTP 404');
  await expect(page.getByTestId('update-audit')).toHaveCount(0);
});

test('a security release is marked on the page and in the dialog', async ({ page }) => {
  await mockUpdate(page, {
    status: status({ severity: 'security', security: ['GHSA-aaaa (high): token leak'] }),
    initial: { phase: 'idle' }
  });
  await page.goto('/settings/updates');
  await expect(page.getByTestId('update-box')).toContainText('GHSA-aaaa (high): token leak');
  await page.getByTestId('update-now').click();
  await expect(page.getByTestId('update-security')).toContainText('token leak');
});

test('no button in a container or without the root updater', async ({ page }) => {
  await mockUpdate(page, { initial: { phase: 'idle', unavailable: 'container' } });
  await page.goto('/settings/updates');
  await expect(page.getByTestId('update-container')).toBeVisible();
  await expect(page.getByTestId('update-now')).toHaveCount(0);

  await page.unrouteAll();
  await mockUpdate(page, { initial: { phase: 'idle', unavailable: 'no_updater' } });
  await page.goto('/settings/updates');
  await expect(page.getByTestId('update-no-updater')).toBeVisible();
  await expect(page.getByTestId('update-now')).toHaveCount(0);
  await expect(page.locator('.up-cmd code')).toHaveText('curl -fsSL https://get.dockmesh.dev | sudo bash');
});

test('an install already running opens the dialog on its step', async ({ page }) => {
  await mockUpdate(page, { initial: { phase: 'waiting_for_stacks', version: 'v0.6.1' } });
  await page.goto('/settings/updates');
  await expect(page.getByTestId('update-dialog')).toHaveAttribute('data-step', 'waiting_for_stacks');
});
