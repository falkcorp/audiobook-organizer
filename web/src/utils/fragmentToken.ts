// file: web/src/utils/fragmentToken.ts
// version: 0.1.0
// guid: 845c0f64-7fb4-466b-871e-d0f1dc2d96e8
// last-edited: 2026-11-22

import { useEffect, useState } from 'react';

/** The `token` of the URL fragment (`#token=...`), or null. */
export function readFragmentToken(): string | null {
  return new URLSearchParams(window.location.hash.replace(/^#/, '')).get('token') || null;
}

/**
 * The one-time token of `/invite#token=` and `/login/temp#token=` (07 section
 * 7.4.3). The secret rides in the fragment, never the path or query, so it is
 * not in an access log or a Referer. It is read once, from `location.hash`, and
 * the fragment is then removed with `history.replaceState`, keeping the history
 * state the router stored, so a reload, a screenshot or a bookmark never carries
 * it. The token lives in component state and goes to the server in a request
 * body only.
 *
 * The read is a state initializer, so it is pure under React's double render;
 * the removal is an effect. A page that sends a request on mount must call this
 * hook before its own effects, so the fragment is gone before the first request.
 */
export function useFragmentToken(): string | null {
  const [token] = useState(readFragmentToken);
  useEffect(() => {
    if (readFragmentToken() === null) return;
    const { pathname, search } = window.location;
    window.history.replaceState(window.history.state, '', pathname + search);
  }, []);
  return token;
}
