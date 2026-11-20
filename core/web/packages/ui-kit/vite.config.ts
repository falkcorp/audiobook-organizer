// file: core/web/packages/ui-kit/vite.config.ts
// version: 0.1.0
// guid: 5f0c8e23-9b14-4a7d-b6e2-3d1a7c9f4b80
// last-edited: 2026-11-20

import { defineConfig, mergeConfig } from 'vitest/config';
// The one shell file ui-kit's build may import: the unbuilt preset (07 §7.3.1, FE8-1).
import { mediacoreLibrary } from '@mediacore/shell/vite';

export default mergeConfig(mediacoreLibrary({ name: '@mediacore/ui-kit' }), defineConfig({
  test: {
    environment: 'jsdom',
    globals: true,
    include: ['src/**/*.test.{ts,tsx}'],
  },
}));
