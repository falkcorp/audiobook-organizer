// file: web/src/components/review/lanes/useRegroupLane.ts
// version: 1.6.0
// guid: 3f8b2c07-9d41-4e56-b8a3-1c7e05d9a264
// last-edited: 2026-09-01

/**
 * The regroup lane's data layer.
 *
 * Shaped like useDupesLane (own fetch, own AbortController, `active` gating) and
 * deliberately NOT like the page it replaces, which read its rows from the
 * globally polled useReviewStore.
 *
 * That store stays. `ReviewBanner`, `Sidebar` and `App.tsx` all read its `count`,
 * and `App.tsx` owns its polling, so deleting it at Phase 7 would silently kill
 * the sidebar badge. What moves here is `items` / `itemsLoading` / `loadItems`,
 * which had exactly one consumer and were lane-private state living in a global
 * store by accident of history.
 *
 * The deciding reason is gating: a lane whose rows live in a globally polled
 * store cannot be switched off when the reviewer moves to another lane, because
 * the store has no notion of who is looking.
 *
 * See docs/port-inventory-regroup.md for the three defects this port fixes
 * rather than carries.
 *
 * ---------------------------------------------------------------------------
 * FILTERING: ONE DIMENSION SERVER-SIDE, TWO CLIENT-SIDE — AND WHY THAT SPLIT
 * ---------------------------------------------------------------------------
 *
 * `kind` is pushed to the SERVER. It is the only one of the three that can be,
 * and it is the only one that changes which rows exist rather than which of the
 * fetched rows are shown. That matters here more than it reads: the fetch is
 * capped at REGROUP_FETCH_LIMIT, so an unfiltered load of a queue holding 730
 * items across kinds spends its whole budget on a mixture. Asking the server for
 * one kind spends all 500 on the kind the reviewer is actually working.
 *
 * `search` and `sortBy` are CLIENT-side, because the endpoint offers neither.
 * That makes them narrower than they look, and the UI has to say so: they act on
 * the loaded page, not on the queue. `truncated` below is what keeps that
 * honest.
 *
 * ---------------------------------------------------------------------------
 * THREE COUNTS, THREE MEANINGS — DO NOT COLLAPSE THEM
 * ---------------------------------------------------------------------------
 *
 *   queueTotal    every pending hold, all kinds        (polled /review/count)
 *   total         pending holds matching the FETCH     (kind-scoped when filtering)
 *   loaded        rows this lane actually holds        (<= REGROUP_FETCH_LIMIT)
 *   visible       rows left after the search box       (<= loaded)
 *
 * The trap is `total`: the server applies `kind` before taking the length (see
 * getReviewItems' doc comment), so under a kind filter it is that kind's count
 * and NOT the queue's. Rendering it as "N pending" beside a kind selector would
 * understate the queue by everything the other kinds hold.
 *
 * The second trap is truncation. `truncated` is derived from `loaded` against
 * the server's count and NEVER from `visible`: a search that hides rows is the
 * reviewer narrowing their own view, while truncation is the lane failing to
 * load rows that exist. Feeding the search count into `truncated` would raise a
 * "your view is partial" warning on every keystroke and thereby make the one
 * warning that means something unreadable.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import * as api from '../../../services/api';
import type { ReviewBulkSkip, ReviewItem, ReviewItemsPage } from '../../../services/api';
import { useReviewStore } from '../../../stores/useReviewStore';
import { labelForKind } from '../../../lib/reviewKinds';
import { defaultActionFor, parsePayload } from '../../../lib/reviewPayload';
import type { ReviewPayload } from '../../../lib/reviewPayload';

type Toast = (message: string, severity?: 'success' | 'error' | 'warning' | 'info') => void;

/**
 * How many holds the lane loads. The source used the same number.
 *
 * It is a real ceiling, not a formality: production currently holds 730 pending
 * items, 714 of them one kind. Everything below that reports a loaded count
 * separately from the true count precisely because those two numbers differ.
 *
 * 🔴 The kind filter RAISES this ceiling's usefulness; it does not remove it. A
 * kind holding 714 still truncates at 500 — what changes is that all 500 are now
 * that kind instead of ~484 of it plus 16 of something else. Paging past the cap
 * needs `offset`, which the client already supports and this lane does not use.
 */
export const REGROUP_FETCH_LIMIT = 500;

