// file: web/src/hooks/useUrlState.ts
// version: 2.3.1
// guid: 3f6c1a52-7d94-4e08-b5a1-9c20e8d4f713
// last-edited: 2026-11-07
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
//   there is anything to write, is the LIVE one (the history object's location):
//   the router commits location changes in a transition, so the rendered
//   `searchParams` can be a step behind. `pending` is kept only to recognise our
//   own echoes.
// * A write is DROPPED when the live pathname is not the rendered one (the page is
//   on its way out: a link to a lazy page whose chunk is still loading) or the live
//   query is neither the rendered one nor one we issued (an external navigation is
//   in flight). Without that, a throttled trailing write fires after the click and
//   puts the old page back.
// * A write that throws is forgotten again (it leaves `pending`).
// * A write still waiting on the throttle is flushed BEFORE a navigation starts,
//   not after it (see FLUSHING BEFORE A NAVIGATION). The unmount flush that used
//   to be the only safety net cannot do this job: by the time a page unmounts the
//   address has already moved on, so `liveBase` rightly drops the write, and an
//   entry that has been pushed over cannot be rewritten. The unmount flush
//   remains for a mirror that leaves the tree while the address stays put.
// * `useUrlParamWriter` gives other components the same view of "latest", so a
//   write from a child does not merge into stale params and revert a mirrored key.
// * The pathname compared against the rendered one has the router's basename
//   stripped first: the history object reports the full path, `useLocation` the
//   path relative to the basename.
// * Writes are throttled (leading and trailing, `WRITE_THROTTLE_MS`). Browsers
//   cap history.replaceState (Safari throws a SecurityError past ~100 calls per
//   30 s) and a held key or a typed search would reach that in seconds. A single
//   click writes at once; a burst writes the first and the last.
//
// FLUSHING BEFORE A NAVIGATION
//
// The throttle leaves a window (up to `WRITE_THROTTLE_MS`) in which the page's
// latest state is not yet in the address bar. Leave the page inside it and the
// entry you left holds the state from before the burst, so Back lands on a
// stale page (`?q=1` after you had typed `q=2`).
//
// `flushOnNavigate` wraps the router's navigator. Every mirror registers a
// callback with it, and a push or a traversal (`navigate(-1)`) first runs them
// all against the CURRENT location, then delegates. That is the only moment the
// entry being left can still be written. A replace is not flushed: it overwrites
// the entry being left anyway. `<FlushOnNavigate>` (mounted just inside the
// router in main.tsx) installs the wrapper and also flushes on `pagehide`, so a
// reload or a tab close inside the window keeps the last state too.
//
// NOT COVERED: the browser's own Back/Forward buttons. By the time `popstate`
// fires the browser has already switched entries, so `replaceState` would write
// the entry being ENTERED, not the one being left, and the write is dropped as
// it must be. That path can lose a trailing write at most `WRITE_THROTTLE_MS`
// old; the entry then shows the previous burst value. (The Navigation API's
// `navigate` event fires before a traversal commits, but it is not available in
// every supported browser, and rewriting history from inside a traversal event
// is not something to rely on.) Links and `navigate()` calls, which is how pages
// are left in practice, are covered.

import {
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ContextType,
} from 'react';
import {
  UNSAFE_NavigationContext,
  useLocation,
  useSearchParams,
  type Navigator,
} from 'react-router';

type NavigationValue = ContextType<typeof UNSAFE_NavigationContext>;

/** A value for one owned key: null/undefined/'' means "absent from the URL". */
export type UrlValue = string | null | undefined;

/** Keys to write through `useUrlParamWriter`; null removes one. */
export type UrlPatch = Readonly<Record<string, UrlValue>>;

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
  /** The subset of `pending` that came from `useUrlParamWriter`, not from a mirror. */
  foreign: Set<string>;
  /** The last query string `observe` was given, and what it decided. */
  seen: string | null;
  verdict: Verdict;
  /** Mirrors' "write what is waiting now" callbacks, run before a navigation. */
  flushers: Set<() => void>;
}

