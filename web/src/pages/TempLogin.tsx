// file: web/src/pages/TempLogin.tsx
// version: 0.1.0
// guid: 90840e19-587e-41b8-992c-4914ddf6b98a
// last-edited: 2026-11-22

// Public page behind an admin's temp-login or password-reset link: /login/temp#token=... (core's
// link form, F4-08 B8). The token is read from the URL fragment once and the fragment is removed
// at once (useFragmentToken); it goes to core in one request body (POST /auth/temp-login/consume),
// never in a URL. A plain temp-login link signs the user in on arrival. A reset link also needs
// the new password: core answers the first, password-less call with a 400 and does not spend the
// token, so the page then asks for a password and sends the same token again.

import { useEffect, useRef, useState, type FormEvent } from 'react';
import { Alert, Box, Button, CircularProgress, Paper, TextField, Typography } from '@mui/material';
import { consumeTempLogin } from '../services/coreIdentity';
import { ApiError } from '../services/apiError';
import { useFragmentToken } from '../utils/fragmentToken';
import { describeRequestError } from '../utils/apiResponse';

/** The server's own reason for a refusal; the generic text for a transport failure. */
const reasonOf = (err: unknown, fallback: string) =>
  err instanceof ApiError && err.message ? err.message : describeRequestError(err, fallback);

const MIN_PASSWORD = 8;

type Phase = 'signing-in' | 'needs-password' | 'invalid' | 'error';

/** Leaves for the app with a full load, so the new session cookie is what every request carries. */
const goHome = () => window.location.replace('/');

export function TempLogin({ onSignedIn = goHome }: { onSignedIn?: () => void } = {}) {
  // Declared before the effect below, so the fragment is removed before the consume request.
  const token = useFragmentToken();
  const [phase, setPhase] = useState<Phase>(token ? 'signing-in' : 'invalid');
  const [message, setMessage] = useState('');
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [submitting, setSubmitting] = useState(false);
  // The token is single-use: React's dev double-mount must not spend it twice.
  const started = useRef(false);

  const consume = async (newPassword?: string) => {
    if (!token) return;
    try {
      await consumeTempLogin(token, newPassword);
      onSignedIn();
    } catch (err) {
      if (err instanceof ApiError && err.status === 401) {
        setPhase('invalid');
      } else if (err instanceof ApiError && err.status === 400 && newPassword === undefined) {
        setPhase('needs-password');
      } else {
        setMessage(reasonOf(err, 'Could not sign in with this link'));
        setPhase(newPassword === undefined ? 'error' : 'needs-password');
      }
    }
  };

  useEffect(() => {
    if (!token || started.current) return;
    started.current = true;
    void consume();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [token]);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setMessage('');
    if (password.length < MIN_PASSWORD) {
      setMessage(`Password must be at least ${MIN_PASSWORD} characters.`);
      return;
    }
    if (password !== confirm) {
      setMessage('The two passwords do not match.');
      return;
    }
    setSubmitting(true);
    await consume(password);
    setSubmitting(false);
  };

  let body;
  if (phase === 'signing-in') {
    body = <CircularProgress aria-label="Signing in" />;
  } else if (phase === 'invalid') {
    body = (
      <Alert severity="warning">
        This sign-in link is invalid, has already been used, or has expired. Ask an administrator
        for a new one.
      </Alert>
    );
  } else if (phase === 'error') {
    body = <Alert severity="error">{message}</Alert>;
  } else {
    body = (
      <Box component="form" onSubmit={handleSubmit} noValidate>
        <Typography>Choose a new password to finish signing in.</Typography>
        <TextField
          fullWidth
          type="password"
          label="New password"
          autoComplete="new-password"
          value={password}
          onChange={(e) => setPassword(e.target.value)}
          margin="normal"
          required
          helperText={`At least ${MIN_PASSWORD} characters`}
        />
        <TextField
          fullWidth
          type="password"
          label="Confirm password"
          autoComplete="new-password"
          value={confirm}
          onChange={(e) => setConfirm(e.target.value)}
          margin="normal"
          required
        />
        {message && (
          <Alert severity="error" sx={{ mt: 1 }}>
            {message}
          </Alert>
        )}
        <Button type="submit" variant="contained" fullWidth sx={{ mt: 2 }} disabled={submitting}>
          {submitting ? 'Saving…' : 'Set password and sign in'}
        </Button>
      </Box>
    );
  }

  return (
    <Box sx={{ minHeight: '100vh', display: 'flex', justifyContent: 'center', p: 2 }}>
      <Paper sx={{ p: { xs: 2, sm: 4 }, maxWidth: 480, width: '100%', mt: { xs: 2, sm: 6 }, alignSelf: 'flex-start' }}>
        <Typography variant="h5" gutterBottom>
          Sign in
        </Typography>
        {body}
      </Paper>
    </Box>
  );
}

export default TempLogin;
