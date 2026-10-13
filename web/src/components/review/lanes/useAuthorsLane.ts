// file: web/src/components/review/lanes/useAuthorsLane.ts
// version: 1.2.0
// guid: c9e4a1d7-5b83-4f26-a0d9-3e7b61f48a25
// last-edited: 2026-10-13

/**
 * The Authors & series lane's data layer: the Authors sub-view and the Series
 * sub-view, one hook, one write queue.
 *
 * Shaped like useDupesLabels, not like the DedupAuthorTab it replaces:
 *
 *   - Every read has an AbortController and ends in one of four states
 *     (loading, error, empty, populated), plus the one this data genuinely has
 *     and the tab hid: `needs-scan`, the server holding no cached scan at all.
 *     A cold cache is not "no duplicates", and rendering it as such is the
 *     failed-request / empty-result confusion this codebase keeps shipping.
 *   - Results are KEYED by the request that produced them, and anything that
 *     does not answer the current request reads as loading. A response that
 *     arrives after the lane was left, or after a newer reload, can therefore
 *     never be painted.
 *   - Nothing is requested while the lane is not the visible one, and every
 *     request in flight (reads and the polls that follow a write) is aborted the
 *     moment it stops being.
 *
 * ---------------------------------------------------------------------------
 * CONCURRENT ACTIONS
 * ---------------------------------------------------------------------------
 *
 * The workspace's earlier review lane had a bug where two overrides resolving
 * together undid each other: each built its next list from a snapshot taken
 * before the other landed. Everything here that changes the list goes through
 * `commitGroups` / `commitChoices`, which read the LATEST value from a ref
 * (written synchronously by the same call), never from a closure. Two merges
 * finishing in the same tick each remove their own group from whatever is
 * current, and neither can bring the other's back.
 *
 * Server writes that rewrite book rows (merge, split, resolve) run ONE AT A
 * TIME through a queue, as the tab's loops did and for the same reason: two
 * merges touching the same book at once is the failure sequential execution
 * exists to prevent. The queue is what lets a second Merge click be accepted
 * (its row shows as pending) without becoming a concurrent write.
 *
 * What a merge DOES (which variants are excluded, which are narrators) is read
 * when the merge RUNS, not when it was clicked, so an exclusion made while the
 * merge was waiting in the queue is honoured.
 *
 * ---------------------------------------------------------------------------
 * TWO SUB-VIEWS, ONE QUEUE
 * ---------------------------------------------------------------------------
 *
 * Authors and Series share this hook because they share the queue: a series
 * merge repoints books and an author merge relinks them, and the two must not run
 * at once any more than two author merges may. Everything else is kept apart.
 *
 *   - READS and KEYBOARD follow the SUB-VIEW. `authorsOn` / `seriesOn` are
 *     "the lane is visible AND this sub-view is the one on screen". The Series
 *     list is not requested while Authors is showing, and `m` on the Series
 *     sub-view can never merge an author card nobody can see.
 *   - WRITES follow the LANE. Switching sub-views does not abort a merge that is
 *     queued or running (the progress bar and the busy state are shared, so it
 *     stays visible and the other sub-view's buttons wait for it); leaving the
 *     lane does, exactly as before, and says which writes were not sent.
 *   - Each sub-view has its own list, selection, choices and page state, and the
 *     list is edited IN PLACE after a write (see GroupsValue.stale): the server
 *     clears its cached list on every write, so re-reading would blank it.
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import * as api from '../../../services/api';
import type {
  AuthorDedupGroup,
  DedupCandidate,
  Operation,
  ValidationResult,
} from '../../../services/api';
import { ApiTimeoutError, isAbortError, isAuthRedirectError } from '../../../utils/apiFetch';
import { SESSION_EXPIRED_MESSAGE } from '../../../utils/apiResponse';
import { useRowSelection, type RowSelection } from '../../../hooks/useRowSelection';
import {
  asAuthorId,
  type AuthorId,
  type AuthorsAction,
  type SeriesEntityAction,
  type SeriesId,
} from '../reviewActions';
import { isKeyboardShortcutSuppressed } from './useDupesLane';
import {
  fetchAuthorGroups,
  fetchAuthorName,
  fetchSeriesGroups,
  validateAuthorName,
  validateSeriesName,
} from './authorsReads';
import {
  NO_SERIES_CHOICES,
  dropSeriesRows,
  inMergeAll,
  isSeriesMergeable,
  parseLeftSeriesIds,
  planSeriesMerge,
  renameSeriesRow,
  toSeriesGroup,
  type SeriesChoices,
  type SeriesGroup,
  type SeriesKindFilter,
} from './seriesGroups';

/** The toast API (ToastProvider's), with the optional action button an undo needs. */
type Toast = (
  message: string,
  severity?: 'success' | 'error' | 'warning' | 'info',
  action?: { label: string; onClick: () => void }
) => void;

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

/**
 * Which verb a group offers. The three partition the list, so the counts on the
 * kind chips add up to the total and a chip's number is exactly the rows it shows.
 * Precedence: a production company is never treated as a composite or a merge.
 */
export type AuthorGroupKind = 'variants' | 'composite' | 'production';

export type AuthorKindFilter = 'all' | AuthorGroupKind;

export interface AuthorRef {
  id: AuthorId;
  name: string;
}

export interface AuthorGroup {
  /** `String(canonical.id)`: stable across removals, unlike an index. */
  key: string;
  canonical: AuthorRef;
  variants: AuthorRef[];
  bookCount: number;
  suggestedName?: string;
  splitNames: string[];
  isProductionCompany: boolean;
  kind: AuthorGroupKind;
}

/** Wire group -> lane group. The one place plain numbers become AuthorIds. */
export function toAuthorGroup(g: AuthorDedupGroup): AuthorGroup {
  const splitNames = g.split_names ?? [];
  const isProductionCompany = Boolean(g.is_production_company);
  return {
    key: String(g.canonical.id),
    canonical: { id: asAuthorId(g.canonical.id), name: g.canonical.name },
    variants: (g.variants ?? []).map((v) => ({ id: asAuthorId(v.id), name: v.name })),
    bookCount: g.book_count,
    suggestedName: g.suggested_name,
    splitNames,
    isProductionCompany,
    kind: isProductionCompany ? 'production' : splitNames.length > 1 ? 'composite' : 'variants',
  };
}

export type ListStatus = 'loading' | 'error' | 'needs-scan' | 'empty' | 'populated';

type Load<T> =
  | { state: 'loading' }
  | { state: 'error'; message: string }
  | { state: 'ready'; value: T };

type Keyed<T> = { key: string; load: Load<T> };

function current<T>(keyed: Keyed<T> | null, key: string): Load<T> {
  return keyed && keyed.key === key ? keyed.load : { state: 'loading' };
}

interface GroupsValue {
  groups: AuthorGroup[];
  needsScan: boolean;
  /**
   * The server said its cached list is gone (needs_refresh) while this lane
   * already held groups. Every author write clears that cache, so this is what a
   * re-read looks like after the reviewer's own changes. The held groups are kept
   * (they already reflect those changes); the panel says they are not backed by
   * a fresh scan.
   */
  stale: boolean;
}

interface SeriesValue {
  groups: SeriesGroup[];
  totalSeries: number;
  needsScan: boolean;
  /** As GroupsValue.stale: the server's list is gone while this lane holds an edited one. */
  stale: boolean;
}

export type ValidationState =
  | { forName: string; state: 'loading' }
  | { forName: string; state: 'error'; message: string }
  | { forName: string; state: 'ready'; results: ValidationResult[] };

export interface CandidatesPage {
  rows: DedupCandidate[];
  total: number;
}

export interface AuthorsUrlFilters {
  /**
   * `?author=<id>`: focus the group that author belongs to (as canonical or as a
   * variant). A seed, read once on the first load and never mirrored back.
   */
  authorId: string | null;
  /**
   * `?view=series`: open the Series sub-view. A seed like `authorId`, read once
   * (the sub-view is local state afterwards; a click never writes the URL). The
   * caller passes it only when the URL landed on this lane, because `view` is also
   * the Duplicates lane's parameter.
   */
  view?: string | null;
}

/** The lane's sub-views. AI scans joins them in a later change. */
export type AuthorsSubView = 'authors' | 'series';

/** An unknown `?view=` opens Authors rather than nothing. */
export function parseAuthorsSubView(raw: string | null | undefined): AuthorsSubView {
  return raw === 'series' ? 'series' : 'authors';
}

export interface BooksTarget {
  key: string;
  anchor: HTMLElement | null;
}

/** Page sizes for the group list. */
export const AUTHOR_PAGE_SIZES = [10, 25, 50, 100] as const;
export const AUTHORS_DEFAULT_PAGE_SIZE = 25;
/** Author candidates per page. */
export const AUTHOR_CANDIDATES_PAGE = 25;
/** Concurrent name lookups for candidate rows. */
const NAME_LOOKUP_CONCURRENCY = 4;
/** Operation poll interval and per-poll deadline. */
export const AUTHORS_POLL_MS = 1000;
const POLL_REQUEST_TIMEOUT_MS = 30_000;

const EMPTY_NUMBERS: ReadonlySet<number> = new Set<number>();
const EMPTY_STRINGS: ReadonlySet<string> = new Set<string>();

function errorMessage(e: unknown, fallback: string): string {
  return e instanceof Error && e.message ? e.message : fallback;
}

/** `v:<id>` / `s:<name>`: what a role flag is attached to. */
const variantRoleKey = (id: AuthorId) => `v:${id}`;
const splitRoleKey = (name: string) => `s:${name}`;

interface Choices {
  /** Variants left out of a group's merge, by canonical key. */
  excluded: Readonly<Record<string, ReadonlySet<number>>>;
  /** Role flags (narrator) by canonical key. */
  narrators: Readonly<Record<string, ReadonlySet<string>>>;
}

const NO_CHOICES: Choices = { excluded: {}, narrators: {} };

/** What a merge of this group would do, given the current choices. */
export interface MergePlan {
  mergeIds: AuthorId[];
  narratorIds: AuthorId[];
}

export function planMerge(group: AuthorGroup, choices: Choices): MergePlan {
  const excluded = choices.excluded[group.key] ?? EMPTY_NUMBERS;
  const narrators = choices.narrators[group.key] ?? EMPTY_STRINGS;
  const active = group.variants.filter((v) => !excluded.has(v.id));
  return {
    narratorIds: active.filter((v) => narrators.has(variantRoleKey(v.id))).map((v) => v.id),
    mergeIds: active.filter((v) => !narrators.has(variantRoleKey(v.id))).map((v) => v.id),
  };
}

/**
 * Whether anything is left for a Merge to do: the group is a name-variants group
 * and at least one of its variants is not left out. A group whose every variant
 * is excluded has nothing to merge or reclassify, so it is neither selectable nor
 * counted by Merge All.
 */
export function hasMergeWork(group: AuthorGroup, excluded: ReadonlySet<number>): boolean {
  return group.kind === 'variants' && group.variants.some((v) => !excluded.has(v.id));
}

/** A group a Merge button applies to, given the reviewer's current choices. */
export function isMergeable(group: AuthorGroup, choices: Choices): boolean {
  return hasMergeWork(group, choices.excluded[group.key] ?? EMPTY_NUMBERS);
}

/** What one group's merge came to, for the toast and for a bulk run's totals. */
interface MergeOutcome {
  /**
   * `session`: the login expired; `timeout`: a request got no answer in time (so
   * the write may have run); `skipped`: nothing was sent.
   */
  result: 'merged' | 'reclassified' | 'skipped' | 'failed' | 'session' | 'timeout';
  message: string;
  name: string;
  /** Narrator reclassifications that failed; a bulk run sums them. */
  reclassifyFailures: number;
  /**
   * The list can no longer be trusted: a failure that does not say what it left
   * (a cancelled run). The caller re-reads, which lands on the stale banner when
   * the server's list is gone.
   */
  reread?: boolean;
}

