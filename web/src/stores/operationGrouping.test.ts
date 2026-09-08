// file: web/src/stores/operationGrouping.test.ts
// version: 1.0.0
// guid: 4d19a6f2-83bc-4571-b0e8-27fa5c96de13
// last-edited: 2026-09-08

import { describe, it, expect } from 'vitest';
import {
  groupOperations,
  GROUP_IDLE_GAP_MS,
  GROUP_MAX_SPAN_MS,
  MIN_GROUP_SIZE,
} from './operationGrouping';
import type { ActiveOperation } from './useOperationsStore';

const T0 = Date.UTC(2026, 8, 8, 19, 0, 0);

function op(over: Partial<ActiveOperation> & { id: string }): ActiveOperation {
  return {
    def_id: 'library.ai-parse',
    type: 'ai-parse',
    displayName: 'AI Filename Parsing',
    status: 'failed',
    progress: 0,
    total: 5,
    message: '0/5 book(s) parsed',
    finishedAt: T0,
    ...over,
  };
}

/** run builds n same-kind ops spaced `stepMs` apart. */
function run(n: number, stepMs: number, over: Partial<ActiveOperation> = {}): ActiveOperation[] {
  return Array.from({ length: n }, (_, i) =>
    op({ id: `op-${i}`, finishedAt: T0 + i * stepMs, ...over })
  );
}

const parents = (ops: ActiveOperation[]) => ops.filter((o) => o.group);
const children = (ops: ActiveOperation[]) => ops.filter((o) => o.parent_id);

