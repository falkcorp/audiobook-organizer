// file: web/src/components/review/lanes/useDupesLabels.ts
// version: 1.2.0
// guid: 5b2d9e47-3c18-4f60-a7d5-0e9c4b1a8f32
// last-edited: 2026-10-13
//
// Data layer for the Duplicates lane's Labels and Suspicious sub-views (the Gold
// Labels page, moved into the lane). One hook, no rendering.
//
// WHAT IT OWNS
//
// - the label stats (counts by label and by source),
// - the server-paged label list and its filters (label, source, band),
// - the suspicious queue (rule-sourced not_dup rows carrying duplicate-shaped
//   evidence),
// - the one-click human override, which POSTs the existing
//   /dedup/labels/:id/override route and stamps label_source=human.
//
// Every read has an AbortController and ends in one of four states -- loading,
// error, empty, populated -- so a failed load cannot read as "no labels".
// `view` gates the fetches: nothing is requested while the Queue sub-view is the
// visible one, and a request still in flight when the view changes is aborted.
//
// Filters live here as local state. They are seeded by nothing and mirrored to
// nothing: the URL is how you arrive at a sub-view (see ReviewWorkspace), not a
// running record of it.

import { useCallback, useEffect, useRef, useState } from 'react';
import type { DedupScoreBreakdown } from '../../../services/api';
import { ApiTimeoutError, apiFetch, isAbortError } from '../../../utils/apiFetch';
import { useDebouncedSearch } from '../../../hooks/useDebouncedSearch';

const API_BASE = '/api/v1';

/** Rows per page of the label list and the suspicious queue. */
export const LABELS_PAGE = 50;
/** A read that has not answered by now is an error, not a spinner forever. */
export const LABELS_TIMEOUT_MS = 30_000;
/** How long a typed band waits before it becomes a request. */
export const BAND_DEBOUNCE_MS = 300;

/** The dupes lane's three sub-views. Not a SpineViewMode: it picks a surface. */
export type DupesSubView = 'queue' | 'labels' | 'suspicious';

export const DUPES_SUB_VIEWS: readonly DupesSubView[] = ['queue', 'labels', 'suspicious'];

/** Parses a `?view=` value; anything unrecognised is the default Queue. */
export function parseDupesSubView(raw: string | null | undefined): DupesSubView {
  return raw === 'labels' || raw === 'suspicious' ? raw : 'queue';
}

export interface LabelBookFeatures {
  title?: string;
  primary_path?: string;
  total_duration_sec?: number;
}

/**
 * One labeled candidate pair, as /dedup/labels returns it.
 *
 * `score` and `score_breakdown` are on the wire (database.LabeledExample) and
 * were missing from the page's local copy of this type -- audit finding D11
 * (docs/audits/2026-09-02-dedup-review-matching-path-audit.md) -- so the page
 * could never show WHY a pair was labelled the way it was.
 */
export interface LabeledExample {
  candidate_id: number;
  entity_a_id: string;
  entity_b_id: string;
  layer: string;
  band?: string;
  /** 0 when no breakdown was available when the label was written. */
  score?: number;
  score_breakdown?: DedupScoreBreakdown;
  label: string;
  label_source: string;
  label_reason?: string;
  decided_at?: string;
  a?: LabelBookFeatures;
  b?: LabelBookFeatures;
}

/** A labeled row plus the reasons the backend flagged it. */
export interface SuspiciousExample extends LabeledExample {
  suspicion_reasons?: string[];
}

export interface LabelStats {
  total: number;
  by_label: Record<string, number>;
  by_source: Record<string, number>;
}

export interface LabelFilters {
  label: string;
  source: string;
  band: string;
}

export type ListStatus = 'loading' | 'error' | 'empty' | 'populated';

type Load<T> =
  { state: 'loading' } | { state: 'error'; message: string } | { state: 'ready'; value: T };

/**
 * A result tagged with the request it answers. Anything not answering the
 * CURRENT request reads as loading, so a filter change shows the spinner without
 * an effect having to set state synchronously to say so.
 */
type Keyed<T> = { key: string; load: Load<T> };

function current<T>(keyed: Keyed<T> | null, key: string): Load<T> {
  return keyed && keyed.key === key ? keyed.load : { state: 'loading' };
}

interface SuspiciousPage {
  rows: SuspiciousExample[];
  /** The server's count of the whole queue; `rows` is at most one page of it. */
  total: number;
}

interface LabelPage {
  rows: LabeledExample[];
  total: number;
}

function statusOf<T>(load: Load<T>, count: (v: T) => number): ListStatus {
  if (load.state === 'loading') return 'loading';
  if (load.state === 'error') return 'error';
  return count(load.value) === 0 ? 'empty' : 'populated';
}

