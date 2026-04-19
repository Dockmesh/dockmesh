import { test, expect, uniqueSuffix, apiFromPage } from './fixtures';

// Backups + Alerts CRUD via API so we don't have to drive the modal
// dialogs end-to-end — the UI render is covered separately. These
// tests assert the backend + relevant list pages accept the resources.

const SUFFIX = uniqueSuffix();

test.describe('backups', () => {
	const TARGET = `e2e-target-${SUFFIX}`;

	test.afterAll(async ({ browser }) => {
		const page = await browser.newPage();
		const { login, apiFromPage } = await import('./fixtures');
		await login(page);
		const api = await apiFromPage(page);
		// Best-effort delete by name (find id first).
		const list = await api.get('/api/v1/backups/targets').then((r) => r.json());
		const row = (list as Array<{ id: number; name: string }>).find((r) => r.name === TARGET);
		if (row) await api.delete(`/api/v1/backups/targets/${row.id}`);
		await page.close();
	});

	test('backups page loads with its tabs', async ({ authedPage: page }) => {
		await page.goto('/backups');
		await expect(page.locator('main')).toBeVisible();
	});

	test('create a local backup target via API', async ({ authedPage: page }) => {
		const api = await apiFromPage(page);
		const resp = await api.post('/api/v1/backups/targets', {
			name: TARGET,
			type: 'local',
			config: { path: '/tmp/e2e-backups' }
		});
		expect([200, 201]).toContain(resp.status());
	});

	test('the new target appears in the UI list', async ({ authedPage: page }) => {
		await page.goto('/backups');
		await page.getByRole('button', { name: /Targets/i }).first().click().catch(() => undefined);
		await expect(page.getByText(TARGET, { exact: true })).toBeVisible({ timeout: 10_000 });
	});
});

test.describe('alerts', () => {
	const CHANNEL = `e2e-channel-${SUFFIX}`;

	test.afterAll(async ({ browser }) => {
		const page = await browser.newPage();
		const { login, apiFromPage } = await import('./fixtures');
		await login(page);
		const api = await apiFromPage(page);
		const list = await api.get('/api/v1/notifications/channels').then((r) => r.json());
		const row = (list as Array<{ id: number; name: string }>).find((r) => r.name === CHANNEL);
		if (row) await api.delete(`/api/v1/notifications/channels/${row.id}`);
		await page.close();
	});

	test('alerts page loads', async ({ authedPage: page }) => {
		await page.goto('/alerts');
		await expect(page.locator('main')).toBeVisible();
	});

	test('create a webhook notification channel', async ({ authedPage: page }) => {
		const api = await apiFromPage(page);
		const resp = await api.post('/api/v1/notifications/channels', {
			name: CHANNEL,
			type: 'webhook',
			config: { url: 'http://example.invalid/hook' },
			enabled: false
		});
		expect([200, 201]).toContain(resp.status());
	});

	test('the channel appears in the UI list', async ({ authedPage: page }) => {
		await page.goto('/alerts');
		// Channels tab — click whichever nav the page exposes.
		await page.getByRole('button', { name: /Channels/i }).first().click().catch(() => undefined);
		await expect(page.getByText(CHANNEL, { exact: true })).toBeVisible({ timeout: 10_000 });
	});
});
