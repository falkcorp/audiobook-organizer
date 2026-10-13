// file: web/src/components/review/lanes/useAIScans.ts
// version: 1.0.0
// guid: 0d71b3e9-6a25-4c84-b9f0-c4e82a5d1736
// last-edited: 2026-10-13

/**
 * The Authors & series lane's AI scans sub-view: the scan history, one scan's
 * results, the agreement filter, a selection, and the verbs start / cancel /
 * apply.
 *
 * Ported from DedupAIReviewTab.tsx (the old /dedup page), with three differences
 * that are the point of the port:
 *
 *   - NO PAGE-LOCAL POLL LOOP. The old tab ran a 5 s `setInterval` against the
 *     active scan. Starting a scan here enqueues an operation, and the
 *     notification bell already follows operations, so progress is shown there.
 *     The history is re-read when the sub-view opens, after a start or a cancel,
 *     and from the Refresh button; nothing here re-reads on a timer.
 *   - EVERY READ IS ABORTABLE AND HAS A DEADLINE (`aiScanReads.ts`), and the list
 *     and the results each have four distinct states: loading, error, empty,
 *     populated. A failed read used to be swallowed (`.catch(() => {})`), which
 *     rendered the same as "no scans yet".
 *   - APPLY REPORTS WHAT HAPPENED. The server answers 200 with
 *     `{applied, errors}` when some results could not be marked; the old tab
 *     dropped `errors` and looked like a clean apply.
 *
 * Owned by ReviewWorkspace, not by the panel: the lane menu's Start / Cancel
 * commands need this state (the mode switch, which scan is running), and the panel
 * unmounts with its lane.
 *
 * Counts and rows come from ONE array (the scan's full result list, filtered on
 * the client), so a chip's number is exactly the number of rows it shows. The
 * server's `?agreement=` filter is deliberately not used: it would be a second
 * fetch that could disagree with the first.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import * as api from '../../../services/api';
import type { AIScan, AIScanResult } from '../../../services/api';
import { isAbortError } from '../../../utils/apiFetch';
import { useRowSelection, type RowSelection } from '../../../hooks/useRowSelection';
import { fetchAIScanResults, fetchAIScans } from './aiScanReads';

type Toast = (message: string, severity?: 'success' | 'error' | 'warning' | 'info') => void;

/** realtime = faster and dearer; batch = cheaper, takes hours. */
export type AIScanMode = 'realtime' | 'batch';

export type AgreementFilter = 'all' | 'agreed' | 'groups_only' | 'full_only' | 'disagreed';

/** The chips, in the order the old tab's tabs ran. */
export const AGREEMENT_FILTERS: ReadonlyArray<{ value: AgreementFilter; label: string }> = [
  { value: 'all', label: 'All' },
  { value: 'agreed', label: 'Agreed' },
  { value: 'groups_only', label: 'Groups Only' },
  { value: 'full_only', label: 'Full Only' },
  { value: 'disagreed', label: 'Disagreed' },
];

/**
 * A scan in one of these states will not change again, so it does not block
 * starting a new scan. "superseded" is an unreviewed nightly ai-dedup-batch list
 * replaced by a newer run.
 */
const TERMINAL_STATUSES: ReadonlySet<string> = new Set(['complete', 'failed', 'canceled', 'superseded']);
export const isTerminalScan = (status: string): boolean => TERMINAL_STATUSES.has(status);

/** A scan whose results exist and are worth showing. A superseded scan's stay readable. */
const hasResults = (scan: AIScan): boolean => scan.status === 'complete' || scan.status === 'superseded';

export type AIHistoryStatus = 'loading' | 'error' | 'empty' | 'populated';
/** `idle`: no scan is chosen, or the chosen one has no results to read yet. */
export type AIResultsStatus = 'idle' | 'loading' | 'error' | 'empty' | 'populated';

/** The outcome of the last apply (or refused apply), shown until another scan is chosen. */
export interface AIScanNotice {
  severity: 'success' | 'warning' | 'error';
  text: string;
}

