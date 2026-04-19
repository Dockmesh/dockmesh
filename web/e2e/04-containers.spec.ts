import { test, expect, uniqueSuffix, apiFromPage, waitForStackRunning, cleanupStack } from './fixtures';

// Containers list + detail flow. Spins up its own one-service stack so
// we have a known container to inspect (avoids depending on whatever
// happens to be running on the server).

const STACK = `e2e-containers-${uniqueSuffix()}`;
const COMPOSE = `services:
  probe:
    image: nginx:alpine
    ports:
      - "39081:80"
`;

test.describe('containers', () => {
	test.beforeAll(async ({ browser }) => {
		const page = await browser.newPage();
		const { login } = await import('./fixtures');
		await login(page);
		const api = page.request;
		await api.post('/api/v1/stacks', { data: { name: STACK, compose: COMPOSE, env: '' } });
		await api.post(`/api/v1/stacks/${STACK}/deploy`);
		await waitForStackRunning(page, STACK, 1);
		await page.close();
	});

	test.afterAll(async ({ browser }) => {
		const page = await browser.newPage();
		const { login } = await import('./fixtures');
		await login(page);
		await cleanupStack(page, STACK);
		await page.close();
	});

	test('list page renders with at least one container row', async ({ authedPage: page }) => {
		await page.goto('/containers');
		await expect(page.locator('table').first()).toBeVisible();
		// At least one `running` row somewhere in the table.
		await expect(page.getByText('running').first()).toBeVisible();
	});

	test('container detail page shows Overview / Logs / Terminal tabs', async ({
		authedPage: page
	}) => {
		const api = await apiFromPage(page);
		const status = await api
			.get(`/api/v1/stacks/${STACK}/status`)
			.then((r) => r.json() as Promise<Array<{ container_id: string }>>);
		const cid = status[0].container_id;
		await page.goto(`/containers/${cid}`);
		await expect(page.getByRole('button', { name: 'Overview' })).toBeVisible();
		await expect(page.getByRole('button', { name: 'Logs' })).toBeVisible();
		await expect(page.getByRole('button', { name: 'Terminal' })).toBeVisible();
	});

	test('logs tab streams at least one line of nginx startup', async ({ authedPage: page }) => {
		const api = await apiFromPage(page);
		const status = await api
			.get(`/api/v1/stacks/${STACK}/status`)
			.then((r) => r.json() as Promise<Array<{ container_id: string }>>);
		const cid = status[0].container_id;
		await page.goto(`/containers/${cid}`);
		await page.getByRole('button', { name: 'Logs' }).click();
		// nginx:alpine always logs this line on start, so it's a reliable probe
		// that the WS stream connected and delivered data.
		await expect(page.getByText(/nginx|start worker|listening/i).first()).toBeVisible({
			timeout: 30_000
		});
	});
});
