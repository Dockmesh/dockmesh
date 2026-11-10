// The deploy confirms a default environment override, and scaling asks
// before it builds from stack files changed outside Dockmesh. Needs
// DOCKMESH_STACKS_ROOT of the instance on this machine.
import { test, expect, type Page } from '@playwright/test';
import { writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { ADMIN, password, signIn } from './env';

const root = process.env.DOCKMESH_STACKS_ROOT;
const base = 'services:\n  web:\n    image: busybox:1\n';

async function apiToken(page: Page): Promise<string> {
  const login = await page.request.post('/api/v1/auth/login', { data: { username: ADMIN, password: password() } });
  return (await login.json()).access_token;
}

async function createStack(page: Page, token: string, name: string) {
  const res = await page.request.post('/api/v1/stacks', {
    headers: { Authorization: `Bearer ${token}` },
    data: { name, compose: base }
  });
  expect(res.ok(), await res.text()).toBeTruthy();
}

test('deploy shows the default environment override before deploying', async ({ page }, info) => {
  test.skip(!root, 'needs DOCKMESH_STACKS_ROOT');
  const name = `override-${info.project.name}-${Date.now()}`;
  await signIn(page, ADMIN);
  const token = await apiToken(page);
  await createStack(page, token, name);
  writeFileSync(join(root!, name, 'compose.prod.yaml'), 'services:\n  web:\n    image: busybox:prod\n');
  const set = await page.request.put(`/api/v1/stacks/${name}/environments/active`, {
    headers: { Authorization: `Bearer ${token}` },
    data: { active: 'prod' }
  });
  expect(set.ok(), await set.text()).toBeTruthy();

  let deployed = false;
  await page.route(`**/api/v1/stacks/${name}/deploy*`, (route) => {
    if (route.request().method() !== 'POST') return route.continue();
    deployed = true;
    return route.abort();
  });
  await page.goto(`/stacks/${name}`);
  await page.getByRole('button', { name: /^(Deploy|Deployen)$/ }).first().click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toContainText('prod');
  await expect(dialog).toContainText('compose.prod.yaml');
  await dialog.getByRole('button', { name: /^(Cancel|Abbrechen)$/ }).click();
  await expect(dialog).toBeHidden();
  expect(deployed).toBe(false);
});

test('scaling asks before it uses stack files changed outside Dockmesh', async ({ page }, info) => {
  test.skip(!root, 'needs DOCKMESH_STACKS_ROOT');
  const name = `scale409-${info.project.name}-${Date.now()}`;
  await signIn(page, ADMIN);
  await createStack(page, await apiToken(page), name);
  writeFileSync(join(root!, name, 'compose.yaml'), 'services:\n  web:\n    image: busybox:outside\n');

  // A service without Docker (stopped, so no stats stream is opened): the status and the scale check are
  // stubbed; the unconfirmed scale goes to the server, which refuses it.
  await page.route(`**/api/v1/stacks/${name}/status*`, (route) =>
    route.fulfill({ json: [{ service: 'web', container_id: 'c0ffee', state: 'exited', status: 'Exited (0)', image: 'busybox:1' }] })
  );
  await page.route(`**/api/v1/stacks/${name}/scale*`, (route) => route.fulfill({ json: [{ service: 'web', replicas: 1 }] }));
  let confirmed = false;
  await page.route(`**/api/v1/stacks/${name}/services/web/scale*`, (route) => {
    const req = route.request();
    if (req.method() === 'GET') {
      return route.fulfill({ json: { service: 'web', current_replicas: 1, has_container_name: false, has_hard_port: false, is_stateful: false, has_volumes: false } });
    }
    if (req.url().includes('confirm_external=1')) {
      confirmed = true;
      return route.fulfill({ json: { service: 'web', previous: 1, current: 2 } });
    }
    return route.continue();
  });

  await page.goto(`/stacks/${name}`);
  await page.getByRole('button', { name: /^(Actions for|Aktionen für) web$/ }).click();
  const checked = page.waitForResponse((r) => r.url().includes('/services/web/scale') && r.request().method() === 'GET');
  await page.getByRole('menuitem', { name: /^(Scale…|Skalieren…)$/ }).click();
  await checked;
  const scaleDialog = page.getByRole('dialog');
  await scaleDialog.locator('#scale-slider').fill('2');
  await scaleDialog.getByRole('button', { name: /^(Scale to 2|Auf 2 skalieren)$/ }).click();

  const ask = page.getByRole('dialog').filter({ hasText: /Stack files changed outside Dockmesh|Stack-Dateien außerhalb von Dockmesh geändert/ });
  await expect(ask).toContainText('compose.yaml');
  await ask.getByRole('button', { name: /^(Deploy anyway|Trotzdem deployen)$/ }).click();
  await expect.poll(() => confirmed).toBe(true);
});