/**
 * What a newly rendered query string is:
 * - `echo`: a write a mirror made; nothing to do.
 * - `foreign`: a write a child made through `useUrlParamWriter`; the mirror owns
 *   keys the child did not touch, but the write can still have left the address
 *   bar saying something the screen does not (clearing `book` from a link that
 *   only implied its lane), so the mirror reconciles once.
 * - `external`: not ours at all.
 */
type Verdict = 'echo' | 'foreign' | 'external';

const stores = new WeakMap<object, UrlStore>();

function storeFor(key: object): UrlStore {
  let store = stores.get(key);
  if (!store) {
    store = {
      pending: [],
      foreign: new Set(),
      seen: null,
      verdict: 'external',
      flushers: new Set(),
    };
    stores.set(key, store);
  }
  return store;
}

/**
 * Classify a newly rendered query string once, however many hooks look at it.
 * Ours when it is one we issued (it and everything before it leave `pending`:
 * writes that landed in one batch are superseded, and a stale entry would
 * swallow a real navigation to that same URL later); otherwise external, and
 * whatever we had in flight is moot. The LAST match is the one that rendered:
 * a batch that wrote A, B, A commits A, and stopping at the first A would leave
 * [B, A] behind for `liveBase` to trust.
 */
function observe(store: UrlStore, search: string): Verdict {
  if (store.seen === search) return store.verdict;
  store.seen = search;
  const at = store.pending.lastIndexOf(search);
  if (at >= 0) {
    const landed = store.pending.splice(0, at + 1);
    store.verdict = landed.some((p) => store.foreign.has(p)) ? 'foreign' : 'echo';
    landed.forEach((p) => store.foreign.delete(p));
  } else {
    store.pending = [];
    store.foreign.clear();
    store.verdict = 'external';
  }
  return store.verdict;
}

/**
 * history.replaceState can throw (Safari and Firefox rate-limit it): a lost write
 * beats a dead page. BrowserRouter throws synchronously; a data router returns a
 * promise that rejects, so both are caught. `onFail` runs for either, so the
 * caller can forget a URL that never became the address.
 */
function safeSet(
  set: ReturnType<typeof useSearchParams>[1],
  next: URLSearchParams,
  onFail: () => void
): void {
  const fail = (err: unknown) => {
    console.warn('Could not update the address bar', err);
    onFail();
  };
  try {
    const result = set(next, { replace: true }) as unknown;
    if (result instanceof Promise) result.catch(fail);
  } catch (err) {
    fail(err);
  }
}

interface Where {
  pathname: string;
  search: string;
}

/**
 * `pathname` relative to the router's basename, the way `useLocation` reports it.
 * The history object's location is the full browser path. A path outside the
 * basename is returned as it is, so it never equals a rendered pathname.
 */
function stripBasename(pathname: string, basename: string | undefined): string {
  if (!basename || basename === '/') return pathname;
  const base = (basename.endsWith('/') ? basename.slice(0, -1) : basename).toLowerCase();
  const lower = pathname.toLowerCase();
  if (lower === base) return '/';
  if (!lower.startsWith(`${base}/`)) return pathname;
  return pathname.slice(base.length) || '/';
}

/**
 * The query string a write must be merged into, or null when the write must be
 * dropped.
 *
 * The router commits a location change in a transition, so what a component has
 * RENDERED can trail the address bar: a link to a page whose chunk is still
 * loading leaves the old page mounted at the old pathname while the address
 * already says the new one. A write made from the old page then resolves against
 * the old pathname and puts the old page back. So the LIVE location (the history
 * object's, which BrowserRouter and MemoryRouter expose) decides:
 * - a different pathname than the rendered one: the page is on its way out;
 * - a query string that is neither the rendered one nor one we issued: an
 *   external navigation is in flight, and what we hold is about to be replaced.
 * Either way the write is dropped; the reconciling pass after an external change
 * rewrites whatever the page still needs. Otherwise the live string is the base,
 * so a write never merges into a stale copy. A router that does not expose its
 * location (a data router) falls back to the latest string this hook issued.
 */
