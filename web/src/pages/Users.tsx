// file: web/src/pages/Users.tsx
// version: 1.2.0
// guid: 4d2e3f1a-5b6c-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-10-10

import { useCallback, useEffect, useState, useRef } from 'react';
import { apiFetch } from '../utils/apiFetch';
import { describeRequestError, responseErrorMessage, unwrapData } from '../utils/apiResponse';
import {
  Box,
  Button,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
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
  role_id: string;
  expires_at: string;
}

export default function Users() {
  const [users, setUsers] = useState<User[]>([]);
  const [invites, setInvites] = useState<Invite[]>([]);
  const [inviteOpen, setInviteOpen] = useState(false);
  const [error, setError] = useState('');
  const [copiedToken, setCopiedToken] = useState('');

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
      const iBody = unwrapData<{ invites?: Invite[] }>(await iResp.json());
      setUsers(uBody.users || []);
      setInvites(iBody.invites || []);
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

  const handleCopyToken = useCallback((token: string) => {
    navigator.clipboard.writeText(token).catch(() => {});
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
                  <TableCell>Role</TableCell>
                  <TableCell>Expires</TableCell>
                  <TableCell>Token</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {invites.map((inv) => (
                  <TableRow key={inv.token}>
                    <TableCell>{inv.username}</TableCell>
                    <TableCell>{inv.role_id}</TableCell>
                    <TableCell>{new Date(inv.expires_at).toLocaleString()}</TableCell>
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
                        <Tooltip title={copiedToken === inv.token ? 'Copied!' : 'Copy token'}>
                          <IconButton size="small" onClick={() => handleCopyToken(inv.token)}>
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

      <CreateInviteDialog open={inviteOpen} onClose={() => setInviteOpen(false)} onCreated={load} />
    </Box>
  );
}

function CreateInviteDialog({
  open,
  onClose,
  onCreated,
}: {
  open: boolean;
  onClose: () => void;
  onCreated: () => void;
}) {
  const [username, setUsername] = useState('');
  const [roleId, setRoleId] = useState('editor');
  const [error, setError] = useState('');

  const handleCreate = async () => {
    setError('');
    try {
      const resp = await apiFetch(`${API_BASE}/users/invite`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, role_id: roleId }),
      });
      if (!resp.ok) {
        const reason = await responseErrorMessage(resp);
        throw new Error(reason.startsWith('HTTP ') ? 'Failed to create invite' : reason);
      }
      setUsername('');
      onClose();
      onCreated();
    } catch (err: unknown) {
      setError(describeRequestError(err, (err as Error).message));
    }
  };

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>Create Invite</DialogTitle>
      <DialogContent>
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
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button onClick={handleCreate} variant="contained" disabled={!username}>
          Create Invite
        </Button>
      </DialogActions>
    </Dialog>
  );
}