/**
 * How long the lane waits after the last keystroke before searching.
 *
 * The search never leaves the browser, so this is not about sparing the server.
 * It is about the work between a keystroke and a frame: every character
 * re-filters up to REGROUP_FETCH_LIMIT rows and re-renders every bucket beneath
 * them, and doing that per keystroke is what makes a text box feel broken.
 */
export const REGROUP_SEARCH_DEBOUNCE_MS = 250;

/**
 * Deadline on the lane's own fetch.
 *
 * apiFetch defaults to NO timeout on purpose (scans and compactions legitimately
 * run for minutes), so a lane that wants one asks. This one wants one: a review
 * queue read is a bounded key scan, and without a deadline a server that never
 * answers leaves a spinner turning forever with nothing to tell the reviewer.
 */
export const REGROUP_FETCH_TIMEOUT_MS = 30_000;

/** How the visible holds are ordered. */
export type RegroupSort = 'kind' | 'newest' | 'oldest';

export const REGROUP_SORTS: { value: RegroupSort; label: string }[] = [
  { value: 'kind', label: 'Kind (A–Z)' },
  { value: 'newest', label: 'Newest first' },
  { value: 'oldest', label: 'Oldest first' },
];

export interface RegroupFilters {
  /**
   * Server-side. Empty string means every kind.
   *
   * The only filter that round-trips, and the only one that produces an honest
   * `total`.
   */
  kind: string;
  /**
   * Client-side, over the loaded rows only.
   *
   * Debounced by REGROUP_SEARCH_DEBOUNCE_MS before it reaches the buckets; the
   * raw value stays here so the text field never lags the typist.
   */
  search: string;
  sortBy: RegroupSort;
}

export const REGROUP_INITIAL_FILTERS: RegroupFilters = {
  kind: '',
  search: '',
  sortBy: 'kind',
};

/** One selectable kind, with the server's pending count for it. */
export interface RegroupKindOption {
  kind: string;
  label: string;
  count: number;
}

export interface RegroupBucket {
  kind: string;
  label: string;
  /** The holds VISIBLE for this kind — loaded, then narrowed by the search. */
  items: ReviewItem[];
  /**
   * Holds LOADED for this kind, before the search box touched them.
   *
   * Separate from `items.length` so the truncation warning and the bulk-scope
   * numbers keep meaning "what the lane fetched" while a search is active.
   */
  loadedForKind: number;
  /**
   * Every pending hold of this kind on the server, from the polled count.
   *
   * Separate from `loadedForKind` because they genuinely differ, and the gap is
   * the whole point: "Approve all" is kind-scoped server-side, so it acts on
   * this number, not on the one the reviewer scrolled through.
   */
  totalForKind: number;
  /** True when the server holds more of this kind than the lane loaded. */
  truncated: boolean;
  /** Loaded holds of this kind the search box is currently hiding. */
  hiddenBySearch: number;
}

export interface RegroupLane {
  loading: boolean;
  error: string | null;

  buckets: RegroupBucket[];
  /**
   * Pending holds matching the CURRENT FETCH, per the server.
   *
   * 🔴 Kind-scoped when `filters.kind` is set. For the whole queue use
   * `queueTotal`.
   */
  total: number;
  /**
   * Pending holds across every kind, per the polled /review/count.
   *
   * 🔴 `null` when that number is genuinely not known — the count poll is a
   * SECOND request that swallows its own failure, so under a kind filter it can
   * be absent while rows are on screen. Rendering its zero would have put
   * "0 pending" beside "16 in Multi-disc groups"; a lane with two contradicting
   * numbers is worse than a lane with one. Callers must render the absence, not
   * substitute a number they do not have.
   */
  queueTotal: number | null;
  /** Holds actually loaded, across every loaded kind. */
  loaded: number;
  /** Loaded holds still visible after the search box. */
  visible: number;
  /**
   * True when "Oldest first" is ordering a page that cannot answer it.
   *
   * 🔴 The store sorts `CreatedAt DESC, ID DESC` and slices AFTERWARDS
   * (`ListReviewItems`), so a short page is the NEWEST rows of the matching
   * set. The cut is made along the very axis this control re-orders. Sorting
   * that page ascending therefore puts the oldest of the newest N on top while
   * the genuinely oldest holds were never fetched at all — an answer that looks
   * authoritative and is wrong. "Newest first" over the same page is not
   * affected: the newest N sorted newest-first really are the newest holds.
   */
  oldestSortIsPartial: boolean;

