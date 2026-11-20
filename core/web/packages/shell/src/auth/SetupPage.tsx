// file: core/web/packages/shell/src/auth/SetupPage.tsx
// version: 0.1.0
// guid: bb1c119e-ab47-40c1-88e3-601bb77918c6
// last-edited: 2026-11-20

import { DataState } from '@mediacore/ui-kit';
import { Alert, Button, Stack, TextField, Typography } from '@mui/material';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { Link as RouterLink, Navigate } from 'react-router';
import { ApiCallError, errorMessage, isAbortError, unwrap } from '../api/problems';
import { AuthCard } from './AuthCard';
import { useAuth, useAuthClient } from './hooks';

/**
 * /setup: first-run creation of the admin account while no user exists. Step 1
 * takes the bootstrap token the server printed (core.auth.setup_requires_token
 * may waive it for a local caller, so it can stay empty and the server decides);
 * step 2 the account. Setup opens no session, so the page then signs in with the
 * same credentials. The token is checked and spent by POST /auth/setup itself, so
 * a wrong one comes back as `bootstrap_invalid` and returns to step 1.
 */
export default function SetupPage() {
  const client = useAuthClient();
  const auth = useAuth();
  const [step, setStep] = useState<'token' | 'account'>('token');
  const [token, setToken] = useState('');
  const [username, setUsername] = useState('');
  const [email, setEmail] = useState('');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [created, setCreated] = useState(false);
  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => () => abortRef.current?.abort(), []);

  if (auth.status === 'loading') return <DataState status="loading" loadingText="Checking setup…" />;
  if (auth.status === 'error') return <DataState status="error" error={auth.error ?? undefined} onRetry={() => void auth.refresh()} />;
  if (auth.status === 'authenticated') return <Navigate to="/" replace />;
  // Setup is over (a user exists), unless this page itself just finished it and is about to sign in.
  if (!auth.setupRequired && !created) return <Navigate to="/login" replace />;

  const mismatch = confirm !== '' && confirm !== password;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (password !== confirm) return;
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    const { signal } = controller;
    setBusy(true);
    setError(null);
    try {
      await unwrap(
        client.POST('/api/core/v1/auth/setup', {
          body: { username, password, ...(email ? { email } : {}), ...(token ? { bootstrap_token: token } : {}) },
          signal,
        }),
      );
      setCreated(true);
    } catch (err) {
      if (isAbortError(err)) return;
      if (err instanceof ApiCallError && err.problem.code === 'bootstrap_invalid') setStep('token');
      setError(errorMessage(err));
      setBusy(false);
      return;
    }
    try {
      await unwrap(client.POST('/api/core/v1/auth/login', { body: { username, password }, signal }));
      await auth.refresh();
    } catch (err) {
      if (isAbortError(err)) return;
      setError(`The account was created, but signing in failed: ${errorMessage(err)}`);
    } finally {
      setBusy(false);
    }
  };

  if (created && error) {
    return (
      <AuthCard title="Set up">
        <Stack spacing={2}>
          <Alert severity="warning">{error}</Alert>
          <Button component={RouterLink} to="/login" variant="contained">
            Go to sign in
          </Button>
        </Stack>
      </AuthCard>
    );
  }

  if (step === 'token') {
    return (
      <AuthCard title="Set up">
        <Stack spacing={2}>
          {error && <Alert severity="error">{error}</Alert>}
          <Typography color="text.secondary">No account exists yet. Enter the bootstrap token the server printed to create the first administrator.</Typography>
          <TextField label="Bootstrap token" value={token} onChange={(e) => setToken(e.target.value)} autoComplete="off" autoFocus />
          <Button
            variant="contained"
            onClick={() => {
              setError(null);
              setStep('account');
            }}
          >
            Continue
          </Button>
        </Stack>
      </AuthCard>
    );
  }

  return (
    <AuthCard title="Create the administrator">
      <form onSubmit={(e) => void submit(e)}>
        <Stack spacing={2}>
          {error && <Alert severity="error">{error}</Alert>}
          <TextField label="Username" value={username} onChange={(e) => setUsername(e.target.value)} autoComplete="username" autoFocus required />
          <TextField label="Email (optional)" type="email" value={email} onChange={(e) => setEmail(e.target.value)} autoComplete="email" />
          <TextField label="Password" type="password" value={password} onChange={(e) => setPassword(e.target.value)} autoComplete="new-password" required />
          <TextField
            label="Confirm password"
            type="password"
            value={confirm}
            onChange={(e) => setConfirm(e.target.value)}
            autoComplete="new-password"
            error={mismatch}
            helperText={mismatch ? 'The passwords do not match.' : undefined}
            required
          />
          <Stack direction="row" spacing={1}>
            <Button onClick={() => setStep('token')} disabled={busy}>
              Back
            </Button>
            <Button type="submit" variant="contained" disabled={busy || username === '' || password === '' || password !== confirm}>
              {busy ? 'Creating…' : 'Create account'}
            </Button>
          </Stack>
        </Stack>
      </form>
    </AuthCard>
  );
}
