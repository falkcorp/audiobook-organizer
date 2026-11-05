// file: web/src/hooks/useUrlState.ts
// version: 2.0.0
// guid: 3f6c1a52-7d94-4e08-b5a1-9c20e8d4f713
// last-edited: 2026-11-05
//
// Deep links: keep the address bar equal to what is on screen.
//
// Two hooks, for the two shapes of page.
//
// `useUrlEnumParam` is for a page whose only navigable state is one choice from
// a closed set (a tab). The URL IS the state: the value is parsed out of the
// query string on every render and the setter writes it back. Back/forward and
// a pasted link need no code at all, and there is no second copy to disagree.
//
// `useUrlStateSync` is for a page whose state lives in hooks that are already
// written and tested around local state (the Review lanes, the Activity feed).
// Component state stays the source of truth and an effect MIRRORS the keys the
// page owns into the query string. One way: state -> URL.
//
// WHY THE MIRROR NEVER FEEDS BACK, AND WHY IT SKIPS THE MOUNT
//
// ReviewWorkspace used to read `?lane=` once and never write it, because a tab
// click that wrote the param while lane state also derived from it fetched the
// lane's data twice per switch. The mirror avoids that by construction: nothing
// in the page ever reads the param back except `onExternalChange`, and that runs
// only for a URL change the mirror did not make. So a click changes state once,
// the effect writes the URL, the URL echo is recognised as ours and ignored.
//
// The mount is skipped so that arriving on a link does not rewrite it: a seed
// that has not been consumed yet (the fixer list is still loading, say) would
// otherwise be erased from the address bar by the first mirror pass. It is
// skipped by VALUE (the owned values the page mounted with), not by a "first run"
// flag, so StrictMode's second effect pass, which has the same values, skips too.
//
// RECOGNISING OUR OWN ECHO
//
// Every write is remembered as an ordered list of pending query strings. When a
// URL arrives it is ours if it is in that list; everything up to and including
// it is then dropped (writes that landed in the same batch are superseded, and a
// stale entry would swallow a real navigation to that same URL later). Anything
// else is external -- a link click on the same route, back/forward, an edited
// address -- and is handed to `onExternalChange`, which must be idempotent: it is
// called with the whole query string and sets state to match.
//
// RECONCILING AFTER AN EXTERNAL CHANGE
//
// `onExternalChange` adopts what it can, and a page is free to ignore part of a
// URL (an unknown fixer id, a lane it does not have, a link that names no lane).
// Left alone, the address bar would then say one thing and the screen another.
// So every external change is followed by exactly one reconciling pass, which
// rewrites the URL from the state the page actually ended up in. There is no
// per-page code for it: whatever the page did not adopt is simply overwritten.
//
// WRITING SAFELY
//
// * The query string a write is merged into, and compared against to see whether
//   there is anything to write, is the LATEST one issued (the router commits
//   location changes in a transition, so the rendered `searchParams` can be a
//   step behind): a write that is undone before it commits is still written.
// * `useUrlParamWriter` gives other components the same view of "latest", so a
//   write from a child does not merge into stale params and revert a mirrored key.
// * Writes are throttled (leading and trailing, `WRITE_THROTTLE_MS`). Browsers
//   cap history.replaceState (Safari throws a SecurityError past ~100 calls per
//   30 s) and a held key or a typed search would reach that in seconds. A single
//   click writes at once; a burst writes the first and the last.

import { useCallback, useContext, useEffect, useMemo, useRef, useState } from 'react';
import { UNSAFE_NavigationContext, useSearchParams } from 'react-router-dom';

/** A value for one owned key: null/undefined/'' means "absent from the URL". */
export type UrlValue = string | null | undefined;

/** Minimum gap between two history writes from one mirror (leading + trailing). */
export const WRITE_THROTTLE_MS = 400;

/**
 * The query string after writing `owned` over `current`: keys not in `owned` are
 * kept as they are, an owned key whose value is empty or equal to its default is
 * removed, any other is set. Pure, so the rule can be tested without a router.
 */
export function applyOwnedParams(
  current: URLSearchParams,
  owned: Readonly<Record<string, UrlValue>>,
  defaults: Readonly<Record<string, string>> = {}
): URLSearchParams {
  const next = new URLSearchParams(current);
  for (const [key, value] of Object.entries(owned)) {
    if (value === null || value === undefined || value === '' || value === defaults[key]) {
      next.delete(key);
    } else {
      next.set(key, value);
    }
  }
  return next;
}

