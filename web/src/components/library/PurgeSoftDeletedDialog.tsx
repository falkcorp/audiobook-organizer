// file: web/src/components/library/PurgeSoftDeletedDialog.tsx
// version: 1.2.0
// guid: 9d4e7b21-6f38-4a05-8c12-0b5a3e9d7f46
// last-edited: 2026-10-22

import { useCallback, useEffect, useRef, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  Checkbox,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogTitle,
  FormControlLabel,
  LinearProgress,
  Typography,
} from '@mui/material';
import * as api from '../../services/api';
import type { PurgeRunController } from '../../hooks/usePurgeRun';
import {
  errorDetails,
  hasDetails,
  keptReasonsOfResult,
  outcomeHeadline,
  outcomeNotes,
  outcomeSeverity,
  previewHeadline,
  previewNotes,
  progressPercent,
} from '../../utils/purgeSummary';

export interface PurgeSoftDeletedDialogProps {
  open: boolean;
  onClose: () => void;
  /**
   * The purge run, owned by the page (usePurgeRun) so it outlives this dialog:
   * closing the dialog mid-run does not stop the polling or lose the outcome.
   */
  run: PurgeRunController;
}

/** The preview's own phases; the run's phases come from `run.state`. */
type PreviewPhase = 'previewing' | 'ready' | 'error';

const books = (n: number) => `${n.toLocaleString()} ${n === 1 ? 'book' : 'books'}`;

function messageOf(e: unknown): string {
  return e instanceof Error && e.message ? e.message : 'Something went wrong.';
}

/**
 * The "Purge soft-deleted" dialog. It does not promise a count: on open it runs
 * a PREVIEW (a dry run of the real purge, same rules) and says how many books a
 * purge would remove now and how many it keeps, by reason. Confirming starts
 * the purge as an operation through `run`, shows its progress, and ends with
 * the full breakdown of what was purged and what was kept. Reopening the
 * dialog while a purge runs shows the running state, not a new preview.
 */
