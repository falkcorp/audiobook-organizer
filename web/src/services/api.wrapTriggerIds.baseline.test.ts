// file: web/src/services/api.wrapTriggerIds.baseline.test.ts
// version: 1.0.0
// guid: d93b5e07-2a61-4c48-b0f7-14c8a6e5d329
// last-edited: 2026-11-21

// BASELINE of the operation ids api.ts hands wrapTrigger. wrapTrigger inserts the
// bell's optimistic placeholder under that id, and the bell labels the placeholder
// from it, so an id no def has leaves the placeholder labelled with its raw text.
// This pins the five ids that match no registered def today; it is replaced by
// api.wrapTriggerIds.core.test.ts in the commit that points them at real defs.

import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

const apiSource = readFileSync(resolve(import.meta.dirname, 'api.ts'), 'utf8');
const registry = readFileSync(
  resolve(
    import.meta.dirname,
    '../../../internal/books/cutover/kindrename/testdata/head_registry.golden'
  ),
  'utf8'
);
const registered = new Set(
  registry
    .split('\n')
    .filter((l) => l.startsWith('id '))
    .map((l) => l.slice(3).trim())
);

/** The literal first argument of every wrapTrigger(...) call in api.ts. */
function wrapTriggerIds(): string[] {
  return [...apiSource.matchAll(/\bwrapTrigger(?:<[^>(]*>)?\(\s*'([^']+)'/g)].map((m) => m[1]!);
}

describe('api.ts wrapTrigger ids (baseline)', () => {
  it('finds the calls', () => {
    expect(wrapTriggerIds().length).toBeGreaterThan(15);
  });

  it('has exactly five ids that no registered def has', () => {
    const unregistered = wrapTriggerIds().filter((id) => !registered.has(id));
    expect(unregistered.sort()).toEqual([
      'dedup.embed',
      'dedup.scan',
      'dedup.scan-llm',
      'library.reconcile',
      'library.reconcile-scan',
    ]);
  });
});
