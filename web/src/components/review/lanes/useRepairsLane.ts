// file: web/src/components/review/lanes/useRepairsLane.ts
// version: 1.4.0
// guid: 7b1e5c28-3a94-4d6f-8e02-c5f9a1d7b340
// last-edited: 2026-10-06

/**
 * The repairs lane's data layer: fixers, their trials (plans), plan rows, and
 * applies.
 *
 * Shaped like the other lanes (own fetches, own AbortControllers, `active`
 * gating: an inactive lane makes no requests and polls nothing).
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

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
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

function isAbort(err: unknown): boolean {
  return err instanceof DOMException && err.name === 'AbortError';
}

/** Statuses of a plan that has not finished. */
function isInFlight(status: string | undefined): boolean {
  return status === 'queued' || status === 'running' || status === 'pending';
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
  if (r.not_in_plan && r.not_in_plan.length > 0) parts.push(`not in trial ${r.not_in_plan.length}`);
  let line = parts.join(' · ');
  if (r.dry_run) line = `Preview only, nothing was written: ${line}`;
  if (r.aborted) line += ` · stopped early: ${r.aborted}`;
  return line;
}

/**
 * The severity of an apply result, for the toast and the result banner alike:
 * warning when anything was not written (failed, partly applied, retry later,
 * stopped early) or when it was only a preview.
 */
