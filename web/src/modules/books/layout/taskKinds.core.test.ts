// file: web/src/modules/books/layout/taskKinds.core.test.ts
// version: 0.2.0
// guid: 9e1c4a07-3b52-4d68-8f20-c5a7d0b3e619
// last-edited: 2026-11-21

// The books taskKinds against the kind-rename table. The Go source is the oracle,
// as core/web/packages/shell/test/taskRuns.core.test.ts does for the run states:
// no TypeScript list of the table exists, so this test parses
// internal/books/cutover/kindrename/{map_gen,rules}.go and fails the moment a
// label's kind is not where the table sends its legacy def id, or a kind the
// table no longer produces is still labelled.

import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it } from 'vitest';
import { OPERATION_DEF_LABELS } from '../../../components/layout/operationLabels';
import { BOOKS_KIND_LABELS, booksTaskKinds } from './taskKinds';

const dir = resolve(import.meta.dirname, '../../../../../internal/books/cutover/kindrename');
const mapGen = readFileSync(resolve(dir, 'map_gen.go'), 'utf8');
const rules = readFileSync(resolve(dir, 'rules.go'), 'utf8');

interface Entry {
  kind: string;
  rule: string;
}

/** The body of `var <name> = map[...]... {` up to its closing brace at column 0. */
function mapBody(name: string): string {
  const m = new RegExp(`^var ${name} = [^\\n]*\\{\\n([\\s\\S]*?)\\n\\}`, 'm').exec(mapGen);
  if (!m) throw new Error(`map_gen.go has no var ${name}`);
  return m[1]!;
}

/** defTargets: HEAD def id to {Kind, Rule}. Every line must parse, so a format change fails loudly. */
function defTargets(): Map<string, Entry> {
  const out = new Map<string, Entry>();
  const lines = mapBody('defTargets')
    .split('\n')
    .filter((l) => l.trim() !== '');
  for (const l of lines) {
    // The two repairs defs carry no Kind: the fixer id names it per run (a run-level rule).
    const m = /^\t"([^"]+)":\s*\{(?:Kind: "([^"]+)", )?Rule: (\w+)/.exec(l);
    if (!m) throw new Error(`unparsed defTargets line: ${l}`);
    out.set(m[1]!, { kind: m[2] ?? '', rule: m[3]! });
  }
  return out;
}

function schedulerKinds(): Set<string> {
  const lines = mapBody('schedulerTargets')
    .split('\n')
    .filter((l) => l.trim() !== '');
  return new Set(
    lines.map((l) => {
      const m = /^\t"([^"]+)":\s*\{(?:Kind: "([^"]*)")?/.exec(l);
      if (!m) throw new Error(`unparsed schedulerTargets line: ${l}`);
      return m[2] ?? '';
    })
  );
}

function goConst(name: string): string {
  const m = new RegExp(`^\\t${name}\\s+tasks\\.Kind = "([^"]+)"`, 'm').exec(rules);
  if (!m) throw new Error(`rules.go has no const ${name}`);
  return m[1]!;
}

const retiredPrefix = /^\tRetiredKindPrefix\s*= "([^"]+)"/m.exec(rules)![1]!;
const targets = defTargets();

describe('the kind-rename table as parsed', () => {
  it('is read in full (a vacuous parse would pass every test below)', () => {
    expect(targets.size).toBeGreaterThan(250);
    expect(targets.get('library.scan')).toEqual({ kind: 'books.library.scan', rule: 'RuleBooks' });
    expect(targets.get('itunes.sync')).toEqual({
      kind: 'books.retired.itunes.sync',
      rule: 'RuleRetired',
    });
    expect(retiredPrefix).toBe('books.retired.');
    expect(goConst('FullSweepKind')).toBe('books.library.scan-full');
  });
});

describe('booksTaskKinds against the kind-rename table', () => {
  it.each(BOOKS_KIND_LABELS.map((e) => [e.defId, e] as const))(
    'sends %s to the kind its label is keyed by',
    (_id, e) => {
      const t = targets.get(e.defId);
      // An ID the table never knew resolves by rule 5 (books.retired.<id>).
      const want = t ? t.kind : `${retiredPrefix}${e.defId}`;
      expect(e.kind).toBe(want);
      const rule = t ? t.rule : 'RuleRetired';
      // A history label must be on a retired kind, and a live label must not.
      expect(rule === 'RuleRetired').toBe(e.history === true);
      if (e.alsoKind) {
        // The only def id whose runs split is library.scan, into scan and scan-full.
        expect(e.defId).toBe('library.scan');
        expect(e.alsoKind).toBe(goConst('FullSweepKind'));
        expect(e.kind).toBe(goConst('ScanKind'));
      }
    }
  );

  it('labels only kinds the table or the scheduler produces', () => {
    const produced = new Set<string>([
      ...[...targets.values()].map((t) => t.kind).filter((k) => k !== ''),
      ...[...schedulerKinds()].filter((k) => k !== ''),
      goConst('ScanKind'),
      goConst('FullSweepKind'),
    ]);
    // A retired kind is produced by rule 5 for an id outside the generated map.
    for (const e of BOOKS_KIND_LABELS) {
      const ok = produced.has(e.kind) || e.kind === `${retiredPrefix}${e.defId}`;
      expect(ok, `${e.kind} is not a kind the table produces`).toBe(true);
    }
    for (const kind of Object.keys(booksTaskKinds)) {
      const entry = BOOKS_KIND_LABELS.find((e) => e.kind === kind || e.alsoKind === kind);
      expect(entry, `${kind} has no def id evidence`).toBeDefined();
      expect(produced.has(kind) || kind.startsWith(retiredPrefix), kind).toBe(true);
    }
  });

  it('keys every kind under the books namespace with a non-empty label', () => {
    const kinds = Object.entries(booksTaskKinds);
    expect(kinds.length).toBeGreaterThan(0);
    for (const [kind, p] of kinds) {
      expect(kind.startsWith('books.')).toBe(true);
      expect(p.label.trim()).not.toBe('');
    }
  });

  it('has one entry per def id and one label per kind', () => {
    const ids = BOOKS_KIND_LABELS.map((e) => e.defId);
    expect(new Set(ids).size).toBe(ids.length);
    const kinds = BOOKS_KIND_LABELS.flatMap((e) => [e.kind, ...(e.alsoKind ? [e.alsoKind] : [])]);
    expect(new Set(kinds).size).toBe(kinds.length);
    expect(Object.keys(booksTaskKinds).sort()).toEqual([...kinds].sort());
  });

  it('labels each kind with the label the bell shows for its def id', () => {
    // One wording in both places: the def-id label table is what the bell and the
    // Activity page fall back to, and these are the task-run labels for the same defs.
    for (const e of BOOKS_KIND_LABELS) {
      expect(OPERATION_DEF_LABELS[e.defId], e.defId).toBeDefined();
      expect(e.label, e.defId).toBe(OPERATION_DEF_LABELS[e.defId]);
    }
  });
});
