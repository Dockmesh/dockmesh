import { defineConfig, devices } from '@playwright/test';

// Playwright configuration for Dockmesh.
//
// Two projects:
//
//   rbac-smoke     — legacy, runs against a local dev server; untouched.
//   v1-regression  — full v1 acceptance suite. Runs against a live
//                    Dockmesh server (default 192.168.10.164:8080) with
//                    admin credentials. Creates its own data, cleans up
//                    after itself.
//
// Select a project with `--project=<name>` or run both (default).
//
// Env vars consumed by v1-regression:
//   DOCKMESH_URL   — base URL (default http://192.168.10.164:8080)
//   DOCKMESH_USER  — admin username (default admin)
//   DOCKMESH_PASS  — admin password (default admin123#)
//
// Retries: 2 globally — WebSocket log streams + docker image pulls
// have inherent timing variance, and a test failing on attempt 1 but
// green on retry 2 is almost always flake, not a regression.
//
// Workers: 1 — tests exercise shared server state (single stacks
// filesystem, shared docker daemon). Parallelism would introduce
// flakes unrelated to actual bugs.
export default defineConfig({
	timeout: 60_000,
	expect: { timeout: 10_000 },
	fullyParallel: false,
	workers: 1,
	retries: 2,
	reporter: [
		['list'],
		['html', { outputFolder: 'playwright-report', open: 'never' }],
		['json', { outputFile: 'playwright-report/results.json' }]
	],
	use: {
		headless: true,
		trace: 'retain-on-failure',
		screenshot: 'only-on-failure',
		video: 'retain-on-failure',
		ignoreHTTPSErrors: true,
		actionTimeout: 15_000,
		navigationTimeout: 30_000
	},
	projects: [
		{
			name: 'rbac-smoke',
			testDir: './tests',
			use: { ...devices['Desktop Chrome'] }
		},
		{
			name: 'v1-regression',
			testDir: './e2e',
			use: {
				...devices['Desktop Chrome'],
				baseURL: process.env.DOCKMESH_URL || 'http://192.168.10.164:8080'
			}
		}
	]
});
