// file: web/src/stores/operationGrouping.ts
// version: 1.2.0
// guid: 8c4a1f37-2b95-4e60-9d13-6a7fb2e08c54
// last-edited: 2026-09-10

import type { ActiveOperation } from './useOperationsStore';

/**
 * Read-time grouping of consecutive same-type operations.
 *
 * WHY READ-TIME, AND NOT A COMPACTOR. The activity log has a raw+compacted tier
 * split (six raw tiers folded into a denylisted `digest` output tier, see
 * internal/database/nuts_activity_store.go) and it exists to reclaim SPACE
 * across millions of rows. Operations are nothing like that: 40 rows over 6
 * hours in prod. This is a readability problem, not a volume problem, so
 * nothing here is written down.
 *
 * That choice also dissolves a hazard rather than solving it. A PERSISTED
 * synthetic parent would sit non-terminal for up to 30 minutes, which is
 * exactly what resumeAfterStartup scans for — re-creating the fail-closed
 * problem the activity migration just solved with interrupted_dropped. Nothing
 * is written, so nothing scans it.
 *
 * TWO PASSES. The first folds each KIND on its own: consecutive same-kind runs,
 * split by an idle gap and a span cap. That alone left a six-hour AI-parse run
 * on the page as a chain of ×N rows with stray singletons between them — every
 * piece correct, the whole unreadable. The second pass (mergeAdjacentRows)
 * looks at the timeline the reader actually sees: any same-kind rows that end
 * up next to each other, whatever the gap between them, become one row. The gap
 * and span rules therefore only show through when something of another kind
 * ran in between — which is exactly when a split carries information. Because
 * nothing is persisted, both passes re-derive from scratch on every read, so
 * "already-merged groups drifting apart" cannot happen.
 *
 * And it belongs in the STORE rather than a request handler because the full
 * 24-hour window is already in memory here, so a group can never be truncated
 * across a pagination page boundary — and the bell gets the same grouping from
 * the same code.
 */

/** A new group starts when the gap to the previous member exceeds this. This is
 *  the "rolling idle timer that restarts on each new entry", expressed as a gap
 *  test over sorted rows so it needs no timer and no state. */
export const GROUP_IDLE_GAP_MS = 2 * 60 * 1000;

/** Hard cap on how long one group may span, however busy the run. */
export const GROUP_MAX_SPAN_MS = 30 * 60 * 1000;

/**
 * Fewer members than this and no group is synthesized.
 *
 * Not a tuning knob — it is a floor. A group renders a parent row plus its
 * children, so grouping 2 rows produces 3 where there were 2. At 3 the collapsed
 * form is strictly shorter than what it replaces, which is the entire point.
 */
export const MIN_GROUP_SIZE = 3;

/** Extra fields carried by a synthetic group row. Present ONLY on group rows,
 *  so `op.group !== undefined` is the test for "this is not a real operation":
 *  its id names no record on the server, and anything that would fetch, cancel
 *  or refresh by id must check this first. */
export interface OperationGroup {
  /** How many real operations this row stands for. */
  count: number;
  /** Member ids, oldest first. */
  memberIds: string[];
  /** Epoch ms of the first and last member. */
  firstAt?: number;
  lastAt?: number;
}

/** groupTimestamp is the single time an op is ordered by. finishedAt for
 *  anything that ended, startedAt otherwise (which fromV2 falls back to
 *  queued_at, so a queued op still has one). */
function groupTimestamp(op: ActiveOperation): number {
  return op.finishedAt ?? op.startedAt ?? 0;
}

/**
 * groupKey is what makes two runs the same KIND of thing.
 *
 * Status is in the key, and that is a deliberate addition to "same operation
 * type, consecutive in time". The Activity page partitions rows into sections
 * BY STATUS (Active / Pending / Completed / Failed / Canceled / Interrupted).
 * A group spanning two statuses would put the parent in one section and its
 * children in another, and collapsing it would remove a `failed` child from the
 * Failed section — the exact hazard ActivityLog.tsx warns about where it
 * deliberately does not collapse-filter the history sections. Keying on status
 * makes every group homogeneous, so a group always lands whole in one section,
 * and collapse-filtering those sections becomes safe.
 *
 * The cost is that a run of 12 that alternates completed/failed yields two
 * groups instead of one. That is the better answer anyway: it shows the split
 * instead of averaging it away.
 */
