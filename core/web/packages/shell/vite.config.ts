// file: core/web/packages/shell/vite.config.ts
// version: 0.1.0
// guid: 9a5c2e18-7d43-4f0b-8e61-b2d4f6a9c3e7
// last-edited: 2026-11-20

import { defineConfig, mergeConfig } from 'vitest/config';
import { mediacoreLibrary } from './vite/index.mjs';

export default mergeConfig(mediacoreLibrary({ name: '@mediacore/shell' }), defineConfig({
  test: {
    environment: 'jsdom',
    globals: true,
    include: ['src/**/*.test.{ts,tsx}', 'test/**/*.test.{ts,tsx}'],
    setupFiles: ['./test/setup.ts'],
  },
}));