/**
 * A merge that was sent whose outcome the lane never saw (see unconfirmedRef).
 * Authors and series are followed differently when they finish, so the entry says
 * which it is and, for a series, which rows the merge folded away.
 */
type Unconfirmed =
  | { entity: 'author'; opId: string; name: string }
  | { entity: 'series'; opId: string; name: string; mergeIds: number[] };

/** Removes `gone` variants from one group, and the group itself when none is left. */
function dropVariants(gs: AuthorGroup[], key: string, gone: ReadonlySet<number>): AuthorGroup[] {
  if (gone.size === 0) return gs;
  return gs.flatMap((g) => {
    if (g.key !== key) return [g];
    const rest = g.variants.filter((v) => !gone.has(v.id));
    return rest.length === 0 ? [] : [{ ...g, variants: rest }];
  });
}

/**
 * The ids a failed entities.author-merge run says it left behind. The server
 * writes them in this exact form (AuthorMergeFailedIDsPrefix in
 * internal/server/entities_ops.go); change both together. Null when the message
 * does not carry the list (any other failure).
 */
export function parseLeftIds(message: string): Set<number> | null {
  const m = /author merge left author ids \[([0-9 ]*)\]/.exec(message);
  if (!m) return null;
  return new Set(
    m[1]
      .split(' ')
      .filter(Boolean)
      .map(Number)
  );
}

/**
 * How long a write waits for its request to answer. The api.ts write wrappers
 * set no timeout and apiFetch has no default, so one hung request would hold the
 * lane's write queue (and its "busy" state) forever.
 */
export const WRITE_DEADLINE_MS = 60_000;

/**
 * A series rename waits for its queued operation inside the api.ts wrapper for up
 * to SERIES_RENAME_WAIT_MS, and says "rename still queued" when that runs out.
 * The lane's own deadline must come AFTER that, or it would pre-empt the
 * wrapper's more specific message with a generic "no answer". (A literal, not
 * `api.SERIES_RENAME_WAIT_MS + n`: a test that replaces the api module without
 * that constant would otherwise get a NaN timer that fires at once. The lane's
 * tests assert this stays above the wrapper's wait.)
 */
export const SERIES_RENAME_DEADLINE_MS = 75_000;

class WriteTimeoutError extends Error {
  readonly ms: number;
  constructor(ms = WRITE_DEADLINE_MS) {
    super(`no answer after ${ms / 1000}s`);
    this.name = 'WriteTimeoutError';
    this.ms = ms;
  }
}

/** Rejects with WriteTimeoutError if `work` has not settled by the deadline. */
function withDeadline<T>(work: Promise<T>, ms = WRITE_DEADLINE_MS): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const timer = setTimeout(() => reject(new WriteTimeoutError(ms)), ms);
    work.then(
      (v) => {
        clearTimeout(timer);
        resolve(v);
      },
      (e: unknown) => {
        clearTimeout(timer);
        reject(e);
      }
    );
  });
}

/** A bulk merge in progress. `entity` says which sub-view started it, so the other does not draw its bar. */
export interface BulkProgress {
  done: number;
  total: number;
  entity: 'author' | 'series';
}

/** The series row whose name is being edited. */
export interface SeriesEditing {
  groupKey: string;
  seriesId: SeriesId;
  initial: string;
}

/** A rename that can still be undone: the operation to revert and the names either side. */
export interface SeriesRenameRecord {
  opId: string;
  oldName: string;
  newName: string;
}

/** Page sizes for the series group list. */
export const SERIES_PAGE_SIZES = AUTHOR_PAGE_SIZES;
export const SERIES_DEFAULT_PAGE_SIZE = AUTHORS_DEFAULT_PAGE_SIZE;

/** The Series sub-view's own state. The write state (pending, busy, bulk) is the lane's. */
export interface SeriesLane {
  status: ListStatus;
  error: string | null;
  /** The whole list, unfiltered. */
  groups: SeriesGroup[];
  /** Every series in the library, duplicate or not (the scan's denominator). */
  totalSeries: number;
  /** Per-kind counts; `exact` + `subseries` = `all` = `groups.length`. */
  kindCounts: Record<SeriesKindFilter, number>;
  filter: SeriesKindFilter;
  setFilter: (f: SeriesKindFilter) => void;
  filtered: SeriesGroup[];
  pageGroups: SeriesGroup[];
  page: number;
  setPage: (p: number) => void;
  pageSize: number;
  setPageSize: (n: number) => void;
  /** Groups a Merge button applies to (two or more series included), across the whole list. */
  mergeable: SeriesGroup[];
  /** The groups Merge All takes: same-name groups with something to merge. */
  mergeAll: SeriesGroup[];
  /** Selectable (mergeable) groups matching the current kind filter. */
  selectableTotal: number;
  /** The list is the lane's own edit of an earlier read; the server's copy is gone. */
  stale: boolean;
  selection: RowSelection<string>;
  /** Selected groups that still exist and are mergeable: the rows `selectedCount` counts. */
  selectedGroups: SeriesGroup[];
  selectedCount: number;
  choices: SeriesChoices;
  /** Validation by group key. */
  validation: Readonly<Record<string, ValidationState>>;
  /** Renames that can still be undone, by series id. */
  renames: Readonly<Record<number, SeriesRenameRecord>>;
  editing: SeriesEditing | null;
  startEdit: (groupKey: string, seriesId: SeriesId, initial: string) => void;
  cancelEdit: () => void;
  focusedKey: string | null;
  helpOpen: boolean;
  setHelpOpen: (open: boolean) => void;
  validate: (key: string) => void;
  /** Starts the series scan, then reloads the groups when it finishes. */
  refresh: () => void;
  /** Re-reads the groups without starting a scan. */
  reload: () => void;
}

// ---------------------------------------------------------------------------
// The lane
// ---------------------------------------------------------------------------

export interface AuthorsLane {
  /** Which sub-view is showing. Local state, seeded once from `?view=`. */
  view: AuthorsSubView;
  setView: (v: AuthorsSubView) => void;
  /** The Series sub-view's list and choices. */
  series: SeriesLane;

  // ---- the Authors sub-view ----
  status: ListStatus;
  error: string | null;
  /** The whole list, unfiltered. */
  groups: AuthorGroup[];
  /** Per-kind counts; they sum to `groups.length`. */
  kindCounts: Record<AuthorKindFilter, number>;
  filter: AuthorKindFilter;
  setFilter: (f: AuthorKindFilter) => void;
  /** The filtered list and the page of it on screen. */
  filtered: AuthorGroup[];
  pageGroups: AuthorGroup[];
  page: number;
  setPage: (p: number) => void;
  pageSize: number;
  setPageSize: (n: number) => void;

  /** Groups a Merge button applies to, across the whole list (not the filter). */
  mergeable: AuthorGroup[];
  /** Selectable (mergeable) groups matching the current kind filter. */
  selectableTotal: number;
  /** The list is the lane's own edit of an earlier read; the server's copy is gone. */
  stale: boolean;
  selection: RowSelection<string>;
  /** Selected groups that still exist: the rows `selectedCount` counts. */
  selectedGroups: AuthorGroup[];
  selectedCount: number;

  choices: Choices;
  validation: Readonly<Record<string, ValidationState>>;
  pendingKeys: ReadonlySet<string>;
  /** A bulk merge is running: `done` of `total` groups processed so far. */
  bulk: BulkProgress | null;
  scanning: boolean;
  /** Anything that writes is in flight: bulk buttons wait for it. */
  busy: boolean;

  focusedKey: string | null;
  editing: { key: string; initial: string } | null;
  startEdit: (key: string, initial: string) => void;
  cancelEdit: () => void;
  booksFor: BooksTarget | null;
  openBooks: (key: string, anchor: HTMLElement | null) => void;
  closeBooks: () => void;
  helpOpen: boolean;
  setHelpOpen: (open: boolean) => void;

  /** Every action of the lane, Authors and Series alike (they name their `entity`). */
  dispatch: (action: AuthorsAction) => void;
  validate: (key: string) => void;
  /** Starts the author scan, then reloads the groups when it finishes. */
  refresh: () => void;
  /** Re-reads the groups without starting a scan. */
  reload: () => void;
  focusGroup: (key: string) => void;

  candidates: {
    status: 'loading' | 'error' | 'empty' | 'populated';
    error: string | null;
    rows: DedupCandidate[];
    total: number;
    offset: number;
    setOffset: (n: number) => void;
    /** Display names by author id (from the groups, then looked up). */
    nameOf: (authorId: string) => string;
    reload: () => void;
  };
}