function groupKey(op: ActiveOperation): string {
  // \u0000 separator, written as an escape rather than a literal control byte:
  // def_id and status are both free-form server strings, and any printable
  // separator could appear inside one of them and collide two distinct keys.
  return `${op.def_id ?? op.type ?? ''}\u0000${op.status}`;
}

/**
 * byTimeThenId orders rows for the fold.
 *
 * The id tiebreak is load-bearing, not tidiness. Ops queued by one scan share a
 * timestamp to the millisecond, and the input order is Object.values() over a
 * map that is REBUILT on every poll — so without it the fold's member order,
 * and therefore the group id derived from the first member, changes every five
 * seconds and collapsedParents thrashes.
 */
function byTimeThenId(a: ActiveOperation, b: ActiveOperation): number {
  const at = groupTimestamp(a);
  const bt = groupTimestamp(b);
  if (at !== bt) return at - bt;
  return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
}

/**
 * groupOperations folds runs of consecutive same-kind operations into synthetic
 * parent rows.
 *
 * Returns a NEW array: parents, their children (cloned with parent_id set), and
 * every ungrouped op unchanged. The input is not mutated — `activeOperations`
 * is read by Library.tsx, OpenLibraryDumps, ITunesImport and
 * OperationActivityPanel, none of which should ever see a synthetic op.
 *
 * Pure: same input, same output, including the ids.
 */
export function groupOperations(ops: ActiveOperation[]): ActiveOperation[] {
  const byKey = new Map<string, ActiveOperation[]>();
  const out: ActiveOperation[] = [];
  for (const op of ops) {
    // An op that already declares a parent is left exactly as it is. Real
    // lineage outranks inferred timing: a server-declared parent means "this
    // op spawned that one", where a group only ever means "these ran back to
    // back". Re-parenting such a row into a group would silently destroy the
    // stronger claim, and detach it from a parent that is still on the page.
    //
    // Nothing in production sets parent_id today (registry.WithParent has no
    // callers), so this costs nothing now — it is here so that wiring lineage
    // up later is not a change that quietly breaks this function.
    if (op.parent_id) {
      out.push(op);
      continue;
    }
    const key = groupKey(op);
    const bucket = byKey.get(key);
    if (bucket) bucket.push(op);
    else byKey.set(key, [op]);
  }

  for (const bucket of byKey.values()) {
    if (bucket.length < MIN_GROUP_SIZE) {
      out.push(...bucket);
      continue;
    }
    const sorted = [...bucket].sort(byTimeThenId);

    // The fold. One pass, no timers, no carried state beyond the run being
    // accumulated.
    let run: ActiveOperation[] = [];
    const flush = () => {
      if (run.length >= MIN_GROUP_SIZE) {
        out.push(makeGroupParent(run), ...run.map((op) => ({ ...op, parent_id: groupId(run) })));
      } else {
        out.push(...run);
      }
      run = [];
    };
    for (const op of sorted) {
      if (run.length > 0) {
        const gap = groupTimestamp(op) - groupTimestamp(run[run.length - 1]);
        const span = groupTimestamp(op) - groupTimestamp(run[0]);
        if (gap > GROUP_IDLE_GAP_MS || span > GROUP_MAX_SPAN_MS) flush();
      }
      run.push(op);
    }
    flush();
  }
  return mergeAdjacentRows(out);
}

/**
 * mergeAdjacentRows is the second pass: same-kind top-level rows that sit next
 * to each other in the timeline become one group.
 *
 * "Next to each other" is decided on the rows the first pass produced — group
 * parents and ungrouped ops alike, ordered by the same timestamp the fold uses —
 * so a group's position is its last member's time and a row of another kind
 * anywhere between two same-kind rows keeps them apart. A run of adjacent rows
 * merges only when it would replace at least two rows AND the merged group
 * clears MIN_GROUP_SIZE; two lone singletons stay two rows for the same reason
 * the first pass leaves them alone.
 *
 * The merged group's id comes from groupId over its (re-sorted) members, so it
 * equals the id of the earliest piece: a group that absorbs a later neighbour
 * keeps the id the user may already have collapsed. Rows that declare a real
 * (server) parent were never top-level and pass through untouched.
 */
