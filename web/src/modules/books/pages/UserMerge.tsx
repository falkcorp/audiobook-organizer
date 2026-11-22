// file: web/src/modules/books/pages/UserMerge.tsx
// version: 1.0.2
// guid: 0b6d2f48-7a31-4c95-9e84-c3a1d58f7e20
// last-edited: 2026-10-23

// F9-04 slice 4: books' account-merge extension of the Users page (07 §7.11 userPageExtensions):
// the merge dialog and the merge journals panel. The footprint drawer stayed with the shell page.

import { useCallback, useEffect, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  Checkbox,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControlLabel,
  IconButton,
  MenuItem,
  Paper,
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableRow,
  TextField,
  Tooltip,
  Typography,
} from '@mui/material';
import { Refresh as RefreshIcon } from '@mui/icons-material';
import {
  applyUserMerge,
  listUserMerges,
  previewUserMerge,
  resumeUserMerge,
  undoUserMerge,
  userMergePlanFromError,
  type UserMergeCoreSummary,
  type UserMergeJournal,
  type UserMergePlan,
} from '../../../services/api';
import { IdentityList } from '../../../components/users/IdentityList';
import { errorText, familyLabel, type MergeableUser } from '../../../components/users/userAccountParts';

/**
 * What core says moves, under core identity. Core owns sign-ins and API keys, so
 * books has no identity lists to show; core's masked summary names the providers
 * of the sign-ins that move and counts the keys and allow-list rows. Key ids are
 * not shown: the count is what the admin needs.
 */
function CoreMovesSummary({
  core,
  survivorName,
}: {
  core: UserMergeCoreSummary;
  survivorName: string;
}) {
  const providers = core.identity_providers ?? [];
  const keys = core.api_key_ids ?? [];
  return (
    <Paper variant="outlined" sx={{ p: 1.5 }}>
      <Typography variant="body2">
        Sign-ins that move to {survivorName}: {providers.length > 0 ? providers.join(', ') : 'none'}
      </Typography>
      <Typography variant="body2">API keys that move: {keys.length}</Typography>
      {core.allow_list_rows > 0 && (
        <Typography variant="body2">
          Access allow-list entries that move: {core.allow_list_rows}
        </Typography>
      )}
    </Paper>
  );
}

/**
 * The merge dialog: pick the account that stays, read the preview (counts,
 * conflicts, sign-ins, who holds the owner's identity), then confirm. The
 * digest of the preview that was read is what apply sends, so a merge never
 * runs against a plan the admin did not see. The page keys it by the account
 * being merged away, so each merge starts from an empty state.
 */
