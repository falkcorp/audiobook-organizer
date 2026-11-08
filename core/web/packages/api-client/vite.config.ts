// file: core/web/packages/api-client/vite.config.ts
// version: 1.0.0
// guid: bbd5ddf1-2167-4df6-bcb6-116f4bea1e1a
// last-edited: 2026-11-08

import { defineConfig } from 'vitest/config';

// Library mode, no React and no preset (07 §7.3.1: the api-client holds no React
// code, so before the shell package exists it needs none). `dist/index.js` is what
// an app's production build resolves; `tsc --emitDeclarationOnly` writes the .d.ts
// beside it (the `build` script). openapi-fetch stays external: it is the package's
// dependency, resolved from the consumer's install.
export default defineConfig({
  build: {
    lib: { entry: 'src/index.ts', formats: ['es'], fileName: 'index' },
    rollupOptions: { external: ['openapi-fetch'] },
    sourcemap: true,
  },
  test: {
    environment: 'jsdom',
    globals: true,
    include: ['src/**/*.test.ts', 'test/**/*.test.ts'],
  },
});
