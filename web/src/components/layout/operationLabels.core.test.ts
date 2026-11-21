// file: web/src/components/layout/operationLabels.core.test.ts
// version: 1.0.0
// guid: b2d86f13-7e40-4a9c-91d5-c3f08a64e2b7
// last-edited: 2026-11-21

// The def-id label table against the kind-rename table. The Go source is the oracle,
// as taskKinds.core.test.ts does for the books task kinds: this parses
// internal/books/cutover/kindrename/map_gen.go (every def id the server ever
// resolved: registered, retired, former) and testdata/head_registry.golden (which of
// those are registered, and which are former ids of which), and fails when a def has
// no label, two defs share one, or a label names a def that is not there. It replaces
// operationLabels.baseline.test.ts, which pinned the tail-keyed behaviour this fixes.

import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import {
  LEGACY_TYPE_DEFS,
  OPERATION_DEF_LABELS,
  UNREGISTERED_LABELLED_DEFS,
} from './operationLabels';
import { operationDisplayName } from './operationsFormat';

const dir = resolve(import.meta.dirname, '../../../../internal/books/cutover/kindrename');
const mapGen = readFileSync(resolve(dir, 'map_gen.go'), 'utf8');
const registry = readFileSync(resolve(dir, 'testdata/head_registry.golden'), 'utf8');

/** defTargets' keys. Every line must parse, so a format change fails loudly. */
function defTargetIds(): string[] {
  const m = /^var defTargets = [^\n]*\{\n([\s\S]*?)\n\}/m.exec(mapGen);
  if (!m) throw new Error('map_gen.go has no var defTargets');
  return m[1]!
    .split('\n')
    .filter((l) => l.trim() !== '')
    .map((l) => {
      const k = /^\t"([^"]+)":\s*\{/.exec(l);
      if (!k) throw new Error(`unparsed defTargets line: ${l}`);
      return k[1]!;
    });
}

function registryLines(prefix: 'id' | 'alias'): string[][] {
  return registry
    .split('\n')
    .filter((l) => l.startsWith(`${prefix} `))
    .map((l) => l.trim().split(/\s+/).slice(1));
}

const allDefs = defTargetIds();
const registered = registryLines('id').map(([id]) => id!);
/** former id to the registered def it was renamed to */
const formerOf = new Map(registryLines('alias').map(([old, canon]) => [old!, canon!]));
const retired = allDefs.filter((id) => !registered.includes(id) && !formerOf.has(id));

describe('the kind-rename inputs this test reads', () => {
  it('parse to a non-empty table of the expected shape', () => {
    expect(allDefs.length).toBeGreaterThan(250);
    expect(new Set(allDefs).size).toBe(allDefs.length);
    expect(registered.length).toBeGreaterThan(200);
    expect(formerOf.size).toBeGreaterThan(10);
    expect(retired.length).toBeGreaterThan(10);
    // every registered def and every former id is a def the table knows
    for (const id of [...registered, ...formerOf.keys()]) expect(allDefs, id).toContain(id);
  });
});

describe('OPERATION_DEF_LABELS', () => {
  it('labels every def id the kind-rename table knows', () => {
    const missing = allDefs.filter((id) => !OPERATION_DEF_LABELS[id]?.trim());
    expect(missing).toEqual([]);
  });

  it('has no key that matches no def', () => {
    const known = new Set([...allDefs, ...UNREGISTERED_LABELLED_DEFS]);
    expect(Object.keys(OPERATION_DEF_LABELS).filter((k) => !known.has(k))).toEqual([]);
  });

  it('keeps its one non-def exception honest: it names no def the table knows', () => {
    for (const id of UNREGISTERED_LABELLED_DEFS) {
      expect(allDefs, id).not.toContain(id);
      expect(OPERATION_DEF_LABELS[id], id).toBeDefined();
    }
  });

  it('gives no two defs the same label', () => {
    // A former id is the same def under its old name, so it is checked against its
    // canonical's label below, not for distinctness. Everything else is a def of its own.
    const own = [...registered, ...retired, ...UNREGISTERED_LABELLED_DEFS];
    const byLabel = new Map<string, string[]>();
    for (const id of own) {
      const label = OPERATION_DEF_LABELS[id]!.trim().toLowerCase();
      byLabel.set(label, [...(byLabel.get(label) ?? []), id]);
    }
    const shared = [...byLabel.entries()].filter(([, ids]) => ids.length > 1);
    expect(shared).toEqual([]);
  });

  it('labels a former id as the def it was renamed to', () => {
    for (const [old, canon] of formerOf) {
      expect(OPERATION_DEF_LABELS[old], old).toBe(OPERATION_DEF_LABELS[canon]);
    }
  });

  it('points every legacy type code at a registered def', () => {
    for (const [type, defId] of Object.entries(LEGACY_TYPE_DEFS)) {
      expect(registered, `${type} -> ${defId}`).toContain(defId);
    }
  });
});

describe('operationDisplayName', () => {
  /** What the bell shows when the server's display_name is absent or echoes the def id. */
  const bell = (defId: string) => operationDisplayName({ def_id: defId, displayName: defId });

  it('tells the three scans apart (they all read "Library Scan" when keyed on the tail)', () => {
    expect(bell('library.scan')).toBe('Library Scan');
    expect(bell('acoustid.scan')).toBe('AcoustID fingerprint scan');
    expect(bell('reconcile.scan')).toBe('Reconcile Scan');
  });

  it('labels the defs the old tail-keyed table could not reach', () => {
    expect(bell('openlibrary.import')).toBe('OpenLibrary Dump Import');
    expect(bell('dedup.full-scan')).toBe('Full dedup scan');
  });

  it('labels an operation that has no def_id yet by its legacy type code or def id', () => {
    expect(operationDisplayName({ type: 'scan' })).toBe('Library Scan');
    expect(operationDisplayName({ type: 'ol_dump_import' })).toBe('OpenLibrary Dump Import');
    expect(operationDisplayName({ type: 'acoustid.scan' })).toBe('AcoustID fingerprint scan');
  });

  it('prefers the server display_name, and still de-slugs a def nothing knows', () => {
    expect(operationDisplayName({ def_id: 'acoustid.scan', displayName: 'Custom' })).toBe('Custom');
    expect(bell('maintenance.title-backfill-nonexistent')).toBe('Title Backfill Nonexistent');
  });
});
