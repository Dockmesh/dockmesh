// Host › Setup: the stacks directory against a mocked agent (connected
// state, check, disks and the apply answer); the host itself is real.
import { test, expect, type Page } from '@playwright/test';
import { ADMIN, signIn, trackCreatedHosts } from './env';

const DEFAULT = '/var/lib/dockmesh-agent/stacks';

async function realHost(page: Page): Promise<string> {
  await page.goto('/hosts');
  await page.getByTestId('add-host').click();
  await page.locator('#add-host-name').fill(`e2e-setup-${Date.now().toString(36)}`);
  const res = page.waitForResponse((r) => r.url().endsWith('/api/v1/agents') && r.request().method() === 'POST');
  await page.getByTestId('add-host-create').click();
  const body = await (await res).json();
  await page.keyboard.press('Escape');
  return body.agent.id as string;
}

async function mockAgent(page: Page, agent: () => Record<string, unknown>) {
  await page.route(/\/api\/v1\/agents\/[0-9a-f-]{36}$/, async (route) => {
    if (route.request().method() !== 'GET') return route.continue();
    const real = await (await route.fetch()).json();
    return route.fulfill({ json: { ...real, ...agent() } });
  });
  await page.route(/\/stacks-dir\/disks$/, (r) => r.fulfill({ json: { disks: [
    { mountpoint: '/', fs_type: 'ext4', total_bytes: 30e9, free_bytes: 21e9, suggested: DEFAULT, usable: true },
    { mountpoint: '/srv', fs_type: 'xfs', total_bytes: 1.8e12, free_bytes: 1.7e12, suggested: '/srv/stacks', usable: true },
    { mountpoint: '/mnt/media', fs_type: 'nfs4', total_bytes: 1e12, free_bytes: 5e11, suggested: '/mnt/media/stacks', usable: false, reason: 'network share' }
  ] } }));
  await page.route(/\/stacks-dir\/check$/, async (r) => {
    const { path } = r.request().postDataJSON();
    if (path === '/home/stacks') return r.fulfill({ json: { path, status: 'fail', issue: 'new_dir_not_allowed', message: 'New directory: root creates a stacks directory only directly in /srv, /opt. Prepare /home/stacks as an empty directory of the agent: sudo install -d -o dockmesh-agent -g dockmesh-agent -m 0750 /home/stacks', command: 'sudo install -d -o dockmesh-agent -g dockmesh-agent -m 0750 /home/stacks' } });
    if (path === '/srv/full') return r.fulfill({ json: { path, status: 'fail', issue: 'not_empty', message: '/srv/full is not empty. Choose an empty or new directory.' } });
    return r.fulfill({ json: { path, status: 'ok', issue: path === DEFAULT ? 'unchanged' : '', message: '', will_create: path !== DEFAULT, fs_type: 'ext4', free_bytes: 21e9 } });
  });
}

const managed = (extra: Record<string, unknown> = {}) => ({
  status: 'online', layout: 'managed', needs_setup: true, data_dir: '/var/lib/dockmesh-agent', run_user: 'dockmesh-agent',
  stacks_dirs: [{ path: DEFAULT, current: true, stacks: 0 }], ...extra
});

let cleanup: Awaited<ReturnType<typeof trackCreatedHosts>> | null = null;

test.beforeEach(async ({ page }) => {
  cleanup = trackCreatedHosts(page);
  await signIn(page, ADMIN);
});

test.afterEach(async ({ request }) => {
  await cleanup?.(request);
});

test('waiting host: banner, check, refusal, then save the default', async ({ page }) => {
  const id = await realHost(page);
  let agent = managed();
  await mockAgent(page, () => agent);
  await page.route(/\/stacks-dir$/, (r) => {
    agent = managed({ needs_setup: false, stacks_dir: { path: DEFAULT, status: 'applied' } });
    return r.fulfill({ json: { ...agent, id, name: 'x' } });
  });
  await page.goto(`/hosts/${id}`);
  await page.getByTestId('setup-banner-btn').click();
  await expect(page).toHaveURL(/tab=setup/);
  const tab = page.getByTestId('setup-tab');
  await expect(tab.locator('#setup-stacks-dir')).toHaveValue(DEFAULT);
  await expect(tab.getByTestId('setup-check').locator('.okline')).toBeVisible();
  await expect(tab.getByTestId('setup-check')).not.toContainText('ext4');

  await tab.locator('#setup-stacks-dir').fill('/srv/full');
  await expect(tab.getByTestId('setup-check')).toContainText('is not empty');
  await expect(tab.getByTestId('setup-save')).toBeDisabled();

  await tab.locator('#setup-stacks-dir').fill('/home/stacks');
  await expect(tab.getByTestId('setup-check')).toContainText('does not create a new directory at /home/stacks');
  await expect(tab.getByTestId('setup-check')).not.toContainText('root creates');
  await expect(tab.getByTestId('setup-prepare')).toContainText('sudo install -d -o dockmesh-agent');
  await expect(tab.getByTestId('setup-save')).toBeDisabled();

  await tab.getByRole('button', { name: DEFAULT }).click();
  await expect(tab.locator('#setup-stacks-dir')).toHaveValue(DEFAULT);
  await tab.getByTestId('setup-save').click();
  await expect(tab.getByTestId('setup-done')).toContainText(DEFAULT);
  await expect(page.getByTestId('setup-banner-btn')).toHaveCount(0);
});

test('managed host: switching shows the apply steps until applied', async ({ page }) => {
  const id = await realHost(page);
  let agent = managed({ needs_setup: false });
  let polls = 0;
  await mockAgent(page, () => {
    if (agent.stacks_dir && ++polls > 2) agent = managed({ needs_setup: false, stacks_dir: { path: '/srv/stacks', status: 'applied' }, stacks_dirs: [{ path: '/srv/stacks', current: true, stacks: 0 }, { path: DEFAULT, current: false, stacks: 2 }] });
    return agent;
  });
  await page.route(/\/stacks-dir$/, (r) => {
    agent = managed({ needs_setup: false, stacks_dir: { path: '/srv/stacks', status: 'pending' } });
    return r.fulfill({ status: 202, json: { ...agent, id, name: 'x' } });
  });
  await page.goto(`/hosts/${id}?tab=setup`);
  const tab = page.getByTestId('setup-tab');
  await tab.getByRole('button', { name: '/srv/stacks' }).click();
  await expect(tab.getByTestId('setup-check')).toContainText('ext4');
  await tab.getByTestId('setup-save').click();
  await expect(tab.getByTestId('setup-progress')).toBeVisible();
  await expect(tab.getByTestId('setup-done')).toContainText('/srv/stacks', { timeout: 15_000 });
  await expect(tab).toContainText(DEFAULT);
});

test('legacy host: field disabled with the re-run command', async ({ page }) => {
  const id = await realHost(page);
  await mockAgent(page, () => managed({ needs_setup: false, layout: 'legacy', rerun_command: "curl -fsSLk https://dm.example:8443/install/agent.sh | sudo bash" }));
  await page.goto(`/hosts/${id}?tab=setup`);
  const tab = page.getByTestId('setup-tab');
  await expect(tab.getByTestId('setup-legacy')).toContainText('install/agent.sh');
  await expect(tab.locator('#setup-stacks-dir')).toBeDisabled();
  await expect(tab.getByTestId('setup-save')).toBeDisabled();
});
