// file: web/src/hooks/useUrlState.ts
// version: 1.0.0
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
// WHY THE MIRROR NEVER FEEDS BACK, AND WHY IT SKIPS THE FIRST RUN
//
// ReviewWorkspace used to read `?lane=` once and never write it, because a tab
// click that wrote the param while lane state also derived from it fetched the
// lane's data twice per switch. The mirror avoids that by construction: nothing
// in the page ever reads the param back except `onExternalChange`, and that runs
// only for a URL change the mirror did not make. So a click changes state once,
// the effect writes the URL, the URL echo is recognised as ours and ignored.
//
// The first run is skipped so that arriving on a link does not rewrite it: a seed
// that has not been consumed yet (the fixer list is still loading, say) would
// otherwise be erased from the address bar by the first mirror pass.
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

import { useCallback, useEffect, useMemo, useRef } from 'react';
import { useSearchParams } from 'react-router-dom';

/** A value for one owned key: null/undefined/'' means "absent from the URL". */
export type UrlValue = string | null | undefined;

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
      setSearchParams(
        (prev) => applyOwnedParams(prev, { [key]: next }, { [key]: fallback }),
        { replace: true }
      );
    },
    [key, fallback, setSearchParams]
  );
  return [value, set];
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
  const search = searchParams.toString();

  // Latest-value refs, assigned in an effect so render stays pure. Both are read
  // only inside the effects below, which run after this one in the same commit.
  const latest = useRef({ searchParams, defaults, onExternalChange, setSearchParams });
  useEffect(() => {
    latest.current = { searchParams, defaults, onExternalChange, setSearchParams };
  });

  const pending = useRef<string[]>([]);
  const seen = useRef(search);

  // A stable key for the owned map: the effect below must run when a VALUE
  // changes, not on every render's fresh object.
  const ownedKey = useMemo(() => JSON.stringify(Object.entries(owned)), [owned]);

  const firstRun = useRef(true);
  useEffect(() => {
    if (firstRun.current) {
      firstRun.current = false;
      return;
    }
    const { searchParams: current, defaults: d, setSearchParams: set } = latest.current;
    const next = applyOwnedParams(current, owned, d);
    const nextStr = next.toString();
    if (nextStr === current.toString()) return;
    pending.current.push(nextStr);
    set(next, { replace: true });
    // Deliberately keyed on the owned VALUES only. Adding `search` here would
    // rewrite the URL the moment an external change arrived, before
    // onExternalChange had moved the state, and revert the very navigation the
    // user made.
  }, [ownedKey]); // eslint-disable-line react-hooks/exhaustive-deps

  useEffect(() => {
    if (search === seen.current) return;
    seen.current = search;
    const at = pending.current.indexOf(search);
    if (at >= 0) {
      pending.current.splice(0, at + 1);
      return;
    }
    // A URL we did not write; whatever we had in flight is now moot.
    pending.current = [];
    latest.current.onExternalChange?.(new URLSearchParams(search));
  }, [search]);
}
