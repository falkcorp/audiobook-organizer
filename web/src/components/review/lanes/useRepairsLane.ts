// file: web/src/components/review/lanes/useRepairsLane.ts
// version: 1.11.0
// guid: 7b1e5c28-3a94-4d6f-8e02-c5f9a1d7b340
// last-edited: 2026-10-18

/**
 * The repairs lane's data layer: fixers, their trials (plans), plan rows, and
 * applies.
 *
 * Reads, writes and polls go through TanStack Query (keys in lib/queryClient),
 * gated on `active`: an inactive lane makes no requests and polls nothing.
 *
 * ---------------------------------------------------------------------------
 * WHICH PLAN THE TABLE SHOWS
 * ---------------------------------------------------------------------------
 *
 * The server records only each fixer's NEWEST plan (`last_plan`), overwritten
 * when a trial is enqueued. So a trial that is running, or that failed, hides
 * the previous completed plan from GET /repairs. The lane therefore keeps the
 * newest COMPLETED plan it has seen per fixer (`knownPlans`) and shows that,
 * with the running or failed trial reported beside it rather than in its place.
 * Rows a reviewer was reading do not vanish because they pressed "Run trial".
 * (After a reload the older plan is gone until the server keeps it too.)
 *
 * ---------------------------------------------------------------------------
 * APPLY ALWAYS WRITES, AND ASKS ONCE
 * ---------------------------------------------------------------------------
 *
 * Both apply gestures go through `dispatch`, and `dispatch` confirms, so every
 * entry point asks exactly once with the row count (the metadata lane's Replace
 * confirm, same shape). The request always carries `dry_run: false` -- see
 * startRepairApply -- because the server treats an omitted flag as a preview.
 */

import { useCallback, useMemo, useState } from 'react';
import {
  useMutation,
  useQueries,
  useQuery,
  useQueryClient,
  type QueryClient,
  type UseQueryResult,
} from '@tanstack/react-query';
import { queryKeys } from '../../../lib/queryClient';
import * as api from '../../../services/api';
import type {
  OperationV2,
  RepairApplyResult,
  RepairOpStarted,
  RepairFixer,
  RepairRow,
  RepairRowResult,
  RepairRowsFilter,
  RepairRowsPage,
} from '../../../services/api';
import type { RepairsAction } from '../reviewActions';

type Toast = (message: string, severity?: 'success' | 'error' | 'warning' | 'info') => void;

/** Page sizes offered under the rows table. The server caps a page at 500. */
export const REPAIRS_PAGE_SIZES = [25, 50, 100, 250, 500] as const;
export const REPAIRS_DEFAULT_PAGE_SIZE = 50;

/** How often a running trial or apply is polled. */
export const REPAIRS_POLL_INTERVAL_MS = 1500;

/** Per-viewer conveniences only: which fixer was open, and the page size. */
export const REPAIRS_FIXER_STORAGE_KEY = 'review-repairs-fixer';
export const REPAIRS_PAGE_SIZE_STORAGE_KEY = 'review-repairs-page-size';

function readStored(key: string): string | null {
  try {
    return window.localStorage.getItem(key);
  } catch {
    return null;
  }
}

function writeStored(key: string, value: string): void {
  try {
    window.localStorage.setItem(key, value);
  } catch {
    // Storage blocked (private mode, sandboxed frame): the preference is lost,
    // nothing else is.
  }
}

function initialPageSize(): number {
  const n = Number(readStored(REPAIRS_PAGE_SIZE_STORAGE_KEY));
  return (REPAIRS_PAGE_SIZES as readonly number[]).includes(n) ? n : REPAIRS_DEFAULT_PAGE_SIZE;
}

function errorMessage(err: unknown, fallback: string): string {
  return err instanceof Error && err.message ? err.message : fallback;
}

/** Statuses of a plan that has not finished. */
function isInFlight(status: string | undefined): boolean {
  return status === 'queued' || status === 'running' || status === 'pending';
}

function trialFailure(op: OperationV2): string {
  return op.error_message || `The trial ended with status "${op.status}".`;
}

/**
 * The confirm every apply shows, whichever button sent it. Names the count and
 * says what happens to a running scan -- it pauses briefly; nothing is blocked.
 */
