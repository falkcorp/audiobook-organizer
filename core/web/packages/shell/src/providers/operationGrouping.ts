// file: core/web/packages/shell/src/providers/operationGrouping.ts
// version: 0.1.0
// guid: 254ccbfb-1e37-4812-bbc2-43742cd3ec9e
// last-edited: 2026-11-21

// Read-time grouping of consecutive same-kind operations (07 §7.11: the shell's
// Activity page and operations indicator). Ported from ABO's
// web/src/stores/operationGrouping.ts and typed over the structural
// GroupableOperation, so a host's richer row type (ABO's ActiveOperation, the
// shell's OperationRow) goes in and comes out unchanged.
//
// WHY READ-TIME, AND NOT PERSISTED. Operations are a readability problem, not a
// volume problem: a day of runs is dozens of rows. Nothing is written, so there
// is no synthetic record for a startup scan to mistake for an unfinished run;
// every read re-derives the groups from scratch, so "already-merged groups
// drifting apart" cannot happen.
//
// TWO PASSES. The first folds each KIND on its own: consecutive same-kind runs,
// split by an idle gap and a span cap. That alone left a six-hour run on the
// page as a chain of xN rows with stray singletons between them. The second
// pass (mergeAdjacentRows) looks at the timeline the reader actually sees: any
// same-kind rows that end up next to each other, whatever the gap between them,
// become one row. The gap and span rules therefore only show through when
// something of another kind ran in between, which is exactly when a split
// carries information.
//
// The grouping belongs in the store layer, not a request handler, because the
// whole window is already in memory there, so a group is never truncated across
// a pagination page boundary, and the bell gets the same grouping from the same
// code.

/**
 * The fields grouping reads, and the ones a synthetic group row sets. `def_id`
 * (falling back to `type`) names the kind; `status` is the run's state.
 *
 * A row type passed to groupOperations may carry more fields, but every extra
 * field must be optional: the synthetic parent is built from this interface
 * alone and handed back as the caller's type.
 */
export interface GroupableOperation {
  id: string;
  def_id?: string;
  plugin?: string;
  /** Legacy short kind code; grouping falls back to it when `def_id` is absent. */
  type?: string;
  displayName?: string;
  status: string;
  progress: number;
  total: number;
  message: string;
  /** Epoch ms. */
  startedAt?: number;
  /** Epoch ms, set only for a run that has ended. */
  finishedAt?: number;
  parent_id?: string | null;
  /** 0 = alert (shows in the bell badge), 1 = activity-only. */
  notify_level?: number;
  /**
   * Set ONLY on the synthetic rows groupOperations builds. Its presence means
   * this row stands for several real operations and its id names no record on
   * the server: anything that fetches, cancels or refreshes by id must check it
   * first. Absent on every operation that came from the API.
   */
  group?: OperationGroup;
}

/** Extra fields carried by a synthetic group row. */
export interface OperationGroup {
  /** How many real operations this row stands for. */
  count: number;
  /** Member ids, oldest first. */
  memberIds: string[];
  /** Epoch ms of the first and last member. */
  firstAt?: number;
  lastAt?: number;
}

/**
 * A new group starts when the gap to the previous member exceeds this: a
 * rolling idle timer that restarts on each new entry, expressed as a gap test
 * over sorted rows so it needs no timer and no state.
 */
export const GROUP_IDLE_GAP_MS = 2 * 60 * 1000;

/** Hard cap on how long one group may span, however busy the run. */
export const GROUP_MAX_SPAN_MS = 30 * 60 * 1000;

/**
 * Fewer members than this and no group is synthesized. Not a tuning knob, a
 * floor: a group renders a parent row plus its children, so grouping 2 rows
 * produces 3 where there were 2. At 3 the collapsed form is strictly shorter
 * than what it replaces.
 */
export const MIN_GROUP_SIZE = 3;

/**
 * The single time a row is ordered by: finishedAt for anything that ended,
 * startedAt otherwise (an adapter falls back to the creation time, so a queued
 * run still has one). Exported so the bell orders rows by the same clock the
 * fold does. A synthetic group parent carries the timestamps makeGroupParent
 * gave it (head's start, tail's finish), so it sorts among real rows without a
 * special case.
 */
export function groupTimestamp(op: GroupableOperation): number {
  return op.finishedAt ?? op.startedAt ?? 0;
}

/**
 * Orders rendered rows newest-first.
 *
 * groupOperations cannot do this for its callers: it emits bucket by bucket
 * (all of one kind+status, then all of the next), so its output is ordered by
 * kind, never by time, whatever order the input arrived in. The list that is
 * rendered is the only place the order can be fixed.
 *
 * The id tiebreak is load-bearing: runs queued together share a timestamp to
 * the millisecond, and the underlying map is rebuilt on every refresh, so
 * without a total order the rows reshuffle under the cursor. Ids are ULIDs, so
 * descending id is descending creation time.
 */
export function byNewestFirst(a: GroupableOperation, b: GroupableOperation): number {
  const at = groupTimestamp(a);
  const bt = groupTimestamp(b);
  if (at !== bt) return bt - at;
  return a.id < b.id ? 1 : a.id > b.id ? -1 : 0;
}

