// file: web/src/stores/operationGrouping.test.ts
// version: 1.2.0
// guid: 4d19a6f2-83bc-4571-b0e8-27fa5c96de13
// last-edited: 2026-09-10

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

  // The idle gap splits a run in the first pass, but two pieces with nothing
  // of another kind between them are "next to each other" and the second pass
  // joins them again. The gap only holds when something else sits in it.
  it('re-joins the pieces an idle gap split when nothing else lies between', () => {
    const first = run(4, 10_000);
    const second = run(4, 10_000).map((o, i) => ({
      ...o,
      id: `late-${i}`,
      finishedAt: (o.finishedAt ?? 0) + GROUP_IDLE_GAP_MS + 60_000,
    }));

    const out = groupOperations([...first, ...second]);

    expect(parents(out)).toHaveLength(1);
    expect(parents(out)[0].group?.count).toBe(8);
  });

  it('keeps the pieces apart when another kind sits in the idle gap', () => {
    const first = run(4, 10_000);
    const second = run(4, 10_000).map((o, i) => ({
      ...o,
      id: `late-${i}`,
      finishedAt: (o.finishedAt ?? 0) + GROUP_IDLE_GAP_MS + 60_000,
    }));
    const between = op({
      id: 'scan-between',
      def_id: 'library.scan',
      type: 'scan',
      finishedAt: T0 + 3 * 10_000 + 30_000,
    });

    const out = groupOperations([...first, between, ...second]);

    expect(parents(out).map((p) => p.group?.count)).toEqual([4, 4]);
    expect(out.find((o) => o.id === 'scan-between')?.parent_id).toBeFalsy();
  });

  // A gap of exactly the threshold is NOT a split — the test is `>`, so a
  // steady drumbeat at the boundary stays one group instead of splitting into
  // singletons.
  it('does not split on a gap exactly at the threshold', () => {
    const out = groupOperations(run(4, GROUP_IDLE_GAP_MS));
    expect(parents(out)).toHaveLength(1);
    expect(parents(out)[0].group?.count).toBe(4);
  });

  // The span cap breaks a busy run up in the first pass; the second pass joins
  // the pieces back when nothing else ran between them, so a six-hour drumbeat
  // of one kind is one row. The cap only shows when another kind interleaves.
  it('re-joins the pieces the span cap split when nothing else lies between', () => {
    // 40 ops one minute apart: never idle, 39 minutes of span.
    const out = groupOperations(run(40, 60_000));

    expect(parents(out)).toHaveLength(1);
    expect(parents(out)[0].group?.count).toBe(40);
    expect((parents(out)[0].group?.lastAt ?? 0) - (parents(out)[0].group?.firstAt ?? 0)).toBe(
      39 * 60_000
    );
  });

  it('keeps the span-capped pieces apart when another kind sits between them', () => {
    // First pass: ops 0..30 (span exactly the cap) and 31..39. A scan lands
    // after the first piece ends and before the second begins.
    const between = op({
      id: 'scan-between',
      def_id: 'library.scan',
      type: 'scan',
      finishedAt: T0 + GROUP_MAX_SPAN_MS + 30_000,
    });

    const out = groupOperations([...run(40, 60_000), between]);

    const aiParents = parents(out).filter((p) => p.def_id === 'library.ai-parse');
    expect(aiParents).toHaveLength(2);
    for (const p of aiParents) {
      expect((p.group?.lastAt ?? 0) - (p.group?.firstAt ?? 0)).toBeLessThanOrEqual(GROUP_MAX_SPAN_MS);
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

// Server-declared lineage outranks inferred timing. Nothing sets parent_id in
// production today, so this is a guard for the day something does: grouping
// must not re-parent a row away from a parent that really did spawn it.
describe('groupOperations and real lineage', () => {
  it('leaves an op that already declares a parent alone', () => {
    const owned = run(5, 10_000).map((o) => ({ ...o, parent_id: 'real-parent' }));

    const out = groupOperations(owned);

    expect(out.filter((o) => o.group)).toHaveLength(0);
    for (const o of out) expect(o.parent_id).toBe('real-parent');
  });

  it('still groups the parentless ops beside them', () => {
    const owned = run(4, 10_000).map((o, i) => ({
      ...o,
      id: `owned-${i}`,
      parent_id: 'real-parent',
    }));

    const out = groupOperations([...owned, ...run(4, 10_000)]);

    const synthetic = out.filter((o) => o.group);
    expect(synthetic).toHaveLength(1);
    expect(synthetic[0].group?.count).toBe(4);
    expect(synthetic[0].group?.memberIds.every((id) => id.startsWith('op-'))).toBe(true);
  });
});

// The second pass. The first pass folds by kind with a gap and a span rule,
// which leaves a long run as a chain of ×N rows with stray singletons between
// them. Anything of one kind that ends up next to each other in the timeline
// is one thing to the reader, so it becomes one row.
describe('groupOperations second pass (adjacent rows)', () => {
  it('folds a same-kind singleton after a group into the group', () => {
    const late = op({ id: 'late', finishedAt: T0 + 4 * 10_000 + GROUP_IDLE_GAP_MS + 60_000 });

    const out = groupOperations([...run(5, 10_000), late]);

    expect(parents(out)).toHaveLength(1);
    expect(parents(out)[0].group?.count).toBe(6);
    expect(out.find((o) => o.id === 'late')?.parent_id).toBe(parents(out)[0].id);
  });

  it('turns three far-apart singletons into one group', () => {
    // Ten minutes apart: the first pass leaves all three alone.
    const out = groupOperations(run(3, 10 * 60_000));

    expect(parents(out)).toHaveLength(1);
    expect(parents(out)[0].group?.count).toBe(3);
  });

  it('leaves two lone singletons alone (below the floor)', () => {
    const out = groupOperations(run(2, 10 * 60_000));

    expect(parents(out)).toHaveLength(0);
    expect(out).toHaveLength(2);
  });

  it('keeps the earlier group id and lists every member oldest first', () => {
    const first = run(4, 10_000);
    const second = run(4, 10_000).map((o, i) => ({
      ...o,
      id: `late-${i}`,
      finishedAt: (o.finishedAt ?? 0) + GROUP_IDLE_GAP_MS + 60_000,
    }));
    const firstAlone = parents(groupOperations(first))[0].id;

    const merged = parents(groupOperations([...second, ...first]))[0];

    expect(merged.id).toBe(firstAlone);
    expect(merged.group?.memberIds).toEqual([
      'op-0',
      'op-1',
      'op-2',
      'op-3',
      'late-0',
      'late-1',
      'late-2',
      'late-3',
    ]);
    expect(merged.group?.firstAt).toBe(T0);
    expect(merged.group?.lastAt).toBe(T0 + 3 * 10_000 + GROUP_IDLE_GAP_MS + 60_000);
  });

  it('never joins across statuses even when adjacent', () => {
    const failed = run(3, 10_000);
    const ok = run(3, 10_000).map((o, i) => ({
      ...o,
      id: `ok-${i}`,
      status: 'completed',
      finishedAt: (o.finishedAt ?? 0) + 10 * 60_000,
    }));

    const out = groupOperations([...failed, ...ok]);

    expect(parents(out).map((p) => [p.status, p.group?.count])).toEqual([
      ['failed', 3],
      ['completed', 3],
    ]);
  });

  it('is stable: the same input grouped twice yields the same rows', () => {
    const ops = [...run(5, 10_000), op({ id: 'late', finishedAt: T0 + 20 * 60_000 })];
    expect(groupOperations([...ops].reverse())).toEqual(groupOperations(ops));
  });
});
