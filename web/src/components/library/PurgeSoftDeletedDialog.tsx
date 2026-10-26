// file: web/src/components/library/PurgeSoftDeletedDialog.tsx
// version: 1.8.0
// guid: 9d4e7b21-6f38-4a05-8c12-0b5a3e9d7f46
// last-edited: 2026-10-26

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
  previewFiles,
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
 * purge would remove now and how many it keeps, by reason, and with "also delete
 * files" ticked which files it would remove from disk and which stay. Confirming
 * starts the purge as an operation through `run`, bound to that preview (it
 * carries the preview's trashed_before, so it takes no book trashed since), shows its progress, and ends with
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

  // The preview is made with the same delete-files choice the purge will run
  // with, so what it lists as purgeable is what the run will purge: a book whose
  // file cannot be decided is kept by a delete-files run, and only a delete-files
  // preview says so.
  const runPreview = useCallback(async (withFiles: boolean) => {
    abortRef.current?.abort();
    const ac = new AbortController();
    abortRef.current = ac;
    setPhase('previewing');
    setPreview(null);
    setPreviewError(null);
    try {
      const handle = await api.previewPurgeSoftDeleted(withFiles);
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
    void runPreview(false);
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
  // Files an earlier delete-files purge left waiting (it died before its
  // removal). With nothing purgeable the delete-files box is shown only while
  // it is ticked, so without its own button nothing in the product would ever drain them: the
  // files would sit on disk, named by no book, until a library scan imported
  // them as new books. The run is the ordinary purge with delete files on,
  // bound to this preview; it purges no book here (none is purgeable) and
  // replays the pending removals.
  const earlierFiles = preview?.files_pending_from_earlier_pass ?? 0;
  const earlierOnly = purgeable === 0 && earlierFiles > 0;
  // The purge is bound to this preview: it carries the instant the preview was
  // taken at and takes no book trashed after it. A preview without one cannot
  // be purged from.
  const trashedBefore = preview?.trashed_before ?? null;
  const files = preview ? previewFiles(preview) : null;
  const progress = run.state.progress;
  const percent = progressPercent(progress);
  const errorText = runPhase === 'error' ? run.state.error : previewError;
  const showError = runPhase === 'error' || (runPhase === 'idle' && phase === 'error');
  const showPreview = runPhase === 'idle';

  const tryAgain = () => {
    run.reset();
    void runPreview(deleteFiles);
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
            {earlierOnly && (
              <Alert severity="warning" sx={{ mt: 2 }} data-testid="purge-earlier-only">
                {earlierFiles.toLocaleString()} {earlierFiles === 1 ? 'file' : 'files'} of books an
                earlier purge removed {earlierFiles === 1 ? 'is' : 'are'} still waiting to be
                deleted from disk: that purge stopped before it got to{' '}
                {earlierFiles === 1 ? 'it' : 'them'}. Finishing deletes{' '}
                {earlierFiles === 1 ? 'it' : 'them'} unless a book names{' '}
                {earlierFiles === 1 ? 'it' : 'them'} again, or the file is no longer the one that
                purge decided on. No book is purged.
                {trashedBefore === null
                  ? ' The server did not say which books this check covered, so this is off; try again.'
                  : ''}
              </Alert>
            )}
            {/* Shown while the box is ticked even when the preview with files on
                purges nothing: hiding it then would leave no way to untick it and
                see the preview without it. */}
            {(purgeable > 0 || deleteFiles) && (
              <FormControlLabel
                control={
                  <Checkbox
                    checked={deleteFiles}
                    onChange={(e) => {
                      setDeleteFiles(e.target.checked);
                      void runPreview(e.target.checked);
                    }}
                  />
                }
                label="Also delete files from disk (if they still exist)"
              />
            )}
            {purgeable > 0 && (
              <>
                <Alert severity="warning" sx={{ mt: 2 }}>
                  This cannot be undone. The {books(purgeable)} above are removed for good
                  {deleteFiles ? ', and their files are deleted from disk' : ''}.
                  {trashedBefore === null
                    ? ' The server did not say which books this check covered, so the purge is off; try again.'
                    : ' Books put in the trash after this check are left alone.'}
                </Alert>
                {deleteFiles && files && (
                  <Box sx={{ mt: 1 }} data-testid="purge-files-preview">
                    <Typography variant="body2">
                      {files.count === 0
                        ? 'No file would be deleted from disk.'
                        : `${files.count.toLocaleString()} ${files.count === 1 ? 'file' : 'files'} would be deleted from disk:`}
                      {files.kept > 0
                        ? ` ${files.kept.toLocaleString()} ${files.kept === 1 ? 'file stays' : 'files stay'} where ${files.kept === 1 ? 'it is' : 'they are'} (outside the library folder, protected, or also named by another book, including one that stays in the trash).`
                        : ''}
                    </Typography>
                    <Box component="ul" sx={{ mt: 0.5, mb: 0, pl: 3 }}>
                      {files.shown.map((p) => (
                        <li key={p}>
                          <Typography
                            variant="body2"
                            sx={{ color: 'text.secondary', wordBreak: 'break-all' }}
                          >
                            {p}
                          </Typography>
                        </li>
                      ))}
                      {files.more > 0 && (
                        <li>
                          <Typography variant="body2" sx={{ color: 'text.secondary' }}>
                            and {files.more.toLocaleString()} more
                          </Typography>
                        </li>
                      )}
                    </Box>
                    {files.earlier > 0 && (
                      <Typography variant="body2" sx={{ mt: 1 }} data-testid="purge-files-earlier">
                        {files.earlier.toLocaleString()} {files.earlier === 1 ? 'file' : 'files'} of
                        books an earlier purge removed {files.earlier === 1 ? 'is' : 'are'} also
                        waiting to be deleted: that purge stopped before it got to{' '}
                        {files.earlier === 1 ? 'it' : 'them'}.{' '}
                        {files.earlier === 1 ? 'It is' : 'They are'} deleted too, unless a book
                        names {files.earlier === 1 ? 'it' : 'them'} again.
                      </Typography>
                    )}
                  </Box>
                )}
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
            {run.state.stillChecking && (
              <Alert severity="warning" sx={{ mt: 1, mb: 1 }} data-testid="purge-still-checking">
                Still checking: the server could not be reached. The purge may still be running, so
                this keeps trying instead of treating it as failed.
              </Alert>
            )}
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
              if (trashedBefore === null) return;
              closeOnStartRef.current = true;
              void run.start(trashedBefore, earlierOnly ? true : deleteFiles);
            }}
            color="error"
            variant="contained"
            disabled={(purgeable === 0 && !earlierOnly) || trashedBefore === null}
          >
            {earlierOnly
              ? `Finish removing ${earlierFiles.toLocaleString()} ${earlierFiles === 1 ? 'file' : 'files'}`
              : purgeable === 0
                ? 'Nothing to purge'
                : `Purge ${books(purgeable)}`}
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
