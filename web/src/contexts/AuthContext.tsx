// file: web/src/contexts/AuthContext.tsx
// version: 1.1.2
// guid: 2b3c4d5e-6f70-4819-a2b3-c4d5e6f70819

import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useRef,
  useState,
  type ReactNode,
} from 'react';
import * as api from '../services/api';
import { ApiError, type AuthUser } from '../services/api';

interface AuthContextValue {
  initialized: boolean;
  loading: boolean;
  user: AuthUser | null;
  requiresAuth: boolean;
  bootstrapReady: boolean;
  isAuthenticated: boolean;
  refresh: () => Promise<void>;
  login: (username: string, password: string, rememberMe?: boolean) => Promise<void>;
  logout: () => Promise<void>;
  setupAdmin: (payload: { username: string; password: string; email?: string }) => Promise<void>;
}

const AuthContext = createContext<AuthContextValue | null>(null);

export function AuthProvider({ children }: { children: ReactNode }) {
  const [initialized, setInitialized] = useState(false);
  const [loading, setLoading] = useState(false);
  const [user, setUser] = useState<AuthUser | null>(null);
  const [requiresAuth, setRequiresAuth] = useState(false);
  const [bootstrapReady, setBootstrapReady] = useState(false);
  const isMountedRef = useRef(true);

  useEffect(() => {
    return () => {
      isMountedRef.current = false;
    };
  }, []);

  const refresh = useCallback(async () => {
    setLoading(true);

    const MAX_RETRIES = 3;
    const RETRY_DELAY_MS = 1_000;

    let lastError: unknown;
    for (let attempt = 0; attempt <= MAX_RETRIES; attempt++) {
      try {
        if (attempt > 0) {
          await new Promise((r) => setTimeout(r, RETRY_DELAY_MS));
        }
        if (!isMountedRef.current) return;
        const status = await api.getAuthStatus();
        if (!isMountedRef.current) return;
        setRequiresAuth(status.requires_auth);
        setBootstrapReady(status.bootstrap_ready);

        if (!status.requires_auth) {
          if (!isMountedRef.current) return;
          setUser(null);
          setInitialized(true);
          setLoading(false);
          return;
        }

        const me = await api.getMe();
        if (!isMountedRef.current) return;
        setUser(me);
        setInitialized(true);
        setLoading(false);
        return;
      } catch (error) {
        // 401 = definitively unauthenticated; don't retry.
        if (error instanceof ApiError && error.status === 401) {
          if (!isMountedRef.current) return;
          setUser(null);
          setInitialized(true);
          setLoading(false);
          return;
        }
        // Transient error (503, network failure during startup); retry.
        lastError = error;
      }
    }

    // All retries exhausted — server is unreachable; preserve existing user
    // state rather than flashing the login screen for a healthy session.
    if (!isMountedRef.current) return;
    console.warn('Auth check failed after retries:', lastError);
    setInitialized(true);
    setLoading(false);
  }, []);

  useEffect(() => {
    void refresh();
  }, [refresh]);

  const login = useCallback(async (username: string, password: string, rememberMe?: boolean) => {
    const response = await api.login({ username, password, remember_me: rememberMe });
    setUser(response.user);
    setRequiresAuth(true);
    setBootstrapReady(false);
  }, []);

  const logout = useCallback(async () => {
    try {
      await api.logout();
    } finally {
      setUser(null);
    }
  }, []);

  const setupAdmin = useCallback(
    async (payload: { username: string; password: string; email?: string }) => {
      await api.setupAdmin(payload);
      setBootstrapReady(false);
      setRequiresAuth(true);
    },
    []
  );

  const value = useMemo<AuthContextValue>(
    () => ({
      initialized,
      loading,
      user,
      requiresAuth,
      bootstrapReady,
      isAuthenticated: Boolean(user),
      refresh,
      login,
      logout,
      setupAdmin,
    }),
    [initialized, loading, user, requiresAuth, bootstrapReady, refresh, login, logout, setupAdmin]
  );

  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

// eslint-disable-next-line react-refresh/only-export-components
export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) {
    throw new Error('useAuth must be used within AuthProvider');
  }
  return ctx;
}
