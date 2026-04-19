import { test, expect } from './fixtures';

test.describe('dashboard', () => {
	test('loads with all metric cards present', async ({ authedPage: page }) => {
		await page.goto('/');
		await expect(page.getByRole('heading', { name: 'Dashboard', level: 1 })).toBeVisible();
		// CPU / Memory / Disk / Containers metric cards.
		await expect(page.getByText('CPU', { exact: true })).toBeVisible();
		await expect(page.getByText('Memory', { exact: true })).toBeVisible();
		await expect(page.getByText('Disk', { exact: true })).toBeVisible();
		await expect(page.getByText('Containers', { exact: true }).first()).toBeVisible();
	});

	test('host picker dropdown opens and lists Local + All hosts', async ({ authedPage: page }) => {
		await page.goto('/');
		// The button's label is the currently-selected host name, default "Local".
		await page.getByRole('button', { name: /^Local$/ }).click();
		await expect(page.getByRole('option', { name: /^All hosts/ })).toBeVisible();
		// Close by selecting Local again to avoid leaking state.
		await page.getByRole('option', { name: /^Local/ }).click();
	});

	test('switching to All hosts mode shows the per-host system health table', async ({
		authedPage: page
	}) => {
		await page.goto('/');
		await page.getByRole('button', { name: /^Local$/ }).click();
		await page.getByRole('option', { name: /^All hosts/ }).click();
		await expect(page.getByRole('heading', { name: 'Per-host system health' })).toBeVisible();
		// Switch back.
		await page.getByRole('button', { name: /^All hosts$/ }).click();
		await page.getByRole('option', { name: /^Local/ }).click();
	});

	test('recent activity and quick actions sections render', async ({ authedPage: page }) => {
		await page.goto('/');
		await expect(page.getByRole('heading', { name: 'Recent activity' })).toBeVisible();
		await expect(page.getByRole('heading', { name: 'Quick actions' })).toBeVisible();
	});
});
