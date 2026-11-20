// file: core/examples/hello/web/vite.config.ts
// version: 0.1.0
// guid: 8c1f5a39-2d64-4e07-b8a2-0f6d3e9c1b75
// last-edited: 2026-11-20

import { defineConfig, mergeConfig } from 'vitest/config';
import { mediacore } from '@mediacore/shell/vite';

export default mergeConfig(mediacore({ appId: 'hello' }), defineConfig({
  test: { environment: 'jsdom', globals: true, include: ['src/**/*.test.{ts,tsx}'] },
}));
