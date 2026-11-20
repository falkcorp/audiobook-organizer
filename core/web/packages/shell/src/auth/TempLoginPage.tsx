// file: core/web/packages/shell/src/auth/TempLoginPage.tsx
// version: 0.1.0
// guid: 39227909-e671-45f3-8a08-50c663e57cd9
// last-edited: 2026-11-20

import { Alert, Button, Stack, TextField, Typography } from '@mui/material';
import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useNavigate } from 'react-router';
import { errorMessage, isAbortError, unwrap } from '../api/problems';
import { AuthCard } from './AuthCard';
import { useFragmentToken } from './fragmentToken';
import { useAuth, useAuthClient } from './hooks';

/**
 * /login/temp#token=...: spends a temp-login or reset link. A reset link needs a
 * new password and a plain temp-login link refuses one (core's ConsumeTempLogin),
 * and a link that is refused for that reason stays unspent, so the password field
 * is optional and sent only when filled in; nothing is spent on page load.
 */
export default function TempLoginPage() {
  const client = useAuthClient();
  const auth = useAuth();
  const navigate = useNavigate();
  const token = useFragmentToken();
  const [newPassword, setNewPassword] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);

  useEffect(() => () => abortRef.current?.abort(), []);

  if (!token) {
    return (
      <AuthCard title="Temporary sign-in">
        <Alert severity="warning">This link is missing its token. Ask for a new link.</Alert>
      </AuthCard>
    );
  }

  const submit = async (e: FormEvent) => {
    e.preventDefault();
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    setBusy(true);
    setError(null);
    try {
      await unwrap(
        client.POST('/api/core/v1/auth/temp-login/consume', {
          body: newPassword ? { token, new_password: newPassword } : { token },
          signal: controller.signal,
        }),
      );
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
    <AuthCard title="Temporary sign-in">
      <form onSubmit={(e) => void submit(e)}>
        <Stack spacing={2}>
          {error && <Alert severity="error">{error}</Alert>}
          <Typography color="text.secondary">
            Continue to sign in with this link. If it is a password reset link, choose a new password first.
          </Typography>
          <TextField
            label="New password (reset links only)"
            type="password"
            value={newPassword}
            onChange={(e) => setNewPassword(e.target.value)}
            autoComplete="new-password"
          />
          <Button type="submit" variant="contained" disabled={busy}>
            {busy ? 'Signing in…' : 'Continue'}
          </Button>
        </Stack>
      </form>
    </AuthCard>
  );
}
