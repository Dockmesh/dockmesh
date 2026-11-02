import { defineConfig, devices } from '@playwright/test';

// End-to-end flows against a running Dockmesh. Point DOCKMESH_URL at a
// throwaway instance; tests/global-setup.ts signs in as the admin from
// DOCKMESH_E2E_USER / DOCKMESH_E2E_PASSWORD and creates the role users.
const baseURL = process.env.DOCKMESH_URL;
if (!baseURL) throw new Error('set DOCKMESH_URL to the Dockmesh instance under test');

const language = (locale: string) => ({
  ...devices['Desktop Chrome'],
  viewport: { width: 1280, height: 800 },
  locale,
  extraHTTPHeaders: { 'Accept-Language': locale }
});

export default defineConfig({
  testDir: './tests',
  globalSetup: './tests/global-setup.ts',
  fullyParallel: false,
  workers: 1,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: { baseURL, trace: 'retain-on-failure' },
  projects: [
    { name: 'en', use: language('en-US') },
    { name: 'de', use: language('de-DE') }
  ]
});
