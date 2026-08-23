// file: web/vite.config.ts
// version: 1.8.0
// guid: 9a8b7c6d-5e4f-3a2b-1c0d-9e8f7a6b5c4d
// last-edited: 2026-08-23

import { defineConfig } from 'vite';
import react, { reactCompilerPreset } from '@vitejs/plugin-react';
import babel from '@rolldown/plugin-babel';
import path from 'path';

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    react(),
    // React Compiler 1.0, via the babel path. plugin-react 6 also exposes a
    // native `compiler: true` option backed by the Rust oxc port, but that is
    // documented as experimental; babel-plugin-react-compiler is the stable 1.0
    // release. Passing the plugin through react()'s own `babel` option is a
    // silent no-op under rolldown -- it builds cleanly and emits nothing -- so
    // the @rolldown/plugin-babel wiring below is required, not optional.
    babel({ presets: [reactCompilerPreset()] }),
  ],
  resolve: {
    // import.meta.dirname rather than __dirname: vite 8 warns that __dirname is
    // unsupported by configLoader: 'native', which is slated to become the default.
    alias: {
      '@': path.resolve(import.meta.dirname, './src'),
    },
    // @vitejs/plugin-react 4.x added react/react-dom to resolve.dedupe for you;
    // 5.x dropped that, so we set it explicitly to keep the previous behaviour.
    // A second React instance reaching MUI/emotion produces React error #130 and
    // takes every page down, and the e2e suite that would catch it is currently
    // broken -- so this stays pinned rather than relying on npm's tree happening
    // to dedupe.
    dedupe: ['react', 'react-dom'],
  },
  server: {
    port: 5173,
    proxy: {
      '/api': {
        target: 'http://localhost:8484',
        changeOrigin: true,
      },
    },
  },
  build: {
    outDir: 'dist',
    sourcemap: true,
    // The 500 kB warning on the `mui` chunk is left as-is: it's needed even on
    // the login screen (App.tsx imports MUI core eagerly), and its size is
    // diffuse across 122 component subpaths with no dead weight to trim --
    // see docs/executive-summaries for the 2026-08-23 measurement. Splitting
    // it further trades one request for several at the same total byte count.
    chunkSizeWarningLimit: 600,
    rolldownOptions: {
      output: {
        // rolldown (vite 8) rejects the object form of manualChunks -- it accepts
        // only a function -- and exposes codeSplitting as its native equivalent
        // (advancedChunks/rollupOptions were the pre-8.2 names; both are now
        // @deprecated in favor of codeSplitting/rolldownOptions, same shape).
        // react-is and scheduler are listed explicitly: the old object form
        // pulled them in via rollup's module graph, whereas codeSplitting
        // matches on module path, so they would otherwise land in the entry chunk.
        //
        // These groups are a hint, not a partition: rolldown still places a
        // shared module wherever its own analysis prefers. Verified 2026-08-23
        // (after the rollupOptions->rolldownOptions / advancedChunks->codeSplitting
        // rename) by grouping every emitted `.js.map`'s `sources` by exact module
        // file path across all chunks: zero files land in more than one chunk.
        // react.production.js/jsx-runtime and react-dom's small shared-internals
        // shim end up in `mui`, while react-dom-client (the actual renderer) and
        // the compiler-runtime end up in `vendor` -- different files within the
        // same package, not two live instances. Duplicating either is what
        // produced the React #130 crash on the previous Vite 8 attempt, so
        // re-run that exact-file check (not just a per-package check -- a
        // package can legitimately have different files in different chunks)
        // before changing these groups.
        codeSplitting: {
          groups: [
            { name: 'mui', test: /[\\/]node_modules[\\/]@mui[\\/]/ },
            {
              name: 'vendor',
              test: /[\\/]node_modules[\\/](react|react-dom|react-is|scheduler|react-router|react-router-dom)[\\/]/,
            },
          ],
        },
      },
    },
  },
  test: {
    globals: true,
    environment: 'jsdom',
    setupFiles: ['./src/test/setup.ts'],
    coverage: {
      provider: 'v8',
      reporter: ['text', 'json', 'html'],
      thresholds: {
        statements: 15,
        branches: 10,
        functions: 15,
        lines: 15,
      },
    },
  },
});
