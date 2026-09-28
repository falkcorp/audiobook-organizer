// file: web/src/components/review/useDedupPipeline.tsx
// version: 1.0.0
// guid: 8c2e5a17-4d93-4f6b-a0e8-71b3d9c4f25e
// last-edited: 2026-09-27
//
// State + UI for the Dedup menu's one-button run (see dedupPipeline.ts).
//
// Returned as a hook with its own `ui` node so ReviewWorkspace only has to call
// it, point a menu item at `request`, and render `ui` once -- the run's
// progress banner, its merge-risk prompt and its result all live here.
//
// PREFLIGHT FAILS CLOSED. The run reads the dedup settings before it starts,
// because two of them let steps merge books on their own. If the settings
// cannot be read the run does not start: "we couldn't check" must not turn
// into "we assumed it was safe".

import { useCallback, useRef, useState, type ReactNode } from 'react';
import {
  Alert,
  AlertTitle,
  Box,
  Button,
  Dialog,
  DialogActions,
  DialogContent,
  DialogContentText,
  DialogTitle,
  LinearProgress,
  Link,
  Typography,
} from '@mui/material';
import * as api from '../../services/api';
import type { DedupRescoreResult } from '../../services/api';
import {
  DEDUP_PIPELINE_STEPS,
  PipelineStepError,
  PipelineStoppedError,
  autoMergeRisks,
  runDedupPipeline,
  type PipelineProgress,
} from './dedupPipeline';

type Toast = (message: string, severity?: 'success' | 'error' | 'info' | 'warning') => void;

type RunState =
  | { kind: 'idle' }
  | { kind: 'checking' }
  | { kind: 'confirm'; risks: string[] }
  | { kind: 'running'; progress: PipelineProgress | null; stopRequested: boolean }
  | { kind: 'done'; result: DedupRescoreResult }
  | { kind: 'failed'; message: string };

export interface UseDedupPipelineOptions {
  toast: Toast;
  /** Called after a successful run -- the workspace opens the Dupes lane. */
  onFinished: () => void;
  /** Test seam; production polls every 5s. */
  pollIntervalMs?: number;
}

export function useDedupPipeline({ toast, onFinished, pollIntervalMs }: UseDedupPipelineOptions) {
  const [state, setState] = useState<RunState>({ kind: 'idle' });
  const stopRef = useRef(false);

  const start = useCallback(async () => {
    stopRef.current = false;
    setState({ kind: 'running', progress: null, stopRequested: false });
    try {
      const result = await runDedupPipeline({
        pollIntervalMs,
        shouldStop: () => stopRef.current,
        onProgress: (progress) =>
          setState((s) => (s.kind === 'running' ? { ...s, progress } : s)),
      });
      setState({ kind: 'done', result });
      toast('Duplicate check finished — the results are in the Dupes tab.', 'success');
      onFinished();
    } catch (err) {
      let message: string;
      if (err instanceof PipelineStoppedError) {
        message = `Stopped. ${err.message} Steps already finished keep their results.`;
      } else if (err instanceof PipelineStepError) {
        message = `"${err.step.label}" failed — ${err.message}. Later steps were not run.`;
      } else {
        message = err instanceof Error ? err.message : 'The duplicate check failed.';
      }
      setState({ kind: 'failed', message });
      toast(message, err instanceof PipelineStoppedError ? 'info' : 'error');
    }
  }, [toast, onFinished, pollIntervalMs]);

  const request = useCallback(async () => {
    setState({ kind: 'checking' });
    let risks: string[];
    try {
      risks = autoMergeRisks(await api.getConfig());
    } catch {
      setState({ kind: 'idle' });
      toast(
        'Could not read the dedup settings, so the duplicate check did not start. Try again in a moment.',
        'error'
      );
      return;
    }
    if (risks.length > 0) {
      setState({ kind: 'confirm', risks });
      return;
    }
    void start();
  }, [start, toast]);

  const busy = state.kind === 'checking' || state.kind === 'running';

  let ui: ReactNode = null;
  if (state.kind === 'confirm') {
    ui = (
      <Dialog open onClose={() => setState({ kind: 'idle' })} data-testid="dedup-pipeline-confirm">
        <DialogTitle>Some duplicates may be merged automatically</DialogTitle>
        <DialogContent>
          <DialogContentText component="div">
            This check never merges anything by itself, but your settings let parts of it do so:
            <ul>
              {state.risks.map((r) => (
                <li key={r}>{r}</li>
              ))}
            </ul>
            To review everything yourself instead, turn these off in{' '}
            <Link href="/settings#dedup">Settings → Dedup</Link> first.
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setState({ kind: 'idle' })}>Cancel</Button>
          <Button
            variant="contained"
            color="warning"
            data-testid="dedup-pipeline-confirm-run"
            onClick={() => void start()}
          >
            Run anyway
          </Button>
        </DialogActions>
      </Dialog>
    );
  } else if (state.kind === 'running') {
    const p = state.progress;
    const stepNo = p ? p.stepIndex + 1 : 1;
    const total = DEDUP_PIPELINE_STEPS.length;
    const op = p?.op;
    const pct = op && op.total > 0 ? Math.min(100, (op.progress / op.total) * 100) : undefined;
    ui = (
      <Alert
        severity="info"
        data-testid="dedup-pipeline-progress"
        sx={{ mx: 2, mt: 1 }}
        action={
          <Button
            color="inherit"
            size="small"
            disabled={state.stopRequested}
            onClick={() => {
              stopRef.current = true;
              setState((s) => (s.kind === 'running' ? { ...s, stopRequested: true } : s));
            }}
          >
            {state.stopRequested ? 'Stopping after this step…' : 'Stop'}
          </Button>
        }
      >
        <AlertTitle>
          Finding duplicates — step {stepNo} of {total}: {p?.step.label ?? DEDUP_PIPELINE_STEPS[0].label}
        </AlertTitle>
        <Typography variant="body2">
          {op?.message || 'Working…'} Keep this tab open; closing it stops the remaining steps.
        </Typography>
        <Box sx={{ mt: 1 }}>
          <LinearProgress variant={pct === undefined ? 'indeterminate' : 'determinate'} value={pct} />
        </Box>
      </Alert>
    );
  } else if (state.kind === 'done') {
    const r = state.result;
    ui = (
      <Alert
        severity="success"
        data-testid="dedup-pipeline-done"
        sx={{ mx: 2, mt: 1 }}
        onClose={() => setState({ kind: 'idle' })}
      >
        Duplicate check finished — the pairs it found are below for your review.
        {r.changed > 0
          ? ` ${r.changed.toLocaleString()} of ${r.inspected.toLocaleString()} scored pairs would get a different confidence with the latest scoring; Advanced → Recalculate scores saves that.`
          : ` All ${r.inspected.toLocaleString()} scored pairs are up to date.`}
      </Alert>
    );
  } else if (state.kind === 'failed') {
    ui = (
      <Alert
        severity="error"
        data-testid="dedup-pipeline-failed"
        sx={{ mx: 2, mt: 1 }}
        onClose={() => setState({ kind: 'idle' })}
      >
        {state.message}
      </Alert>
    );
  }

  return { request, busy, ui };
}