export function repairApplyConfirmMessage(count: number, fixerTitle: string): string {
  return (
    `Apply ${count.toLocaleString()} repair row(s) from "${fixerTitle}"? ` +
    'This writes to the library, and each change is recorded in the book history. ' +
    'If a library scan is running it pauses briefly while the rows are written.'
  );
}

/** One line summarising an apply result, for the toast and the result banner. */
export function summarizeApply(r: RepairApplyResult): string {
  const parts = [
    `${r.dry_run ? 'Would apply' : 'Applied'} ${r.dry_run ? (r.by_outcome.would_apply ?? 0) : r.applied}`,
    `changed since trial ${r.changed_since_plan}`,
    `failed ${r.failed}`,
  ];
  if (r.partially_applied > 0) parts.push(`partly applied ${r.partially_applied}`);
  const retry = r.retry_later ?? r.by_outcome.retry_later ?? 0;
  if (retry > 0) parts.push(`retry later ${retry}`);
  const guarded = r.by_outcome.skipped_guard ?? 0;
  if (guarded > 0) parts.push(`skipped by guard ${guarded}`);
  const ownerRefused = r.by_outcome.owner_apply_refused ?? 0;
  if (ownerRefused > 0) parts.push(`owner apply refused ${ownerRefused}`);
  const canceled = r.by_outcome.canceled_skipped ?? 0;
  if (canceled > 0) parts.push(`canceled before finishing ${canceled}`);
  if (r.not_in_plan && r.not_in_plan.length > 0) parts.push(`not in trial ${r.not_in_plan.length}`);
  let line = parts.join(' · ');
  if (r.dry_run) line = `Preview only, nothing was written: ${line}`;
  if (r.aborted) line += ` · stopped early: ${r.aborted}`;
  return line;
}

/**
 * The severity of an apply result, for the toast and the result banner alike:
 * warning when anything was not written (failed, partly applied, retry later,
 * owner apply refused, canceled before the row, stopped early) or when it was
 * only a preview. A canceled apply does not set `aborted` (that is the lost
 * stand-down lease), so canceled_skipped is checked on its own.
 */
export function applySeverity(r: RepairApplyResult): 'success' | 'warning' {
  return r.failed > 0 ||
    r.partially_applied > 0 ||
    (r.retry_later ?? 0) > 0 ||
    (r.by_outcome.owner_apply_refused ?? 0) > 0 ||
    (r.by_outcome.canceled_skipped ?? 0) > 0 ||
    r.aborted ||
    r.dry_run
    ? 'warning'
    : 'success';
}

/**
 * The owner-apply confirm: names the book and how many fragment books the
 * one click retires, and says plainly that iTunes tracks those files, only
 * database rows change, and the parent is not written.
 */
export function repairOwnerApplyConfirmMessage(
  title: string,
  fixerTitle: string,
  fragments: number
): string {
  const what = fragments === 1 ? '1 fragment book' : `${fragments} fragment books`;
  return (
    `Apply "${title}" from "${fixerTitle}" as the owner, retiring ${what} into the parent? ` +
    'iTunes tracks these files; only database rows change. ' +
    'The parent, every file and iTunes are not written. The change is recorded in the ' +
    "operation's history under your name and can be reverted from the operation."
  );
}

/** The newest completed plan the lane has seen for a fixer. */
interface KnownPlan {
  opId: string;
  completedAt: string;
}

/**
 * Records `plan` unless the lane already holds a NEWER completed plan for the
 * fixer. A fixer list fetched before a trial finished (or one whose last-run
 * pointer the server failed to record) must not take the table back to an
 * older plan.
 */
function withNewerPlan(
  prev: Record<string, KnownPlan>,
  fixerId: string,
  plan: KnownPlan
): Record<string, KnownPlan> {
  const cur = prev[fixerId];
  if (cur?.opId === plan.opId) return prev;
  if (cur && Date.parse(cur.completedAt) > Date.parse(plan.completedAt)) return prev;
  return { ...prev, [fixerId]: plan };
}

/**
 * Outcomes after which a row is not re-sent from the same plan: it was written
 * (in full or in part), or the server refused it as changed or not applicable
 * and will again until a new trial. Failed, aborted, retry-later and
 * guard-skipped rows stay selectable: a retry can succeed.
 */
export const SETTLED_OUTCOMES: ReadonlySet<string> = new Set([
  'applied',
  'partially_applied',
  'changed_since_plan',
  'not_applicable',
]);

