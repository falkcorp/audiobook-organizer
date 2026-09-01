// file: web/src/hooks/useDebouncedSearch.ts
// version: 1.0.0
// guid: 3d8b1c47-6e29-4a05-9f73-2c5a8e0b4d16
// last-edited: 2026-09-01

import { useEffect, useState } from 'react';

/**
 * The debounced twin of a search box's raw value, for a term that gates a
 * NETWORK REQUEST rather than a local filter.
 *
 * The raw value stays wherever it lives so the text field never lags the
 * typist; this returns the settled term to send. Two lanes need exactly this
 * and previously only one had it: useRegroupLane hand-rolled it when review
 * search was pushed server-side, and useDupesLane was about to grow a second
 * copy when dedup search followed. The mechanism is identical in both, so it
 * lives here once.
 *
 * What is deliberately NOT shared is the client-side predicate each lane runs
 * during the debounce window. Those differ on purpose -- regroup also matches
 * a kind label the server has no counterpart for, dupes matches band/layer and
 * joined book fields -- and collapsing them into one "generic" matcher would
 * mean inventing a predicate neither lane actually wants.
 *
 * @param value the raw, per-keystroke term
 * @param delayMs how long to wait after the last keystroke
 * @returns the settled term, safe to put in a request
 */
export function useDebouncedSearch(value: string, delayMs: number): string {
  const [debounced, setDebounced] = useState('');

  useEffect(() => {
    // An empty box clears its twin in the SAME tick rather than waiting out the
    // timer. Leaving it to the timer means the moment the reviewer clears the
    // box, the lane spends delayMs still showing results for a term that is no
    // longer typed anywhere -- which reads as the clear having failed.
    if (value === '') {
      setDebounced('');
      return;
    }
    if (value === debounced) return;
    const t = setTimeout(() => setDebounced(value), delayMs);
    return () => clearTimeout(t);
  }, [value, debounced, delayMs]);

  return debounced;
}

/**
 * Whether the server has already answered for `query`, meaning the lane's own
 * client-side filter must STAND DOWN and show the server's rows untouched.
 *
 * This is a correctness guard, not an optimisation, and it is shared because
 * getting it wrong fails the same way in every lane: the server's predicate is
 * WIDER than any client one, so a client filter left running throws away rows
 * the server correctly found. Regroup hit this with recommendationReason --
 * the sentence a reviewer actually reads, which the server matches and the
 * client index does not hold. Dupes hits it with author name, which the server
 * resolves through the author table and the client cannot see at all, because
 * the book objects it renders carry no author_name key.
 *
 * Both produce the same symptom: "1 matched, 0 shown", and the row still
 * unfindable -- the exact defect server-side search was added to fix, wearing a
 * different hat.
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
