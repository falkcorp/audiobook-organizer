// file: web/src/pages/AcceptInvite.tsx
// version: 1.0.0
// guid: 8e5c1a47-2d93-4b6f-a0e8-3f7b9d2c6a15
// last-edited: 2026-10-19

// Public page an invitee opens from their invitation link: /invite/:token.
// It shows who the invitation is for and when it expires, lets them choose a
// password, and repeats the AudioBooth setup steps with the shared guest
// header pair. The header pair comes from GET /api/v1/auth/invite/:token,
// which only answers for a valid, unexpired, unused token.

import { useEffect, useState, type FormEvent, type ReactNode } from 'react';
import { useParams } from 'react-router-dom';
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  IconButton,
  Paper,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import { ContentCopy as CopyIcon } from '@mui/icons-material';
import { apiFetch } from '../utils/apiFetch';
import { describeRequestError, responseErrorMessage, unwrapData } from '../utils/apiResponse';

interface GuestHeaders {
  client_id: string;
  client_secret: string;
}

interface PublicInvite {
  username: string;
  expires_at: string;
  guest_headers?: GuestHeaders;
}

type LoadState =
  | { kind: 'loading' }
  | { kind: 'invalid' }
  | { kind: 'error'; message: string }
  | { kind: 'ready'; invite: PublicInvite };

const MIN_PASSWORD = 8;

function CopyField({ label, value }: { label: string; value: string }) {
  const [copied, setCopied] = useState(false);
  return (
    <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, my: 0.5 }}>
      <Typography variant="body2" sx={{ minWidth: 48 }}>
        {label}
      </Typography>
      <Typography
        variant="body2"
        component="code"
        sx={{ fontFamily: 'monospace', wordBreak: 'break-all', flex: 1 }}
      >
        {value}
      </Typography>
      <Tooltip title={copied ? 'Copied!' : `Copy ${label.toLowerCase()}`}>
        <IconButton
          size="small"
          aria-label={`Copy ${label.toLowerCase()} ${value.slice(0, 24)}`}
          onClick={() => {
            navigator.clipboard?.writeText(value).catch(() => {});
            setCopied(true);
          }}
        >
          <CopyIcon fontSize="small" />
        </IconButton>
      </Tooltip>
    </Box>
  );
}

export function AudioBoothSteps({
  headers,
  username,
}: {
  headers: GuestHeaders;
  username: string;
}) {
  const origin = typeof window !== 'undefined' ? window.location.origin : '';
  return (
    <Box>
      <Typography variant="h6" sx={{ mt: 2 }}>
        Set up AudioBooth on your phone
      </Typography>
      <Box component="ol" sx={{ pl: 3, '& li': { mb: 1 } }}>
        <li>
          Open AudioBooth and go to <strong>Servers</strong>. Tap <strong>+</strong> to add a
          server.
        </li>
        <li>
          Server address: <code>{origin}</code>
        </li>
        <li>
          <strong>Before signing in</strong>, tap <strong>Custom Headers</strong> and add these two
          headers, exactly as written:
          <Paper variant="outlined" sx={{ p: 1.5, my: 1 }}>
            <CopyField label="Name" value="CF-Access-Client-Id" />
            <CopyField label="Value" value={headers.client_id} />
            <Box sx={{ height: 8 }} />
            <CopyField label="Name" value="CF-Access-Client-Secret" />
            <CopyField label="Value" value={headers.client_secret} />
          </Paper>
        </li>
        <li>
          Go back. The Custom Headers row should read <strong>2</strong>.
        </li>
        <li>
          Sign in with your username (<strong>{username}</strong>) and your password.
        </li>
      </Box>
      <Typography variant="body2" color="text.secondary">
        These two header values are shared guest credentials for reaching the server. They do not
        sign you in; your own password does. Please do not share them.
      </Typography>
    </Box>
  );
}

export function AcceptInvite() {
  const { token = '' } = useParams<{ token: string }>();
  const [state, setState] = useState<LoadState>({ kind: 'loading' });
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [formError, setFormError] = useState('');
  const [accepted, setAccepted] = useState(false);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const resp = await apiFetch(`/api/v1/auth/invite/${encodeURIComponent(token)}`);
        if (cancelled) return;
        if (resp.status === 404) {
          setState({ kind: 'invalid' });
          return;
        }
        if (!resp.ok) {
          setState({ kind: 'error', message: await responseErrorMessage(resp) });
          return;
        }
        const invite = unwrapData<PublicInvite>(await resp.json());
        setState({ kind: 'ready', invite });
      } catch (err) {
        if (!cancelled)
          setState({
            kind: 'error',
            message: describeRequestError(err, 'Could not load the invitation'),
          });
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [token]);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setFormError('');
    if (password.length < MIN_PASSWORD) {
      setFormError(`Password must be at least ${MIN_PASSWORD} characters.`);
      return;
    }
    if (password !== confirm) {
      setFormError('The two passwords do not match.');
      return;
    }
    setSubmitting(true);
    try {
      const resp = await apiFetch('/api/v1/auth/accept-invite', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token, password }),
      });
      if (!resp.ok) {
        setFormError(await responseErrorMessage(resp));
        return;
      }
      setAccepted(true);
    } catch (err) {
      setFormError(describeRequestError(err, 'Could not accept the invitation'));
    } finally {
      setSubmitting(false);
    }
  };

  let body: ReactNode;
  if (state.kind === 'loading') {
    body = <CircularProgress aria-label="Loading invitation" />;
  } else if (state.kind === 'invalid') {
    body = (
      <Alert severity="warning">
        This invitation link is invalid, has already been used, or has expired. Ask the person who
        invited you for a new one.
      </Alert>
    );
  } else if (state.kind === 'error') {
    body = <Alert severity="error">{state.message}</Alert>;
  } else {
    const { invite } = state;
    body = (
      <Stack spacing={2}>
        {accepted ? (
          <>
            <Alert severity="success">
              Your account <strong>{invite.username}</strong> is ready.
            </Alert>
            <Button variant="contained" onClick={() => window.location.assign('/')}>
              Continue to the library
            </Button>
          </>
        ) : (
          <>
            <Typography>
              This invitation is for <strong>{invite.username}</strong> and expires{' '}
              {new Date(invite.expires_at).toLocaleString()}.
            </Typography>
            <Box component="form" onSubmit={handleSubmit} noValidate>
              <TextField
                fullWidth
                type="password"
                label="Choose a password"
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
              {formError && (
                <Alert severity="error" sx={{ mt: 1 }}>
                  {formError}
                </Alert>
              )}
              <Button
                type="submit"
                variant="contained"
                fullWidth
                sx={{ mt: 2 }}
                disabled={submitting}
              >
                {submitting ? 'Creating account…' : 'Create my account'}
              </Button>
            </Box>
          </>
        )}
        {invite.guest_headers && (
          <AudioBoothSteps headers={invite.guest_headers} username={invite.username} />
        )}
      </Stack>
    );
  }

  return (
    <Box
      sx={{
        minHeight: '100vh',
        width: '100%',
        display: 'flex',
        justifyContent: 'center',
        alignItems: 'flex-start',
        p: 2,
        overflowY: 'auto',
      }}
    >
      <Paper sx={{ p: { xs: 2, sm: 4 }, maxWidth: 640, width: '100%', mt: { xs: 2, sm: 6 } }}>
        <Typography variant="h5" gutterBottom>
          You're invited to Audiobook Organizer
        </Typography>
        {body}
      </Paper>
    </Box>
  );
}

export default AcceptInvite;
