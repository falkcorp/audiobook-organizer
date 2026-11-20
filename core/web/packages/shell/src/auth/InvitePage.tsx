// file: core/web/packages/shell/src/auth/InvitePage.tsx
// version: 0.1.0
// guid: 2e2c420b-d00f-4d80-bc4c-0a6e9ecd6d8a
// last-edited: 2026-11-20

import { DataState } from '@mediacore/ui-kit';
import { Alert, Button, Stack, TextField, Typography } from '@mui/material';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router';
import type { InvitePreviewBody } from '../api/interim';
import { errorMessage, isAbortError, unwrap } from '../api/problems';
import { AuthCard } from './AuthCard';
import { useFragmentToken } from './fragmentToken';
import { useAuth, useAuthClient } from './hooks';

type Lookup = { phase: 'loading' } | { phase: 'error'; message: string } | { phase: 'ready'; invite: InvitePreviewBody };

/**
 * /invite#token=...: shows what the invite creates (POST /auth/invites/lookup,
 * which spends nothing) and accepts it with a chosen password. It renders for a
 * signed-in visitor too, so it does not redirect on its own.
 */
export default function InvitePage() {
  const client = useAuthClient();
  const auth = useAuth();
  const navigate = useNavigate();
  const token = useFragmentToken();
  const [lookup, setLookup] = useState<Lookup>({ phase: 'loading' });
  const [attempt, setAttempt] = useState(0);
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const acceptAbort = useRef<AbortController | null>(null);

  useEffect(() => {
    if (!token) return;
    const controller = new AbortController();
    unwrap(client.POST('/api/core/v1/auth/invites/lookup', { body: { token }, signal: controller.signal }))
      .then((invite) => setLookup({ phase: 'ready', invite }))
      .catch((err: unknown) => {
        if (isAbortError(err) || controller.signal.aborted) return;
        setLookup({ phase: 'error', message: errorMessage(err) });
      });
    return () => controller.abort();
  }, [client, token, attempt]);

  useEffect(() => () => acceptAbort.current?.abort(), []);

  if (!token) {
    return (
      <AuthCard title="Accept your invite">
        <DataState status="empty" emptyText="This invite link is missing its token. Ask for a new invite." />
      </AuthCard>
    );
  }

  const mismatch = confirm !== '' && confirm !== password;

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    if (password !== confirm) return;
    acceptAbort.current?.abort();
    const controller = new AbortController();
    acceptAbort.current = controller;
    setBusy(true);
    setError(null);
    try {
      await unwrap(client.POST('/api/core/v1/auth/invites/accept', { body: { token, password }, signal: controller.signal }));
      await auth.refresh();
      void navigate('/', { replace: true });
    } catch (err) {
      if (isAbortError(err)) return;
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  };

  return (
    <AuthCard title="Accept your invite">
      <DataState
        status={lookup.phase === 'ready' ? 'ready' : lookup.phase}
        error={lookup.phase === 'error' ? lookup.message : undefined}
        onRetry={() => {
          setLookup({ phase: 'loading' });
          setAttempt((n) => n + 1);
        }}
        loadingText="Checking your invite…"
      >
        {lookup.phase === 'ready' && (
          <form onSubmit={(e) => void submit(e)}>
            <Stack spacing={2}>
              {error && <Alert severity="error">{error}</Alert>}
              <Typography>
                You are invited as <strong>{lookup.invite.username}</strong> ({lookup.invite.role}).
              </Typography>
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
              <Button type="submit" variant="contained" disabled={busy || password === '' || password !== confirm}>
                {busy ? 'Accepting…' : 'Accept invite'}
              </Button>
            </Stack>
          </form>
        )}
      </DataState>
    </AuthCard>
  );
}
