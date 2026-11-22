// file: web/src/pages/AcceptInvite.tsx
// version: 1.3.2
// guid: 8e5c1a47-2d93-4b6f-a0e8-3f7b9d2c6a15
// last-edited: 2026-11-07

// Public page an invitee opens from their invitation link: /invite#token=... (core's link form,
// F4-08 B8). The token is read from the URL fragment once and the fragment is removed at once
// (useFragmentToken); it goes to core in a request body only (POST /auth/invites/lookup, then
// /auth/invites/accept), never in a URL. The page shows who the invitation is for and when it
// expires, lets the invitee choose a password, and accepting opens their session.

import { useEffect, useState, type FormEvent, type ReactNode } from 'react';
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  Paper,
  Stack,
  TextField,
  Typography,
} from '@mui/material';
import {
  acceptInvite,
  lookupInvite,
  type InvitePreview,
  type PhoneAppKey,
} from '../services/coreIdentity';
import { ApiError } from '../services/apiError';
import { useFragmentToken } from '../utils/fragmentToken';
import { describeRequestError } from '../utils/apiResponse';

/** The server's own reason for a refusal; the generic text for a transport failure. */
const reasonOf = (err: unknown, fallback: string) =>
  err instanceof ApiError && err.message ? err.message : describeRequestError(err, fallback);

type LoadState =
  | { kind: 'loading' }
  | { kind: 'invalid' }
  | { kind: 'error'; message: string }
  | { kind: 'ready'; invite: InvitePreview };

const MIN_PASSWORD = 8;

export function PhoneAppKeyPanel({ phoneApp }: { phoneApp: PhoneAppKey }) {
  if (phoneApp.status !== 'issued') {
    return (
      <Alert severity="warning">
        {phoneApp.message ||
          'Your phone-app key could not be created. Ask the person who invited you to look into it.'}
      </Alert>
    );
  }
  const idHeader = phoneApp.id_header || 'CF-Access-Client-Id';
  const secretHeader = phoneApp.secret_header || 'CF-Access-Client-Secret';
  return (
    <Box>
      <Alert severity="warning">
        This is your own phone-app key. It is shown <strong>only once</strong>: copy it now. If you
        lose it, ask the person who invited you to issue a new one.
      </Alert>
      <Typography variant="h6" sx={{ mt: 2 }}>
        Phone app
      </Typography>
      <Box component="ol" sx={{ pl: 3, '& li': { mb: 1 } }}>
        <li>
          In your Audiobookshelf-compatible app, add this server and open its custom headers
          setting.
        </li>
        <li>
          Add the header <code>{idHeader}</code> with the value
          <br />
          <code data-testid="phone-app-client-id">{phoneApp.client_id}</code>
        </li>
        <li>
          Add the header <code>{secretHeader}</code> with the value
          <br />
          <code data-testid="phone-app-client-secret">{phoneApp.client_secret}</code>
        </li>
        <li>
          Sign in with your username and the password you just chose. Do not share these values:
          they are for you alone and can be revoked at any time.
        </li>
      </Box>
    </Box>
  );
}

export function SignInSteps({
  username,
  emailLogin,
  phoneAppToken = false,
  showPhoneNote = true,
}: {
  username: string;
  emailLogin: boolean;
  phoneAppToken?: boolean;
  // false once the page is already showing the phone-app key (or why there is none).
  showPhoneNote?: boolean;
}) {
  const origin = typeof window !== 'undefined' ? window.location.origin : '';
  return (
    <Box>
      <Typography variant="h6" sx={{ mt: 2 }}>
        Signing in
      </Typography>
      <Box component="ol" sx={{ pl: 3, '& li': { mb: 1 } }}>
        <li>
          Open <code>{origin}</code> in your browser.
        </li>
        {emailLogin && (
          <li>
            If Cloudflare asks you to sign in, enter the email address this invitation was sent to.
            Cloudflare emails you a one-time code; type it in. This works once you have created your
            account below.
          </li>
        )}
        <li>
          Sign in with your username (<strong>{username}</strong>) and your password.
        </li>
      </Box>
      {showPhoneNote && (
        <Typography variant="body2" color="text.secondary">
          {phoneAppToken
            ? 'After you create your account, this page shows your own phone-app key once. Keep the page open until you have copied it.'
            : 'Phone app access will be added to your account soon.'}
        </Typography>
      )}
    </Box>
  );
}

/** core answers a missing, spent or expired invitation with one of these codes. */
function isLinkInvalid(err: unknown): boolean {
  if (!(err instanceof ApiError)) return false;
  const code = (err.data as { code?: string } | undefined)?.code;
  return code === 'invite_invalid' || code === 'link_invalid' || err.status === 404;
}

export function AcceptInvite() {
  // Declared before the effect below, so the fragment is removed before the lookup request.
  const token = useFragmentToken();
  const [state, setState] = useState<LoadState>(token ? { kind: 'loading' } : { kind: 'invalid' });
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [formError, setFormError] = useState('');
  const [accepted, setAccepted] = useState(false);
  const [phoneApp, setPhoneApp] = useState<PhoneAppKey | null>(null);

  useEffect(() => {
    if (!token) return;
    let cancelled = false;
    (async () => {
      try {
        const invite = await lookupInvite(token);
        if (!cancelled) setState({ kind: 'ready', invite });
      } catch (err) {
        if (cancelled) return;
        setState(
          isLinkInvalid(err)
            ? { kind: 'invalid' }
            : {
                kind: 'error',
                message: reasonOf(err, 'Could not load the invitation'),
              }
        );
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [token]);

  const handleSubmit = async (e: FormEvent) => {
    e.preventDefault();
    setFormError('');
    if (!token) return;
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
      const done = await acceptInvite(token, password);
      // The one-time phone-app key, when the server hands one over (B10s feeds it from a books
      // claim endpoint after core's accept; core's own answer carries none).
      if (done.phone_app) {
        setPhoneApp(done.phone_app);
        setPassword('');
        setConfirm('');
      }
      setAccepted(true);
    } catch (err) {
      setFormError(
        isLinkInvalid(err)
          ? 'This invitation is no longer valid. Ask the person who invited you for a new one.'
          : reasonOf(err, 'Could not accept the invitation')
      );
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
    body = accepted ? (
      <Stack spacing={2}>
        <Alert severity="success">
          Your account <strong>{invite.username}</strong> is ready and you are signed in.
        </Alert>
        {phoneApp && <PhoneAppKeyPanel phoneApp={phoneApp} />}
        <SignInSteps
          username={invite.username}
          emailLogin={!!invite.email_login}
          phoneAppToken={!!invite.phone_app_token}
          showPhoneNote={!phoneApp}
        />
        <Button variant="contained" onClick={() => window.location.assign('/')}>
          Continue to the library
        </Button>
      </Stack>
    ) : (
      <Stack spacing={2}>
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
          <Button type="submit" variant="contained" fullWidth sx={{ mt: 2 }} disabled={submitting}>
            {submitting ? 'Creating account…' : 'Create my account'}
          </Button>
        </Box>
        <SignInSteps
          username={invite.username}
          emailLogin={!!invite.email_login}
          phoneAppToken={!!invite.phone_app_token}
        />
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