function liveBase(store: UrlStore, nav: NavigationValue, rendered: Where): string | null {
  const renderedStr = new URLSearchParams(rendered.search).toString();
  const live = (nav.navigator as { location?: Where }).location;
  if (!live) return store.pending.at(-1) ?? renderedStr;
  if (stripBasename(live.pathname, nav.basename) !== rendered.pathname) return null;
  const liveStr = new URLSearchParams(live.search).toString();
  return liveStr === renderedStr || store.pending.includes(liveStr) ? liveStr : null;
}

/** Record `nextStr` as issued and write it; a write that fails is forgotten again. */
function issue(
  store: UrlStore,
  set: ReturnType<typeof useSearchParams>[1],
  next: URLSearchParams,
  foreign: boolean
): void {
  const nextStr = next.toString();
  store.pending.push(nextStr);
  if (foreign) store.foreign.add(nextStr);
  safeSet(set, next, () => {
    const at = store.pending.lastIndexOf(nextStr);
    if (at >= 0) store.pending.splice(at, 1);
    if (!store.pending.includes(nextStr)) store.foreign.delete(nextStr);
  });
}

/**
 * Write some query keys from a component that does not own the page's mirror
 * (a child panel with its own filter chips). The patch is merged into the LATEST
 * query string, including writes still in flight, so it cannot revert a key the
 * page's `useUrlStateSync` just wrote. A null value removes the key. The patch
 * may be a function of that latest query string, for a write that depends on it
 * (a toggle: clearing a band only when it is the one the address holds).
 */