export interface RepairApplyProblem {
  severity: 'error' | 'warning';
  message: string;
}

/** A trial the lane is following (or that ended without a usable plan). */
export interface RepairTrialState {
  opId: string;
  phase: 'running' | 'failed';
  message?: string;
}

export interface RepairsLane {
  active: boolean;

  fixers: RepairFixer[];
  fixersLoading: boolean;
  /** GET /repairs failed. Distinct from an empty list (no fixers registered). */
  fixersError: string | null;
  reloadFixers: () => void;
  /**
   * Whether this sign-in may apply owner rows (GET /repairs/owner-status), or
   * null while unknown / when the read failed (the server still decides).
   */
  ownerStatus: api.RepairOwnerStatus | null;

  selectedFixerId: string | null;
  selectedFixer: RepairFixer | null;
  selectFixer: (id: string) => void;

  /** The completed plan whose rows are shown, or null when there is none yet. */
  planOpId: string | null;
  /** The selected fixer's running or failed trial, if any. */
  trial: RepairTrialState | null;
  /** Trials by fixer id, for the rail. */
  trials: Record<string, RepairTrialState>;

  filter: RepairRowsFilter;
  setFilter: (f: RepairRowsFilter) => void;
  /** Row class the page is narrowed to (fixers that classify rows), or null. */
  rowClass: string | null;
  setRowClass: (c: string | null) => void;
  offset: number;
  pageSize: number;
  setOffset: (offset: number) => void;
  setPageSize: (n: number) => void;

  /** The loaded page, only when it belongs to the plan and filter on screen. */
  page: RepairRowsPage | null;
  rows: RepairRow[];
  rowsLoading: boolean;
  rowsError: string | null;
  reloadRows: () => void;

  selectedRowIds: ReadonlySet<string>;
  toggleRow: (rowId: string) => void;
  /** Selects every applicable row on the loaded page. */
  selectPage: () => void;
  /**
   * Ticks (`on`) or unticks the given rows, leaving every other row as it is.
   * Skipped and already-settled rows are never ticked.
   */
  selectRows: (rowIds: readonly string[], on: boolean) => void;
  clearSelection: () => void;

  /** An apply is in flight for the selected fixer. */
  applying: boolean;
  /** The last apply went wrong (error) or out of sight (warning). */
  applyError: RepairApplyProblem | null;
  /** Rows of the plan on screen that an apply already settled; not re-sent. */
  settledRowIds: ReadonlySet<string>;
  /** The plan's applicable count less the settled rows (null: no page yet). */
  remainingApplicable: number | null;
  /** The newest apply result for the plan on screen. */
  applyResult: RepairApplyResult | null;
  /** Every outcome reported for the plan on screen, across applies. */
  rowOutcomes: ReadonlyMap<string, RepairRowResult>;

  dispatch: (action: RepairsAction) => void;
}

/** The fixer list, and the newest completed plan seen per fixer across fetches. */
interface FixerList {
  list: RepairFixer[];
  plans: Record<string, KnownPlan>;
}

const NO_FIXERS: FixerList = { list: [], plans: {} };

/** Reads the fixer list, keeping each plan the cache already holds unless the list has a newer one. */
async function loadFixers(qc: QueryClient, signal: AbortSignal): Promise<FixerList> {
  const list = await api.listRepairFixers({ signal });
  let plans = qc.getQueryData<FixerList>(queryKeys.repairs.fixers())?.plans ?? {};
  for (const f of list) {
    const lp = f.last_plan;
    if (lp?.status === 'completed') {
      plans = withNewerPlan(plans, f.id, {
        opId: lp.operation_id,
        completedAt: lp.completed_at ?? lp.queued_at,
      });
    } else if (lp && isInFlight(lp.status)) {
      // Still running after its poll lost track: follow it again.
      const key = queryKeys.ops.v2(lp.operation_id);
      if (qc.getQueryState(key)?.status === 'error') void qc.resetQueries({ queryKey: key });
    }
  }
  return { list, plans };
}

/**
 * Follows an operation to its end, calling `onEnd` once when it gets there. An
 * ended op never changes, so a refetch (an event-stream invalidation) returns
 * the cached end. A poll that loses track calls `onLost`; one cut off because
 * the lane went inactive or unmounted (no observer left) calls neither.
 */
