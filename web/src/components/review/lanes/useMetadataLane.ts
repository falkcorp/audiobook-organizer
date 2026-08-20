// file: web/src/components/review/lanes/useMetadataLane.ts
// version: 1.0.0
// guid: 7c4e1a90-3b58-4d26-9a07-1e5a8b2c4f70
// last-edited: 2026-08-20
//
// The metadata lane's data layer, LIFTED out of MetadataReviewDialog.
//
// Same discipline as spine/rowState.ts: this is a move, not a rewrite. The
// dialog accumulated 27 state hooks, a filter chain, a grouping pass, a
// client-side paginator and a debounced apply pipeline, and every one of those
// encodes a decision somebody made for a reason. Retyping them from a reading is
// how a port loses the reasons.
//
// WHY ONE HOOK RATHER THAN STATE PER COMPONENT
//
// The derivations form a chain, and every link depends on both the filters and
// `rowStates`:
//
//   filters -> preGroupFiltered -> multiBookIds -> filteredResults
//           -> pageResults -> multiGroups + { highConfidenceIds, ... }
//
// Split that across a component boundary and the halves either recompute the
// chain or prop-drill it. So the chain stays here and callers get slices: the
// rail takes `pageResults`, the spine takes `spineCtx`, the action bar takes the
// id sets. Nobody downstream re-derives anything.
//
// WHAT IS DELIBERATELY NOT HERE
//
// `hasChangesRef` and `handleClose` do not survive the lift. They exist because
// a dialog closes and has one moment to tell the library to refresh; a route
// does not close, so there is no such moment. The workspace refreshes the
// library when an apply operation actually finishes instead -- strictly more
// accurate, since the dialog's version fired on close whether or not the
// background op had done anything yet. docs/port-inventory.md records this as a
// deliberate drop rather than leaving the row to rot.

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import type { CandidateResult, MetadataCandidate } from '../../../services/api';
import * as api from '../../../services/api';
import { isAuthRedirectError } from '../../../utils/apiFetch';
import { STORAGE_KEYS } from '../../../lib/storageKeys';
import type { CandidateGroup, SpineContext } from '../spine/CompareSpine';
import type { RowState } from '../spine/rowState';
import type { MetadataAction } from '../reviewActions';

/** Stable identity for "no un-groupings on this page" -- see `ungroupedIds`. */
const EMPTY_IDS: ReadonlySet<string> = new Set<string>();

export type Toast = (
  message: string,
  severity?: 'success' | 'error' | 'warning' | 'info',
  action?: { label: string; onClick: () => void }
) => void;

// ---------------------------------------------------------------------------
// Persisted preferences
//
// All three loaders are lifted verbatim. `loadReviewPageSize` in particular is
// load-bearing and reads like paranoia: it CLAMPS a stored value and rewrites
// the correction. The history is that 250 was once an offered option, picking it
// froze the dialog hard enough that the size control itself could not be
// reached, and the control lives inside the dialog -- so the only escape was
// clearing localStorage by hand. Clamping on read is what makes that
// self-healing for anyone who still has the bad value stored.
// ---------------------------------------------------------------------------

/** Review rows are heavy. Deliberately NOT the activity log's 250/500 list. */
export const PAGE_SIZE_OPTIONS = [25, 50, 100];

/** Largest size a stored preference may restore. See the note above. */
export const MAX_REVIEW_PAGE_SIZE = 50;

/**
 * The "Strict review" preset: three filters that were always being set together
 * by hand. 190 is above 100 on purpose -- candidate scores are sums that
 * routinely exceed 100%, so 190 means "several strong signals agree".
 */
export const STRICT_PRESET = {
  hideSkipped: true,
  hideMultiBook: true,
  confidenceThreshold: 190,
} as const;

/** Default min-confidence when the preset is OFF. */
export const DEFAULT_CONFIDENCE = 85;

export function loadLanguageFilter(): boolean {
  if (typeof window === 'undefined') return true;
  const raw = window.localStorage.getItem(STORAGE_KEYS.METADATA_REVIEW_LANGUAGE_FILTER);
  return raw === null ? true : raw === 'true';
}

export function saveLanguageFilter(on: boolean): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_LANGUAGE_FILTER, String(on));
  } catch {
    // Private-mode / quota failure: the filter still applies this session.
  }
}

