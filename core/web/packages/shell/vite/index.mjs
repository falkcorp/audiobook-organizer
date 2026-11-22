// file: core/web/packages/shell/vite/index.mjs
// version: 0.1.0
// guid: 0b7d3e95-4a21-4c68-9f0e-6e2c1a8d5b73
// last-edited: 2026-11-20

// @mediacore/shell/vite: the app preset mediacore() and the library preset
// mediacoreLibrary() (07 §7.10). Unbuilt plain ESM, shipped outside dist/, so
// every vite.config.ts can load it before any package is built (§7.3.1).

import { execFileSync } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { readFileSync } from 'node:fs';
import { createRequire } from 'node:module';
import { resolve } from 'node:path';
import react, { reactCompilerPreset } from '@vitejs/plugin-react';
import babel from '@rolldown/plugin-babel';

export const APP_ID_PATTERN = /^[a-z][a-z0-9]{1,15}$/;
export const THEME_KEY_PATTERN = /^[A-Za-z0-9._:-]{1,128}$/;
export const BUILD_ID_PATTERN = /^[A-Za-z0-9._-]{1,64}$/;
export const MODE_ATTRIBUTE = 'data-mui-color-scheme';

/** The peer singletons every build dedupes (§7.3.2); check-singletons.mjs reads the same list. */
export const SINGLETONS = [
  'react',
  'react-dom',
  'react-router',
  '@mui/material',
  '@mui/icons-material',
  '@emotion/react',
  '@emotion/styled',
  'react/compiler-runtime',
];

/** The theme boot script; ui-kit's themeBootScript returns the same string (presets.test.mjs). */
export function themeBootScript(storageKey, defaultMode) {
  if (!THEME_KEY_PATTERN.test(storageKey)) throw new Error(`theme storage key ${JSON.stringify(storageKey)} must match ${THEME_KEY_PATTERN}`);
  if (defaultMode !== 'light' && defaultMode !== 'dark') throw new Error(`default mode must be light or dark`);
  return (
    `(function(){var m=${JSON.stringify(defaultMode)};try{var s=localStorage.getItem(${JSON.stringify(storageKey)});` +
    `if(s==="light"||s==="dark")m=s;else if(s==="system"&&typeof matchMedia==="function")m=matchMedia("(prefers-color-scheme: dark)").matches?"dark":"light"}catch(e){}document.documentElement.setAttribute(${JSON.stringify(MODE_ATTRIBUTE)},m)})();`
  );
}

function utcStamp(d) {
  return d.toISOString().replace(/[-:]/g, '').replace(/\.\d+Z$/, 'Z');
}

/**
 * The UI build ID of a `vite build` (§7.6.3): $BUILD_ID when set (validated,
 * "dev" refused in any case), else "<short sha>-<commit unix time>" for a
 * clean tree, else "<UTC stamp>-<8 random hex>".
 */
export function resolveBuildId({ env, cleanCommit, now, randomHex }) {
  const fromEnv = env ?? '';
  if (fromEnv !== '') {
    if (!BUILD_ID_PATTERN.test(fromEnv)) throw new Error(`BUILD_ID ${JSON.stringify(fromEnv)} is not a valid UI build ID: it must match ${BUILD_ID_PATTERN}`);
    if (fromEnv.toLowerCase() === 'dev') throw new Error('BUILD_ID "dev" is reserved: a dev build ID disables the stale-build reload rule');
    return fromEnv;
  }
  const clean = cleanCommit();
  if (clean) return `${clean.shortSha}-${clean.unixTime}`;
  return `${utcStamp(now())}-${randomHex(8)}`;
}

function gitCleanCommit(cwd) {
  const git = (...args) => execFileSync('git', args, { cwd, encoding: 'utf8', stdio: ['ignore', 'pipe', 'ignore'] }).trim();
  try {
    if (git('status', '--porcelain', '--untracked-files=no') !== '') return null;
    const shortSha = git('rev-parse', '--short', 'HEAD');
    const unixTime = Number(git('log', '-1', '--format=%ct'));
    if (!shortSha || !Number.isInteger(unixTime)) return null;
    return { shortSha, unixTime };
  } catch {
    return null;
  }
}

/** The head tags of the app preset: three metas and the boot script, in that order. */
export function headTags({ buildId, appId, themeStorageKey, defaultMode }) {
  return [
    { tag: 'meta', attrs: { name: 'mediacore-build', content: buildId }, injectTo: 'head-prepend' },
    { tag: 'meta', attrs: { name: 'mediacore-app', content: appId }, injectTo: 'head-prepend' },
    { tag: 'meta', attrs: { name: 'mediacore-theme', content: `${themeStorageKey} ${defaultMode}` }, injectTo: 'head-prepend' },
    { tag: 'script', children: themeBootScript(themeStorageKey, defaultMode), injectTo: 'head-prepend' },
  ];
}

function shellVersion(fromDir) {
  const require = createRequire(resolve(fromDir, 'noop.js'));
  return JSON.parse(readFileSync(require.resolve('@mediacore/shell/package.json'), 'utf8')).version;
}

