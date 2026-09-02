// file: web/src/components/review/lanes/useDupesLane.ts
// version: 1.8.0
// guid: 5e9c1a74-0d38-4b62-9f15-6c2a8d4b7e31
// last-edited: 2026-09-01

import {
  startTransition,
  useCallback,
  useDeferredValue,
  useEffect,
  useMemo,
  useRef,
  useState,
} from 'react';
import { serverAnsweredTerm, useDebouncedSearch } from '../../../hooks/useDebouncedSearch';
import * as api from '../../../services/api';
import type { DedupBand, DedupCandidate, DedupStats } from '../../../services/api';
import type { DupesAction } from '../reviewActions';
import { dupesLane } from './dupes';
import { keepIdForMerge } from './keepDecision';
import type { Toast } from './useMetadataLane';
import { PAGE_SIZE_OPTIONS } from './useMetadataLane';

export { PAGE_SIZE_OPTIONS };

const DEFAULT_PAGE_SIZE = 50;

/**
 * How long the lane waits after the last keystroke before searching.
 *
 * The term gates a NETWORK REQUEST now, so an undebounced box would issue one
 * per character. Matches REGROUP_SEARCH_DEBOUNCE_MS: the two lanes sit side by
 * side in the same panel, and a reviewer who learns the feel of one should not
 * have to relearn it in the other.
 */
export const DUPES_SEARCH_DEBOUNCE_MS = 250;

/**
 * How many rows mount in the first, synchronous pass.
 *
 * Chosen by measurement, not by feel: the load task is ~18 ms of fixed cost
 * plus ~1.4 ms per row, so this many rows is what fits in the first pass while
 * staying under the 50 ms that the longtask API calls a long task -- which is
 * also roughly the point a user starts to feel a freeze. Raising it puts the
 * first paint back over that line; lowering it buys nothing, because the
 * remainder is already off the critical path.
 */
export const FIRST_PAINT_ROWS = 20;

/**
 * Why "merge everything matching this filter" is refused while the
 * both-unmatched triage view is active.
 *
 * The bulk-merge endpoint filters candidates; "both unmatched" is a property of
 * the two BOOKS behind a candidate, so the endpoint cannot express it. Sending
 * the rest of the filter without it would merge a strictly larger set than the
 * reviewer is looking at, and merges are the hardest operation in this system
 * to undo. Refusing is the only honest option -- silently widening a
 * destructive action is the exact failure this whole filter-parity thread
 * exists to prevent.
 */
export const MERGE_ALL_BLOCKED_REASON =
  'The bulk endpoint cannot express "both unmatched", so this would merge more pairs than you can see. Clear that filter first.';

/**
 * Refusal while a search has not settled.
 *
 * Between the keystroke and the server's answer, the rows on screen were
 * narrowed by the LOCAL pass while the server has answered only for the
 * previous term. Merging then sends the previous term -- so the reviewer sees
 * a handful of search hits, presses a button labelled "merge everything
 * matching this filter", and merges the set the filter matched BEFORE they
 * typed. The window is short and the action is irreversible, which is the
 * worst combination to leave open.
 */
export const MERGE_ALL_SEARCH_PENDING_REASON =
  'Still searching. Wait for the results to load before merging everything that matches.';

export type DedupStatusFilter = 'pending' | 'merged' | 'dismissed' | '';

export interface DupesFilters {
  /** Server-side. */
  band: DedupBand | null;
  /** Server-side. */
  status: DedupStatusFilter;
  /** Server-side, but NOT expressible to the bulk endpoint -- see above. */
  bothUnmatched: boolean;
  /** Server-side since the ?book= deep link was fixed. */
  entityId: string | null;
  /**
   * Server-side as of the `q` parameter, so "no results" now means "not in the
   * queue" rather than "none on this page", and `total` counts matches. Before
   * that, this searched only the rows already fetched: 50 of 40,251 candidates
   * in production, or 0.12% of the queue per page.
   *
   * Debounced by DUPES_SEARCH_DEBOUNCE_MS before it is sent; the raw value
   * stays here so the text field never lags the typist. A local pass over the
   * already-loaded rows covers that debounce window and then STANDS DOWN --
   * see the `filtered` derivation, where standing down is a correctness
   * requirement rather than an optimisation.
   */
  search: string;
}

/**
 * The filters this hook owns as state. `band` and `entityId` are deliberately
 * absent: the URL owns those, and they are passed in.
 */
export type LocalDupesFilters = Omit<DupesFilters, 'band' | 'entityId'>;

/**
 * The two filters the URL owns, passed in rather than mirrored into state.
 *
 * Mirroring them was a real defect, not a style choice. The panel synced
 * URL -> filters in an effect, so arriving at `?book=X` rendered once with no
 * entity filter, fired a fetch for the ENTIRE unfiltered pending set, then
 * corrected itself and fetched again. On a large library that first request is
 * the expensive one, and it is pure waste. Deriving makes the first render
 * already correct, so a deep link costs exactly one fetch.
 */
export interface DupesUrlFilters {
  band: DedupBand | null;
  entityId: string | null;
}

const NO_URL_FILTERS: DupesUrlFilters = { band: null, entityId: null };

