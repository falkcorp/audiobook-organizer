// file: web/src/components/settings/APIKeysTab.tsx
// version: 1.1.0
// guid: f6a7b8c9-d0e1-2345-fabc-456789012345
// last-edited: 2026-06-22

import { useState, useEffect, useRef } from 'react';
import {
  Box,
  Stack,
  Typography,
  Alert,
  Button,
  Dialog,
  DialogTitle,
  DialogContent,
  DialogActions,
  TextField,
  Chip,
  CircularProgress,
  FormControlLabel,
  Switch,
  FormGroup,
  Checkbox,
  Select,
  InputLabel,
  MenuItem,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Tooltip,
  IconButton,
  Paper,
  FormControl as MuiFormControl,
} from '@mui/material';
import {
  VpnKey as VpnKeyIcon,
  Add as AddIcon,
  Delete as DeleteIcon,
  ContentCopy as ContentCopyIcon,
  CheckBox as CheckBoxIcon,
  CheckBoxOutlineBlank as CheckBoxOutlineBlankIcon,
} from '@mui/icons-material';
import * as api from '../../services/api';

const ALL_SCOPES = [
  'library.view',
  'library.edit_metadata',
  'library.delete',
  'library.organize',
  'scan.trigger',
  'integrations.manage',
  'users.manage',
  'settings.manage',
  'playlists.create',
  'requests.create',
  'requests.approve',
];

const EXPIRES_OPTIONS = [
  { label: '30 days', value: 30 },
  { label: '60 days', value: 60 },
  { label: '90 days', value: 90 },
  { label: '180 days', value: 180 },
  { label: '365 days', value: 365 },
  { label: 'Never', value: 0 },
];

function relativeTime(dateStr: string): string {
  const date = new Date(dateStr);
  const days = Math.floor((Date.now() - date.getTime()) / 86400000);
  if (days === 0) return 'Today';
  if (days === 1) return 'Yesterday';
  if (days < 30) return `${days}d ago`;
  if (days < 365) return `${Math.floor(days / 30)}mo ago`;
  return `${Math.floor(days / 365)}y ago`;
}

function statusChip(status: string) {
  const colors: Record<string, 'success' | 'warning' | 'error' | 'default'> = {
    active: 'success',
    inactive: 'warning',
    revoked: 'error',
  };
  return (
    <Chip
      label={status}
      color={colors[status] ?? 'default'}
      size="small"
      sx={{ textTransform: 'capitalize' }}
    />
  );
}