function followOp(
  qc: QueryClient,
  opId: string,
  onEnd: (op: OperationV2) => void,
  onLost: (err: unknown) => void
) {
  const queryKey = queryKeys.ops.v2(opId);
  return {
    queryKey,
    retry: false,
    queryFn: async ({ signal }: { signal: AbortSignal }) => {
      const ended = qc.getQueryData<OperationV2>(queryKey);
      if (ended) return ended;
      const op = await api
        .pollOperationV2(opId, undefined, REPAIRS_POLL_INTERVAL_MS, {
          signal,
          requestTimeoutMs: api.REPAIRS_FETCH_TIMEOUT_MS,
        })
        .catch((err: unknown) => {
          if (!signal.aborted) onLost(err);
          throw err;
        });
      onEnd(op);
      return op;
    },
  };
}

/** What a followed trial's poll says about it: running, failed, or (null) done. */
function trialState(opId: string, q: UseQueryResult<OperationV2>): RepairTrialState | null {
  if (q.data?.status === 'completed') return null;
  if (q.data) return { opId, phase: 'failed', message: trialFailure(q.data) };
  if (q.error) {
    const message = `Lost track of the trial: ${errorMessage(q.error, 'unknown error')}`;
    return { opId, phase: 'failed', message };
  }
  return { opId, phase: 'running' };
}

interface ApplyRequest {
  fixerId: string;
  plan: string;
  resolveIds: () => Promise<string[]>;
  start?: (fixerId: string, plan: string, rowIds: string[]) => Promise<RepairOpStarted>;
}

/**
 * `seedFixerId` is the URL's `?fixer=<id>`: a seed, read once on the first
 * render and never mirrored back to the URL or into the per-viewer storage key
 * (only a reviewer's own click does that). It is preferred over the remembered
 * fixer, and only if the server lists that id; an unknown id is ignored,
 * leaving the viewer's remembered fixer (or the first one listed).
 */
