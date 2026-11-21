// file: web/src/services/api.wrapTriggerIds.core.test.ts
// version: 1.0.0
// guid: 17e4c9a2-5b80-4d36-a1f9-3c6d08b2e7a5
// last-edited: 2026-11-21

// Every operation id api.ts hands wrapTrigger is a registered def. wrapTrigger inserts
// the bell's optimistic placeholder under that id, and the bell labels the placeholder
// from it, so an id no def has leaves the placeholder showing its raw text (it showed
// "Library.Scan starting…" and "Dedup.Scan starting…" until the ids named real defs).
// The registered ids come from the kind-rename registry dump, the oracle the label
// tests use. Replaces api.wrapTriggerIds.baseline.test.ts.

import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { OPERATION_DEF_LABELS } from '../components/layout/operationLabels';
import { operationDisplayName } from '../components/layout/operationsFormat';

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

const CALL = /\bwrapTrigger(?:<[^>(]*>)?\(/g;

/** The literal first argument of every wrapTrigger(...) call in api.ts. */
function wrapTriggerIds(): string[] {
  return [...apiSource.matchAll(/\bwrapTrigger(?:<[^>(]*>)?\(\s*'([^']+)'/g)].map((m) => m[1]!);
}

describe('api.ts wrapTrigger ids', () => {
  it('reads every call site, and every one passes a string literal', () => {
    const calls = [...apiSource.matchAll(CALL)].length - 1; // the declaration is not a call
    expect(wrapTriggerIds().length).toBe(calls);
    expect(calls).toBeGreaterThan(15);
  });

  it('are all registered defs', () => {
    expect(wrapTriggerIds().filter((id) => !registered.has(id))).toEqual([]);
  });

  it('names, for each endpoint it wraps, the def that endpoint enqueues', () => {
    // Registered is not enough: a placeholder labelled for the wrong def is as wrong as
    // an unlabelled one. The Go handlers are the oracle for these (each EnqueueOp call
    // names its def): POST /dedup/scan in internal/server/handlers/dedup/handler.go
    // enqueues dedup.full-scan, /dedup/scan-llm dedup.llm-review, /dedup/embed
    // dedup.embed-scan; startReconcile and startReconcileScan in
    // internal/server/reconcile.go enqueue reconcile.apply and reconcile.scan.
    const byPath = new Map(
      [
        ...apiSource.matchAll(
          /wrapTrigger\('([^']+)',\s*async \(\) => \{\s*const response = await apiFetch\(`\$\{API_BASE\}([^`]+)`/g
        ),
      ].map((m) => [m[2]!, m[1]!])
    );
    expect(Object.fromEntries(byPath)).toMatchObject({
      '/operations/reconcile': 'reconcile.apply',
      '/operations/reconcile/scan': 'reconcile.scan',
      '/dedup/scan': 'dedup.full-scan',
      '/dedup/scan-llm': 'dedup.llm-review',
      '/dedup/embed': 'dedup.embed-scan',
    });
  });

  it('give each placeholder the label of its def', () => {
    for (const id of new Set(wrapTriggerIds())) {
      expect(OPERATION_DEF_LABELS[id], id).toBeDefined();
      expect(operationDisplayName({ type: id }), id).toBe(OPERATION_DEF_LABELS[id]);
    }
  });
});
