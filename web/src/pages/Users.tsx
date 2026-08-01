// file: web/src/pages/Users.tsx
// version: 1.1.0
// guid: 4d2e3f1a-5b6c-4a70-b8c5-3d7e0f1b9a99

import { useCallback, useEffect, useState, useRef } from 'react';
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
        fetch(`${API_BASE}/users`).then((r) => r.json()),
        fetch(`${API_BASE}/users/invites`).then((r) => r.json()),
      ]);
      setUsers(uResp.users || []);
      setInvites(iResp.invites || []);
    } catch {
      setError('Failed to load users');
    }
  }, []);

  useEffect(() => { load(); }, [load]);

  const handleDeactivate = useCallback(async (id: string) => {
    await fetch(`${API_BASE}/users/${id}/deactivate`, { method: 'POST' });
    load();
  }, [load]);

  const handleReactivate = useCallback(async (id: string) => {
    await fetch(`${API_BASE}/users/${id}/reactivate`, { method: 'POST' });
    load();
  }, [load]);

  const handleResetPassword = useCallback(async (id: string) => {
    const resp = await fetch(`${API_BASE}/users/${id}/reset-password`, { method: 'POST' });
    const data = await resp.json();
    // The endpoint returns { token, login_url }. Copy the URL — it is what the
    // user actually clicks. The bare token is kept as a fallback for a server
    // that predates login_url, and because login_url is relative when
    // EXTERNAL_URL is unset (the admin then prepends the address themselves).
    const payload = data?.data ?? data;
    const copyable = payload?.login_url || payload?.token;
    if (copyable) {
      setCopiedToken(copyable);
      navigator.clipboard.writeText(copyable).catch(() => {});
    }
    load();
  }, [load]);

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

      {error && <Alert severity="error" sx={{ mb: 2 }}>{error}</Alert>}

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
                    color={u.status === 'active' ? 'success' : u.status === 'locked' ? 'error' : 'default'}
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
          <Typography variant="h5" sx={{ mb: 1 }}>Pending Invites</Typography>
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
                        <Typography variant="caption" fontFamily="monospace" noWrap sx={{ maxWidth: 200 }}>
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
      const resp = await fetch(`${API_BASE}/users/invite`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ username, role_id: roleId }),
      });
      if (!resp.ok) {
        const body = await resp.json().catch(() => ({}));
        throw new Error(body.error || 'Failed to create invite');
      }
      setUsername('');
      onClose();
      onCreated();
    } catch (err: unknown) {
      setError((err as Error).message);
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
          SelectProps={{ native: true }}
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
