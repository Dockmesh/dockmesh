// Shared fixtures + helpers for the v1 regression suite.
//
// The `test` export is an extended Playwright test with an
// `authedPage` fixture: a Page that has already logged in as the
// configured admin user. Specs that intentionally exercise the
// login form itself should use the base `test` from @playwright/test.

import { test as base, expect, type Page, type APIResponse } from '@playwright/test';

export const USER = process.env.DOCKMESH_USER || 'admin';
export const PASS = process.env.DOCKMESH_PASS || 'admin123#';

/**
 * Log in via the UI and return once the dashboard is loaded.
 * Side effect: the page's localStorage now holds the JWT access token,
 * which `apiFromPage()` reads out to authenticate backend calls.
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

/**
 * Bearer-authed API client bound to a page's session. The web UI
 * stores the JWT in localStorage['dockmesh_auth'] and attaches it as
 * a Bearer header via its own fetch wrapper — plain Playwright
 * `page.request` doesn't pick that up, so we read the token out of
 * the browser and attach it manually.
 */
export interface AuthedAPI {
	get(path: string): Promise<APIResponse>;
	post(path: string, body?: unknown): Promise<APIResponse>;
	put(path: string, body?: unknown): Promise<APIResponse>;
	delete(path: string): Promise<APIResponse>;
}

export async function apiFromPage(page: Page): Promise<AuthedAPI> {
	const token = await page.evaluate(() => {
		const raw = localStorage.getItem('dockmesh_auth');
		if (!raw) return null;
		try {
			return (JSON.parse(raw) as { accessToken?: string }).accessToken ?? null;
		} catch {
			return null;
		}
	});
	if (!token) throw new Error('no access token in localStorage — call login() first');
	const headers = { Authorization: `Bearer ${token}` };
	return {
		get: (path) => page.request.get(path, { headers }),
		post: (path, body) =>
			page.request.post(path, body !== undefined ? { headers, data: body } : { headers }),
		put: (path, body) =>
			page.request.put(path, body !== undefined ? { headers, data: body } : { headers }),
		delete: (path) => page.request.delete(path, { headers })
	};
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
	throw new Error(
		`stack ${name} did not reach ${expectedServiceCount} running services within ${timeoutMs}ms`
	);
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
