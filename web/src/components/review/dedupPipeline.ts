// file: web/src/components/review/dedupPipeline.ts
// version: 2.2.0
// guid: 3f6b1d82-7a4e-4c90-b5d1-2e8f0a9c6d47
// last-edited: 2026-10-03
//
// The Dedup menu's two whole-library runs, both SERVER-SIDE operations that the
// browser only starts and follows:
//
//  - 'all'    "Find all duplicates" -> dedup.run-all. One registry op that runs
//             every duplicate check in order as child ops and ends in a
//             no-write score preview (internal/plugins/dedup/run_all.go has the
//             order and why). Until 2026-09-28 this file chained those steps
//             itself; that chain died with the tab and did not survive a
//             deploy. The server op checkpoints the step it is on and resumes
//             there after a restart.
//  - 'rescan' "Force full rescan" -> dedup.full-scan, the scan that writes the
//             candidate queue the Dupes tab reads. It used to start
//             dedup.book-scan, a DIFFERENT pipeline: hash/folder/fuzzy-title
//             GROUPS kept for 30 minutes in an in-memory server cache and shown
//             only on the /dedup page's Duplicate Scan tab. Nothing it found
//             ever reached the Dupes tab.
//
// Both can link or merge books on their own when a server setting allows it,
// so the caller checks `autoMergeRisks` first and asks.

import * as api from '../../services/api';
import type { Config, DedupRunAllResult, Operation } from '../../services/api';

export type DedupRunKind = 'all' | 'rescan';

export interface DedupRunInfo {
  /** Plain-language name for banners and toasts. */
  label: string;
  /** Operation def the run enqueues (for tests and the Operations page). */
  defId: string;
}

export const DEDUP_RUNS: Record<DedupRunKind, DedupRunInfo> = {
  all: { label: 'Finding duplicates', defId: 'dedup.run-all' },
  rescan: { label: 'Full rescan', defId: 'dedup.full-scan' },
};

/**
 * Starts the run's server op for real and returns its id. 'all' sends
 * dry_run=false explicitly: dedup.run-all previews when the mode is omitted.
 */
export async function startDedupRun(kind: DedupRunKind): Promise<string> {
  const op = kind === 'all' ? await api.startDedupRunAll(false) : await api.triggerDedupScan();
  if (!op?.id) {
    // Without an id there is nothing to follow.
    throw new Error('the server did not return an operation id');
  }
  return op.id;
}

/**
 * Longest wait between status reads while the server cannot be reached. The
 * follower never gives up on an unreachable server: the run lives there, and a
 * deploy (server down, then warming up) is the normal reason reads fail. It
 * stops only when the op itself is gone (404).
 */
export const MAX_RETRY_BACKOFF_MS = 60_000;

/** The op id no longer exists on the server: nothing left to follow. */
export class OperationGoneError extends Error {
  constructor(public readonly opId: string) {
    super(`operation ${opId} no longer exists on the server`);
    this.name = 'OperationGoneError';
  }
}

function isNotFound(err: unknown): boolean {
  // Duck-typed: ApiError carries `status`, and a structural check keeps this
  // working when the api module is mocked in tests.
  return (err as { status?: unknown } | null)?.status === 404;
}

/**
 * `interrupted_quiesced` is how a resumable op reads while the server restarts
 * (a deploy): the registry puts it back in the queue at boot under the SAME id.
 * isOperationTerminal counts every interrupted_* status as final, so the
 * follower special-cases this one and keeps polling.
 */
export function isPausedForRestart(status: string): boolean {
  return status === 'interrupted_quiesced';
}

/**
 * Waits `ms`, or until `signal` aborts, whichever comes first. The timer is
 * always cleared and the abort listener always removed, so a wait that is cut
 * short leaves nothing pending: without this a follower that had been told to
 * stop kept its timer (and everything its callbacks close over) alive for up to
 * MAX_RETRY_BACKOFF_MS, and then made one more status read before noticing.
 */
function sleep(ms: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    if (signal?.aborted) {
      resolve();
      return;
    }
    const onAbort = () => {
      clearTimeout(timeoutId);
      resolve();
    };
    const timeoutId = setTimeout(() => {
      signal?.removeEventListener('abort', onAbort);
      resolve();
    }, ms);
    signal?.addEventListener('abort', onAbort, { once: true });
  });
}

