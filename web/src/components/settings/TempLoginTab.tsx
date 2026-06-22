// file: web/src/components/settings/TempLoginTab.tsx
// version: 1.1.0
// guid: 6c7d8e9f-0a1b-2c3d-4e5f-6a7b8c9d0e1f
// last-edited: 2026-06-22

// Settings tab: pick a user, mint a 15-min single-use login URL,
// copy it to the clipboard. Useful for signing yourself in on a new
// phone / browser without typing a password.

import { useEffect, useMemo, useRef, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  CircularProgress,
  IconButton,
  MenuItem,
  Stack,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import ContentCopyIcon from '@mui/icons-material/ContentCopy';
import * as api from '../../services/api';
import { useToast } from '../toast/ToastProvider';

export function TempLoginTab() {
  const { toast } = useToast();
  const [users, setUsers] = useState<api.AdminUserSummary[]>([]);
  const [usersError, setUsersError] = useState('');
  const [selectedUserID, setSelectedUserID] = useState('');
  const [loading, setLoading] = useState(false);
  const [result, setResult] = useState<api.TempLoginTokenResponse | null>(null);
  const [error, setError] = useState('');
  const clearRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  // Auto-clear token from state 30s after display (FE-7).
  useEffect(() => {
    if (!result) return;
    clearRef.current = setTimeout(() => setResult(null), 30_000);
    return () => {
      if (clearRef.current) clearTimeout(clearRef.current);
    };
  }, [result]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      try {
        const list = await api.listAdminUsers();
        if (cancelled) return;
        const active = list.filter((u) => u.status === 'active');
        setUsers(active);
        if (active.length > 0 && !selectedUserID) {
          setSelectedUserID(active[0].id);
        }
      } catch (err) {
        if (cancelled) return;
        setUsersError(
          err instanceof Error ? err.message : 'Failed to load users'
        );
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [selectedUserID]);

  const handleMint = async () => {
    if (!selectedUserID) return;
    setLoading(true);
    setError('');
    setResult(null);
    try {
      const res = await api.createTempLoginToken(selectedUserID);
      setResult(res);
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to mint token');
    } finally {
      setLoading(false);
    }
  };

  const handleCopy = async () => {
    if (!result) return;
    try {
      await navigator.clipboard.writeText(result.login_url);
      toast('Login URL copied to clipboard', 'success');
      if (clearRef.current) clearTimeout(clearRef.current);
      setResult(null);
    } catch {
      toast('Could not copy — select the URL and copy manually', 'warning');
    }
  };

  const expiresMessage = useMemo(() => {
    if (!result) return '';
    const ts = new Date(result.expires_at);
    return `Single-use. Expires ${ts.toLocaleString()}.`;
  }, [result]);

  return (
    <Box sx={{ maxWidth: 720 }}>
      <Typography variant="h6" gutterBottom>
        Temp Login URL
      </Typography>
      <Typography variant="body2" color="text.secondary" gutterBottom>
        Generate a single-use URL that signs the target user in for 24 hours
        when opened. URL is valid for 15 minutes and can only be used once.
      </Typography>

      {usersError && (
        <Alert severity="error" sx={{ mt: 2 }}>
          {usersError}
        </Alert>
      )}

      <Stack spacing={2} sx={{ mt: 2 }}>
        <TextField
          select
          label="User"
          value={selectedUserID}
          onChange={(e) => setSelectedUserID(e.target.value)}
          disabled={users.length === 0 || loading}
          fullWidth
        >
          {users.map((u) => (
            <MenuItem key={u.id} value={u.id}>
              {u.username} {u.email ? `— ${u.email}` : ''}
            </MenuItem>
          ))}
        </TextField>

        <Box>
          <Button
            variant="contained"
            onClick={handleMint}
            disabled={!selectedUserID || loading}
          >
            {loading ? <CircularProgress size={20} /> : 'Generate Login URL'}
          </Button>
        </Box>

        {error && <Alert severity="error">{error}</Alert>}

        {result && (
          <Alert severity="success" sx={{ flexDirection: 'column', alignItems: 'stretch' }}>
            <Typography variant="body2" gutterBottom>
              URL for <strong>{result.user.username}</strong> — opens a {result.session_ttl_hours}h session.
            </Typography>
            <Stack direction="row" spacing={1} alignItems="center" sx={{ mt: 1 }}>
              <TextField
                value={result.login_url}
                fullWidth
                size="small"
                InputProps={{ readOnly: true }}
                onFocus={(e) => (e.target as HTMLInputElement).select()}
              />
              <Tooltip title="Copy URL">
                <IconButton onClick={handleCopy} size="small">
                  <ContentCopyIcon />
                </IconButton>
              </Tooltip>
            </Stack>
            <Typography variant="caption" color="text.secondary" sx={{ mt: 1, display: 'block' }}>
              {expiresMessage}
            </Typography>
          </Alert>
        )}
      </Stack>
    </Box>
  );
}

export default TempLoginTab;