export function useRepairsLane(
  toast: Toast,
  active = true,
  seedFixerId: string | null = null
): RepairsLane {
  const qc = useQueryClient();
  const [initialFixer] = useState(() => [seedFixerId, readStored(REPAIRS_FIXER_STORAGE_KEY)]);
  const [pickedFixerId, setPickedFixerId] = useState<string | null>(null);
  // Trials started from this lane, by fixer: followed until the server lists them.
  const [startedTrials, setStartedTrials] = useState<Record<string, string>>({});

  const [filter, setFilterState] = useState<RepairRowsFilter>('applicable');
  const [rowClass, setRowClassState] = useState<string | null>(null);
  const [offset, setOffset] = useState(0);
  const [pageSize, setPageSizeState] = useState<number>(initialPageSize);

  const [selectedRowIds, setSelectedRowIds] = useState<Set<string>>(() => new Set());
  // The apply being followed, by fixer (its op id).
  const [applyOps, setApplyOps] = useState<Record<string, string>>({});
  const [applyErrors, setApplyErrors] = useState<Record<string, RepairApplyProblem>>({});
  const [applyResults, setApplyResults] = useState<Record<string, RepairApplyResult>>({});
  // Every row outcome reported for a plan, merged across applies, so a second
  // apply does not wipe the first one's badges.
  const [outcomesByPlan, setOutcomesByPlan] = useState<
    Record<string, Record<string, RepairRowResult>>
  >({});

  // ---- fixer list and owner status ----------------------------------------
  const fixersQuery = useQuery({
    queryKey: queryKeys.repairs.fixers(),
    queryFn: ({ signal }) => loadFixers(qc, signal),
    enabled: active,
  });
  const { list: fixers, plans: knownPlans } = fixersQuery.data ?? NO_FIXERS;
  // Read with the fixer list. A failure leaves it unknown: the button stays,
  // and the server's 403 names the reason if it refuses.
  const ownerQuery = useQuery({
    queryKey: queryKeys.repairs.ownerStatus(),
    queryFn: async ({ signal }) => (await api.getRepairOwnerStatus?.({ signal })) ?? null,
    enabled: active,
  });
  // Reloads the owner status too: its key is under the list's.
  const reloadFixers = useCallback(
    () => void qc.invalidateQueries({ queryKey: queryKeys.repairs.fixers() }),
    [qc]
  );

  const selectedFixerId =
    [pickedFixerId, ...initialFixer].find((id) => fixers.some((f) => f.id === id)) ??
    fixers[0]?.id ??
    null;
  const selectedFixer = useMemo(
    () => fixers.find((f) => f.id === selectedFixerId) ?? null,
    [fixers, selectedFixerId]
  );
  const planOpId = selectedFixerId ? (knownPlans[selectedFixerId]?.opId ?? null) : null;

  // Offset and selection belong to one plan of one fixer. Reset them during
  // render when that changes (React's derived-state pattern), not in an effect:
  // an effect would let the rows fetch fire once at the stale offset first.
  const viewKey = `${selectedFixerId ?? ''}|${planOpId ?? ''}`;
  const [seenViewKey, setSeenViewKey] = useState(viewKey);
  if (seenViewKey !== viewKey) {
    setSeenViewKey(viewKey);
    setOffset(0);
    setSelectedRowIds(new Set());
  }

  // ---- following trials ---------------------------------------------------
  // Each fixer's trial the server reports as still running (one started from
  // another tab, or before a reload), else the one started here. Polled only
  // while the lane is active: an unobserved poll is aborted, and resumes when
  // the lane is active again.
  const followedTrials = active
    ? fixers.flatMap((f) => {
        const lp = f.last_plan;
        const opId = lp && isInFlight(lp.status) ? lp.operation_id : startedTrials[f.id];
        const listedDone = lp?.operation_id === opId && lp?.status === 'completed';
        return opId && !listedDone ? [{ fixerId: f.id, opId }] : [];
      })
    : [];
  const endTrial = (fixerId: string, opId: string, op: OperationV2) => {
    if (op.status !== 'completed') return toast(`Trial failed: ${trialFailure(op)}`, 'error');
    qc.setQueryData<FixerList>(queryKeys.repairs.fixers(), (d) => {
      const plan = { opId, completedAt: op.completed_at ?? new Date().toISOString() };
      return d && { ...d, plans: withNewerPlan(d.plans, fixerId, plan) };
    });
    toast('Trial finished. Its rows are ready to review.', 'success');
    reloadFixers();
  };
  const trials = useQueries({
    queries: followedTrials.map(({ fixerId, opId }) =>
      // If a lost trial is still running, the reload follows it again.
      followOp(qc, opId, (op) => endTrial(fixerId, opId, op), reloadFixers)
    ),
    combine: (results) => {
      const out: Record<string, RepairTrialState> = {};
      results.forEach((q, i) => {
        const t = trialState(followedTrials[i].opId, q);
        if (t) out[followedTrials[i].fixerId] = t;
      });
      return out;
    },
  });

  // The class of every row this session has read, keyed "<plan>|<row id>":
  // settled outcomes carry no class, and under a selected class the "apply
  // all" count subtracts only that class's settled rows. Every settled row
  // was read first (a page on screen, or collectApplicableIds), so it is here.
  const [rowClasses, setRowClasses] = useState<Readonly<Record<string, string>>>({});
  const noteRowClasses = useCallback((plan: string, rows: readonly RepairRow[]) => {
    setRowClasses((prev) => {
      let next: Record<string, string> | null = null;
      for (const r of rows) {
        const key = `${plan}|${r.row_id}`;
        if (r.class && prev[key] !== r.class) {
          next ??= { ...prev };
          next[key] = r.class;
        }
      }
      return next ?? prev;
    });
  }, []);

  // ---- rows ---------------------------------------------------------------
  const rowsParams = { filter, offset, limit: pageSize, rowClass: rowClass ?? undefined };
  const isOnScreen = (p: RepairRowsPage | undefined) =>
    p?.fixer_id === selectedFixerId &&
    p?.plan_op_id === planOpId &&
    (p?.filter ?? '') === filter &&
    (p?.class ?? '') === (rowClass ?? '');
  const rowsQuery = useQuery({
    queryKey: queryKeys.repairs.rows(selectedFixerId ?? '', planOpId ?? '', rowsParams),
    queryFn: async ({ signal }) => {
      const p = await api.getRepairPlanRows(selectedFixerId ?? '', planOpId ?? '', rowsParams, {
        signal,
      });
      noteRowClasses(p.plan_op_id, p.rows);
      return p;
    },
    enabled: active && selectedFixerId !== null && planOpId !== null,
    // The page stays up while the next page of the same view loads. A page of
    // the previous fixer, plan, filter or class never renders under the new one.
    placeholderData: (prev) => (isOnScreen(prev) ? prev : undefined),
  });
  const { refetch: refetchRows } = rowsQuery;
  const reloadRows = useCallback(() => void refetchRows(), [refetchRows]);
  const currentPage = (!rowsQuery.isError && rowsQuery.data) || null;
  const rows = useMemo(() => currentPage?.rows ?? [], [currentPage]);

  // ---- selection ----------------------------------------------------------
  const toggleRow = useCallback((rowId: string) => {
    setSelectedRowIds((prev) => {
      const next = new Set(prev);
      if (next.has(rowId)) next.delete(rowId);
      else next.add(rowId);
      return next;
    });
  }, []);

  const planOutcomes = useMemo(
    () => (planOpId ? (outcomesByPlan[planOpId] ?? {}) : {}),
    [outcomesByPlan, planOpId]
  );
  const settledRowIds = useMemo(() => {
    const set = new Set<string>();
    for (const r of Object.values(planOutcomes)) {
      if (SETTLED_OUTCOMES.has(r.outcome)) set.add(r.row_id);
    }
    return set;
  }, [planOutcomes]);

  const selectPage = useCallback(() => {
    setSelectedRowIds((prev) => {
      const next = new Set(prev);
      for (const r of rows) if (!r.skipped && !settledRowIds.has(r.row_id)) next.add(r.row_id);
      return next;
    });
  }, [rows, settledRowIds]);

  const selectRows = useCallback(
    (rowIds: readonly string[], on: boolean) => {
      const skipped = new Set(rows.filter((r) => r.skipped).map((r) => r.row_id));
      setSelectedRowIds((prev) => {
        const next = new Set(prev);
        for (const id of rowIds) {
          if (!on) next.delete(id);
          else if (!skipped.has(id) && !settledRowIds.has(id)) next.add(id);
        }
        return next;
      });
    },
    [rows, settledRowIds]
  );

  const clearSelection = useCallback(() => setSelectedRowIds(new Set()), []);

  const setFilter = useCallback((f: RepairRowsFilter) => {
    setFilterState(f);
    setOffset(0);
  }, []);

  const setRowClass = useCallback((c: string | null) => {
    setRowClassState(c);
    setOffset(0);
  }, []);

  const setPageSize = useCallback((n: number) => {
    setPageSizeState(n);
    setOffset(0);
    writeStored(REPAIRS_PAGE_SIZE_STORAGE_KEY, String(n));
  }, []);

  const selectFixer = useCallback((id: string) => {
    setPickedFixerId(id);
    setFilterState('applicable');
    setRowClassState(null);
    writeStored(REPAIRS_FIXER_STORAGE_KEY, id);
  }, []);

  // ---- actions ------------------------------------------------------------
  const { mutate: startTrial } = useMutation({
    mutationFn: (fixerId: string) => api.startRepairPlan(fixerId),
    onSuccess: (started, fixerId) => {
      if (started.deduped) {
        toast('A trial with the same settings is already running; following it.', 'info');
      } else {
        toast('Trial started. Nothing is written until you apply rows from it.', 'info');
      }
      setStartedTrials((prev) => ({ ...prev, [fixerId]: started.operation_id }));
    },
    onError: (err) =>
      toast(`Could not start the trial: ${errorMessage(err, 'unknown error')}`, 'error'),
  });

  /**
   * Every applicable row id of the stored plan (of rowClass only, when set),
   * less the rows already settled.
   */
  const collectApplicableIds = useCallback(
    async (
      fixerId: string,
      plan: string,
      settled: ReadonlySet<string>,
      rowClass?: string
    ): Promise<string[]> => {
      const ids: string[] = [];
      for (let off = 0; ;) {
        const p = await api.getRepairPlanRows(fixerId, plan, {
          filter: 'applicable',
          offset: off,
          limit: api.REPAIRS_ROWS_MAX_LIMIT,
          rowClass,
        });
        noteRowClasses(plan, p.rows);
        for (const r of p.rows) if (!r.skipped && !settled.has(r.row_id)) ids.push(r.row_id);
        off += p.rows.length;
        if (p.rows.length === 0 || off >= p.total) return ids;
      }
    },
    [noteRowClasses]
  );

  const report = (fixerId: string, problem: RepairApplyProblem) => {
    setApplyErrors((prev) => ({ ...prev, [fixerId]: problem }));
    toast(problem.message, problem.severity);
  };

  // 1. Start. A failure here means nothing was enqueued.
  const startApply = useMutation({
    mutationFn: async ({ fixerId, plan, resolveIds, start }: ApplyRequest) => {
      const rowIds = await resolveIds();
      return rowIds.length === 0 ? null : (start ?? api.startRepairApply)(fixerId, plan, rowIds);
    },
    onMutate: ({ fixerId }) =>
      setApplyErrors((prev) => {
        const next = { ...prev };
        delete next[fixerId];
        return next;
      }),
    onSuccess: (started, { fixerId }) => {
      if (!started) return toast('No rows left to apply from this trial.', 'info');
      // A new op has no end yet, whatever the cache holds under its id.
      qc.removeQueries({ queryKey: queryKeys.ops.v2(started.operation_id) });
      setApplyOps((prev) => ({ ...prev, [fixerId]: started.operation_id }));
    },
    onError: (err, { fixerId }) =>
      report(fixerId, {
        severity: 'error',
        message: `Apply not started: ${errorMessage(err, 'unknown error')}`,
      }),
  });

  const endApply = (fixerId: string, problem?: RepairApplyProblem) => {
    if (problem) report(fixerId, problem);
    setApplyOps((prev) => {
      const next = { ...prev };
      delete next[fixerId];
      return next;
    });
    reloadFixers();
    void qc.invalidateQueries({ queryKey: queryKeys.books.all() });
  };

  // 3. Read the result. A failed op may have stored none; its own error then
  // says why. A completed op whose result cannot be read DID run: report
  // that, not a failure.
  const readApplyResult = async (fixerId: string, opId: string, op: OperationV2) => {
    let r: RepairApplyResult | null = null;
    try {
      r = await api.getRepairApplyResult(opId);
    } catch (err) {
      if (op.status === 'completed') {
        return endApply(fixerId, {
          severity: 'warning',
          message:
            `Apply ${opId} finished, but its result could not be read ` +
            `(${errorMessage(err, 'unknown error')}). Re-run the trial to see what changed.`,
        });
      }
    }
    if (!r) {
      const why = op.error_message || `the operation ended with status "${op.status}".`;
      return endApply(fixerId, { severity: 'error', message: `Apply failed: ${why}` });
    }
    const result = r;
    setApplyResults((prev) => ({ ...prev, [fixerId]: result }));
    setOutcomesByPlan((prev) => {
      const merged = { ...(prev[result.plan_op_id] ?? {}) };
      for (const row of result.rows) merged[row.row_id] = row;
      return { ...prev, [result.plan_op_id]: merged };
    });
    setSelectedRowIds(new Set());
    toast(summarizeApply(result), applySeverity(result));
    endApply(fixerId);
  };

  const { mutate: applyMutate } = startApply;

  // 2. Follow, while the lane is active. From here the op exists on the server
  // and may be writing, so losing sight of it is NOT a failed apply: saying so
  // would invite a second apply of the same rows.
  useQueries({
    queries: Object.entries(active ? applyOps : {}).map(([fixerId, opId]) =>
      followOp(
        qc,
        opId,
        (op) => void readApplyResult(fixerId, opId, op),
        (err) =>
          endApply(fixerId, {
            severity: 'warning',
            message:
              `Lost track of apply ${opId} (${errorMessage(err, 'unknown error')}); ` +
              'it may still be writing. Check the operations list before applying again.',
          })
      )
    ),
  });

  const fixerTitle = useCallback(
    (id: string) => fixers.find((f) => f.id === id)?.title ?? id,
    [fixers]
  );

  // Rows of this plan still worth sending. The stored plan never changes, so
  // without the subtraction a second "apply all" would re-send rows already
  // applied and the server would report them as changed since the trial.
  // Under a selected class it counts that class's rows only, the rows "apply
  // all" then sends.
  const remainingApplicable = useMemo(() => {
    if (currentPage === null) return null;
    if (!rowClass) return Math.max(0, currentPage.applicable - settledRowIds.size);
    let settledInClass = 0;
    for (const id of settledRowIds)
      if (rowClasses[`${planOpId}|${id}`] === rowClass) settledInClass++;
    return Math.max(0, (currentPage.applicable_in_class ?? 0) - settledInClass);
  }, [currentPage, rowClass, settledRowIds, planOpId, rowClasses]);

  const dispatch = useCallback(
    (action: RepairsAction) => {
      switch (action.type) {
        case 'runTrial':
          startTrial(action.fixerId);
          return;
        case 'applyRows': {
          const ids = action.rowIds.filter((id) => !settledRowIds.has(id));
          if (ids.length === 0) return;
          if (!window.confirm(repairApplyConfirmMessage(ids.length, fixerTitle(action.fixerId)))) {
            return;
          }
          applyMutate({
            fixerId: action.fixerId,
            plan: action.planOpId,
            resolveIds: async () => ids,
          });
          return;
        }
        case 'applyAllApplicable': {
          // The count is the stored plan's own tally (which does not change)
          // less the rows this session already settled: exact before a single
          // id is fetched, so the confirm comes first and a cancel sends
          // nothing at all.
          const n = remainingApplicable ?? 0;
          if (n === 0) return;
          if (!window.confirm(repairApplyConfirmMessage(n, fixerTitle(action.fixerId)))) return;
          const settled = new Set(settledRowIds);
          applyMutate({
            fixerId: action.fixerId,
            plan: action.planOpId,
            resolveIds: () =>
              collectApplicableIds(action.fixerId, action.planOpId, settled, action.rowClass),
          });
          return;
        }
        case 'ownerApplyRow': {
          // One row, its own endpoint: the server mints a one-shot grant only
          // for an interactive owner sign-in and refuses anything else.
          if (settledRowIds.has(action.rowId)) return;
          const msg = repairOwnerApplyConfirmMessage(
            action.title,
            fixerTitle(action.fixerId),
            action.fragments
          );
          if (!window.confirm(msg)) {
            return;
          }
          applyMutate({
            fixerId: action.fixerId,
            plan: action.planOpId,
            resolveIds: async () => [action.rowId],
            start: (fixerId, plan, rowIds) => api.startRepairOwnerApply(fixerId, plan, rowIds[0]),
          });
          return;
        }
      }
    },
    [collectApplicableIds, fixerTitle, remainingApplicable, applyMutate, startTrial, settledRowIds]
  );

  const applyResult =
    selectedFixerId && applyResults[selectedFixerId]?.plan_op_id === planOpId
      ? applyResults[selectedFixerId]
      : null;

  const rowOutcomes = useMemo(() => new Map(Object.entries(planOutcomes)), [planOutcomes]);

  const trial = selectedFixerId ? (trials[selectedFixerId] ?? null) : null;
  const fixersFailure = fixersQuery.error as (Error & { status?: number }) | null;
  const fixersError = !fixersFailure
    ? null
    : fixersFailure.status === 404
      ? 'This server has no repairs endpoints yet. It needs a build that includes the repairs framework.'
      : errorMessage(fixersFailure, 'Failed to load the repair fixers.');

  return {
    active,
    fixers,
    fixersLoading: fixersQuery.isFetching,
    fixersError,
    reloadFixers,
    ownerStatus: ownerQuery.isError ? null : (ownerQuery.data ?? null),
    selectedFixerId,
    selectedFixer,
    selectFixer,
    planOpId,
    trial,
    trials,
    filter,
    setFilter,
    rowClass,
    setRowClass,
    offset,
    pageSize,
    setOffset,
    setPageSize,
    page: currentPage,
    rows,
    rowsLoading: rowsQuery.isFetching,
    rowsError: rowsQuery.error
      ? errorMessage(rowsQuery.error, 'Failed to load the trial rows.')
      : null,
    reloadRows,
    selectedRowIds,
    toggleRow,
    selectPage,
    selectRows,
    clearSelection,
    applying:
      selectedFixerId !== null &&
      (selectedFixerId in applyOps ||
        (startApply.isPending && startApply.variables.fixerId === selectedFixerId)),
    applyError: selectedFixerId ? (applyErrors[selectedFixerId] ?? null) : null,
    settledRowIds,
    remainingApplicable,
    applyResult,
    rowOutcomes,
    dispatch,
  };
}
