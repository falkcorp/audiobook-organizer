// file: web/src/pages/DedupRedirect.tsx
// version: 1.0.1
// guid: 5c0d8e41-7a36-4b92-8f1d-3e6a9b2c4d70
// last-edited: 2026-10-15

import { Navigate, useLocation } from 'react-router-dom';

/**
 * Where each legacy `/dedup?tab=` value lands in the Review workspace. A tab
 * that is missing or not listed here (including the retired `unified`) takes
 * the fallback rows below. Values are the query pairs appended after `lane`.
 */
const TAB_TARGETS: Record<string, string> = {
  books: 'lane=dupes&layer=exact',
  'book-duplicates': 'lane=dupes&layer=exact',
  embedding: 'lane=dupes',
  acoustic: 'lane=dupes&layer=acoustid',
  authors: 'lane=authors',
  series: 'lane=authors&view=series',
  ai: 'lane=authors&view=ai',
  reconcile: 'lane=repairs&fixer=reconcile.missing-files',
  'split-books': 'lane=repairs&fixer=dedup.split-books',
};

// `/dedup` with no `tab` at all: the old default tab was the exact-match list.
const DEFAULT_TARGET = 'lane=dupes&layer=exact';
// A `tab` that is present but unknown (the retired `unified`, a typo) has no
// layer of its own, so it lands on the plain Duplicates lane.
const UNKNOWN_TAB_TARGET = 'lane=dupes';
const LABELS_TARGET = 'lane=dupes&view=labels';

/** The only legacy params that still mean something in Review. */
const KEPT_PARAMS = ['book', 'band'] as const;

/**
 * Pure mapping from a legacy dedup URL to its Review URL.
 * `book` and `band` are kept verbatim; every other param is dropped.
 */
function dedupRedirectTarget(pathname: string, search: string): string {
  const params = new URLSearchParams(search);
  let base: string;
  if (pathname.replace(/\/+$/, '').endsWith('/dedup/labels')) {
    base = LABELS_TARGET;
  } else {
    const tab = params.get('tab');
    if (tab === null || tab === '') base = DEFAULT_TARGET;
    else base = TAB_TARGETS[tab] ?? UNKNOWN_TAB_TARGET;
  }
  const out = new URLSearchParams(base);
  for (const key of KEPT_PARAMS) {
    const v = params.get(key);
    if (v) out.set(key, v);
  }
  return `/review?${out.toString()}`;
}

/**
 * One element for /dedup, /dedup/labels, /authors/dedup and /books/dedup. It
 * replaces the history entry so Back does not return to the redirecting URL.
 */
export function DedupRedirect() {
  const { pathname, search } = useLocation();
  return <Navigate replace to={dedupRedirectTarget(pathname, search)} />;
}

export default DedupRedirect;
