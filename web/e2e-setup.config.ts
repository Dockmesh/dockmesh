import { defineConfig, devices } from '@playwright/test';

// The setup wizard against a server in setup mode. web/tests-setup/run.sh
// starts one in a container and passes its URL and setup code.
const baseURL = process.env.DOCKMESH_SETUP_URL;
if (!baseURL) throw new Error('set DOCKMESH_SETUP_URL to a Dockmesh in setup mode (see tests-setup/run.sh)');

export default defineConfig({
  testDir: './tests-setup',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: 'list',
  use: {
    ...devices['Desktop Chrome'],
    baseURL,
    ignoreHTTPSErrors: true,
    viewport: { width: 1280, height: 860 },
    locale: 'en-US',
    trace: 'retain-on-failure'
  }
});
