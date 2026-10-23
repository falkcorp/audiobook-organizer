// file: web/src/pages/AcceptInvite.tsx
// version: 1.3.0
// guid: 8e5c1a47-2d93-4b6f-a0e8-3f7b9d2c6a15
// last-edited: 2026-10-23

// Public page an invitee opens from their invitation link: /invite/:token.
// It shows who the invitation is for and when it expires, lets them choose a
// password, and says how to sign in from a browser. A guest gets through
// Cloudflare by email login when accepting adds their address to the guest
// Access policy (the lookup's email_login), so there is no shared header or
// token to copy. When per-guest phone-app keys are on (the lookup's
// phone_app_token), accepting also returns that guest's OWN Cloudflare
// service token once (phone_app in the accept response); this page is the only
// place the secret is ever shown, and it is kept in component state only. The
// invitation comes from GET /api/v1/auth/invite/:token, which only answers for
// a valid, unexpired, unused token.

import { useEffect, useState, type FormEvent, type ReactNode } from 'react';
import { useParams } from 'react-router-dom';
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
import { apiFetch } from '../utils/apiFetch';
import { describeRequestError, responseErrorMessage, unwrapData } from '../utils/apiResponse';

interface PublicInvite {
  username: string;
  expires_at: string;
  // email_login: accepting adds the invited address to the Cloudflare guest
  // policy, so the guest signs in to Cloudflare with that address first.
  email_login?: boolean;
  // phone_app_token: accepting will mint this guest's own phone-app key.
  phone_app_token?: boolean;
}

// The accept response's phone_app object. client_secret is shown once and
// never stored by this page beyond component state.
interface PhoneAppKey {
  status: 'issued' | 'unavailable';
  client_id?: string;
  client_secret?: string;
  id_header?: string;
  secret_header?: string;
  message?: string;
}

type LoadState =
  | { kind: 'loading' }
  | { kind: 'invalid' }
  | { kind: 'error'; message: string }
  | { kind: 'ready'; invite: PublicInvite };

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

export function AcceptInvite() {
  const { token = '' } = useParams<{ token: string }>();
  const [state, setState] = useState<LoadState>({ kind: 'loading' });
  const [password, setPassword] = useState('');
  const [confirm, setConfirm] = useState('');
  const [submitting, setSubmitting] = useState(false);
  const [formError, setFormError] = useState('');
  const [accepted, setAccepted] = useState(false);
  const [phoneApp, setPhoneApp] = useState<PhoneAppKey | null>(null);

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
      // Read the one-time key before anything else can drop the response. A
      // body that does not parse must not turn a created account into an error.
      try {
        const data = unwrapData<{ phone_app?: PhoneAppKey }>(await resp.json());
        if (data?.phone_app) setPhoneApp(data.phone_app);
      } catch {
        /* no phone-app key in the response */
      }
      setPassword('');
      setConfirm('');
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
            {phoneApp && <PhoneAppKeyPanel phoneApp={phoneApp} />}
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
        <SignInSteps
          username={invite.username}
          emailLogin={!!invite.email_login}
          phoneAppToken={!!invite.phone_app_token}
          showPhoneNote={!(accepted && phoneApp)}
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
