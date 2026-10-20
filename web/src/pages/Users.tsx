// file: web/src/pages/Users.tsx
// version: 1.5.1
// guid: 4d2e3f1a-5b6c-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-10-19

import { useCallback, useEffect, useState, useRef } from 'react';
import { apiFetch } from '../utils/apiFetch';
import { describeRequestError, responseErrorMessage, unwrapData } from '../utils/apiResponse';
import {
  emailBindingLabel,
  guestPolicyLabel,
  inviteLink,
  type AcceptedInvite,
} from './usersInvites';
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
  AlternateEmail as SetEmailIcon,
} from '@mui/icons-material';

const API_BASE = '/api/v1';

interface User {
  id: string;
  username: string;
  email: string;
  roles: string[];
  status: string;
  created_at: string;
}

interface Invite {
  token: string;
  username: string;
  email?: string;
  role_id: string;
  expires_at: string;
  email_sent_at?: string;
}

interface InviteSettings {
  mailConfigured: boolean;
  /** Why an invitation email cannot go out, when it cannot. */
  mailUnavailableReason: string;
  guestHeadersConfigured: boolean;
  guestPolicySyncConfigured: boolean;
}

interface InvitesBody {
  invites?: Invite[];
  accepted?: AcceptedInvite[];
  mail_configured?: boolean;
  mail_unavailable_reason?: string;
  guest_headers_configured?: boolean;
  guest_policy_sync_configured?: boolean;
}