function errorMessage(e: unknown, fallback: string): string {
  return e instanceof Error && e.message ? e.message : fallback;
}

async function getData<T>(url: string, signal: AbortSignal): Promise<T> {
  const r = await apiFetch(url, { signal, timeoutMs: LABELS_TIMEOUT_MS });
  if (!r.ok) throw new Error(`HTTP ${r.status}`);
  return (await r.json()).data as T;
}

/**
 * Whether a row carrying `label` / `source` still belongs in a list filtered by
 * `filters`. Mirrors the server: "unlabeled" selects every row without one of
 * the three decisions (LabelFilterUnlabeled in internal/database).
 */
export function rowMatchesFilters(
  row: { label: string; label_source: string },
  filters: Pick<LabelFilters, 'label' | 'source'>
): boolean {
  if (filters.label === 'unlabeled') {
    if (row.label === 'true_dup' || row.label === 'not_dup' || row.label === 'unsure') return false;
  } else if (filters.label && row.label !== filters.label) {
    return false;
  }
  return !filters.source || row.label_source === filters.source;
}

function postOverride(candidateId: number, label: string): Promise<Response> {
  return apiFetch(`${API_BASE}/dedup/labels/${candidateId}/override`, {
    method: 'POST',
    body: JSON.stringify({ label, reason: 'ui_override' }),
    // A POST that never answers would hold the row in `pending` (its controls
    // disabled) forever.
    timeoutMs: LABELS_TIMEOUT_MS,
  });
}

export interface DupesLabels {
  filters: LabelFilters;
  /** The band text as typed; `filters.band` is its settled twin. */
  bandInput: string;
  setBandInput: (value: string) => void;
  setLabelFilter: (value: string) => void;
  setSourceFilter: (value: string) => void;
  /**
   * Replace ALL filters with exactly this one (clearing the rest), so the rows
   * listed are the rows the clicked count counted. `{}` clears every filter.
   */
  showOnly: (patch: Partial<Pick<LabelFilters, 'label' | 'source'>>) => void;

  stats: Load<LabelStats>;
  reloadStats: () => void;

  status: ListStatus;
  error: string | null;
  rows: LabeledExample[];
  total: number;
  offset: number;
  setOffset: (n: number) => void;
  reload: () => void;

  suspiciousStatus: ListStatus;
  suspiciousError: string | null;
  suspiciousRows: SuspiciousExample[];
  /** The whole queue's size; `suspiciousRows` is one page of it. */
  suspiciousTotal: number;
  reloadSuspicious: () => void;

  /** The last failed override on THIS sub-view, or null. */
  actionError: string | null;
  /** Candidates with an override POST in flight (their controls are disabled). */
  pending: ReadonlySet<number>;
  override: (candidateId: number, label: string) => Promise<void>;
  overrideSuspicious: (candidateId: number, label: string) => Promise<void>;
}

