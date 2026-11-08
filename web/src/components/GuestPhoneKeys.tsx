// file: web/src/components/GuestPhoneKeys.tsx
// version: 1.1.0
// guid: c4f8a2d6-93b1-4e07-8a5d-1e6b7c9d3f24
// last-edited: 2026-11-07

// Admin panel on the Users page: each guest's own Cloudflare phone-app key
// (service token), with Revoke and Issue / Reissue actions. Issue and Reissue
// email the key to the user's verified address; the server never returns the
// secret to the admin (the guest saw it once on the invitation page, or gets
// it in the email). Users who could be given a first key but have none are
// listed too, so the admin can click Issue. Renders nothing when the feature is
// off and no key exists.

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
import { ApiTimeoutError, apiFetch } from '../utils/apiFetch';
import { describeRequestError, responseErrorMessage, unwrapData } from '../utils/apiResponse';
import {
  phoneKeyIssueVerb,
  phoneKeyReissuable,
  phoneKeyRevocable,
  phoneKeyStatusLabel,
  type GuestPhoneKey,
  type GuestPhoneKeysBody,
} from './phoneKeyLabels';

const ENDPOINT = '/api/v1/admin/guest-service-tokens';

// Cloudflare cuts a proxied request off at 100 s; the server answers 202 at 85 s.
const ISSUE_TIMEOUT_MS = 100_000;

const STILL_RUNNING = 'Still working; the list will show the result.';

type Notice = { severity: 'success' | 'info'; text: string };

export default function GuestPhoneKeys({ refreshToken }: { refreshToken?: unknown }) {
  const [body, setBody] = useState<GuestPhoneKeysBody | null>(null);
  const [loadFailed, setLoadFailed] = useState(false);
  const [revoking, setRevoking] = useState<GuestPhoneKey | null>(null);
  const [revokeError, setRevokeError] = useState('');
  const [busy, setBusy] = useState(false);
  const [notice, setNotice] = useState<Notice | null>(null);
  const [issuing, setIssuing] = useState<GuestPhoneKey | null>(null);
  const [issueError, setIssueError] = useState('');
  const [issueBusy, setIssueBusy] = useState(false);

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
      setNotice({
        severity: 'success',
        text: `Phone-app key revoked for ${revoking.username || revoking.user_id}.`,
      });
      setRevoking(null);
      await load();
    } catch (err) {
      setRevokeError(describeRequestError(err, 'Could not revoke the phone-app key'));
    } finally {
      setBusy(false);
    }
  };

  const confirmIssue = async () => {
    if (!issuing) return;
    const target = issuing;
    const name = target.username || target.user_id;
    const first = target.status === 'none';
    setIssueBusy(true);
    setIssueError('');
    try {
      const resp = await apiFetch(`${ENDPOINT}/${encodeURIComponent(target.user_id)}/reissue`, {
        method: 'POST',
        timeoutMs: ISSUE_TIMEOUT_MS,
      });
      if (!resp.ok) {
        // The old key may already be revoked, so show the reason and reload.
        setIssueError(await responseErrorMessage(resp));
        await load();
        return;
      }
      if (resp.status === 202) {
        setNotice({ severity: 'info', text: STILL_RUNNING });
      } else {
        setNotice({
          severity: 'success',
          text: first
            ? `A phone-app key was emailed to ${name}.`
            : `A new phone-app key was emailed to ${name}. Their old key no longer works.`,
        });
      }
      setIssuing(null);
      await load();
    } catch (err) {
      if (err instanceof ApiTimeoutError) {
        // The work continues on the server; the list shows how it ended.
        setNotice({ severity: 'info', text: STILL_RUNNING });
        setIssuing(null);
      } else {
        setIssueError(describeRequestError(err, 'Could not issue the phone-app key'));
      }
      await load();
    } finally {
      setIssueBusy(false);
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
        guest, immediately. Issue or Reissue emails a key to the guest&apos;s verified address; you
        never see it.
      </Typography>
      {body.flag_on && !body.enabled && (
        <Alert severity="warning" sx={{ mb: 1 }}>
          Per-guest phone-app keys are switched on, but the Cloudflare settings they need are
          incomplete or wrong (CF_ACCOUNT_ID, CF_POLICY_API_TOKEN and CF_SERVICE_AUTH_POLICY_ID,
          which must not be the same policy as CF_GUESTS_POLICY_ID). New guests will not get a key
          until they are fixed.
        </Alert>
      )}
      {notice && (
        <Alert severity={notice.severity} sx={{ mb: 1 }} onClose={() => setNotice(null)}>
          {notice.text}
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
                const name = t.username || t.user_id;
                const verb = phoneKeyIssueVerb(t.status);
                return (
                  <TableRow key={t.user_id}>
                    <TableCell>{t.username || t.user_id}</TableCell>
                    <TableCell>
                      <Tooltip title={t.detail || t.revoke_reason || ''}>
                        <Chip size="small" label={s.label} color={s.color} />
                      </Tooltip>
                    </TableCell>
                    <TableCell sx={{ fontFamily: 'monospace' }}>{t.token_id || '—'}</TableCell>
                    <TableCell>
                      {t.created_at ? new Date(t.created_at).toLocaleString() : '—'}
                      {t.reissued_at && (
                        <Typography
                          variant="caption"
                          color="text.secondary"
                          sx={{ display: 'block' }}
                        >
                          Reissued {new Date(t.reissued_at).toLocaleString()}
                        </Typography>
                      )}
                    </TableCell>
                    <TableCell>
                      {t.last_seen_at ? new Date(t.last_seen_at).toLocaleString() : 'Not reported'}
                    </TableCell>
                    <TableCell align="right">
                      {phoneKeyReissuable(t.status) && (
                        <Tooltip title={t.reissue_blocked || ''}>
                          {/* A disabled button swallows hover; the span carries the tooltip. */}
                          <span>
                            <Button
                              size="small"
                              disabled={!!t.reissue_blocked}
                              aria-label={`${verb} phone-app key for ${name}`}
                              onClick={() => {
                                setIssueError('');
                                setIssuing(t);
                              }}
                            >
                              {verb} key
                            </Button>
                          </span>
                        </Tooltip>
                      )}
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
            Their phone app stops working right away. They can still use the website. To give them a
            new key, use Reissue.
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
      <Dialog open={!!issuing} onClose={() => !issueBusy && setIssuing(null)}>
        <DialogTitle>{issuing?.status === 'none' ? 'Issue' : 'Reissue'} phone-app key</DialogTitle>
        <DialogContent>
          {issuing?.status === 'none' ? (
            <Typography>
              Issue a phone-app key for <strong>{issuing.username || issuing.user_id}</strong>? A
              key is emailed to their verified address; you will not see it. They will need to put
              the key into their phone app.
            </Typography>
          ) : (
            <Typography>
              Reissue the phone-app key for <strong>{issuing?.username || issuing?.user_id}</strong>
              ? Their current key stops working right away. A new key is emailed to their verified
              address; you will not see it. They will need to put the new key into their phone app.
            </Typography>
          )}
          {issueError && (
            <Alert severity="error" sx={{ mt: 2 }}>
              {issueError}
            </Alert>
          )}
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setIssuing(null)} disabled={issueBusy}>
            Cancel
          </Button>
          <Button variant="contained" onClick={confirmIssue} disabled={issueBusy}>
            {issuing?.status === 'none'
              ? issueBusy
                ? 'Issuing…'
                : 'Issue and email'
              : issueBusy
                ? 'Reissuing…'
                : 'Reissue and email'}
          </Button>
        </DialogActions>
      </Dialog>
    </Box>
  );
}