export function loadStrictPreset(): boolean {
  if (typeof window === 'undefined') return false;
  return window.localStorage.getItem(STORAGE_KEYS.METADATA_REVIEW_STRICT_PRESET) === 'true';
}

export function saveStrictPreset(on: boolean): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_STRICT_PRESET, String(on));
  } catch {
    // Private-mode / quota failure: the preset still applies this session.
  }
}

export function loadReviewPageSize(): number {
  if (typeof window === 'undefined') return 25;
  const raw = window.localStorage.getItem(STORAGE_KEYS.METADATA_REVIEW_PAGE_SIZE);
  if (raw === null) return 25;

  const n = Number(raw);
  if (!Number.isFinite(n) || n <= 0) return 25;
  if (PAGE_SIZE_OPTIONS.includes(n)) return n;

  // Out of range or no longer offered. Persist the correction so the bad value
  // is gone for good rather than being re-clamped on every open.
  const safe = Math.min(n, MAX_REVIEW_PAGE_SIZE);
  const corrected = PAGE_SIZE_OPTIONS.includes(safe) ? safe : MAX_REVIEW_PAGE_SIZE;
  try {
    window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_PAGE_SIZE, String(corrected));
  } catch {
    // Private-mode / quota failure: the clamp still applies this session.
  }
  return corrected;
}

export function saveReviewPageSize(size: number): void {
  if (typeof window === 'undefined') return;
  try {
    window.localStorage.setItem(STORAGE_KEYS.METADATA_REVIEW_PAGE_SIZE, String(size));
  } catch {
    // Private-mode / quota failure: non-fatal.
  }
}

/**
 * Normalizes a language string to an ISO 639-1 code for comparison. Mirrors the
 * server's `metadataLanguageTag`: book and candidate can come from different
 * provider APIs that spell the same language three different ways.
 *
 * Unknown languages fall through lowercased so equality still works.
 */
export function normalizeLanguage(lang: string | undefined | null): string {
  if (!lang) return '';
  const s = lang.trim().toLowerCase();
  if (!s) return '';
  const canonical: Record<string, string> = {
    english: 'en',
    eng: 'en',
    spanish: 'es',
    spa: 'es',
    french: 'fr',
    fre: 'fr',
    fra: 'fr',
    german: 'de',
    ger: 'de',
    deu: 'de',
    italian: 'it',
    ita: 'it',
    japanese: 'ja',
    jpn: 'ja',
    chinese: 'zh',
    chi: 'zh',
    zho: 'zh',
    mandarin: 'zh',
    portuguese: 'pt',
    por: 'pt',
    russian: 'ru',
    rus: 'ru',
    dutch: 'nl',
    nld: 'nl',
    korean: 'ko',
    kor: 'ko',
    arabic: 'ar',
    ara: 'ar',
  };
  if (canonical[s]) return canonical[s];
  if (s.length === 2) return s;
  return s;
}

/**
 * Group key for "these books were assigned the same candidate".
 * Priority: asin (most specific) -> isbn -> source+title+author.
 */
export function candidateKey(c: MetadataCandidate): string {
  if (c.asin) return `asin:${c.asin}`;
  if (c.isbn) return `isbn:${c.isbn}`;
  return `${c.source}:${c.title.trim().toLowerCase()}:${c.author.trim().toLowerCase()}`;
}

// ---------------------------------------------------------------------------
// Filters
// ---------------------------------------------------------------------------

/**
 * The eight switches, the provider filter, the title regex and the threshold,
 * as one object.
 *
 * Grouped rather than kept as eleven `useState`s because they move together:
 * the strict preset sets three at once, and the "reset to page 1" effect wants
 * one dependency rather than eleven. The dialog had both problems.
 */
export interface MetadataFilters {
  sourceFilter: string | null;
  confidenceThreshold: number;
  titleFilter: string;
  hideApplied: boolean;
  hideRejected: boolean;
  hideSkipped: boolean;
  hideNoMatch: boolean;
  /**
   * Hides any book that shares a match with another book, AND takes those books
   * out of Apply Selected. The second half is behaviour, not description --
   * see the deselect effect below.
   */
  hideMultiBook: boolean;
  matchLanguage: boolean;
  onlyWithTranscription: boolean;
  /** NOT the same as `onlyWithTranscription`: this one means the score was boosted by it. */
  onlyTranscriptionMatched: boolean;
}

