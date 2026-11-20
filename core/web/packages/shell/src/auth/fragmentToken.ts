// file: core/web/packages/shell/src/auth/fragmentToken.ts
// version: 0.1.0
// guid: 045366e7-665f-4162-bd0e-9eb44e32cca2
// last-edited: 2026-11-20

import { useEffect, useState } from 'react';

/** The `token` of the URL fragment (`#token=...`), or null. */
export function readFragmentToken(): string | null {
  return new URLSearchParams(window.location.hash.replace(/^#/, '')).get('token') || null;
}

/**
 * The one-time token of `/invite#token=` and `/login/temp#token=` (07 §7.4.3).
 * It is read once, from `location.hash`, and the fragment is then removed with
 * `history.replaceState`, keeping the history state the router stored, so a
 * reload, a screenshot, a Referer or a bookmark never carries it. The token
 * lives in component state and goes to the server in a request body only.
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
