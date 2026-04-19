import { test, expect } from './fixtures';

// Settings has 8 tabs (Account, Users, Roles, SSO, API Tokens,
// Registries, Audit Log, System). Each one should load without a
// console error or a 500 from the backend.

test.describe('settings tabs', () => {
	const tabs = [
		{ name: 'Account' },
		{ name: 'Users' },
		{ name: 'Roles' },
		{ name: 'SSO' },
		{ name: 'API Tokens' },
		{ name: 'Registries' },
		{ name: 'Audit Log' },
		{ name: 'System' }
	];

	for (const t of tabs) {
		test(`${t.name} tab loads without errors`, async ({ authedPage: page }) => {
			const errors: string[] = [];
			page.on('pageerror', (e) => errors.push(String(e)));
			page.on('console', (msg) => {
				if (msg.type() === 'error') errors.push(msg.text());
			});

			await page.goto('/settings');
			await page.getByRole('button', { name: t.name, exact: true }).click();
			// Give data fetches a beat to complete.
			await page.waitForLoadState('networkidle').catch(() => undefined);
			// No pageerrors (real JS crashes). Console errors are allowed if
			// they're the known git-404 noise from stack-detail views that
			// we don't visit here, so this is a strict check.
			const realErrors = errors.filter(
				(e) => !e.includes('net::ERR_') && !e.includes('favicon')
			);
			expect(realErrors, `errors on ${t.name}: ${realErrors.join('\n')}`).toHaveLength(0);
		});
	}
});
