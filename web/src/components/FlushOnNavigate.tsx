// file: web/src/components/FlushOnNavigate.tsx
// version: 1.0.1
// guid: 5d2a8e91-6c37-4b04-a1f8-3e7c90b2d645
// last-edited: 2026-11-07

import { useContext, useEffect, useMemo, type ReactNode } from 'react';
import { UNSAFE_NavigationContext } from 'react-router';
import { flushOnNavigate, flushPendingWrites } from '../hooks/useUrlState';

/**
 * Mount directly inside a `BrowserRouter` (or `MemoryRouter`). Pages that mirror
 * their state into the address bar throttle the write; this makes a push or a
 * `navigate(-1)` write whatever is still waiting FIRST, so the history entry the
 * user leaves holds what they were looking at, and also flushes on `pagehide`
 * for a reload or a tab close. See "FLUSHING BEFORE A NAVIGATION" in
 * `hooks/useUrlState.ts`.
 *
 * Provides the router's own navigation context with a wrapped navigator; the
 * hooks under it must be rendered inside, which is what makes them register.
 */
export function FlushOnNavigate({ children }: { children: ReactNode }) {
  const nav = useContext(UNSAFE_NavigationContext);
  const navigator = useMemo(() => flushOnNavigate(nav.navigator), [nav.navigator]);
  const value = useMemo(() => ({ ...nav, navigator }), [nav, navigator]);
  useEffect(() => {
    const onPageHide = () => flushPendingWrites(navigator);
    window.addEventListener('pagehide', onPageHide);
    return () => window.removeEventListener('pagehide', onPageHide);
  }, [navigator]);
  return (
    <UNSAFE_NavigationContext.Provider value={value}>{children}</UNSAFE_NavigationContext.Provider>
  );
}
