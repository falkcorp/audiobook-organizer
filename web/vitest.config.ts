// file: vitest.config.ts
// version: 1.3.0
// guid: 9c0d1e2f-3a4b-5c6d-7e8f-9a0b1c2d3e4f

import { defineConfig } from 'vitest/config';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { SINGLETONS } from '@mediacore/shell/vite';

// Every run, local or CI, uses one US time zone with daylight saving time, so
// tests of local dates and DST shifts (the API key expiry picker) are not
// blind on a UTC runner. Set here as well as in test.env: the env reaches the
// workers, and setting it before they start keeps Node's zone cache from
// holding the runner's own zone.
const TEST_TZ = 'America/New_York';
process.env.TZ = TEST_TZ;

function shellVersion(): string {
  const manifest = createRequire(import.meta.url).resolve('@mediacore/shell/package.json');
  return JSON.parse(readFileSync(manifest, 'utf8')).version;
}

export default defineConfig({
  // One copy of each peer singleton for code reached through the file: links
  // @mediacore/shell and ui-kit (07 §7.3.2); the same list as vite.config.ts.
  resolve: { dedupe: SINGLETONS },
  // vitest reads the shell from source (the package's `development`
  // condition), which reads the build-time global the shell's own builds and
  // the mediacore() app preset define (07 §7.10). The preset replaces this at
  // F9-03; until then web/ defines it the way the preset does.
  define: { MEDIACORE_CORE_VERSION: JSON.stringify(shellVersion()) },
  test: {
    env: { TZ: TEST_TZ },
    environment: 'jsdom',
    setupFiles: './src/test/setup.ts',
    globals: true,
    exclude: ['tests/e2e/**', 'node_modules/**', 'dist/**'],
    // CI runners are slower than local dev machines. The default
    // 5000ms timeout was causing false failures on tests that
    // render complex components (Library, BookDetail) with
    // multiple async operations. 30s is generous enough for CI
    // while still catching genuinely hung tests.
    testTimeout: 30000,
    // Persist transformed modules between runs (Vitest 5, default false).
    // Stored in node_modules/.vitest-cache, so a reinstall invalidates it
    // and it is already git-ignored. https://vitest.dev/config/fsmodulecache
    // Off in CI: every CI job starts from `npm ci`, so the cache is always
    // cold there, and a cold run measured slower than no cache (103 s vs
    // 81 s); only a warm local run gains (72.6 s). Woodpecker sets CI, and so
    // does GitHub Actions.
    fsModuleCache: !process.env.CI,
    coverage: {
      provider: 'v8',
      reporter: ['text', 'html', 'lcov'],
      thresholds: {
        lines: 30,
        functions: 20,
        branches: 20,
        statements: 25,
      },
    },
  },
});