export interface AIScansLane {
  /** The two states of the Realtime / Batch switch. */
  mode: AIScanMode;
  setMode: (mode: AIScanMode) => void;

  // ---- the history ----
  /** Newest first. Empty until the first read answers. */
  scans: AIScan[];
  historyStatus: AIHistoryStatus;
  historyError: string | null;
  /** A re-read is in flight (the list already shown stays on screen). */
  refreshing: boolean;
  /** Re-reads the history and the open scan's results. */
  refresh: () => void;
  retryHistory: () => void;

  // ---- the open scan ----
  scan: AIScan | null;
  selectScan: (id: number) => void;
  superseded: boolean;
  resultsStatus: AIResultsStatus;
  resultsError: string | null;
  retryResults: () => void;
  results: AIScanResult[];

  // ---- filter, counts, rows ----
  filter: AgreementFilter;
  setFilter: (f: AgreementFilter) => void;
  /** Counted from the same array the rows come from. */
  counts: Record<AgreementFilter, number>;
  filtered: AIScanResult[];
  selection: RowSelection<number>;

  // ---- verbs ----
  applying: boolean;
  applySelected: () => Promise<void>;
  notice: AIScanNotice | null;
  dismissNotice: () => void;
  /** Starts a scan in the current mode and resolves with it. Rejects if the server refuses. */
  startScan: () => Promise<AIScan>;
  /** Why Start is unavailable, or undefined when it is available. */
  startDisabledReason: string | undefined;
  /** Cancels the running scan (the open one if it is running, else the newest running one). */
  cancelScan: () => Promise<void>;
  /** The scan Cancel would act on. */
  cancelTarget: AIScan | null;
}

const EMPTY_COUNTS: Record<AgreementFilter, number> = {
  all: 0,
  agreed: 0,
  groups_only: 0,
  full_only: 0,
  disagreed: 0,
};

const messageOf = (e: unknown, fallback: string): string => (e instanceof Error && e.message ? e.message : fallback);

/**
 * @param active  The lane is visible AND this sub-view is the one on screen.
 *                Nothing is read while it is false.
 */