function initialFilters(): MetadataFilters {
  const strict = loadStrictPreset();
  return {
    sourceFilter: null,
    confidenceThreshold: strict ? STRICT_PRESET.confidenceThreshold : DEFAULT_CONFIDENCE,
    titleFilter: '',
    hideApplied: true,
    hideRejected: true,
    hideSkipped: strict && STRICT_PRESET.hideSkipped,
    hideNoMatch: true,
    hideMultiBook: strict && STRICT_PRESET.hideMultiBook,
    matchLanguage: loadLanguageFilter(),
    onlyWithTranscription: false,
    onlyTranscriptionMatched: false,
  };
}

export interface MetadataLane {
  loading: boolean;
  /** Every cached row the server returned, unfiltered. */
  results: CandidateResult[];
  /** After filters, before pagination. */
  filteredResults: CandidateResult[];
  /** The current page. */
  pageResults: CandidateResult[];
  /** Multi-book groups on the current page. Singletons render as rows. */
  groups: CandidateGroup[];
  /** Book ids belonging to a group on this page -- excluded from `rows`. */
  groupedBookIds: Set<string>;
  /** Rows to hand the spine: the page minus anything rendered as a group. */
  rows: CandidateResult[];

  sourceCounts: Record<string, number>;
  summary: { matched: number; no_match: number; errors: number; total: number };

  page: number;
  totalPages: number;
  pageSize: number;
  setPage: (p: number) => void;
  setPageSize: (n: number) => void;

  filters: MetadataFilters;
  setFilters: (patch: Partial<MetadataFilters>) => void;
  strictPreset: boolean;
  setStrictPreset: (on: boolean) => void;

  selectedIds: Set<string>;
  applying: boolean;

  /** Matched, above threshold, has a narrator, still undecided. */
  highConfidenceIds: string[];
  /** Every undecided matched row on this page. */
  allVisiblePendingIds: string[];

  previewCover: string | null;
  setPreviewCover: (url: string | null) => void;

  /** Satisfies the spine's contract directly -- see the note on SpineContext. */
  spineCtx: SpineContext;
  dispatch: (action: MetadataAction) => void;
  refresh: () => void;
}

/**
 * @param active  Whether to fetch. The workspace passes `lane === 'metadata'`
 *                so switching lanes does not keep three fetches in flight.
 */
