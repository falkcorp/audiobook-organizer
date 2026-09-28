// file: web/src/components/review/dedupPipeline.ts
// version: 1.0.0
// guid: 3f6b1d82-7a4e-4c90-b5d1-2e8f0a9c6d47
// last-edited: 2026-09-27
//
// The Dedup menu's one-button run: every duplicate check, in order, ending in
// a score preview. Owner request 2026-09-27: "make an easy section where the
// user only has to press 1 button and everything is automatic for them."
//
// ORDER, AND WHY
//
//  1. Embeddings   (dedup.embed-scan)   -- the similarity data step 3 compares.
//                                          Full scan re-embeds stale books too,
//                                          but only one at a time inside its
//                                          own pass; doing it first is cheaper.
//  2. Acoustic     (acoustid.scan)      -- emits audio-match pairs from the
//                                          fingerprints already stored. Before
//                                          step 3 so its scoring pass sees them.
//                                          Step 3's stale-candidate purge only
//                                          drops pairs whose books went
//                                          non-primary/missing, not fresh ones.
//  3. Find dupes   (dedup.full-scan)    -- exact + similarity checks, then the
//                                          unified score for every book.
//  4. AI review    (dedup.llm-review)   -- reads the ambiguous pairs step 3
//                                          just scored, so it must follow it.
//  5. Rescore preview (POST /dedup/rescore apply=false) -- synchronous, writes
//                                          nothing, returns the counts shown at
//                                          the end.
//
// Each op step is started and then polled to a terminal status before the next
// starts. Anything other than `completed` stops the run: a later step built on
// a failed earlier one would report results that are not real.
//
// WHAT THIS DOES NOT DO
//
// It never calls an apply/merge route. Two server settings can still make a
// step merge on its own (auto_merge_enabled for step 3's identical-file pairs,
// llm_auto_merge_high_confidence for step 4); the caller checks them first and
// asks -- see `autoMergeRisks`. The server has no per-run "never merge" switch,
// so the check is the best the client can do; a server-side chain op taking
// such a flag would be the robust fix, and would also survive the tab closing,
// which this client-side sequence does not.

import * as api from '../../services/api';
import type { Config, DedupRescoreResult, Operation } from '../../services/api';

export type PipelineStepId = 'embeddings' | 'acoustic' | 'find' | 'ai-review' | 'rescore-preview';

export interface PipelineStep {
  id: PipelineStepId;
  /** Plain-language name shown in the progress banner. */
  label: string;
}

export const DEDUP_PIPELINE_STEPS: readonly PipelineStep[] = [
  { id: 'embeddings', label: 'Preparing similarity data' },
  { id: 'acoustic', label: 'Comparing audio fingerprints' },
  { id: 'find', label: 'Finding and scoring duplicates' },
  { id: 'ai-review', label: 'AI review of unclear pairs' },
  { id: 'rescore-preview', label: 'Checking scores' },
];

/** The op-starting steps, keyed so tests can see exactly which route each hits. */
const START: Record<Exclude<PipelineStepId, 'rescore-preview'>, () => Promise<Operation>> = {
  embeddings: () => api.triggerEmbedScan(),
  acoustic: () => api.triggerDedupAcoustID(),
  find: () => api.triggerDedupScan(),
  'ai-review': () => api.triggerDedupLLM(),
};

export interface PipelineProgress {
  stepIndex: number;
  step: PipelineStep;
  /** Latest poll of the step's operation; absent for the synchronous last step. */
  op?: Operation;
}

export class PipelineStepError extends Error {
  constructor(
    public readonly step: PipelineStep,
    message: string
  ) {
    super(message);
    this.name = 'PipelineStepError';
  }
}

export class PipelineStoppedError extends Error {
  constructor(public readonly step: PipelineStep) {
    super(`Stopped before "${step.label}".`);
    this.name = 'PipelineStoppedError';
  }
}

export interface RunDedupPipelineOptions {
  onProgress?: (p: PipelineProgress) => void;
  /** Checked between steps; true stops the run before the next one starts. */
  shouldStop?: () => boolean;
  /** These ops run for minutes to hours, so polling is deliberately slow. */
  pollIntervalMs?: number;
}

export async function runDedupPipeline(
  opts: RunDedupPipelineOptions = {}
): Promise<DedupRescoreResult> {
  const { onProgress, shouldStop, pollIntervalMs = 5000 } = opts;

  for (let i = 0; i < DEDUP_PIPELINE_STEPS.length; i++) {
    const step = DEDUP_PIPELINE_STEPS[i];
    if (shouldStop?.()) throw new PipelineStoppedError(step);
    onProgress?.({ stepIndex: i, step });

    if (step.id === 'rescore-preview') {
      try {
        return await api.rescoreDedupCandidates(false);
      } catch (err) {
        throw new PipelineStepError(step, errorText(err, 'could not check scores'));
      }
    }

    let started: Operation;
    try {
      started = await START[step.id]();
    } catch (err) {
      throw new PipelineStepError(step, errorText(err, 'could not start'));
    }
    if (!started?.id) {
      // Without an id there is nothing to wait on, and running the next step
      // without knowing this one finished is exactly what the ordering forbids.
      throw new PipelineStepError(step, 'the server did not return an operation id');
    }

    let final: Operation;
    try {
      final = await api.pollOperation(
        started.id,
        (op) => onProgress?.({ stepIndex: i, step, op }),
        pollIntervalMs
      );
    } catch (err) {
      throw new PipelineStepError(step, errorText(err, 'lost track of its progress'));
    }
    if (final.status !== 'completed') {
      const why = final.error_message ? `: ${final.error_message}` : '';
      throw new PipelineStepError(step, `ended as "${final.status}"${why}`);
    }
  }
  // Unreachable: the last step returns. Kept so the signature stays honest if
  // the step list is ever reordered.
  throw new Error('dedup pipeline ended without a score check');
}

/**
 * The server settings that let a pipeline step merge books without a review.
 * Empty means the run cannot merge anything on its own.
 */
export function autoMergeRisks(config: Pick<Config, 'dedup'>): string[] {
  const risks: string[] = [];
  if (!config.dedup) {
    // The server's own default for auto_merge_enabled is TRUE, so a missing
    // block is not "off" -- say we could not tell.
    return [
      "The server did not report its automatic-merge settings, so the scan may link identical copies and the AI review may merge pairs it is sure about.",
    ];
  }
  if (config.dedup.auto_merge_enabled) {
    risks.push(
      'Automatic merge is on: books with the same author, the same title and an identical audio file will be linked as versions during the scan.'
    );
  }
  if (config.dedup.llm_auto_merge_high_confidence) {
    risks.push(
      'AI auto-merge is on: pairs the AI is highly confident about will be merged during the AI review.'
    );
  }
  return risks;
}

function errorText(err: unknown, fallback: string): string {
  return err instanceof Error && err.message ? err.message : fallback;
}
