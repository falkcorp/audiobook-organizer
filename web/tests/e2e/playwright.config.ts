// file: tests/e2e/playwright.config.ts
// version: 1.11.0
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
  // Fails the run if :8484 is already serving a bundle older than the code
  // under test. `reuseExistingServer` below is what makes that possible, and a
  // silently-reused stale server is what produced a false green on 2026-08-08
  // while the suite was ~50% red. See global-setup.ts.
  globalSetup: './global-setup.ts',
  timeout: 30 * 1000,
  fullyParallel: true,
  retries: 0,
  // One worker on CI, two locally.
  //
  // Measured 2026-08-09 in the official Playwright linux image, pinned to 2
  // CPUs to approximate a runner:
  //   workers=2 -> library-browser + scan-import FAIL (3 separate runs)
  //   workers=1 -> 27 passed, 0 failed
  // The failures were 30s `locator.click` timeouts with a MUI modal backdrop
  // (Drawer in one case, Select menu in the other) still intercepting pointer
  // events. Two browser workers plus the Go server on 2 cores starve the close
  // TRANSITION, so the backdrop outlives any timeout worth setting. Neither the
  // app nor the tests are wrong — a real user is not running two headless
  // browsers on two pinned cores.
  //
  // The cost is wall-clock: chromium goes from ~4.5min to ~9min. That is the
  // right trade for a gate that is supposed to block merges — a fast check
  // people learn to distrust is worth less than a slow one they believe.
  workers: process.env.CI ? 1 : 2,
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
      // Double the per-test budget for webkit only.
      //
      // Measured on the real runner: webkit has a POPULATION of tests sitting
      // close to the 30s budget, and roughly one loses per run. Two
      // consecutive both-engine runs scored an identical 543 passed / 1 failed
      // / 16 skipped and failed DIFFERENT tests -- scan-import-organize:259,
      // then (after that one was fixed) itunes-bidirectional-sync:121, in a
      // different spec file. Fixing them individually is a treadmill: each fix
      // is real and the score does not move.
      //
      // This is headroom, not blindness. A genuinely broken test does not
      // finish in 60s either; what changes is that a slow-but-correct one
      // stops being reported as a failure. The same reasoning as
      // `workers: process.env.CI ? 1 : 2` above -- the app is not slow, the
      // runner is loaded, and the suite should measure the app.
      //
      // Chromium keeps the tighter 30s: it stopped failing entirely once CI
      // dropped to one worker, so it has margin to spare and there is no
      // reason to spend it.
      timeout: 60 * 1000,
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