export function useMetadataLane(toast: Toast, active = true): MetadataLane {
  const [results, setResults] = useState<CandidateResult[]>([]);
  const [loading, setLoading] = useState(true);
  const [rowStates, setRowStates] = useState<Map<string, RowState>>(new Map());
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [filters, setFiltersState] = useState<MetadataFilters>(initialFilters);
  const [strictPreset, setStrictPresetState] = useState(loadStrictPreset);
  const [requestedPage, setPage] = useState(1);
  const [pageSize, setPageSizeState] = useState<number>(loadReviewPageSize);
  const [applying, setApplying] = useState(false);
  const [previewCover, setPreviewCover] = useState<string | null>(null);
  const [expandedId, setExpandedId] = useState<string | null>(null);
  // Un-groupings are per-page: a group is a set of books on the CURRENT page
  // that share a candidate, so navigating away makes them meaningless. Carrying
  // the page they belong to makes that automatic -- the alternative, an effect
  // that empties the set whenever `page` changes, costs a second render pass on
  // every page turn and briefly renders the new page with the old page's
  // un-groupings still applied.
  const [ungrouped, setUngrouped] = useState<{ page: number; ids: Set<string> }>({
    page: 1,
    ids: new Set(),
  });
  // Keyed on `requestedPage`, NOT on the clamped `page` below: `page` is derived
  // from totalPages <- filteredResults <- multiBookIds <- ungroupedIds, so
  // keying on it would close a cycle.
  const ungroupedIds = useMemo(
    () => (ungrouped.page === requestedPage ? ungrouped.ids : EMPTY_IDS),
    [ungrouped, requestedPage]
  );
  const [summary, setSummary] = useState({ matched: 0, no_match: 0, errors: 0, total: 0 });
  const [refreshKey, setRefreshKey] = useState(0);

  // Discards out-of-order responses. Without it a slow page-1 fetch that
  // resolves after page 2 overwrites page 2's rows, and the user is looking at
  // the wrong page with the right page number.
  const fetchIdRef = useRef(0);

  // Guards the deferred refresh below: the apply op outlives the component if
  // the user navigates away mid-apply, and setting state then warns.
  const aliveRef = useRef(true);
  useEffect(() => {
    aliveRef.current = true;
    return () => {
      aliveRef.current = false;
    };
  }, []);

  const refresh = useCallback(() => setRefreshKey((k) => k + 1), []);

  // Fetch the entire cached review set once; paginate and filter client-side.
  // `limit=0` tells the server "return all rows".
  useEffect(() => {
    if (!active) return;
    setLoading(true);
    const fetchId = ++fetchIdRef.current;
    api
      .getCachedReviewResults(0, 0)
      .then((data) => {
        if (fetchId !== fetchIdRef.current) return; // stale -- a newer fetch is in flight
        const allResults = data.results || [];

        // Seed row state from what the server already knows, without clobbering
        // decisions the user made in this session.
        const seed = new Map<string, RowState>();
        for (const r of allResults) {
          if (r.status === 'applied') seed.set(r.book.id, 'applied');
          else if (r.status === 'no_match') seed.set(r.book.id, 'rejected');
        }
        setRowStates((prev) => {
          const merged = new Map(prev);
          seed.forEach((v, k) => {
            if (!merged.has(k)) merged.set(k, v);
          });
          return merged;
        });

        setResults(allResults);
        const tc = data.total_count ?? allResults.length;
        setSummary({
          matched: data.matched ?? allResults.filter((r) => r.status === 'matched').length,
          no_match: data.no_match ?? allResults.filter((r) => r.status === 'no_match').length,
          errors: data.errors ?? 0,
          total: tc,
        });
        setLoading(false);
      })
      .catch(() => {
        if (fetchId !== fetchIdRef.current) return;
        setLoading(false);
      });
  }, [active, refreshKey]);

  const setFilters = useCallback((patch: Partial<MetadataFilters>) => {
    setFiltersState((prev) => {
      const next = { ...prev, ...patch };
      if (patch.matchLanguage !== undefined) saveLanguageFilter(patch.matchLanguage);
      return next;
    });
    setPage(1); // a filter change always returns to the first page of results
  }, []);

  const setStrictPreset = useCallback((on: boolean) => {
    setStrictPresetState(on);
    saveStrictPreset(on);
    setFiltersState((prev) => ({
      ...prev,
      hideSkipped: on ? STRICT_PRESET.hideSkipped : false,
      hideMultiBook: on ? STRICT_PRESET.hideMultiBook : false,
      confidenceThreshold: on ? STRICT_PRESET.confidenceThreshold : DEFAULT_CONFIDENCE,
    }));
    setPage(1);
  }, []);

  const setPageSize = useCallback((n: number) => {
    setPageSizeState(n);
    saveReviewPageSize(n);
    setPage(1);
  }, []);

  // --- the derivation chain -------------------------------------------------

  const sourceCounts = useMemo(
    () =>
      results.reduce<Record<string, number>>((acc, r) => {
        if (r.candidate?.source) acc[r.candidate.source] = (acc[r.candidate.source] || 0) + 1;
        return acc;
      }, {}),
    [results]
  );

  const titleRegex = useMemo(() => {
    if (!filters.titleFilter) return null;
    try {
      return new RegExp(filters.titleFilter, 'i');
    } catch {
      // A half-typed regex is not an error state -- it just does not filter yet.
      return null;
    }
  }, [filters.titleFilter]);

  const preGroupFiltered = useMemo(
    () =>
      results
        .filter((r) => !titleRegex || titleRegex.test(r.book.title || ''))
        .filter((r) => !filters.sourceFilter || r.candidate?.source === filters.sourceFilter)
        .filter(
          (r) =>
            (r.status === 'matched' &&
              r.candidate &&
              r.candidate.score * 100 >= filters.confidenceThreshold) ||
            r.status !== 'matched'
        )
        .filter((r) => !filters.hideApplied || rowStates.get(r.book.id) !== 'applied')
        .filter((r) => !filters.hideRejected || rowStates.get(r.book.id) !== 'rejected')
        .filter((r) => !filters.hideSkipped || rowStates.get(r.book.id) !== 'skipped')
        .filter((r) => !filters.hideNoMatch || (r.status !== 'no_match' && r.status !== 'error'))
        .filter((r) => {
          // An unknown language on EITHER side is a no-op, not a hide: a book
          // with no language set must still be offered its candidates.
          if (!filters.matchLanguage) return true;
          if (!r.candidate) return true;
          const bookLang = normalizeLanguage(r.book.language);
          const candLang = normalizeLanguage(r.candidate.language);
          if (!bookLang || !candLang) return true;
          return bookLang === candLang;
        })
        .filter((r) => !filters.onlyWithTranscription || !!r.book.transcribed_title)
        .filter((r) => !filters.onlyTranscriptionMatched || !!r.candidate?.transcription_boosted),
    [results, rowStates, titleRegex, filters]
  );

  // Book ids sharing a candidate with at least one other book.
  //
  // Computed over the WHOLE filtered set, not the current page -- unlike the
  // per-page `groups` below. Two files of one book landing on opposite sides of
  // a page boundary each look like a singleton to a per-page pass and would
  // survive the hide, which is the exact case this toggle exists to remove.
  const multiBookIds = useMemo(() => {
    if (!filters.hideMultiBook) return new Set<string>();
    const byKey = new Map<string, string[]>();
    for (const r of preGroupFiltered) {
      if (!r.candidate || r.status !== 'matched' || ungroupedIds.has(r.book.id)) continue;
      const key = candidateKey(r.candidate);
      const ids = byKey.get(key);
      if (ids) ids.push(r.book.id);
      else byKey.set(key, [r.book.id]);
    }
    const out = new Set<string>();
    for (const ids of byKey.values()) {
      if (ids.length > 1) ids.forEach((id) => out.add(id));
    }
    return out;
  }, [filters.hideMultiBook, preGroupFiltered, ungroupedIds]);

  const filteredResults = useMemo(
    () =>
      filters.hideMultiBook
        ? preGroupFiltered.filter((r) => !multiBookIds.has(r.book.id))
        : preGroupFiltered,
    [filters.hideMultiBook, preGroupFiltered, multiBookIds]
  );

  // Deselect anything the multi-book filter has hidden. `selectedIds` is built
  // by hand and does not shrink when a filter hides its members, so without this
  // "Apply Selected" would still apply a grouped book that was ticked before the
  // toggle went on -- the precise opposite of what the toggle is for -- and the
  // button's count would disagree with what it does.
  const multiBookKey = useMemo(() => [...multiBookIds].sort().join(','), [multiBookIds]);
  useEffect(() => {
    if (!filters.hideMultiBook || multiBookIds.size === 0) return;
    setSelectedIds((prev) => {
      const next = new Set([...prev].filter((id) => !multiBookIds.has(id)));
      return next.size === prev.size ? prev : next;
    });
    // multiBookIds is rebuilt each render; key the effect on its contents.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [filters.hideMultiBook, multiBookKey]);

  const totalPages = Math.max(1, Math.ceil(filteredResults.length / pageSize));

  // Clamp rather than auto-advance: filters can shrink the set below the current
  // page index, and with client-side pagination there is no empty page to skip
  // past.
  //
  // DERIVED, not synced in an effect. An effect would render one frame at the
  // out-of-range page before correcting it -- `pageResults` would slice past the
  // end and the spine would flash empty -- and it would cost a second render
  // pass every time a filter changed. `requestedPage` is what the reviewer
  // asked for; `page` is what is actually reachable.
  const page = Math.min(requestedPage, totalPages);

  const pageResults = useMemo(() => {
    const start = (page - 1) * pageSize;
    return filteredResults.slice(start, start + pageSize);
  }, [filteredResults, page, pageSize]);

  const { groups, groupedBookIds } = useMemo(() => {
    const groupMap = new Map<string, CandidateGroup>();
    for (const r of pageResults) {
      if (!r.candidate || r.status !== 'matched' || ungroupedIds.has(r.book.id)) continue;
      const key = candidateKey(r.candidate);
      if (!groupMap.has(key)) groupMap.set(key, { key, candidate: r.candidate, results: [] });
      groupMap.get(key)!.results.push(r);
    }
    // Only multi-book groups are groups; singletons fall through to rows.
    const multi: CandidateGroup[] = [];
    const ids = new Set<string>();
    for (const g of groupMap.values()) {
      if (g.results.length > 1) {
        multi.push(g);
        g.results.forEach((r) => ids.add(r.book.id));
      }
    }
    return { groups: multi, groupedBookIds: ids };
  }, [pageResults, ungroupedIds]);

  const rows = useMemo(
    () => pageResults.filter((r) => !groupedBookIds.has(r.book.id)),
    [pageResults, groupedBookIds]
  );

  const undecided = useCallback(
    (id: string) => !['applied', 'skipped', 'rejected'].includes(rowStates.get(id) || ''),
    [rowStates]
  );

  const highConfidenceIds = useMemo(
    () =>
      pageResults
        .filter(
          (r) =>
            r.status === 'matched' &&
            r.candidate &&
            r.candidate.score * 100 >= filters.confidenceThreshold &&
            r.candidate.narrator &&
            undecided(r.book.id)
        )
        .map((r) => r.book.id),
    [pageResults, filters.confidenceThreshold, undecided]
  );

  const allVisiblePendingIds = useMemo(
    () =>
      pageResults
        .filter((r) => r.status === 'matched' && r.candidate && undecided(r.book.id))
        .map((r) => r.book.id),
    [pageResults, undecided]
  );

  // --- the apply pipeline ---------------------------------------------------

  const applyQueueRef = useRef<string[]>([]);
  const applyTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(
    () => () => {
      if (applyTimerRef.current) clearTimeout(applyTimerRef.current);
    },
    []
  );

  // An auth bounce during a batch apply does NOT mean nothing was applied.
  //
  // This used to assert that it did -- every row reverted to 'pending' with a
  // "nothing was applied" toast -- reasoning that a Cloudflare Access bounce
  // never reaches the origin. That holds only while the request is short.
  // Measured in production: a 250-book apply ran 2m0s, the origin returned 200,
  // 67 files were written, and the user was told nothing had happened.
  // Reverting then invites re-applying work that already succeeded. So: keep the
  // detection, report it accurately, and re-read server state rather than guess.
  const handleApplyError = useCallback(
    (err: unknown, requestedIds: string[]) => {
      refresh();
      if (isAuthRedirectError(err)) {
        toast(
          'Session expired — sign in again. Any books already applied were kept; the list has been refreshed.',
          'error'
        );
        return;
      }
      toast(`Failed to start applying ${requestedIds.length} book(s)`, 'error');
    },
    [toast, refresh]
  );

  // Dispatches the background apply and returns; the bell owns progress.
  // Re-reads the list when the op finishes rather than diffing a client guess.
  const runApplyOp = useCallback(
    async (requestedIds: string[], writeBack?: boolean): Promise<void> => {
      const dispatch = await api.batchApplyFromCache(requestedIds, writeBack);
      toast(
        `Metadata apply queued for ${requestedIds.length.toLocaleString()} book(s) — watch the bell for progress.`,
        'success'
      );
      void api
        .pollOperationV2(dispatch.op_id)
        .catch(() => undefined)
        .finally(() => {
          if (!aliveRef.current) return;
          refresh();
        });
    },
    [toast, refresh]
  );

  const flushApplyQueue = useCallback(async () => {
    const ids = [...applyQueueRef.current];
    applyQueueRef.current = [];
    if (ids.length === 0) return;
    try {
      await runApplyOp(ids);
    } catch (err) {
      handleApplyError(err, ids);
    }
  }, [runApplyOp, handleApplyError]);

  const applyOne = useCallback(
    (bookId: string) => {
      setRowStates((prev) => new Map(prev).set(bookId, 'applied'));
      applyQueueRef.current.push(bookId);
      if (applyTimerRef.current) clearTimeout(applyTimerRef.current);
      applyTimerRef.current = setTimeout(() => void flushApplyQueue(), 500);
    },
    [flushApplyQueue]
  );

  const applyMany = useCallback(
    async (bookIds: string[]) => {
      if (bookIds.length === 0) return;
      setApplying(true);
      try {
        await runApplyOp(bookIds);
        // The refresh re-derives every row from the server, so clearing the
        // whole selection is safe: anything that did not apply comes back
        // pending and can be re-selected.
        setSelectedIds(new Set());
      } catch (err) {
        handleApplyError(err, bookIds);
      } finally {
        setApplying(false);
      }
    },
    [runApplyOp, handleApplyError]
  );

  const reject = useCallback(
    async (bookId: string) => {
      try {
        await api.markNoMatch(bookId);
        setRowStates((prev) => new Map(prev).set(bookId, 'rejected'));
        toast('Candidate rejected — will be excluded from future fetches', 'info', {
          label: 'Undo',
          onClick: () => {
            void (async () => {
              try {
                await api.clearMetadataNoMatch(bookId);
                setRowStates((prev) => new Map(prev).set(bookId, 'pending'));
                toast('Rejection undone', 'success');
              } catch {
                toast('Failed to undo rejection', 'error');
              }
            })();
          },
        });
      } catch {
        toast('Failed to reject', 'error');
      }
    },
    [toast]
  );

  const unreject = useCallback(
    async (bookId: string) => {
      try {
        await api.clearMetadataNoMatch(bookId);
        setRowStates((prev) => new Map(prev).set(bookId, 'pending'));
        toast('Rejection undone', 'success');
      } catch {
        toast('Failed to undo rejection', 'error');
      }
    },
    [toast]
  );

  /**
   * Every action the metadata lane supports, in one place.
   *
   * The union is exhaustive and the switch has no `default`, so an action added
   * to `MetadataAction` and not handled here is a compile error rather than a
   * button that silently does nothing.
   */
  const dispatch = useCallback(
    (action: MetadataAction) => {
      switch (action.type) {
        case 'apply':
          applyOne(action.id);
          return;
        case 'applySelected':
          void applyMany(action.ids);
          return;
        case 'reject':
          void reject(action.id);
          return;
        case 'unreject':
          void unreject(action.id);
          return;
        case 'skip':
          setRowStates((prev) => new Map(prev).set(action.id, 'skipped'));
          return;
        case 'unskip':
          setRowStates((prev) => new Map(prev).set(action.id, 'pending'));
          return;
        case 'rejectGroup':
          void (async () => {
            try {
              await Promise.all(action.ids.map((id) => api.markNoMatch(id)));
              setRowStates((prev) => {
                const next = new Map(prev);
                action.ids.forEach((id) => next.set(id, 'rejected'));
                return next;
              });
            } catch {
              toast('Failed to reject group', 'error');
            }
          })();
          return;
        case 'skipAllUnmatched':
          setRowStates((prev) => {
            const next = new Map(prev);
            results
              .filter((r) => r.status === 'no_match' || r.status === 'error')
              .forEach((r) => next.set(r.book.id, 'skipped'));
            return next;
          });
          return;
        case 'ungroup':
          setUngrouped((prev) => ({
            page: requestedPage,
            ids: new Set(prev.page === requestedPage ? prev.ids : []).add(action.id),
          }));
          return;
      }
    },
    [applyOne, applyMany, reject, unreject, results, toast, requestedPage]
  );

  const toggleSelect = useCallback((bookId: string) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(bookId)) next.delete(bookId);
      else next.add(bookId);
      return next;
    });
  }, []);

  const toggleExpand = useCallback(
    (bookId: string) => setExpandedId((prev) => (prev === bookId ? null : bookId)),
    []
  );

  const spineCtx: SpineContext = useMemo(
    () => ({
      rowState: (id) => rowStates.get(id),
      isSelected: (id) => selectedIds.has(id),
      onToggleSelect: toggleSelect,
      onPreviewCover: setPreviewCover,
      onAction: dispatch,
      expandedId,
      onToggleExpand: toggleExpand,
    }),
    [rowStates, selectedIds, toggleSelect, dispatch, expandedId, toggleExpand]
  );

  return {
    loading,
    results,
    filteredResults,
    pageResults,
    groups,
    groupedBookIds,
    rows,
    sourceCounts,
    summary,
    page,
    totalPages,
    pageSize,
    setPage,
    setPageSize,
    filters,
    setFilters,
    strictPreset,
    setStrictPreset,
    selectedIds,
    applying,
    highConfidenceIds,
    allVisiblePendingIds,
    previewCover,
    setPreviewCover,
    spineCtx,
    dispatch,
    refresh,
  };
}