function mergeAdjacentRows(rows: ActiveOperation[]): ActiveOperation[] {
  const groupIds = new Set(rows.filter((r) => r.group).map((r) => r.id));
  const childrenOf = new Map<string, ActiveOperation[]>();
  const passthrough: ActiveOperation[] = [];
  const top: ActiveOperation[] = [];
  for (const row of rows) {
    if (row.parent_id && groupIds.has(row.parent_id)) {
      const bucket = childrenOf.get(row.parent_id);
      if (bucket) bucket.push(row);
      else childrenOf.set(row.parent_id, [row]);
    } else if (row.parent_id) {
      passthrough.push(row);
    } else {
      top.push(row);
    }
  }
  top.sort(byTimeThenId);

  const out: ActiveOperation[] = [...passthrough];
  const membersOf = (row: ActiveOperation): ActiveOperation[] =>
    row.group ? (childrenOf.get(row.id) ?? []) : [row];
  let run: ActiveOperation[] = [];
  const flush = () => {
    if (run.length === 0) return;
    const members = run.flatMap(membersOf);
    if (run.length >= 2 && members.length >= MIN_GROUP_SIZE) {
      // Members are the first pass's clones (or untouched singletons); sorting
      // a fresh array and re-cloning with the new parent leaves the input alone.
      const sorted = [...members].sort(byTimeThenId);
      const id = groupId(sorted);
      out.push(makeGroupParent(sorted), ...sorted.map((op) => ({ ...op, parent_id: id })));
    } else {
      for (const row of run) {
        out.push(row);
        if (row.group) out.push(...(childrenOf.get(row.id) ?? []));
      }
    }
    run = [];
  };
  for (const row of top) {
    if (run.length > 0 && groupKey(row) !== groupKey(run[run.length - 1])) flush();
    run.push(row);
  }
  flush();
  return out;
}

/**
 * groupId is derived from the group's content, never minted.
 *
 * A fresh ULID per poll would make collapsedParents useless: every 5-second
 * refresh would produce new ids, the collapsed set would never match, and every
 * group would silently re-expand. Keyed on the FIRST member rather than a
 * quantized time bucket so there are no boundary cases — a group that gains a
 * newer member keeps its id.
 */
function groupId(members: ActiveOperation[]): string {
  const head = members[0];
  return `group:${head.def_id ?? head.type ?? ''}:${head.status}:${head.id}`;
}

/** makeGroupParent builds the synthetic row that stands for a run.
 *
 *  Its status is the members' status — they are homogeneous by construction (see
 *  groupKey) — so the existing section partitioning and status chip work on it
 *  unchanged, and isTerminal() gives the right answer. Progress is summed, which
 *  is what makes the parent's bar mean something: 0 / 60 across 12 failed runs. */
function makeGroupParent(members: ActiveOperation[]): ActiveOperation {
  const head = members[0];
  const tail = members[members.length - 1];
  const progress = members.reduce((n, op) => n + (op.progress || 0), 0);
  const total = members.reduce((n, op) => n + (op.total || 0), 0);
  return {
    id: groupId(members),
    def_id: head.def_id,
    plugin: head.plugin,
    type: head.type,
    displayName: head.displayName,
    status: head.status,
    progress,
    total,
    // The count is a chip on the row, so this line spends itself on something
    // the count cannot say: what the runs actually reported. The most recent
    // member, labelled as a sample rather than presented as the group's own
    // message — the members' messages are usually identical (which is why the
    // rows were unreadable in the first place) but nothing guarantees it.
    message: tail.message ? `Most recent: ${tail.message}` : `${members.length} runs`,
    startedAt: groupTimestamp(head),
    finishedAt: tail.finishedAt !== undefined ? groupTimestamp(tail) : undefined,
    // notify_level 1 = activity-only. A synthetic row must never reach the bell
    // badge, which counts real work from alertOperations.
    notify_level: 1,
    parent_id: null,
    group: {
      count: members.length,
      memberIds: members.map((op) => op.id),
      firstAt: groupTimestamp(head),
      lastAt: groupTimestamp(tail),
    },
  };
}
