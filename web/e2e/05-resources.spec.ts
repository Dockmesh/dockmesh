import { test, expect } from './fixtures';

// Read-mostly pages. These tests verify the page mounts without
// console errors / page crashes. Heading text matches are intentionally
// loose (no level: 1 / level: 2 asserted) because different list pages
// use different heading hierarchies.

test.describe('resources — list pages', () => {
	const pages = [
		{ path: '/images',       marker: /images/i },
		{ path: '/volumes',      marker: /volumes/i },
		{ path: '/networks',     marker: /networks/i },
		{ path: '/agents',       marker: /agents/i },
		{ path: '/migrations',   marker: /migration/i },
		{ path: '/proxy',        marker: /(proxy|routes|caddy)/i },
		{ path: '/environment',  marker: /environment/i },
		{ path: '/templates',    marker: /templates/i }
	];

	for (const p of pages) {
		test(`${p.path} renders without errors`, async ({ authedPage: page }) => {
			const jsErrors: string[] = [];
			page.on('pageerror', (e) => jsErrors.push(String(e)));

			await page.goto(p.path);
			// Main content renders.
			await expect(page.locator('main')).toBeVisible();
			// Page-identifying text is somewhere in the main content. We
			// accept any heading OR body text — the pages use a mix of
			// h1 / h2 / section-label styling.
			await expect(page.locator('main').getByText(p.marker).first()).toBeVisible({
				timeout: 10_000
			});
			// No JS crashes (page errors). Ignored: console errors, which
			// can be noise from unrelated background calls.
			expect(jsErrors, `${p.path}: ${jsErrors.join('\n')}`).toHaveLength(0);
		});
	}
});
