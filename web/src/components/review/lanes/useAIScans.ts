// file: web/src/components/review/lanes/useAIScans.ts
// version: 1.2.0
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
 *   - NO PAGE-LOCAL POLL OF THE SCAN. The old tab ran a 5 s `setInterval` against
 *     the active scan. Starting a scan here enqueues an operation, and the
 *     notification bell already follows operations, so progress is shown there.
 *     The history is re-read when the sub-view opens, after a start, a cancel or
 *     an apply finishing, and from the Refresh button; nothing re-reads on a timer.
 *     (The only poll is the apply operation's own, below.)
 *   - EVERY READ IS ABORTABLE AND HAS A DEADLINE (`aiScanReads.ts`), and the list
 *     and the results each have four distinct states: loading, error, empty,
 *     populated. A failed read used to be swallowed (`.catch(() => {})`), which
 *     rendered the same as "no scans yet".
 *   - APPLY REALLY MERGES, AND SAYS WHAT HAPPENED. The old tab's Apply only marked
 *     the rows applied. It now queues an ai.author-scan-apply operation (202),
 *     which merges each selected result's authors (keeping locked author names
 *     where they are) and marks only the results whose merge succeeded. The
 *     operation is followed with a bounded, abortable poll of THAT operation (the
 *     rows cannot be re-read until it has ended, and a toast cannot say what it
 *     did until then); its failure text names each result that was not applied.
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

