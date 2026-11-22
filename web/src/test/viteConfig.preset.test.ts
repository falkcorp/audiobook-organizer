// file: web/src/test/viteConfig.preset.test.ts
// version: 0.1.0
// guid: 74dae3eb-6f6f-478c-9d30-81a4854d807a
// last-edited: 2026-11-22

// F9-03 commit 1 (07 section 7.19 step 4): web/vite.config.ts is built on the
// core mediacore(...) preset, and web/index.html loses its hand-written theme
// script and its build-ID meta, because the preset writes the three mediacore-*
// metas and the one boot script (ui.FromFS refuses a bundle without them, and the
// preset throws on an index.html that already carries one).
//
// The `preset` blocks are written first and skipped until commit 1 lands: at HEAD the
// config stamps the build meta with web/build-id.ts and index.html carries the old
// script, so each fails there (checked by un-skipping them against HEAD: 6 red). The
// `settings ABO keeps` block passes on both sides: what the switch must not change.

import { readFileSync } from 'node:fs';
import path from 'node:path';
import { afterEach, beforeEach, describe, expect, it } from 'vitest';
import { SINGLETONS } from '@mediacore/shell/vite';
import { STORAGE_KEYS } from '../lib/storageKeys';
import { SHELL_APP_ID } from '../shellAppId';

// Red at HEAD (before F9-03 commit 1, the vite preset): 6 failing.
const preset = describe;

interface HtmlTag {
  tag: string;
  attrs?: Record<string, string>;
  children?: string;
  injectTo?: string;
}
type TransformHook = (html: string) => string | { html: string; tags: HtmlTag[] } | undefined | void;
interface TestPlugin {
  name: string;
  config?: (c: unknown, env: { command: string; mode: string }) => unknown;
  transformIndexHtml?: TransformHook | { order?: string; handler: TransformHook };
}
interface ResolvedConfig {
  plugins: unknown[];
  resolve?: { alias?: Record<string, string>; dedupe?: string[] };
  server?: { port?: number; proxy?: Record<string, { target?: string }> };
  build?: { outDir?: string; sourcemap?: boolean; chunkSizeWarningLimit?: number; rolldownOptions?: { output?: { codeSplitting?: { groups?: { name: string }[] } } } };
}

const indexHtml = readFileSync(path.resolve(import.meta.dirname, '../../index.html'), 'utf8');

// web/vite.config.ts belongs to tsconfig.node.json, which src cannot import from, so it
// is loaded by path. The cast is the shape vite calls it with.
async function resolveConfig(command: 'build' | 'serve'): Promise<ResolvedConfig> {
  const file = path.resolve(import.meta.dirname, '../../vite.config.ts');
  const config: unknown = ((await import(/* @vite-ignore */ file)) as { default: unknown }).default;
  if (typeof config !== 'function') return config as ResolvedConfig; // an object export
  const make = config as (env: { command: string; mode: string }) => ResolvedConfig | Promise<ResolvedConfig>;
  return make({ command, mode: command === 'build' ? 'production' : 'development' });
}

function pluginsOf(cfg: ResolvedConfig): TestPlugin[] {
  return (cfg.plugins as unknown[])
    .flat(Infinity)
    .filter((p): p is TestPlugin => typeof p === 'object' && p !== null && typeof (p as TestPlugin).name === 'string');
}

/** What `vite build` / `vite serve` does to index.html: the config hooks, then the HTML transforms. */
async function transformed(command: 'build' | 'serve'): Promise<{ html: string; tags: HtmlTag[] }> {
  const cfg = await resolveConfig(command);
  const plugins = pluginsOf(cfg);
  for (const p of plugins) p.config?.(cfg, { command, mode: command === 'build' ? 'production' : 'development' });
  let html = indexHtml;
  const tags: HtmlTag[] = [];
  for (const p of plugins) {
    const t = p.transformIndexHtml;
    if (!t) continue;
    const out = (typeof t === 'function' ? t : t.handler)(html);
    if (out === undefined || out === null) continue; // e.g. react()'s serve-only preamble
    if (typeof out === 'string') html = out;
    else {
      html = out.html;
      tags.push(...(out.tags ?? []));
    }
  }
  return { html, tags };
}

let savedBuildId: string | undefined;
beforeEach(() => {
  savedBuildId = process.env.BUILD_ID;
  process.env.BUILD_ID = 'batcave-42';
});
afterEach(() => {
  if (savedBuildId === undefined) delete process.env.BUILD_ID;
  else process.env.BUILD_ID = savedBuildId;
});

