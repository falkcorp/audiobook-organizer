// file: web/src/components/users/UsersPage.tsx
// version: 0.1.0
// guid: 56ff03cc-6476-4d12-8d77-fff90254cd30
// last-edited: 2026-11-21

// F9-04 slice 4: the shell half of the Users page (07 §7.11): the accounts table, invitations and
// the sign-ins drawer, with slots for the modules' row actions and panels. It imports no books code.

import { useCallback, useEffect, useState } from 'react';
import { useSearchParams } from 'react-router';
import { apiFetch } from '../../utils/apiFetch';
import { describeRequestError, responseErrorMessage, unwrapData } from '../../utils/apiResponse';
import { createPasswordResetLink, disableUser, enableUser, listAdminUsers } from '../../services/coreIdentity';
import { deleteInvite, listInvites, type PendingInviteRow } from '../../services/booksInvites';
import { emailBindingLabel, guestPolicyLabel, inviteLink, type AcceptedInvite } from './usersInvites';
import { UserFootprintDrawer } from './UserFootprintDrawer';
import type { UsersPageExtensions } from './usersExtensions';
import { errorText } from './userAccountParts';
import { SignInRequiredNotice } from '../common/SignInRequiredNotice';
import { apiErrorFromResponse, isSignInRequiredRefusal } from '../../utils/signInRequired';
import {
  Box,
  Button,
  Checkbox,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControlLabel,
  IconButton,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  TextField,
  Tooltip,
  Typography,
  Alert,
} from '@mui/material';
import {
  PersonAdd as InviteIcon,
  Block as BlockIcon,
  CheckCircle as ActiveIcon,
  VpnKey as ResetIcon,
  ContentCopy as CopyIcon,
  Email as EmailIcon,
  Delete as RevokeInviteIcon,
  AlternateEmail as SetEmailIcon,
  Fingerprint as FootprintIcon,
} from '@mui/icons-material';

const API_BASE = '/api/v1';

interface InviteSettings {
  mailConfigured: boolean;
  /** Why an invitation email cannot go out, when it cannot. */
  mailUnavailableReason: string;
  guestPolicySyncConfigured: boolean;
}

interface User {
  id: string;
  username: string;
  email: string;
  roles: string[];
  /** `active` or `disabled` (core's words); `merged_into_user_id` marks a merged-away account. */
  status: string;
  created_at?: string;
  /** Set once an admin merged this account into another. */
  merged_into_user_id?: string;
}

/**
 * `authDisabled` is the host's reading of the server's auth setting (books: AuthContext.authEnabled
 * === false; the shell: /auth/status auth_disabled). Under it core refuses invite create and resend,
 * deactivate, reactivate and reset-password for the auth-disabled principal (SEC5-8). The page says
 * "Turn on sign-in to do this" and:
 *   - hides deactivate, reactivate and reset-password (nothing the page can do makes them work);
 *   - keeps invite create and resend visible but disabled, so the message sits next to the controls
 *     the admin came for;
 *   - leaves the invite list and revoke live: they answer under auth-off (books routes, see
 *     services/booksInvites.ts).
 * A refusal that arrives anyway (the auth setting was unknown) only disables.
 */