export function APIKeysTab() {
  const [keys, setKeys] = useState<api.APIKey[]>([]);
  const [loading, setLoading] = useState(true);
  const [showAll, setShowAll] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const [createOpen, setCreateOpen] = useState(false);
  const [newName, setNewName] = useState('');
  const [newDesc, setNewDesc] = useState('');
  const [newScopes, setNewScopes] = useState<string[]>([]);
  const [newExpires, setNewExpires] = useState(90);
  const [creating, setCreating] = useState(false);
  const [createdToken, setCreatedToken] = useState<string | null>(null);
  const [copied, setCopied] = useState(false);
  const timeoutRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const isUnmountedRef = useRef(false);

  const fetchKeys = async (all: boolean) => {
    setLoading(true);
    setError(null);
    try {
      const data = await api.listAPIKeys(all);
      setKeys(data);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to load API keys');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => {
    fetchKeys(showAll);
  }, [showAll]);

  // Cleanup timeouts on unmount
  useEffect(() => {
    return () => {
      isUnmountedRef.current = true;
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
        timeoutRef.current = null;
      }
    };
  }, []);

  const handleCreate = async () => {
    if (!newName.trim()) return;
    setCreating(true);
    try {
      const resp = await api.createAPIKey({
        name: newName,
        description: newDesc,
        scopes: newScopes,
        expires_in_days: newExpires,
      });
      setCreatedToken(resp.token);
      setCreateOpen(false);
      setNewName('');
      setNewDesc('');
      setNewScopes([]);
      setNewExpires(90);
      fetchKeys(showAll);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to create API key');
    } finally {
      setCreating(false);
    }
  };

  const handleToggleStatus = async (key: api.APIKey) => {
    const next: 'active' | 'inactive' = key.status === 'active' ? 'inactive' : 'active';
    try {
      await api.updateAPIKeyStatus(key.id, next);
      fetchKeys(showAll);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to update status');
    }
  };

  const handleRevoke = async (key: api.APIKey) => {
    if (!window.confirm(`Permanently revoke "${key.name}"? This cannot be undone.`)) return;
    try {
      await api.revokeAPIKey(key.id);
      fetchKeys(showAll);
    } catch (e) {
      setError(e instanceof Error ? e.message : 'Failed to revoke key');
    }
  };

  const copyToken = () => {
    if (!createdToken) return;
    navigator.clipboard.writeText(createdToken).then(() => {
      setCopied(true);
      // Clear any existing timeout
      if (timeoutRef.current) {
        clearTimeout(timeoutRef.current);
      }
      // Schedule reset with cleanup protection; clear token from state after confirmation (FE-7).
      timeoutRef.current = setTimeout(() => {
        if (!isUnmountedRef.current) {
          setCopied(false);
          setCreatedToken(null);
        }
        timeoutRef.current = null;
      }, 2000);
    });
  };

  return (
    <Box>
      <Stack direction="row" alignItems="center" justifyContent="space-between" mb={2}>
        <Typography variant="h6">
          <VpnKeyIcon sx={{ mr: 1, verticalAlign: 'middle' }} />
          API Keys
        </Typography>
        <Stack direction="row" spacing={1} alignItems="center">
          <FormControlLabel
            control={
              <Switch
                checked={showAll}
                onChange={(e) => setShowAll(e.target.checked)}
                size="small"
              />
            }
            label="All Keys"
          />
          <Button variant="contained" startIcon={<AddIcon />} onClick={() => setCreateOpen(true)}>
            Create API Key
          </Button>
        </Stack>
      </Stack>

      {error && <Alert severity="error" sx={{ mb: 2 }} onClose={() => setError(null)}>{error}</Alert>}

      {loading ? (
        <CircularProgress />
      ) : (
        <TableContainer component={Paper} variant="outlined">
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Name</TableCell>
                <TableCell>Identifier</TableCell>
                <TableCell>Scopes</TableCell>
                <TableCell>Status</TableCell>
                <TableCell>Last Used</TableCell>
                <TableCell>Expires</TableCell>
                <TableCell>Age</TableCell>
                {showAll && <TableCell>User</TableCell>}
                <TableCell align="right">Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {keys.length === 0 ? (
                <TableRow>
                  <TableCell colSpan={showAll ? 9 : 8} align="center">
                    <Typography color="text.secondary" variant="body2">No API keys yet</Typography>
                  </TableCell>
                </TableRow>
              ) : keys.map((k) => {
                const age = Math.floor((Date.now() - new Date(k.created_at).getTime()) / 86400000);
                const neverUsed = k.never_used;
                const staleUsage = !neverUsed && k.days_since_last_use != null && k.days_since_last_use > 365;
                const warnUsage = !neverUsed && k.days_since_last_use != null && k.days_since_last_use > 30;
                const expired = k.expires_at ? new Date(k.expires_at) < new Date() : false;
                const expiringSoon = k.expires_at && !expired
                  ? (new Date(k.expires_at).getTime() - Date.now()) < 14 * 86400000
                  : false;

                return (
                  <TableRow key={k.id} sx={{ opacity: k.status === 'revoked' ? 0.5 : 1 }}>
                    <TableCell>
                      <Tooltip title={k.description || ''} placement="top">
                        <Typography variant="body2" fontWeight={500}>{k.name}</Typography>
                      </Tooltip>
                    </TableCell>
                    <TableCell>
                      <Typography variant="caption" fontFamily="monospace" color="text.secondary">
                        {k.identifier}
                      </Typography>
                    </TableCell>
                    <TableCell>
                      <Stack direction="row" spacing={0.5} flexWrap="wrap">
                        {(k.scopes ?? []).map((s) => (
                          <Chip key={s} label={s} size="small" variant="outlined" />
                        ))}
                        {(k.scopes ?? []).length === 0 && (
                          <Typography variant="caption" color="text.secondary">none</Typography>
                        )}
                      </Stack>
                    </TableCell>
                    <TableCell>{statusChip(k.status)}</TableCell>
                    <TableCell>
                      {neverUsed ? (
                        <Chip label="Never" color="warning" size="small" />
                      ) : (
                        <Tooltip title={k.last_used_at ? new Date(k.last_used_at).toLocaleString() : ''}>
                          <Chip
                            label={k.last_used_at ? relativeTime(k.last_used_at) : '—'}
                            color={staleUsage ? 'error' : warnUsage ? 'warning' : 'default'}
                            size="small"
                          />
                        </Tooltip>
                      )}
                    </TableCell>
                    <TableCell>
                      {k.expires_at ? (
                        <Chip
                          label={expired ? 'Expired' : relativeTime(k.expires_at)}
                          color={expired || expiringSoon ? 'error' : 'default'}
                          size="small"
                        />
                      ) : (
                        <Typography variant="caption" color="text.secondary">Never</Typography>
                      )}
                    </TableCell>
                    <TableCell>
                      <Typography variant="caption">{age}d</Typography>
                    </TableCell>
                    {showAll && <TableCell><Typography variant="caption">{k.username}</Typography></TableCell>}
                    <TableCell align="right">
                      <Stack direction="row" spacing={0.5} justifyContent="flex-end">
                        {k.status !== 'revoked' && (
                          <Tooltip title={k.status === 'active' ? 'Deactivate' : 'Activate'}>
                            <IconButton
                              size="small"
                              onClick={() => handleToggleStatus(k)}
                            >
                              {k.status === 'active' ? <CheckBoxIcon fontSize="small" /> : <CheckBoxOutlineBlankIcon fontSize="small" />}
                            </IconButton>
                          </Tooltip>
                        )}
                        {k.status !== 'revoked' && (
                          <Tooltip title="Revoke permanently">
                            <IconButton size="small" color="error" onClick={() => handleRevoke(k)}>
                              <DeleteIcon fontSize="small" />
                            </IconButton>
                          </Tooltip>
                        )}
                      </Stack>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </TableContainer>
      )}

      {/* Create API Key Dialog */}
      <Dialog open={createOpen} onClose={() => setCreateOpen(false)} maxWidth="sm" fullWidth>
        <DialogTitle>Create API Key</DialogTitle>
        <DialogContent>
          <Stack spacing={2} sx={{ mt: 1 }}>
            <TextField
              label="Name"
              required
              fullWidth
              value={newName}
              onChange={(e) => setNewName(e.target.value)}
            />
            <TextField
              label="Description"
              fullWidth
              multiline
              rows={2}
              value={newDesc}
              onChange={(e) => setNewDesc(e.target.value)}
            />
            <Box>
              <Typography variant="body2" gutterBottom>Scopes</Typography>
              <FormGroup>
                {ALL_SCOPES.map((s) => (
                  <FormControlLabel
                    key={s}
                    control={
                      <Checkbox
                        checked={newScopes.includes(s)}
                        onChange={(e) => {
                          if (e.target.checked) setNewScopes((p) => [...p, s]);
                          else setNewScopes((p) => p.filter((x) => x !== s));
                        }}
                        size="small"
                      />
                    }
                    label={<Typography variant="caption" fontFamily="monospace">{s}</Typography>}
                  />
                ))}
              </FormGroup>
            </Box>
            <MuiFormControl fullWidth size="small">
              <InputLabel>Expires in</InputLabel>
              <Select
                label="Expires in"
                value={newExpires}
                onChange={(e) => setNewExpires(Number(e.target.value))}
              >
                {EXPIRES_OPTIONS.map((o) => (
                  <MenuItem key={o.value} value={o.value}>{o.label}</MenuItem>
                ))}
              </Select>
            </MuiFormControl>
          </Stack>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setCreateOpen(false)}>Cancel</Button>
          <Button
            variant="contained"
            onClick={handleCreate}
            disabled={!newName.trim() || creating}
          >
            {creating ? 'Creating...' : 'Create'}
          </Button>
        </DialogActions>
      </Dialog>

      {/* One-time token display */}
      <Dialog open={!!createdToken} maxWidth="sm" fullWidth>
        <DialogTitle>API Key Created</DialogTitle>
        <DialogContent>
          <Alert severity="warning" sx={{ mb: 2 }}>
            Copy this token now. You will not be able to see it again.
          </Alert>
          <Box sx={{ display: 'flex', alignItems: 'center', gap: 1, bgcolor: 'action.hover', p: 1.5, borderRadius: 1 }}>
            <Typography fontFamily="monospace" fontSize="0.8rem" sx={{ wordBreak: 'break-all', flex: 1 }}>
              {createdToken}
            </Typography>
            <Tooltip title={copied ? 'Copied!' : 'Copy'}>
              <IconButton onClick={copyToken} size="small">
                <ContentCopyIcon fontSize="small" />
              </IconButton>
            </Tooltip>
          </Box>
        </DialogContent>
        <DialogActions>
          <Button
            variant="contained"
            onClick={() => { setCreatedToken(null); setCopied(false); }}
          >
            I've saved this, close
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}
