import { defineConfig } from '@playwright/test';

export default defineConfig({
  testDir: 'tests',
  timeout: 30_000,
  fullyParallel: false, // RBAC state is global — serialize to keep login clean.
  retries: 0,
  reporter: 'list',
  use: {
    headless: true,
    trace: 'retain-on-failure'
  }
});