export function useAuthorsLane(
  toast: Toast,
  active = true,
  urlFilters: AuthorsUrlFilters = { authorId: null }
): AuthorsLane {
  // ---- which sub-view is showing -------------------------------------------
  // `active` is "the lane is the visible one"; `authorsOn` / `seriesOn` add "and
  // this sub-view is the one on screen". Reads, keyboard and ephemeral UI follow
  // the sub-view; the write queue follows the lane (see the header note).
  const [view, setView] = useState<AuthorsSubView>(() => parseAuthorsSubView(urlFilters.view));
  const authorsOn = active && view === 'authors';
  const seriesOn = active && view === 'series';

  // ---- request identity ----------------------------------------------------
  // A new activation is a new request: re-entering the lane (or a sub-view) must
  // not paint what it held when it was left. Derived during render (React's
  // "adjust state when a prop changes" pattern) so there is no committed frame in
  // between.
  const [activation, setActivation] = useState(0);
  const [prevAuthorsOn, setPrevAuthorsOn] = useState(authorsOn);
  // Declared ahead of the block below, which clears them on leaving.
  const [editing, setEditing] = useState<{ key: string; initial: string } | null>(null);
  const [booksFor, setBooksFor] = useState<BooksTarget | null>(null);
  const [helpOpen, setHelpOpen] = useState(false);
  if (prevAuthorsOn !== authorsOn) {
    setPrevAuthorsOn(authorsOn);
    if (authorsOn) setActivation((a) => a + 1);
    else {
      // An open popover is anchored to a card that is about to unmount, and an
      // open editor or help dialog belongs to a screen nobody is on.
      setBooksFor(null);
      setEditing(null);
      setHelpOpen(false);
    }
  }
  const [seriesActivation, setSeriesActivation] = useState(0);
  const [prevSeriesOn, setPrevSeriesOn] = useState(seriesOn);
  const [seriesEditing, setSeriesEditing] = useState<SeriesEditing | null>(null);
  const [seriesHelpOpen, setSeriesHelpOpen] = useState(false);
  if (prevSeriesOn !== seriesOn) {
    setPrevSeriesOn(seriesOn);
    if (seriesOn) setSeriesActivation((a) => a + 1);
    else {
      setSeriesEditing(null);
      setSeriesHelpOpen(false);
    }
  }
  const [reloadToken, setReloadToken] = useState(0);
  const groupsKey = `${activation}:${reloadToken}`;

  // ---- groups ---------------------------------------------------------------
  const [groupsResult, setGroupsResult] = useState<Keyed<GroupsValue> | null>(null);
  const groupsRef = useRef<Keyed<GroupsValue> | null>(null);
  const commitResult = useCallback((next: Keyed<GroupsValue>) => {
    groupsRef.current = next;
    setGroupsResult(next);
  }, []);
  /** Edit the list the lane currently holds. Reads the latest, writes the latest. */
  const commitGroups = useCallback(
    (fn: (gs: AuthorGroup[]) => AuthorGroup[]) => {
      const prev = groupsRef.current;
      if (!prev || prev.load.state !== 'ready') return;
      const next = fn(prev.load.value.groups);
      if (next === prev.load.value.groups) return;
      commitResult({
        key: prev.key,
        load: { state: 'ready', value: { ...prev.load.value, groups: next } },
      });
    },
    [commitResult]
  );

  const groupsLoad = current(groupsResult, groupsKey);
  const groups = useMemo(
    () => (groupsLoad.state === 'ready' ? groupsLoad.value.groups : []),
    [groupsLoad]
  );
  const needsScan = groupsLoad.state === 'ready' && groupsLoad.value.needsScan;
  const stale = groupsLoad.state === 'ready' && groupsLoad.value.stale;
  // A popover or editor on a group that has left the list (merged, split,
  // resolved) would point at a card that is gone.
  if (groupsLoad.state === 'ready') {
    if (booksFor && !groups.some((g) => g.key === booksFor.key)) setBooksFor(null);
    if (editing && !groups.some((g) => g.key === editing.key)) setEditing(null);
  }

  // ---- choices (excluded variants, narrator flags) ---------------------------
  const [choices, setChoices] = useState<Choices>(NO_CHOICES);
  const choicesRef = useRef<Choices>(NO_CHOICES);
  const commitChoices = useCallback((fn: (c: Choices) => Choices) => {
    const next = fn(choicesRef.current);
    if (next === choicesRef.current) return;
    choicesRef.current = next;
    setChoices(next);
  }, []);

  // ---- view state -------------------------------------------------------------
  const [filter, setFilterState] = useState<AuthorKindFilter>('all');
  const [pageState, setPageState] = useState(0);
  const [pageSize, setPageSizeState] = useState<number>(AUTHORS_DEFAULT_PAGE_SIZE);
  const [focusedKey, setFocusedKey] = useState<string | null>(null);

  const kindCounts = useMemo(() => {
    const counts: Record<AuthorKindFilter, number> = {
      all: groups.length,
      variants: 0,
      composite: 0,
      production: 0,
    };
    for (const g of groups) counts[g.kind] += 1;
    return counts;
  }, [groups]);
  const filtered = useMemo(
    () => (filter === 'all' ? groups : groups.filter((g) => g.kind === filter)),
    [groups, filter]
  );
  const lastPage = Math.max(0, Math.ceil(filtered.length / pageSize) - 1);
  const page = Math.min(pageState, lastPage);
  const pageGroups = useMemo(
    () => filtered.slice(page * pageSize, page * pageSize + pageSize),
    [filtered, page, pageSize]
  );
  const setFilter = useCallback((f: AuthorKindFilter) => {
    setFilterState(f);
    setPageState(0);
  }, []);
  const setPage = useCallback((p: number) => setPageState(Math.max(0, p)), []);
  const setPageSize = useCallback((n: number) => {
    setPageSizeState(n);
    setPageState(0);
  }, []);

  // ---- selection --------------------------------------------------------------
  const mergeable = useMemo(() => groups.filter((g) => isMergeable(g, choices)), [groups, choices]);
  const mergeableKeys = useMemo(() => new Set(mergeable.map((g) => g.key)), [mergeable]);
  const isUnselectable = useCallback((key: string) => !mergeableKeys.has(key), [mergeableKeys]);
  const pageKeys = useMemo(() => pageGroups.map((g) => g.key), [pageGroups]);
  const allSelectableKeys = useMemo(
    () => filtered.filter((g) => isMergeable(g, choices)).map((g) => g.key),
    [filtered, choices]
  );
  const selection = useRowSelection<string>({
    pageKeys,
    totalMatching: allSelectableKeys.length,
    resetKey: `${filter}|${pageSize}`,
    isDisabled: isUnselectable,
    allKeys: allSelectableKeys,
  });
  const { clear: clearSelection } = selection;
  // The rows the count counts: a group merged (or removed) while it was still
  // ticked leaves a stale key in `selection.selected`, and a count taken from the
  // set would then claim a row nobody can see.
  const selectedGroups = useMemo(
    () => groups.filter((g) => selection.selected.has(g.key) && mergeableKeys.has(g.key)),
    [groups, selection.selected, mergeableKeys]
  );

  // ---- series: the list ----------------------------------------------------------
  const [seriesReloadToken, setSeriesReloadToken] = useState(0);
  const seriesKey = `${seriesActivation}:${seriesReloadToken}`;
  const [seriesResult, setSeriesResult] = useState<Keyed<SeriesValue> | null>(null);
  const seriesRef = useRef<Keyed<SeriesValue> | null>(null);
  const commitSeriesResult = useCallback((next: Keyed<SeriesValue>) => {
    seriesRef.current = next;
    setSeriesResult(next);
  }, []);
  /** Edit the series list the lane currently holds. Reads the latest, writes the latest. */
  const commitSeries = useCallback(
    (fn: (gs: SeriesGroup[]) => SeriesGroup[]) => {
      const prev = seriesRef.current;
      if (!prev || prev.load.state !== 'ready') return;
      const next = fn(prev.load.value.groups);
      if (next === prev.load.value.groups) return;
      commitSeriesResult({
        key: prev.key,
        load: { state: 'ready', value: { ...prev.load.value, groups: next } },
      });
    },
    [commitSeriesResult]
  );
  const seriesLoad = current(seriesResult, seriesKey);
  const seriesGroups = useMemo(
    () => (seriesLoad.state === 'ready' ? seriesLoad.value.groups : []),
    [seriesLoad]
  );
  const seriesTotal = seriesLoad.state === 'ready' ? seriesLoad.value.totalSeries : 0;
  const seriesNeedsScan = seriesLoad.state === 'ready' && seriesLoad.value.needsScan;
  const seriesStale = seriesLoad.state === 'ready' && seriesLoad.value.stale;
  // An editor on a series whose group has left the list (merged away) would point
  // at a row that is gone.
  if (seriesLoad.state === 'ready' && seriesEditing && !seriesGroups.some((g) => g.key === seriesEditing.groupKey)) {
    setSeriesEditing(null);
  }

  // ---- series: choices (left-out series, narrator flags) --------------------------
  const [seriesChoices, setSeriesChoices] = useState<SeriesChoices>(NO_SERIES_CHOICES);
  const seriesChoicesRef = useRef<SeriesChoices>(NO_SERIES_CHOICES);
  const commitSeriesChoices = useCallback((fn: (c: SeriesChoices) => SeriesChoices) => {
    const next = fn(seriesChoicesRef.current);
    if (next === seriesChoicesRef.current) return;
    seriesChoicesRef.current = next;
    setSeriesChoices(next);
  }, []);

  // ---- series: view state and selection -------------------------------------------
  const [seriesFilter, setSeriesFilterState] = useState<SeriesKindFilter>('all');
  const [seriesPageState, setSeriesPageState] = useState(0);
  const [seriesPageSize, setSeriesPageSizeState] = useState<number>(SERIES_DEFAULT_PAGE_SIZE);
  const [seriesFocusedKey, setSeriesFocusedKey] = useState<string | null>(null);
  const seriesKindCounts = useMemo(() => {
    const counts: Record<SeriesKindFilter, number> = { all: seriesGroups.length, exact: 0, subseries: 0 };
    for (const g of seriesGroups) counts[g.matchKind] += 1;
    return counts;
  }, [seriesGroups]);
  const seriesFiltered = useMemo(
    () => (seriesFilter === 'all' ? seriesGroups : seriesGroups.filter((g) => g.matchKind === seriesFilter)),
    [seriesGroups, seriesFilter]
  );
  const seriesLastPage = Math.max(0, Math.ceil(seriesFiltered.length / seriesPageSize) - 1);
  const seriesPage = Math.min(seriesPageState, seriesLastPage);
  const seriesPageGroups = useMemo(
    () => seriesFiltered.slice(seriesPage * seriesPageSize, seriesPage * seriesPageSize + seriesPageSize),
    [seriesFiltered, seriesPage, seriesPageSize]
  );
  const setSeriesFilter = useCallback((f: SeriesKindFilter) => {
    setSeriesFilterState(f);
    setSeriesPageState(0);
  }, []);
  const setSeriesPage = useCallback((p: number) => setSeriesPageState(Math.max(0, p)), []);
  const setSeriesPageSize = useCallback((n: number) => {
    setSeriesPageSizeState(n);
    setSeriesPageState(0);
  }, []);

  const seriesMergeable = useMemo(
    () => seriesGroups.filter((g) => isSeriesMergeable(g, seriesChoices)),
    [seriesGroups, seriesChoices]
  );
  const seriesMergeAll = useMemo(
    () => seriesGroups.filter((g) => inMergeAll(g, seriesChoices)),
    [seriesGroups, seriesChoices]
  );
  const seriesMergeableKeys = useMemo(() => new Set(seriesMergeable.map((g) => g.key)), [seriesMergeable]);
  const isSeriesUnselectable = useCallback((key: string) => !seriesMergeableKeys.has(key), [seriesMergeableKeys]);
  const seriesPageKeys = useMemo(() => seriesPageGroups.map((g) => g.key), [seriesPageGroups]);
  const allSeriesSelectableKeys = useMemo(
    () => seriesFiltered.filter((g) => isSeriesMergeable(g, seriesChoices)).map((g) => g.key),
    [seriesFiltered, seriesChoices]
  );
  const seriesSelection = useRowSelection<string>({
    pageKeys: seriesPageKeys,
    totalMatching: allSeriesSelectableKeys.length,
    resetKey: `${seriesFilter}|${seriesPageSize}`,
    isDisabled: isSeriesUnselectable,
    allKeys: allSeriesSelectableKeys,
  });
  const { clear: clearSeriesSelection } = seriesSelection;
  // The rows the count counts, as for authors: a group merged while ticked leaves a
  // stale key in the set, and a count taken from the set would claim a row nobody
  // can see.
  const selectedSeriesGroups = useMemo(
    () => seriesGroups.filter((g) => seriesSelection.selected.has(g.key) && seriesMergeableKeys.has(g.key)),
    [seriesGroups, seriesSelection.selected, seriesMergeableKeys]
  );

  // ---- lifetime -----------------------------------------------------------------
  // Aborted when the lane stops being the visible one and when the workspace
  // unmounts. Created in the effect so each mount (StrictMode runs two) has its own.
  const lifeRef = useRef<AbortController>(abortedController());
  const mountedRef = useRef(false);
  useEffect(() => {
    mountedRef.current = true;
    return () => {
      mountedRef.current = false;
    };
  }, []);
  useEffect(() => {
    if (!active) return;
    const ctl = new AbortController();
    lifeRef.current = ctl;
    return () => ctl.abort();
  }, [active]);

  // ---- loading the groups --------------------------------------------------------
  const seedRef = useRef<number | null>(parseAuthorId(urlFilters.authorId));
  useEffect(() => {
    if (!authorsOn) return;
    const ctl = new AbortController();
    fetchAuthorGroups(ctl.signal)
      .then((r) => {
        if (ctl.signal.aborted) return;
        const mapped = r.groups.map(toAuthorGroup);
        // Read the latest, now: the lane may have edited its list since the
        // request was sent.
        const prev = groupsRef.current;
        const held = prev && prev.load.state === 'ready' ? prev.load.value.groups : [];
        if (r.needsRefresh && mapped.length === 0 && held.length > 0) {
          // The server's list is gone -- every author write clears it, including
          // this lane's own -- but the lane holds groups that already reflect
          // those writes. Replacing them with "not scanned yet" would throw away
          // real rows; keep them and say they are not backed by a fresh scan.
          commitResult({
            key: groupsKey,
            load: { state: 'ready', value: { groups: held, needsScan: false, stale: true } },
          });
          return;
        }
        commitResult({
          key: groupsKey,
          load: { state: 'ready', value: { groups: mapped, needsScan: r.needsRefresh, stale: false } },
        });
        const seed = seedRef.current;
        if (seed !== null) {
          seedRef.current = null;
          const at = mapped.find((g) => g.canonical.id === seed || g.variants.some((v) => v.id === seed));
          if (at) {
            setFilterState('all');
            setFocusedKey(at.key);
            setPageState(Math.floor(mapped.indexOf(at) / AUTHORS_DEFAULT_PAGE_SIZE));
          }
        }
      })
      .catch((e: unknown) => {
        if (ctl.signal.aborted || isAbortError(e)) return;
        commitResult({
          key: groupsKey,
          load: { state: 'error', message: errorMessage(e, 'Failed to fetch author duplicates') },
        });
      });
    return () => ctl.abort();
  }, [authorsOn, groupsKey, commitResult]);

  // ---- loading the series groups ---------------------------------------------------
  useEffect(() => {
    if (!seriesOn) return;
    const ctl = new AbortController();
    fetchSeriesGroups(ctl.signal)
      .then((r) => {
        if (ctl.signal.aborted) return;
        const mapped = r.groups.map(toSeriesGroup);
        // The latest, now: the lane may have edited its list since the request
        // was sent.
        const prev = seriesRef.current;
        const held = prev && prev.load.state === 'ready' ? prev.load.value : null;
        if (r.needsRefresh && mapped.length === 0 && held && held.groups.length > 0) {
          // As for authors: every series write clears the server's list, including
          // this lane's own, but the lane holds groups that already reflect those
          // writes. Replacing them with "not scanned yet" would throw away real
          // rows; keep them and say they are not backed by a fresh scan.
          commitSeriesResult({
            key: seriesKey,
            load: { state: 'ready', value: { ...held, needsScan: false, stale: true } },
          });
          return;
        }
        commitSeriesResult({
          key: seriesKey,
          load: {
            state: 'ready',
            value: { groups: mapped, totalSeries: r.totalSeries, needsScan: r.needsRefresh, stale: false },
          },
        });
      })
      .catch((e: unknown) => {
        if (ctl.signal.aborted || isAbortError(e)) return;
        commitSeriesResult({
          key: seriesKey,
          load: { state: 'error', message: errorMessage(e, 'Failed to fetch series duplicates') },
        });
      });
    return () => ctl.abort();
  }, [seriesOn, seriesKey, commitSeriesResult]);

  // ---- author candidates ---------------------------------------------------------
  const [candOffset, setCandOffset] = useState(0);
  const [candToken, setCandToken] = useState(0);
  const candKey = `${activation}:${candToken}:${reloadToken}:${candOffset}`;
  const [candResult, setCandResult] = useState<Keyed<CandidatesPage> | null>(null);
  const candLoad = current(candResult, candKey);
  useEffect(() => {
    if (!authorsOn) return;
    const ctl = new AbortController();
    api
      .getDedupCandidates(
        {
          entity_type: 'author',
          status: 'pending',
          limit: AUTHOR_CANDIDATES_PAGE,
          offset: candOffset,
        },
        { signal: ctl.signal }
      )
      .then((r) => {
        if (ctl.signal.aborted) return;
        setCandResult({
          key: candKey,
          load: { state: 'ready', value: { rows: r.candidates ?? [], total: r.total ?? 0 } },
        });
      })
      .catch((e: unknown) => {
        if (ctl.signal.aborted || isAbortError(e)) return;
        setCandResult({
          key: candKey,
          load: { state: 'error', message: errorMessage(e, 'Failed to fetch author candidates') },
        });
      });
    return () => ctl.abort();
  }, [authorsOn, candKey, candOffset]);

  // Names for the candidate rows: the groups already hold many; the rest are
  // looked up, a few at a time, and a failed lookup falls back to "Author #id"
  // rather than failing the row (the pair itself is real either way).
  const [looked, setLooked] = useState<Keyed<Record<string, string>> | null>(null);
  const groupNames = useMemo(() => {
    const m = new Map<string, string>();
    for (const g of groups) {
      m.set(String(g.canonical.id), g.canonical.name);
      for (const v of g.variants) m.set(String(v.id), v.name);
    }
    return m;
  }, [groups]);
  const candRows = candLoad.state === 'ready' ? candLoad.value.rows : null;
  // The ids the rows name that no group knows, as one string: the effect below
  // depends on THIS, not on `groupNames`, which changes whenever the list is
  // edited and would otherwise restart every lookup on each merge.
  const wantedKey = useMemo(() => {
    if (!candRows) return '';
    const wanted = new Set<string>();
    for (const r of candRows) {
      for (const id of [r.entity_a_id, r.entity_b_id]) if (!groupNames.has(id)) wanted.add(id);
    }
    return [...wanted].sort().join(',');
  }, [candRows, groupNames]);
  // Ids already looked up (or given up on) for this page of candidates.
  const askedRef = useRef<{ key: string; ids: Set<string> }>({ key: '', ids: new Set() });
  useEffect(() => {
    if (!authorsOn || wantedKey === '') return;
    if (askedRef.current.key !== candKey) askedRef.current = { key: candKey, ids: new Set() };
    const asked = askedRef.current.ids;
    const queue = wantedKey.split(',').filter((id) => !asked.has(id));
    if (queue.length === 0) return;
    const ctl = new AbortController();
    const lookups = Array.from({ length: Math.min(NAME_LOOKUP_CONCURRENCY, queue.length) }, async () => {
      for (let id = queue.shift(); id !== undefined; id = queue.shift()) {
        const n = Number(id);
        if (!Number.isFinite(n)) continue;
        try {
          const name = await fetchAuthorName(n, ctl.signal);
          if (ctl.signal.aborted) continue;
          asked.add(id);
          if (!name) continue;
          setLooked((prev) => ({
            key: candKey,
            load: {
              state: 'ready',
              value: {
                ...(prev && prev.key === candKey && prev.load.state === 'ready' ? prev.load.value : {}),
                [id]: name,
              },
            },
          }));
        } catch (e) {
          // A failed lookup falls back to the id; an aborted one is retried
          // by the next run, so it is not marked as asked.
          if (!ctl.signal.aborted && !isAbortError(e)) asked.add(id);
        }
      }
    });
    void Promise.all(lookups);
    return () => ctl.abort();
  }, [authorsOn, wantedKey, candKey]);
  const lookedNames = looked && looked.key === candKey && looked.load.state === 'ready' ? looked.load.value : null;
  const nameOf = useCallback(
    (authorId: string) => groupNames.get(authorId) ?? lookedNames?.[authorId] ?? `Author #${authorId}`,
    [groupNames, lookedNames]
  );

  // ---- writes: pending marks, the queue, operation waits --------------------------
  const pendingRef = useRef<Set<string>>(new Set());
  const [pendingKeys, setPendingKeys] = useState<ReadonlySet<string>>(() => new Set());
  const markPending = useCallback((key: string, on: boolean) => {
    if (!mountedRef.current) return;
    if (on) pendingRef.current.add(key);
    else pendingRef.current.delete(key);
    setPendingKeys(new Set(pendingRef.current));
  }, []);
  const [bulk, setBulk] = useState<BulkProgress | null>(null);
  const [scanning, setScanning] = useState(false);
  // The ref is what the guard reads: two clicks inside one render must not
  // start two scans, and state would still say "not scanning" for the second.
  const scanningRef = useRef(false);
  const busy = pendingKeys.size > 0 || bulk !== null || scanning;

  /**
   * Merges that were SENT but whose outcome this lane never saw, because the
   * lane was left while it waited. Outlives the activation on purpose: coming
   * back, those groups stay locked and their operations are followed again
   * (see the effect below) instead of being offered for a second merge.
   */
  const unconfirmedRef = useRef(new Map<string, Unconfirmed>());

  const queueRef = useRef<Promise<void>>(Promise.resolve());
  /**
   * Runs `task` after every earlier write has finished. `task` must not throw.
   *
   * It is handed the lane's lifetime signal AS IT WAS when the write was queued.
   * A write still waiting when the lane is left is not sent -- `onSkip` clears
   * its pending marks and says so instead -- because leaving the lane stops its
   * requests, and a queued merge that fired a minute later would be a request
   * from a screen nobody is looking at. (Re-entering the lane makes a new
   * signal; it does not revive what was queued under the old one.)
   *
   * This queue is the lane's own rather than the dupes lane's `runSequential`:
   * that helper runs one ids-array to completion inside one action, while this
   * lane has several kinds of write (merge, split, resolve, scan) that must also
   * wait for each other, and accepts a second click while the first is running.
   */
  const enqueue = useCallback((task: (life: AbortSignal) => Promise<void>, onSkip: () => void) => {
    const life = lifeRef.current.signal;
    const run = queueRef.current.then(() => {
      if (life.aborted) {
        onSkip();
        return undefined;
      }
      return task(life);
    });
    queueRef.current = run.catch(() => undefined);
  }, []);

  /** Waits for a started operation. `ok` is true only for 'completed'. */
  const waitFor = useCallback(
    async (op: Operation, life: AbortSignal): Promise<{ ok: boolean; message: string }> => {
      const final = await api.pollOperationV2(op.id, undefined, AUTHORS_POLL_MS, {
        signal: life,
        requestTimeoutMs: POLL_REQUEST_TIMEOUT_MS,
      });
      return {
        ok: final.status === 'completed',
        message: final.error_message || final.status,
      };
    },
    []
  );

  const reload = useCallback(() => {
    setReloadToken((t) => t + 1);
  }, []);
  const reloadSeries = useCallback(() => {
    setSeriesReloadToken((t) => t + 1);
  }, []);
  const candidatesReload = useCallback(() => setCandToken((t) => t + 1), []);

  /** Tells the caller a thrown error was the lane being left, not a failure. */
  const abandoned = useCallback((e: unknown) => isAbortError(e) || !mountedRef.current, []);

  /**
   * A write that got no answer is not a failure: say it may have run, and re-read
   * (the sub-view's own list: `again` defaults to the Authors one).
   */
  const timedOut = useCallback(
    (what: string, again: () => void = reload, ms: number = WRITE_DEADLINE_MS) => {
      if (!mountedRef.current) return;
      toast(`No answer after ${ms / 1000}s — the ${what} may have run; re-reading.`, 'warning');
      again();
    },
    [reload, toast]
  );

  /**
   * Queued merges that were never sent because the lane was left. `names` says
   * WHICH (a handful are listed, the rest counted), so the reviewer knows what to
   * redo rather than only how many.
   */
  const notSent = useCallback(
    (n: number, names: string[] = []) => {
      if (n <= 0 || !mountedRef.current) return;
      const shown = names.slice(0, 5).map((x) => `"${x}"`);
      const which =
        shown.length === 0 ? '' : `: ${shown.join(', ')}${names.length > shown.length ? `, and ${names.length - shown.length} more` : ''}`;
      toast(
        `${n} queued merge${n === 1 ? ' was' : 's were'} not sent because you left the lane${which}.`,
        'warning'
      );
    },
    [toast]
  );

  /** The name a series group goes by in a message, from the list as it is now. */
  const seriesLabel = useCallback((key: string) => {
    const cur = seriesRef.current;
    const g = cur && cur.load.state === 'ready' ? cur.load.value.groups.find((x) => x.key === key) : undefined;
    return g?.name ?? key;
  }, []);

  // ---- actions ------------------------------------------------------------------------

  /**
   * One group's merge, run inside the queue. Returns what happened so a bulk run
   * can total it; the caller owns the toast. The list is edited here, from what
   * the server confirmed: the whole group goes when the merge completed, only the
   * variants that really left when it partly failed.
   */
  const mergeOne = useCallback(
    async (key: string, life: AbortSignal): Promise<MergeOutcome> => {
      // Read NOW: the list and the choices as they are when this turn comes.
      const cur = groupsRef.current;
      const group =
        cur && cur.load.state === 'ready' ? cur.load.value.groups.find((g) => g.key === key) : undefined;
      const outcome = (
        result: MergeOutcome['result'],
        message: string,
        name: string,
        reclassifyFailures = 0
      ): MergeOutcome => ({ result, message, name, reclassifyFailures });
      if (!group) return outcome('skipped', 'the group is no longer in the list', key);
      const { mergeIds, narratorIds } = planMerge(group, choicesRef.current);
      const name = group.canonical.name;
      if (mergeIds.length === 0 && narratorIds.length === 0) {
        // Every variant is left out: there is nothing to merge or reclassify, and
        // saying "merged" would be a lie about a request that was never made.
        return outcome('skipped', 'every variant is left out of this merge', name);
      }

      const reclassified: AuthorId[] = [];
      let reclassifyFailures = 0;
      for (const id of narratorIds) {
        throwIfLeft(life);
        try {
          await withDeadline(api.reclassifyAuthorAsNarrator(id));
          reclassified.push(id);
        } catch (e) {
          if (abandoned(e)) throw e;
          if (e instanceof WriteTimeoutError) return outcome('timeout', 'no answer', name, reclassifyFailures);
          if (isAuthRedirectError(e)) return outcome('session', SESSION_EXPIRED_MESSAGE, name, reclassifyFailures);
          reclassifyFailures += 1;
        }
      }
      const suffix =
        reclassifyFailures > 0
          ? ` (${reclassifyFailures} narrator reclassification${reclassifyFailures === 1 ? '' : 's'} failed)`
          : '';
      if (mergeIds.length === 0) {
        // Narrator-only: the server's duplicate list is gone (every write
        // invalidates it), so edit ours instead of re-reading it.
        const gone = new Set<number>(reclassified);
        commitGroups((gs) => dropVariants(gs, key, gone));
        return outcome(
          'reclassified',
          `Reclassified ${reclassified.length} variant(s) as narrator${suffix}`,
          name,
          reclassifyFailures
        );
      }
      throwIfLeft(life);
      try {
        const op = await withDeadline(api.mergeAuthors(group.canonical.id, mergeIds));
        unconfirmedRef.current.set(key, { entity: 'author', opId: op.id, name });
        const done = await waitFor(op, life);
        unconfirmedRef.current.delete(key);
        if (!done.ok) {
          const left = parseLeftIds(done.message);
          if (left) {
            // A partial merge: keep the group, minus what really went.
            const gone = new Set<number>([...reclassified, ...mergeIds.filter((id) => !left.has(id))]);
            commitGroups((gs) => dropVariants(gs, key, gone));
            return outcome(
              'failed',
              `${left.size} author record(s) were not merged (ids ${[...left].join(', ')}); the rest were${suffix}`,
              name,
              reclassifyFailures
            );
          }
          return outcome('failed', `${done.message}${suffix}`, name, reclassifyFailures);
        }
      } catch (e) {
        // An abandon leaves the entry in `unconfirmedRef`: the merge was sent.
        if (abandoned(e)) throw e;
        unconfirmedRef.current.delete(key);
        if (e instanceof WriteTimeoutError) return outcome('timeout', 'no answer', name, reclassifyFailures);
        if (isAuthRedirectError(e)) return outcome('session', SESSION_EXPIRED_MESSAGE, name, reclassifyFailures);
        return outcome('failed', `${errorMessage(e, 'merge request failed')}${suffix}`, name, reclassifyFailures);
      }
      commitGroups((gs) => gs.filter((g) => g.key !== key));
      return outcome('merged', `Merged author(s) into "${name}"${suffix}`, name, reclassifyFailures);
    },
    [abandoned, commitGroups, waitFor]
  );

  /**
   * One series group's merge, run inside the queue; the Series twin of mergeOne.
   * The kept series and the ones folded into it are read NOW, from the list and
   * choices as they are when this turn comes. The list is edited from what the
   * server confirmed: the folded rows go when the merge completed, only the ones
   * that really left when it partly failed, nothing when it failed without saying.
   */
  const mergeSeriesOne = useCallback(
    async (key: string, life: AbortSignal): Promise<MergeOutcome> => {
      const cur = seriesRef.current;
      const group =
        cur && cur.load.state === 'ready' ? cur.load.value.groups.find((g) => g.key === key) : undefined;
      const outcome = (
        result: MergeOutcome['result'],
        message: string,
        name: string,
        reclassifyFailures = 0,
        reread = false
      ): MergeOutcome => ({ result, message, name, reclassifyFailures, reread });
      if (!group) return outcome('skipped', 'the group is no longer in the list', key);
      const name = group.name;
      const plan = planSeriesMerge(group, seriesChoicesRef.current);
      if (!plan) return outcome('skipped', 'fewer than two series are included in this merge', name);
      const keepName = group.series.find((r) => r.id === plan.keepId)?.name ?? name;

      // Reclassify the flagged authors first, as the old tab did. A failure here
      // does not stop the merge (the series rows are independent of the author
      // record); it is counted and reported with the result.
      let reclassifyFailures = 0;
      const reclassified: AuthorId[] = [];
      for (const id of plan.narratorIds) {
        throwIfLeft(life);
        try {
          await withDeadline(api.reclassifyAuthorAsNarrator(id));
          reclassified.push(id);
        } catch (e) {
          if (abandoned(e)) throw e;
          if (e instanceof WriteTimeoutError) return outcome('timeout', 'no answer', name, reclassifyFailures);
          if (isAuthRedirectError(e)) return outcome('session', SESSION_EXPIRED_MESSAGE, name, reclassifyFailures);
          reclassifyFailures += 1;
        }
      }
      if (reclassified.length > 0) {
        // Done is done: the flag has nothing left to say about these authors.
        commitSeriesChoices((c) => {
          const flagged = c.narrators[key];
          if (!flagged) return c;
          const rest = new Set([...flagged].filter((id) => !reclassified.includes(asAuthorId(id))));
          return { ...c, narrators: { ...c.narrators, [key]: rest } };
        });
      }
      const suffix =
        reclassifyFailures > 0
          ? ` (${reclassifyFailures} narrator reclassification${reclassifyFailures === 1 ? '' : 's'} failed)`
          : '';

      throwIfLeft(life);
      const folded = new Set<number>(plan.mergeIds);
      try {
        const op = await withDeadline(api.mergeSeriesGroup(plan.keepId, plan.mergeIds));
        unconfirmedRef.current.set(key, { entity: 'series', opId: op.id, name, mergeIds: plan.mergeIds });
        const done = await waitFor(op, life);
        unconfirmedRef.current.delete(key);
        if (!done.ok) {
          const left = parseLeftSeriesIds(done.message);
          if (left) {
            // Some (or, after a later step failed, none) of the series were left:
            // keep the group minus what really went.
            const gone = new Set<number>(plan.mergeIds.filter((id) => !left.has(id)));
            commitSeries((gs) => dropSeriesRows(gs, key, gone));
            return outcome(
              'failed',
              left.size > 0
                ? `${left.size} series ${left.size === 1 ? 'was' : 'were'} not merged (ids ${[...left].join(', ')})${gone.size > 0 ? `; the other ${gone.size} ${gone.size === 1 ? 'was' : 'were'}` : ''}${suffix}`
                : `${done.message}${suffix}`,
              name,
              reclassifyFailures
            );
          }
          // The failure does not say what it left (a cancelled run can stop after
          // some deletes): keep the group, and have the caller re-read.
          return outcome('failed', `${done.message}${suffix}`, name, reclassifyFailures, true);
        }
      } catch (e) {
        // An abandon leaves the entry in `unconfirmedRef`: the merge was sent.
        if (abandoned(e)) throw e;
        unconfirmedRef.current.delete(key);
        if (e instanceof WriteTimeoutError) return outcome('timeout', 'no answer', name, reclassifyFailures);
        if (isAuthRedirectError(e)) return outcome('session', SESSION_EXPIRED_MESSAGE, name, reclassifyFailures);
        return outcome('failed', `${errorMessage(e, 'merge request failed')}${suffix}`, name, reclassifyFailures);
      }
      commitSeries((gs) => dropSeriesRows(gs, key, folded));
      return outcome(
        'merged',
        `Merged ${folded.size} series into "${keepName}"${suffix}`,
        name,
        reclassifyFailures
      );
    },
    [abandoned, commitSeries, commitSeriesChoices, waitFor]
  );

  /** One group's merge, queued. Which list it edits depends on the sub-view that asked. */
  const mergeGroup = useCallback(
    (key: string, entity: 'author' | 'series' = 'author') => {
      if (pendingRef.current.has(key)) return;
      markPending(key, true);
      const again = entity === 'author' ? reload : reloadSeries;
      enqueue(
        async (life) => {
          try {
            const out = await (entity === 'author' ? mergeOne : mergeSeriesOne)(key, life);
            if (!mountedRef.current) return;
            switch (out.result) {
              case 'failed':
                toast(`"${out.name}": ${out.message}`, 'error');
                break;
              case 'skipped':
                toast(`"${out.name}": ${out.message}`, 'warning');
                break;
              case 'session':
                toast(out.message, 'error');
                break;
              case 'timeout':
                timedOut('merge', again);
                break;
              default:
                toast(out.message, out.reclassifyFailures > 0 ? 'warning' : 'success');
            }
            if (out.reread) again();
          } catch (e) {
            // Only an abandon (the lane was left) reaches here; mergeOne reports
            // every other failure as a result.
            if (abandoned(e)) return;
            toast(errorMessage(e, 'Merge failed'), 'error');
          } finally {
            markPending(key, false);
          }
        },
        () => {
          markPending(key, false);
          notSent(1, entity === 'series' ? [seriesLabel(key)] : []);
        }
      );
    },
    [abandoned, enqueue, markPending, mergeOne, mergeSeriesOne, notSent, reload, reloadSeries, seriesLabel, timedOut, toast]
  );

  /**
   * Merges `keys` one after another and reports the totals in one toast. A run
   * stops at the first expired login or unanswered request (the writes after it
   * would be sent blind) and says how many were merged, how many were never
   * sent, and which one may or may not have gone through.
   */
  const runBulk = useCallback(
    (keys: string[], noun: string, entity: 'author' | 'series' = 'author') => {
      if (keys.length === 0) return;
      const mergeFor = entity === 'author' ? mergeOne : mergeSeriesOne;
      const again = entity === 'author' ? reload : reloadSeries;
      // What a message calls a key: a series group goes by its name, not `s11-12`.
      const labelOf = (k: string) => (entity === 'series' ? seriesLabel(k) : k);
      setBulk({ done: 0, total: keys.length, entity });
      for (const k of keys) markPending(k, true);
      const clear = () => {
        for (const k of keys) markPending(k, false);
        if (mountedRef.current) setBulk(null);
      };
      enqueue(
        async (life) => {
          let merged = 0;
          let reclassifyFailures = 0;
          const failures: string[] = [];
          let done = 0;
          let stop: { why: 'left' | 'session' | 'timeout'; at: number; name: string } | null = null;
          let reread = false;
          for (let i = 0; i < keys.length; i++) {
            const key = keys[i];
            let out: MergeOutcome;
            try {
              out = await mergeFor(key, life);
            } catch (e) {
              if (abandoned(e)) {
                stop = { why: 'left', at: i, name: labelOf(key) };
                break;
              }
              failures.push(`"${labelOf(key)}": ${errorMessage(e, 'merge request failed')}`);
              done += 1;
              markPending(key, false);
              continue;
            }
            reclassifyFailures += out.reclassifyFailures;
            if (out.reread) reread = true;
            if (out.result === 'session' || out.result === 'timeout') {
              stop = { why: out.result, at: i, name: out.name };
              break;
            }
            if (out.result === 'failed' || out.result === 'skipped') {
              failures.push(`"${out.name}": ${out.message}`);
            } else merged += 1;
            done += 1;
            if (mountedRef.current) setBulk({ done, total: keys.length, entity });
            markPending(key, false);
          }
          clear();
          if (!mountedRef.current) return;
          if (stop) {
            // Groups after the one that stopped the run were never sent. The one
            // that stopped it was, unless the lane was left before its request.
            const stoppedKey = keys[stop.at];
            const sent = unconfirmedRef.current.has(stoppedKey);
            const unsent = keys.length - stop.at - 1 + (sent ? 0 : 1);
            if (stop.why === 'left') {
              // The group being merged when the lane was left was sent already if
              // it has an entry: it is followed, not counted as unsent.
              notSent(unsent, entity === 'series' ? keys.slice(sent ? stop.at + 1 : stop.at).map(labelOf) : []);
            } else if (stop.why === 'session') {
              toast(
                `${SESSION_EXPIRED_MESSAGE} ${merged} group(s) were merged before it expired; the other ${keys.length - stop.at - 1} were not sent, and "${stop.name}" may or may not have gone through.`,
                'error'
              );
            } else {
              toast(
                `No answer after ${WRITE_DEADLINE_MS / 1000}s — the merge of "${stop.name}" may have run. ${merged} group(s) were merged before it; the other ${keys.length - stop.at - 1} were not sent. Re-reading.`,
                'warning'
              );
              again();
            }
            return;
          }
          if (reread) again();
          const attempted = merged + failures.length;
          const narrator =
            reclassifyFailures > 0
              ? ` ${reclassifyFailures} narrator reclassification${reclassifyFailures === 1 ? '' : 's'} failed.`
              : '';
          if (failures.length === 0) {
            toast(
              `Merged ${merged} of ${attempted} ${noun} group(s).${narrator}`.trim(),
              reclassifyFailures > 0 ? 'warning' : 'success'
            );
          } else {
            toast(
              `Merged ${merged} of ${attempted} ${noun} group(s); ${failures.length} failed: ${failures.slice(0, 3).join('; ')}${failures.length > 3 ? '; …' : ''}.${narrator}`,
              'error'
            );
          }
        },
        () => {
          clear();
          notSent(keys.length, entity === 'series' ? keys.map(labelOf) : []);
        }
      );
    },
    [abandoned, enqueue, markPending, mergeOne, mergeSeriesOne, notSent, reload, reloadSeries, seriesLabel, toast]
  );

  const rename = useCallback(
    async (key: string, canonicalId: AuthorId, rawName: string) => {
      const name = rawName.trim();
      if (!name || pendingRef.current.has(key)) return;
      markPending(key, true);
      try {
        await withDeadline(api.renameAuthor(canonicalId, name));
        if (!mountedRef.current) return;
        commitGroups((gs) =>
          gs.map((g) =>
            g.key === key ? { ...g, canonical: { ...g.canonical, name } } : g
          )
        );
        setEditing((cur) => (cur && cur.key === key ? null : cur));
      } catch (e) {
        if (abandoned(e)) return;
        if (e instanceof WriteTimeoutError) timedOut('rename');
        else toast(isAuthRedirectError(e) ? SESSION_EXPIRED_MESSAGE : errorMessage(e, 'Failed to rename author'), 'error');
      } finally {
        markPending(key, false);
      }
    },
    [abandoned, commitGroups, markPending, timedOut, toast]
  );

  // ---- series rename and its undo ------------------------------------------------
  // Renames that can still be undone. The ref is what a toast's Undo button reads:
  // it fires long after the render that created it.
  const [seriesRenames, setSeriesRenames] = useState<Readonly<Record<number, SeriesRenameRecord>>>({});
  const seriesRenamesRef = useRef<Readonly<Record<number, SeriesRenameRecord>>>({});
  const commitSeriesRenames = useCallback(
    (fn: (r: Readonly<Record<number, SeriesRenameRecord>>) => Readonly<Record<number, SeriesRenameRecord>>) => {
      seriesRenamesRef.current = fn(seriesRenamesRef.current);
      setSeriesRenames(seriesRenamesRef.current);
    },
    []
  );
  /** The group a series row sits in now, or null once it has left the list. */
  const groupKeyOfSeries = useCallback((seriesId: number): string | null => {
    const cur = seriesRef.current;
    if (!cur || cur.load.state !== 'ready') return null;
    return cur.load.value.groups.find((g) => g.series.some((r) => r.id === seriesId))?.key ?? null;
  }, []);

  /**
   * Reverts a rename through the operation journal, not by renaming back: the
   * server refuses when the series was renamed again since, and that refusal is
   * shown rather than papered over.
   */
  const undoSeriesRenameOf = useCallback(
    async (seriesId: number) => {
      const record = seriesRenamesRef.current[seriesId];
      if (!record) return;
      const groupKey = groupKeyOfSeries(seriesId);
      if (groupKey && pendingRef.current.has(groupKey)) return;
      if (groupKey) markPending(groupKey, true);
      try {
        await withDeadline(api.undoSeriesRename(record.opId));
        if (!mountedRef.current) return;
        commitSeries((gs) => renameSeriesRow(gs, seriesId, record.oldName));
        commitSeriesRenames((r) => withoutKey(r, seriesId));
        toast(`Rename undone: "${record.newName}" is "${record.oldName}" again.`, 'success');
      } catch (e) {
        if (abandoned(e)) return;
        if (e instanceof WriteTimeoutError) timedOut('undo', reloadSeries);
        else {
          // The revert said no (or the login expired). Offering the same undo
          // again would only get the same answer, so the button goes.
          if (!isAuthRedirectError(e)) commitSeriesRenames((r) => withoutKey(r, seriesId));
          toast(isAuthRedirectError(e) ? SESSION_EXPIRED_MESSAGE : errorMessage(e, 'Could not undo the rename'), 'error');
        }
      } finally {
        if (groupKey) markPending(groupKey, false);
      }
    },
    [abandoned, commitSeries, commitSeriesRenames, groupKeyOfSeries, markPending, reloadSeries, timedOut, toast]
  );

  const renameSeriesTo = useCallback(
    async (groupKey: string, seriesId: SeriesId, rawName: string) => {
      const name = rawName.trim();
      if (!name || pendingRef.current.has(groupKey)) return;
      const cur = seriesRef.current;
      const row =
        cur && cur.load.state === 'ready'
          ? cur.load.value.groups.find((g) => g.key === groupKey)?.series.find((r) => r.id === seriesId)
          : undefined;
      if (!row) return;
      if (row.name === name) {
        // Nothing to rename: close the editor instead of queueing a no-op that
        // would also offer an undo of nothing.
        setSeriesEditing((e) => (e && e.seriesId === seriesId ? null : e));
        return;
      }
      const oldName = row.name;
      markPending(groupKey, true);
      try {
        const op = await withDeadline(api.renameSeries(seriesId, name), SERIES_RENAME_DEADLINE_MS);
        if (!mountedRef.current) return;
        commitSeries((gs) => renameSeriesRow(gs, seriesId, name));
        setSeriesEditing((e) => (e && e.seriesId === seriesId ? null : e));
        commitSeriesRenames((r) => ({ ...r, [seriesId]: { opId: op.id, oldName, newName: name } }));
        toast(`Renamed "${oldName}" to "${name}".`, 'success', {
          label: 'Undo',
          onClick: () => void undoSeriesRenameOf(seriesId),
        });
      } catch (e) {
        if (abandoned(e)) return;
        if (e instanceof WriteTimeoutError) timedOut('rename', reloadSeries, e.ms);
        else toast(isAuthRedirectError(e) ? SESSION_EXPIRED_MESSAGE : errorMessage(e, 'Failed to rename series'), 'error');
      } finally {
        markPending(groupKey, false);
      }
    },
    [abandoned, commitSeries, commitSeriesRenames, markPending, reloadSeries, timedOut, toast, undoSeriesRenameOf]
  );

  const split = useCallback(
    (key: string, canonicalId: AuthorId) => {
      if (pendingRef.current.has(key)) return;
      markPending(key, true);
      enqueue(
        async (life) => {
          try {
            throwIfLeft(life);
            const cur = groupsRef.current;
            const group =
              cur && cur.load.state === 'ready' ? cur.load.value.groups.find((g) => g.key === key) : undefined;
            const narrators = choicesRef.current.narrators[key] ?? EMPTY_STRINGS;
            const flagged = (group?.splitNames ?? []).filter((n) => narrators.has(splitRoleKey(n)));
            const result = await withDeadline(api.splitCompositeAuthor(canonicalId));
            // The composite is gone server-side, and so is the server's cached
            // list: edit ours rather than re-read it.
            commitGroups((gs) => gs.filter((g) => g.key !== key));
            let failed = 0;
            const unmatched: string[] = [];
            for (const name of flagged) {
              const match = result.authors.find((a) => a.name === name);
              if (!match) {
                unmatched.push(name);
                continue;
              }
              try {
                throwIfLeft(life);
                await withDeadline(api.reclassifyAuthorAsNarrator(match.id));
              } catch (e) {
                if (abandoned(e)) throw e;
                failed += 1;
              }
            }
            if (!mountedRef.current) return;
            const notes: string[] = [];
            if (failed > 0) notes.push(`${failed} narrator reclassification${failed === 1 ? '' : 's'} failed`);
            if (unmatched.length > 0)
              notes.push(
                `${unmatched.map((n) => `"${n}"`).join(', ')} matched no new author, so ${unmatched.length === 1 ? 'it was' : 'they were'} not reclassified as narrator`
              );
            toast(
              `Split "${group?.canonical.name ?? key}" into ${result.authors.length} authors` +
                (notes.length > 0 ? ` (${notes.join('; ')})` : ''),
              notes.length > 0 ? 'warning' : 'success'
            );
          } catch (e) {
            if (abandoned(e)) return;
            if (e instanceof WriteTimeoutError) timedOut('split');
            else toast(isAuthRedirectError(e) ? SESSION_EXPIRED_MESSAGE : errorMessage(e, 'Failed to split author'), 'error');
          } finally {
            markPending(key, false);
          }
        },
        () => markPending(key, false)
      );
    },
    [abandoned, commitGroups, enqueue, markPending, timedOut, toast]
  );

  const resolveProduction = useCallback(
    (key: string, canonicalId: AuthorId) => {
      if (pendingRef.current.has(key)) return;
      markPending(key, true);
      enqueue(
        async (life) => {
          try {
            const op = await withDeadline(api.resolveProductionAuthor(canonicalId));
            if (mountedRef.current) toast('Looking up the real author — watch the bell for progress.', 'info');
            const done = await waitFor(op, life);
            if (!mountedRef.current) return;
            if (done.ok) {
              // Resolved: the books moved off the production company, so its
              // group is done. Edit the list; the server's cached copy is gone.
              commitGroups((gs) => gs.filter((g) => g.key !== key));
              toast('Found the real author(s).', 'success');
            } else toast(`Could not resolve the production company: ${done.message}`, 'error');
          } catch (e) {
            if (abandoned(e)) return;
            if (e instanceof WriteTimeoutError) timedOut('lookup');
            else toast(isAuthRedirectError(e) ? SESSION_EXPIRED_MESSAGE : errorMessage(e, 'Failed to resolve'), 'error');
          } finally {
            markPending(key, false);
          }
        },
        () => markPending(key, false)
      );
    },
    [abandoned, commitGroups, enqueue, markPending, timedOut, toast, waitFor]
  );

  /** The Series sub-view's actions. None of them can reach an author endpoint: they never leave this function. */
  const dispatchSeries = useCallback(
    (action: SeriesEntityAction) => {
      switch (action.type) {
        case 'mergeSeriesGroup':
          mergeGroup(action.groupKey, 'series');
          return;
        case 'mergeSelectedSeries':
          runBulk(action.groupKeys, 'selected', 'series');
          return;
        case 'mergeAllSeriesGroups': {
          const cur = seriesRef.current;
          const now =
            cur && cur.load.state === 'ready'
              ? cur.load.value.groups.filter((g) => inMergeAll(g, seriesChoicesRef.current))
              : [];
          // The SET, not just its size: one group leaving and another becoming
          // mergeable keeps the count and changes what would run.
          const expected = new Set(action.expectedKeys);
          if (now.length !== expected.size || now.some((g) => !expected.has(g.key))) {
            toast(
              `The list changed while you were confirming (${expected.size} group(s) became ${now.length}). Nothing was merged; check the count and try again.`,
              'warning'
            );
            return;
          }
          runBulk(now.map((g) => g.key), 'same-name', 'series');
          return;
        }
        case 'renameSeries':
          void renameSeriesTo(action.groupKey, action.seriesId, action.name);
          return;
        case 'undoRenameSeries':
          void undoSeriesRenameOf(action.seriesId);
          return;
        case 'toggleSeriesNarrator':
          commitSeriesChoices((c) => {
            const set = new Set(c.narrators[action.groupKey] ?? EMPTY_NUMBERS);
            if (!set.delete(action.authorId)) set.add(action.authorId);
            return { ...c, narrators: { ...c.narrators, [action.groupKey]: set } };
          });
          return;
        case 'toggleSeriesIncluded':
          commitSeriesChoices((c) => {
            // A toggle, as for authors: leaving a series out is not a one-way door.
            const set = new Set(c.excluded[action.groupKey] ?? EMPTY_NUMBERS);
            if (!set.delete(action.seriesId)) set.add(action.seriesId);
            return { ...c, excluded: { ...c.excluded, [action.groupKey]: set } };
          });
          return;
      }
    },
    [commitSeriesChoices, mergeGroup, renameSeriesTo, runBulk, toast, undoSeriesRenameOf]
  );

  const dispatch = useCallback(
    (action: AuthorsAction) => {
      // `entity` is checked first: a series action must never reach an author
      // endpoint, whatever shape its ids have, and an author action never a
      // series one. Each branch hands over to its own switch.
      if (action.entity === 'series') {
        dispatchSeries(action);
        return;
      }
      switch (action.type) {
        case 'mergeGroup':
          mergeGroup(String(action.canonicalId));
          return;
        case 'mergeSelected':
          runBulk(action.ids.map(String), 'selected');
          return;
        case 'mergeAllGroups': {
          const cur = groupsRef.current;
          const now =
            cur && cur.load.state === 'ready'
              ? cur.load.value.groups.filter((g) => isMergeable(g, choicesRef.current))
              : [];
          // The SET, not just its size: one group leaving and another becoming
          // mergeable keeps the count and changes what would run.
          const expected = new Set(action.expectedIds.map(String));
          if (now.length !== expected.size || now.some((g) => !expected.has(g.key))) {
            toast(
              `The list changed while you were confirming (${expected.size} group(s) became ${now.length}). Nothing was merged; check the count and try again.`,
              'warning'
            );
            return;
          }
          runBulk(now.map((g) => g.key), 'name-variant');
          return;
        }
        case 'renameCanonical':
          void rename(String(action.canonicalId), action.canonicalId, action.name);
          return;
        case 'splitComposite':
          split(String(action.canonicalId), action.canonicalId);
          return;
        case 'resolveProduction':
          resolveProduction(String(action.canonicalId), action.canonicalId);
          return;
        case 'toggleRole': {
          const key = String(action.canonicalId);
          const flag =
            action.target.kind === 'variant' ? variantRoleKey(action.target.id) : splitRoleKey(action.target.name);
          commitChoices((c) => {
            const set = new Set(c.narrators[key] ?? EMPTY_STRINGS);
            if (!set.delete(flag)) set.add(flag);
            return { ...c, narrators: { ...c.narrators, [key]: set } };
          });
          return;
        }
        case 'excludeVariant': {
          const key = String(action.canonicalId);
          commitChoices((c) => {
            // A toggle: leaving a variant out is not a one-way door, and a
            // reviewer who clicked the wrong one needs a way back that is not
            // a full reload.
            const set = new Set(c.excluded[key] ?? EMPTY_NUMBERS);
            if (!set.delete(action.variantId)) set.add(action.variantId);
            return { ...c, excluded: { ...c.excluded, [key]: set } };
          });
          return;
        }
      }
    },
    [commitChoices, dispatchSeries, mergeGroup, rename, resolveProduction, runBulk, split, toast]
  );

  // Merges that were sent before the lane was left: follow them again. The row
  // stays locked until the operation settles, so a second Merge cannot be sent
  // for a group whose first one may have gone through.
  useEffect(() => {
    if (!active) return;
    const life = lifeRef.current.signal;
    // The Map itself is never replaced, only mutated, so this is the live one.
    const unconfirmed = unconfirmedRef.current;
    for (const [key, entry] of unconfirmed) {
      const { opId, name } = entry;
      markPending(key, true);
      api
        .pollOperationV2(opId, undefined, AUTHORS_POLL_MS, {
          signal: life,
          requestTimeoutMs: POLL_REQUEST_TIMEOUT_MS,
        })
        .then((final) => {
          unconfirmed.delete(key);
          if (entry.entity === 'series') {
            // The Series twin: a completed merge folded every merge id away, a
            // failed one folded away all but the ids it names. A failure that
            // names none (a cancelled run) leaves the list as it was and
            // re-reads it.
            const folded = new Set<number>(entry.mergeIds);
            if (final.status === 'completed') {
              commitSeries((gs) => dropSeriesRows(gs, key, folded));
              toast(`Merged series into "${name}" (it finished after you left).`, 'success');
              return;
            }
            const message = final.error_message || final.status;
            const left = parseLeftSeriesIds(message);
            if (left) commitSeries((gs) => dropSeriesRows(gs, key, new Set(entry.mergeIds.filter((id) => !left.has(id)))));
            else reloadSeries();
            toast(`"${name}": ${message}`, 'error');
            return;
          }
          if (final.status === 'completed') {
            commitGroups((gs) => gs.filter((g) => g.key !== key));
            toast(`Merged author(s) into "${name}" (it finished after you left).`, 'success');
          } else {
            toast(`"${name}": ${final.error_message || final.status}`, 'error');
          }
        })
        .catch((e: unknown) => {
          if (life.aborted || isAbortError(e)) return;
          unconfirmed.delete(key);
          toast(`Could not confirm the merge into "${name}": ${errorMessage(e, 'no answer')}`, 'warning');
        })
        .finally(() => markPending(key, false));
    }
    return () => {
      // markPending is a no-op once unmounted; while mounted this unlocks rows
      // whose follow-up was cut short (they are followed again next time).
      for (const key of unconfirmed.keys()) markPending(key, false);
    };
  }, [active, commitGroups, commitSeries, markPending, reloadSeries, toast]);

  // ---- validate ------------------------------------------------------------------------
  const [validation, setValidation] = useState<Readonly<Record<string, ValidationState>>>({});
  const validateCtls = useRef(new Map<string, AbortController>());
  useEffect(() => {
    if (!authorsOn) return;
    const ctls = validateCtls.current;
    return () => {
      for (const c of ctls.values()) c.abort();
      ctls.clear();
    };
  }, [authorsOn]);
  const validate = useCallback(
    (key: string) => {
      const cur = groupsRef.current;
      const group = cur && cur.load.state === 'ready' ? cur.load.value.groups.find((g) => g.key === key) : undefined;
      if (!group) return;
      const forName = group.canonical.name;
      validateCtls.current.get(key)?.abort();
      const ctl = new AbortController();
      validateCtls.current.set(key, ctl);
      setValidation((v) => ({ ...v, [key]: { forName, state: 'loading' } }));
      validateAuthorName(forName, ctl.signal)
        .then((r) => {
          if (ctl.signal.aborted) return;
          setValidation((v) => ({ ...v, [key]: { forName, state: 'ready', results: r.results } }));
        })
        .catch((e: unknown) => {
          if (ctl.signal.aborted || isAbortError(e)) return;
          setValidation((v) => ({
            ...v,
            [key]: {
              forName,
              state: 'error',
              message:
                e instanceof ApiTimeoutError ? 'The providers did not answer in time.' : errorMessage(e, 'Validation failed'),
            },
          }));
        })
        .finally(() => {
          if (validateCtls.current.get(key) === ctl) validateCtls.current.delete(key);
        });
    },
    []
  );

  // ---- validate (series) ------------------------------------------------------------------
  const [seriesValidation, setSeriesValidation] = useState<Readonly<Record<string, ValidationState>>>({});
  const seriesValidateCtls = useRef(new Map<string, AbortController>());
  useEffect(() => {
    if (!seriesOn) return;
    const ctls = seriesValidateCtls.current;
    return () => {
      for (const c of ctls.values()) c.abort();
      ctls.clear();
    };
  }, [seriesOn]);
  const validateSeries = useCallback((key: string) => {
    const cur = seriesRef.current;
    const group = cur && cur.load.state === 'ready' ? cur.load.value.groups.find((g) => g.key === key) : undefined;
    if (!group) return;
    // The group's name, which does not follow a rename of one row, so the
    // evidence on screen always answers the name the card heads with.
    const forName = group.name;
    seriesValidateCtls.current.get(key)?.abort();
    const ctl = new AbortController();
    seriesValidateCtls.current.set(key, ctl);
    setSeriesValidation((v) => ({ ...v, [key]: { forName, state: 'loading' } }));
    validateSeriesName(forName, ctl.signal)
      .then((r) => {
        if (ctl.signal.aborted) return;
        setSeriesValidation((v) => ({ ...v, [key]: { forName, state: 'ready', results: r.results } }));
      })
      .catch((e: unknown) => {
        if (ctl.signal.aborted || isAbortError(e)) return;
        setSeriesValidation((v) => ({
          ...v,
          [key]: {
            forName,
            state: 'error',
            message:
              e instanceof ApiTimeoutError ? 'The providers did not answer in time.' : errorMessage(e, 'Validation failed'),
          },
        }));
      })
      .finally(() => {
        if (seriesValidateCtls.current.get(key) === ctl) seriesValidateCtls.current.delete(key);
      });
  }, []);

  // ---- scan -----------------------------------------------------------------------------
  /**
   * Starts a scan op through the queue and waits for it. One scan at a time across
   * both sub-views (the ref is what the guard reads), and the cards are frozen for
   * its duration. `finished` runs when the scan completed.
   */
  const startScan = useCallback(
    (noun: 'Author' | 'Series', start: () => Promise<Operation>, finished: () => void, again: () => void) => {
      if (scanningRef.current) return;
      scanningRef.current = true;
      setScanning(true);
      const settle = () => {
        scanningRef.current = false;
        if (mountedRef.current) setScanning(false);
      };
      enqueue(async (life) => {
        try {
          const op = await withDeadline(start());
          if (mountedRef.current) toast(`${noun} scan started — watch the bell for progress.`, 'info');
          const done = await waitFor(op, life);
          if (!mountedRef.current) return;
          if (done.ok) {
            toast(`${noun} scan finished.`, 'success');
            finished();
          } else toast(`${noun} scan did not finish: ${done.message}`, 'error');
        } catch (e) {
          if (abandoned(e)) return;
          if (e instanceof WriteTimeoutError) timedOut('scan request', again);
          else
            toast(
              isAuthRedirectError(e)
                ? SESSION_EXPIRED_MESSAGE
                : errorMessage(e, `Failed to start the ${noun.toLowerCase()} scan`),
              'error'
            );
        } finally {
          settle();
        }
      }, settle);
    },
    [abandoned, enqueue, timedOut, toast, waitFor]
  );
  const refresh = useCallback(
    () =>
      startScan(
        'Author',
        () => api.refreshAuthorDuplicates(),
        () => {
          clearSelection();
          reload();
          candidatesReload();
        },
        reload
      ),
    [startScan, clearSelection, reload, candidatesReload]
  );
  const refreshSeries = useCallback(
    () =>
      startScan(
        'Series',
        () => api.refreshSeriesDuplicates(),
        () => {
          clearSeriesSelection();
          reloadSeries();
        },
        reloadSeries
      ),
    [startScan, clearSeriesSelection, reloadSeries]
  );

  // ---- books popover / editing / focus ---------------------------------------------------
  const openBooks = useCallback((key: string, anchor: HTMLElement | null) => {
    setBooksFor({ key, anchor });
  }, []);
  const closeBooks = useCallback(() => setBooksFor(null), []);
  const startEdit = useCallback((key: string, initial: string) => setEditing({ key, initial }), []);
  const cancelEdit = useCallback(() => setEditing(null), []);
  const focusGroup = useCallback(
    (key: string) => {
      const at = groups.findIndex((g) => g.key === key);
      if (at < 0) return;
      setFilterState('all');
      setFocusedKey(key);
      setPageState(Math.floor(at / pageSize));
    },
    [groups, pageSize]
  );

  // ---- series: editing and keyboard -------------------------------------------------------
  const startSeriesEdit = useCallback(
    (groupKey: string, seriesId: SeriesId, initial: string) => setSeriesEditing({ groupKey, seriesId, initial }),
    []
  );
  const cancelSeriesEdit = useCallback(() => setSeriesEditing(null), []);
  // Same contract as the Authors sub-view's, minus the books popover (a series
  // card has none): bound only while the Series sub-view is on screen, Escape
  // first so it can always close what is open, then the typing guard, and j/k/m
  // do nothing while the help or a name editor is in front.
  //
  // CAPTURE PHASE, and `?` stops propagating once this handler has taken it. The
  // app has its own `?` handler (App.tsx -> useKeyboardShortcuts: a window
  // listener that opens the app-wide shortcut dialog). It is registered first, so
  // in the bubble phase it runs first: it opens its dialog, React renders it
  // before the next listener is called, focus moves into it, and this handler
  // then sees `isKeyboardShortcutSuppressed()` and never opens the lane's help.
  // Measured on /review?lane=authors with Playwright: `?` opened the app dialog
  // and `authors-shortcut-help` never appeared. Taking the key in the capture
  // phase and stopping it is what lets the lane's own help open on this sub-view;
  // every other key propagates as before.
  useEffect(() => {
    if (!seriesOn) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === '?' && seriesHelpOpen) {
        event.preventDefault();
        event.stopPropagation();
        setSeriesHelpOpen(false);
        return;
      }
      if (event.key === 'Escape') {
        if (seriesHelpOpen) setSeriesHelpOpen(false);
        else if (seriesEditing) setSeriesEditing(null);
        return;
      }
      if (isKeyboardShortcutSuppressed()) return;
      if (event.key === '?') {
        event.preventDefault();
        event.stopPropagation();
        setSeriesHelpOpen(!seriesHelpOpen);
        return;
      }
      if (seriesHelpOpen || seriesEditing) return;
      if (seriesPageGroups.length === 0) return;
      const at = Math.max(0, seriesPageGroups.findIndex((g) => g.key === seriesFocusedKey));
      const focused = seriesPageGroups[at];
      switch (event.key) {
        case 'j':
          event.preventDefault();
          setSeriesFocusedKey(seriesPageGroups[Math.min(at + 1, seriesPageGroups.length - 1)].key);
          return;
        case 'k':
          event.preventDefault();
          setSeriesFocusedKey(seriesPageGroups[Math.max(at - 1, 0)].key);
          return;
        case 'm':
          if (!isSeriesMergeable(focused, seriesChoicesRef.current) || pendingRef.current.has(focused.key)) return;
          event.preventDefault();
          dispatch({ lane: 'authors', entity: 'series', type: 'mergeSeriesGroup', groupKey: focused.key });
          return;
      }
    };
    window.addEventListener('keydown', onKey, true);
    return () => window.removeEventListener('keydown', onKey, true);
  }, [seriesOn, seriesHelpOpen, seriesEditing, seriesPageGroups, seriesFocusedKey, dispatch]);

  // ---- keyboard ----------------------------------------------------------------------------
  // In the hook, with the lane's state, and bound only while the lane is active:
  // a window listener that outlives the lane would move a focus ring nobody sees.
  useEffect(() => {
    if (!authorsOn) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === '?' && helpOpen) {
        event.preventDefault();
        setHelpOpen(false);
        return;
      }
      // Escape runs before the typing guard so it can always close what is open.
      // (The guard below only knows role=dialog; the books popover is MUI's
      // role=presentation modal root, so it is covered by the explicit return
      // further down, not by the guard.)
      if (event.key === 'Escape') {
        if (booksFor) setBooksFor(null);
        else if (helpOpen) setHelpOpen(false);
        else if (editing) setEditing(null);
        return;
      }
      if (isKeyboardShortcutSuppressed()) return;
      if (event.key === '?') {
        event.preventDefault();
        setHelpOpen(!helpOpen);
        return;
      }
      // While the books popover or the help is open it is the foreground: j/k/m/
      // Enter would act on a card behind it, and `m` would merge it unseen.
      if (booksFor || helpOpen) return;
      if (pageGroups.length === 0) return;
      const at = Math.max(0, pageGroups.findIndex((g) => g.key === focusedKey));
      const focused = pageGroups[at];
      switch (event.key) {
        case 'j':
          event.preventDefault();
          setFocusedKey(pageGroups[Math.min(at + 1, pageGroups.length - 1)].key);
          return;
        case 'k':
          event.preventDefault();
          setFocusedKey(pageGroups[Math.max(at - 1, 0)].key);
          return;
        case 'Enter':
          // Enter on a focused button, link or checkbox activates THAT control;
          // taking it for "open the books" would fire both.
          if (document.activeElement?.closest('button, a, [role="button"], [role="checkbox"], summary')) return;
          event.preventDefault();
          setBooksFor({
            key: focused.key,
            anchor: document.querySelector<HTMLElement>(`[data-author-books-chip="${focused.key}"]`),
          });
          return;
        case 'm':
          if (!isMergeable(focused, choicesRef.current) || pendingRef.current.has(focused.key)) return;
          event.preventDefault();
          dispatch({ lane: 'authors', entity: 'author', type: 'mergeGroup', canonicalId: focused.canonical.id });
          return;
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [authorsOn, booksFor, helpOpen, editing, pageGroups, focusedKey, dispatch]);

  // ---- result ------------------------------------------------------------------------------
  const status: ListStatus =
    groupsLoad.state === 'loading'
      ? 'loading'
      : groupsLoad.state === 'error'
        ? 'error'
        : groups.length > 0
          ? 'populated'
          : needsScan
            ? 'needs-scan'
            : 'empty';

  const seriesStatus: ListStatus =
    seriesLoad.state === 'loading'
      ? 'loading'
      : seriesLoad.state === 'error'
        ? 'error'
        : seriesGroups.length > 0
          ? 'populated'
          : seriesNeedsScan
            ? 'needs-scan'
            : 'empty';

  const seriesLane: SeriesLane = {
    status: seriesStatus,
    error: seriesLoad.state === 'error' ? seriesLoad.message : null,
    groups: seriesGroups,
    totalSeries: seriesTotal,
    kindCounts: seriesKindCounts,
    filter: seriesFilter,
    setFilter: setSeriesFilter,
    filtered: seriesFiltered,
    pageGroups: seriesPageGroups,
    page: seriesPage,
    setPage: setSeriesPage,
    pageSize: seriesPageSize,
    setPageSize: setSeriesPageSize,
    mergeable: seriesMergeable,
    mergeAll: seriesMergeAll,
    selectableTotal: allSeriesSelectableKeys.length,
    stale: seriesStale,
    selection: seriesSelection,
    selectedGroups: selectedSeriesGroups,
    selectedCount: selectedSeriesGroups.length,
    choices: seriesChoices,
    validation: seriesValidation,
    renames: seriesRenames,
    editing: seriesEditing,
    startEdit: startSeriesEdit,
    cancelEdit: cancelSeriesEdit,
    focusedKey: seriesPageGroups.some((g) => g.key === seriesFocusedKey)
      ? seriesFocusedKey
      : (seriesPageGroups[0]?.key ?? null),
    helpOpen: seriesHelpOpen,
    setHelpOpen: setSeriesHelpOpen,
    validate: validateSeries,
    refresh: refreshSeries,
    reload: reloadSeries,
  };

  return {
    view,
    setView,
    series: seriesLane,
    status,
    error: groupsLoad.state === 'error' ? groupsLoad.message : null,
    groups,
    kindCounts,
    filter,
    setFilter,
    filtered,
    pageGroups,
    page,
    setPage,
    pageSize,
    setPageSize,
    mergeable,
    selectableTotal: allSelectableKeys.length,
    stale,
    selection,
    selectedGroups,
    selectedCount: selectedGroups.length,
    choices,
    validation,
    pendingKeys,
    bulk,
    scanning,
    busy,
    focusedKey: pageGroups.some((g) => g.key === focusedKey) ? focusedKey : (pageGroups[0]?.key ?? null),
    editing,
    startEdit,
    cancelEdit,
    booksFor,
    openBooks,
    closeBooks,
    helpOpen,
    setHelpOpen,
    dispatch,
    validate,
    refresh,
    reload,
    focusGroup,
    candidates: {
      status:
        candLoad.state === 'loading'
          ? 'loading'
          : candLoad.state === 'error'
            ? 'error'
            : candLoad.value.rows.length === 0
              ? 'empty'
              : 'populated',
      error: candLoad.state === 'error' ? candLoad.message : null,
      rows: candLoad.state === 'ready' ? candLoad.value.rows : [],
      total: candLoad.state === 'ready' ? candLoad.value.total : 0,
      offset: candOffset,
      setOffset: setCandOffset,
      nameOf,
      reload: candidatesReload,
    },
  };
}

