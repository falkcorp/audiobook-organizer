// file: core/web/packages/shell/src/auth/AuthProvider.tsx
// version: 0.1.0
// guid: 793aacc6-6d14-4a75-bc3a-0f5e047c9905
// last-edited: 2026-11-20

import { isAuthRedirectError } from '@mediacore/api-client';
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from 'react';
import { onShellUnauthorized, type ShellClient } from '../api/interim';
import { ApiCallError, errorMessage, isAbortError, unwrap } from '../api/problems';
import type { AuthState, AuthUser } from '../contracts';
import type { Permission } from '../types';
import { AuthClientContext, AuthContext } from './context';

export interface AuthProviderProps {
  client: ShellClient;
  children: ReactNode;
  /** Runs when an authenticated session ends: a session-ending 401 on any call, or logout(). */
  onSignedOut?: () => void;
}

type Snapshot = Omit<AuthState, 'refresh' | 'logout'>;

const LOADING: Snapshot = {
  status: 'loading',
  user: null,
  permissions: [],
  method: null,
  ownerVerified: false,
  setupRequired: false,
  authDisabled: false,
  error: null,
};

function anonymous(over: Partial<Snapshot> = {}): Snapshot {
  return { ...LOADING, status: 'anonymous', ...over };
}

/**
 * Probes GET /auth/status, then GET /me (07 §7.9.1). Navigation is not its job:
 * it only reports who the user is, and RequireAuth turns "anonymous" into
 * /login?return= or /setup. That keeps the public pages (/invite, /login/temp,
 * /setup) where they are. A 401 from the anonymous /me probe is "anonymous",
 * not an error, and does not count as a sign-out.
 *
 * A probe that fails (network, 5xx, 503 auth_unavailable) is an `error` state
 * with its message; if the user was already signed in the session is kept and
 * the message sits in `error` for a retry banner, it never logs the user out.
 */
export function AuthProvider({ client, children, onSignedOut }: AuthProviderProps) {
  const [snap, setSnap] = useState<Snapshot>(LOADING);
  const statusRef = useRef(snap.status);
  const abortRef = useRef<AbortController | null>(null);
  const onSignedOutRef = useRef(onSignedOut);

  const commit = useCallback((next: Snapshot) => {
    statusRef.current = next.status;
    setSnap(next);
  }, []);

  useEffect(() => {
    onSignedOutRef.current = onSignedOut;
  }, [onSignedOut]);

  const probe = useCallback(
    async (signal: AbortSignal): Promise<void> => {
      try {
        const status = await unwrap(client.GET('/api/core/v1/auth/status', { signal }));
        if (status.setup_required) {
          commit(anonymous({ setupRequired: true, authDisabled: status.auth_disabled }));
          return;
        }
        if (status.auth_disabled) {
          // No session exists to ask about: the server treats every caller as the
          // auth-disabled principal. There is no user, and no permission to read,
          // so usePermission stays false (advisory; the server decides).
          commit({ ...LOADING, status: 'authenticated', authDisabled: true });
          return;
        }
        try {
          const me = await unwrap(client.GET('/api/core/v1/me', { signal }));
          commit({
            ...LOADING,
            status: 'authenticated',
            user: me.user as unknown as AuthUser,
            permissions: me.permissions as Permission[],
            method: me.method,
            ownerVerified: me.owner_verified,
          });
        } catch (err) {
          if (err instanceof ApiCallError && err.status === 401) {
            commit(anonymous());
            return;
          }
          throw err;
        }
      } catch (err) {
        if (isAbortError(err) || signal.aborted) return;
        if (isAuthRedirectError(err)) {
          // An SSO proxy answered with its login page: a full reload lets it run.
          window.location.reload();
          return;
        }
        const message = errorMessage(err);
        setSnap((prev) => {
          const next: Snapshot = prev.status === 'authenticated' ? { ...prev, error: message } : { ...LOADING, status: 'error', error: message };
          statusRef.current = next.status;
          return next;
        });
      }
    },
    [client, commit],
  );

  const refresh = useCallback((): Promise<void> => {
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    return probe(controller.signal);
  }, [probe]);

  useEffect(() => {
    void refresh();
    return () => abortRef.current?.abort();
  }, [refresh]);

  useEffect(
    () =>
      onShellUnauthorized(client, () => {
        // Only an authenticated session can end; the anonymous probe's own 401 must not.
        if (statusRef.current !== 'authenticated') return;
        commit(anonymous());
        onSignedOutRef.current?.();
      }),
    [client, commit],
  );

  const logout = useCallback(async (): Promise<void> => {
    await unwrap(client.POST('/api/core/v1/auth/logout', {}));
    commit(anonymous());
    onSignedOutRef.current?.();
  }, [client, commit]);

  const value = useMemo<AuthState>(() => ({ ...snap, refresh, logout }), [snap, refresh, logout]);
  return (
    <AuthClientContext value={client}>
      <AuthContext value={value}>{children}</AuthContext>
    </AuthClientContext>
  );
}