/** `value` when it is one of `allowed`, otherwise `fallback`. */
export function parseEnum<T extends string>(
  value: string | null | undefined,
  allowed: readonly T[],
  fallback: T
): T {
  return allowed.find((a) => a === value) ?? fallback;
}

/**
 * One closed-set choice (a tab) that lives in the URL as `?<key>=<value>`.
 * The default is omitted from the URL, and an unknown value reads as the default.
 * Other query keys are preserved, and the write replaces the history entry.
 */
export function useUrlEnumParam<T extends string>(
  key: string,
  allowed: readonly T[],
  fallback: T
): [T, (next: T) => void] {
  const [searchParams, setSearchParams] = useSearchParams();
  const value = parseEnum(searchParams.get(key), allowed, fallback);
  const set = useCallback(
    (next: T) => {
      setSearchParams((prev) => applyOwnedParams(prev, { [key]: next }, { [key]: fallback }), {
        replace: true,
      });
    },
    [key, fallback, setSearchParams]
  );
  return [value, set];
}

// ---------------------------------------------------------------------------
// The per-router record of what we wrote and have not yet seen come back.
// Keyed by the router's navigator so it is shared by every hook under one router
// and never leaks between routers (tests mount many).
// ---------------------------------------------------------------------------

interface UrlStore {
  /** Query strings issued, oldest first, not yet seen rendered. */
  pending: string[];
  /** The last query string `observe` was given, and what it decided. */
  seen: string | null;
  verdict: 'echo' | 'external';
}

const stores = new WeakMap<object, UrlStore>();

function storeFor(key: object): UrlStore {
  let store = stores.get(key);
  if (!store) {
    store = { pending: [], seen: null, verdict: 'external' };
    stores.set(key, store);
  }
  return store;
}

/**
 * Classify a newly rendered query string once, however many hooks look at it.
 * Ours when it is one we issued (it and everything before it leave `pending`:
 * writes that landed in one batch are superseded, and a stale entry would
 * swallow a real navigation to that same URL later); otherwise external, and
 * whatever we had in flight is moot.
 */
function observe(store: UrlStore, search: string): 'echo' | 'external' {
  if (store.seen === search) return store.verdict;
  store.seen = search;
  const at = store.pending.indexOf(search);
  if (at >= 0) {
    store.pending.splice(0, at + 1);
    store.verdict = 'echo';
  } else {
    store.pending = [];
    store.verdict = 'external';
  }
  return store.verdict;
}

function useUrlStore(): UrlStore {
  const nav = useContext(UNSAFE_NavigationContext);
  return storeFor(nav.navigator);
}

/**
 * history.replaceState can throw (Safari and Firefox rate-limit it): a lost write
 * beats a dead page. BrowserRouter throws synchronously; a data router returns a
 * promise that rejects, so both are caught.
 */
function safeSet(set: ReturnType<typeof useSearchParams>[1], next: URLSearchParams): boolean {
  const warn = (err: unknown) => console.warn('Could not update the address bar', err);
  try {
    const result = set(next, { replace: true }) as unknown;
    if (result instanceof Promise) result.catch(warn);
    return true;
  } catch (err) {
    warn(err);
    return false;
  }
}

/**
 * Write some query keys from a component that does not own the page's mirror
 * (a child panel with its own filter chips). The patch is merged into the LATEST
 * query string, including writes still in flight, so it cannot revert a key the
 * page's `useUrlStateSync` just wrote. A null value removes the key.
 */
export function useUrlParamWriter(): (patch: Readonly<Record<string, UrlValue>>) => void {
  const [searchParams, setSearchParams] = useSearchParams();
  const store = useUrlStore();
  const search = searchParams.toString();
  // Keep `pending` honest even on a page with no mirror of its own.
  useEffect(() => {
    observe(store, search);
  }, [store, search]);
  const latest = useRef({ searchParams, setSearchParams, store });
  useEffect(() => {
    latest.current = { searchParams, setSearchParams, store };
  });
  return useCallback((patch) => {
    const { searchParams: rendered, setSearchParams: set, store: st } = latest.current;
    const base = new URLSearchParams(st.pending.at(-1) ?? rendered.toString());
    const next = applyOwnedParams(base, patch);
    const nextStr = next.toString();
    if (nextStr === (st.pending.at(-1) ?? rendered.toString())) return;
    st.pending.push(nextStr);
    safeSet(set, next);
  }, []);
}