const INITIAL_FILTERS: LocalDupesFilters = {
  status: 'pending',
  bothUnmatched: false,
  search: '',
};

export interface DupesLane {
  loading: boolean;
  error: string | null;

  /** The loaded page, after the client-side search narrows it. */
  candidates: DedupCandidate[];
  /** Server count for the current server-side filter. Not the page length. */
  total: number;

  /** 1-based, matching MetadataLane. The source was 0-based only to feed MUI's TablePagination. */
  page: number;
  totalPages: number;
  pageSize: number;
  setPage: (p: number) => void;
  setPageSize: (n: number) => void;

  filters: DupesFilters;
  /**
   * Patches only the filters this hook owns. `band` and `entityId` are not
   * accepted -- they live in the URL, and the caller changes them by navigating.
   */
  setFilters: (patch: Partial<LocalDupesFilters>) => void;
  /**
   * Pending candidates across the whole library, ignoring every filter. Shown
   * beside `total` so a narrow filter reads as "12 of 4,300" rather than "12".
   *
   * NOT a per-band breakdown. The stats endpoint groups by entity_type, layer
   * and status -- there is no band dimension in the schema -- so the source's
   * deriveBandCounts returned a hardcoded zero for every band and only the
   * total was real. Reproducing that shape here would ship a map whose name
   * promises counts it cannot contain.
   */
  pendingTotal: number;

  selectedIds: Set<number>;
  /**
   * `index` and `shiftKey` come from the row so a shift-click can extend from
   * the last row clicked, the way a file list does. Passing them is optional
   * only because the `s` shortcut has no click to extend from.
   */
  toggleSelect: (id: number, index?: number, shiftKey?: boolean) => void;
  /** Selects every row currently on screen -- which the search may have narrowed. */
  selectAllVisible: () => void;
  clearSelection: () => void;

  /** Row the j/k shortcuts act on. Index into `candidates`. */
  focusedIndex: number;
  setFocusedIndex: (i: number) => void;

  drawerCandidateId: number | null;
  setDrawerCandidateId: (id: number | null) => void;
  shortcutHelpOpen: boolean;
  setShortcutHelpOpen: (open: boolean) => void;

  /** A destructive bulk action is in flight; a second must not overlap it. */
  busy: boolean;

  /**
   * Non-null when `mergeAllFiltered` must not be offered, and why. The reason
   * is the message, so the UI never has to invent one.
   */
  mergeAllFilteredDisabledReason: string | null;

  verbs: (typeof dupesLane)['verbs'];
  dispatch: (action: DupesAction) => void;
  refresh: () => void;
}

/**
 * The duplicate-candidate lane's data layer.
 *
 * Deliberately NOT shaped like useMetadataLane, though the two sit side by side.
 * That lane fetches every cached row once and slices client-side, so its page
 * clamp can be DERIVED (`Math.min(requestedPage, totalPages)`) from an array it
 * already holds. Here `total` comes from the server, so a clamp would mean a
 * refetch and `page` stays plain state. Likewise the stale-response guard: the
 * metadata lane discards late responses by fetch id, this one aborts them, which
 * is strictly better and is kept rather than unified.
 *
 * @param active Whether to fetch and bind shortcuts. The workspace passes
 *               `lane === 'dupes'` so switching lanes neither keeps fetches in
 *               flight nor leaves a window key listener stealing `j`.
 */