export function PurgeSoftDeletedDialog({ open, onClose, run }: PurgeSoftDeletedDialogProps) {
  const [phase, setPhase] = useState<PreviewPhase>('previewing');
  const [deleteFiles, setDeleteFiles] = useState(false);
  const [preview, setPreview] = useState<api.PurgeResult | null>(null);
  const [previewError, setPreviewError] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);
  // Set by the Purge button: the dialog closes as soon as the server has
  // queued the purge (progress then lives in the background operation), but
  // stays open when the server refuses, so its reason is shown here.
  const closeOnStartRef = useRef(false);
  const runRef = useRef(run);
  useEffect(() => {
    runRef.current = run;
  });

  const runPreview = useCallback(async () => {
    abortRef.current?.abort();
    const ac = new AbortController();
    abortRef.current = ac;
    setPhase('previewing');
    setPreview(null);
    setPreviewError(null);
    try {
      const handle = await api.previewPurgeSoftDeleted();
      const res = await api.awaitPurgeResult(handle.id, undefined, { signal: ac.signal });
      if (ac.signal.aborted) return;
      setPreview(res);
      setPhase('ready');
    } catch (e) {
      if (ac.signal.aborted) return;
      setPreviewError(messageOf(e));
      setPhase('error');
    }
  }, []);

  useEffect(() => {
    if (!open) return;
    setDeleteFiles(false);
    // A purge started earlier is still running: show it, do not preview.
    if (runRef.current.running) return;
    runRef.current.reset();
    void runPreview();
    return () => abortRef.current?.abort();
  }, [open, runPreview]);

  const runPhase = run.state.phase;
  const running = runPhase === 'running';
  const queuedId = run.state.operationId;
  useEffect(() => {
    if (!closeOnStartRef.current) return;
    if (runPhase === 'error') {
      closeOnStartRef.current = false;
    } else if (queuedId) {
      closeOnStartRef.current = false;
      onClose();
    }
  }, [runPhase, queuedId, onClose]);
  const purgeable = preview?.purgeable ?? 0;
  const progress = run.state.progress;
  const percent = progressPercent(progress);
  const errorText = runPhase === 'error' ? run.state.error : previewError;
  const showError = runPhase === 'error' || (runPhase === 'idle' && phase === 'error');
  const showPreview = runPhase === 'idle';

  const tryAgain = () => {
    run.reset();
    void runPreview();
  };

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>Purge Soft-Deleted Books</DialogTitle>
      <DialogContent>
        {showPreview && phase === 'previewing' && (
          <Box
            data-testid="purge-previewing"
            sx={{ display: 'flex', alignItems: 'center', gap: 2, py: 1 }}
          >
            <CircularProgress size={20} />
            <Typography variant="body2">
              Checking what a purge would remove. Nothing is changed by this check.
            </Typography>
          </Box>
        )}

        {showPreview && phase === 'ready' && preview && (
          <>
            <Typography
              variant="body1"
              gutterBottom
              data-testid="purge-preview-headline"
              sx={preview.stopped_early ? { color: 'warning.main' } : undefined}
            >
              {previewHeadline(preview)}
            </Typography>
            <KeptList result={preview} lead="The rest stay in the trash:" />
            {previewNotes(preview).map((n) => (
              <Typography key={n} variant="body2" sx={{ mt: 1 }} data-testid="purge-preview-note">
                {n}
              </Typography>
            ))}
            <ErrorDetails result={preview} />
            {purgeable > 0 && (
              <>
                <FormControlLabel
                  control={
                    <Checkbox
                      checked={deleteFiles}
                      onChange={(e) => setDeleteFiles(e.target.checked)}
                    />
                  }
                  label="Also delete files from disk (if they still exist)"
                />
                <Alert severity="warning" sx={{ mt: 2 }}>
                  This cannot be undone. The {books(purgeable)} above are removed for good
                  {deleteFiles ? ', and their files are deleted from disk' : ''}.
                </Alert>
              </>
            )}
          </>
        )}

        {running && (
          <Box data-testid="purge-progress">
            <Typography variant="body2" gutterBottom>
              {progress?.message || 'Starting the purge...'}
            </Typography>
            <LinearProgress
              variant={percent !== null ? 'determinate' : 'indeterminate'}
              value={percent ?? 0}
            />
            <Typography variant="caption" sx={{ color: 'text.secondary' }}>
              The purge runs on the server; closing this window does not stop it. You will get a
              notification when it ends.
            </Typography>
          </Box>
        )}

        {runPhase === 'done' && run.state.result && (
          <>
            <Alert severity={outcomeSeverity(run.state.result)} data-testid="purge-result-headline">
              {outcomeHeadline(run.state.result)}
            </Alert>
            {outcomeNotes(run.state.result).map((n) => (
              <Typography key={n} variant="body2" sx={{ mt: 1 }}>
                {n}
              </Typography>
            ))}
            <KeptList result={run.state.result} lead="Kept in the trash:" />
            <ErrorDetails result={run.state.result} />
          </>
        )}

        {showError && (
          <Alert severity="error" data-testid="purge-error">
            {errorText}
          </Alert>
        )}
      </DialogContent>
      <DialogActions>
        {showError && <Button onClick={tryAgain}>Try again</Button>}
        <Button onClick={onClose}>{runPhase === 'done' || running ? 'Close' : 'Cancel'}</Button>
        {showPreview && phase === 'ready' && (
          <Button
            onClick={() => {
              closeOnStartRef.current = true;
              void run.start(deleteFiles);
            }}
            color="error"
            variant="contained"
            disabled={purgeable === 0}
          >
            {purgeable === 0 ? 'Nothing to purge' : `Purge ${books(purgeable)}`}
          </Button>
        )}
        {running && (
          <Button color="error" variant="contained" disabled>
            Purging...
          </Button>
        )}
      </DialogActions>
    </Dialog>
  );
}

/** Error lines of a result, capped at five with "and N more". */
function ErrorDetails({ result }: { result: api.PurgeResult }) {
  if (!hasDetails(result)) return null;
  const { shown, more, noDetails } = errorDetails(result);
  return (
    <Box sx={{ mt: 2 }} data-testid="purge-result-errors">
      <Typography variant="subtitle2">Details</Typography>
      {noDetails && (
        <Typography variant="body2" sx={{ color: 'text.secondary' }}>
          No details were recorded for the books that could not be decided.
        </Typography>
      )}
      {shown.map((e) => (
        <Typography key={e} variant="body2" sx={{ color: 'text.secondary' }}>
          {e}
        </Typography>
      ))}
      {more > 0 && (
        <Typography variant="body2" sx={{ color: 'text.secondary' }}>
          and {more} more
          {result.truncated ? ' (the list is capped; the counts above are exact)' : ''}
        </Typography>
      )}
    </Box>
  );
}

function KeptList({ result, lead }: { result: api.PurgeResult; lead: string }) {
  const reasons = keptReasonsOfResult(result);
  if (reasons.length === 0) return null;
  return (
    <Box sx={{ mt: 1 }} data-testid="purge-kept-list">
      <Typography variant="body2">{lead}</Typography>
      <Box component="ul" sx={{ mt: 0.5, mb: 1, pl: 3 }}>
        {reasons.map((r) => (
          <li key={r.key} data-testid={`purge-kept-${r.key}`}>
            <Typography variant="body2">
              <strong>{r.count.toLocaleString()}</strong> {r.label}
            </Typography>
          </li>
        ))}
      </Box>
    </Box>
  );
}
