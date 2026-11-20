// file: core/web/e2e/playwright.config.ts
// version: 0.1.0
// guid: 6d2f9a14-8b53-4e70-a1c6-0e7b3d5f9c28
// last-edited: 2026-11-20

// The shell's Playwright suite against the hello example built with
// -tags embed_frontend (07 §7.3.1, §7.18): `make -C core e2e` builds
// core/bin/hello and runs this. hello takes no listen flag until config
// lands (F3), so it serves the kernel default, :8484.

import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { defineConfig } from '@playwright/test';

const state = mkdtempSync(join(tmpdir(), 'hello-e2e-'));
const BASE = process.env.HELLO_E2E_URL ?? 'http://localhost:8484';

export default defineConfig({
  testDir: '.',
  testMatch: '*.spec.ts',
  fullyParallel: false,
  workers: 1,
  retries: 0,
  reporter: [['list']],
  use: { baseURL: BASE, trace: 'retain-on-failure' },
  webServer: process.env.HELLO_E2E_URL
    ? undefined
    : {
        command: join(import.meta.dirname, '../../bin/hello'),
        url: `${BASE}/livez`,
        reuseExistingServer: false,
        timeout: 60_000,
        env: { HELLO_CORE_PATHS_STATE_DIR: state, HELLO_CORE_STORAGE_PATH: join(state, 'db') },
        stdout: 'pipe',
        stderr: 'pipe',
      },
});
