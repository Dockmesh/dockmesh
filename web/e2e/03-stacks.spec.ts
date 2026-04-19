import { test, expect, uniqueSuffix, apiFromPage, waitForStackRunning, cleanupStack } from './fixtures';

// Full stack lifecycle. This is the fattest spec — it creates a real
// multi-container stack, deploys it on the local docker daemon, exercises
// history + rollback + redeploy, then tears it down.

const STACK = `e2e-stack-${uniqueSuffix()}`;

// Minimal-but-real compose — two services so we can sanity-check
// multi-service wiring without pulling large images every run.
// Using nginx:alpine twice keeps the pull size tiny on cold start.
const COMPOSE = `services:
  web:
    image: nginx:alpine
    ports:
      - "39080:80"
    networks:
      - e2enet
  sidecar:
    image: nginx:alpine
    networks:
      - e2enet
networks:
  e2enet:
`;

test.describe('stacks — full lifecycle', () => {
	test.afterAll(async ({ browser }) => {
		// Guaranteed cleanup even if a test in the middle failed.
		const page = await browser.newPage();
		const { login } = await import('./fixtures');
		await login(page);
		await cleanupStack(page, STACK);
		await page.close();
	});

	// Stacks lifecycle is one continuous flow — create, deploy, history,
	// rollback, stop, delete. Playwright's default test isolation (fresh
	// page per test) kept returning 404 between tests for reasons that
	// didn't reproduce manually. Running the whole flow as one test
	// sidesteps that entirely and matches how an operator actually
	// interacts with a stack.

	test('full lifecycle: create, deploy, history, rollback, stop, delete', async ({
		authedPage: page
	}) => {
		const api = await apiFromPage(page);

		// --- create ---
		const createResp = await api.post('/api/v1/stacks', {
			name: STACK,
			compose: COMPOSE,
			env: ''
		});
		expect(createResp.ok()).toBeTruthy();

		// --- detail page renders ---
		await page.goto(`/stacks/${STACK}`);
		await expect(page.getByRole('heading', { name: STACK })).toBeVisible();
		await expect(page.getByRole('button', { name: 'Deploy' })).toBeVisible();
		await expect(page.getByRole('tab', { name: /Overview/ })).toBeVisible();
		await expect(page.getByRole('tab', { name: /History/ })).toBeVisible();

		// --- deploy ---
		const deployResp = await api.post(`/api/v1/stacks/${STACK}/deploy`);
		expect(deployResp.ok()).toBeTruthy();
		await waitForStackRunning(page, STACK, 2);

		// --- history has one entry ---
		const entries1: Array<{ id: number }> = await api
			.get(`/api/v1/stacks/${STACK}/deployments`)
			.then((r) => r.json());
		expect(entries1.length).toBeGreaterThanOrEqual(1);

		// --- change compose + redeploy → history grows ---
		await api.put(`/api/v1/stacks/${STACK}`, {
			compose: COMPOSE.replace('nginx:alpine', 'nginx:1.27-alpine'),
			env: ''
		});
		const deployResp2 = await api.post(`/api/v1/stacks/${STACK}/deploy`);
		expect(deployResp2.ok()).toBeTruthy();
		await waitForStackRunning(page, STACK, 2);
		const entries2: Array<{ id: number }> = await api
			.get(`/api/v1/stacks/${STACK}/deployments`)
			.then((r) => r.json());
		expect(entries2.length).toBeGreaterThanOrEqual(2);

		// --- rollback to oldest entry ---
		const oldestId = entries2[entries2.length - 1].id;
		const rbResp = await api.post(`/api/v1/stacks/${STACK}/deployments/${oldestId}/rollback`);
		expect(rbResp.ok()).toBeTruthy();
		await waitForStackRunning(page, STACK, 2);

		// --- stop ---
		const stopResp = await api.post(`/api/v1/stacks/${STACK}/stop`);
		expect(stopResp.status()).toBe(204);

		// --- delete ---
		const delResp = await api.delete(`/api/v1/stacks/${STACK}`);
		expect(delResp.ok()).toBeTruthy();
		const gone = await api.get(`/api/v1/stacks/${encodeURIComponent(STACK)}`);
		expect(gone.status()).toBe(404);
	});
});