describe('groupOperations', () => {
  // The reported shape: a dozen consecutive AI-parse runs, seconds apart.
  it('folds a run of same-kind ops into one parent', () => {
    const out = groupOperations(run(12, 10_000));

    expect(parents(out)).toHaveLength(1);
    expect(parents(out)[0].group?.count).toBe(12);
    expect(children(out)).toHaveLength(12);
    // Every child points at the parent that is actually present.
    const parentId = parents(out)[0].id;
    for (const c of children(out)) expect(c.parent_id).toBe(parentId);
  });

  it('starts a new group when the idle gap is exceeded', () => {
    const first = run(4, 10_000);
    const second = run(4, 10_000).map((o, i) => ({
      ...o,
      id: `late-${i}`,
      finishedAt: (o.finishedAt ?? 0) + GROUP_IDLE_GAP_MS + 60_000,
    }));

    const out = groupOperations([...first, ...second]);

    expect(parents(out)).toHaveLength(2);
    expect(parents(out).map((p) => p.group?.count)).toEqual([4, 4]);
  });

  // A gap of exactly the threshold is NOT a split — the test is `>`, so a
  // steady drumbeat at the boundary stays one group instead of splitting into
  // singletons.
  it('does not split on a gap exactly at the threshold', () => {
    const out = groupOperations(run(4, GROUP_IDLE_GAP_MS));
    expect(parents(out)).toHaveLength(1);
    expect(parents(out)[0].group?.count).toBe(4);
  });

  // The hard cap: a busy run with no idle gap must still be broken up, or one
  // group swallows a whole day.
  it('starts a new group when the span cap is exceeded', () => {
    // 40 ops one minute apart: never idle, but 39 minutes of span.
    const out = groupOperations(run(40, 60_000));

    expect(parents(out).length).toBeGreaterThan(1);
    for (const p of parents(out)) {
      const span = (p.group?.lastAt ?? 0) - (p.group?.firstAt ?? 0);
      expect(span).toBeLessThanOrEqual(GROUP_MAX_SPAN_MS);
    }
  });

  it('leaves a run below the minimum size alone', () => {
    const out = groupOperations(run(MIN_GROUP_SIZE - 1, 10_000));

    expect(parents(out)).toHaveLength(0);
    expect(out).toHaveLength(MIN_GROUP_SIZE - 1);
    // A group of 2 would render 3 rows where there were 2 — strictly worse.
    expect(children(out)).toHaveLength(0);
  });

  // Status is in the key, so a group can never span two Activity-page sections
  // and collapsing one can never hide a failed row from the Failed section.
  it('never groups across statuses', () => {
    const mixed = [
      ...run(4, 10_000),
      ...run(4, 10_000).map((o, i) => ({ ...o, id: `ok-${i}`, status: 'completed' })),
    ];

    const out = groupOperations(mixed);

    expect(parents(out)).toHaveLength(2);
    for (const p of parents(out)) {
      const kids = out.filter((o) => o.parent_id === p.id);
      expect(kids.every((k) => k.status === p.status)).toBe(true);
    }
  });

  it('never groups across operation types', () => {
    const mixed = [
      ...run(4, 10_000),
      ...run(4, 10_000).map((o, i) => ({
        ...o,
        id: `scan-${i}`,
        def_id: 'library.scan',
        type: 'scan',
      })),
    ];

    const out = groupOperations(mixed);

    expect(parents(out)).toHaveLength(2);
    expect(new Set(parents(out).map((p) => p.def_id))).toEqual(
      new Set(['library.ai-parse', 'library.scan'])
    );
  });

  // THE constraint: ids must be stable across polls or collapsedParents thrashes
  // and every group silently re-expands every five seconds. The input order is
  // Object.values() over a map rebuilt on each poll, so shuffling is the
  // realistic case, not a contrived one.
  it('produces identical ids for shuffled input', () => {
    const ops = run(12, 10_000);
    const shuffled = [...ops].reverse();

    const a = parents(groupOperations(ops)).map((p) => p.id);
    const b = parents(groupOperations(shuffled)).map((p) => p.id);

    expect(a).toEqual(b);
    expect(a[0]).not.toMatch(/optimistic|[0-9a-f]{8}-[0-9a-f]{4}/); // not a minted uuid
  });

  it('produces identical ids when ops share a timestamp', () => {
    // Queued ops from one scan land on the same millisecond; only the id
    // tiebreak keeps the fold deterministic.
    const same = Array.from({ length: 6 }, (_, i) =>
      op({ id: `q-${i}`, status: 'queued', finishedAt: undefined, startedAt: T0 })
    );

    const a = parents(groupOperations(same)).map((p) => p.id);
    const b = parents(groupOperations([...same].reverse())).map((p) => p.id);

    expect(a).toEqual(b);
    expect(a).toHaveLength(1);
  });

  it('keeps its id when the group gains a newer member', () => {
    const ops = run(5, 10_000);
    const before = parents(groupOperations(ops))[0].id;

    const after = parents(
      groupOperations([...ops, op({ id: 'op-5', finishedAt: T0 + 5 * 10_000 })])
    )[0].id;

    expect(after).toBe(before);
  });

  it('does not mutate the input array or its members', () => {
    const ops = run(5, 10_000);
    const snapshot = JSON.parse(JSON.stringify(ops));

    groupOperations(ops);

    expect(JSON.parse(JSON.stringify(ops))).toEqual(snapshot);
  });

  it('carries an aggregate the parent row can render', () => {
    const out = groupOperations(run(12, 10_000, { progress: 0, total: 5 }));
    const p = parents(out)[0];

    expect(p.displayName).toBe('AI Filename Parsing');
    expect(p.status).toBe('failed');
    expect(p.total).toBe(60); // 12 runs × 5 books
    expect(p.progress).toBe(0);
    // Never in the bell badge: that counts real work.
    expect(p.notify_level).toBe(1);
  });

  it('returns ungrouped ops untouched', () => {
    const lonely = op({ id: 'solo', def_id: 'library.scan', type: 'scan' });
    const out = groupOperations([...run(4, 10_000), lonely]);

    expect(out.find((o) => o.id === 'solo')).toEqual(lonely);
  });

  it('handles an empty input', () => {
    expect(groupOperations([])).toEqual([]);
  });
});