export interface FollowOptions {
  onUpdate?: (op: Operation) => void;
  pollIntervalMs?: number;
  /** Checked between polls; true stops FOLLOWING (not the server op). */
  shouldStopFollowing?: () => boolean;
  /**
   * Aborting stops FOLLOWING (not the server op) immediately: it ends the wait
   * between polls instead of letting it run out. Same outcome as
   * shouldStopFollowing returning true, without the delay.
   */
  signal?: AbortSignal;
  /** Called on each failed read with the consecutive-failure count. */
  onUnreachable?: (failures: number) => void;
}

/**
 * Polls the op until it is terminal and returns its final row. Failed reads are
 * retried forever with a capped backoff (see MAX_RETRY_BACKOFF_MS); a 404
 * throws OperationGoneError.
 */
export async function followOperation(id: string, opts: FollowOptions = {}): Promise<Operation> {
  const { onUpdate, pollIntervalMs = 5000, shouldStopFollowing, onUnreachable, signal } = opts;
  const stopped = () => signal?.aborted === true || shouldStopFollowing?.() === true;
  let failures = 0;
  while (true) {
    let op: Operation;
    try {
      op = await api.getOperationStatus(id);
      failures = 0;
    } catch (err) {
      if (isNotFound(err)) throw new OperationGoneError(id);
      failures++;
      onUnreachable?.(failures);
      if (stopped()) throw err;
      const backoff = Math.min(pollIntervalMs * 2 ** Math.min(failures, 6), MAX_RETRY_BACKOFF_MS);
      await sleep(Math.max(backoff, pollIntervalMs), signal);
      // Told to stop during the wait: do not make another read first.
      if (stopped()) throw err;
      continue;
    }
    onUpdate?.(op);
    if (api.isOperationTerminal(op.status) && !isPausedForRestart(op.status)) return op;
    if (stopped()) return op;
    await sleep(pollIntervalMs, signal);
    if (stopped()) return op;
  }
}

/**
 * Runs dedup.run-all as a PREVIEW (writes nothing: no child op starts, only the
 * read-only score check runs) and returns its result, or null if it could not
 * be run or read. Used to put numbers in the confirmation prompt; a failure
 * here never blocks the prompt.
 */
export async function previewRunAll(
  opts: { pollIntervalMs?: number; shouldStop?: () => boolean; signal?: AbortSignal } = {}
): Promise<DedupRunAllResult | null> {
  try {
    const op = await api.startDedupRunAll(true);
    if (!op?.id) return null;
    const final = await followOperation(op.id, {
      pollIntervalMs: opts.pollIntervalMs,
      shouldStopFollowing: opts.shouldStop,
      signal: opts.signal,
    });
    if (final.status !== 'completed') return null;
    return await readRunAllResult(op.id);
  } catch {
    return null;
  }
}

/** Reads a finished dedup.run-all's result; null if it cannot be read. */
export async function readRunAllResult(id: string): Promise<DedupRunAllResult | null> {
  try {
    const { result_data } = await api.getOperationResult(id);
    if (result_data && typeof result_data === 'object') return result_data as DedupRunAllResult;
  } catch {
    // The run itself succeeded; a missing summary is not a failure.
  }
  return null;
}

/**
 * The server settings that let the run link or merge books without a review.
 * Empty means the run cannot merge anything on its own. The rescan is only the
 * find step, so only the identical-copy auto-link applies to it.
 */
export function autoMergeRisks(
  config: Pick<Config, 'dedup'>,
  kind: DedupRunKind = 'all'
): string[] {
  const risks: string[] = [];
  if (!config.dedup) {
    // The server's own default for auto_merge_enabled is TRUE, so a missing
    // block is not "off" -- say we could not tell.
    return [
      kind === 'all'
        ? 'The server did not report its automatic-merge settings, so the scan may link identical copies and the AI review may merge pairs it is sure about.'
        : 'The server did not report its automatic-merge settings, so the scan may link identical copies.',
    ];
  }
  if (config.dedup.auto_merge_enabled) {
    risks.push(
      'Automatic merge is on: books with the same author, the same title and an identical audio file will be linked as versions during the scan.'
    );
  }
  if (kind === 'all' && config.dedup.llm_auto_merge_high_confidence) {
    risks.push(
      'AI auto-merge is on: pairs the AI is highly confident about will be merged during the AI review.'
    );
  }
  return risks;
}