export function useUrlParamWriter(): (
  patch: UrlPatch | ((live: URLSearchParams) => UrlPatch)
) => void {
  const [searchParams, setSearchParams] = useSearchParams();
  const { pathname, search: renderedSearch } = useLocation();
  const nav = useContext(UNSAFE_NavigationContext);
  const store = storeFor(nav.navigator);
  const search = searchParams.toString();
  // Keep `pending` honest even on a page with no mirror of its own.
  useEffect(() => {
    observe(store, search);
  }, [store, search]);
  const latest = useRef({ pathname, renderedSearch, setSearchParams, store, nav });
  useEffect(() => {
    latest.current = { pathname, renderedSearch, setSearchParams, store, nav };
  });
  return useCallback((patch) => {
    const {
      pathname: path,
      renderedSearch: rs,
      setSearchParams: set,
      store: st,
      nav: n,
    } = latest.current;
    const base = liveBase(st, n, { pathname: path, search: rs });
    if (base === null) return;
    const live = new URLSearchParams(base);
    const next = applyOwnedParams(live, typeof patch === 'function' ? patch(live) : patch);
    if (next.toString() === base) return;
    issue(st, set, next, true);
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
  const { pathname, search: renderedSearch } = useLocation();
  const nav = useContext(UNSAFE_NavigationContext);
  const store = storeFor(nav.navigator);
  const search = searchParams.toString();

  // Latest-value refs, assigned in an effect so render stays pure. They are read
  // only inside the effects and timers below, which run after this one.
  const latest = useRef({
    pathname,
    renderedSearch,
    defaults,
    onExternalChange,
    setSearchParams,
    owned,
    nav,
  });
  useEffect(() => {
    latest.current = {
      pathname,
      renderedSearch,
      defaults,
      onExternalChange,
      setSearchParams,
      owned,
      nav,
    };
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
    if (timer.current) clearTimeout(timer.current);
    timer.current = null;
    const { defaults: d, setSearchParams: set, owned: o, nav: n } = latest.current;
    const base = liveBase(store, n, {
      pathname: latest.current.pathname,
      search: latest.current.renderedSearch,
    });
    if (base === null) return;
    const next = applyOwnedParams(new URLSearchParams(base), o, d);
    if (next.toString() === base) return;
    lastWrite.current = Date.now();
    issue(store, set, next, false);
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

  // A write still waiting on the throttle is written NOW when a navigation is about
  // to start (see FLUSHING BEFORE A NAVIGATION), through the callback registered
  // here, and again if this mirror unmounts with the address unchanged. The
  // unmount flush is no help for a page that is being LEFT: by cleanup time the
  // address has moved on, so `flush` finds a different pathname and drops the
  // write, as it must, because it would land on the page that replaced this one.
  useEffect(() => {
    const flushWaiting = () => {
      if (timer.current) flush();
    };
    store.flushers.add(flushWaiting);
    return () => {
      store.flushers.delete(flushWaiting);
      flushWaiting();
    };
  }, [flush, store]);

  const seen = useRef(search);
  useEffect(() => {
    if (search === seen.current) return;
    seen.current = search;
    const verdict = observe(store, search);
    if (verdict === 'echo') return;
    // The nonce is the point: a reconciling mirror pass has to run in a LATER
    // render than the one that saw this URL, after the page's own state setters
    // have been applied, and it must run even if no state changed. A synchronous
    // setState in this effect is the only way to schedule that render.
    if (verdict === 'foreign') {
      // eslint-disable-next-line react-hooks/set-state-in-effect
      setReconcile((n) => n + 1);
      return;
    }
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

/**
 * Run every mirror's waiting write now, against the location still on screen. A
 * flusher that throws is reported and skipped: one broken mirror must not stop
 * the others, the navigation that triggered this, or the `pagehide` handler.
 */
export function flushPendingWrites(navigator: object): void {
  for (const flushWaiting of [...storeFor(navigator).flushers]) {
    try {
      flushWaiting();
    } catch (err) {
      console.warn('Could not write a pending address change', err);
    }
  }
}

/**
 * Write what is waiting, then forget every write still in flight. The flush put
 * its own query string into `pending`, but the page is about to be left: that
 * string will never render, and the push or traversal target is by definition
 * not one of our writes. Left in `pending` it would make a later real
 * navigation to the same query look like our echo (the page would never be told
 * about it) and `liveBase` would trust it. This is what `observe` does for any
 * URL it did not issue.
 */
function flushBeforeLeaving(navigator: object): void {
  flushPendingWrites(navigator);
  const store = storeFor(navigator);
  store.pending = [];
  store.foreign.clear();
}

const wrappers = new WeakMap<object, Navigator>();

/**
 * A navigator that writes the mirrors' waiting trailing writes before it pushes
 * or traverses history, so the entry being left holds what the page showed.
 * Hooks under the router must see THIS navigator (it keys the store), which is
 * what `<FlushOnNavigate>` arranges. One wrapper per raw navigator, so there is
 * one store per router however often this is called. Every other member
 * (`location`, `createURL`, `listen`, `action`, ...) is inherited from the real
 * navigator, so the live `location` the hooks read is the router's own.
 */
export function flushOnNavigate(navigator: Navigator): Navigator {
  const cached = wrappers.get(navigator);
  if (cached) return cached;
  const wrapped: Navigator = Object.create(navigator, {
    go: {
      value: (delta: number) => {
        flushBeforeLeaving(wrapped);
        navigator.go(delta);
      },
      enumerable: true,
    },
    push: {
      value: (...args: Parameters<Navigator['push']>) => {
        flushBeforeLeaving(wrapped);
        navigator.push(...args);
      },
      enumerable: true,
    },
    // A replace overwrites the entry being left, so a flush first would be lost
    // work; and the mirrors' own writes are replaces, so it would also recurse.
    replace: {
      value: (...args: Parameters<Navigator['replace']>) => navigator.replace(...args),
      enumerable: true,
    },
  });
  wrappers.set(navigator, wrapped);
  return wrapped;
}
