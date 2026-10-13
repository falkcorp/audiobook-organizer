// file: web/src/components/review/lanes/useAuthorsLane.ts
// version: 1.0.0
// guid: c9e4a1d7-5b83-4f26-a0d9-3e7b61f48a25
// last-edited: 2026-10-13

/**
 * The Authors & series lane's data layer (Authors sub-view).
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
 */

import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import * as api from '../../../services/api';
import type { AuthorDedupGroup, DedupCandidate, Operation, ValidationResult } from '../../../services/api';
import { ApiTimeoutError, isAbortError } from '../../../utils/apiFetch';
import { useRowSelection, type RowSelection } from '../../../hooks/useRowSelection';
import {
  asAuthorId,
  type AuthorId,
  type AuthorsAction,
} from '../reviewActions';
import { isKeyboardShortcutSuppressed } from './useDupesLane';
import { fetchAuthorGroups, fetchAuthorName, validateAuthorName } from './authorsReads';

type Toast = (message: string, severity?: 'success' | 'error' | 'warning' | 'info') => void;

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

/** A group a Merge button applies to: it has variants to fold in. */
export function isMergeable(group: AuthorGroup): boolean {
  return group.kind === 'variants' && group.variants.length > 0;
}

// ---------------------------------------------------------------------------
// The lane
// ---------------------------------------------------------------------------

export interface AuthorsLane {
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
  selection: RowSelection<string>;
  /** Selected groups that still exist: the rows `selectedCount` counts. */
  selectedGroups: AuthorGroup[];
  selectedCount: number;

