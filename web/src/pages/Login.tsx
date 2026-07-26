// file: web/src/pages/Login.tsx
// version: 1.3.0
// guid: 9a3f2c1d-4b5e-6f70-8a9b-0c1d2e3f4a5b

import { useEffect, useMemo, useState, type FormEvent } from 'react';
import { useLocation, useNavigate } from 'react-router-dom';
import {
  Alert,
  Box,
  Button,
  Checkbox,
  CircularProgress,
  Divider,
  FormControlLabel,
  Paper,
  Stack,
  TextField,
  Typography,
} from '@mui/material';
import { useAuth } from '../contexts/AuthContext';

type AuthMode = 'login' | 'setup';

// oauthErrorMessage maps the short ?error= code the OAuth callback redirects with to a
// user-facing message. The most important is not-authorized: a valid Google/GitHub
// login by an email that is not on the allowlist.
function oauthErrorMessage(code: string): string {
  switch (code) {
    case 'oauth_not_authorized':
      return 'That account is not authorized to access this server.';
    case 'oauth_denied':
      return 'Sign-in was cancelled.';
    case 'oauth_state_missing':
    case 'oauth_state_invalid':
      return 'Sign-in session expired — please try again.';
    case 'oauth_exchange_failed':
    case 'oauth_no_code':
      return 'Sign-in failed. Please try again.';
    default:
      return 'Sign-in failed.';
  }
}

const OAUTH_LABELS: Record<string, string> = {
  google: 'Sign in with Google',
  github: 'Sign in with GitHub',
};

export function Login() {
  const auth = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const [mode, setMode] = useState<AuthMode>('login');
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [email, setEmail] = useState('');
  const [rememberMe, setRememberMe] = useState(false);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [providers, setProviders] = useState<string[]>([]);
  const [oauthError, setOauthError] = useState('');

  // Read a failed-OAuth code from the callback redirect and load the enabled providers.
  useEffect(() => {
    const code = new URLSearchParams(location.search).get('error');
    if (code) setOauthError(oauthErrorMessage(code));
    fetch('/api/v1/auth/oauth-providers', { credentials: 'include' })
      .then((r) => (r.ok ? r.json() : null))
      .then((body) => {
        const list = body?.data?.providers ?? body?.providers ?? [];
        if (Array.isArray(list)) setProviders(list.filter((p): p is string => typeof p === 'string'));
      })
      .catch(() => {
        /* providers endpoint absent → no SSO buttons */
      });
  }, [location.search]);

  const redirectTo = useMemo(() => {
    const state = location.state as { from?: string } | null;
    return state?.from || '/dashboard';
  }, [location.state]);

  useEffect(() => {
    if (auth.bootstrapReady) {
      setMode('setup');
    } else {
      setMode('login');
    }
  }, [auth.bootstrapReady]);

  useEffect(() => {
    if (auth.isAuthenticated) {
      navigate(redirectTo, { replace: true });
    }
  }, [auth.isAuthenticated, navigate, redirectTo]);

  const submit = async (event: FormEvent) => {
    event.preventDefault();
    setError('');
    setLoading(true);
    try {
      if (mode === 'setup') {
        await auth.setupAdmin({ username, password, email });
      }
      await auth.login(username, password, rememberMe);
      navigate(redirectTo, { replace: true });
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Authentication failed');
    } finally {
      setLoading(false);
    }
  };

  return (
    <Box
      sx={{
        width: '100%',
        minHeight: '100vh',
        display: 'flex',
        alignItems: 'center',
        justifyContent: 'center',
        p: 3,
      }}
    >
      <Paper
        component="form"
        id="login-form"
        name="login-form"
        method="post"
        action="#"
        onSubmit={submit}
        sx={{ p: 4, maxWidth: 480, width: '100%' }}
      >
        <Stack spacing={2}>
          <Typography variant="h4" gutterBottom>
            {mode === 'setup' ? 'Create Admin Account' : 'Login'}
          </Typography>
          <Typography variant="body2" color="text.secondary">
            {mode === 'setup'
              ? 'First run detected. Create your first admin account.'
              : 'Sign in to access audiobook organizer.'}
          </Typography>

          {error && <Alert severity="error">{error}</Alert>}

          <TextField
            label="Username"
            value={username}
            onChange={(e) => setUsername(e.target.value)}
            name="username"
            id="username"
            type="text"
            autoComplete="username"
            inputProps={{
              autoCapitalize: 'none',
              autoCorrect: 'off',
              spellCheck: false,
            }}
            required
            fullWidth
          />

          {mode === 'setup' && (
            <TextField
              label="Email (optional)"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              name="email"
              id="email"
              type="email"
              autoComplete="email"
              inputProps={{
                autoCapitalize: 'none',
                autoCorrect: 'off',
                spellCheck: false,
              }}
              fullWidth
            />
          )}

          <TextField
            label="Password"
            type="password"
            value={password}
            onChange={(e) => setPassword(e.target.value)}
            name="password"
            id="password"
            autoComplete={
              mode === 'setup' ? 'new-password' : 'current-password'
            }
            helperText={
              mode === 'setup'
                ? 'Use at least 8 characters for the admin password.'
                : undefined
            }
            required
            fullWidth
          />

          {mode === 'login' && (
            <FormControlLabel
              control={
                <Checkbox
                  checked={rememberMe}
                  onChange={(e) => setRememberMe(e.target.checked)}
                  name="remember_me"
                />
              }
              label="Remember me for 1 week"
            />
          )}

          <Button
            type="submit"
            variant="contained"
            size="large"
            disabled={loading}
            fullWidth
          >
            {loading ? (
              <CircularProgress size={20} />
            ) : mode === 'setup' ? (
              'Create And Login'
            ) : (
              'Login'
            )}
          </Button>

          {mode === 'login' && providers.length > 0 && (
            <>
              {oauthError && <Alert severity="error">{oauthError}</Alert>}
              <Divider>or</Divider>
              {providers
                .filter((p) => OAUTH_LABELS[p])
                .map((p) => (
                  <Button
                    key={p}
                    variant="outlined"
                    size="large"
                    fullWidth
                    disabled={loading}
                    onClick={() => {
                      window.location.href = `/api/v1/auth/oauth/${p}/start`;
                    }}
                  >
                    {OAUTH_LABELS[p]}
                  </Button>
                ))}
            </>
          )}

          {auth.bootstrapReady && (
            <Button
              variant="text"
              onClick={() =>
                setMode((current) =>
                  current === 'setup' ? 'login' : 'setup'
                )
              }
              disabled={loading}
            >
              {mode === 'setup'
                ? 'Already have credentials? Login'
                : 'Need to create first admin? Setup'}
            </Button>
          )}
        </Stack>
      </Paper>
    </Box>
  );
}