/** How often the apply operation is polled, and how long one poll may take. */
export const AI_APPLY_POLL_MS = 1500;
const AI_APPLY_REQUEST_TIMEOUT_MS = 15_000;

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
  /** Author names by id for the open scan's rows. A merged-away author is absent. */
  authorNames: Readonly<Record<number, string>>;
  /**
   * Whether a row can be selected and applied: a merge that has a keep author, is
   * not applied yet, in a scan that is not superseded. Every other action
   * (rename, alias, split, reclassify, skip) is read-only here.
   */
  isApplicable: (r: AIScanResult) => boolean;
  /** The other half of a disagreement (the other phase's answer), or null. */
  pairOf: (r: AIScanResult) => AIScanResult | null;

  // ---- filter, counts, rows ----
  filter: AgreementFilter;
  setFilter: (f: AgreementFilter) => void;
  /** Counted from the same array the rows come from. */
  counts: Record<AgreementFilter, number>;
  filtered: AIScanResult[];
  selection: RowSelection<number>;

  // ---- verbs ----
  /** An apply is in flight: the POST, or the operation it queued. */
  applying: boolean;
  /**
   * Queues the merge of the selected results and resolves once the server has
   * accepted it. REJECTS when the server refuses (the message is the server's own),
   * so a caller can show it. The operation is then followed in the background; its
   * outcome arrives as `notice` (or a toast, when another scan has been opened),
   * and the history and rows are re-read when it ends.
   */
  applySelected: () => Promise<void>;
  notice: AIScanNotice | null;
  dismissNotice: () => void;
  /** Starts a scan in the current mode and resolves with it. Rejects if the server refuses. */
  startScan: () => Promise<AIScan>;
  /** A start is in flight (the POST has not answered). */
  starting: boolean;
  /** Why Start is unavailable, or undefined when it is available. */
  startDisabledReason: string | undefined;
  /** Cancels the running scan (the open one if it is running, else the newest running one). */
  cancelScan: () => Promise<void>;
  /** The scan Cancel would act on. */
  cancelTarget: AIScan | null;
  /** A cancel is in flight (the POST has not answered). */
  canceling: boolean;
  /** Why Cancel is unavailable, or undefined when it is available. */
  cancelDisabledReason: string | undefined;
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
  // The scan open RIGHT NOW, for continuations that outlive a render: an apply
  // that is still in flight when the reviewer opens another scan must not write
  // its outcome, clear a selection or re-read rows that now belong to that other
  // scan. Set in the same call that sets the state, never during render.
  const openIdRef = useRef<number | null>(null);
  const openScanId = useCallback((id: number | null) => {
    openIdRef.current = id;
    setSelectedId(id);
  }, []);
  const scan = useMemo(
    () => (selectedId === null ? null : (history.scans.find((s) => s.id === selectedId) ?? null)),
    [history.scans, selectedId]
  );
  const [notice, setNotice] = useState<AIScanNotice | null>(null);

  const selectScan = useCallback(
    (id: number) => {
      openScanId(id);
      setNotice(null);
    },
    [openScanId]
  );

  // ---- results ----
  // `forId` says which scan the held rows belong to, so a newly opened scan never
  // shows the previous scan's rows while its own are loading. Rows of the SAME
  // scan stay on screen across a re-read (an apply, a refresh).
  const [res, setRes] = useState<{
    forId: number | null;
    rows: AIScanResult[];
    names: Record<number, string>;
    answered: number | null;
    error: string | null;
  }>({ forId: null, rows: [], names: {}, answered: null, error: null });
  const loadableId = scan && hasResults(scan) ? scan.id : null;
  // The scan whose results should be on screen. A second guard beside the abort:
  // an answer for any other id is dropped even if its request was somehow not
  // aborted (a mocked or non-cooperating transport), so a late answer for scan A
  // can never replace scan B's rows.
  const loadableIdRef = useRef<number | null>(null);
  useEffect(() => {
    loadableIdRef.current = loadableId;
  }, [loadableId]);

  useEffect(() => {
    if (!active || loadableId === null) return;
    const ctl = new AbortController();
    const stale = () => ctl.signal.aborted || loadableIdRef.current !== loadableId;
    fetchAIScanResults(loadableId, ctl.signal)
      .then(({ results: rows, authorNames }) => {
        if (stale()) return;
        setRes({ forId: loadableId, rows, names: authorNames, answered: resultsNonce, error: null });
      })
      .catch((e: unknown) => {
        if (stale() || isAbortError(e)) return;
        setRes((r) => ({
          forId: loadableId,
          rows: r.forId === loadableId ? r.rows : [],
          names: r.forId === loadableId ? r.names : {},
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
  const authorNames = useMemo(() => (mine ? res.names : {}), [mine, res.names]);
  const byId = useMemo(() => new Map(results.map((r) => [r.id, r])), [results]);
  const pairOf = useCallback(
    (r: AIScanResult): AIScanResult | null => (r.paired_with ? (byId.get(r.paired_with) ?? null) : null),
    [byId]
  );
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

  // One list, no paging. Only merge rows can be selected, and not an applied one or a
  // superseded scan's. Switching scan or filter clears the selection (the key changes
  // during render, not in an effect), so an apply never reaches rows the
  // reviewer can no longer see.
  const superseded = scan?.status === 'superseded';
  const isApplicable = useCallback(
    (r: AIScanResult) =>
      !superseded && !r.applied && r.suggestion.action === 'merge' && (r.suggestion.keep_id ?? 0) > 0,
    [superseded]
  );
  const applicableIds = useMemo(() => new Set(results.filter(isApplicable).map((r) => r.id)), [results, isApplicable]);
  const isDisabled = useCallback((id: number) => !applicableIds.has(id), [applicableIds]);
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

  // The apply operation being followed, so leaving the sub-view stops the poll.
  const applyWatchRef = useRef<AbortController | null>(null);
  useEffect(
    () => () => {
      applyWatchRef.current?.abort();
    },
    []
  );

  const applySelected = useCallback(async () => {
    if (!scan || applyingRef.current) return;
    const forId = scan.id;
    const sel = selection;
    const ids = [...sel.selected];
    if (ids.length === 0) return;
    applyingRef.current = true;
    setApplying(true);
    setNotice(null);
    // Whether the scan applied from is still the one on screen. If the reviewer
    // opened another while the request was out, the outcome is reported by toast
    // (naming the scan) and nothing else is touched: the notice, the selection
    // and the rows now belong to the other scan.
    const stillOpen = () => openIdRef.current === forId;

    let started: api.AIScanApplyStarted;
    try {
      started = await api.applyAIScanResults(forId, ids);
    } catch (e: unknown) {
      // The server's own words: a superseded scan names the scan to apply from, a
      // row that is not a merge names itself.
      const text = messageOf(e, 'Failed to apply the selected results');
      // 409: the scan was superseded (or a row applied) after it was loaded.
      // Re-read the history and rows so they show as they are now, instead of
      // leaving an Apply button that can only fail again.
      const conflict = e instanceof api.ApiError && e.status === 409;
      if (stillOpen()) {
        setNotice({ severity: 'error', text });
        if (conflict) {
          sel.clear();
          refresh();
        }
      } else {
        toast(`Scan #${forId}: ${text}`, 'error');
        if (conflict) setHistoryNonce((n) => n + 1);
      }
      applyingRef.current = false;
      setApplying(false);
      // Rethrown so the caller (startJob) can report the refusal as a toast too.
      throw e;
    }

    // Accepted. The merges happen in the operation; its progress is in the bell.
    if (stillOpen()) sel.clear();
    const ctl = new AbortController();
    applyWatchRef.current = ctl;
    void (async () => {
      try {
        const final = await api.pollOperationV2(started.id, undefined, AI_APPLY_POLL_MS, {
          signal: ctl.signal,
          requestTimeoutMs: AI_APPLY_REQUEST_TIMEOUT_MS,
        });
        const ok = final.status === 'completed';
        const noun = ids.length === 1 ? 'result' : 'results';
        const text = ok
          ? `Merged the authors of ${ids.length} selected ${noun}.`
          : final.error_message || `The apply ended ${final.status.replaceAll('_', ' ')}. Refresh to see which results were applied.`;
        // A failed run still merged some results; the rows say which.
        if (stillOpen()) {
          setNotice({ severity: ok ? 'success' : 'warning', text });
        } else {
          toast(`Scan #${forId}: ${text}`, ok ? 'success' : 'warning');
        }
        if (stillOpen()) setResultsNonce((n) => n + 1);
        setHistoryNonce((n) => n + 1);
      } catch (e: unknown) {
        if (ctl.signal.aborted || isAbortError(e)) return;
        const text = `Could not follow the apply: ${messageOf(e, 'the server did not answer')}. Refresh to see which results were applied.`;
        if (stillOpen()) setNotice({ severity: 'warning', text });
        else toast(`Scan #${forId}: ${text}`, 'warning');
      } finally {
        if (applyWatchRef.current === ctl) applyWatchRef.current = null;
        applyingRef.current = false;
        setApplying(false);
      }
    })();
  }, [scan, selection, refresh, toast]);

  // Newest-first, so the first running scan is the newest one.
  const runningScan = useMemo(() => history.scans.find((s) => !isTerminalScan(s.status)) ?? null, [history.scans]);
  const cancelTarget = scan && !isTerminalScan(scan.status) ? scan : runningScan;

  const dismissNotice = useCallback(() => setNotice(null), []);

  // Start. `starting` is state (the menu's disabled reason reads it) and the ref
  // is the synchronous guard for a second call in the same tick.
  const [starting, setStarting] = useState(false);
  const startingRef = useRef(false);
  const startDisabledReason = starting
    ? 'A scan is being started.'
    : !history.loaded
      ? historyError
        ? 'The scan history could not be loaded, so a running scan cannot be ruled out. Refresh it first.'
        : 'The scan history is still loading.'
      : runningScan
        ? `Scan #${runningScan.id} is still running. Refresh if it has finished.`
        : undefined;

  const startScan = useCallback(async () => {
    if (startingRef.current) throw new Error('A scan is already being started.');
    startingRef.current = true;
    setStarting(true);
    try {
      const started = await api.startAIScan(mode);
      // Put the new scan in the list NOW. Until the re-read answers (or if it
      // fails) the list would show nothing running and Start would be live again,
      // so a second click would queue a second paid scan. Opening it is the
      // reviewer's next step anyway.
      setHistory((h) => ({
        ...h,
        scans: [started, ...h.scans.filter((s) => s.id !== started.id)].sort((a, b) => b.id - a.id),
      }));
      openScanId(started.id);
      setNotice(null);
      reloadHistory();
      return started;
    } finally {
      startingRef.current = false;
      setStarting(false);
    }
  }, [mode, reloadHistory, openScanId]);

  const [canceling, setCanceling] = useState(false);
  const cancelingRef = useRef(false);
  const cancelDisabledReason = canceling
    ? 'A cancel is in progress.'
    : !history.loaded
      ? 'The scan history is still loading.'
      : cancelTarget
        ? undefined
        : 'No AI author scan is running. Refresh if one was just started.';

  const cancelScan = useCallback(async () => {
    if (!cancelTarget || cancelingRef.current) return;
    cancelingRef.current = true;
    setCanceling(true);
    try {
      await api.cancelAIScan(cancelTarget.id);
      toast(`Scan #${cancelTarget.id} canceled.`, 'success');
    } catch (e: unknown) {
      toast(`Could not cancel scan #${cancelTarget.id}: ${messageOf(e, 'the server refused')}`, 'error');
    } finally {
      cancelingRef.current = false;
      setCanceling(false);
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
    authorNames,
    isApplicable,
    pairOf,
    filter,
    setFilter,
    counts,
    filtered,
    selection,
    applying,
    applySelected,
    notice,
    dismissNotice,
    starting,
    startScan,
    startDisabledReason,
    cancelScan,
    cancelTarget,
    canceling,
    cancelDisabledReason,
  };
}