export function applySeverity(r: RepairApplyResult): 'success' | 'warning' {
  return r.failed > 0 || r.partially_applied > 0 || (r.retry_later ?? 0) > 0 || r.aborted || r.dry_run
    ? 'warning'
    : 'success';
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

export function useRepairsLane(toast: Toast, active = true): RepairsLane {
  const [fixers, setFixers] = useState<RepairFixer[]>([]);
  const [fixersLoading, setFixersLoading] = useState(false);
  const [fixersError, setFixersError] = useState<string | null>(null);
  const [fixersNonce, setFixersNonce] = useState(0);

  const [selectedFixerId, setSelectedFixerId] = useState<string | null>(() =>
    readStored(REPAIRS_FIXER_STORAGE_KEY)
  );
  const [knownPlans, setKnownPlans] = useState<Record<string, KnownPlan>>({});
  const [trials, setTrials] = useState<Record<string, RepairTrialState>>({});

  const [filter, setFilterState] = useState<RepairRowsFilter>('applicable');
  const [rowClass, setRowClassState] = useState<string | null>(null);
  const [offset, setOffset] = useState(0);
  const [pageSize, setPageSizeState] = useState<number>(initialPageSize);
  const [page, setPage] = useState<RepairRowsPage | null>(null);
  const [rowsLoading, setRowsLoading] = useState(false);
  const [rowsError, setRowsError] = useState<string | null>(null);
  const [rowsNonce, setRowsNonce] = useState(0);

  const [selectedRowIds, setSelectedRowIds] = useState<Set<string>>(() => new Set());
  const [applyingFor, setApplyingFor] = useState<string | null>(null);
  const [applyErrors, setApplyErrors] = useState<Record<string, RepairApplyProblem>>({});
  const [applyResults, setApplyResults] = useState<Record<string, RepairApplyResult>>({});
  // Every row outcome reported for a plan, merged across applies, so a second
  // apply does not wipe the first one's badges.
  const [outcomesByPlan, setOutcomesByPlan] = useState<
    Record<string, Record<string, RepairRowResult>>
  >({});

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

  // ---- fixer list ---------------------------------------------------------
  useEffect(() => {
    if (!active) return;
    const ctrl = new AbortController();
    setFixersLoading(true);
    setFixersError(null);
    api
      .listRepairFixers({ signal: ctrl.signal })
      .then((list) => {
        if (ctrl.signal.aborted) return;
        setFixers(list);
        setKnownPlans((prev) => {
          let next = prev;
          for (const f of list) {
            const lp = f.last_plan;
            if (lp?.status !== 'completed') continue;
            next = withNewerPlan(next, f.id, {
              opId: lp.operation_id,
              completedAt: lp.completed_at ?? lp.queued_at,
            });
          }
          return next;
        });
        // A trial the lane was following that the server now reports as done
        // (followed poll aborted when the lane went inactive, say).
        setTrials((prev) => {
          let changed = false;
          const next = { ...prev };
          for (const f of list) {
            const t = next[f.id];
            if (t && f.last_plan?.operation_id === t.opId && f.last_plan.status === 'completed') {
              delete next[f.id];
              changed = true;
            }
          }
          return changed ? next : prev;
        });
        setSelectedFixerId((cur) =>
          cur && list.some((f) => f.id === cur) ? cur : (list[0]?.id ?? null)
        );
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        const status = (err as { status?: number } | null)?.status;
        setFixersError(
          status === 404
            ? 'This server has no repairs endpoints yet. It needs a build that includes the repairs framework.'
            : errorMessage(err, 'Failed to load the repair fixers.')
        );
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setFixersLoading(false);
      });
    return () => ctrl.abort();
  }, [active, fixersNonce]);

  const reloadFixers = useCallback(() => setFixersNonce((n) => n + 1), []);

  // ---- following trials ---------------------------------------------------
  const trialPolls = useRef(new Map<string, AbortController>());

  const followTrial = useCallback(
    (fixerId: string, opId: string) => {
      if (trialPolls.current.has(opId)) return;
      const ctrl = new AbortController();
      trialPolls.current.set(opId, ctrl);
      setTrials((prev) => ({ ...prev, [fixerId]: { opId, phase: 'running' } }));
      api
        .pollOperationV2(opId, undefined, REPAIRS_POLL_INTERVAL_MS, {
          signal: ctrl.signal,
          requestTimeoutMs: api.REPAIRS_FETCH_TIMEOUT_MS,
        })
        .then((op) => {
          if (ctrl.signal.aborted) return;
          if (op.status === 'completed') {
            setKnownPlans((prev) =>
              withNewerPlan(prev, fixerId, {
                opId,
                completedAt: op.completed_at ?? new Date().toISOString(),
              })
            );
            setTrials((prev) => {
              if (prev[fixerId]?.opId !== opId) return prev;
              const next = { ...prev };
              delete next[fixerId];
              return next;
            });
            toast('Trial finished. Its rows are ready to review.', 'success');
            setFixersNonce((n) => n + 1);
          } else {
            const message = op.error_message || `The trial ended with status "${op.status}".`;
            setTrials((prev) => ({ ...prev, [fixerId]: { opId, phase: 'failed', message } }));
            toast(`Trial failed: ${message}`, 'error');
          }
        })
        .catch((err: unknown) => {
          if (ctrl.signal.aborted || isAbort(err)) {
            // The lane went inactive or unmounted. Forget the running state;
            // the next fixer load re-follows the trial if it is still going.
            setTrials((prev) => {
              if (prev[fixerId]?.opId !== opId) return prev;
              const next = { ...prev };
              delete next[fixerId];
              return next;
            });
            return;
          }
          const message = `Lost track of the trial: ${errorMessage(err, 'unknown error')}`;
          setTrials((prev) => ({ ...prev, [fixerId]: { opId, phase: 'failed', message } }));
          // If it is still running, the reload's auto-follow picks it back up.
          setFixersNonce((n) => n + 1);
        })
        .finally(() => {
          if (trialPolls.current.get(opId) === ctrl) trialPolls.current.delete(opId);
        });
    },
    [toast]
  );

  // Follow any trial the server reports as still running -- one started from
  // another tab, or before a reload -- so it does not look idle.
  useEffect(() => {
    if (!active) return;
    for (const f of fixers) {
      const lp = f.last_plan;
      if (lp && isInFlight(lp.status)) followTrial(f.id, lp.operation_id);
    }
  }, [active, fixers, followTrial]);

  // An inactive lane polls nothing.
  useEffect(() => {
    if (active) return;
    const polls = trialPolls.current;
    for (const ctrl of polls.values()) ctrl.abort();
    polls.clear();
  }, [active]);

  const applyPolls = useRef(new Set<AbortController>());
  useEffect(() => {
    const trialsMap = trialPolls.current;
    const applies = applyPolls.current;
    return () => {
      for (const ctrl of trialsMap.values()) ctrl.abort();
      trialsMap.clear();
      for (const ctrl of applies) ctrl.abort();
      applies.clear();
    };
  }, []);

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
  useEffect(() => {
    if (!active || !selectedFixerId || !planOpId) return;
    const ctrl = new AbortController();
    setRowsLoading(true);
    setRowsError(null);
    api
      .getRepairPlanRows(
        selectedFixerId,
        planOpId,
        { filter, offset, limit: pageSize, rowClass: rowClass ?? undefined },
        { signal: ctrl.signal }
      )
      .then((p) => {
        if (ctrl.signal.aborted) return;
        noteRowClasses(p.plan_op_id, p.rows);
        setPage(p);
      })
      .catch((err: unknown) => {
        if (ctrl.signal.aborted) return;
        setPage(null);
        setRowsError(errorMessage(err, 'Failed to load the trial rows.'));
      })
      .finally(() => {
        if (!ctrl.signal.aborted) setRowsLoading(false);
      });
    return () => ctrl.abort();
  }, [active, selectedFixerId, planOpId, filter, rowClass, offset, pageSize, rowsNonce, noteRowClasses]);

  const reloadRows = useCallback(() => setRowsNonce((n) => n + 1), []);

  // The loaded page counts only when it is the page on screen: a response for
  // the previous fixer, plan or filter must not render under the new one.
  const currentPage =
    page &&
    page.fixer_id === selectedFixerId &&
    page.plan_op_id === planOpId &&
    (page.filter ?? '') === filter &&
    (page.class ?? '') === (rowClass ?? '')
      ? page
      : null;
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
    setSelectedFixerId(id);
    setFilterState('applicable');
    setRowClassState(null);
    writeStored(REPAIRS_FIXER_STORAGE_KEY, id);
  }, []);

  // ---- actions ------------------------------------------------------------
  const runTrial = useCallback(
    async (fixerId: string) => {
      try {
        const started = await api.startRepairPlan(fixerId);
        if (started.deduped) {
          toast('A trial with the same settings is already running; following it.', 'info');
        } else {
          toast('Trial started. Nothing is written until you apply rows from it.', 'info');
        }
        followTrial(fixerId, started.operation_id);
      } catch (err) {
        toast(`Could not start the trial: ${errorMessage(err, 'unknown error')}`, 'error');
      }
    },
    [followTrial, toast]
  );

  /**
   * Every applicable row id of the stored plan (of rowClass only, when set),
   * less the rows already settled.
   */
  const collectApplicableIds = useCallback(
    async (fixerId: string, plan: string, settled: ReadonlySet<string>, rowClass?: string): Promise<string[]> => {
      const ids: string[] = [];
      for (let off = 0; ; ) {
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

  const runApply = useCallback(
    async (fixerId: string, plan: string, resolveIds: () => Promise<string[]>) => {
      const ctrl = new AbortController();
      applyPolls.current.add(ctrl);
      setApplyingFor(fixerId);
      setApplyErrors((prev) => {
        const next = { ...prev };
        delete next[fixerId];
        return next;
      });
      const report = (problem: RepairApplyProblem) => {
        setApplyErrors((prev) => ({ ...prev, [fixerId]: problem }));
        toast(problem.message, problem.severity);
      };
      try {
        // 1. Start. A failure here means nothing was enqueued.
        let started: RepairOpStarted;
        try {
          const rowIds = await resolveIds();
          if (rowIds.length === 0) {
            toast('No rows left to apply from this trial.', 'info');
            return;
          }
          started = await api.startRepairApply(fixerId, plan, rowIds);
        } catch (err) {
          report({
            severity: 'error',
            message: `Apply not started: ${errorMessage(err, 'unknown error')}`,
          });
          return;
        }

        // 2. Follow. From here the op exists on the server and may be
        // writing, so losing sight of it is NOT a failed apply: saying so
        // would invite a second apply of the same rows.
        let op: OperationV2;
        try {
          op = await api.pollOperationV2(started.operation_id, undefined, REPAIRS_POLL_INTERVAL_MS, {
            signal: ctrl.signal,
            requestTimeoutMs: api.REPAIRS_FETCH_TIMEOUT_MS,
          });
        } catch (err) {
          if (ctrl.signal.aborted || isAbort(err)) return;
          report({
            severity: 'warning',
            message:
              `Lost track of apply ${started.operation_id} (${errorMessage(err, 'unknown error')}); ` +
              'it may still be writing. Check the operations list before applying again.',
          });
          setFixersNonce((n) => n + 1);
          return;
        }

        // 3. Read the result. A failed op may have stored none; its own error
        // then says why. A completed op whose result cannot be read DID run:
        // report that, not a failure.
        let result: RepairApplyResult | null = null;
        try {
          result = await api.getRepairApplyResult(started.operation_id);
        } catch (err) {
          if (op.status === 'completed') {
            report({
              severity: 'warning',
              message:
                `Apply ${started.operation_id} finished, but its result could not be read ` +
                `(${errorMessage(err, 'unknown error')}). Re-run the trial to see what changed.`,
            });
            setFixersNonce((n) => n + 1);
            return;
          }
        }
        if (!result) {
          report({
            severity: 'error',
            message: `Apply failed: ${op.error_message || `the operation ended with status "${op.status}".`}`,
          });
          setFixersNonce((n) => n + 1);
          return;
        }
        const r = result;
        setApplyResults((prev) => ({ ...prev, [fixerId]: r }));
        setOutcomesByPlan((prev) => {
          const merged = { ...(prev[r.plan_op_id] ?? {}) };
          for (const row of r.rows) merged[row.row_id] = row;
          return { ...prev, [r.plan_op_id]: merged };
        });
        setSelectedRowIds(new Set());
        toast(summarizeApply(r), applySeverity(r));
        setFixersNonce((n) => n + 1);
      } finally {
        applyPolls.current.delete(ctrl);
        if (!ctrl.signal.aborted) setApplyingFor((cur) => (cur === fixerId ? null : cur));
      }
    },
    [toast]
  );

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
    for (const id of settledRowIds) if (rowClasses[`${planOpId}|${id}`] === rowClass) settledInClass++;
    return Math.max(0, (currentPage.applicable_in_class ?? 0) - settledInClass);
  }, [currentPage, rowClass, settledRowIds, planOpId, rowClasses]);

  const dispatch = useCallback(
    (action: RepairsAction) => {
      switch (action.type) {
        case 'runTrial':
          void runTrial(action.fixerId);
          return;
        case 'applyRows': {
          const ids = action.rowIds.filter((id) => !settledRowIds.has(id));
          if (ids.length === 0) return;
          if (!window.confirm(repairApplyConfirmMessage(ids.length, fixerTitle(action.fixerId)))) {
            return;
          }
          void runApply(action.fixerId, action.planOpId, async () => ids);
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
          void runApply(action.fixerId, action.planOpId, () =>
            collectApplicableIds(action.fixerId, action.planOpId, settled, action.rowClass)
          );
          return;
        }
      }
    },
    [collectApplicableIds, fixerTitle, remainingApplicable, runApply, runTrial, settledRowIds]
  );

  const applyResult =
    selectedFixerId && applyResults[selectedFixerId]?.plan_op_id === planOpId
      ? applyResults[selectedFixerId]
      : null;

  const rowOutcomes = useMemo(() => new Map(Object.entries(planOutcomes)), [planOutcomes]);

  const trial = selectedFixerId ? (trials[selectedFixerId] ?? null) : null;

  return {
    active,
    fixers,
    fixersLoading,
    fixersError,
    reloadFixers,
    selectedFixerId: selectedFixer ? selectedFixerId : null,
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
    // Only meaningful while there is a plan to load: a fetch cut off by a
    // switch to a fixer with no plan would otherwise leave the flag stuck.
    rowsLoading: rowsLoading && planOpId !== null,
    rowsError: planOpId !== null ? rowsError : null,
    reloadRows,
    selectedRowIds,
    toggleRow,
    selectPage,
    clearSelection,
    applying: applyingFor !== null && applyingFor === selectedFixerId,
    applyError: selectedFixerId ? (applyErrors[selectedFixerId] ?? null) : null,
    settledRowIds,
    remainingApplicable,
    applyResult,
    rowOutcomes,
    dispatch,
  };
}