function withoutKey<T>(record: Readonly<Record<number, T>>, key: number): Readonly<Record<number, T>> {
  const next = { ...record };
  delete next[key];
  return next;
}

/** Stops a queued write that was caught by the lane being left. */
function throwIfLeft(life: AbortSignal): void {
  if (life.aborted) throw new DOMException('The lane was left', 'AbortError');
}

function abortedController(): AbortController {
  const c = new AbortController();
  c.abort();
  return c;
}

function parseAuthorId(raw: string | null): number | null {
  if (!raw) return null;
  const n = Number(raw);
  return Number.isInteger(n) && n > 0 ? n : null;
}

/** The Series sub-view's, rendered by its help overlay. Kept beside the handler that implements them. */
export const SERIES_SHORTCUTS = [
  { keys: 'j / k', action: 'Move to the next / previous group' },
  { keys: 'm', action: 'Merge the focused group' },
  { keys: 'Esc', action: 'Close this help or the name editor' },
  { keys: '?', action: 'Toggle this shortcut help' },
];

/** Rendered by the shortcut help overlay. Kept beside the handler that implements them. */
export const AUTHOR_SHORTCUTS = [
  { keys: 'j / k', action: 'Move to the next / previous group' },
  { keys: 'm', action: 'Merge the focused group' },
  { keys: 'Enter', action: "Show the focused group's books" },
  { keys: 'Esc', action: 'Close the books list, this help, or the name editor' },
  { keys: '?', action: 'Toggle this shortcut help' },
];
