// End-to-end tests against the OpenCloud fixture (sub-phase 8f, issue #42).
//
// Preconditions, as for `make test-opencloud`: the fixture is up and seeded
// (`make dev-up`), the extension is installed into it (`make
// web-install-fixture`), and the dev Garage answers on :3900. globalSetup
// checks all three and then starts backupd itself; see e2e/global-setup.ts.
//
// One worker, no parallelism: the specs share one backupd, one OpenCloud and
// one Space per user, and the journey is a sequence, not a set.
import { defineConfig } from '@playwright/test'
import { FIXTURE_ORIGIN } from './e2e/support/env'

// E2E_CHROME points at a system Chrome for local runs without a Playwright
// browser download. CI uses the pinned Playwright Chromium.
const executablePath = process.env.E2E_CHROME

export default defineConfig({
  testDir: 'e2e',
  testMatch: '*.e2e.ts',
  outputDir: 'e2e-results',
  globalSetup: './e2e/global-setup.ts',
  workers: 1,
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: 0,
  // The Recovery Key ceremony runs Argon2id in the browser, and a backup
  // and a restore run end to end inside a single test.
  timeout: 5 * 60_000,
  expect: { timeout: 30_000 },
  reporter: process.env.CI ? [['list'], ['html', { open: 'never' }]] : 'list',
  use: {
    baseURL: FIXTURE_ORIGIN,
    // The fixture's Caddy serves a certificate from its own local CA.
    ignoreHTTPSErrors: true,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    launchOptions: executablePath ? { executablePath } : {}
  },
  projects: [
    { name: 'admin', testMatch: 'admin.e2e.ts' },
    { name: 'journey', testMatch: 'journey.e2e.ts', dependencies: ['admin'] }
  ]
})
