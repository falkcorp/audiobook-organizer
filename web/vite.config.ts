// file: web/vite.config.ts
// version: 1.10.0
// guid: 9a8b7c6d-5e4f-3a2b-1c0d-9e8f7a6b5c4d
// last-edited: 2026-11-08

import { defineConfig, mergeConfig } from 'vite';
import path from 'path';
import { mediacore } from '@mediacore/shell/vite';

// The core app preset (07 section 7.10) owns what every mediacore app shares:
// the React compiler plugins, the UI build ID (BUILD_ID, else the clean commit,
// else a timestamp; "dev" under `vite serve`), the three mediacore-* metas and the
// pre-paint theme boot script it writes into index.html, the peer-singleton
// dedupe (a second React copy reaching MUI/emotion is React error #130), the
// mui/vendor chunk groups, sourcemaps, outDir and the /api dev proxy. Only what is
// ABO's own is added here.
//
// The ABO values the preset takes are literals, because this file is loaded by vite
// before any package is built and must not import the app: the app ID is the one Go
// declares (books.AppID; SHELL_APP_ID in src/shellAppId.ts) and the theme key is the
// one the ThemeProvider already reads, so a saved preference carries over
// (STORAGE_KEYS.APP_THEME_MODE). src/test/viteConfig.preset.test.ts pins both.

// https://vite.dev/config/
export default defineConfig(() =>
  mergeConfig(
    mediacore({
      appId: 'abo',
      themeStorageKey: 'app-theme-mode',
      defaultMode: 'dark',
    }),
    {
      resolve: {
        // import.meta.dirname rather than __dirname: vite 8 warns that __dirname is
        // unsupported by configLoader: 'native'.
        alias: {
          '@': path.resolve(import.meta.dirname, './src'),
        },
      },
      server: {
        port: 5173,
      },
      build: {
        // The 500 kB warning on the `mui` chunk is left as-is: it's needed even on
        // the login screen (App.tsx imports MUI core eagerly), and its size is
        // diffuse across 122 component subpaths with no dead weight to trim --
        // see docs/executive-summaries for the 2026-08-23 measurement.
        chunkSizeWarningLimit: 600,
      },
    }
  )
);
