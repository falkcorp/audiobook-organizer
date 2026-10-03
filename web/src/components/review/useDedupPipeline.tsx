// file: web/src/components/review/useDedupPipeline.tsx
// version: 2.2.0
// guid: 8c2e5a17-4d93-4f6b-a0e8-71b3d9c4f25e
// last-edited: 2026-10-03
//
// State + UI for the Dedup menu's two whole-library runs (see dedupPipeline.ts):
// "Find all duplicates" (server op dedup.run-all) and "Force full rescan"
// (dedup.full-scan). The browser only starts the op and follows it; the work
// runs on the server and survives this tab closing.
//
// Returned as a hook with its own `ui` node so ReviewWorkspace only has to call
// it, point menu items at `request`, and render `ui` once -- the progress
// banner, the merge-risk prompt and the result all live here.
//
// PREFLIGHT FAILS CLOSED. Both runs can link or merge books on their own when a
// server setting allows it, so the settings are read before starting. If they
// cannot be read the run does not start: "we couldn't check" must not turn into
// "we assumed it was safe".

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
import type { DedupRunAllResult, Operation } from '../../services/api';
import {
  DEDUP_RUNS,
  autoMergeRisks,
  followOperation,
  previewRunAll,
  OperationGoneError,
  isPausedForRestart,
  readRunAllResult,
  startDedupRun,
  type DedupRunKind,
} from './dedupPipeline';

type Toast = (message: string, severity?: 'success' | 'error' | 'info' | 'warning') => void;

type RunState =
  | { kind: 'idle' }
  | { kind: 'checking'; run: DedupRunKind }
  | {
      kind: 'confirm';
      run: DedupRunKind;
      risks: string[];
      /**
       * The dedup.run-all PREVIEW shown in the prompt: 'loading' while it
       * runs, null when there is none (the rescan, whose op has no preview
       * mode) or it could not be read. Never blocks "Run anyway".
       */
      preview: DedupRunAllResult | 'loading' | null;
    }
  | {
      kind: 'running';
      run: DedupRunKind;
      opId: string | null;
      op: Operation | null;
      stopRequested: boolean;
      /** Last status read failed; retrying (a deploy, a blip). */
      unreachable: boolean;
    }
  | { kind: 'done'; run: DedupRunKind; result: DedupRunAllResult | null }
  | { kind: 'failed'; run: DedupRunKind; message: string };

export interface UseDedupPipelineOptions {
  toast: Toast;
  /** Called after a successful run -- the workspace opens the Dupes lane. */
  onFinished: () => void;
  /** Test seam; production polls every 5s. */
  pollIntervalMs?: number;
}

// ── Module-level run store ──────────────────────────────────────────────────
//
// Following a multi-hour op cannot live in the component that started it:
// leave /review and come back and a component-local `busy` would read idle
// while the op was still going. The state lives here, read through
// useSyncExternalStore; the mounted workspace supplies the toast / "open Dupes"
// callbacks via `bindings`.
//
// The op id is also remembered in localStorage (a per-viewer convenience, not
// state anything depends on) so a reload picks the banner back up. Losing it is
// harmless: the op keeps running on the server, shows in Operations, and a
// second press of "Find all duplicates" returns the same run.

let runState: RunState = { kind: 'idle' };
const runListeners = new Set<() => void>();
let bindings: UseDedupPipelineOptions | null = null;
let followGeneration = 0;
let previewGeneration = 0;
// One controller per live follower. Bumping a generation makes the old
// follower's result ignored; aborting its controller is what ends its wait
// between polls, so it does not sit on a timer for up to a minute first.
let followAbort: AbortController | null = null;
let previewAbort: AbortController | null = null;

function nextFollow(): { gen: number; signal: AbortSignal } {
  followAbort?.abort();
  followAbort = new AbortController();
  return { gen: ++followGeneration, signal: followAbort.signal };
}

function nextPreview(): { gen: number; signal: AbortSignal } {
  previewAbort?.abort();
  previewAbort = new AbortController();
  return { gen: ++previewGeneration, signal: previewAbort.signal };
}

const STORAGE_KEY = 'review.dedupRun';

function remember(run: DedupRunKind, opId: string | null) {
  try {
    if (opId) window.localStorage.setItem(STORAGE_KEY, JSON.stringify({ run, opId }));
    else window.localStorage.removeItem(STORAGE_KEY);
  } catch {
    // Storage blocked (private window, previews): the banner just won't come
    // back after a reload.
  }
}

function recall(): { run: DedupRunKind; opId: string } | null {
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return null;
    const v = JSON.parse(raw) as { run?: string; opId?: string };
    if ((v.run === 'all' || v.run === 'rescan') && typeof v.opId === 'string' && v.opId) {
      return { run: v.run, opId: v.opId };
    }
  } catch {
    // Unreadable or malformed: treat as nothing remembered.
  }
  return null;
}

