// file: web/src/stores/operationGrouping.ts
// version: 1.1.0
// guid: 8c4a1f37-2b95-4e60-9d13-6a7fb2e08c54
// last-edited: 2026-09-08

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
 * is written, so nothing scans it. It also makes "two already-merged groups end
 * up adjacent" impossible: that is an artifact of persisting a merge, and this
 * re-derives from scratch on every read.
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
