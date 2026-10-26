// file: web/src/components/library/PurgeSoftDeletedDialog.tsx
// version: 1.9.0
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
  /**
   * Called after the owner forgot the records of file removals that were
   * waiting for a folder (the page refreshes the trash counts behind its
   * "Purge Deleted" button).
   */
  onFileRecordsForgotten?: () => void;
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
export function PurgeSoftDeletedDialog({
  open,
  onClose,
  run,
  onFileRecordsForgotten,
}: PurgeSoftDeletedDialogProps) {
  const [phase, setPhase] = useState<PreviewPhase>('previewing');
  const [deleteFiles, setDeleteFiles] = useState(false);
  const [preview, setPreview] = useState<api.PurgeResult | null>(null);
  const [previewError, setPreviewError] = useState<string | null>(null);
  const abortRef = useRef<AbortController | null>(null);
  // The preview operation the server is running for the latest check, until its
  // result arrives. A check that is replaced, closed or whose polling failed is
  // cancelled on the server, so abandoned previews do not pile up behind it
  // (each one walks the whole trash).
  const previewOpRef = useRef<string | null>(null);
  // "Forget these records": the owner's two-step choice and its outcome. The
  // outcome outlives the re-run preview that follows it.
  const [forgetStage, setForgetStage] = useState<'idle' | 'confirm' | 'working'>('idle');
  const [forgetError, setForgetError] = useState<string | null>(null);
  const [forgetNote, setForgetNote] = useState<string | null>(null);
  const onForgottenRef = useRef(onFileRecordsForgotten);
  // Set by the Purge button: the dialog closes as soon as the server has
  // queued the purge (progress then lives in the background operation), but
  // stays open when the server refuses, so its reason is shown here.
  const closeOnStartRef = useRef(false);
  const runRef = useRef(run);
  useEffect(() => {
    runRef.current = run;
    onForgottenRef.current = onFileRecordsForgotten;
  });

  const cancelPreviewOp = useCallback(() => {
    const id = previewOpRef.current;
    previewOpRef.current = null;
    if (id) void api.cancelOperation(id).catch(() => undefined);
  }, []);

  // The preview is made with the same delete-files choice the purge will run
  // with, so what it lists as purgeable is what the run will purge: a book whose
  // file cannot be decided is kept by a delete-files run, and only a delete-files
  // preview says so.
  const runPreview = useCallback(
    async (withFiles: boolean) => {
      abortRef.current?.abort();
      cancelPreviewOp();
      const ac = new AbortController();
      abortRef.current = ac;
      setPhase('previewing');
      setPreview(null);
      setPreviewError(null);
      let opId: string | null = null;
      try {
        const handle = await api.previewPurgeSoftDeleted(withFiles);
        opId = handle.id;
        if (ac.signal.aborted) {
          // Replaced or closed while the server was queueing it.
          void api.cancelOperation(opId).catch(() => undefined);
          return;
        }
        previewOpRef.current = opId;
        const res = await api.awaitPurgeResult(opId, undefined, { signal: ac.signal });
        if (ac.signal.aborted) return;
        // Finished: nothing left to cancel.
        if (previewOpRef.current === opId) previewOpRef.current = null;
        setPreview(res);
        setPhase('ready');
      } catch (e) {
        if (ac.signal.aborted) return;
        // Polling failed, not the check itself: stop the check the server may
        // still be running, since this dialog will never read its result.
        if (opId !== null && previewOpRef.current === opId) cancelPreviewOp();
        setPreviewError(messageOf(e));
        setPhase('error');
      }
    },
    [cancelPreviewOp]
  );

  useEffect(() => {
    if (!open) return;
    setDeleteFiles(false);
    setForgetStage('idle');
    setForgetError(null);
    setForgetNote(null);
    // A purge started earlier is still running: show it, do not preview.
    if (runRef.current.running) return;
    runRef.current.reset();
    void runPreview(false);
    return () => {
      abortRef.current?.abort();
      cancelPreviewOp();
    };
  }, [open, runPreview, cancelPreviewOp]);

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
  // Earlier-pass removals whose file is not there and whose folder is missing or
  // empty. They are NOT in earlierFiles: a purge cannot check them and removes
  // nothing for them, so "Finish removing" must never be offered for them alone.
  const waitingFiles = preview?.files_waiting_for_folder ?? 0;
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

  const forgetRecords = async () => {
    setForgetStage('working');
    setForgetError(null);
    try {
      const res = await api.forgetWaitingFileRecords();
      const n = res.records_forgotten;
      setForgetNote(
        `Forgot ${n.toLocaleString()} ${n === 1 ? 'record' : 'records'} of ${res.paths_forgotten.toLocaleString()} ${res.paths_forgotten === 1 ? 'file' : 'files'}. No file and no book was touched.` +
          (res.warnings && res.warnings.length > 0 ? ` ${res.warnings.join(' ')}` : '')
      );
      setForgetStage('idle');
      onForgottenRef.current?.();
      void runPreview(deleteFiles);
    } catch (e) {
      // Nothing was deleted when the server refuses (403, 503): say so, keep
      // the control so the owner can try again or leave the records alone.
      setForgetError(`${messageOf(e)} The records were not forgotten.`);
      setForgetStage('confirm');
    }
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
            {waitingFiles > 0 && (
              <Alert severity="warning" sx={{ mt: 2 }} data-testid="purge-waiting-for-folder">
                {waitingFiles.toLocaleString()} {waitingFiles === 1 ? 'file' : 'files'} of books an
                earlier purge removed could not be checked: {waitingFiles === 1 ? 'its' : 'their'}{' '}
                folder is missing or empty, which is also what a drive that is not mounted looks
                like. A purge does not delete {waitingFiles === 1 ? 'it' : 'them'}, and &quot;Finish
                removing&quot; does not either. The server keeps a record for{' '}
                {waitingFiles === 1 ? 'it' : 'each'} and checks again when it starts and on every
                purge that deletes files, so {waitingFiles === 1 ? 'it is' : 'they are'} removed
                once the folder is back. If the folder is gone for good, you can forget the records:
                that deletes only the records, never a file or a book, and the action is logged.
                {forgetStage === 'idle' && (
                  <Box sx={{ mt: 1 }}>
                    <Button
                      size="small"
                      color="warning"
                      variant="outlined"
                      onClick={() => setForgetStage('confirm')}
                      data-testid="purge-forget-open"
                    >
                      Forget {waitingFiles.toLocaleString()}{' '}
                      {waitingFiles === 1 ? 'record' : 'records'}
                    </Button>
                  </Box>
                )}
                {forgetStage !== 'idle' && (
                  <Box sx={{ mt: 1 }} data-testid="purge-forget-confirm">
                    <Typography variant="body2" gutterBottom>
                      Forget {waitingFiles.toLocaleString()}{' '}
                      {waitingFiles === 1 ? 'record' : 'records'}? If the folder comes back later,
                      its files stay on disk, and a library scan would import them as new books.
                    </Typography>
                    <Button
                      size="small"
                      color="error"
                      variant="contained"
                      disabled={forgetStage === 'working'}
                      onClick={() => void forgetRecords()}
                      data-testid="purge-forget-confirm-button"
                      sx={{ mr: 1 }}
                    >
                      {forgetStage === 'working' ? 'Forgetting...' : 'Yes, forget them'}
                    </Button>
                    <Button
                      size="small"
                      disabled={forgetStage === 'working'}
                      onClick={() => {
                        setForgetStage('idle');
                        setForgetError(null);
                      }}
                    >
                      Keep them
                    </Button>
                  </Box>
                )}
                {forgetError && (
                  <Typography variant="body2" sx={{ mt: 1 }} data-testid="purge-forget-error">
                    {forgetError}
                  </Typography>
                )}
              </Alert>
            )}
            {forgetNote && (
              <Alert severity="info" sx={{ mt: 2 }} data-testid="purge-forget-note">
                {forgetNote}
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