function setRunState(next: RunState | ((s: RunState) => RunState)) {
  runState = typeof next === 'function' ? next(runState) : next;
  // The preview only feeds the confirmation prompt. Once the prompt is gone
  // (cancelled, closed, or confirmed) nothing reads it, so stop following it
  // now rather than at its next poll.
  if (runState.kind !== 'confirm' && previewAbort) {
    previewAbort.abort();
    previewAbort = null;
  }
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
  bindings = null;
  followGeneration++;
  previewGeneration++;
  followAbort?.abort();
  previewAbort?.abort();
  followAbort = null;
  previewAbort = null;
}

async function follow(run: DedupRunKind, opId: string) {
  const { gen, signal } = nextFollow();
  const label = DEDUP_RUNS[run].label;
  let final: Operation;
  try {
    final = await followOperation(opId, {
      pollIntervalMs: bindings?.pollIntervalMs,
      shouldStopFollowing: () => gen !== followGeneration,
      signal,
      onUpdate: (op) =>
        setRunState((s) =>
          s.kind === 'running' && s.opId === opId ? { ...s, op, unreachable: false } : s
        ),
      onUnreachable: () =>
        setRunState((s) =>
          s.kind === 'running' && s.opId === opId ? { ...s, unreachable: true } : s
        ),
    });
  } catch (err) {
    if (gen !== followGeneration) return;
    // Forget the id either way, or every later visit would re-follow a run
    // that cannot be followed, with both buttons disabled while it fails.
    // Pressing the button again is safe: an active run is returned, not
    // duplicated.
    remember(run, null);
    const message =
      err instanceof OperationGoneError
        ? `"${label}" is no longer on the server (it may have been cleaned up). Start it again to check for duplicates.`
        : `Lost track of "${label}" (${err instanceof Error ? err.message : 'no response'}). It may still be running — check Operations.`;
    setRunState({ kind: 'failed', run, message });
    bindings?.toast(message, 'error');
    return;
  }
  if (gen !== followGeneration) return;
  remember(run, null);

  if (final.status === 'completed') {
    const result = run === 'all' ? await readRunAllResult(opId) : null;
    setRunState({ kind: 'done', run, result });
    bindings?.toast(`${label} finished — the results are in the Dupes tab.`, 'success');
    bindings?.onFinished();
    return;
  }
  const why = final.error_message ? `: ${final.error_message}` : '';
  const message =
    final.status === 'canceled'
      ? `${label} stopped. Steps already finished keep their results.`
      : `${label} ended as "${final.status}"${why}. Later steps were not run.`;
  setRunState({ kind: 'failed', run, message });
  bindings?.toast(message, final.status === 'canceled' ? 'info' : 'error');
}

async function startRun(run: DedupRunKind) {
  setRunState({
    kind: 'running',
    run,
    opId: null,
    op: null,
    stopRequested: false,
    unreachable: false,
  });
  let opId: string;
  try {
    opId = await startDedupRun(run);
  } catch (err) {
    const message = `Could not start "${DEDUP_RUNS[run].label}": ${err instanceof Error ? err.message : 'unknown error'}.`;
    setRunState({ kind: 'failed', run, message });
    bindings?.toast(message, 'error');
    return;
  }
  remember(run, opId);
  setRunState((s) => (s.kind === 'running' ? { ...s, opId } : s));
  void follow(run, opId);
}

async function requestRun(run: DedupRunKind) {
  if (runState.kind === 'checking' || runState.kind === 'running') return;
  setRunState({ kind: 'checking', run });
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
  const risks = autoMergeRisks(config ?? {}, run);
  if (risks.length > 0) {
    if (run !== 'all') {
      setRunState({ kind: 'confirm', run, risks, preview: null });
      return;
    }
    // Put real numbers in the prompt: run the op as a preview (writes
    // nothing) while the dialog is open.
    const { gen, signal } = nextPreview();
    setRunState({ kind: 'confirm', run, risks, preview: 'loading' });
    const preview = await previewRunAll({
      pollIntervalMs: bindings?.pollIntervalMs,
      shouldStop: () => gen !== previewGeneration || runState.kind !== 'confirm',
      signal,
    });
    setRunState((s) => (s.kind === 'confirm' && gen === previewGeneration ? { ...s, preview } : s));
    return;
  }
  void startRun(run);
}

async function requestStop() {
  if (runState.kind !== 'running' || !runState.opId || runState.stopRequested) return;
  const { opId } = runState;
  setRunState((s) => (s.kind === 'running' ? { ...s, stopRequested: true } : s));
  try {
    await api.cancelOperation(opId);
  } catch {
    setRunState((s) => (s.kind === 'running' ? { ...s, stopRequested: false } : s));
    bindings?.toast('Could not stop it. Try again, or cancel it under Operations.', 'error');
  }
}

/** On mount: pick a remembered run back up after a reload. */
function resumeRemembered() {
  if (runState.kind !== 'idle') return;
  const r = recall();
  if (!r) return;
  setRunState({
    kind: 'running',
    run: r.run,
    opId: r.opId,
    op: null,
    stopRequested: false,
    unreachable: false,
  });
  void follow(r.run, r.opId);
}