export function useDupesLabels(view: DupesSubView): DupesLabels {
  const showLabels = view === 'labels';
  const showSuspicious = view === 'suspicious';
  const showStats = view !== 'queue';

  const [labelFilter, setLabelFilterState] = useState('');
  const [sourceFilter, setSourceFilterState] = useState('');
  const [bandInput, setBandInput] = useState('');
  const debouncedBand = useDebouncedSearch(bandInput, BAND_DEBOUNCE_MS);
  // An emptied box means no band NOW: the debounced twin follows a render later,
  // and using it here would send one request with the old band that the next
  // render cancels (clicking a count while a band is typed).
  const band = bandInput === '' ? '' : debouncedBand;

  // The page offset is only valid for the filters it was set under; a different
  // filter set means page one. Deriving that (instead of resetting in an effect)
  // means a filter change is one fetch, not a fetch at the stale offset followed
  // by a second one at zero.
  const filterKey = `${labelFilter}|${sourceFilter}|${band}`;
  const [page, setPage] = useState({ key: filterKey, offset: 0 });
  const offset = page.key === filterKey ? page.offset : 0;
  const setOffset = useCallback((n: number) => setPage({ key: filterKey, offset: n }), [filterKey]);

  const [statsLoad, setStatsLoad] = useState<Load<LabelStats>>({ state: 'loading' });
  const [listResult, setListResult] = useState<Keyed<LabelPage> | null>(null);
  const [suspResult, setSuspResult] = useState<Keyed<SuspiciousPage> | null>(null);
  // The same two results, held where an override that resolves later can read the
  // LATEST value synchronously. Two overrides resolving in the same tick must
  // each build on the other's result; a snapshot synced in a passive effect (or a
  // render-time value) would hand the second one the page as it was before the
  // first, and the first row would come back. Every write goes through
  // commitList / commitSusp so the ref and the state cannot diverge.
  const listRef = useRef<Keyed<LabelPage> | null>(null);
  const suspRef = useRef<Keyed<SuspiciousPage> | null>(null);
  const commitList = useCallback((next: Keyed<LabelPage>) => {
    listRef.current = next;
    setListResult(next);
  }, []);
  const commitSusp = useCallback((next: Keyed<SuspiciousPage>) => {
    suspRef.current = next;
    setSuspResult(next);
  }, []);
  const [actionErr, setActionErr] = useState<{ view: DupesSubView; message: string } | null>(null);
  const [pending, setPending] = useState<ReadonlySet<number>>(() => new Set());
  const [statsToken, setStatsToken] = useState(0);
  const [listToken, setListToken] = useState(0);
  const [suspToken, setSuspToken] = useState(0);

  // Everything that selects WHICH page is wanted, plus the retry token.
  const listKey = `${labelFilter}|${sourceFilter}|${band}|${offset}|${listToken}`;
  const listLoad = current(listResult, listKey);
  const suspKey = String(suspToken);
  const suspLoad = current(suspResult, suspKey);

  // What an override that finishes later needs to know about the screen it
  // finishes on: which page it is (these only change by user events, never
  // between two overrides resolving). Synced in an effect, not during render.
  const latest = useRef({ labelFilter, sourceFilter, listKey, suspKey, filterKey, offset });
  useEffect(() => {
    latest.current = { labelFilter, sourceFilter, listKey, suspKey, filterKey, offset };
  });
  const inFlight = useRef(new Set<number>());

  useEffect(() => {
    if (!showStats) return;
    const ctl = new AbortController();
    getData<LabelStats>(`${API_BASE}/dedup/labels/stats`, ctl.signal)
      .then((value) => setStatsLoad({ state: 'ready', value }))
      .catch((e: unknown) => {
        if (isAbortError(e)) return;
        setStatsLoad({ state: 'error', message: errorMessage(e, 'Failed to load label counts') });
      });
    return () => ctl.abort();
  }, [showStats, statsToken]);

  useEffect(() => {
    if (!showLabels) return;
    const ctl = new AbortController();
    const params = new URLSearchParams({ limit: String(LABELS_PAGE), offset: String(offset) });
    if (labelFilter) params.set('label', labelFilter);
    if (sourceFilter) params.set('label_source', sourceFilter);
    if (band) params.set('band', band);
    getData<{ labels?: LabeledExample[]; total?: number }>(
      `${API_BASE}/dedup/labels?${params}`,
      ctl.signal
    )
      .then((d) =>
        commitList({
          key: listKey,
          load: { state: 'ready', value: { rows: d.labels ?? [], total: d.total ?? 0 } },
        })
      )
      .catch((e: unknown) => {
        if (isAbortError(e)) return;
        commitList({
          key: listKey,
          load: { state: 'error', message: errorMessage(e, 'Failed to load labels') },
        });
      });
    return () => ctl.abort();
  }, [showLabels, labelFilter, sourceFilter, band, offset, listKey, commitList]);

  useEffect(() => {
    if (!showSuspicious) return;
    const ctl = new AbortController();
    getData<{ labels?: SuspiciousExample[]; total?: number }>(
      `${API_BASE}/dedup/labels/suspicious?limit=${LABELS_PAGE}`,
      ctl.signal
    )
      .then((d) =>
        commitSusp({
          key: suspKey,
          load: {
            state: 'ready',
            value: { rows: d.labels ?? [], total: d.total ?? d.labels?.length ?? 0 },
          },
        })
      )
      .catch((e: unknown) => {
        if (isAbortError(e)) return;
        commitSusp({
          key: suspKey,
          load: { state: 'error', message: errorMessage(e, 'Failed to load suspicious labels') },
        });
      });
    return () => ctl.abort();
  }, [showSuspicious, suspKey, commitSusp]);

  // POSTs the EXISTING override route (stamping label_source=human). Resolves
  // with the server's stamped label, or throws; the callers decide what the row
  // does next.
  //
  // Deliberately NOT tied to an AbortController: it is a write, and abandoning
  // the response of a write that has already been sent would leave the screen
  // not knowing whether it landed. It finishes even if the lane is left, and the
  // hook lives in the workspace so the result still has somewhere to land.
  const runOverride = useCallback(
    async (
      candidateId: number,
      failView: DupesSubView,
      post: () => Promise<Response>,
      apply: (d: { label?: string; label_source?: string }) => void
    ) => {
      if (inFlight.current.has(candidateId)) return;
      inFlight.current.add(candidateId);
      setPending(new Set(inFlight.current));
      setActionErr(null);
      try {
        const r = await post();
        if (!r.ok) throw new Error(`HTTP ${r.status}`);
        apply(((await r.json()).data ?? {}) as { label?: string; label_source?: string });
        setStatsToken((t) => t + 1);
      } catch (e) {
        setActionErr({
          view: failView,
          message:
            e instanceof ApiTimeoutError
              ? 'The override did not answer in time. It may have been saved; reload to check.'
              : errorMessage(e, 'Override failed'),
        });
      } finally {
        inFlight.current.delete(candidateId);
        setPending(new Set(inFlight.current));
      }
    },
    []
  );

  const override = useCallback(
    (candidateId: number, label: string) =>
      runOverride(
        candidateId,
        'labels',
        () => postOverride(candidateId, label),
        (d) => {
          const {
            labelFilter: lf,
            sourceFilter: sf,
            listKey: key,
            filterKey: fk,
            offset: at,
          } = latest.current;
          const prev = listRef.current;
          // Only the page on screen, and only if the row is on it: a result for
          // another page is refetched anyway, and its total is not ours to edit.
          if (!prev || prev.key !== key || prev.load.state !== 'ready') return;
          if (!prev.load.value.rows.some((row) => row.candidate_id === candidateId)) return;
          const stamped = { label: d.label || label, label_source: d.label_source || 'human' };
          const stays = rowMatchesFilters(stamped, { label: lf, source: sf });
          const rows = stays
            ? prev.load.value.rows.map((row) =>
                row.candidate_id === candidateId
                  ? { ...row, ...stamped, label_reason: 'ui_override' }
                  : row
              )
            : prev.load.value.rows.filter((row) => row.candidate_id !== candidateId);
          const total = stays ? prev.load.value.total : Math.max(0, prev.load.value.total - 1);
          commitList({ key: prev.key, load: { state: 'ready', value: { rows, total } } });
          if (rows.length === 0 && total > 0) {
            if (at > 0 && at >= total) {
              // That was the last row of the last page: step back to the new
              // last page instead of fetching one that is past the end.
              setPage({ key: fk, offset: Math.floor((total - 1) / LABELS_PAGE) * LABELS_PAGE });
            } else {
              // The page emptied but the filter still has rows behind it.
              setListToken((t) => t + 1);
            }
          }
        }
      ),
    [runOverride, commitList]
  );

  const overrideSuspicious = useCallback(
    (candidateId: number, label: string) =>
      runOverride(
        candidateId,
        'suspicious',
        () => postOverride(candidateId, label),
        () => {
          const prev = suspRef.current;
          if (!prev || prev.key !== latest.current.suspKey || prev.load.state !== 'ready') return;
          if (!prev.load.value.rows.some((row) => row.candidate_id === candidateId)) return;
          const rows = prev.load.value.rows.filter((row) => row.candidate_id !== candidateId);
          const total = Math.max(0, prev.load.value.total - 1);
          commitSusp({ key: prev.key, load: { state: 'ready', value: { rows, total } } });
          // The queue shows one page of a longer one: refill it instead of
          // announcing "nothing suspicious" over rows that are still there.
          if (rows.length === 0 && total > 0) setSuspToken((t) => t + 1);
        }
      ),
    [runOverride, commitSusp]
  );

  const showOnly = useCallback((patch: Partial<Pick<LabelFilters, 'label' | 'source'>>) => {
    setLabelFilterState(patch.label ?? '');
    setSourceFilterState(patch.source ?? '');
    setBandInput('');
  }, []);

  return {
    filters: { label: labelFilter, source: sourceFilter, band },
    bandInput,
    setBandInput,
    setLabelFilter: setLabelFilterState,
    setSourceFilter: setSourceFilterState,
    showOnly,
    stats: statsLoad,
    reloadStats: () => {
      setStatsLoad({ state: 'loading' });
      setStatsToken((t) => t + 1);
    },
    status: statusOf(listLoad, (v) => v.rows.length),
    error: listLoad.state === 'error' ? listLoad.message : null,
    rows: listLoad.state === 'ready' ? listLoad.value.rows : [],
    total: listLoad.state === 'ready' ? listLoad.value.total : 0,
    offset,
    setOffset,
    reload: () => setListToken((t) => t + 1),
    suspiciousStatus: statusOf(suspLoad, (v) => v.rows.length),
    suspiciousError: suspLoad.state === 'error' ? suspLoad.message : null,
    suspiciousRows: suspLoad.state === 'ready' ? suspLoad.value.rows : [],
    suspiciousTotal: suspLoad.state === 'ready' ? suspLoad.value.total : 0,
    reloadSuspicious: () => setSuspToken((t) => t + 1),
    actionError: actionErr && actionErr.view === view ? actionErr.message : null,
    pending,
    override,
    overrideSuspicious,
  };
}
