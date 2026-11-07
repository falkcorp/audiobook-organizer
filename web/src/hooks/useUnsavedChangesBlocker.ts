// file: web/src/hooks/useUnsavedChangesBlocker.ts
// version: 1.2.1
// guid: e3f4a5b6-c7d8-9e0f-1a2b-3c4d5e6f7a8b
// last-edited: 2026-11-07

import { useEffect, useCallback, useRef, useState } from 'react';
import { useNavigate, useLocation } from 'react-router';

/** Whether `url` (as passed to history.pushState/replaceState) is on `pathname`. */
function isSamePath(url: string, pathname: string): boolean {
  try {
    return new URL(url, window.location.href).pathname === pathname;
  } catch {
    return false;
  }
}

interface Blocker {
  state: 'idle' | 'blocked';
  proceed: (() => void) | null;
  reset: (() => void) | null;
}

/**
 * Custom hook that replaces react-router's useBlocker without requiring a data router.
 * Warns before browser unload and intercepts in-app navigation when there are unsaved changes.
 */
export function useUnsavedChangesBlocker(shouldBlock: boolean): Blocker {
  const [blocked, setBlocked] = useState(false);
  const [pendingPath, setPendingPath] = useState<string | null>(null);
  const proceedingRef = useRef(false);
  const proceedingTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const navigate = useNavigate();
  const location = useLocation();

  // Handle browser beforeunload
  useEffect(() => {
    if (!shouldBlock) return;
    const handler = (e: BeforeUnloadEvent) => {
      e.preventDefault();
    };
    window.addEventListener('beforeunload', handler);
    return () => window.removeEventListener('beforeunload', handler);
  }, [shouldBlock]);

  // Override navigate to intercept in-app navigation
  // We do this by patching pushState/replaceState
  useEffect(() => {
    if (!shouldBlock || proceedingRef.current) {
      setBlocked(false);
      setPendingPath(null);
      return;
    }

    const originalPushState = history.pushState.bind(history);
    const originalReplaceState = history.replaceState.bind(history);

    const intercept = (
      original: typeof history.pushState,
      data: unknown,
      unused: string,
      url?: string | URL | null
    ) => {
      // Leaving the PAGE is what the unsaved-changes warning is for. A write to
      // the same path (a query string or hash change: the page's own tab or
      // filter written to the address bar) is not leaving, and blocking it made
      // every deep-link write on a dirty page raise the "leave this page?" dialog.
      if (url && typeof url === 'string' && !isSamePath(url, location.pathname)) {
        setPendingPath(url);
        setBlocked(true);
        return; // Block the navigation
      }
      original(data, unused, url);
    };

    history.pushState = function (data, unused, url) {
      intercept(originalPushState, data, unused, url);
    };
    history.replaceState = function (data, unused, url) {
      intercept(originalReplaceState, data, unused, url);
    };

    return () => {
      history.pushState = originalPushState;
      history.replaceState = originalReplaceState;
    };
  }, [shouldBlock, location.pathname]);

  const proceed = useCallback(() => {
    if (pendingPath) {
      const path = pendingPath;
      // Mark as proceeding so the effect doesn't re-intercept
      proceedingRef.current = true;
      setBlocked(false);
      setPendingPath(null);
      // Navigate after state cleanup
      if (proceedingTimerRef.current) clearTimeout(proceedingTimerRef.current);
      proceedingTimerRef.current = setTimeout(() => {
        navigate(path);
        proceedingRef.current = false;
      }, 0);
    }
  }, [pendingPath, navigate]);

  // Cleanup setTimeout on unmount
  useEffect(() => {
    return () => {
      if (proceedingTimerRef.current) {
        clearTimeout(proceedingTimerRef.current);
      }
    };
  }, []);

  const reset = useCallback(() => {
    setBlocked(false);
    setPendingPath(null);
  }, []);

  return {
    state: blocked ? 'blocked' : 'idle',
    proceed: blocked ? proceed : null,
    reset: blocked ? reset : null,
  };
}
