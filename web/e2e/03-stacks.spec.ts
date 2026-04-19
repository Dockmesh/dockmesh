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

	test('create via API (faster than clicking the modal every run)', async ({ authedPage: page }) => {
		const api = await apiFromPage(page);
		const resp = await api.post('/api/v1/stacks', {
			data: { name: STACK, compose: COMPOSE, env: '' }
		});
		expect(resp.ok()).toBeTruthy();
	});

	test('stack detail page renders', async ({ authedPage: page }) => {
		await page.goto(`/stacks/${STACK}`);
		await expect(page.getByRole('heading', { name: STACK })).toBeVisible();
		await expect(page.getByRole('button', { name: 'Deploy' })).toBeVisible();
		await expect(page.getByRole('tab', { name: /Overview/ })).toBeVisible();
		await expect(page.getByRole('tab', { name: /History/ })).toBeVisible();
	});

	test('deploy brings all services up', async ({ authedPage: page }) => {
		await page.goto(`/stacks/${STACK}`);
		await page.getByRole('button', { name: 'Deploy' }).click();
		await waitForStackRunning(page, STACK, 2);
		await page.reload();
		await expect(page.getByText('2/2 running')).toBeVisible({ timeout: 20_000 });
	});

	test('history tab shows exactly one deploy entry', async ({ authedPage: page }) => {
		await page.goto(`/stacks/${STACK}`);
		await page.getByRole('tab', { name: /History/ }).click();
		await expect(page.getByText('Deploy history')).toBeVisible();
		await expect(page.getByText('current')).toBeVisible();
	});

	test('rollback flow opens confirm modal and completes', async ({ authedPage: page }) => {
		// Create a second deploy by changing image tag → Save → Deploy, then
		// roll back to the first.
		const api = await apiFromPage(page);
		await api.put(`/api/v1/stacks/${STACK}`, {
			data: {
				compose: COMPOSE.replace('nginx:alpine', 'nginx:1.27-alpine'),
				env: ''
			}
		});
		await page.goto(`/stacks/${STACK}`);
		await page.getByRole('button', { name: 'Deploy' }).click();
		await waitForStackRunning(page, STACK, 2);

		await page.getByRole('tab', { name: /History/ }).click();
		// Two entries now — current + one older (with rollback button).
		const rollbackBtn = page.getByRole('button', { name: 'Roll back to this version' });
		await expect(rollbackBtn).toBeVisible();
		await rollbackBtn.click();
		// Styled confirm dialog with "Roll back" action.
		await expect(page.getByRole('heading', { name: /^Roll back to deploy #/ })).toBeVisible();
		await page.getByRole('button', { name: /^Roll back$/ }).click();
		// After rollback → redeploy → history grows by one.
		await waitForStackRunning(page, STACK, 2);
	});

	test('stop removes all containers', async ({ authedPage: page }) => {
		const api = await apiFromPage(page);
		const resp = await api.post(`/api/v1/stacks/${STACK}/stop`);
		expect(resp.status()).toBe(204);
	});

	test('delete removes stack + uses styled confirm dialog', async ({ authedPage: page }) => {
		await page.goto(`/stacks/${STACK}`);
		await page.getByRole('button', { name: 'Delete' }).click();
		// Global ConfirmDialog rendered in +layout.
		await expect(page.getByRole('heading', { name: `Delete stack ${STACK}` })).toBeVisible();
		await page.getByRole('button', { name: /^Delete$/ }).click();
		// Redirects to /stacks after successful delete.
		await expect(page).toHaveURL(/\/stacks$/);
		// And the stack is really gone.
		const api = await apiFromPage(page);
		const resp = await api.get(`/api/v1/stacks/${encodeURIComponent(STACK)}`);
		expect(resp.status()).toBe(404);
	});
});