/**
 * What makes two runs the same KIND of thing.
 *
 * Status is in the key, a deliberate addition to "same kind, consecutive in
 * time". The Activity page partitions rows into sections BY STATUS. A group
 * spanning two statuses would put the parent in one section and its children in
 * another, and collapsing it would remove a failed child from the Failed
 * section. Keying on status makes every group homogeneous, so a group always
 * lands whole in one section.
 *
 * The cost is that a run of 12 that alternates completed/failed yields two
 * groups instead of one. That is the better answer anyway: it shows the split
 * instead of averaging it away.
 */
function groupKey(op: GroupableOperation): string {
  // \u0000 separator, written as an escape rather than a literal control byte:
  // kind and status are both free-form server strings, and any printable
  // separator could appear inside one of them and collide two distinct keys.
  return `${op.def_id ?? op.type ?? ''}\u0000${op.status}`;
}

/**
 * Orders rows for the fold. The id tiebreak is load-bearing, not tidiness: runs
 * queued together share a timestamp to the millisecond and the input order is a
 * map rebuilt on every refresh, so without it the fold's member order, and
 * therefore the group id derived from the first member, changes on every
 * refresh and collapsed groups thrash.
 */
function byTimeThenId(a: GroupableOperation, b: GroupableOperation): number {
  const at = groupTimestamp(a);
  const bt = groupTimestamp(b);
  if (at !== bt) return at - bt;
  return a.id < b.id ? -1 : a.id > b.id ? 1 : 0;
}

/**
 * Folds runs of consecutive same-kind operations into synthetic parent rows.
 *
 * Returns a NEW array: parents, their children (cloned with parent_id set), and
 * every ungrouped op unchanged. The input is not mutated, so the flat list a
 * caller keeps for lookups by id never sees a synthetic row.
 *
 * Pure: same input, same output, including the ids.
 */
export function groupOperations<T extends GroupableOperation>(ops: readonly T[]): T[] {
  const byKey = new Map<string, T[]>();
  const out: T[] = [];
  for (const op of ops) {
    // An op that already declares a parent is left exactly as it is. Real
    // lineage outranks inferred timing: a declared parent means "this op
    // spawned that one", where a group only ever means "these ran back to
    // back". Re-parenting such a row into a group would silently destroy the
    // stronger claim, and detach it from a parent that is still on the page.
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
    let run: T[] = [];
    const flush = () => {
      if (run.length >= MIN_GROUP_SIZE) {
        const id = groupId(run);
        out.push(makeGroupParent(run), ...run.map((op) => ({ ...op, parent_id: id })));
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
 * The second pass: same-kind top-level rows that sit next to each other in the
 * timeline become one group.
 *
 * "Next to each other" is decided on the rows the first pass produced (group
 * parents and ungrouped ops alike), ordered by the same timestamp the fold
 * uses, so a group's position is its last member's time and a row of another
 * kind anywhere between two same-kind rows keeps them apart. A run of adjacent
 * rows merges only when it would replace at least two rows AND the merged group
 * clears MIN_GROUP_SIZE; two lone singletons stay two rows for the same reason
 * the first pass leaves them alone.
 *
 * The merged group's id comes from groupId over its (re-sorted) members, so it
 * equals the id of the earliest piece: a group that absorbs a later neighbour
 * keeps the id the user may already have collapsed. Rows that declare a real
 * parent were never top-level and pass through untouched.
 */
function mergeAdjacentRows<T extends GroupableOperation>(rows: T[]): T[] {
  const groupIds = new Set(rows.filter((r) => r.group).map((r) => r.id));
  const childrenOf = new Map<string, T[]>();
  const passthrough: T[] = [];
  const top: T[] = [];
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

  const out: T[] = [...passthrough];
  const membersOf = (row: T): T[] => (row.group ? (childrenOf.get(row.id) ?? []) : [row]);
  let run: T[] = [];
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
 * A group's id is derived from its content, never minted. A fresh id per
 * refresh would make a collapsed-groups set useless: every refresh would
 * produce new ids, the set would never match, and every group would silently
 * re-expand. Keyed on the FIRST member rather than a quantized time bucket so
 * there are no boundary cases: a group that gains a newer member keeps its id.
 */
function groupId(members: readonly GroupableOperation[]): string {
  const head = members[0];
  return `group:${head.def_id ?? head.type ?? ''}:${head.status}:${head.id}`;
}

/**
 * The synthetic row that stands for a run.
 *
 * Its status is the members' status (they are homogeneous by construction, see
 * groupKey), so section partitioning and the status chip work on it unchanged.
 * Progress is summed, which is what makes the parent's bar mean something.
 *
 * Built from GroupableOperation alone; the cast to T is sound only because
 * groupOperations' contract makes every other field of T optional.
 */
function makeGroupParent<T extends GroupableOperation>(members: readonly T[]): T {
  const head = members[0];
  const tail = members[members.length - 1];
  const progress = members.reduce((n, op) => n + (op.progress || 0), 0);
  const total = members.reduce((n, op) => n + (op.total || 0), 0);
  const parent: GroupableOperation = {
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
    // message.
    message: tail.message ? `Most recent: ${tail.message}` : `${members.length} runs`,
    startedAt: groupTimestamp(head),
    finishedAt: tail.finishedAt !== undefined ? groupTimestamp(tail) : undefined,
    // A synthetic row must never reach the bell badge, which counts real work.
    notify_level: 1,
    parent_id: null,
    group: {
      count: members.length,
      memberIds: members.map((op) => op.id),
      firstAt: groupTimestamp(head),
      lastAt: groupTimestamp(tail),
    },
  };
  return parent as T;
}
