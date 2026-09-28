// file: web/src/components/review/useDedupPipeline.tsx
// version: 1.1.0
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

import { useCallback, useEffect, useSyncExternalStore, type ReactNode } from 'react';
import { Link as RouterLink } from 'react-router-dom';
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
  type PipelineStepId,
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

// ── Module-level run store ──────────────────────────────────────────────────
//
// The run is a multi-hour client-side promise chain, so its state cannot live
// in the component that started it: leave /review and come back and a
// component-local `busy` would read idle while the first chain was still going,
// re-enabling the button and letting a second chain start alongside it. The
// state lives here instead, read through useSyncExternalStore, and the
// component currently mounted supplies the toast / "open Dupes" callbacks via
// `bindings` (null while no workspace is mounted -- the run carries on and its
// result is waiting when one mounts again).

let runState: RunState = { kind: 'idle' };
let stopRequested = false;
/** Steps skipped by preflight (e.g. embeddings turned off). */
let skipSteps: PipelineStepId[] = [];
const runListeners = new Set<() => void>();
let bindings: UseDedupPipelineOptions | null = null;

function setRunState(next: RunState | ((s: RunState) => RunState)) {
  runState = typeof next === 'function' ? next(runState) : next;
  runListeners.forEach((l) => l());
}
function subscribeRun(l: () => void) {
  runListeners.add(l);
  return () => runListeners.delete(l);
}
const getRunState = () => runState;

/** Test-only: forget any run left over from a previous test. */
export function resetDedupPipelineForTests() {
  runState = { kind: 'idle' };
  stopRequested = false;
  skipSteps = [];
  bindings = null;
}

async function startRun() {
  stopRequested = false;
  setRunState({ kind: 'running', progress: null, stopRequested: false });
  try {
    const result = await runDedupPipeline({
      pollIntervalMs: bindings?.pollIntervalMs,
      skip: skipSteps,
      shouldStop: () => stopRequested,
      onProgress: (progress) => setRunState((s) => (s.kind === 'running' ? { ...s, progress } : s)),
    });
    setRunState({ kind: 'done', result });
    bindings?.toast('Duplicate check finished — the results are in the Dupes tab.', 'success');
    bindings?.onFinished();
  } catch (err) {
    let message: string;
    if (err instanceof PipelineStoppedError) {
      message = `Stopped. ${err.message} Steps already finished keep their results.`;
    } else if (err instanceof PipelineStepError) {
      message = `"${err.step.label}" failed — ${err.message}. Later steps were not run.`;
    } else {
      message = err instanceof Error ? err.message : 'The duplicate check failed.';
    }
    setRunState({ kind: 'failed', message });
    bindings?.toast(message, err instanceof PipelineStoppedError ? 'info' : 'error');
  }
}

async function requestRun() {
  if (runState.kind === 'checking' || runState.kind === 'running') return;
  setRunState({ kind: 'checking' });
  let config: api.Config;
  try {
    config = await api.getConfig();
  } catch {
    setRunState({ kind: 'idle' });
    bindings?.toast(
      'Could not read the dedup settings, so the duplicate check did not start. Try again in a moment.',
      'error'
    );
    return;
  }
  // With embeddings switched off the step can only fail, and a failed step
  // stops every later one; leave it out rather than block the whole run.
  skipSteps = config?.dedup?.embeddings_enabled === false ? ['embeddings'] : [];
  const risks = autoMergeRisks(config ?? {});
  if (risks.length > 0) {
    setRunState({ kind: 'confirm', risks });
    return;
  }
  void startRun();
}

export function useDedupPipeline(options: UseDedupPipelineOptions) {
  const state = useSyncExternalStore(subscribeRun, getRunState, getRunState);

  // The mounted workspace owns the callbacks. Re-bound every render so the
  // latest toast / lane setter is used.
  useEffect(() => {
    bindings = options;
    return () => {
      if (bindings === options) bindings = null;
    };
  });

  const setState = setRunState;
  const start = useCallback(() => void startRun(), []);
  const request = useCallback(() => requestRun(), []);

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
            {/* Router navigation, not href: a full page load would kill a
                run in progress. */}
            <Link component={RouterLink} to={{ pathname: '/settings', hash: '#dedup' }}>
              Settings → Dedup
            </Link>{' '}
            first.
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
              stopRequested = true;
              setState((s) => (s.kind === 'running' ? { ...s, stopRequested: true } : s));
            }}
          >
            {state.stopRequested ? 'Stopping after this step…' : 'Stop'}
          </Button>
        }
      >
        <AlertTitle>
          Finding duplicates — step {stepNo} of {total}:{' '}
          {p?.step.label ?? DEDUP_PIPELINE_STEPS[0].label}
        </AlertTitle>
        <Typography variant="body2">
          {op?.message || 'Working…'} You can use other pages meanwhile, but keep this browser tab
          open: closing or reloading it stops the remaining steps.
        </Typography>
        <Box sx={{ mt: 1 }}>
          <LinearProgress
            variant={pct === undefined ? 'indeterminate' : 'determinate'}
            value={pct}
          />
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
        {/* Deliberately no pointer to "Recalculate scores": saving new bands
            feeds automatic resolution, which is not a simple-user action. */}
        {` Score check: ${(r.changed ?? 0).toLocaleString()} of ${(r.inspected ?? 0).toLocaleString()} pairs would score differently under the latest rules; nothing was changed.`}
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
