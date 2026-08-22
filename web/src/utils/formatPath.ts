// file: web/src/utils/formatPath.ts
// version: 1.0.1
// guid: 5d9a2c84-7e61-4f30-b8a2-0c6f1e9d4a72
// last-edited: 2026-08-22

// Path abbreviation for the UI: replace known library roots with literal
// $(var) tokens so paths render short. Mirrors the backend rules in
// internal/pathutil/abbreviate.go — keep the two in sync.

import { useEffect, useState } from 'react';
import { getConfig } from '../services/api';

export interface PathVar {
  name: string;
  value: string;
}

/**
 * formatPath replaces the most-specific matching root in `path` with a literal
 * `$(name)` token. `vars` are checked in order, so the caller must list the
 * most-specific root first (libroot before books). Empty-valued vars are
 * skipped so they never match everything. Unmatched paths are returned as-is.
 */
export function formatPath(path: string, vars: PathVar[]): string {
  for (const v of vars) {
    if (!v.value) continue;
    if (path === v.value) return `$(${v.name})`;
    if (path.startsWith(v.value + '/')) return `$(${v.name})` + path.slice(v.value.length);
  }
  return path;
}

/**
 * derivePathVars builds the abbreviation vars from the library root dir:
 * libroot = rootDir (trailing slash stripped), books = its parent directory.
 */
export function derivePathVars(rootDir: string): PathVar[] {
  const root = rootDir.replace(/\/+$/, '');
  if (!root) return [];
  const i = root.lastIndexOf('/');
  const books = i > 0 ? root.slice(0, i) : root;
  return [
    { name: 'libroot', value: root },
    { name: 'books', value: books },
  ];
}

// Single shared fetch of the config so every consumer of usePathVars reuses one
// request rather than each refetching /config.
let cachedVarsPromise: Promise<PathVar[]> | null = null;

function loadPathVars(): Promise<PathVar[]> {
  if (!cachedVarsPromise) {
    cachedVarsPromise = getConfig()
      .then((cfg) => derivePathVars(cfg.root_dir || ''))
      .catch(() => {
        // On failure, don't poison the cache — allow a later retry.
        cachedVarsPromise = null;
        return [];
      });
  }
  return cachedVarsPromise;
}

/**
 * usePathVars returns the abbreviation vars (libroot/books), loaded once from
 * config and shared across all callers. Empty until loaded.
 */
export function usePathVars(): PathVar[] {
  const [vars, setVars] = useState<PathVar[]>([]);
  useEffect(() => {
    let alive = true;
    void loadPathVars().then((v) => {
      if (alive) setVars(v);
    });
    return () => {
      alive = false;
    };
  }, []);
  return vars;
}

/**
 * Test-only: clears the module-scope config-fetch cache so the next
 * usePathVars()/loadPathVars() call re-fetches. Mirrors
 * __resetPathAliasesCacheForTests in components/common/PathLinks.tsx -- call it
 * from beforeEach/afterEach in any test file that needs a fresh or per-test var
 * set, so the first test in a file cannot seed the promise for every later one.
 *
 * Safe to call while a fetch is still in flight: nulling the `let` only means
 * that promise's result stops being cached, and the in-flight consumer's
 * `.then` still fires against its own promise reference.
 */
export function __resetPathVarsCacheForTests(): void {
  cachedVarsPromise = null;
}
