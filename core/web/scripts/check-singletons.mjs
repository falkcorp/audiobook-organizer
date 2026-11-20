// file: core/web/scripts/check-singletons.mjs
// version: 0.1.0
// guid: 7b2f5e40-9c13-4d86-a5e1-3f8c0d6b2a97
// last-edited: 2026-11-20

// 07 §7.3.2 / §7.18: one copy of each peer singleton and of
// react/compiler-runtime in a built app. Reads the sourcemaps of a vite build
// (default: hello's web/dist) and fails when any singleton's files appear
// from two package directories, which a missed resolve.dedupe produces (two
// Reacts: hooks throw "invalid hook call" only at runtime).
// Usage: node scripts/check-singletons.mjs [distDir]

import { readdirSync, readFileSync } from 'node:fs';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { SINGLETONS } from '@mediacore/shell/vite';

const here = dirname(fileURLToPath(import.meta.url));
const dist = resolve(process.argv[2] ?? join(here, '../../examples/hello/web/dist'));

/** The package directory (…/node_modules/<name>) a source path comes from, for a singleton, or null. */
export function singletonDir(source) {
  const norm = source.replace(/\\/g, '/');
  for (const name of SINGLETONS) {
    const pkg = name === 'react/compiler-runtime' ? 'react' : name;
    const marker = `/node_modules/${pkg}/`;
    const at = norm.lastIndexOf(marker);
    if (at < 0) continue;
    if (name === 'react/compiler-runtime' && !/compiler-runtime/.test(norm.slice(at))) continue;
    return { name, dir: norm.slice(0, at + marker.length) };
  }
  return null;
}

export function findDuplicates(sources) {
  const seen = new Map();
  for (const s of sources) {
    const hit = singletonDir(s);
    if (!hit) continue;
    if (!seen.has(hit.name)) seen.set(hit.name, new Set());
    seen.get(hit.name).add(hit.dir);
  }
  return [...seen].filter(([, dirs]) => dirs.size > 1).map(([name, dirs]) => ({ name, dirs: [...dirs] }));
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  const maps = readdirSync(dist, { recursive: true }).map(String).filter((f) => f.endsWith('.js.map'));
  if (maps.length === 0) {
    console.error(`check-singletons: no sourcemaps under ${dist}; build with sourcemap: true first`);
    process.exit(2);
  }
  const sources = maps.flatMap((m) => {
    const map = JSON.parse(readFileSync(join(dist, m), 'utf8'));
    return (map.sources ?? []).map((s) => resolve(dirname(join(dist, m)), s));
  });
  const dups = findDuplicates(sources);
  for (const d of dups) console.error(`check-singletons: ${d.name} is bundled from ${d.dirs.length} paths:\n  ${d.dirs.join('\n  ')}`);
  if (dups.length) process.exit(1);
  console.log(`check-singletons: ${SINGLETONS.length} singletons, one copy each, across ${maps.length} sourcemaps`);
}