  filters: RegroupFilters;
  setFilters: (patch: Partial<RegroupFilters>) => void;
  clearFilters: () => void;
  /** True when a kind or a search is narrowing the view. Sort alone is not a filter. */
  filtersActive: boolean;
  /** Every kind the SERVER has pending, not merely the ones that fit in this page. */
  kindOptions: RegroupKindOption[];

  /** Resolves what Approve will send for one hold. */
  actionFor: (item: ReviewItem) => string;
  /** The row's parsed payload, from the lane's one-parse-per-row index. Row
   *  renderers MUST use this rather than calling parsePayload themselves. */
  payloadFor: (item: ReviewItem) => ReviewPayload | null;
  setAction: (id: string, value: string) => void;

  isItemBusy: (id: string) => boolean;
  isKindBusy: (kind: string) => boolean;

  approveItem: (item: ReviewItem) => void;
  rejectItem: (item: ReviewItem) => void;
  bulkAction: (kind: string, action: 'approve' | 'reject') => void;

  /**
   * Skips from the last bulk action, keyed BY KIND.
   *
   * The source held a single `{kind, skipped}` and overwrote it on every bulk
   * call, so acting on a second bucket erased the first bucket's report while
   * those holds were still sitting there undecided. Nothing was destroyed, but
   * the one message telling the reviewer what still needed them vanished.
   */
  skipsByKind: Record<string, ReviewBulkSkip[]>;
  dismissSkips: (kind: string) => void;

  refresh: () => void;
}

/**
 * searchTextFor flattens one hold into the string the search box matches.
 *
 * Built once per loaded page rather than per keystroke: the payload is a JSON
 * STRING on the wire, and re-parsing 500 of them on every character typed is the
 * difference between a responsive box and a janky one.
 *
 * 🔴 THERE IS NO AUTHOR HERE, and none is invented. A ReviewItem carries no
 * author field; the member books that do carry one are fetched lazily, per row,
 * only when a reviewer expands it (MemberFilesDetail). Matching on an author the
 * lane has not loaded would silently mean "matches the rows you already opened",
 * which is worse than not offering it. What IS matched: the summary, the folder
 * path (both the item's own `folder_ref` and the payload's `folder`), the
 * proposed title, the member file paths, the dedup key, the id, and the kind's
 * human label.
 */
export function searchTextFor(item: ReviewItem, parsed?: ReviewPayload | null): string {
  // Takes the parsed payload when the caller already has one. The lane parses
  // each row exactly once into payloadIndex and passes it in; the parameter is
  // optional only so the exported helper stays usable on its own in tests.
  const payload = parsed !== undefined ? parsed : parsePayload(item.payload);
  const parts: (string | undefined)[] = [
    item.summary,
    item.folder_ref,
    item.dedup_key,
    item.id,
    labelForKind(item.kind),
    item.kind,
    payload?.folder,
    payload?.survivorTitle,
    payload?.derived_title,
    payload?.title,
  ];
  if (Array.isArray(payload?.files)) {
    for (const f of payload.files) {
      if (typeof f === 'string') parts.push(f);
    }
  }
  return parts
    .filter((p): p is string => typeof p === 'string' && p.length > 0)
    .join('\n')
    .toLowerCase();
}

/**
 * compareItems is the ONE comparator behind both row order and bucket order.
 *
 * Total by construction: every branch ends on the id, so two holds are never
 * "equal" and the order cannot reshuffle between renders. That is not a
 * theoretical worry here — the queue is written in bulk by a scan, so holds
 * sharing a `created_at` to the second are the normal case, not the exotic one,
 * and a comparator that returned 0 for them would leave their order at the mercy
 * of whatever the fetch happened to hand back.
 */
export function compareItems(a: ReviewItem, b: ReviewItem, sortBy: RegroupSort): number {
  if (sortBy === 'kind') {
    const byLabel = labelForKind(a.kind).localeCompare(labelForKind(b.kind));
    if (byLabel !== 0) return byLabel;
    // Within a kind, newest first — the server's own order, kept rather than
    // re-derived differently.
    const byDate = b.created_at.localeCompare(a.created_at);
    if (byDate !== 0) return byDate;
    return b.id.localeCompare(a.id);
  }
  if (sortBy === 'newest') {
    const byDate = b.created_at.localeCompare(a.created_at);
    if (byDate !== 0) return byDate;
    return b.id.localeCompare(a.id);
  }
  const byDate = a.created_at.localeCompare(b.created_at);
  if (byDate !== 0) return byDate;
  return a.id.localeCompare(b.id);
}