export function UsersPage({
  extensions,
  authDisabled = false,
}: {
  extensions?: UsersPageExtensions;
  authDisabled?: boolean;
}) {
  // Set when a call came back refused anyway (the auth setting was unknown to the page).
  const [signInRefused, setSignInRefused] = useState(false);
  const signInBlocked = authDisabled || signInRefused;
  const [users, setUsers] = useState<User[]>([]);
  const [invites, setInvites] = useState<PendingInviteRow[]>([]);
  const [accepted, setAccepted] = useState<AcceptedInvite[]>([]);
  // Books owns invitation e-mail (send, resend), the guest-access flags and the pending list itself
  // (it answers under auth-off, where core's native list does not): one books call fills all three.
  const [settings, setSettings] = useState<InviteSettings>({
    mailConfigured: false,
    mailUnavailableReason: '',
    guestPolicySyncConfigured: false,
  });
  const [notice, setNotice] = useState('');
  const [inviteOpen, setInviteOpen] = useState(false);
  const [error, setError] = useState('');
  const [emailUser, setEmailUser] = useState<User | null>(null);
  // The user whose sign-ins-and-data drawer is open lives in the address bar
  // (`?footprint=<userId>`), so the drawer can be linked to. The id, not the
  // row, is what the URL holds; the row is looked up in the loaded list, so a
  // link opens once the list has arrived and an id nobody has opens nothing.
  // Merge and invite dialogs are transient actions and are not linked.
  const [searchParams, setSearchParams] = useSearchParams();
  const footprintId = searchParams.get('footprint');
  const setFootprintUser = useCallback(
    (u: User | null) => {
      setSearchParams(
        (prev) => {
          const next = new URLSearchParams(prev);
          if (u) next.set('footprint', u.id);
          else next.delete('footprint');
          return next;
        },
        { replace: true }
      );
    },
    [setSearchParams]
  );
  // Bumped whenever an extension reports a change, so panels that list its records reload.
  const [extensionsRefresh, setExtensionsRefresh] = useState(0);
  const footprintUser = footprintId ? (users.find((u) => String(u.id) === footprintId) ?? null) : null;

  const load = useCallback(async () => {
    try {
      const [u, list] = await Promise.all([listAdminUsers(), listInvites()]);
      setUsers(u);
      setInvites(list.invites);
      setAccepted(list.accepted);
      setSettings({
        mailConfigured: list.mailConfigured,
        mailUnavailableReason: list.mailUnavailableReason,
        guestPolicySyncConfigured: list.guestPolicySyncConfigured,
      });
      setError('');
    } catch (err) {
      setError(errorText(err, 'Failed to load users'));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  // Runs a user-management write and surfaces any failure in the page alert.
  // A failed call must never look like a success, so the list is only reloaded
  // when the write actually went through.
  const runUserAction = useCallback(
    async <T,>(action: () => Promise<T>, failureMessage: string): Promise<T | null> => {
      setError('');
      try {
        return await action();
      } catch (err) {
        if (isSignInRequiredRefusal(err, authDisabled)) {
          setSignInRefused(true);
          return null;
        }
        setError(errorText(err, failureMessage));
        return null;
      }
    },
    [authDisabled]
  );

  const handleDisable = useCallback(
    async (id: string) => {
      if ((await runUserAction(() => disableUser(id).then(() => true), 'Failed to deactivate user')) !== null) {
        load();
      }
    },
    [load, runUserAction]
  );

  // The server answers 409 `merged_away` for a merged-away account; the page does not offer
  // the action for one, so that answer only reaches a stale row, and shows as the page error.
  const handleEnable = useCallback(
    async (id: string) => {
      if ((await runUserAction(() => enableUser(id).then(() => true), 'Failed to reactivate user')) !== null) {
        load();
      }
    },
    [load, runUserAction]
  );

  const handleResetPassword = useCallback(
    async (user: User) => {
      setNotice('');
      const link = await runUserAction(() => createPasswordResetLink(user.id), 'Failed to reset password');
      if (!link) return;
      // The link is shown once: core does not keep the token, so it cannot be listed again.
      navigator.clipboard.writeText(link).catch(() => {});
      setNotice(`Password-reset link for ${user.username} (copied; it is shown once and works once): ${link}`);
      load();
    },
    [load, runUserAction]
  );

  const handleResend = useCallback(
    async (inv: PendingInviteRow) => {
      setNotice('');
      // Books' route: core has no mailer.
      if (
        await runUserAction(async () => {
          const resp = await apiFetch(`${API_BASE}/users/invites/${encodeURIComponent(inv.id)}/resend`, {
            method: 'POST',
          });
          if (!resp.ok) throw await apiErrorFromResponse(resp, `HTTP ${resp.status}`);
          return true;
        }, 'Failed to resend invitation')
      ) {
        setNotice(`Invitation emailed again to ${inv.email}.`);
        load();
      }
    },
    [load, runUserAction]
  );

  const handleRevokeInvite = useCallback(
    async (inv: PendingInviteRow) => {
      setNotice('');
      if ((await runUserAction(() => deleteInvite(inv.id).then(() => true), 'Failed to revoke invitation')) !== null) {
        setNotice(`Invitation for ${inv.username} revoked.`);
        load();
      }
    },
    [load, runUserAction]
  );

  return (
    <Box sx={{ p: 3 }}>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', mb: 2 }}>
        <Typography variant="h4">Users</Typography>
        <Button
          variant="contained"
          startIcon={<InviteIcon />}
          onClick={() => setInviteOpen(true)}
          disabled={signInBlocked}
        >
          Create Invite
        </Button>
      </Box>

      {signInBlocked && <SignInRequiredNotice sx={{ mb: 2 }} />}
      {error && (
        <Alert severity="error" sx={{ mb: 2 }}>
          {error}
        </Alert>
      )}
      {notice && (
        <Alert severity="success" sx={{ mb: 2 }} onClose={() => setNotice('')}>
          {notice}
        </Alert>
      )}

      <TableContainer component={Paper} sx={{ mb: 4 }}>
        <Table size="small">
          <TableHead>
            <TableRow>
              <TableCell>Username</TableCell>
              <TableCell>Email</TableCell>
              <TableCell>Roles</TableCell>
              <TableCell>Status</TableCell>
              <TableCell>Created</TableCell>
              <TableCell align="right">Actions</TableCell>
            </TableRow>
          </TableHead>
          <TableBody>
            {users.map((u) => (
              <TableRow key={u.id}>
                <TableCell>{u.username}</TableCell>
                <TableCell>{u.email}</TableCell>
                <TableCell>
                  {u.roles?.map((r) => (
                    <Chip key={r} label={r} size="small" sx={{ mr: 0.5 }} />
                  ))}
                </TableCell>
                <TableCell>
                  {u.merged_into_user_id ? (
                    <Tooltip title="This account was merged into another and cannot be enabled">
                      <Chip label="merged" size="small" />
                    </Tooltip>
                  ) : (
                    <Chip
                      label={u.status}
                      size="small"
                      color={
                        u.status === 'active'
                          ? 'success'
                          : u.status === 'disabled'
                            ? 'error'
                            : 'default'
                      }
                    />
                  )}
                </TableCell>
                <TableCell>
                  {u.created_at ? new Date(u.created_at).toLocaleDateString() : ''}
                </TableCell>
                <TableCell align="right">
                  {!authDisabled && u.status === 'active' && u.username !== '_system' && (
                    <Tooltip title="Deactivate">
                      <IconButton size="small" disabled={signInBlocked} onClick={() => handleDisable(u.id)}>
                        <BlockIcon fontSize="small" />
                      </IconButton>
                    </Tooltip>
                  )}
                  {!authDisabled && u.status === 'disabled' && !u.merged_into_user_id && (
                    <Tooltip title="Reactivate">
                      <IconButton size="small" disabled={signInBlocked} onClick={() => handleEnable(u.id)}>
                        <ActiveIcon fontSize="small" color="success" />
                      </IconButton>
                    </Tooltip>
                  )}
                  {u.username !== '_system' && (
                    <Tooltip title="Sign-ins & data">
                      <IconButton
                        size="small"
                        aria-label={`Sign-ins and data for ${u.username}`}
                        onClick={() => setFootprintUser(u)}
                      >
                        <FootprintIcon fontSize="small" />
                      </IconButton>
                    </Tooltip>
                  )}
                  {extensions?.rowActions?.map((RowAction, index) => (
                    <RowAction
                      key={index}
                      user={u}
                      users={users}
                      onDone={(message) => {
                        if (message) setNotice(message);
                        setExtensionsRefresh((n) => n + 1);
                        load();
                      }}
                    />
                  ))}
                  {u.username !== '_system' && (
                    <Tooltip title="Set email">
                      <IconButton
                        size="small"
                        aria-label={`Set email for ${u.username}`}
                        onClick={() => setEmailUser(u)}
                      >
                        <SetEmailIcon fontSize="small" />
                      </IconButton>
                    </Tooltip>
                  )}
                  {!authDisabled && u.username !== '_system' && (
                    <Tooltip title="Reset Password">
                      <IconButton size="small" disabled={signInBlocked} onClick={() => handleResetPassword(u)}>
                        <ResetIcon fontSize="small" />
                      </IconButton>
                    </Tooltip>
                  )}
                </TableCell>
              </TableRow>
            ))}
          </TableBody>
        </Table>
      </TableContainer>

      {invites.length > 0 && (
        <>
          <Typography variant="h5" sx={{ mb: 1 }}>
            Pending Invites
          </Typography>
          {!settings.guestPolicySyncConfigured && (
            <Alert severity="warning" sx={{ mb: 1 }}>
              {GUEST_ACCESS_NOT_CONFIGURED}
            </Alert>
          )}
          <TableContainer component={Paper}>
            <Table size="small">
              <TableHead>
                <TableRow>
                  <TableCell>Username</TableCell>
                  <TableCell>Email</TableCell>
                  <TableCell>Role</TableCell>
                  <TableCell>Expires</TableCell>
                  <TableCell>Invitation email</TableCell>
                  <TableCell align="right">Actions</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {invites.map((inv) => (
                  <TableRow key={inv.id}>
                    <TableCell>{inv.username}</TableCell>
                    <TableCell>{inv.email || '—'}</TableCell>
                    <TableCell>{inv.role_id}</TableCell>
                    <TableCell>{new Date(inv.expires_at).toLocaleString()}</TableCell>
                    <TableCell>
                      <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5 }}>
                        <Typography variant="body2">
                          {inv.email_sent_at
                            ? `Sent ${new Date(inv.email_sent_at).toLocaleString()}`
                            : 'Not sent'}
                        </Typography>
                        {inv.email && settings.mailConfigured && (
                          <Tooltip title="Resend email">
                            <IconButton
                              size="small"
                              aria-label={`Resend invitation email to ${inv.email}`}
                              disabled={signInBlocked}
                              onClick={() => handleResend(inv)}
                            >
                              <EmailIcon fontSize="small" />
                            </IconButton>
                          </Tooltip>
                        )}
                      </Box>
                    </TableCell>
                    <TableCell align="right">
                      <Tooltip title="Revoke invitation">
                        <IconButton
                          size="small"
                          aria-label={`Revoke invitation for ${inv.username}`}
                          onClick={() => handleRevokeInvite(inv)}
                        >
                          <RevokeInviteIcon fontSize="small" />
                        </IconButton>
                      </Tooltip>
                    </TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          </TableContainer>
        </>
      )}

      {accepted.length > 0 && (
        <>
          <Typography variant="h5" sx={{ mt: 4, mb: 1 }}>
            Accepted Invites (last 30 days)
          </Typography>
          <TableContainer component={Paper}>
            <Table size="small">
              <TableHead>
                <TableRow>
                  <TableCell>Username</TableCell>
                  <TableCell>Email</TableCell>
                  <TableCell>Accepted</TableCell>
                  <TableCell>Cloudflare browser access</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {accepted.map((a) => {
                  const g = guestPolicyLabel(a);
                  return (
                    <TableRow key={`${a.username}-${a.accepted_at}`}>
                      <TableCell>{a.username}</TableCell>
                      <TableCell>{emailBindingLabel(a)}</TableCell>
                      <TableCell>{new Date(a.accepted_at).toLocaleString()}</TableCell>
                      <TableCell>
                        <Tooltip title={a.guest_policy_detail || ''}>
                          <Chip size="small" label={g.label} color={g.ok ? 'success' : 'warning'} />
                        </Tooltip>
                      </TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </TableContainer>
        </>
      )}

      {extensions?.panels?.map((Panel, index) => (
        <Panel
          key={index}
          users={users}
          refreshKey={extensionsRefresh}
          onNotice={(message) => {
            setNotice(message);
            load();
          }}
        />
      ))}
      <UserFootprintDrawer
        key={footprintUser?.id ?? 'none'}
        user={footprintUser}
        onClose={() => setFootprintUser(null)}
      />

      <CreateInviteDialog
        open={inviteOpen}
        mailConfigured={settings.mailConfigured}
        mailUnavailableReason={settings.mailUnavailableReason}
        guestPolicySyncConfigured={settings.guestPolicySyncConfigured}
        onClose={() => setInviteOpen(false)}
        onCreated={load}
        authDisabled={authDisabled}
        onRefused={() => setSignInRefused(true)}
      />
      {emailUser && (
        <SetEmailDialog
          user={emailUser}
          onClose={() => setEmailUser(null)}
          onSaved={(message) => {
            setEmailUser(null);
            setNotice(message);
            load();
          }}
        />
      )}
    </Box>
  );
}

/**
 * Sets or clears another user's email. The server records an admin-set
 * address as verified, so single sign-on with it reaches this account; it
 * needs the admin role.
 */
function SetEmailDialog({
  user,
  onClose,
  onSaved,
}: {
  user: User;
  onClose: () => void;
  onSaved: (message: string) => void;
}) {
  const [email, setEmail] = useState(user.email || '');
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);

  const save = async (value: string) => {
    setError('');
    setSaving(true);
    try {
      const resp = await apiFetch(`${API_BASE}/users/${user.id}/email`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ email: value }),
      });
      if (!resp.ok) {
        setError(await responseErrorMessage(resp));
        return;
      }
      onSaved(
        value.trim()
          ? `Email for ${user.username} set to ${value.trim().toLowerCase()}.`
          : `Email for ${user.username} cleared.`
      );
    } catch (err) {
      setError(describeRequestError(err, 'Failed to set email'));
    } finally {
      setSaving(false);
    }
  };

  return (
    <Dialog open onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>Set email for {user.username}</DialogTitle>
      <DialogContent>
        {error && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {error}
          </Alert>
        )}
        <Typography variant="body2" sx={{ mb: 2 }}>
          The address counts as verified: Google, GitHub or Cloudflare sign-in with it will open
          this account. Only set an address you know belongs to this person.
        </Typography>
        <TextField
          autoFocus
          fullWidth
          label="Email"
          type="email"
          value={email}
          onChange={(e) => setEmail(e.target.value)}
        />
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button color="warning" disabled={saving || !user.email} onClick={() => save('')}>
          Clear email
        </Button>
        <Button
          variant="contained"
          disabled={saving || email.trim() === ''}
          onClick={() => save(email)}
        >
          Save
        </Button>
      </DialogActions>
    </Dialog>
  );
}

interface CreateInviteResult {
  token?: string;
  invite_url?: string;
  email_sent?: boolean;
  email_error?: string;
}

// Shown when the Cloudflare guest-policy sync is not configured: an invited guest
// then has no way through Cloudflare, whatever the email says.
const GUEST_ACCESS_NOT_CONFIGURED =
  'Cloudflare guest access is not configured on this server (CF_ACCOUNT_ID, CF_GUESTS_POLICY_ID and CF_POLICY_API_TOKEN). ' +
  'A guest invited from outside the local network cannot get through Cloudflare until their address is added to the guest policy by hand.';

function CreateInviteDialog({
  open,
  mailConfigured,
  mailUnavailableReason,
  guestPolicySyncConfigured,
  onClose,
  onCreated,
  authDisabled,
  onRefused,
}: {
  open: boolean;
  mailConfigured: boolean;
  mailUnavailableReason?: string;
  guestPolicySyncConfigured: boolean;
  onClose: () => void;
  onCreated: () => void;
  authDisabled: boolean;
  /** The create came back refused for want of sign-in (SEC5-8); the page now shows the notice. */
  onRefused: () => void;
}) {
  const [username, setUsername] = useState('');
  const [email, setEmail] = useState('');
  const [sendEmail, setSendEmail] = useState(true);
  const [roleId, setRoleId] = useState('editor');
  const [error, setError] = useState('');
  const [result, setResult] = useState<CreateInviteResult | null>(null);
  const [copied, setCopied] = useState(false);

  const willSend = mailConfigured && sendEmail && email.trim() !== '';

  const reset = () => {
    setUsername('');
    setEmail('');
    setSendEmail(true);
    setError('');
    setResult(null);
    setCopied(false);
  };

  const handleClose = () => {
    reset();
    onClose();
  };

  const handleCreate = async () => {
    setError('');
    try {
      // Books' route (e-mail delivery lives there; core's create has none). Kept until the books
      // wrapper over core's invite create exists (lead hold-point, F4-08 B8).
      const resp = await apiFetch(`${API_BASE}/users/invite`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          username,
          role_id: roleId,
          email: email.trim(),
          send_email: willSend,
        }),
      });
      if (!resp.ok) {
        throw await apiErrorFromResponse(resp, 'Failed to create invite');
      }
      const body = unwrapData<CreateInviteResult>(await resp.json());
      setResult(body);
      onCreated();
    } catch (err: unknown) {
      if (isSignInRequiredRefusal(err, authDisabled)) {
        onRefused();
        handleClose();
        return;
      }
      setError(describeRequestError(err, (err as Error).message));
    }
  };

  const link = result?.token ? inviteLink(result.token) : '';

  return (
    <Dialog open={open} onClose={handleClose} maxWidth="sm" fullWidth>
      <DialogTitle>Create Invite</DialogTitle>
      <DialogContent>
        {result ? (
          <Box>
            {result.email_sent ? (
              <Alert severity="success" sx={{ mb: 2 }}>
                Invitation emailed to {email.trim()}.
              </Alert>
            ) : (
              <>
                {result.email_error && (
                  <Alert severity="warning" sx={{ mb: 2 }}>
                    The email was not sent: {result.email_error}
                  </Alert>
                )}
                <Typography variant="body2" sx={{ mb: 1 }}>
                  Send this link to {username} yourself. It works once.
                </Typography>
              </>
            )}
            <Box sx={{ display: 'flex', alignItems: 'center', gap: 1 }}>
              <Typography
                variant="body2"
                component="code"
                sx={{ fontFamily: 'monospace', wordBreak: 'break-all', flex: 1 }}
              >
                {link}
              </Typography>
              <Tooltip title={copied ? 'Copied!' : 'Copy invitation link'}>
                <IconButton
                  size="small"
                  aria-label="Copy new invitation link"
                  onClick={() => {
                    navigator.clipboard.writeText(link).catch(() => {});
                    setCopied(true);
                  }}
                >
                  <CopyIcon fontSize="small" />
                </IconButton>
              </Tooltip>
            </Box>
          </Box>
        ) : (
          <>
            {!guestPolicySyncConfigured && (
              <Alert severity="warning" sx={{ mb: 1 }}>
                {GUEST_ACCESS_NOT_CONFIGURED}
              </Alert>
            )}
            <TextField
              fullWidth
              label="Username"
              value={username}
              onChange={(e) => setUsername(e.target.value)}
              margin="normal"
              required
            />
            <TextField
              fullWidth
              label="Email"
              type="email"
              value={email}
              onChange={(e) => setEmail(e.target.value)}
              margin="normal"
              helperText="Optional. When the invitation is emailed, accepting it sets this as their account email and allows single sign-on with it."
            />
            <FormControlLabel
              control={
                <Checkbox
                  checked={mailConfigured && sendEmail}
                  onChange={(e) => setSendEmail(e.target.checked)}
                  disabled={!mailConfigured}
                />
              }
              label={
                mailConfigured
                  ? 'Send the invitation by email'
                  : `Send the invitation by email (${mailUnavailableReason || 'email is not configured on this server'})`
              }
            />
            <TextField
              fullWidth
              label="Role"
              value={roleId}
              onChange={(e) => setRoleId(e.target.value)}
              margin="normal"
              select
              slotProps={{
                select: { native: true },
              }}
            >
              <option value="admin">Admin</option>
              <option value="editor">Editor</option>
              <option value="viewer">Viewer</option>
            </TextField>
            {error && (
              <Typography color="error" variant="body2" sx={{ mt: 1 }}>
                {error}
              </Typography>
            )}
          </>
        )}
      </DialogContent>
      <DialogActions>
        {result ? (
          <Button onClick={handleClose} variant="contained">
            Done
          </Button>
        ) : (
          <>
            <Button onClick={handleClose}>Cancel</Button>
            <Button onClick={handleCreate} variant="contained" disabled={!username}>
              Create Invite
            </Button>
          </>
        )}
      </DialogActions>
    </Dialog>
  );
}