function compilerPlugins() {
  // The compiler goes through @rolldown/plugin-babel: react()'s babel option
  // is a silent no-op under rolldown.
  return [react(), babel({ presets: [reactCompilerPreset()] })];
}

/**
 * The plugin that adds the `development` condition under `vite serve` and
 * vitest only (§7.3.1): a config hook, because the command is known only
 * there and the presets return a plain UserConfig.
 */
function conditionsPlugin(onCommand) {
  return {
    name: 'mediacore-conditions',
    config(_config, env) {
      onCommand?.(env.command);
      const dev = env.command === 'serve' || !!process.env.VITEST;
      return dev ? { resolve: { conditions: ['development', 'module', 'browser', 'import', 'default'] } } : undefined;
    },
  };
}

/** The app preset (07 §7.10). */
export function mediacore(options) {
  const { appId, defaultMode = 'dark' } = options ?? {};
  if (typeof appId !== 'string' || !APP_ID_PATTERN.test(appId)) throw new Error(`mediacore(): appId ${JSON.stringify(appId)} must match ${APP_ID_PATTERN}`);
  const themeStorageKey = options.themeStorageKey ?? `${appId}.shell.themeMode`;
  if (!THEME_KEY_PATTERN.test(themeStorageKey)) throw new Error(`mediacore(): themeStorageKey ${JSON.stringify(themeStorageKey)} must match ${THEME_KEY_PATTERN}`);
  if (defaultMode !== 'light' && defaultMode !== 'dark') throw new Error(`mediacore(): defaultMode must be 'light' or 'dark'`);
  const target = process.env.MEDIACORE_DEV_TARGET || 'http://localhost:8484';
  const root = process.cwd();
  let buildId = 'dev';
  return {
    plugins: [
      ...compilerPlugins(),
      conditionsPlugin((command) => {
        // Evaluated once per vite build (§7.6.3); "dev" under vite serve.
        buildId =
          command === 'build'
            ? resolveBuildId({
                env: process.env.BUILD_ID,
                cleanCommit: () => gitCleanCommit(root),
                now: () => new Date(),
                randomHex: (n) => randomBytes(Math.ceil(n / 2)).toString('hex').slice(0, n),
              })
            : 'dev';
      }),
      {
        name: 'mediacore-html',
        transformIndexHtml: {
          order: 'pre',
          handler: (html) => {
            if (/name=["']mediacore-(build|app|theme)["']/.test(html)) {
              throw new Error('index.html must not carry mediacore-* metas: the preset writes them');
            }
            return { html, tags: headTags({ buildId, appId, themeStorageKey, defaultMode }) };
          },
        },
      },
    ],
    define: { MEDIACORE_CORE_VERSION: JSON.stringify(shellVersion(root)) },
    resolve: { dedupe: SINGLETONS },
    server: {
      proxy: {
        '/api': {
          target,
          changeOrigin: true,
          // Origin as well as Host, so a dev server opened at a LAN address
          // passes the cross-origin stage (FE3-19). Dev only.
          configure: (proxy) => proxy.on('proxyReq', (req) => req.setHeader('origin', new URL(target).origin)),
        },
      },
    },
    build: {
      outDir: 'dist',
      sourcemap: true,
      rolldownOptions: {
        output: {
          codeSplitting: {
            groups: [
              { name: 'mui', test: /[\\/]node_modules[\\/]@mui[\\/]/ },
              { name: 'vendor', test: /[\\/]node_modules[\\/](react|react-dom|react-is|scheduler|react-router)[\\/]/ },
            ],
          },
        },
      },
    },
  };
}

/** Every dependency the library build leaves external: peers and their subpaths. */
function external(id) {
  return SINGLETONS.some((p) => id === p || id.startsWith(p + '/')) || id.startsWith('@mediacore/') || id === 'react/jsx-runtime';
}

/** The library preset for ui-kit and shell (07 §7.10). */
export function mediacoreLibrary(options) {
  const name = options?.name;
  if (name !== '@mediacore/ui-kit' && name !== '@mediacore/shell') throw new Error(`mediacoreLibrary(): name must be @mediacore/ui-kit or @mediacore/shell, got ${JSON.stringify(name)}`);
  const root = process.cwd();
  const version = JSON.parse(readFileSync(resolve(root, 'package.json'), 'utf8')).version;
  return {
    plugins: [...compilerPlugins(), conditionsPlugin()],
    ...(name === '@mediacore/shell' ? { define: { MEDIACORE_CORE_VERSION: JSON.stringify(version) } } : {}),
    resolve: { dedupe: SINGLETONS },
    build: {
      outDir: 'dist',
      sourcemap: true,
      emptyOutDir: true,
      lib: {
        entry:
          name === '@mediacore/shell'
            ? { index: resolve(root, 'src/index.ts'), testing: resolve(root, 'src/testing/index.tsx') }
            : { index: resolve(root, 'src/index.ts') },
        formats: ['es'],
      },
      rolldownOptions: { external },
    },
  };
}
