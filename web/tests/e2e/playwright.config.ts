// file: tests/e2e/playwright.config.ts
// version: 1.8.0
// guid: 7c8d9e0f-1a2b-3c4d-5e6f-7a8b9c0d1e2f
// last-edited: 2026-08-08

import { defineConfig, devices } from '@playwright/test';
import { fileURLToPath } from 'url';
import { dirname, join } from 'path';

// Centralized demo artifacts directory for all recordings and screenshots
const __filename = fileURLToPath(import.meta.url);
const __dirname = dirname(__filename);
const DEMO_ARTIFACTS_DIR = join(__dirname, '../../..', 'demo_artifacts');

export default defineConfig({
  testDir: '.',
  timeout: 30 * 1000,
  fullyParallel: true,
  retries: 0,
  workers: 2,
  reporter: [
    ['list'],
    ['html', { outputFolder: 'playwright-report', open: 'never' }],
  ],
  use: {
    baseURL: 'http://127.0.0.1:8484',
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
    headless: true,
  },
  projects: [
    // --- CI / default projects --------------------------------------------------
    // demo-*.spec.ts and interactive-*.spec.ts are excluded here so that `make
    // test-e2e` (and `npm run test:e2e`) only runs functional tests. Demo
    // recording requires a live server with media content and must be invoked
    // explicitly via `make test-e2e-demo` / `npm run test:e2e:demo`.
    {
      name: 'chromium',
      testIgnore: ['**/demo-*.spec.ts', '**/interactive-*.spec.ts'],
      use: { ...devices['Desktop Chrome'] },
    },
    {
      name: 'webkit',
      testIgnore: ['**/demo-*.spec.ts', '**/interactive-*.spec.ts'],
      use: { ...devices['Desktop Safari'] },
      // We accept WebKit failures for now; main gate stays on Chromium.
      expect: {
        toMatchSnapshot: { maxDiffPixelRatio: 0.05 },
      },
    },
    // --- Opt-in demo recording project ------------------------------------------
    // Run with: npx playwright test --project chromium-record
    //       or: npm run test:e2e:demo
    //       or: make test-e2e-demo
    {
      name: 'chromium-record',
      testMatch: ['**/interactive-*.spec.ts', '**/demo-*.spec.ts'],
      outputDir: DEMO_ARTIFACTS_DIR,
      use: {
        ...devices['Desktop Chrome'],
        screenshot: 'on',
        video: 'on',
      },
    },
  ],
  webServer: {
    // Build full app (frontend + embedded backend) and run single Go binary
    // Disable TLS for testing by passing empty cert/key flags
    // GOEXPERIMENT=jsonv2 is required for the Go backend (encoding/json/v2)
    command: `bash -c 'export GOEXPERIMENT=jsonv2 && cd ${__dirname}/../../.. && cd web && npm run build && cd .. && go build -tags embed_frontend -o audiobook-organizer . && rm -rf /tmp/ao-e2e-db && mkdir -p /tmp/ao-e2e-db /tmp/ao-e2e-books && ./audiobook-organizer serve --tls-cert "" --tls-key "" --host 127.0.0.1 --db /tmp/ao-e2e-db/e2e.pebble --dir /tmp/ao-e2e-books'`,
    url: 'http://127.0.0.1:8484',
    // The command above builds the frontend AND compiles the Go binary before
    // it can serve anything. 120s was enough on a warm developer machine and
    // nowhere near enough on a cold CI runner with an empty Go build cache —
    // the first CI run of this suite died on exactly this. Raised to 10
    // minutes: it is a ceiling, not a delay, so a warm local run still starts
    // as fast as it ever did.
    timeout: 600000,
    reuseExistingServer: !process.env.CI,
  },
});