export function useRegroupLane(toast: Toast, active = true): RegroupLane {
  const [items, setItems] = useState<ReviewItem[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [busyItems, setBusyItems] = useState<Set<string>>(new Set());
  const [busyKinds, setBusyKinds] = useState<Set<string>>(new Set());
  const [chosenActions, setChosenActions] = useState<Record<string, string>>({});
  const [skipsByKind, setSkipsByKind] = useState<Record<string, ReviewBulkSkip[]>>({});
  const [reloadToken, setReloadToken] = useState(0);
  const [filters, setFiltersState] = useState<RegroupFilters>(REGROUP_INITIAL_FILTERS);
  // The debounced twin of filters.search. The raw value drives the text field so
  // typing never lags; this one drives the buckets.
  const [debouncedSearch, setDebouncedSearch] = useState('');

  // The badge count is shared, genuinely global, and already polled by App.tsx.
  // The lane READS it for honest per-kind totals rather than counting its own
  // loaded rows and calling that the total.
  const byKind = useReviewStore((s) => s.byKind);
  const storeCount = useReviewStore((s) => s.count);
  const loadCount = useReviewStore((s) => s.loadCount);

  const abortRef = useRef<AbortController | null>(null);
  // Which kind the rows on screen were fetched for. Changing the kind must not
  // leave the previous kind's rows sitting there looking like an answer while
  // the new request is in flight — that is the "a failed, an empty and a hung
  // request all render identically" failure in its stale-data form.
  const fetchedKindRef = useRef<string | null>(null);
  /**
   * Monotonic ordering for the two writers of `items`.
   *
   * 🔴 A KIND COMPARISON CANNOT EXPRESS THIS. The first version of this guard
   * asked "is this response for the kind on screen?", which closes the
   * cross-kind case and nothing else. Row busy state is PER ITEM, so every
   * other row stays clickable while one is applying: a reviewer triaging
   * quickly approves a1 then a2, both reloads are in flight for the SAME kind,
   * and if a1's response (read from the server before a2's write) lands last it
   * overwrites a2's — the hold the reviewer just decided reappears in the list
   * with no error and no spinner, and approving it again either 409s or
   * re-applies a destructive `combine`.
   *
   * `reqSeq` is bumped by BOTH the load effect and reload(); `appliedSeq` is the
   * newest response already painted. A response older than what is on screen is
   * dropped whether it lost the race to a different kind or to a later reload of
   * its own kind. This also removes the `null`-sentinel question entirely: a
   * first reload carries seq 1 against appliedSeq 0 and applies.
   */
  const reqSeq = useRef(0);
  const appliedSeq = useRef(0);

  const setFilters = useCallback((patch: Partial<RegroupFilters>) => {
    setFiltersState((prev) => ({ ...prev, ...patch }));
  }, []);

  const clearFilters = useCallback(() => {
    setFiltersState((prev) => ({ ...prev, kind: '', search: '' }));
    // Clear the debounced twin in the same tick. Leaving it to the timer would
    // mean "Clear filters" visibly did nothing for a quarter of a second.
    setDebouncedSearch('');
  }, []);

  useEffect(() => {
    if (filters.search === debouncedSearch) return;
    const t = setTimeout(() => setDebouncedSearch(filters.search), REGROUP_SEARCH_DEBOUNCE_MS);
    return () => clearTimeout(t);
  }, [filters.search, debouncedSearch]);

  const kindFilter = filters.kind;

  /**
   * The ONE request this lane makes for rows, and the ONE way a response is
   * applied.
   *
   * Both call sites -- the mount/refresh effect below and reload() after an
   * approve, reject or bulk action -- used to build these params separately,
   * and reload's copy carried a comment asserting it sent "the SAME filter as
   * the mount fetch". Nothing enforced that. A comment is not a control: a
   * param added to one and not the other silently changes which holds come back
   * after a reviewer decides one, and the lane would look fine while showing
   * the wrong set.
   *
   * The differences that ARE deliberate stay at the call sites, where they can
   * be read next to the reason for them: the effect passes an abort signal and
   * surfaces errors, reload passes none and swallows them because the ACTION
   * succeeded and stale rows are not a failed action.
   */
  const fetchPage = useCallback(
    (signal?: AbortSignal) =>
      api.getReviewItems(
        {
          status: 'pending',
          limit: REGROUP_FETCH_LIMIT,
          // Pushed down so the fetch budget is spent on the kind being worked.
          // Omitted entirely when empty: getReviewItems only sets the param for
          // a truthy kind, and sending `kind=` would be a filter for the empty
          // kind rather than for no filter.
          ...(kindFilter ? { kind: kindFilter } : {}),
        },
        { ...(signal ? { signal } : {}), timeoutMs: REGROUP_FETCH_TIMEOUT_MS }
      ),
    [kindFilter]
  );

  const applyPage = useCallback((page: ReviewItemsPage) => {
    setItems(page.items ?? []);
    setTotal(page.total ?? 0);
  }, []);

  useEffect(() => {
    if (!active) return;
    abortRef.current?.abort();
    const ctrl = new AbortController();
    abortRef.current = ctrl;

    if (fetchedKindRef.current !== null && fetchedKindRef.current !== kindFilter) {
      // Drop the previous kind's rows rather than showing them under the new
      // kind's heading while this request runs.
      setItems([]);
      setTotal(0);
    }
    fetchedKindRef.current = kindFilter;

    setLoading(true);
    setError(null);

    const seq = ++reqSeq.current;
    fetchPage(ctrl.signal)
      .then((page) => {
        if (ctrl.signal.aborted || seq < appliedSeq.current) return;
        appliedSeq.current = seq;
        applyPage(page);
        setLoading(false);
      })
      .catch((err: unknown) => {
        // An abort is this hook cancelling its own request, not a failure the
        // reviewer should see. A TIMEOUT is not an abort in that sense — it
        // means the server never answered, and it must reach the reviewer.
        if (ctrl.signal.aborted) return;
        setError(err instanceof Error ? err.message : 'Failed to load the review queue');
        setLoading(false);
      });

    return () => ctrl.abort();
  }, [active, reloadToken, kindFilter, fetchPage, applyPage]);

  // Keep the shared count fresh when the lane opens, so per-kind totals are not
  // stale on first paint while waiting for the next poll tick.
  useEffect(() => {
    if (!active) return;
    void loadCount();
  }, [active, loadCount, reloadToken]);

  const refresh = useCallback(() => setReloadToken((t) => t + 1), []);

  // One PARSED payload per loaded row, keyed by id.
  //
  // `payload` arrives as a JSON string on the wire and three separate places
  // want it back: this lane's search index, its actionFor, and the spine's row
  // renderer. The first was already built once per page; the other two called
  // parsePayload inline, per row, on EVERY render pass -- two JSON.parse calls
  // per row, so ~1,000 of them per pass at REGROUP_FETCH_LIMIT. The comment on
  // searchTextFor already said re-parsing 500 payloads per keystroke "is the
  // difference between a responsive box and a janky one"; the render path was
  // doing exactly that and the index it needed was sitting next to it.
  //
  // Parsing once here also makes the returned object identity stable across
  // renders, which is what lets a memoized row skip re-rendering at all.
  const payloadIndex = useMemo(() => {
    const index = new Map<string, ReviewPayload | null>();
    for (const item of items) index.set(item.id, parsePayload(item.payload));
    return index;
  }, [items]);

  // has(), not `?? parsePayload(...)`. A payload that legitimately fails to
  // parse is stored as null, and a nullish fallback would re-parse precisely
  // those rows on every render -- the unparseable ones, forever.
  const payloadFor = useCallback(
    (item: ReviewItem): ReviewPayload | null =>
      payloadIndex.has(item.id) ? (payloadIndex.get(item.id) ?? null) : parsePayload(item.payload),
    [payloadIndex]
  );

  // One search index per loaded page, keyed by id. Recomputed when the rows
  // change, NOT when the query changes — the query is compared against it.
  const searchIndex = useMemo(() => {
    const index = new Map<string, string>();
    // payloadFor, NOT `payloadIndex.get(item.id) ?? null`. searchTextFor's
    // second parameter distinguishes `undefined` ("I do not have it, parse it")
    // from `null` ("there is none"); `?? null` collapses a miss into a definite
    // null and defeats exactly the fallback the signature exists for, silently
    // dropping that row's folder, title and member paths out of the search
    // index. payloadFor already draws that distinction correctly.
    for (const item of items) index.set(item.id, searchTextFor(item, payloadFor(item)));
    return index;
  }, [items, payloadFor]);

  const query = debouncedSearch.trim().toLowerCase();

  const buckets = useMemo(() => {
    // Loaded-per-kind is counted BEFORE the search, because it is what the
    // truncation warning and the bulk-scope numbers are about.
    const loadedPerKind = new Map<string, number>();
    for (const item of items) {
      loadedPerKind.set(item.kind, (loadedPerKind.get(item.kind) ?? 0) + 1);
    }

    const visible = query
      ? items.filter((item) => (searchIndex.get(item.id) ?? '').includes(query))
      : items;

    // One sorted pass feeds both orders: a Map keeps insertion order, so the
    // bucket sequence follows the first row of each kind under the same
    // comparator that ordered the rows. Array.prototype.sort is stable per
    // spec, and the comparator is total anyway, so neither is relied on alone.
    const sorted = [...visible].sort((a, b) => compareItems(a, b, filters.sortBy));

    const map = new Map<string, ReviewItem[]>();
    for (const item of sorted) {
      const list = map.get(item.kind) ?? [];
      list.push(item);
      map.set(item.kind, list);
    }

    return Array.from(map.entries()).map(([kind, kindItems]) => {
      const loadedForKind = loadedPerKind.get(kind) ?? kindItems.length;
      // Fall back to the loaded count when the polled map has no entry for
      // this kind. Claiming a total we do not have would be worse than
      // claiming a small one.
      const totalForKind = byKind[kind] ?? loadedForKind;
      return {
        kind,
        label: labelForKind(kind),
        items: kindItems,
        loadedForKind,
        totalForKind,
        truncated: totalForKind > loadedForKind,
        hiddenBySearch: loadedForKind - kindItems.length,
      };
    });
  }, [items, byKind, query, searchIndex, filters.sortBy]);

  const visible = useMemo(() => buckets.reduce((n, b) => n + b.items.length, 0), [buckets]);

  // Every kind the SERVER holds, unioned with the kinds actually loaded. The
  // polled map is the primary source on purpose: a kind pushed off the end of a
  // truncated page would otherwise be missing from the one control that could
  // bring it back.
  const kindOptions = useMemo(() => {
    const kinds = new Set<string>([...Object.keys(byKind), ...items.map((i) => i.kind)]);
    return Array.from(kinds)
      .map((kind) => ({ kind, label: labelForKind(kind), count: byKind[kind] ?? 0 }))
      .sort((a, b) => a.label.localeCompare(b.label));
  }, [byKind, items]);

  const actionFor = useCallback(
    (item: ReviewItem): string => {
      const override = chosenActions[item.id];
      if (override !== undefined) return override;
      return defaultActionFor(payloadFor(item));
    },
    [chosenActions, payloadFor]
  );

  // runItemAction needs the CURRENT action for a row, but only at the moment a
  // reviewer clicks. Depending on actionFor directly would rebuild approveItem
  // and rejectItem every time any row's dropdown changed, and those two are
  // passed to every row -- so a memoized row would be re-rendered by a change
  // to a DIFFERENT row's action. That is the "memo present and inert" shape
  // DupesPanel warns about. A ref keeps the read current and the identity
  // stable.
  const actionForRef = useRef(actionFor);
  useEffect(() => {
    actionForRef.current = actionFor;
  }, [actionFor]);

  const setAction = useCallback((id: string, value: string) => {
    setChosenActions((prev) => ({ ...prev, [id]: value }));
  }, []);

  const isItemBusy = useCallback((id: string) => busyItems.has(id), [busyItems]);
  const isKindBusy = useCallback((kind: string) => busyKinds.has(kind), [busyKinds]);

  const reload = useCallback(async () => {
    await Promise.all([
      (() => {
        // Reload carries no abort signal ON PURPOSE -- it has to finish so the
        // hold the reviewer just decided actually leaves the list -- so nothing
        // cancels it and it can land after a newer request. It orders itself
        // instead: see reqSeq above for the two races that reach here.
        const seq = ++reqSeq.current;
        return fetchPage().then((page) => {
          if (seq < appliedSeq.current) return;
          appliedSeq.current = seq;
          applyPage(page);
        });
      })().catch(() => {
        // A failed refresh after a SUCCESSFUL action must not report the
        // action as failed. The rows are stale, not wrong.
      }),
      loadCount(),
    ]);
  }, [loadCount, kindFilter, fetchPage, applyPage]);

  const runItemAction = useCallback(
    async (item: ReviewItem, action: 'approve' | 'reject') => {
      setBusyItems((prev) => new Set(prev).add(item.id));
      try {
        if (action === 'approve') {
          // Always send the resolved action explicitly. The backend would accept
          // an empty body and use the recommendation, but sending what the UI
          // actually displayed removes any chance of the two disagreeing.
          await api.approveReviewItem(item.id, actionForRef.current(item));
        } else {
          await api.rejectReviewItem(item.id);
        }
        await reload();
      } catch (err) {
        // The backend's own message is surfaced verbatim, because a refusal
        // carries a reason the reviewer needs to read and a generic "failed to
        // approve" would hide it. Pretending it succeeded would be worse still:
        // the hold would read as decided while nothing had happened.
        //
        // This used to be justified by `duplicate-of` answering 501. That is no
        // longer true -- duplicate-of has had an apply path since 2026-08-19 --
        // but the behaviour is right on its own terms, so it is the REASON that
        // was rewritten here, not the code.
        toast(
          `Failed to ${action} item: ${err instanceof Error ? err.message : 'unknown error'}`,
          'error'
        );
      } finally {
        setBusyItems((prev) => {
          const next = new Set(prev);
          next.delete(item.id);
          return next;
        });
      }
    },
    // actionFor is deliberately absent: it is read through actionForRef at call
    // time, which keeps approveItem/rejectItem stable for the memoized rows.
    [reload, toast]
  );

  const approveItem = useCallback(
    (item: ReviewItem) => void runItemAction(item, 'approve'),
    [runItemAction]
  );
  const rejectItem = useCallback(
    (item: ReviewItem) => void runItemAction(item, 'reject'),
    [runItemAction]
  );

  const bulkAction = useCallback(
    (kind: string, action: 'approve' | 'reject') => {
      void (async () => {
        setBusyKinds((prev) => new Set(prev).add(kind));
        try {
          const result = await api.bulkReviewAction({ action, kind });
          const skipped = result.skipped ?? [];
          // Report the skips in the same breath as the successes. Bulk approve
          // runs each hold's OWN recommendation and refuses the undecidable
          // ones; a message quoting only `processed` would let a reviewer
          // believe a bucket was cleared when a third of it is still sitting
          // there.
          toast(
            `${action === 'approve' ? 'Approved' : 'Rejected'} ${result.processed} item${
              result.processed === 1 ? '' : 's'
            }${skipped.length ? ` · ${skipped.length} skipped, listed below` : ''}`,
            skipped.length ? 'warning' : 'success'
          );
          setSkipsByKind((prev) => {
            const next = { ...prev };
            if (skipped.length) next[kind] = skipped;
            else delete next[kind];
            return next;
          });
          await reload();
        } catch (err) {
          toast(
            `Bulk ${action} failed: ${err instanceof Error ? err.message : 'unknown error'}`,
            'error'
          );
        } finally {
          setBusyKinds((prev) => {
            const next = new Set(prev);
            next.delete(kind);
            return next;
          });
        }
      })();
    },
    [reload, toast]
  );

  const dismissSkips = useCallback((kind: string) => {
    setSkipsByKind((prev) => {
      const next = { ...prev };
      delete next[kind];
      return next;
    });
  }, []);

  return {
    loading,
    error,
    buckets,
    total,
    // Under a kind filter the fetched `total` is that kind's count, so the
    // all-kinds number has to come from the polled endpoint — the same
    // instrument `totalForKind` already trusts, so the two cannot disagree.
    // Unfiltered, the fetched total IS the queue and is preferred: it is the
    // fresher of the two, having just been read.
    queueTotal: kindFilter ? (storeCount > 0 ? storeCount : null) : total,
    loaded: items.length,
    visible,
    oldestSortIsPartial: filters.sortBy === 'oldest' && items.length < total,
    filters,
    setFilters,
    clearFilters,
    filtersActive: Boolean(kindFilter) || query.length > 0,
    kindOptions,
    actionFor,
    payloadFor,
    setAction,
    isItemBusy,
    isKindBusy,
    approveItem,
    rejectItem,
    bulkAction,
    skipsByKind,
    dismissSkips,
    refresh,
  };
}
