import { execFileSync } from 'node:child_process';
import { expect, test, type Page } from '@playwright/test';

// One never set up server for the whole file, so the flows run in order:
// code screen, the new-installation path, and the install itself last.
test.describe.configure({ mode: 'serial' });

const container = process.env.DOCKMESH_SETUP_CONTAINER ?? '';
let code = process.env.DOCKMESH_SETUP_CODE ?? '';

function newCode(): string {
  const out = execFileSync('docker', ['exec', container, 'dockmesh', 'setup', 'new-code', '--porcelain'], { encoding: 'utf8' });
  return /^code=(.+)$/m.exec(out)?.[1] ?? '';
}

async function enterWithLink(page: Page) {
  await page.goto(`/setup#${code}`);
  await expect(page.locator('#setup-code')).toHaveValue(code);
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page.getByRole('heading', { name: 'How do you want to start?' })).toBeVisible();
}

test('the link fills the code, clears the fragment and waits for Continue', async ({ page }) => {
  const codePosts: string[] = [];
  page.on('request', (r) => {
    if (r.url().endsWith('/api/v1/setup/code')) codePosts.push(r.method());
  });
  await page.goto(`/setup#${code}`);
  await expect(page.locator('#setup-code')).toHaveValue(code);
  expect(new URL(page.url()).hash).toBe('');
  expect(new URL(page.url()).pathname).toBe('/setup');
  await expect(page.getByText('Filled in from the link the installer printed.')).toBeVisible();
  await expect(page.getByText('Step 1 · Setup code')).toBeVisible();
  await expect(page.getByRole('link', { name: 'Docs ↗' })).toHaveAttribute('href', 'https://dockmesh.dev/docs/install/setup#setup-code');
  await page.waitForTimeout(1500);
  expect(codePosts).toEqual([]);
});

test('a crafted fragment never reaches the field', async ({ page }) => {
  await page.goto('/setup#%3Cimg%20src%3Dx%20onerror%3Dalert(1)%3E');
  await expect(page.locator('#setup-code')).toHaveValue('');
  expect(new URL(page.url()).hash).toBe('');
});

test('wrong and malformed codes', async ({ page }) => {
  await page.goto('/setup');
  await page.locator('#setup-code').fill('abc');
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page.getByText('A setup code has 12 letters and digits.')).toBeVisible();
  const wrong = code.replace(/^./, (c) => (c === 'A' ? 'B' : 'A'));
  await page.locator('#setup-code').fill(wrong.toLowerCase().replaceAll('-', ' '));
  await page.getByRole('button', { name: 'Continue' }).click();
  await expect(page.getByText(/That code is not right\. \d+ attempts left/)).toBeVisible();
  await page.getByRole('button', { name: 'Lost the code?' }).click();
  await expect(page.getByText('sudo dockmesh setup new-code')).toBeVisible();
});

test('another browser that enters a new code takes the setup over', async ({ page, browser }) => {
  await enterWithLink(page);
  code = newCode();
  const other = await browser.newContext({ ignoreHTTPSErrors: true });
  const second = await other.newPage();
  await enterWithLink(second);
  await page.reload();
  await expect(page.getByRole('heading', { name: 'Setup continued in another browser' })).toBeVisible();
  await page.getByRole('button', { name: 'Enter a new code' }).click();
  await expect(page.locator('#setup-code')).toBeVisible();
  await other.close();
});

