// file: web/src/components/layout/operationLabels.baseline.test.ts
// version: 1.0.0
// guid: 3c7a9e52-1d84-4b06-a5f3-8e2b6d90c147
// last-edited: 2026-11-21

// BASELINE of the bell's label fallback before it is keyed on full def ids. This
// pins what operationDisplayName does today, defects included, so the change
// that fixes them shows up in review as a deliberate flip of these assertions
// rather than as a silent behaviour change. It is replaced by
// operationLabels.core.test.ts in the commit that fixes the labels.

import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { operationDisplayName } from './operationsFormat';

const registry = readFileSync(
  resolve(
    import.meta.dirname,
    '../../../../internal/books/cutover/kindrename/testdata/head_registry.golden'
  ),
  'utf8'
);

/** Every canonical def id HEAD registers (the `id` lines of the registry dump). */
function registeredDefIds(): string[] {
  return registry
    .split('\n')
    .filter((l) => l.startsWith('id '))
    .map((l) => l.slice(3).trim());
}

/** What the bell shows for a def whose server display_name is absent or echoes the def id. */
function bellLabel(defId: string): string {
  return operationDisplayName({ def_id: defId, displayName: defId });
}

describe('operationDisplayName baseline (tail-keyed labels)', () => {
  it('reads the registry dump', () => {
    expect(registeredDefIds().length).toBeGreaterThan(200);
  });

  it('gives three unrelated scans the one label "Library Scan" (the collision)', () => {
    expect(bellLabel('library.scan')).toBe('Library Scan');
    expect(bellLabel('acoustid.scan')).toBe('Library Scan');
    expect(bellLabel('reconcile.scan')).toBe('Library Scan');
  });

  it('labels the keys that match no registered def, by type or by tail', () => {
    // ol_dump_import and dedup-scan are keyed by names no def id has: the
    // registered ids are openlibrary.import and dedup.full-scan.
    expect(operationDisplayName({ type: 'ol_dump_import' })).toBe('Open Library Import');
    expect(operationDisplayName({ type: 'dedup-scan' })).toBe('Dedup Scan');
    expect(bellLabel('openlibrary.import')).toBe('Import');
    expect(bellLabel('dedup.full-scan')).toBe('Full Scan');
  });

  it('de-slugs the def-id tail for most defs, so unrelated defs share a label', () => {
    expect(bellLabel('maintenance.title-backfill')).toBe('Title Backfill');
    expect(bellLabel('maintenance.bulk-write-back')).toBe('Bulk Write Back');
    expect(bellLabel('library.bulk-write-back')).toBe('Bulk Write Back');
  });

  it('counts the registered defs that share their label with another def', () => {
    const byLabel = new Map<string, string[]>();
    for (const id of registeredDefIds()) {
      const label = bellLabel(id);
      byLabel.set(label, [...(byLabel.get(label) ?? []), id]);
    }
    const shared = [...byLabel.values()].filter((ids) => ids.length > 1);
    const defsInCollision = shared.reduce((n, ids) => n + ids.length, 0);
    // 12 defs sit in shared labels, e.g. the three scans above and the two bulk
    // write-backs; the other 224 read as a de-slugged tail, never a curated name.
    expect({
      registered: registeredDefIds().length,
      labels: byLabel.size,
      defsInCollision,
    }).toEqual({ registered: 236, labels: 229, defsInCollision: 12 });
  });
});
