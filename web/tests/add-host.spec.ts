// Hosts › Add host: create with name and tags against the real API, then
// the agent's side (connect, setup state, reachability) mocked on
// GET /agents/{id}.
import { test, expect, type Page } from '@playwright/test';
import { ADMIN, signIn, trackCreatedHosts } from './env';

type AgentState = Record<string, unknown> | null;

// mockAgent answers GET /agents/{id} with `state()` once it returns a
// value; until then the real (pending) host is served.
async function mockAgent(page: Page, state: () => AgentState) {
  await page.route(/\/api\/v1\/agents\/[0-9a-f-]{36}$/, async (route) => {
    if (route.request().method() !== 'GET') return route.continue();
    const res = await route.fetch();
    const real = await res.json();
    const s = state();
    return route.fulfill({ json: s ? { ...real, ...s } : real });
  });
}

async function openDialog(page: Page) {
  await page.goto('/hosts');
  await page.getByTestId('add-host').click();
  const dlg = page.getByTestId('add-host-dialog');
  await expect(dlg).toHaveAttribute('data-step', 'create');
  return dlg;
}

const uniqueName = (p: string) => `${p}-${Date.now().toString(36)}`;

let cleanup: Awaited<ReturnType<typeof trackCreatedHosts>> | null = null;

test.beforeEach(async ({ page }) => {
  cleanup = trackCreatedHosts(page);
  await signIn(page, ADMIN);
});

test.afterEach(async ({ request }) => {
  await cleanup?.(request);
});

test('create, new command, automation, then connected and waiting for setup', async ({ page }) => {
  let state: AgentState = null;
  await mockAgent(page, () => state);
  const dlg = await openDialog(page);
  const name = uniqueName('e2e-setup');
  await dlg.locator('#add-host-name').fill(name);
  await dlg.locator('#add-host-tag').fill('edge');
  await dlg.locator('#add-host-tag').press('Enter');
  const created = page.waitForResponse((r) => r.url().endsWith('/api/v1/agents') && r.request().method() === 'POST');
  await dlg.getByTestId('add-host-create').click();
  const body = (await created).request().postDataJSON();
  expect(body).toEqual({ name, tags: ['edge'] });

  await expect(dlg).toHaveAttribute('data-step', 'command');
  await expect(dlg.getByTestId('add-host-summary')).toContainText(name);
  await expect(dlg.getByTestId('add-host-summary')).toContainText('edge');
  const cmd = dlg.getByTestId('install-command');
  await expect(cmd).toContainText('--pinnedpubkey');
  await expect(cmd).toContainText('--token');
  const first = await cmd.textContent();

  await dlg.getByTestId('add-host-new-command').click();
  await expect(cmd).not.toHaveText(first ?? '');

  await expect(dlg.getByTestId('add-host-flags')).toHaveCount(0);
  await dlg.getByTestId('add-host-automation').click();
  const flags = dlg.getByTestId('add-host-flags');
  for (const f of ['--data-dir', '--stacks-dir', '--skip-docker', 'POST /api/v1/agents']) await expect(flags).toContainText(f);
  await expect(dlg.getByTestId('add-host-waiting')).toContainText(name);

  state = { status: 'online', needs_setup: true, stacks_dirs: [{ path: '/var/lib/dockmesh-agent/stacks', current: true, stacks: 0 }] };
  await expect(dlg).toHaveAttribute('data-step', 'setup');
  await dlg.getByTestId('add-host-setup').click();
  await expect(page).toHaveURL(/\/hosts\/[0-9a-f-]{36}\?tab=setup$/);
});

test('a stacks directory from the install ends with Open host', async ({ page }) => {
  let state: AgentState = null;
  await mockAgent(page, () => state);
  const dlg = await openDialog(page);
  await dlg.getByTestId('add-host-create').click();
  await expect(dlg).toHaveAttribute('data-step', 'command');
  await expect(dlg.getByTestId('add-host-summary')).not.toBeEmpty();

  state = { status: 'online', name: 'nas-01', needs_setup: false, stacks_dirs: [{ path: '/srv/stacks', current: true, stacks: 0 }] };
  await expect(dlg).toHaveAttribute('data-step', 'ready');
  await expect(dlg.getByTestId('add-host-ready')).toContainText('/srv/stacks');
  await expect(dlg.getByTestId('add-host-summary')).toContainText('nas-01');
  await dlg.getByTestId('add-host-open').click();
  await expect(page).toHaveURL(/\/hosts\/[0-9a-f-]{36}$/);
});

test('a taken name is shown under the field', async ({ page }) => {
  const name = uniqueName('e2e-taken');
  let dlg = await openDialog(page);
  await dlg.locator('#add-host-name').fill(name);
  await dlg.getByTestId('add-host-create').click();
  await expect(dlg).toHaveAttribute('data-step', 'command');

  dlg = await openDialog(page);
  await dlg.locator('#add-host-name').fill(name);
  await dlg.getByTestId('add-host-create').click();
  await expect(dlg.getByTestId('add-host-name-error')).toContainText(name);
  await expect(dlg).toHaveAttribute('data-step', 'create');
});

test('after two minutes the dialog checks the agent port', async ({ page }) => {
  await page.clock.install();
  await page.route('**/api/v1/agents/reachability', (r) =>
    r.fulfill({ json: { base_url: '', base_source: 'default', agent_url: 'wss://192.0.2.10:8443/connect', agent_source: 'derived', listener_enabled: true, cert_sans: null, resolved_ips: null, checks: [], ok: false } })
  );
  const dlg = await openDialog(page);
  await dlg.getByTestId('add-host-create').click();
  await expect(dlg).toHaveAttribute('data-step', 'command');
  await expect(dlg.getByTestId('reachability')).toHaveCount(0);
  await page.clock.fastForward('02:05');
  await expect(dlg.getByTestId('reachability')).toContainText('wss://192.0.2.10:8443/connect');
  await expect(dlg.getByTestId('reachability')).toContainText('8443');
});