export function useDupesLane(
  toast: Toast,
  active = true,
  urlFilters: DupesUrlFilters = NO_URL_FILTERS
): DupesLane {
  const [candidates, setCandidates] = useState<DedupCandidate[]>([]);
  const [total, setTotal] = useState(0);
  const [stats, setStats] = useState<DedupStats[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [localFilters, setFiltersState] = useState<LocalDupesFilters>(INITIAL_FILTERS);
  const [page, setPageState] = useState(1);
  const [pageSize, setPageSizeState] = useState(DEFAULT_PAGE_SIZE);
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set());
  const [focusedIndexState, setFocusedIndex] = useState(0);
  const [drawerCandidateId, setDrawerCandidateId] = useState<number | null>(null);
  const [shortcutHelpOpen, setShortcutHelpOpen] = useState(false);
  const [busy, setBusy] = useState(false);

  /**
   * Candidates whose decision has been dispatched but whose refetch has not
   * landed yet. Filtered out of `visible` immediately.
   *
   * This is a correctness guard, not a rendering nicety. `m` fires an async
   * merge and returns; until the refetch lands, `candidates` still holds the
   * row and it still reads `status: 'pending'`, so a second `m` at the same
   * focus index merges the SAME pair twice -- while the pair the reviewer
   * meant to decide second stays untouched and unnoticed. Merges cannot be
   * undone, so "decided twice, and one pair silently skipped" is the worst
   * outcome this lane can produce.
   *
   * Removing the row makes its id unreachable from the key handler, which
   * closes the race, advances focus to the next pending pair for free, and
   * takes the refetch off the critical path of a keyboard-speed reviewer.
   */
  const [decidedIds, setDecidedIds] = useState<Set<number>>(() => new Set());

  const filters: DupesFilters = useMemo(
    () => ({ ...localFilters, band: urlFilters.band, entityId: urlFilters.entityId }),
    [localFilters, urlFilters.band, urlFilters.entityId]
  );
  const [reloadToken, setReloadToken] = useState(0);

  // A URL filter change invalidates the page number and the selection, exactly
  // as setFilters does for the local ones. This runs DURING render rather than
  // in an effect on purpose: React re-renders immediately without committing,
  // so the fetch effect sees page 1 on its first run. An effect would fetch
  // page N, then fetch page 1 -- the bug this whole change removes, relocated.
  const urlFilterKey = `${urlFilters.band ?? ''}\u0000${urlFilters.entityId ?? ''}`;
  const [prevUrlFilterKey, setPrevUrlFilterKey] = useState(urlFilterKey);
  if (prevUrlFilterKey !== urlFilterKey) {
    setPrevUrlFilterKey(urlFilterKey);
    setPageState(1);
    setSelectedIds(new Set());
    setFocusedIndex(0);
  }

  // The settled term actually sent to the server. The raw value drives the
  // text field so typing never lags; this is what the fetch is keyed on.
  const debouncedSearch = useDebouncedSearch(filters.search, DUPES_SEARCH_DEBOUNCE_MS);

  // The term the rows currently in `candidates` were fetched with. Read by
  // `filtered` to decide whether the local pass has been superseded.
  const [appliedSearch, setAppliedSearch] = useState('');

  // A settled search term invalidates the page number for the same reason a
  // URL filter does: page 5 of the old result set is not page 5 of the new one.
  // Keyed on the DEBOUNCED term, not the raw one, so it fires once per search
  // rather than once per keystroke -- resetting the page and clearing the
  // selection on every character would fight the typist. Runs during render,
  // matching the block above: an effect would fetch page N and then fetch
  // page 1.
  const [prevSearchKey, setPrevSearchKey] = useState(debouncedSearch);
  if (prevSearchKey !== debouncedSearch) {
    setPrevSearchKey(debouncedSearch);
    setPageState(1);
    setSelectedIds(new Set());
    setFocusedIndex(0);
  }

  const abortRef = useRef<AbortController | null>(null);

  // -------------------------------------------------------------------------
  // Fetch
  // -------------------------------------------------------------------------

  useEffect(() => {
    if (!active) return;
    abortRef.current?.abort();
    const ctrl = new AbortController();
    abortRef.current = ctrl;

    setLoading(true);
    setError(null);

    const params: Parameters<typeof api.getDedupCandidates>[0] = {
      status: filters.status || undefined,
      limit: pageSize,
      offset: (page - 1) * pageSize,
      include_breakdown: true,
      include_books: true,
    };
    if (filters.band) params.band = filters.band;
    if (filters.bothUnmatched) params.both_unmatched = true;
    if (filters.entityId) params.entity_id = filters.entityId;
    if (debouncedSearch.trim()) params.q = debouncedSearch.trim();

    api
      .getDedupCandidates(params, { signal: ctrl.signal })
      .then((res) => {
        if (ctrl.signal.aborted) return;
        const next = res.candidates ?? [];
        setCandidates(next);
        // Retire local suppressions the server has caught up with, and ONLY
        // those. Decisions and refetches interleave: a row decided while this
        // request was in flight was still pending when the query ran, so it
        // comes back in `next`. Clearing the set wholesale would resurrect it
        // as pending and re-arm the very double-merge the set exists to
        // prevent. Intersecting instead is self-healing -- a suppression lives
        // exactly as long as the server disagrees with it -- and keeps the set
        // bounded by the page size rather than growing across a session.
        setDecidedIds((prev) => {
          if (prev.size === 0) return prev;
          const live = new Set(next.map((c) => c.id));
          const kept = new Set([...prev].filter((id) => live.has(id)));
          return kept.size === prev.size ? prev : kept;
        });
        setTotal(res.total ?? 0);
        // Record the term THESE rows answer for, so the local pass in
        // `filtered` knows it has been superseded and stands down.
        setAppliedSearch(debouncedSearch);
        setLoading(false);
      })
      .catch((err: unknown) => {
        // An abort is this hook cancelling its own request, not a failure the
        // reviewer should see. Surfacing it would flash an error banner on every
        // filter keystroke.
        if (ctrl.signal.aborted) return;
        setError(err instanceof Error ? err.message : 'Failed to load duplicate candidates');
        setLoading(false);
        // Fail TOTALLY, not partially. Keeping the previous rows would leave
        // the error banner sitting above a list that answers a DIFFERENT term,
        // under a total counted for that other term -- and because the local
        // pass keeps running until the server answers, what shows is a
        // narrowed subset of it. Three plausible-looking rows under "40,251
        // candidates match this filter" is exactly the wrong-but-plausible
        // answer server-side search was added to remove. An empty list beneath
        // an error is unambiguous.
        setCandidates([]);
        setTotal(0);
        // Stand the local pass down too: it has nothing left to narrow, and
        // leaving it armed would keep this term looking unanswered forever.
        setAppliedSearch(debouncedSearch);
      });

    return () => ctrl.abort();
  }, [
    active,
    filters.status,
    filters.band,
    filters.bothUnmatched,
    filters.entityId,
    debouncedSearch,
    page,
    pageSize,
    reloadToken,
  ]);

  useEffect(() => {
    if (!active) return;
    let cancelled = false;
    api
      .getDedupStats()
      .then((res) => {
        if (!cancelled) setStats(res.stats ?? []);
      })
      .catch(() => {
        // Band counts are decoration on the filter chips. Failing to load them
        // must not blank the lane, so this is swallowed on purpose.
      });
    return () => {
      cancelled = true;
    };
  }, [active, reloadToken]);

  const refresh = useCallback(() => setReloadToken((t) => t + 1), []);

  // -------------------------------------------------------------------------
  // Derived
  // -------------------------------------------------------------------------

  /**
   * The search term the LOCAL pass is keyed on, deliberately one render behind
   * the debounced term.
   *
   * MEASURED, not stylistic. Re-filtering at the 100-row page cap unmounts ~99
   * rows and mounts ~99 more in one synchronous render. On a prod build that
   * was ONE 215 ms main-thread task -- a visible stall, and the thing that made
   * this lane feel slow at 50 and 100 rows while the metadata and regroup lanes
   * blocked for 0 ms.
   *
   * Deferring lets React 19 treat that re-render as a transition and yield
   * between rows. Measured on the benchmark-review-lanes harness at N=100, this
   * ALONE:
   *
   *   blocked main-thread time  770 ms -> 54 ms
   *   longest single task       215 ms -> 66 ms
   *   at N=50                   269/116 ms -> 0/0 ms
   *   wall-clock to settle      43 ms -> 84 ms
   *
   * The wall-clock RISE is the trade and it is the right one: the work is two
   * passes instead of one, but no single pass owns the main thread long enough
   * to feel.
   *
   * STILL LOAD-BEARING after search moved server-side, though it fires far less
   * often. It is now layered on the DEBOUNCED term rather than the raw one, so
   * the big re-filter happens at most once per search instead of once per
   * keystroke -- and when it does happen it is still a ~99-row swap, which is
   * exactly the task this measurement is about. Debouncing changes how OFTEN;
   * deferring changes how MUCH it blocks. They are not substitutes.
   *
   * SAFETY NOTE, because it looks like a hazard and is not. `selectAllOnScreen`
   * below deliberately reads `visible` rather than the whole page, so a
   * reviewer can never bulk-merge rows they cannot see. `visible` is derived
   * from THIS deferred value, and the rows on screen are rendered from the same
   * `visible` in the same pass -- so the two cannot disagree, including during
   * the transition window.
   */
  const deferredSearch = useDeferredValue(debouncedSearch);

  /**
   * The loaded page, minus locally-decided rows, minus anything the local
   * search pass excludes WHILE that pass is still meaningful.
   *
   * The local pass covers exactly one window: from the moment the debounce
   * fires until the server's answer for that term lands. It removes the round
   * trip from the felt latency; it is not a second opinion on the results.
   *
   * Once the server HAS answered for this term it must stand down, and that is
   * a correctness requirement rather than an optimisation. The server's
   * predicate is strictly wider than this one: it matches author names, which
   * it resolves through the author table, while the objects rendered here carry
   * no author_name at all -- the TypeScript Book interface declares
   * `author_name?: string`, but THIS endpoint never sets it, so the optional
   * read yields undefined and `?? ''` swallows it. (Other endpoints do send
   * it: the audiobooks service and `enrichedBookResponse` both populate the
   * field, which is why the TS declaration is correct and must not be
   * deleted on the strength of what this lane sees.) Left running, this filter
   * would throw away every row the server matched on author: `total` says 12,
   * the list shows 0, and the row stays unfindable -- the exact defect
   * server-side search was added to fix, wearing a different hat.
   */
  const filtered = useMemo(() => {
    const decided = (rows: DedupCandidate[]) =>
      decidedIds.size === 0 ? rows : rows.filter((c) => !decidedIds.has(c.id));
    const q = deferredSearch.trim().toLowerCase();
    if (!q || serverAnsweredTerm(appliedSearch, q)) return decided(candidates);
    // Only the fields actually present on a loaded row. author_name is
    // deliberately absent -- see above; including it would look thorough while
    // contributing an empty string for every candidate.
    const hay = (c: DedupCandidate) =>
      [
        c.entity_a_id,
        c.entity_b_id,
        c.layer,
        c.band ?? '',
        c.book_a?.title ?? '',
        c.book_b?.title ?? '',
        c.book_a?.file_path ?? '',
        c.book_b?.file_path ?? '',
      ]
        .join(' ')
        .toLowerCase();
    const searched = candidates.filter((c) => hay(c).includes(q));
    return decided(searched);
  }, [candidates, deferredSearch, appliedSearch, decidedIds]);

  /**
   * Progressive mount: the first FIRST_PAINT_ROWS rows commit synchronously,
   * the rest arrive in a transition.
   *
   * MEASURED, like the `useDeferredValue` above it. Loading a 100-row page
   * rendered every row in ONE synchronous task, because the fetch resolving is
   * a plain setState outside any transition:
   *
   *   blocked main-thread time  106 ms
   *   longest single task       156 ms
   *
   * against a noise floor that produces no long task at all. The cost is close
   * to linear in the row count -- N=50 measured 87 ms and N=100 156 ms, i.e.
   * ~18 ms of fixed route/mount cost plus ~1.4 ms per row -- so it is the rows,
   * not the shell, and it grows with the page-size option the user picks.
   *
   * The proof that a transition is enough lives one interaction over: the
   * client-side search re-renders ~99 rows through `deferredSearch` and
   * measures ZERO tasks over 50 ms at N=100, because React yields between rows
   * in a transition instead of running the pass to completion.
   *
   * WHY A SYNCHRONOUS FIRST SLICE, rather than deferring the whole list. A bare
   * `useDeferredValue(filtered)` would return the PREVIOUS value -- the empty
   * array -- for the urgent render that follows the fetch, and `loading` is
   * already false by then. The spine renders `emptyMessage` when it gets no
   * rows, so that pass would COMMIT a frame reading "No duplicate candidates"
   * before the rows appeared. Mounting a first slice synchronously means the
   * reviewer never sees an empty state that is not true.
   *
   * SAFETY, and it is the same argument the `deferredSearch` note above makes.
   * `selectAllVisible` must never reach past what the reviewer can see, because
   * a merge cannot be undone. It reads `visible` -- and `visible` is what the
   * rows are rendered from, in the same pass. Capping it here therefore keeps
   * that invariant verbatim rather than reinterpreting it: during the mount
   * window "everything on screen" is the first slice, and select-all selects
   * exactly that. The shift-click anchor is an INDEX into `visible`, and the
   * cap only ever extends a PREFIX, so an index taken before the cap lifted
   * still points at the same row afterwards.
   */
  const [mountCap, setMountCap] = useState(FIRST_PAINT_ROWS);
  // Reset the cap during render rather than in an effect. An effect runs after
  // the render that used the stale cap, so a filter change would commit one
  // full-length pass -- exactly the task this is here to prevent -- before the
  // reset landed. This is React's documented "adjust state when a prop changes"
  // pattern: the extra render is discarded, never committed.
  const [capList, setCapList] = useState(filtered);
  if (capList !== filtered) {
    setCapList(filtered);
    setMountCap(FIRST_PAINT_ROWS);
  }

  useEffect(() => {
    if (mountCap >= filtered.length) return;
    // One transition to the full length, NOT a chunk-at-a-time loop. React
    // yields between rows inside a transition on its own, and every extra step
    // would be another `visible` identity -- which `toggleSelect` depends on,
    // so each one re-renders every already-mounted row.
    startTransition(() => setMountCap(filtered.length));
  }, [mountCap, filtered]);

  const visible = useMemo(
    () => (mountCap >= filtered.length ? filtered : filtered.slice(0, mountCap)),
    [filtered, mountCap]
  );

  /**
   * The focus pointer, clamped to what is actually on screen.
   *
   * `visible` shrinks the instant a decision is dispatched, which can leave the
   * stored index past the end. Clamping on READ rather than correcting it in an
   * effect matters twice over: an effect runs after the render that produced
   * the bad index, so there is one frame where `visible[focusedIndex]` is
   * undefined -- and the key handler reads exactly that as "no focused row",
   * turning every shortcut into a silent no-op. Deriving it means the
   * out-of-range value is never observable, and there is no cascading render.
   *
   * The raw state is left alone so j/k keep their own bounds arithmetic.
   */
  const focusedIndex = Math.min(focusedIndexState, Math.max(0, visible.length - 1));

  const totalPages = Math.max(1, Math.ceil(total / pageSize));

  const pendingTotal = useMemo(
    () => stats.filter((s) => s.status === 'pending').reduce((sum, s) => sum + s.count, 0),
    [stats]
  );

  const mergeAllFilteredDisabledReason = filters.bothUnmatched
    ? MERGE_ALL_BLOCKED_REASON
    : // The rows on screen must be the rows the SERVER selected before a bulk
      // merge can claim to match them. serverAnsweredTerm is the same guard the
      // local filter stands down on, reused so the two cannot disagree.
      serverAnsweredTerm(appliedSearch, filters.search)
      ? null
      : MERGE_ALL_SEARCH_PENDING_REASON;

  // -------------------------------------------------------------------------
  // Filters and pagination
  //
  // Focus resets on any change that swaps the underlying rows. With server-side
  // pagination there is a window where `candidates` still holds the PREVIOUS
  // page while the new one is in flight; leaving focus where it was would point
  // `m` and `d` at a row from the page the reviewer just left.
  // -------------------------------------------------------------------------

  /**
   * The shift-click anchor: an INDEX INTO `visible`, not a candidate id.
   *
   * Declared here rather than beside `toggleSelect` because the pagination
   * setters below have to clear it. An index only means anything against the
   * rows that produced it -- carry it across a page turn and the first
   * shift-click on the new page extends a span from whatever row happens to sit
   * at that index now, silently selecting pairs the reviewer never pointed at.
   */
  const lastClickedIndexRef = useRef<number | null>(null);

  /**
   * Drops a selection that is about to stop being visible, and says so.
   *
   * Selection is keyed by candidate id, so it SURVIVES a page turn -- rows the
   * reviewer can no longer see stay armed for "Merge Selected", and a merge on
   * this lane cannot be undone. Clearing is the only safe answer; the toast
   * exists so the clear is not itself silent.
   *
   * The toast is raised OUTSIDE the state updater on purpose: React invokes
   * updaters twice under StrictMode, which would double every message.
   */
  const clearSelectionForNewRows = useCallback(() => {
    if (selectedIds.size > 0) {
      toast(`Selection cleared — ${selectedIds.size} pair(s) are no longer on screen.`, 'info');
    }
    setSelectedIds(new Set());
    lastClickedIndexRef.current = null;
  }, [selectedIds, toast]);

  const setFilters = useCallback(
    (patch: Partial<LocalDupesFilters>) => {
      setFiltersState((prev) => ({ ...prev, ...patch }));
      // Search invalidates the page too, but on its DEBOUNCED value rather
      // than per keystroke -- that reset lives in the during-render block
      // above. Resetting here as well would clear the selection on every
      // character typed.
      const serverSide = Object.keys(patch).some((k) => k !== 'search');
      if (serverSide) {
        setPageState(1);
        clearSelectionForNewRows();
      }
      setFocusedIndex(0);
    },
    [clearSelectionForNewRows]
  );

  const setPage = useCallback(
    (p: number) => {
      setPageState(p);
      clearSelectionForNewRows();
      setFocusedIndex(0);
    },
    [clearSelectionForNewRows]
  );

  const setPageSize = useCallback(
    (n: number) => {
      setPageSizeState(n);
      setPageState(1);
      clearSelectionForNewRows();
      setFocusedIndex(0);
    },
    [clearSelectionForNewRows]
  );

  // -------------------------------------------------------------------------
  // Selection
  // -------------------------------------------------------------------------

  const toggleSelect = useCallback(
    (id: number, index?: number, shiftKey = false) => {
      const anchor = lastClickedIndexRef.current;
      // A shift-click extends the range rather than toggling one row. It ADDS
      // the span rather than replacing the selection, matching the source and
      // making a selection assembled from several ranges possible.
      if (shiftKey && index != null && anchor != null && anchor !== index) {
        const [lo, hi] = anchor < index ? [anchor, index] : [index, anchor];
        setSelectedIds((prev) => {
          const next = new Set(prev);
          for (const c of visible.slice(lo, hi + 1)) next.add(c.id);
          return next;
        });
        lastClickedIndexRef.current = index;
        return;
      }
      if (index != null) lastClickedIndexRef.current = index;
      setSelectedIds((prev) => {
        const next = new Set(prev);
        if (next.has(id)) next.delete(id);
        else next.add(id);
        return next;
      });
    },
    // Depends on `visible` rather than reading it from a ref. Writing a ref
    // during render is unsafe under concurrent rendering -- React can discard a
    // render pass, leaving the ref holding values from work that never
    // committed. The identity churn this costs is free in practice: a change to
    // `visible` is a new array, so every row re-renders regardless.
    //
    // ONE EXCEPTION, since the progressive mount above introduced it: lifting
    // `mountCap` to the full page produces a `visible` whose first
    // FIRST_PAINT_ROWS entries are the same objects as before, so those rows
    // re-render for no reason other than this dependency. That is ~20 wasted
    // row renders, once per page load, INSIDE the transition and therefore off
    // the critical path -- it does not show up in the benchmark (N=100 load
    // measures no task over the 50 ms longtask threshold). Recorded so the next
    // reader does not mistake it for a bug that was missed. Fixing it properly
    // means giving the row handlers an identity that does not depend on
    // `visible` at all, which the ref shortcut above is explicitly not safe
    // enough to do.
    [visible]
  );

  // `visible`, not the whole page: the client-side search narrows what is on
  // screen, and a select-all that reached past it would stage rows the reviewer
  // cannot see for a merge they cannot undo. The name says `visible` so the
  // shortcut's label has to as well.
  const selectAllVisible = useCallback(() => {
    setSelectedIds(new Set(visible.map((c) => c.id)));
  }, [visible]);

  const clearSelection = useCallback(() => setSelectedIds(new Set()), []);

  // -------------------------------------------------------------------------
  // Actions
  // -------------------------------------------------------------------------

  /** Suppress rows the moment their decision is dispatched -- see `decidedIds`. */
  const markDecided = useCallback((ids: number[]) => {
    setDecidedIds((prev) => {
      const next = new Set(prev);
      for (const id of ids) next.add(id);
      return next;
    });
  }, []);

  /**
   * Put rows back when their decision failed. Without this an optimistic
   * removal outlives the request that justified it: the merge did not happen,
   * the pair is still pending server-side, and the reviewer never sees it
   * again -- a silent drop out of the queue, which is worse than the error
   * toast they already got.
   */
  const unmarkDecided = useCallback((ids: number[]) => {
    setDecidedIds((prev) => {
      if (prev.size === 0) return prev;
      const next = new Set(prev);
      let changed = false;
      for (const id of ids) changed = next.delete(id) || changed;
      return changed ? next : prev;
    });
  }, []);

  /**
   * Sequential rather than concurrent, carried over deliberately. Each merge
   * rewrites book rows on the server, and firing a selection's worth at once
   * lets two merges touch the same book. The loop is slower and correct.
   */
  const runSequential = useCallback(
    async (ids: number[], op: (id: number) => Promise<void>, verb: string) => {
      setBusy(true);
      markDecided(ids);
      let done = 0;
      const failedIds: number[] = [];
      for (const id of ids) {
        try {
          await op(id);
          done++;
        } catch {
          failedIds.push(id);
        }
      }
      if (failedIds.length > 0) unmarkDecided(failedIds);
      setBusy(false);
      toast(
        failedIds.length === 0
          ? `${verb} ${done} candidate${done === 1 ? '' : 's'}`
          : `${verb} ${done}, failed ${failedIds.length}`,
        failedIds.length === 0 ? 'success' : 'warning'
      );
      clearSelection();
      refresh();
    },
    [toast, clearSelection, refresh, markDecided, unmarkDecided]
  );

  const dispatch = useCallback(
    (action: DupesAction) => {
      switch (action.type) {
        case 'merge':
          // Before the await, not after: the whole point is that the row is
          // gone by the time the next keystroke is read.
          markDecided([action.id]);
          void (async () => {
            setBusy(true);
            try {
              await api.mergeDedupCandidate(action.id, action.keepId);
              toast('Merged', 'success');
              refresh();
            } catch (err) {
              unmarkDecided([action.id]);
              toast(err instanceof Error ? err.message : 'Merge failed', 'error');
            } finally {
              setBusy(false);
            }
          })();
          return;

        case 'dismiss':
          markDecided([action.id]);
          void (async () => {
            setBusy(true);
            try {
              await api.dismissDedupCandidate(action.id);
              toast('Dismissed', 'success');
              refresh();
            } catch (err) {
              unmarkDecided([action.id]);
              toast(err instanceof Error ? err.message : 'Dismiss failed', 'error');
            } finally {
              setBusy(false);
            }
          })();
          return;

        case 'mergeSelected':
          if (action.ids.length === 0) return;
          void runSequential(action.ids, (id) => api.mergeDedupCandidate(id), 'Merged');
          return;

        case 'dismissSelected':
          if (action.ids.length === 0) return;
          void runSequential(action.ids, (id) => api.dismissDedupCandidate(id), 'Dismissed');
          return;

        case 'mergeAllFiltered':
          // The guard lives HERE, not only on the button. A disabled control is
          // a UI affordance; this is the invariant. Anything that can dispatch
          // -- a command menu, a shortcut, a future keybinding -- has to hit the
          // same refusal, because the cost of getting it wrong is an
          // irreversible merge of pairs the reviewer never saw.
          if (mergeAllFilteredDisabledReason) {
            toast(mergeAllFilteredDisabledReason, 'warning');
            return;
          }
          void (async () => {
            setBusy(true);
            try {
              const result = await api.bulkMergeDedupCandidates({
                entity_type: 'book',
                status: filters.status || 'pending',
                // Filter parity with what is on screen. Omitting either of these
                // is what made this action merge the whole library.
                band: filters.band ?? undefined,
                entity_id: filters.entityId ?? undefined,
                // The SETTLED term -- the one the visible rows were
                // fetched with. Sending filters.search could transmit a
                // term the reviewer typed but has not seen results for.
                q: appliedSearch.trim() || undefined,
              });
              toast(
                `Bulk merge: ${result.merged} merged, ${result.failed} failed of ${result.attempted}`,
                result.failed === 0 ? 'success' : 'warning'
              );
              clearSelection();
              refresh();
            } catch (err) {
              toast(err instanceof Error ? err.message : 'Bulk merge failed', 'error');
            } finally {
              setBusy(false);
            }
          })();
          return;
      }
    },
    [
      markDecided,
      unmarkDecided,
      toast,
      refresh,
      runSequential,
      clearSelection,
      mergeAllFilteredDisabledReason,
      filters.status,
      filters.band,
      filters.entityId,
      // Read directly by mergeAllFiltered to build the bulk payload. It is
      // currently reachable anyway through mergeAllFilteredDisabledReason,
      // which now depends on it -- but relying on that is a trap: change how
      // the refusal is computed and this closure silently goes stale, and a
      // stale value here means the bulk merge omits `q` and merges the whole
      // queue. Named explicitly so the dependency cannot be lost by accident.
      appliedSearch,
    ]
  );

  // -------------------------------------------------------------------------
  // Keyboard
  //
  // Lives in the hook rather than the view because every shortcut mutates state
  // that lives here, and because a window listener that outlives the lane is a
  // real bug: `j` would move a focus ring nobody can see.
  // -------------------------------------------------------------------------

  useEffect(() => {
    if (!active) return;

    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key === '?' && shortcutHelpOpen) {
        event.preventDefault();
        setShortcutHelpOpen(false);
        return;
      }

      // Escape runs BEFORE the suppression guard. MUI gives the compare
      // drawer's paper role="dialog" and moves focus into it, so guarding
      // Escape would suppress the one shortcut whose entire job is closing
      // that drawer.
      if (event.key === 'Escape') {
        if (drawerCandidateId !== null) {
          event.preventDefault();
          setDrawerCandidateId(null);
        }
        return;
      }

      if (isKeyboardShortcutSuppressed()) return;

      if (event.key === '?') {
        event.preventDefault();
        setShortcutHelpOpen(!shortcutHelpOpen);
        return;
      }

      if (visible.length === 0) return;
      const focused = visible[focusedIndex];
      if (!focused) return;

      switch (event.key) {
        case 'j':
          event.preventDefault();
          setFocusedIndex(Math.min(focusedIndex + 1, visible.length - 1));
          return;
        case 'k':
          event.preventDefault();
          setFocusedIndex(Math.max(focusedIndex - 1, 0));
          return;
        case 's':
          event.preventDefault();
          toggleSelect(focused.id);
          return;
        case 'A':
          if (event.shiftKey) {
            event.preventDefault();
            selectAllVisible();
          }
          return;
        case 'Enter':
          event.preventDefault();
          setDrawerCandidateId(focused.id);
          return;
        case 'd':
          if (focused.status !== 'pending') return;
          event.preventDefault();
          dispatch({ lane: 'dupes', type: 'dismiss', id: focused.id });
          return;
        case 'a':
        case 'b': {
          if (focused.status !== 'pending') return;
          event.preventDefault();
          // Explicit keep-side, as opposed to `m`, which follows the
          // recommendation and silently falls back to A on a tie. These exist
          // because disagreeing with the recommendation is the case that costs
          // a mouse trip today: `recommendedKeepSide` returns null on a tie and
          // renders no chip at all, so on exactly the pairs where the reviewer
          // has to think, `m` is the shortcut that tells them the least.
          //
          // Lowercase only. Shift+A is select-all and arrives as key 'A', so
          // the two never collide.
          dispatch({
            lane: 'dupes',
            type: 'merge',
            id: focused.id,
            keepId: event.key === 'a' ? focused.entity_a_id : focused.entity_b_id,
          });
          return;
        }
        case 'm': {
          if (focused.status !== 'pending') return;
          event.preventDefault();
          // Shared with the recommended-keep chip so the two cannot disagree
          // about which book survives -- see ./keepDecision.
          const { keepId } = keepIdForMerge(focused);
          dispatch({ lane: 'dupes', type: 'merge', id: focused.id, keepId });
          return;
        }
      }
    };

    window.addEventListener('keydown', handleKeyDown);
    return () => window.removeEventListener('keydown', handleKeyDown);
  }, [
    active,
    visible,
    focusedIndex,
    drawerCandidateId,
    shortcutHelpOpen,
    dispatch,
    toggleSelect,
    selectAllVisible,
  ]);

  return {
    loading,
    error,
    candidates: visible,
    total,
    page,
    totalPages,
    pageSize,
    setPage,
    setPageSize,
    filters,
    setFilters,
    pendingTotal,
    selectedIds,
    toggleSelect,
    selectAllVisible,
    clearSelection,
    focusedIndex,
    setFocusedIndex,
    drawerCandidateId,
    setDrawerCandidateId,
    shortcutHelpOpen,
    setShortcutHelpOpen,
    busy,
    mergeAllFilteredDisabledReason,
    verbs: dupesLane.verbs,
    dispatch,
    refresh,
  };
}