export function UserMergeDialog({
  absorbed,
  candidates,
  onClose,
  onStarted,
}: {
  absorbed: MergeableUser | null;
  candidates: MergeableUser[];
  onClose: () => void;
  onStarted: (operationId: string, absorbed: MergeableUser) => void;
}) {
  const [survivorId, setSurvivorId] = useState('');
  const [plan, setPlan] = useState<UserMergePlan | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [notice, setNotice] = useState('');
  const [acknowledged, setAcknowledged] = useState(false);
  const [starting, setStarting] = useState(false);
  const absorbedId = absorbed?.id;

  const loadPreview = useCallback(
    async (target: string) => {
      if (!absorbedId || !target) return;
      setLoading(true);
      setError('');
      setNotice('');
      setPlan(null);
      setAcknowledged(false);
      try {
        setPlan(await previewUserMerge(absorbedId, target));
      } catch (err) {
        setError(errorText(err, 'Failed to preview the merge'));
      } finally {
        setLoading(false);
      }
    },
    [absorbedId]
  );

  const needsAck = !!plan && plan.owner_identity_ack_required;
  const canMerge =
    !!plan && plan.refusals.length === 0 && (!needsAck || acknowledged) && !loading && !starting;

  const confirm = async () => {
    if (!absorbed || !plan) return;
    setStarting(true);
    setError('');
    try {
      const started = await applyUserMerge(absorbed.id, plan.survivor_id, {
        digest: plan.digest,
        acknowledge_owner_identity: acknowledged,
      });
      onStarted(started.operation_id, absorbed);
    } catch (err) {
      const fresh = userMergePlanFromError(err);
      if (fresh) {
        // The accounts changed since the preview (or the merge is now refused):
        // show what is true now and make the admin read it again.
        setPlan(fresh);
        setAcknowledged(false);
        setNotice(
          fresh.refusals.length > 0
            ? 'The merge is refused; see below.'
            : 'The accounts changed since the preview. Review the updated preview and confirm again.'
        );
      } else {
        setError(errorText(err, 'Failed to start the merge'));
      }
    } finally {
      setStarting(false);
    }
  };

  const survivorName = candidates.find((c) => c.id === survivorId)?.username ?? '';

  return (
    <Dialog open={!!absorbed} onClose={onClose} maxWidth="md" fullWidth>
      <DialogTitle>{absorbed ? `Merge ${absorbed.username} into…` : 'Merge into…'}</DialogTitle>
      <DialogContent>
        <Typography variant="body2" sx={{ mb: 2 }}>
          The account you pick stays. This account is locked, and its sign-ins, API keys and
          listening data move to it. The merge can be undone from the account merges list below;
          undoing it signs the surviving account out of the web and Audiobookshelf clients when
          sign-ins or keys move back.
        </Typography>
        <TextField
          select
          fullWidth
          size="small"
          label="Account that stays"
          value={survivorId}
          onChange={(e) => {
            setSurvivorId(e.target.value);
            void loadPreview(e.target.value);
          }}
          sx={{ mb: 2 }}
        >
          {candidates.length === 0 && (
            <MenuItem value="" disabled>
              No other active admin account
            </MenuItem>
          )}
          {candidates.map((c) => (
            <MenuItem key={c.id} value={c.id}>
              {c.username}
            </MenuItem>
          ))}
        </TextField>

        {loading && <CircularProgress size={24} aria-label="Loading preview" />}
        {error && (
          <Alert severity="error" sx={{ mb: 2 }}>
            {error}
          </Alert>
        )}
        {notice && (
          <Alert severity="info" sx={{ mb: 2 }}>
            {notice}
          </Alert>
        )}

        {plan && (
          <Box>
            {plan.refusals.length > 0 && (
              <Alert severity="error" sx={{ mb: 2 }}>
                <Typography variant="subtitle2">This merge cannot run</Typography>
                <Box component="ul" sx={{ m: 0, pl: 2 }}>
                  {plan.refusals.map((r) => (
                    <li key={r}>{r}</li>
                  ))}
                </Box>
              </Alert>
            )}
            {plan.warnings.length > 0 && (
              <Alert severity="warning" sx={{ mb: 2 }}>
                <Box component="ul" sx={{ m: 0, pl: 2 }}>
                  {plan.warnings.map((w) => (
                    <li key={w}>{w}</li>
                  ))}
                </Box>
              </Alert>
            )}
            {plan.owner_identity_holder === 'no_owner_configured' && (
              <Alert severity="info" sx={{ mb: 2 }}>
                No owner email is configured, so the owner&apos;s sign-in cannot be told apart.
                Check the linked sign-ins below by hand.
              </Alert>
            )}
            {plan.owner_identity_ack_required && (
              <Alert severity="warning" sx={{ mb: 2 }}>
                <Typography variant="body2">
                  {plan.core
                    ? `A sign-in on the account being merged away moves to ${plan.survivor.username}, and this page cannot tell whether it is the owner’s. `
                    : plan.owner_identity_holder === 'both'
                      ? 'Both accounts hold a sign-in for the owner address. '
                      : plan.owner_identity_on_absorbed
                        ? 'The account being merged away holds the owner’s sign-in. '
                        : 'The account being merged away has a sign-in that cannot be checked against the owner address. '}
                  {plan.core ? '' : `After the merge its sign-ins belong to ${plan.survivor.username}.`}
                </Typography>
                <FormControlLabel
                  control={
                    <Checkbox
                      checked={acknowledged}
                      onChange={(e) => setAcknowledged(e.target.checked)}
                    />
                  }
                  label="I understand the owner’s sign-in moves to the account that stays"
                />
              </Alert>
            )}

            {plan.core ? (
              <CoreMovesSummary core={plan.core} survivorName={plan.survivor.username} />
            ) : (
              <Box
                sx={{ display: 'grid', gap: 2, gridTemplateColumns: { xs: '1fr', sm: '1fr 1fr' } }}
              >
                <Paper variant="outlined" sx={{ p: 1.5 }}>
                  <Typography variant="subtitle2">Stays: {plan.survivor.username}</Typography>
                  <IdentityList identities={plan.survivor_identities} />
                </Paper>
                <Paper variant="outlined" sx={{ p: 1.5 }}>
                  <Typography variant="subtitle2">Merged away: {plan.absorbed.username}</Typography>
                  <IdentityList identities={plan.absorbed_identities} />
                </Paper>
              </Box>
            )}

            <Table size="small" sx={{ mt: 2 }}>
              <TableHead>
                <TableRow>
                  <TableCell>Data</TableCell>
                  <TableCell align="right">Moves</TableCell>
                  <TableCell align="right">Collisions resolved</TableCell>
                </TableRow>
              </TableHead>
              <TableBody>
                {Object.entries(plan.counts).map(([family, c]) => (
                  <TableRow key={family}>
                    <TableCell>{familyLabel(family)}</TableCell>
                    <TableCell align="right">{c.rows}</TableCell>
                    <TableCell align="right">{c.conflicts}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
            {plan.email_outcome && (
              <Typography variant="body2" sx={{ mt: 1 }}>
                Email: {plan.email_outcome}
              </Typography>
            )}
            {plan.roles_gained.length > 0 && (
              <Typography variant="body2">
                Roles the surviving account gains: {plan.roles_gained.join(', ')}
              </Typography>
            )}
            <Typography variant="caption" color="text.secondary" sx={{ display: 'block', mt: 1 }}>
              {plan.ops} writes · {plan.conflict_total} collisions · preview{' '}
              {plan.digest.slice(0, 12)}
            </Typography>
          </Box>
        )}
      </DialogContent>
      <DialogActions>
        <Button onClick={onClose}>Cancel</Button>
        <Button variant="contained" color="warning" disabled={!canMerge} onClick={confirm}>
          {starting ? 'Starting…' : survivorName ? `Merge into ${survivorName}` : 'Merge'}
        </Button>
      </DialogActions>
    </Dialog>
  );
}

const STATUS_COLOR: Record<string, 'success' | 'warning' | 'default' | 'error'> = {
  applied: 'success',
  applying: 'warning',
  undoing: 'warning',
  undone: 'default',
};

/** The merge journals: what was merged, whether it finished, and resume / undo. */
export function UserMergesPanel({
  users,
  refreshKey,
  onNotice,
}: {
  users: MergeableUser[];
  /** Bumped by the page when a merge starts, so the list reloads. */
  refreshKey: number;
  onNotice: (message: string) => void;
}) {
  const [merges, setMerges] = useState<UserMergeJournal[]>([]);
  const [error, setError] = useState('');
  const [confirmUndo, setConfirmUndo] = useState<UserMergeJournal | null>(null);

  const load = useCallback(async () => {
    try {
      setMerges(await listUserMerges());
      setError('');
    } catch (err) {
      setError(errorText(err, 'Failed to list merges'));
    }
  }, []);

  useEffect(() => {
    void load();
  }, [load, refreshKey]);

  const name = (id: string) => users.find((u) => u.id === id)?.username ?? id;

  const run = async (
    action: (id: string) => Promise<{ operation_id: string }>,
    j: UserMergeJournal,
    done: string
  ) => {
    setError('');
    try {
      await action(j.id);
      onNotice(done);
      await load();
    } catch (err) {
      setError(errorText(err, 'The request failed'));
    }
  };

  if (merges.length === 0 && !error) return null;

  return (
    <Paper sx={{ p: 2, mt: 4 }}>
      <Box sx={{ display: 'flex', alignItems: 'center', justifyContent: 'space-between' }}>
        <Typography variant="h5">Account merges</Typography>
        <Tooltip title="Refresh">
          <IconButton aria-label="Refresh merges" onClick={() => void load()}>
            <RefreshIcon />
          </IconButton>
        </Tooltip>
      </Box>
      {error && (
        <Alert severity="error" sx={{ my: 1 }}>
          {error}
        </Alert>
      )}
      <Table size="small">
        <TableHead>
          <TableRow>
            <TableCell>Started</TableCell>
            <TableCell>Merged away</TableCell>
            <TableCell>Into</TableCell>
            <TableCell>Status</TableCell>
            <TableCell align="right">Actions</TableCell>
          </TableRow>
        </TableHead>
        <TableBody>
          {merges.map((j) => (
            <TableRow key={j.id}>
              <TableCell>{new Date(j.created_at).toLocaleString()}</TableCell>
              <TableCell>{name(j.absorbed_id)}</TableCell>
              <TableCell>{name(j.survivor_id)}</TableCell>
              <TableCell>
                <Chip size="small" label={j.status} color={STATUS_COLOR[j.status] ?? 'default'} />
                {j.status !== 'applied' && j.status !== 'undone' && (
                  <Typography variant="caption" sx={{ ml: 1 }}>
                    {j.cursor}/{j.ops} writes
                  </Typography>
                )}
                {(j.undo_conflicts?.length ?? 0) > 0 && (
                  <Typography variant="caption" color="warning.main" sx={{ display: 'block' }}>
                    {j.undo_conflicts?.length} row(s) changed since the merge were left as they are
                  </Typography>
                )}
                {j.error && (
                  <Typography variant="caption" color="error" sx={{ display: 'block' }}>
                    Stopped: see the operation log, then resume
                  </Typography>
                )}
              </TableCell>
              <TableCell align="right">
                {j.status === 'applying' && (
                  <Button
                    size="small"
                    onClick={() =>
                      void run(resumeUserMerge, j, `Resuming the merge of ${name(j.absorbed_id)}.`)
                    }
                  >
                    Resume
                  </Button>
                )}
                {j.status !== 'undone' && (
                  <Button size="small" color="warning" onClick={() => setConfirmUndo(j)}>
                    Undo
                  </Button>
                )}
              </TableCell>
            </TableRow>
          ))}
        </TableBody>
      </Table>

      <Dialog open={!!confirmUndo} onClose={() => setConfirmUndo(null)}>
        <DialogTitle>Undo this merge?</DialogTitle>
        <DialogContent>
          <Typography variant="body2">
            The merged-away account gets its sign-ins, keys and data back. Anything either account
            changed since the merge is left as it is and reported. If sign-ins or keys move back,
            the account that stayed is signed out of the web and Audiobookshelf clients.
          </Typography>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirmUndo(null)}>Cancel</Button>
          <Button
            color="warning"
            variant="contained"
            onClick={() => {
              const j = confirmUndo;
              setConfirmUndo(null);
              if (j) void run(undoUserMerge, j, `Undoing the merge of ${name(j.absorbed_id)}.`);
            }}
          >
            Undo merge
          </Button>
        </DialogActions>
      </Dialog>
    </Paper>
  );
}
