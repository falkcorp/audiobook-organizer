// file: core/web/packages/shell/test/reservedSegments.test.ts
// version: 0.1.0
// guid: 6e1a9c47-2d58-4f03-b7e6-0c9f4a2d8b13
// last-edited: 2026-11-20

// 07 §7.18: the shell route table equals Go's ui.ReservedSegments() (read
// from core/ui/leaf.go, the one Go list), and there is no /logout page.

import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { SHELL_ROUTES, shellReservedSegments } from '../src/routes';

function goReservedSegments(): string[] {
  const src = readFileSync(join(import.meta.dirname, '../../../../ui/leaf.go'), 'utf8');
  const body = /func ReservedSegments\(\) \[\]string \{\s*return \[\]string\{([^}]*)\}/.exec(src);
  if (!body) throw new Error('core/ui/leaf.go: ReservedSegments has changed shape; update this reader');
  return [...body[1].matchAll(/"([^"]+)"/g)].map((m) => m[1]);
}

describe('shell routes and reserved segments', () => {
  it('equal ui.ReservedSegments(), in the same order', () => {
    const go = goReservedSegments();
    expect(go.length).toBeGreaterThan(0);
    expect(shellReservedSegments()).toEqual(go);
  });

  it('has no /logout page (signing out is a user-menu action, FE2-7)', () => {
    expect(SHELL_ROUTES.some((r) => r.path.split('/')[1] === 'logout')).toBe(false);
  });

  it('keeps the one-time-token pages public', () => {
    for (const id of ['login', 'loginTemp', 'setup', 'invite'] as const) {
      expect(SHELL_ROUTES.find((r) => r.id === id)?.auth).toBe(false);
    }
  });
});
