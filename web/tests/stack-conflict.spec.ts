// A save over a compose.yaml that changed on disk is refused and offers a
// diff. Needs DOCKMESH_STACKS_ROOT of the instance on this machine.
import { test, expect, type Page } from '@playwright/test';
import { readFileSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { ADMIN, password } from './env';

const root = process.env.DOCKMESH_STACKS_ROOT;
const original = 'services:\n  web:\n    image: busybox:1\n';
const onDisk = 'services:\n  web:\n    image: busybox:disk\n';
const mine = 'services:\n  web:\n    image: busybox:mine\n';

async function signIn(page: Page) {
  await page.goto('/login');
  await page.locator('#login-username').fill(ADMIN);
  await page.locator('#login-password').fill(password());
  await page.locator('form button[type="submit"]').click();
  await page.waitForURL((u) => !u.pathname.startsWith('/login'));
}

async function createStack(page: Page, name: string) {
  const login = await page.request.post('/api/v1/auth/login', { data: { username: ADMIN, password: password() } });
  const { access_token: token } = await login.json();
  const res = await page.request.post('/api/v1/stacks', {
    headers: { Authorization: `Bearer ${token}` },
    data: { name, compose: original }
  });
  expect(res.ok(), await res.text()).toBeTruthy();
}

async function editAndSave(page: Page, file: string) {
  await page.getByRole('tab', { name: 'Compose' }).click();
  await page.locator('.stk-toolbar').getByRole('button', { name: /^(Edit|Bearbeiten)$/ }).click();
  await page.getByLabel('compose.yaml').fill(mine);
  writeFileSync(file, onDisk);
  await page.locator('.stk-toolbar').getByRole('button', { name: /^(Save|Speichern)$/ }).click();
  const dialog = page.getByTestId('save-conflict');
  await expect(dialog).toBeVisible();
  await expect(page.getByTestId('save-conflict-diff')).toContainText('busybox:disk');
  await expect(page.getByTestId('save-conflict-diff')).toContainText('busybox:mine');
  expect(readFileSync(file, 'utf8')).toBe(onDisk);
}

test('a save over a file changed on disk shows a diff and both choices work', async ({ page }, info) => {
  test.skip(!root, 'needs DOCKMESH_STACKS_ROOT');
  const name = `conflict-${info.project.name}-${Date.now()}`;
  await signIn(page);
  await createStack(page, name);
  const file = join(root!, name, 'compose.yaml');

  await page.goto(`/stacks/${name}`);
  await editAndSave(page, file);
  await page.getByTestId('save-conflict-use-disk').click();
  await expect(page.getByTestId('save-conflict')).toBeHidden();
  await page.getByRole('tab', { name: 'Compose' }).click();
  await expect(page.locator('.stk-yaml-view')).toContainText('busybox:disk');
  expect(readFileSync(file, 'utf8')).toBe(onDisk);

  writeFileSync(file, original);
  await page.goto(`/stacks/${name}`);
  await editAndSave(page, file);
  await page.getByTestId('save-conflict-force').click();
  await expect(page.getByTestId('save-conflict')).toBeHidden();
  await expect.poll(() => readFileSync(file, 'utf8')).toBe(mine);

  writeFileSync(file, original);
  await page.goto(`/stacks/${name}`);
  await editAndSave(page, file);
  await page.getByTestId('save-conflict-cancel').click();
  await expect(page.getByTestId('save-conflict')).toBeHidden();
  await expect(page.getByLabel('compose.yaml')).toHaveValue(mine);
  expect(readFileSync(file, 'utf8')).toBe(onDisk);
});