/** The preview's counts inside the confirmation prompt. */
function previewSummary(preview: DedupRunAllResult | 'loading' | null): ReactNode {
  if (preview === null) return null;
  if (preview === 'loading') {
    return (
      <Typography variant="body2" component="p" sx={{ mb: 1 }} data-testid="dedup-pipeline-preview">
        Checking what is waiting now (this changes nothing)…
      </Typography>
    );
  }
  const r = preview.rescore_preview;
  const steps = (preview.preview_skipped ?? []).map((s) => s.label);
  return (
    <Typography variant="body2" component="p" sx={{ mb: 1 }} data-testid="dedup-pipeline-preview">
      {r
        ? `Right now ${(r.inspected ?? 0).toLocaleString()} pairs are waiting for review, and ${(r.changed ?? 0).toLocaleString()} would score differently under the latest rules. `
        : ''}
      {steps.length > 0 ? `Running it will: ${steps.join(', ')}.` : ''}
    </Typography>
  );
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
  useEffect(() => resumeRemembered(), []);

  const request = useCallback((run: DedupRunKind = 'all') => requestRun(run), []);
  const busy = state.kind === 'checking' || state.kind === 'running';

  let ui: ReactNode = null;
  if (state.kind === 'confirm') {
    const run = state.run;
    ui = (
      <Dialog
        open
        onClose={() => setRunState({ kind: 'idle' })}
        data-testid="dedup-pipeline-confirm"
      >
        <DialogTitle>Some duplicates may be merged automatically</DialogTitle>
        <DialogContent>
          <DialogContentText component="div">
            This check never merges anything by itself, but your settings let parts of it do so:
            <ul>
              {state.risks.map((r) => (
                <li key={r}>{r}</li>
              ))}
            </ul>
            {previewSummary(state.preview)}
            To review everything yourself instead, turn these off in{' '}
            <Link component={RouterLink} to={{ pathname: '/settings', hash: '#dedup' }}>
              Settings → Dedup
            </Link>{' '}
            first.
          </DialogContentText>
        </DialogContent>
        <DialogActions>
          <Button onClick={() => setRunState({ kind: 'idle' })}>Cancel</Button>
          <Button
            variant="contained"
            color="warning"
            data-testid="dedup-pipeline-confirm-run"
            onClick={() => void startRun(run)}
          >
            Run anyway
          </Button>
        </DialogActions>
      </Dialog>
    );
  } else if (state.kind === 'running') {
    const op = state.op;
    const pct = op && op.total > 0 ? Math.min(100, (op.progress / op.total) * 100) : undefined;
    const paused = op ? isPausedForRestart(op.status) : false;
    const stopLabel = state.run === 'all' ? 'Stop after this step' : 'Stop';
    ui = (
      <Alert
        severity="info"
        data-testid="dedup-pipeline-progress"
        sx={{ mx: 2, mt: 1 }}
        action={
          <Button
            color="inherit"
            size="small"
            data-testid="dedup-pipeline-stop"
            disabled={!state.opId || state.stopRequested}
            onClick={() => void requestStop()}
          >
            {state.stopRequested ? 'Stopping…' : stopLabel}
          </Button>
        }
      >
        <AlertTitle>{DEDUP_RUNS[state.run].label}</AlertTitle>
        <Typography variant="body2" data-testid="dedup-pipeline-message">
          {state.unreachable
            ? "Can't reach the server right now — retrying. The run carries on there."
            : paused
              ? 'Paused while the server restarts — it will pick up at the step it was on.'
              : op?.message || (state.opId ? 'Waiting to start…' : 'Starting…')}
        </Typography>
        <Typography variant="caption" component="p" sx={{ mt: 0.5 }}>
          This runs on the server: you can leave this page or close the tab and it keeps going. Its
          progress is also under Operations.
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
    const r = state.result?.rescore_preview;
    ui = (
      <Alert
        severity="success"
        data-testid="dedup-pipeline-done"
        sx={{ mx: 2, mt: 1 }}
        onClose={() => setRunState({ kind: 'idle' })}
      >
        {DEDUP_RUNS[state.run].label} finished — the pairs it found are below for your review.
        {/* Deliberately no pointer to "Recalculate scores": saving new bands
            feeds automatic resolution, which is not a simple-user action. */}
        {r
          ? ` Score check: ${(r.changed ?? 0).toLocaleString()} of ${(r.inspected ?? 0).toLocaleString()} pairs would score differently under the latest rules; nothing was changed.`
          : ''}
      </Alert>
    );
  } else if (state.kind === 'failed') {
    ui = (
      <Alert
        severity="error"
        data-testid="dedup-pipeline-failed"
        sx={{ mx: 2, mt: 1 }}
        onClose={() => setRunState({ kind: 'idle' })}
      >
        {state.message}
      </Alert>
    );
  }

  return { request, busy, ui };
}