test('new installation: every step, then install and sign in', async ({ page }) => {
  await enterWithLink(page);
  const rail = page.getByRole('complementary', { name: 'Setup steps' });
  await expect(rail.getByRole('listitem')).toHaveCount(2);
  await expect(rail.getByText('Next steps depend on your choice')).toBeVisible();
  await expect(page.getByText('Step 2 · Start')).toBeVisible();

  await page.getByText('New installation', { exact: true }).click();
  await page.getByRole('button', { name: 'Continue' }).click();

  // N1
  await expect(page.getByText('Step 3 of 9 · Admin')).toBeVisible();
  await expect(rail.getByRole('listitem')).toHaveCount(9);
  await page.locator('#admin-pw').fill('short');
  await page.getByRole('button', { name: 'Next' }).click();
  await expect(page.getByText('Use at least 12 characters.')).toBeVisible();
  await expect(page.getByText('The passwords do not match.')).toBeVisible();
  await page.locator('#admin-user').fill('Ad');
  await page.locator('#admin-user').blur();
  await expect(page.getByText('Use 3 to 32 characters')).toBeVisible();
  await page.locator('#admin-user').fill('admin');
  await page.getByRole('button', { name: 'Generate' }).click();
  await expect(page.getByText('This password is not shown again. Store it now.')).toBeVisible();
  const password = await page.locator('#admin-pw').inputValue();
  expect(password).toHaveLength(20);
  await expect(page.locator('#admin-pw2')).toHaveValue(password);
  await page.getByRole('button', { name: 'Next' }).click();

  // N2
  await expect(page.getByText('Step 4 of 9 · Data directory')).toBeVisible();
  await expect(page.getByText(/Free space .* · empty/)).toBeVisible();
  await expect(page.getByText('Disks on this server')).toBeVisible();
  await page.locator('#data-dir').fill('/etc');
  await page.locator('#data-dir').blur();
  await expect(page.getByText('/etc is a system directory.')).toBeVisible();
  await page.locator('#data-dir').fill('/var/lib/docker/x');
  await page.getByRole('button', { name: 'Next' }).click();
  await expect(page.getByText('Choose a directory outside /var/lib/docker.')).toBeVisible();
  await page.locator('#data-dir').fill('/var/lib/dockmesh');
  await page.locator('#data-dir').blur();
  await expect(page.getByText(/Free space .* · empty/)).toBeVisible();
  await page.getByRole('button', { name: 'Next' }).click();

  // N3
  await expect(page.getByText('Step 5 of 9 · Address and TLS')).toBeVisible();
  const origin = new URL(page.url()).origin;
  await expect(page.locator('#base-url')).toHaveValue(origin);
  await page.locator('#base-url').fill(origin.replace('https:', 'http:'));
  await page.getByRole('button', { name: 'Next' }).click();
  await expect(page.getByText('The base URL must start with https://.')).toBeVisible();
  await page.locator('#base-url').fill(origin);
  await page.getByRole('button', { name: 'Test connection' }).click();
  // Behind the container's port mapping the server cannot reach itself
  // under the browser's port, so either answer is fine here.
  await expect(page.getByText(/Reached this server in \d+ ms|Could not reach/)).toBeVisible();
  await expect(page.getByText('Certificate fingerprint (SHA-256)')).toBeVisible();
  await expect(page.getByRole('radio', { name: /Let Caddy get a certificate/ })).toBeDisabled();
  await page.getByRole('button', { name: 'Next' }).click();

  // N4, N5
  await expect(page.getByText('Step 6 of 9 · Reverse proxy')).toBeVisible();
  await page.getByRole('button', { name: 'Next' }).click();
  await expect(page.getByText('Step 7 of 9 · Notifications')).toBeVisible();
  await page.getByRole('button', { name: 'Next' }).click();

  // N6, with the rail back to a done step; later steps never link
  await expect(page.getByText('Step 8 of 9 · Review')).toBeVisible();
  await expect(rail.getByRole('button', { name: /Setup code/ })).toHaveCount(0);
  await rail.getByRole('button', { name: /Admin/ }).click();
  await expect(page.getByText('Step 3 of 9 · Admin')).toBeVisible();
  await expect(rail.getByRole('button', { name: /Review/ })).toHaveCount(0);
  await page.getByRole('button', { name: 'Next' }).click();
  await page.getByRole('button', { name: 'Next' }).click();
  await page.getByRole('button', { name: 'Next' }).click();
  await page.getByRole('button', { name: 'Next' }).click();
  await page.getByRole('button', { name: 'Next' }).click();
  await expect(page.getByText('Step 8 of 9 · Review')).toBeVisible();
  await expect(page.getByText('••••')).toBeVisible();
  await page.getByRole('button', { name: 'Install' }).click();

  // N7
  await expect(page.getByText('Step 9 of 9 · Apply')).toBeVisible();
  await expect(rail.getByRole('button')).toHaveCount(0);
  await expect(page.getByRole('heading', { name: 'Dockmesh is ready' })).toBeVisible({ timeout: 120_000 });
  await page.waitForURL('**/login', { timeout: 15_000 });

  await page.getByLabel(/username/i).fill('admin');
  await page.getByLabel(/password/i).first().fill(password);
  await page.getByRole('button', { name: /sign in/i }).click();
  await page.waitForURL((u) => !u.pathname.startsWith('/login'), { timeout: 15_000 });
});

test('after setup /setup leads to the sign-in page', async ({ page }) => {
  await page.goto('/setup');
  await page.waitForURL('**/login');
});
