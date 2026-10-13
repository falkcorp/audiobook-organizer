// file: web/src/components/review/lanes/useDupesLabels.ts
// version: 1.0.0
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
import { apiFetch, isAbortError } from '../../../utils/apiFetch';
import { useDebouncedSearch } from '../../../hooks/useDebouncedSearch';

const API_BASE = '/api/v1';

/** Rows per page of the label list and the suspicious queue. */
export const LABELS_PAGE = 50;
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
  const r = await apiFetch(url, { signal });
  if (!r.ok) throw new Error(`HTTP ${r.status}`);
  return (await r.json()).data as T;
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
  reloadSuspicious: () => void;

  /** The last failed override, or null. Cleared when the next one starts. */
  actionError: string | null;
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
  const band = useDebouncedSearch(bandInput, BAND_DEBOUNCE_MS);

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
  const [suspResult, setSuspResult] = useState<Keyed<SuspiciousExample[]> | null>(null);
  const [actionError, setActionError] = useState<string | null>(null);
  const [statsToken, setStatsToken] = useState(0);
  const [listToken, setListToken] = useState(0);
  const [suspToken, setSuspToken] = useState(0);

  // Aborts an override still in flight when the lane is left. Recreated on every
  // mount so a strict-mode remount does not inherit an already-aborted signal.
  const unmountCtl = useRef<AbortController | null>(null);
  useEffect(() => {
    const ctl = new AbortController();
    unmountCtl.current = ctl;
    return () => ctl.abort();
  }, []);

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

  // Everything that selects WHICH page is wanted, plus the retry token.
  const listKey = `${labelFilter}|${sourceFilter}|${band}|${offset}|${listToken}`;
  const listLoad = current(listResult, listKey);
  const suspKey = String(suspToken);
  const suspLoad = current(suspResult, suspKey);

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
        setListResult({
          key: listKey,
          load: { state: 'ready', value: { rows: d.labels ?? [], total: d.total ?? 0 } },
        })
      )
      .catch((e: unknown) => {
        if (isAbortError(e)) return;
        setListResult({
          key: listKey,
          load: { state: 'error', message: errorMessage(e, 'Failed to load labels') },
        });
      });
    return () => ctl.abort();
  }, [showLabels, labelFilter, sourceFilter, band, offset, listKey]);

  useEffect(() => {
    if (!showSuspicious) return;
    const ctl = new AbortController();
    getData<{ labels?: SuspiciousExample[] }>(
      `${API_BASE}/dedup/labels/suspicious?limit=${LABELS_PAGE}`,
      ctl.signal
    )
      .then((d) => setSuspResult({ key: suspKey, load: { state: 'ready', value: d.labels ?? [] } }))
      .catch((e: unknown) => {
        if (isAbortError(e)) return;
        setSuspResult({
          key: suspKey,
          load: { state: 'error', message: errorMessage(e, 'Failed to load suspicious labels') },
        });
      });
    return () => ctl.abort();
  }, [showSuspicious, suspKey]);

  // POSTs the EXISTING override route (stamping label_source=human). Resolves
  // with the server's stamped label, or throws; the callers decide what the row
  // does next.
  const postOverride = useCallback(async (candidateId: number, label: string) => {
    setActionError(null);
    const r = await apiFetch(`${API_BASE}/dedup/labels/${candidateId}/override`, {
      method: 'POST',
      body: JSON.stringify({ label, reason: 'ui_override' }),
      signal: unmountCtl.current?.signal,
    });
    if (!r.ok) throw new Error(`HTTP ${r.status}`);
    return ((await r.json()).data ?? {}) as { label?: string; label_source?: string };
  }, []);

  const override = useCallback(
    async (candidateId: number, label: string) => {
      try {
        const d = await postOverride(candidateId, label);
        setListResult((cur) =>
          cur?.load.state !== 'ready'
            ? cur
            : {
                key: cur.key,
                load: {
                  state: 'ready',
                  value: {
                    ...cur.load.value,
                    rows: cur.load.value.rows.map((row) =>
                      row.candidate_id === candidateId
                        ? {
                            ...row,
                            label: d.label || label,
                            label_source: d.label_source || 'human',
                            label_reason: 'ui_override',
                          }
                        : row
                    ),
                  },
                },
              }
        );
        setStatsToken((t) => t + 1);
      } catch (e) {
        if (isAbortError(e)) return;
        setActionError(errorMessage(e, 'Override failed'));
      }
    },
    [postOverride]
  );

  const overrideSuspicious = useCallback(
    async (candidateId: number, label: string) => {
      try {
        await postOverride(candidateId, label);
        setSuspResult((cur) =>
          cur?.load.state !== 'ready'
            ? cur
            : {
                key: cur.key,
                load: {
                  state: 'ready',
                  value: cur.load.value.filter((row) => row.candidate_id !== candidateId),
                },
              }
        );
        setStatsToken((t) => t + 1);
      } catch (e) {
        if (isAbortError(e)) return;
        setActionError(errorMessage(e, 'Override failed'));
      }
    },
    [postOverride]
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
    status: statusOf(listLoad, (v) => v.rows.length),
    error: listLoad.state === 'error' ? listLoad.message : null,
    rows: listLoad.state === 'ready' ? listLoad.value.rows : [],
    total: listLoad.state === 'ready' ? listLoad.value.total : 0,
    offset,
    setOffset,
    reload: () => setListToken((t) => t + 1),
    suspiciousStatus: statusOf(suspLoad, (v) => v.length),
    suspiciousError: suspLoad.state === 'error' ? suspLoad.message : null,
    suspiciousRows: suspLoad.state === 'ready' ? suspLoad.value : [],
    reloadSuspicious: () => setSuspToken((t) => t + 1),
    actionError,
    override,
    overrideSuspicious,
  };
}
