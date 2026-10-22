// file: web/src/components/library/PurgeSoftDeletedDialog.tsx
// version: 1.0.0
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
import {
  keptReasonsOfResult,
  outcomeHeadline,
  outcomeNotes,
  outcomeSeverity,
  previewHeadline,
} from '../../utils/purgeSummary';

export interface PurgeSoftDeletedDialogProps {
  open: boolean;
  onClose: () => void;
  /** Called once a purge (not a preview) has finished, with what it did. */
  onFinished: (result: api.PurgeResult) => void;
  /** Told when a purge starts and ends, so the page can disable row actions. */
  onRunningChange?: (running: boolean) => void;
}

type Phase = 'previewing' | 'ready' | 'running' | 'done' | 'error';

/** How many error lines the result shows before "and N more". */
const ERRORS_SHOWN = 5;

const books = (n: number) => `${n.toLocaleString()} ${n === 1 ? 'book' : 'books'}`;

function messageOf(e: unknown): string {
  return e instanceof Error && e.message ? e.message : 'Something went wrong.';
}

/**
 * The "Purge soft-deleted" dialog. It does not promise a count: on open it runs
 * a PREVIEW (a dry run of the real purge, same rules) and says how many books a
 * purge would remove now and how many it keeps, by reason. Confirming runs the
 * purge as an operation, shows its progress, and ends with the full breakdown
 * of what was purged and what was kept.
 */
export function PurgeSoftDeletedDialog({
  open,
  onClose,
  onFinished,
  onRunningChange,
}: PurgeSoftDeletedDialogProps) {
  const [phase, setPhase] = useState<Phase>('previewing');
  const [deleteFiles, setDeleteFiles] = useState(false);
  const [preview, setPreview] = useState<api.PurgeResult | null>(null);
  const [result, setResult] = useState<api.PurgeResult | null>(null);
  const [progress, setProgress] = useState<{ done: number; total: number; message: string } | null>(
    null
  );
  const [error, setError] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);
  // Held in refs so the effects below do not re-run when the page re-renders
  // with fresh callback identities.
  const onFinishedRef = useRef(onFinished);
  const onRunningChangeRef = useRef(onRunningChange);
  useEffect(() => {
    onFinishedRef.current = onFinished;
    onRunningChangeRef.current = onRunningChange;
  });

  const runPreview = useCallback(async () => {
    abortRef.current?.abort();
    const ac = new AbortController();
    abortRef.current = ac;
    setPhase('previewing');
    setPreview(null);
    setResult(null);
    setError(null);
    try {
      const handle = await api.previewPurgeSoftDeleted();
      const res = await api.awaitPurgeResult(handle.id, undefined, { signal: ac.signal });
      if (ac.signal.aborted) return;
      setPreview(res);
      setPhase('ready');
    } catch (e) {
      if (ac.signal.aborted) return;
      setError(messageOf(e));
      setPhase('error');
    }
  }, []);

  useEffect(() => {
    if (!open) return;
    setDeleteFiles(false);
    void runPreview();
    return () => abortRef.current?.abort();
  }, [open, runPreview]);

  const runPurge = async () => {
    abortRef.current?.abort();
    const ac = new AbortController();
    abortRef.current = ac;
    setPhase('running');
    setProgress(null);
    setError(null);
    onRunningChangeRef.current?.(true);
    try {
      const handle = await api.purgeSoftDeletedBooks(deleteFiles);
      const res = await api.awaitPurgeResult(handle.id, setProgress, { signal: ac.signal });
      if (ac.signal.aborted) return;
      setResult(res);
      setPhase('done');
      onFinishedRef.current(res);
    } catch (e) {
      if (ac.signal.aborted) return;
      setError(messageOf(e));
      setPhase('error');
    } finally {
      onRunningChangeRef.current?.(false);
    }
  };

  const purgeable = preview?.purgeable ?? 0;
  const running = phase === 'running';

  return (
    <Dialog open={open} onClose={onClose} maxWidth="sm" fullWidth>
      <DialogTitle>Purge Soft-Deleted Books</DialogTitle>
      <DialogContent>
        {phase === 'previewing' && (
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

        {phase === 'ready' && preview && (
          <>
            <Typography variant="body1" gutterBottom data-testid="purge-preview-headline">
              {previewHeadline(preview)}
            </Typography>
            <KeptList result={preview} lead="The rest stay in the trash:" />
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
              variant={progress && progress.total > 0 ? 'determinate' : 'indeterminate'}
              value={progress && progress.total > 0 ? (progress.done / progress.total) * 100 : 0}
            />
            <Typography variant="caption" sx={{ color: 'text.secondary' }}>
              The purge runs on the server; closing this window does not stop it.
            </Typography>
          </Box>
        )}

        {phase === 'done' && result && (
          <>
            <Alert severity={outcomeSeverity(result)} data-testid="purge-result-headline">
              {outcomeHeadline(result)}
            </Alert>
            {outcomeNotes(result).map((n) => (
              <Typography key={n} variant="body2" sx={{ mt: 1 }}>
                {n}
              </Typography>
            ))}
            <KeptList result={result} lead="Kept in the trash:" />
            {result.errors.length > 0 && (
              <Box sx={{ mt: 2 }} data-testid="purge-result-errors">
                <Typography variant="subtitle2">Details</Typography>
                {result.errors.slice(0, ERRORS_SHOWN).map((e) => (
                  <Typography key={e} variant="body2" sx={{ color: 'text.secondary' }}>
                    {e}
                  </Typography>
                ))}
                {result.errors.length > ERRORS_SHOWN && (
                  <Typography variant="body2" sx={{ color: 'text.secondary' }}>
                    and {result.errors.length - ERRORS_SHOWN} more
                    {result.truncated ? ' (the list is capped; the counts above are exact)' : ''}
                  </Typography>
                )}
              </Box>
            )}
          </>
        )}

        {phase === 'error' && (
          <Alert severity="error" data-testid="purge-error">
            {error}
          </Alert>
        )}
      </DialogContent>
      <DialogActions>
        {phase === 'error' && <Button onClick={() => void runPreview()}>Try again</Button>}
        <Button onClick={onClose}>
          {phase === 'done' ? 'Close' : running ? 'Close' : 'Cancel'}
        </Button>
        {(phase === 'ready' || running) && (
          <Button
            onClick={() => void runPurge()}
            color="error"
            variant="contained"
            disabled={running || purgeable === 0}
          >
            {running
              ? 'Purging...'
              : purgeable === 0
                ? 'Nothing to purge'
                : `Purge ${books(purgeable)}`}
          </Button>
        )}
      </DialogActions>
    </Dialog>
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