preset('web/index.html under the mediacore preset', () => {
  it('carries no mediacore-* meta: the preset writes them', () => {
    expect(indexHtml).not.toMatch(/name=["']mediacore-(build|app|theme)["']/);
  });

  it('carries no inline script: the preset writes the one boot script', () => {
    const inline = [...indexHtml.matchAll(/<script\b([^>]*)>/gi)].filter((m) => !/\bsrc=/.test(m[1]));
    expect(inline).toEqual([]);
    expect(indexHtml).not.toContain('data-mui-color-scheme');
  });

});

preset('web/vite.config.ts on mediacore(...)', () => {
  it('has exactly one plugin that rewrites index.html, the preset\'s', async () => {
    // react() also has an HTML hook, but only the dev-server preamble; it returns nothing in a build.
    const writers = pluginsOf(await resolveConfig('build')).filter((p) => p.transformIndexHtml && /^mediacore/.test(p.name));
    expect(writers.map((p) => p.name)).toEqual(['mediacore-html']);
  });

  it('lists no plugin twice (a merged config must not double the React or babel plugins)', async () => {
    const names = pluginsOf(await resolveConfig('build')).map((p) => p.name);
    expect(names.length).toBeGreaterThan(0);
    expect(names.filter((n, i) => names.indexOf(n) !== i)).toEqual([]);
  });

  it('writes the build, app and theme metas and one boot script in a build', async () => {
    const { tags } = await transformed('build');
    const metas = tags.filter((t) => t.tag === 'meta').map((t) => [t.attrs?.name, t.attrs?.content]);
    expect(metas).toEqual([
      ['mediacore-build', 'batcave-42'],
      ['mediacore-app', SHELL_APP_ID],
      ['mediacore-theme', `${STORAGE_KEYS.APP_THEME_MODE} dark`],
    ]);
    const scripts = tags.filter((t) => t.tag === 'script');
    expect(scripts).toHaveLength(1);
    expect(scripts[0].children).toContain(JSON.stringify(STORAGE_KEYS.APP_THEME_MODE));
  });

  it('keeps the build ID dev under vite serve, which never triggers the stale-build rule', async () => {
    const { tags } = await transformed('serve');
    expect(tags.find((t) => t.attrs?.name === 'mediacore-build')?.attrs?.content).toBe('dev');
  });

  it('uses the app ID of shellAppId.ts and the theme key the ThemeProvider reads', async () => {
    expect(SHELL_APP_ID).toBe('abo');
    const { tags } = await transformed('build');
    expect(tags.find((t) => t.attrs?.name === 'mediacore-app')?.attrs?.content).toBe(SHELL_APP_ID);
  });

});

describe('web/vite.config.ts settings ABO keeps across the preset switch', () => {
  it('still loads the app entry as a module script', () => {
    expect(indexHtml).toMatch(/<script type="module" src="\/src\/main\.tsx"><\/script>/);
  });

  it('refuses a reserved or malformed BUILD_ID before bundling', async () => {
    process.env.BUILD_ID = 'dev';
    await expect(transformed('build')).rejects.toThrow(/reserved/);
    process.env.BUILD_ID = 'has space';
    await expect(transformed('build')).rejects.toThrow(/not a valid UI build ID/);
  });

  it('keeps the ABO settings the preset does not own', async () => {
    const cfg = await resolveConfig('build');
    expect(cfg.server?.port).toBe(5173);
    expect(cfg.server?.proxy?.['/api']?.target).toBe(process.env.MEDIACORE_DEV_TARGET || 'http://localhost:8484');
    expect(cfg.resolve?.alias?.['@']).toBe(path.resolve(import.meta.dirname, '../'));
    expect(cfg.build?.outDir).toBe('dist');
    expect(cfg.build?.sourcemap).toBe(true);
    expect(cfg.build?.chunkSizeWarningLimit).toBe(600);
  });

  it('dedupes the peer singletons and splits the mui and vendor chunks', async () => {
    const cfg = await resolveConfig('build');
    expect(cfg.resolve?.dedupe).toEqual([...SINGLETONS]);
    const groups = cfg.build?.rolldownOptions?.output?.codeSplitting?.groups ?? [];
    expect(groups.map((g) => g.name)).toEqual(['mui', 'vendor']);
  });
});
