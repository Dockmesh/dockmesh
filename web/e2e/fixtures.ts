// Shared fixtures + helpers for the v1 regression suite.
//
// The main value here is the `authed` fixture: a Playwright `page`
// that has already logged in as the configured admin user. Every spec
// that needs auth declares `test.use({ storageState: ... })` via the
// extended test below.

import { test as base, expect, type Page, type APIRequestContext } from '@playwright/test';

export const USER = process.env.DOCKMESH_USER || 'admin';
export const PASS = process.env.DOCKMESH_PASS || 'admin123#';

/**
 * Log in via the UI and return once the dashboard is loaded. Used by
 * the global setup and by any test that intentionally exercises the
 * login form itself.
 */
export async function login(page: Page) {
	await page.goto('/login');
	await page.getByRole('textbox', { name: 'Username' }).fill(USER);
	await page.getByRole('textbox', { name: 'Password' }).fill(PASS);
	await page.getByRole('button', { name: /^Sign in$/ }).click();
	await expect(page.getByRole('heading', { name: 'Dashboard', level: 1 })).toBeVisible();
}

/** Seconds-granularity unique suffix so test-created resources don't
 *  collide across quick re-runs. */
export function uniqueSuffix(): string {
	return String(Date.now()).slice(-8);
}

/** Build an API client that reuses the admin's cookie auth from a
 *  logged-in page. Useful for tests that need to prep state quickly
 *  without going through the UI. */
export async function apiFromPage(page: Page): Promise<APIRequestContext> {
	// Playwright's `page.request` is already bound to the same storage
	// context as the page — inherits the login cookie.
	return page.request;
}

/** Wait for a stack to reach a given service-count / state via the
 *  REST status endpoint. Avoids scraping the UI for container states. */
export async function waitForStackRunning(
	page: Page,
	name: string,
	expectedServiceCount: number,
	timeoutMs = 90_000
) {
	const api = await apiFromPage(page);
	const deadline = Date.now() + timeoutMs;
	while (Date.now() < deadline) {
		const resp = await api.get(`/api/v1/stacks/${encodeURIComponent(name)}/status`);
		if (resp.ok()) {
			const rows: Array<{ state: string }> = await resp.json();
			const running = rows.filter((r) => r.state === 'running').length;
			if (running >= expectedServiceCount) return;
		}
		await page.waitForTimeout(1000);
	}
	throw new Error(`stack ${name} did not reach ${expectedServiceCount} running services within ${timeoutMs}ms`);
}

/** Delete a stack + best-effort stop first. Called in afterEach /
 *  afterAll hooks to keep the server tidy between runs. Safe to call
 *  on non-existent stacks. */
export async function cleanupStack(page: Page, name: string) {
	const api = await apiFromPage(page);
	try {
		await api.post(`/api/v1/stacks/${encodeURIComponent(name)}/stop`);
	} catch {
		/* ignore — stack may already be stopped */
	}
	try {
		await api.delete(`/api/v1/stacks/${encodeURIComponent(name)}`);
	} catch {
		/* ignore — stack may not exist */
	}
}

// Extend the base test with common setup: every test starts already
// logged in. Spec files that want to test login itself should use the
// base `test` from @playwright/test directly.
export const test = base.extend<{ authedPage: Page }>({
	authedPage: async ({ page }, use) => {
		await login(page);
		await use(page);
	}
});

export { expect };
