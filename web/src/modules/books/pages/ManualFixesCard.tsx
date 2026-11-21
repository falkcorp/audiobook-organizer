// file: web/src/modules/books/pages/ManualFixesCard.tsx
// version: 0.1.0
// guid: fa2b9e37-b8aa-422b-85ff-a1b9aa3914b7
// last-edited: 2026-11-21

// A books system widget (placement "system", 07 §7.11): the manual-fix jobs (F9-04 slice 1).

import { useEffect, useState } from 'react';
import {
  Alert,
  Box,
  Button,
  Card,
  CardContent,
  CardHeader,
  Chip,
  CircularProgress,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  Stack,
  Typography,
} from '@mui/material';
import PlayArrowIcon from '@mui/icons-material/PlayArrow';
import * as api from '../../../services/api';
import { withOptimisticOperation } from '../../../utils/withOptimisticOperation';

/** Jobs whose real run has a dedicated card with its own preview + confirm. */
const manualFixesApplyElsewhere: Record<string, string> = {
  'merge-chapter-groups': 'Chapter Consolidation card',
};

function advertisesDryRun(job: api.MaintenanceJobDef): boolean {
  return job.default_params?.dry_run === true;
}

interface ManualFixPreview {
  opId: string;
  status: string;
  total: number;
}

/**
 * Lists every registered maintenance job. "Run" never sends dry_run, so each
 * job's advertised default applies: a job that advertises dry_run:true only
 * previews. Running such a job for real is a separate "Apply…" action that is
 * enabled only after a completed preview in this session and goes through a
 * confirm dialog naming the preview's item count. Jobs with a dedicated card
 * (merge-chapter-groups) cannot be applied from here at all.
 */
export function ManualFixesCard() {
  const [jobs, setJobs] = useState<api.MaintenanceJobDef[]>([]);
  const [loading, setLoading] = useState(true);
  const [running, setRunning] = useState<string | null>(null);
  const [previews, setPreviews] = useState<Record<string, ManualFixPreview>>({});
  const [confirmJob, setConfirmJob] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  useEffect(() => {
    api
      .listMaintenanceJobs()
      .then(setJobs)
      .catch((e) => setError(e instanceof Error ? e.message : 'Failed to load jobs'))
      .finally(() => setLoading(false));
  }, []);

  // Run with dry_run OMITTED. For a dry-run-default job this is the preview,
  // followed to completion so its item count can back the confirm dialog.
  const handleRun = async (job: api.MaintenanceJobDef) => {
    setRunning(job.id);
    setError(null);
    try {
      // Bell + toast appear instantly; reconcile to the real op id once the
      // dispatcher returns.
      const { operation_id: opId } = await withOptimisticOperation(`maintenance:${job.id}`, () =>
        api.runMaintenanceJob(job.id)
      );
      if (advertisesDryRun(job) && opId) {
        const op = await api.pollOperation(opId);
        setPreviews((p) => ({
          ...p,
          [job.id]: { opId, status: op.status, total: op.total ?? 0 },
        }));
        if (op.status !== 'completed') {
          setError(op.error_message || `Preview of ${job.id} ended ${op.status}`);
        }
      }
    } catch (e) {
      setError(e instanceof Error ? e.message : `Failed to run ${job.id}`);
    } finally {
      setRunning(null);
    }
  };

  const handleApply = async (jobId: string) => {
    setConfirmJob(null);
    setRunning(jobId);
    setError(null);
    try {
      await withOptimisticOperation(`maintenance:${jobId}`, () =>
        api.runMaintenanceJob(jobId, false)
      );
      // A preview is spent once applied; the next real run needs a new one.
      setPreviews((p) => {
        const next = { ...p };
        delete next[jobId];
        return next;
      });
    } catch (e) {
      setError(e instanceof Error ? e.message : `Failed to run ${jobId}`);
    } finally {
      setRunning(null);
    }
  };

  const confirmPreview = confirmJob ? previews[confirmJob] : undefined;

  return (
    <Card sx={{ mb: 3 }}>
      <CardHeader
        title="Manual Fixes"
        subheader="One-shot maintenance jobs dispatched as async operations. Jobs marked dry run only preview until applied."
      />
      <CardContent>
        {error && (
          <Alert severity="error" sx={{ mb: 1.5 }} onClose={() => setError(null)}>
            {error}
          </Alert>
        )}
        {loading ? (
          <CircularProgress size={24} />
        ) : (
          <Stack spacing={1}>
            {jobs.map((job) => {
              const dry = advertisesDryRun(job);
              const elsewhere = manualFixesApplyElsewhere[job.id];
              const preview = previews[job.id];
              const previewDone = preview?.status === 'completed';
              return (
                <Box
                  key={job.id}
                  data-testid={`manual-fix-${job.id}`}
                  sx={{ display: 'flex', alignItems: 'center', gap: 1, flexWrap: 'wrap' }}
                >
                  <Box sx={{ flex: 1 }}>
                    <Typography
                      variant="body2"
                      sx={{
                        fontWeight: 'medium',
                      }}
                    >
                      {job.id}
                      {dry && <Chip size="small" label="dry run" sx={{ ml: 1 }} />}
                    </Typography>
                    <Typography
                      variant="caption"
                      sx={{
                        color: 'text.secondary',
                        display: 'block',
                      }}
                    >
                      {job.description}
                    </Typography>
                    {dry && elsewhere && (
                      <Typography variant="caption" sx={{ color: 'text.secondary' }}>
                        Real runs happen from the {elsewhere}.
                      </Typography>
                    )}
                    {dry && !elsewhere && preview && (
                      <Typography variant="caption" sx={{ color: 'text.secondary' }}>
                        Preview {preview.status}: {preview.total} item(s)
                      </Typography>
                    )}
                  </Box>
                  <Button
                    variant="outlined"
                    size="small"
                    data-action="preview"
                    startIcon={
                      running === job.id ? (
                        <CircularProgress size={14} color="inherit" />
                      ) : (
                        <PlayArrowIcon />
                      )
                    }
                    disabled={running !== null}
                    onClick={() => void handleRun(job)}
                  >
                    {running === job.id ? 'Starting…' : 'Run'}
                  </Button>
                  {dry && !elsewhere && (
                    <Button
                      variant="contained"
                      color="warning"
                      size="small"
                      data-action="apply"
                      disabled={running !== null || !previewDone}
                      onClick={() => setConfirmJob(job.id)}
                    >
                      Apply…
                    </Button>
                  )}
                </Box>
              );
            })}
          </Stack>
        )}
      </CardContent>

      <Dialog open={confirmJob !== null} onClose={() => setConfirmJob(null)}>
        <DialogTitle>Run {confirmJob} for real?</DialogTitle>
        <DialogContent>
          <DialogContentText>
            The dry-run preview (operation {confirmPreview?.opId}) covered{' '}
            <strong>{confirmPreview?.total ?? 0} item(s)</strong>. Running for real applies the
            changes; the job detects again when it runs, so the count can differ if the library
            changed.
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setConfirmJob(null)}>Cancel</Button>
          <Button
            color="warning"
            variant="contained"
            onClick={() => confirmJob && void handleApply(confirmJob)}
          >
            Run for real
          </Button>
        </DialogActions>
      </Dialog>
    </Card>
  );
}
