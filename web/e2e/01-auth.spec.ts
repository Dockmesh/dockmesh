import { test as base, expect } from '@playwright/test';
import { USER, PASS, login } from './fixtures';

// Auth area covers login, logout, and the sessions panel. Uses the
// base test (not the authed one) because half the flows test the
// logged-out state.

base.describe('auth', () => {
	base('login with valid credentials lands on dashboard', async ({ page }) => {
		await page.goto('/login');
		await expect(page.getByRole('heading', { name: 'Welcome to Dockmesh' })).toBeVisible();
		await page.getByRole('textbox', { name: 'Username' }).fill(USER);
		await page.getByRole('textbox', { name: 'Password' }).fill(PASS);
		await page.getByRole('button', { name: /^Sign in$/ }).click();
		await expect(page.getByRole('heading', { name: 'Dashboard', level: 1 })).toBeVisible();
	});

	base('login with wrong password stays on login with an error', async ({ page }) => {
		await page.goto('/login');
		await page.getByRole('textbox', { name: 'Username' }).fill(USER);
		await page.getByRole('textbox', { name: 'Password' }).fill('wrong-on-purpose');
		await page.getByRole('button', { name: /^Sign in$/ }).click();
		// URL should NOT have redirected away from /login.
		await expect(page).toHaveURL(/\/login$/);
	});

	base('logout clears session and returns to login', async ({ page }) => {
		await login(page);
		await page.getByRole('button', { name: 'Sign out' }).click();
		await expect(page).toHaveURL(/\/login$/);
		// Navigating to a protected page should redirect back to /login.
		await page.goto('/');
		await expect(page).toHaveURL(/\/login/);
	});

	base('sessions panel loads and lists the current session', async ({ page }) => {
		await login(page);
		await page.goto('/settings?tab=account');
		await expect(page.getByRole('heading', { name: 'Active sessions' })).toBeVisible();
		// At least one row (the current session we just created).
		const rows = page.getByRole('list').locator('li');
		await expect.poll(async () => await rows.count()).toBeGreaterThanOrEqual(1);
	});
});