export default function Users() {
  const [users, setUsers] = useState<User[]>([]);
  const [invites, setInvites] = useState<Invite[]>([]);
  const [accepted, setAccepted] = useState<AcceptedInvite[]>([]);
  const [settings, setSettings] = useState<InviteSettings>({
    mailConfigured: false,
    mailUnavailableReason: '',
    guestHeadersConfigured: false,
    guestPolicySyncConfigured: false,
  });
  const [notice, setNotice] = useState('');
  const [inviteOpen, setInviteOpen] = useState(false);
  const [error, setError] = useState('');
  const [copiedToken, setCopiedToken] = useState('');
  const [emailUser, setEmailUser] = useState<User | null>(null);

  // Ref for token reset timeout
  const copiedTokenTimeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const load = useCallback(async () => {
    try {
      const [uResp, iResp] = await Promise.all([
        apiFetch(`${API_BASE}/users`),
        apiFetch(`${API_BASE}/users/invites`),
      ]);
      if (!uResp.ok) throw new Error(await responseErrorMessage(uResp));
      if (!iResp.ok) throw new Error(await responseErrorMessage(iResp));
      const uBody = unwrapData<{ users?: User[] }>(await uResp.json());
      const iBody = unwrapData<InvitesBody>(await iResp.json());
      setUsers(uBody.users || []);
      setInvites(iBody.invites || []);
      setAccepted(iBody.accepted || []);
      setSettings({
        mailConfigured: !!iBody.mail_configured,
        mailUnavailableReason: iBody.mail_unavailable_reason || '',
        guestHeadersConfigured: !!iBody.guest_headers_configured,
        guestPolicySyncConfigured: !!iBody.guest_policy_sync_configured,
      });
      setError('');
    } catch (err) {
      setError(describeRequestError(err, 'Failed to load users'));
    }
  }, []);

  useEffect(() => {
    load();
  }, [load]);

  // Runs a user-management write and surfaces any failure in the page alert.
  // A failed or login-redirected call must never look like a success, so the
  // list is only reloaded when the write actually went through.
  const runUserAction = useCallback(
    async (path: string, failureMessage: string): Promise<Response | null> => {
      setError('');
      try {
        const resp = await apiFetch(`${API_BASE}${path}`, { method: 'POST' });
        if (!resp.ok) {
          const reason = await responseErrorMessage(resp);
          setError(reason.startsWith('HTTP ') ? `${failureMessage} (${reason})` : reason);
          return null;
        }
        return resp;
      } catch (err) {
        setError(describeRequestError(err, failureMessage));
        return null;
      }
    },
    []
  );

  const handleDeactivate = useCallback(
    async (id: string) => {
      if (await runUserAction(`/users/${id}/deactivate`, 'Failed to deactivate user')) {
        load();
      }
    },
    [load, runUserAction]
  );

  const handleReactivate = useCallback(
    async (id: string) => {
      if (await runUserAction(`/users/${id}/reactivate`, 'Failed to reactivate user')) {
        load();
      }
    },
    [load, runUserAction]
  );

  const handleResetPassword = useCallback(
    async (id: string) => {
      const resp = await runUserAction(`/users/${id}/reset-password`, 'Failed to reset password');
      if (!resp) return;
      const data = await resp.json().catch(() => null);
      // The endpoint returns { token, login_url }. Copy the URL — it is what the
      // user actually clicks. The bare token is kept as a fallback for a server
      // that predates login_url, and because login_url is relative when
      // EXTERNAL_URL is unset (the admin then prepends the address themselves).
      const payload = unwrapData<{ login_url?: string; token?: string } | null>(data);
      const copyable = payload?.login_url || payload?.token;
      if (copyable) {
        setCopiedToken(copyable);
        navigator.clipboard.writeText(copyable).catch(() => {});
      }
      load();
    },
    [load, runUserAction]
  );

  const handleResend = useCallback(
    async (inv: Invite) => {
      setNotice('');
      if (
        await runUserAction(`/users/invites/${inv.token}/resend`, 'Failed to resend invitation')
      ) {
        setNotice(`Invitation emailed again to ${inv.email}.`);
        load();
      }
    },
    [load, runUserAction]
  );

  const handleCopyToken = useCallback((token: string) => {
    navigator.clipboard.writeText(inviteLink(token)).catch(() => {});
    setCopiedToken(token);
    if (copiedTokenTimeoutRef.current) clearTimeout(copiedTokenTimeoutRef.current);
    copiedTokenTimeoutRef.current = setTimeout(() => setCopiedToken(''), 3000);
  }, []);

  // Cleanup timeout on unmount
  useEffect(() => {
    return () => {
      if (copiedTokenTimeoutRef.current) {
        clearTimeout(copiedTokenTimeoutRef.current);
      }
    };
  }, []);

  return (
    <Box sx={{ p: 3 }}>
      <Box sx={{ display: 'flex', justifyContent: 'space-between', mb: 2 }}>
        <Typography variant="h4">Users</Typography>
        <Button variant="contained" startIcon={<InviteIcon />} onClick={() => setInviteOpen(true)}>
          Create Invite
        </Button>
      </Box>

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
                  <Chip
                    label={u.status}
                    size="small"
                    color={
                      u.status === 'active'
                        ? 'success'
                        : u.status === 'locked'
                          ? 'error'
                          : 'default'
                    }
                  />
                </TableCell>
                <TableCell>{new Date(u.created_at).toLocaleDateString()}</TableCell>
                <TableCell align="right">
                  {u.status === 'active' && u.username !== '_system' && (
                    <Tooltip title="Deactivate">
                      <IconButton size="small" onClick={() => handleDeactivate(u.id)}>
                        <BlockIcon fontSize="small" />
                      </IconButton>
                    </Tooltip>
                  )}
                  {u.status === 'locked' && (
                    <Tooltip title="Reactivate">
                      <IconButton size="small" onClick={() => handleReactivate(u.id)}>
                        <ActiveIcon fontSize="small" color="success" />
                      </IconButton>
                    </Tooltip>
                  )}
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
                  {u.username !== '_system' && (
                    <Tooltip title="Reset Password">
                      <IconButton size="small" onClick={() => handleResetPassword(u.id)}>
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
          <TableContainer component={Paper}>
            <Table size="small">
              <TableHead>
                <TableRow>
                  <TableCell>Username</TableCell>
                  <TableCell>Email</TableCell>
                  <TableCell>Role</TableCell>
                  <TableCell>Expires</TableCell>
                  <TableCell>Invitation email</TableCell>
                  <TableCell>Link</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {invites.map((inv) => (
                  <TableRow key={inv.token}>
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
                              onClick={() => handleResend(inv)}
                            >
                              <EmailIcon fontSize="small" />
                            </IconButton>
                          </Tooltip>
                        )}
                      </Box>
                    </TableCell>
                    <TableCell>
                      <Box sx={{ display: 'flex', alignItems: 'center', gap: 0.5 }}>
                        <Typography
                          variant="caption"
                          noWrap
                          sx={{
                            fontFamily: 'monospace',
                            maxWidth: 200,
                          }}
                        >
                          {inv.token.slice(0, 16)}...
                        </Typography>
                        <Tooltip
                          title={copiedToken === inv.token ? 'Copied!' : 'Copy invitation link'}
                        >
                          <IconButton
                            size="small"
                            aria-label={`Copy invitation link for ${inv.username}`}
                            onClick={() => handleCopyToken(inv.token)}
                          >
                            <CopyIcon fontSize="small" />
                          </IconButton>
                        </Tooltip>
                      </Box>
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

      <CreateInviteDialog
        open={inviteOpen}
        mailConfigured={settings.mailConfigured}
        mailUnavailableReason={settings.mailUnavailableReason}
        onClose={() => setInviteOpen(false)}
        onCreated={load}
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

function CreateInviteDialog({
  open,
  mailConfigured,
  mailUnavailableReason,
  onClose,
  onCreated,
}: {
  open: boolean;
  mailConfigured: boolean;
  mailUnavailableReason?: string;
  onClose: () => void;
  onCreated: () => void;
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
        const reason = await responseErrorMessage(resp);
        throw new Error(reason.startsWith('HTTP ') ? 'Failed to create invite' : reason);
      }
      const body = unwrapData<CreateInviteResult>(await resp.json());
      setResult(body);
      onCreated();
    } catch (err: unknown) {
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
