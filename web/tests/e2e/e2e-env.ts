// file: tests/e2e/e2e-env.ts
// version: 1.0.0
// guid: 4d9e2b71-6c05-483a-9f1e-7b28d0c64a53
// last-edited: 2026-08-14

import { dirname, join } from 'path';
import { fileURLToPath } from 'url';

const __dirname = dirname(fileURLToPath(import.meta.url));

/** Absolute repo root of THIS worktree (…/tests/e2e -> repo root). */
export const REPO_ROOT = join(__dirname, '../../..');

/**
 * Per-worktree e2e port (H113).
 *
 * The port used to be a hardcoded 8484 with `reuseExistingServer` on, so two
 * worktrees' agents contended for one port — and on 2026-08-11 a suite ran its
 * assertions against a SIBLING worktree's server whose bind had succeeded
 * first (the loser logged the bind error and idled, curl said 200, everything
 * looked healthy). Deriving the port from the worktree path makes contention
 * structurally impossible: same worktree → same port every run, different
 * worktree → different port.
 *
 * Range 8500–8899: clear of the dev server's 8484 and of prod's convention.
 * E2E_PORT overrides for the rare case two paths hash together or a port is
 * occupied by something unrelated.
 */
function hashPath(p: string): number {
  let h = 5381;
  for (let i = 0; i < p.length; i++) {
    h = ((h << 5) + h + p.charCodeAt(i)) >>> 0; // djb2
  }
  return h;
}

export const E2E_PORT: number = process.env.E2E_PORT
  ? Number.parseInt(process.env.E2E_PORT, 10)
  : 8500 + (hashPath(REPO_ROOT) % 400);

/**
 * Per-worktree scratch prefix. The old fixed /tmp/ao-e2e-db meant two
 * concurrent worktrees clobbered each other's Pebble even on different ports.
 */
export const E2E_TMP_PREFIX = `/tmp/ao-e2e-${(hashPath(REPO_ROOT) % 0xffff).toString(16).padStart(4, '0')}`;