export interface UseUrlStateSyncOptions {
  /** Owned key -> value that equals "the default"; written as absent. */
  defaults?: Readonly<Record<string, string>>;
  /**
   * Called with the full query string when it changes in a way this hook did not
   * write. Must set state to match and must be idempotent.
   */
  onExternalChange?: (params: URLSearchParams) => void;
}

/**
 * Mirror `owned` into the query string, and report URL changes we did not make.
 *
 * `owned` lists every key this page controls with its CURRENT value. A key whose
 * value is empty (or the default) is removed, so pass an owned key as null when
 * it does not apply to the view on screen (a sub-view of another lane) and it is
 * cleaned out of the address bar. Keys not listed are never touched.
 */
export function useUrlStateSync(
  owned: Readonly<Record<string, UrlValue>>,
  { defaults, onExternalChange }: UseUrlStateSyncOptions = {}
): void {
  const [searchParams, setSearchParams] = useSearchParams();
  const store = useUrlStore();
  const search = searchParams.toString();

  // Latest-value refs, assigned in an effect so render stays pure. They are read
  // only inside the effects and timers below, which run after this one.
  const latest = useRef({ searchParams, defaults, onExternalChange, setSearchParams, owned });
  useEffect(() => {
    latest.current = { searchParams, defaults, onExternalChange, setSearchParams, owned };
  });

  // A stable key for the owned map: the mirror must run when a VALUE changes, not
  // on every render's fresh object.
  const ownedKey = useMemo(() => JSON.stringify(Object.entries(owned)), [owned]);

  // Bumped after every external change so one reconciling pass always follows it,
  // even when the state ended up exactly where it already was.
  const [reconcile, setReconcile] = useState(0);

  const mountKey = useRef<string | null>(ownedKey);
  const lastWrite = useRef(0);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const reconciled = useRef(0);

  const flush = useCallback(() => {
    timer.current = null;
    const { searchParams: rendered, defaults: d, setSearchParams: set, owned: o } = latest.current;
    const base = store.pending.at(-1) ?? rendered.toString();
    const next = applyOwnedParams(new URLSearchParams(base), o, d);
    const nextStr = next.toString();
    if (nextStr === base) return;
    lastWrite.current = Date.now();
    store.pending.push(nextStr);
    safeSet(set, next);
  }, [store]);

  useEffect(() => {
    const forced = reconcile !== reconciled.current;
    reconciled.current = reconcile;
    if (mountKey.current !== null) {
      // Arriving on a link must not rewrite it (see the header). A pass with the
      // values we mounted with, however many times the effect runs, is skipped.
      if (ownedKey === mountKey.current && !forced) return;
      mountKey.current = null;
    }
    const wait = WRITE_THROTTLE_MS - (Date.now() - lastWrite.current);
    if (forced || wait <= 0) {
      if (timer.current) clearTimeout(timer.current);
      flush();
    } else if (!timer.current) {
      timer.current = setTimeout(flush, wait);
    }
    // Deliberately keyed on the owned VALUES (and the reconcile nonce) only.
    // Adding `search` here would rewrite the URL the moment an external change
    // arrived, before onExternalChange had moved the state, and revert the very
    // navigation the user made.
  }, [ownedKey, reconcile, flush]);

  // A write that has not fired when the page goes away is dropped.
  useEffect(
    () => () => {
      if (timer.current) clearTimeout(timer.current);
      timer.current = null;
    },
    []
  );

  const seen = useRef(search);
  useEffect(() => {
    if (search === seen.current) return;
    seen.current = search;
    if (observe(store, search) === 'echo') return;
    // A URL we did not write: any write still waiting on the throttle was made
    // against the old URL and must not land on top of this one.
    if (timer.current) {
      clearTimeout(timer.current);
      timer.current = null;
    }
    latest.current.onExternalChange?.(new URLSearchParams(search));
    setReconcile((n) => n + 1);
  }, [search, store]);
}