  choices: Choices;
  validation: Readonly<Record<string, ValidationState>>;
  pendingKeys: ReadonlySet<string>;
  /** A bulk merge is running: `done` of `total` groups so far. */
  bulk: { done: number; total: number } | null;
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
  // ---- request identity ----------------------------------------------------
  // A new activation is a new request: re-entering the lane must not paint what
  // it held when it was left. Derived during render (React's "adjust state when
  // a prop changes" pattern) so there is no committed frame in between.
  const [activation, setActivation] = useState(0);
  const [prevActive, setPrevActive] = useState(active);
  if (prevActive !== active) {
    setPrevActive(active);
    if (active) setActivation((a) => a + 1);
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
  const [editing, setEditing] = useState<{ key: string; initial: string } | null>(null);
  const [booksFor, setBooksFor] = useState<BooksTarget | null>(null);
  const [helpOpen, setHelpOpen] = useState(false);

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
  const mergeable = useMemo(() => groups.filter(isMergeable), [groups]);
  const mergeableKeys = useMemo(() => new Set(mergeable.map((g) => g.key)), [mergeable]);
  const isUnselectable = useCallback((key: string) => !mergeableKeys.has(key), [mergeableKeys]);
  const pageKeys = useMemo(() => pageGroups.map((g) => g.key), [pageGroups]);
  const allSelectableKeys = useMemo(
    () => filtered.filter(isMergeable).map((g) => g.key),
    [filtered]
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
    if (!active) return;
    const ctl = new AbortController();
    fetchAuthorGroups(ctl.signal)
      .then((r) => {
        if (ctl.signal.aborted) return;
        const mapped = r.groups.map(toAuthorGroup);
        commitResult({
          key: groupsKey,
          load: { state: 'ready', value: { groups: mapped, needsScan: r.needsRefresh } },
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
  }, [active, groupsKey, commitResult]);

  // ---- author candidates ---------------------------------------------------------
  const [candOffset, setCandOffset] = useState(0);
  const [candToken, setCandToken] = useState(0);
  const candKey = `${activation}:${candToken}:${reloadToken}:${candOffset}`;
  const [candResult, setCandResult] = useState<Keyed<CandidatesPage> | null>(null);
  const candLoad = current(candResult, candKey);
  useEffect(() => {
    if (!active) return;
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
  }, [active, candKey, candOffset]);

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
  useEffect(() => {
    if (!active || !candRows) return;
    const wanted = new Set<string>();
    for (const r of candRows) {
      for (const id of [r.entity_a_id, r.entity_b_id]) if (!groupNames.has(id)) wanted.add(id);
    }
    if (wanted.size === 0) return;
    const ctl = new AbortController();
    const queue = [...wanted];
    const lookups = Array.from({ length: Math.min(NAME_LOOKUP_CONCURRENCY, queue.length) }, async () => {
      for (let id = queue.shift(); id !== undefined; id = queue.shift()) {
        const n = Number(id);
        if (!Number.isFinite(n)) continue;
        try {
          const name = await fetchAuthorName(n, ctl.signal);
          if (ctl.signal.aborted || !name) continue;
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
        } catch {
          /* falls back to the id; the abort case is the only one that matters here */
        }
      }
    });
    void Promise.all(lookups);
    return () => ctl.abort();
  }, [active, candRows, groupNames, candKey]);
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
  const [bulk, setBulk] = useState<{ done: number; total: number } | null>(null);
  const [scanning, setScanning] = useState(false);
  // The ref is what the guard reads: two clicks inside one render must not
  // start two scans, and state would still say "not scanning" for the second.
  const scanningRef = useRef(false);
  const busy = pendingKeys.size > 0 || bulk !== null || scanning;

  const queueRef = useRef<Promise<void>>(Promise.resolve());
  /**
   * Runs `task` after every earlier write has finished. `task` must not throw.
   *
   * It is handed the lane's lifetime signal AS IT WAS when the write was queued.
   * A write still waiting when the lane is left is not sent -- `onSkip` clears
   * its pending marks instead -- because leaving the lane stops its requests, and
   * a queued merge that fired a minute later would be a request from a screen
   * nobody is looking at. (Re-entering the lane makes a new signal; it does not
   * revive what was queued under the old one.)
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
  const candidatesReload = useCallback(() => setCandToken((t) => t + 1), []);

  /** Tells the caller a thrown error was the lane being left, not a failure. */
  const abandoned = useCallback((e: unknown) => isAbortError(e) || !mountedRef.current, []);

  // ---- actions ------------------------------------------------------------------------

  /**
   * One group's merge, run inside the queue. Returns what happened so a bulk run
   * can total it; the caller owns the toast.
   */
  const mergeOne = useCallback(
    async (
      key: string,
      life: AbortSignal
    ): Promise<{ result: 'merged' | 'reclassified' | 'skipped' | 'failed'; message: string; name: string }> => {
      // Read NOW: the list and the choices as they are when this turn comes.
      const cur = groupsRef.current;
      const group =
        cur && cur.load.state === 'ready' ? cur.load.value.groups.find((g) => g.key === key) : undefined;
      if (!group) return { result: 'skipped', message: 'the group is no longer in the list', name: key };
      const { mergeIds, narratorIds } = planMerge(group, choicesRef.current);
      const name = group.canonical.name;

      let reclassifyFailures = 0;
      for (const id of narratorIds) {
        throwIfLeft(life);
        try {
          await api.reclassifyAuthorAsNarrator(id);
        } catch (e) {
          if (abandoned(e)) throw e;
          reclassifyFailures += 1;
        }
      }
      const suffix =
        reclassifyFailures > 0
          ? ` (${reclassifyFailures} narrator reclassification${reclassifyFailures === 1 ? '' : 's'} failed)`
          : '';
      if (mergeIds.length === 0) {
        return {
          result: 'reclassified',
          message: `Reclassified ${narratorIds.length - reclassifyFailures} variant(s) as narrator${suffix}`,
          name,
        };
      }
      throwIfLeft(life);
      try {
        const op = await api.mergeAuthors(group.canonical.id, mergeIds);
        const done = await waitFor(op, life);
        if (!done.ok) return { result: 'failed', message: `${done.message}${suffix}`, name };
      } catch (e) {
        if (abandoned(e)) throw e;
        return { result: 'failed', message: `${errorMessage(e, 'merge request failed')}${suffix}`, name };
      }
      commitGroups((gs) => gs.filter((g) => g.key !== key));
      return { result: 'merged', message: `Merged author(s) into "${name}"${suffix}`, name };
    },
    [abandoned, commitGroups, waitFor]
  );

  const mergeGroup = useCallback(
    (key: string) => {
      if (pendingRef.current.has(key)) return;
      markPending(key, true);
      enqueue(async (life) => {
        try {
          const out = await mergeOne(key, life);
          if (!mountedRef.current) return;
          if (out.result === 'failed') toast(`"${out.name}": ${out.message}`, 'error');
          else if (out.result === 'skipped') toast(`"${out.name}": ${out.message}`, 'warning');
          else {
            toast(out.message, 'success');
            // Narrator-only: the group itself is unchanged server-side; re-read.
            if (out.result === 'reclassified') reload();
          }
        } catch (e) {
          // Only an abandon (the lane was left) reaches here; mergeOne reports
          // every other failure as a result.
          if (abandoned(e)) return;
          toast(errorMessage(e, 'Merge failed'), 'error');
        } finally {
          markPending(key, false);
        }
      }, () => markPending(key, false));
    },
    [abandoned, enqueue, markPending, mergeOne, reload, toast]
  );

  /** Merges `keys` one after another and reports the totals in one toast. */
  const runBulk = useCallback(
    (keys: string[], noun: string) => {
      if (keys.length === 0) return;
      setBulk({ done: 0, total: keys.length });
      for (const k of keys) markPending(k, true);
      const clear = () => {
        for (const k of keys) markPending(k, false);
        if (mountedRef.current) setBulk(null);
      };
      enqueue(async (life) => {
        let merged = 0;
        const failures: string[] = [];
        let done = 0;
        let left = false;
        for (const key of keys) {
          try {
            const out = await mergeOne(key, life);
            if (out.result === 'failed' || out.result === 'skipped') {
              failures.push(`"${out.name}": ${out.message}`);
            } else merged += 1;
          } catch (e) {
            if (abandoned(e)) {
              left = true;
              break;
            }
            failures.push(`"${key}": ${errorMessage(e, 'merge request failed')}`);
          }
          done += 1;
          if (mountedRef.current) setBulk({ done, total: keys.length });
          markPending(key, false);
        }
        clear();
        if (!mountedRef.current) return;
        if (left) return;
        const attempted = merged + failures.length;
        if (failures.length === 0) toast(`Merged ${merged} of ${attempted} ${noun} group(s)`, 'success');
        else
          toast(
            `Merged ${merged} of ${attempted} ${noun} group(s); ${failures.length} failed: ${failures.slice(0, 3).join('; ')}${failures.length > 3 ? '; …' : ''}`,
            'error'
          );
      }, clear);
    },
    [abandoned, enqueue, markPending, mergeOne, toast]
  );

  const rename = useCallback(
    async (key: string, canonicalId: AuthorId, rawName: string) => {
      const name = rawName.trim();
      if (!name || pendingRef.current.has(key)) return;
      markPending(key, true);
      try {
        await api.renameAuthor(canonicalId, name);
        if (!mountedRef.current) return;
        commitGroups((gs) =>
          gs.map((g) =>
            g.key === key ? { ...g, canonical: { ...g.canonical, name } } : g
          )
        );
        setEditing((cur) => (cur && cur.key === key ? null : cur));
      } catch (e) {
        if (abandoned(e)) return;
        toast(errorMessage(e, 'Failed to rename author'), 'error');
      } finally {
        markPending(key, false);
      }
    },
    [abandoned, commitGroups, markPending, toast]
  );

  const split = useCallback(
    (key: string, canonicalId: AuthorId) => {
      if (pendingRef.current.has(key)) return;
      markPending(key, true);
      enqueue(async (life) => {
        try {
          throwIfLeft(life);
          const cur = groupsRef.current;
          const group =
            cur && cur.load.state === 'ready' ? cur.load.value.groups.find((g) => g.key === key) : undefined;
          const narrators = choicesRef.current.narrators[key] ?? EMPTY_STRINGS;
          const flagged = (group?.splitNames ?? []).filter((n) => narrators.has(splitRoleKey(n)));
          const result = await api.splitCompositeAuthor(canonicalId);
          let failed = 0;
          for (const name of flagged) {
            const match = result.authors.find((a) => a.name === name);
            if (!match) continue;
            try {
              throwIfLeft(life);
              await api.reclassifyAuthorAsNarrator(match.id);
            } catch (e) {
              if (abandoned(e)) throw e;
              failed += 1;
            }
          }
          if (!mountedRef.current) return;
          toast(
            `Split "${group?.canonical.name ?? key}" into ${result.authors.length} authors` +
              (failed > 0 ? ` (${failed} narrator reclassification${failed === 1 ? '' : 's'} failed)` : ''),
            failed > 0 ? 'warning' : 'success'
          );
          reload();
        } catch (e) {
          if (abandoned(e)) return;
          toast(errorMessage(e, 'Failed to split author'), 'error');
        } finally {
          markPending(key, false);
        }
      }, () => markPending(key, false));
    },
    [abandoned, enqueue, markPending, reload, toast]
  );

  const resolveProduction = useCallback(
    (key: string, canonicalId: AuthorId) => {
      if (pendingRef.current.has(key)) return;
      markPending(key, true);
      enqueue(async (life) => {
        try {
          const op = await api.resolveProductionAuthor(canonicalId);
          if (mountedRef.current) toast('Looking up the real author — watch the bell for progress.', 'info');
          const done = await waitFor(op, life);
          if (!mountedRef.current) return;
          if (done.ok) {
            toast('Found the real author(s).', 'success');
            reload();
          } else toast(`Could not resolve the production company: ${done.message}`, 'error');
        } catch (e) {
          if (abandoned(e)) return;
          toast(errorMessage(e, 'Failed to resolve'), 'error');
        } finally {
          markPending(key, false);
        }
      }, () => markPending(key, false));
    },
    [abandoned, enqueue, markPending, reload, toast, waitFor]
  );

  const dispatch = useCallback(
    (action: AuthorsAction) => {
      // `entity` is checked first: a series action must never reach an author
      // endpoint, whatever shape its ids have.
      if (action.entity !== 'author') return;
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
            cur && cur.load.state === 'ready' ? cur.load.value.groups.filter(isMergeable) : [];
          if (now.length !== action.expectedTotal) {
            // The list moved between the confirmation and the click through:
            // what was confirmed is not what would run.
            toast(
              `The list changed while you were confirming (${action.expectedTotal} group(s) became ${now.length}). Nothing was merged; check the count and try again.`,
              'warning'
            );
            return;
          }
          runBulk(now.map((g) => g.key), 'all');
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
    [commitChoices, mergeGroup, rename, resolveProduction, runBulk, split, toast]
  );

  // ---- validate ------------------------------------------------------------------------
  const [validation, setValidation] = useState<Readonly<Record<string, ValidationState>>>({});
  const validateCtls = useRef(new Map<string, AbortController>());
  useEffect(() => {
    if (!active) return;
    const ctls = validateCtls.current;
    return () => {
      for (const c of ctls.values()) c.abort();
      ctls.clear();
    };
  }, [active]);
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

  // ---- scan -----------------------------------------------------------------------------
  const refresh = useCallback(() => {
    if (scanningRef.current) return;
    scanningRef.current = true;
    setScanning(true);
    const settle = () => {
      scanningRef.current = false;
      if (mountedRef.current) setScanning(false);
    };
    enqueue(async (life) => {
      try {
        const op = await api.refreshAuthorDuplicates();
        if (mountedRef.current) toast('Author scan started — watch the bell for progress.', 'info');
        const done = await waitFor(op, life);
        if (!mountedRef.current) return;
        if (done.ok) {
          toast('Author scan finished.', 'success');
          clearSelection();
          reload();
          candidatesReload();
        } else toast(`Author scan did not finish: ${done.message}`, 'error');
      } catch (e) {
        if (abandoned(e)) return;
        toast(errorMessage(e, 'Failed to start the author scan'), 'error');
      } finally {
        settle();
      }
    }, settle);
  }, [abandoned, enqueue, toast, waitFor, reload, candidatesReload, clearSelection]);

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

  // ---- keyboard ----------------------------------------------------------------------------
  // In the hook, with the lane's state, and bound only while the lane is active:
  // a window listener that outlives the lane would move a focus ring nobody sees.
  useEffect(() => {
    if (!active) return;
    const onKey = (event: KeyboardEvent) => {
      if (event.key === '?' && helpOpen) {
        event.preventDefault();
        setHelpOpen(false);
        return;
      }
      // Escape runs before the typing guard: the popover and dialogs are
      // role=dialog and take focus, and closing them is the whole job.
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
          event.preventDefault();
          setBooksFor({
            key: focused.key,
            anchor: document.querySelector<HTMLElement>(`[data-author-books-chip="${focused.key}"]`),
          });
          return;
        case 'm':
          if (!isMergeable(focused) || pendingRef.current.has(focused.key)) return;
          event.preventDefault();
          dispatch({ lane: 'authors', entity: 'author', type: 'mergeGroup', canonicalId: focused.canonical.id });
          return;
      }
    };
    window.addEventListener('keydown', onKey);
    return () => window.removeEventListener('keydown', onKey);
  }, [active, booksFor, helpOpen, editing, pageGroups, focusedKey, dispatch]);

  // ---- result ------------------------------------------------------------------------------
  const status: ListStatus =
    groupsLoad.state === 'loading'
      ? 'loading'
      : groupsLoad.state === 'error'
        ? 'error'
        : needsScan
          ? 'needs-scan'
          : groups.length === 0
            ? 'empty'
            : 'populated';

  return {
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

/** Rendered by the shortcut help overlay. Kept beside the handler that implements them. */
export const AUTHOR_SHORTCUTS = [
  { keys: 'j / k', action: 'Move to the next / previous group' },
  { keys: 'm', action: 'Merge the focused group' },
  { keys: 'Enter', action: "Show the focused group's books" },
  { keys: 'Esc', action: 'Close the books list, this help, or the name editor' },
  { keys: '?', action: 'Toggle this shortcut help' },
];