export function useAIScans(toast: Toast, active: boolean): AIScansLane {
  const [mode, setMode] = useState<AIScanMode>('realtime');

  // ---- what is being asked for ----
  // A read is identified by the nonce it was sent under; its answer carries that
  // nonce back, so "in flight" and "this error is about the current request" are
  // DERIVED (nonce asked !== nonce answered) rather than flagged from inside an
  // effect. Opening the sub-view again is a new request: the nonces are bumped
  // while rendering (React's "adjust state when a prop changes" pattern), so the
  // effects below run once for it, not once and then again.
  const [historyNonce, setHistoryNonce] = useState(0);
  const [resultsNonce, setResultsNonce] = useState(0);
  const [wasActive, setWasActive] = useState(active);
  if (wasActive !== active) {
    setWasActive(active);
    if (active) {
      setHistoryNonce((n) => n + 1);
      setResultsNonce((n) => n + 1);
    }
  }

  // ---- history ----
  // `loaded` is false until the first read answers; a re-read keeps the list.
  const [history, setHistory] = useState<{
    scans: AIScan[];
    loaded: boolean;
    answered: number | null;
    error: string | null;
  }>({ scans: [], loaded: false, answered: null, error: null });

  useEffect(() => {
    if (!active) return;
    const ctl = new AbortController();
    fetchAIScans(ctl.signal)
      .then((scans) => {
        if (ctl.signal.aborted) return;
        // Newest first: the ids only grow, and the nightly scans are the ones to find.
        setHistory({
          scans: [...scans].sort((a, b) => b.id - a.id),
          loaded: true,
          answered: historyNonce,
          error: null,
        });
      })
      .catch((e: unknown) => {
        if (ctl.signal.aborted || isAbortError(e)) return;
        setHistory((h) => ({ ...h, answered: historyNonce, error: messageOf(e, 'Could not load the scan history') }));
      });
    return () => ctl.abort();
  }, [active, historyNonce]);

  const reloadHistory = useCallback(() => setHistoryNonce((n) => n + 1), []);

  // An error is shown until the next read starts, then the list (or the spinner) takes over.
  const historyError = history.answered === historyNonce ? history.error : null;
  const refreshing = active && history.answered !== historyNonce;
  const historyStatus: AIHistoryStatus = historyError
    ? 'error'
    : !history.loaded
      ? 'loading'
      : history.scans.length === 0
        ? 'empty'
        : 'populated';

  // ---- the open scan ----
  const [selectedId, setSelectedId] = useState<number | null>(null);
  const scan = useMemo(
    () => (selectedId === null ? null : (history.scans.find((s) => s.id === selectedId) ?? null)),
    [history.scans, selectedId]
  );
  const [notice, setNotice] = useState<AIScanNotice | null>(null);

  const selectScan = useCallback((id: number) => {
    setSelectedId(id);
    setNotice(null);
  }, []);

  // ---- results ----
  // `forId` says which scan the held rows belong to, so a newly opened scan never
  // shows the previous scan's rows while its own are loading. Rows of the SAME
  // scan stay on screen across a re-read (an apply, a refresh).
  const [res, setRes] = useState<{
    forId: number | null;
    rows: AIScanResult[];
    answered: number | null;
    error: string | null;
  }>({ forId: null, rows: [], answered: null, error: null });
  const loadableId = scan && hasResults(scan) ? scan.id : null;

  useEffect(() => {
    if (!active || loadableId === null) return;
    const ctl = new AbortController();
    fetchAIScanResults(loadableId, ctl.signal)
      .then((rows) => {
        if (ctl.signal.aborted) return;
        setRes({ forId: loadableId, rows, answered: resultsNonce, error: null });
      })
      .catch((e: unknown) => {
        if (ctl.signal.aborted || isAbortError(e)) return;
        setRes((r) => ({
          forId: loadableId,
          rows: r.forId === loadableId ? r.rows : [],
          answered: resultsNonce,
          error: messageOf(e, 'Could not load the scan results'),
        }));
      });
    return () => ctl.abort();
  }, [active, loadableId, resultsNonce]);

  const retryResults = useCallback(() => setResultsNonce((n) => n + 1), []);
  const refresh = useCallback(() => {
    setHistoryNonce((n) => n + 1);
    setResultsNonce((n) => n + 1);
  }, []);

  const mine = loadableId !== null && res.forId === loadableId;
  const settled = mine && res.answered === resultsNonce;
  const results = useMemo(() => (mine ? res.rows : []), [mine, res.rows]);
  const resultsError = settled ? res.error : null;
  const resultsStatus: AIResultsStatus =
    loadableId === null
      ? 'idle'
      : resultsError
        ? 'error'
        : !mine || (!settled && res.rows.length === 0)
          ? 'loading'
          : results.length === 0
            ? 'empty'
            : 'populated';

  // ---- filter, counts, selection ----
  const [filter, setFilter] = useState<AgreementFilter>('all');
  const counts = useMemo(() => {
    const c = { ...EMPTY_COUNTS, all: results.length };
    for (const r of results) {
      if (r.agreement in c) c[r.agreement] += 1;
    }
    return c;
  }, [results]);
  const filtered = useMemo(
    () => (filter === 'all' ? results : results.filter((r) => r.agreement === filter)),
    [results, filter]
  );

  // One list, no paging. Applied results and a superseded scan are not
  // selectable. Switching scan or filter clears the selection (the key changes
  // during render, not in an effect), so an apply never reaches rows the
  // reviewer can no longer see.
  const superseded = scan?.status === 'superseded';
  const appliedIds = useMemo(() => new Set(results.filter((r) => r.applied).map((r) => r.id)), [results]);
  const isDisabled = useCallback((id: number) => superseded || appliedIds.has(id), [superseded, appliedIds]);
  const keys = useMemo(() => filtered.map((r) => r.id), [filtered]);
  const selection = useRowSelection<number>({
    pageKeys: keys,
    totalMatching: keys.length,
    resetKey: `${scan?.id ?? ''}|${filter}`,
    isDisabled,
  });

  // ---- verbs ----
  const [applying, setApplying] = useState(false);
  const applyingRef = useRef(false);

  const applySelected = useCallback(async () => {
    if (!scan || applyingRef.current) return;
    const sel = selection;
    const ids = [...sel.selected];
    if (ids.length === 0) return;
    applyingRef.current = true;
    setApplying(true);
    setNotice(null);
    try {
      const out = await api.applyAIScanResults(scan.id, ids);
      const failed = out.errors ?? [];
      setNotice(
        failed.length === 0
          ? { severity: 'success', text: `Applied ${out.applied} of ${ids.length} selected results.` }
          : {
              severity: 'warning',
              text: `Applied ${out.applied} of ${ids.length} selected results. ${failed.length} could not be applied: ${failed.join('; ')}`,
            }
      );
      sel.clear();
      setResultsNonce((n) => n + 1);
    } catch (e: unknown) {
      // The server's own words: a superseded scan names the scan to apply from.
      setNotice({ severity: 'error', text: messageOf(e, 'Failed to apply the selected results') });
      // 409: the scan was superseded after it was loaded. Re-read it so it shows
      // as superseded and read-only, instead of leaving an Apply button that can
      // only fail again.
      if (e instanceof api.ApiError && e.status === 409) {
        sel.clear();
        refresh();
      }
    } finally {
      applyingRef.current = false;
      setApplying(false);
    }
  }, [scan, selection, refresh]);

  // Newest-first, so the first running scan is the newest one.
  const runningScan = useMemo(() => history.scans.find((s) => !isTerminalScan(s.status)) ?? null, [history.scans]);
  const cancelTarget = scan && !isTerminalScan(scan.status) ? scan : runningScan;

  const dismissNotice = useCallback(() => setNotice(null), []);

  const startingRef = useRef(false);
  const startDisabledReason = !history.loaded
    ? historyError
      ? 'The scan history could not be loaded, so a running scan cannot be ruled out. Refresh it first.'
      : 'The scan history is still loading.'
    : runningScan
      ? `Scan #${runningScan.id} is still running.`
      : undefined;

  const startScan = useCallback(async () => {
    if (startingRef.current) throw new Error('A scan is already being started.');
    startingRef.current = true;
    try {
      const started = await api.startAIScan(mode);
      // The new scan is the one to watch: open it, and re-read the list so it appears.
      setSelectedId(started.id);
      setNotice(null);
      reloadHistory();
      return started;
    } finally {
      startingRef.current = false;
    }
  }, [mode, reloadHistory]);

  const cancelScan = useCallback(async () => {
    if (!cancelTarget) return;
    try {
      await api.cancelAIScan(cancelTarget.id);
      toast(`Scan #${cancelTarget.id} canceled.`, 'success');
    } catch (e: unknown) {
      toast(`Could not cancel scan #${cancelTarget.id}: ${messageOf(e, 'the server refused')}`, 'error');
    } finally {
      // Whatever the answer, the list is now stale.
      reloadHistory();
    }
  }, [cancelTarget, toast, reloadHistory]);

  return {
    mode,
    setMode,
    scans: history.scans,
    historyStatus,
    historyError,
    refreshing,
    refresh,
    retryHistory: reloadHistory,
    scan,
    selectScan,
    superseded,
    resultsStatus,
    resultsError,
    retryResults,
    results,
    filter,
    setFilter,
    counts,
    filtered,
    selection,
    applying,
    applySelected,
    notice,
    dismissNotice,
    startScan,
    startDisabledReason,
    cancelScan,
    cancelTarget,
  };
}