/**
 * Whether a keystroke should be treated as typing rather than a shortcut.
 *
 * Load-bearing in the workspace in a way it was not in the standalone tab: the
 * queue rail is full of text fields, and without this every search box would
 * merge a candidate the moment someone typed "m".
 */
export function isKeyboardShortcutSuppressed(): boolean {
  const active = document.activeElement;
  if (!active) return false;
  const tag = active.tagName.toLowerCase();
  if (tag === 'input' || tag === 'textarea' || tag === 'select') return true;
  const el = active as HTMLElement;
  if (el.isContentEditable) return true;
  // MUI renders modals as div[role="dialog"], not native <dialog>.
  return Boolean(el.closest('[role="dialog"]'));
}

/** Rendered by the shortcut help overlay. Kept beside the handler that implements them. */
export const DEDUP_SHORTCUTS = [
  { keys: 'j / k', action: 'Move to next / previous row' },
  { keys: 'm', action: 'Merge, keeping the recommended side (A when the two tie)' },
  { keys: 'a', action: 'Merge, keeping A -- regardless of the recommendation' },
  { keys: 'b', action: 'Merge, keeping B -- regardless of the recommendation' },
  { keys: 'd', action: 'Dismiss the focused candidate' },
  { keys: 's', action: 'Select / deselect the focused row' },
  { keys: 'Enter', action: 'Open the compare drawer for the focused row' },
  { keys: 'Esc', action: 'Close the compare drawer' },
  { keys: 'Shift+A', action: 'Select all rows shown (the search narrows this)' },
  { keys: '?', action: 'Toggle this shortcut help' },
];
