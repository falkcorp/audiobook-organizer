// file: core/web/packages/ui-kit/src/hooks/useDebouncedSearch.ts
// version: 0.1.0
// guid: b8655ce0-180f-49e5-b33d-97abbdadfe31
// last-edited: 2026-11-21

import { useCallback, useEffect, useState } from 'react';

/**
 * The debounced twin of a search box's raw value, for a term that gates a
 * NETWORK REQUEST rather than a local filter.
 *
 * The raw value stays wherever it lives so the text field never lags the
 * typist; this returns the settled term to send. A page whose search runs on the
 * server and one whose search narrows a list in the browser need the same
 * mechanism, so it lives here once.
 *
 * What is deliberately NOT shared is the client-side predicate a list runs
 * during the debounce window. Those differ on purpose (one matches a label the
 * server has no counterpart for, another matches joined fields), and collapsing
 * them into one "generic" matcher would mean inventing a predicate nobody wants.
 *
 * @param value the raw, per-keystroke term
 * @param delayMs how long to wait after the last keystroke
 * @param initial the term that is already settled on mount (a search arrived at
 *   through a link): without it the twin starts empty and a page that arrives
 *   with `?q=` first fetches unfiltered, then again once the timer fires
 * @returns the settled term, safe to put in a request
 */
export function useDebouncedSearch(value: string, delayMs: number, initial = ''): string {
  return useSettledSearch(value, delayMs, initial).settled;
}

/**
 * `useDebouncedSearch` plus a way to settle a term at once.
 *
 * A page that mirrors its search into the address bar mirrors the SETTLED term
 * (a history write per keystroke is both noise and, past a browser's cap, an
 * error). When the address bar itself changes the search (a pasted link,
 * back/forward) the new term must be settled immediately, or the mirror would
 * see the old settled term and write it straight back over the link.
 */
export function useSettledSearch(
  value: string,
  delayMs: number,
  initial = ''
): { settled: string; settleNow: (term: string) => void } {
  const [debounced, setDebounced] = useState(initial);

  useEffect(() => {
    // An empty box clears its twin in the SAME tick rather than waiting out the
    // timer. Leaving it to the timer means the moment the user clears the
    // box, the list spends delayMs still showing results for a term that is no
    // longer typed anywhere -- which reads as the clear having failed.
    if (value === '') {
      setDebounced('');
      return;
    }
    if (value === debounced) return;
    const t = setTimeout(() => setDebounced(value), delayMs);
    return () => clearTimeout(t);
  }, [value, debounced, delayMs]);

  const settleNow = useCallback((term: string) => setDebounced(term), []);
  return { settled: debounced, settleNow };
}

/**
 * Whether the server has already answered for `query`, meaning the list's own
 * client-side filter must STAND DOWN and show the server's rows untouched.
 *
 * This is a correctness guard, not an optimisation: the server's predicate is
 * usually WIDER than any client one (it matches fields the browser's rows do not
 * carry, or resolves a name through another table), so a client filter left
 * running throws away rows the server correctly found. The symptom is "1
 * matched, 0 shown", and the row is unfindable.
 *
 * Compared case-folded and trimmed because the server matches that way. The
 * question is "has the server answered for THIS term", not "are these two
 * strings byte-identical".
 *
 * @param appliedSearch the term the currently-loaded rows were fetched with
 * @param query the term the user is filtering by now
 */
export function serverAnsweredTerm(appliedSearch: string, query: string): boolean {
  return appliedSearch.trim().toLowerCase() === query.trim().toLowerCase();
}
