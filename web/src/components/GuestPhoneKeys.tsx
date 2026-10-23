// file: web/src/components/GuestPhoneKeys.tsx
// version: 1.0.0
// guid: c4f8a2d6-93b1-4e07-8a5d-1e6b7c9d3f24
// last-edited: 2026-10-23

// Admin panel on the Users page: each guest's own Cloudflare phone-app key
// (service token), with a Revoke action. The server never returns the secret;
// the guest saw it once on the invitation page. Renders nothing when the
// feature is off and no key exists.

import { useCallback, useEffect, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  Chip,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableContainer,
  TableHead,
  TableRow,
  Tooltip,
  Typography,
} from '@mui/material';
import { apiFetch } from '../utils/apiFetch';
import { describeRequestError, responseErrorMessage, unwrapData } from '../utils/apiResponse';
import {
  phoneKeyRevocable,
  phoneKeyStatusLabel,
  type GuestPhoneKey,
  type GuestPhoneKeysBody,
} from './phoneKeyLabels';

const ENDPOINT = '/api/v1/admin/guest-service-tokens';

export default function GuestPhoneKeys({ refreshToken }: { refreshToken?: unknown }) {
  const [body, setBody] = useState<GuestPhoneKeysBody | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [revoking, setRevoking] = useState<GuestPhoneKey | null>(null);
  const [revokeError, setRevokeError] = useState('');
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState('');

  const load = useCallback(async () => {
    try {
      const resp = await apiFetch(ENDPOINT);
      if (!resp.ok) throw new Error(await responseErrorMessage(resp));
      setBody(unwrapData<GuestPhoneKeysBody>(await resp.json()) || {});
      setLoadFailed(false);
    } catch {
      setLoadFailed(true);
    }
  }, []);

  // Reload whenever the page reloads its user list: deactivating a guest
  // revokes their key in the background.
  useEffect(() => {
    load();
  }, [load, refreshToken]);

  const confirmRevoke = async () => {
    if (!revoking) return;
    setBusy(true);
    setRevokeError('');
    try {
      const resp = await apiFetch(`${ENDPOINT}/${encodeURIComponent(revoking.user_id)}/revoke`, {
        method: 'POST',
      });
      if (!resp.ok) {
        setRevokeError(await responseErrorMessage(resp));
        return;
      }
      setNotice(`Phone-app key revoked for ${revoking.username || revoking.user_id}.`);
      setRevoking(null);
      await load();
    } catch (err) {
      setRevokeError(describeRequestError(err, 'Could not revoke the phone-app key'));
    } finally {
      setBusy(false);
    }
  };

  if (loadFailed) {
    return (
      <Alert
        severity="warning"
        sx={{ mt: 4 }}
        action={
          <Button color="inherit" size="small" onClick={load}>
            Retry
          </Button>
        }
      >
        Phone-app keys could not be loaded.
      </Alert>
    );
  }
  const tokens = body?.tokens ?? [];
  if (!body || (!body.flag_on && tokens.length === 0)) return null;

  return (
    <Box sx={{ mt: 4 }}>
      <Typography variant="h5" sx={{ mb: 1 }}>
        Phone app keys
      </Typography>
      <Typography variant="body2" color="text.secondary" sx={{ mb: 1 }}>
        Each guest on the phone app has their own Cloudflare key. Revoking one cuts off only that
        guest, immediately.
      </Typography>
      {body.flag_on && !body.enabled && (
        <Alert severity="warning" sx={{ mb: 1 }}>
          Per-guest phone-app keys are switched on, but the Cloudflare settings they need are
          incomplete (CF_ACCOUNT_ID, CF_POLICY_API_TOKEN and CF_SERVICE_AUTH_POLICY_ID). New guests
          will not get a key until they are set.
        </Alert>
      )}
      {notice && (
        <Alert severity="success" sx={{ mb: 1 }} onClose={() => setNotice('')}>
          {notice}
        </Alert>
      )}
      {tokens.length > 0 && (
        <TableContainer component={Paper}>
          <Table size="small">
            <TableHead>
              <TableRow>
                <TableCell>Guest</TableCell>
                <TableCell>Status</TableCell>
                <TableCell>Token id</TableCell>
                <TableCell>Created</TableCell>
                <TableCell>Last seen</TableCell>
                <TableCell align="right">Actions</TableCell>
              </TableRow>
            </TableHead>
            <TableBody>
              {tokens.map((t) => {
                const s = phoneKeyStatusLabel(t.status);
                return (
                  <TableRow key={t.user_id}>
                    <TableCell>{t.username || t.user_id}</TableCell>
                    <TableCell>
                      <Tooltip title={t.detail || t.revoke_reason || ''}>
                        <Chip size="small" label={s.label} color={s.color} />
                      </Tooltip>
                    </TableCell>
                    <TableCell sx={{ fontFamily: 'monospace' }}>{t.token_id || '—'}</TableCell>
                    <TableCell>{new Date(t.created_at).toLocaleString()}</TableCell>
                    <TableCell>
                      {t.last_seen_at ? new Date(t.last_seen_at).toLocaleString() : 'Not reported'}
                    </TableCell>
                    <TableCell align="right">
                      {phoneKeyRevocable(t.status) && (
                        <Button
                          size="small"
                          color="warning"
                          aria-label={`Revoke phone-app key for ${t.username || t.user_id}`}
                          onClick={() => {
                            setRevokeError('');
                            setRevoking(t);
                          }}
                        >
                          Revoke
                        </Button>
                      )}
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        </TableContainer>
      )}
      {body.last_seen_note && (
        <Typography variant="caption" color="text.secondary">
          {body.last_seen_note}
        </Typography>
      )}
      <Dialog open={!!revoking} onClose={() => !busy && setRevoking(null)}>
        <DialogTitle>Revoke phone-app key</DialogTitle>
        <DialogContent>
          <Typography>
            Revoke the phone-app key for <strong>{revoking?.username || revoking?.user_id}</strong>?
            Their phone app stops working right away. They can still use the website. A new key has
            to be issued separately.
          </Typography>
          {revokeError && (
            <Alert severity="error" sx={{ mt: 2 }}>
              {revokeError}
            </Alert>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setRevoking(null)} disabled={busy}>
            Cancel
          </Button>
          <Button color="warning" variant="contained" onClick={confirmRevoke} disabled={busy}>
            Revoke
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}
