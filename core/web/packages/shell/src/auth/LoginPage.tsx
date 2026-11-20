// file: core/web/packages/shell/src/auth/LoginPage.tsx
// version: 0.1.0
// guid: 2615b290-2719-45a9-b6a0-ebb68fb9dc4a
// last-edited: 2026-11-20

import { DataState } from '@mediacore/ui-kit';
import { Alert, Button, Checkbox, FormControlLabel, Stack, TextField } from '@mui/material';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { Navigate, useSearchParams } from 'react-router';
import { errorMessage, isAbortError, unwrap } from '../api/problems';
import { AuthCard } from './AuthCard';
import { useAuth, useAuthClient } from './hooks';
import { safeReturn } from './safeReturn';

/** /login: username and password. A wrong password is shown here, never a redirect. */
export default function LoginPage() {
  const client = useAuthClient();
  const auth = useAuth();
  const [params] = useSearchParams();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [remember, setRemember] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => () => abortRef.current?.abort(), []);

  if (auth.status === 'loading') return <DataState status="loading" loadingText="Checking your session…" />;
  if (auth.status === 'error') return <DataState status="error" error={auth.error ?? undefined} onRetry={() => void auth.refresh()} />;
  if (auth.setupRequired) return <Navigate to="/setup" replace />;
  // Signed in (also after a successful submit: refresh() flips the status): go on.
  if (auth.status === 'authenticated') return <Navigate to={safeReturn(params.get('return'))} replace />;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    setBusy(true);
    setError(null);
    try {
      await unwrap(client.POST('/api/core/v1/auth/login', { body: { username, password, remember }, signal: controller.signal }));
      await auth.refresh();
    } catch (err) {
      if (isAbortError(err)) return;
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthCard title="Sign in">
      <form onSubmit={(e) => void submit(e)}>
        <Stack spacing={2}>
          {error && <Alert severity="error">{error}</Alert>}
          <TextField label="Username" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" autoFocus required />
          <TextField label="Password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="current-password" required />
          <FormControlLabel control={<Checkbox checked={remember} onChange={(e) => setRemember(e.target.checked)} />} label="Remember me" />
          <Button type="submit" variant="contained" disabled={busy}>
            {busy ? 'Signing in…' : 'Sign in'}
          </Button>
        </Stack>
      </form>
    </AuthCard>
  );
}
