// file: core/web/packages/shell/src/auth/RequireAuth.tsx
// version: 0.1.0
// guid: 52783deb-7722-41b2-a8e8-e4b6b2fe5ad8
// last-edited: 2026-11-20

import { DataState } from '@mediacore/ui-kit';
import { Alert, Button } from '@mui/material';
import type { ReactNode } from 'react';
import { Navigate, useLocation } from 'react-router';
import { loginPath } from './safeReturn';
import { useAuth } from './hooks';

/**
 * The guard of every route behind sign-in. Loading and a failed probe render
 * their own states (never an endless spinner); an anonymous visitor goes to
 * /login?return=<where they were>, or to /setup while no user exists.
 */
export function RequireAuth({ children }: { children: ReactNode }) {
  const auth = useAuth();
  const location = useLocation();

  switch (auth.status) {
    case 'loading':
      return <DataState status="loading" loadingText="Checking your session…" />;
    case 'error':
      return <DataState status="error" error={auth.error ?? undefined} onRetry={() => void auth.refresh()} />;
    case 'anonymous':
      return <Navigate to={auth.setupRequired ? '/setup' : loginPath(location)} replace />;
    default:
      return (
        <>
          {auth.error && (
            <Alert
              severity="warning"
              action={
                <Button color="inherit" size="small" onClick={() => void auth.refresh()}>
                  Retry
                </Button>
              }
              sx={{ m: 2 }}
            >
              {`Could not reach the server to check your session: ${auth.error}`}
            </Alert>
          )}
          {children}
        </>
      );
  }
}

export default RequireAuth;
