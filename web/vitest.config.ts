// file: vitest.config.ts
// version: 1.3.0
// guid: 9c0d1e2f-3a4b-5c6d-7e8f-9a0b1c2d3e4f

import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
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
