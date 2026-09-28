// file: web/src/components/review/dedupPipeline.test.ts
// version: 1.1.0
// guid: 6d1a8f35-2c94-4e7b-b3f0-9a5e4c2d7b18
// last-edited: 2026-09-27

import { describe, it, expect, beforeEach, vi } from 'vitest';
import * as api from '../../services/api';
import {
  DEDUP_PIPELINE_STEPS,
  MAX_CONSECUTIVE_POLL_FAILURES,
  PipelineStepError,
  PipelineStoppedError,
  autoMergeRisks,
  runDedupPipeline,
} from './dedupPipeline';

vi.mock('../../services/api');

const RESCORE = { inspected: 10, skipped: 0, changed: 2, applied: false, band_deltas: {} };

function done(id: string, status = 'completed', error_message?: string): api.Operation {
  return { id, status, progress: 1, total: 1, error_message } as unknown as api.Operation;
}

let calls: string[];

const actualApi = await vi.importActual<typeof api>('../../services/api');

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(api.isOperationTerminal).mockImplementation(actualApi.isOperationTerminal);
  calls = [];
  const trigger = (name: string, id: string) => async () => {
    calls.push(name);
    return { id } as api.Operation;
  };
  vi.mocked(api.triggerEmbedScan).mockImplementation(trigger('embed', 'op-embed'));
  vi.mocked(api.triggerDedupAcoustID).mockImplementation(trigger('acoustic', 'op-acoustic'));
  vi.mocked(api.triggerDedupScan).mockImplementation(trigger('find', 'op-find'));
  vi.mocked(api.triggerDedupLLM).mockImplementation(trigger('llm', 'op-llm'));
  vi.mocked(api.getOperationStatus).mockImplementation(async (id: string) => {
    calls.push(`wait:${id}`);
    return done(id);
  });
  vi.mocked(api.rescoreDedupCandidates).mockImplementation(async (apply?: boolean) => {
    calls.push(`rescore:${apply}`);
    return RESCORE;
  });
});

describe('runDedupPipeline', () => {
  it('runs each step only after the previous one finished, ending in a dry-run rescore', async () => {
    const result = await runDedupPipeline({ pollIntervalMs: 0 });

    expect(calls).toEqual([
      'embed',
      'wait:op-embed',
      'acoustic',
      'wait:op-acoustic',
      'find',
      'wait:op-find',
      'llm',
      'wait:op-llm',
      // Never apply=true: the run must not write scores or merge anything.
      'rescore:false',
    ]);
    expect(result).toEqual(RESCORE);
  });

  it('reports progress for every step in order', async () => {
    const seen: string[] = [];
    await runDedupPipeline({
      pollIntervalMs: 0,
      onProgress: (p) => {
        if (!p.op) seen.push(p.step.id);
      },
    });
    expect(seen).toEqual(DEDUP_PIPELINE_STEPS.map((s) => s.id));
  });

  it('stops at a step whose operation fails, and runs nothing after it', async () => {
    vi.mocked(api.getOperationStatus).mockImplementation(async (id: string) => {
      calls.push(`wait:${id}`);
      return id === 'op-acoustic' ? done(id, 'failed', 'fpcalc missing') : done(id);
    });

    const err = await runDedupPipeline({ pollIntervalMs: 0 }).catch((e) => e);

    expect(err).toBeInstanceOf(PipelineStepError);
    expect((err as PipelineStepError).step.id).toBe('acoustic');
    expect((err as Error).message).toMatch(/failed.*fpcalc missing/);
    expect(calls).toEqual(['embed', 'wait:op-embed', 'acoustic', 'wait:op-acoustic']);
    expect(api.triggerDedupScan).not.toHaveBeenCalled();
    expect(api.rescoreDedupCandidates).not.toHaveBeenCalled();
  });

  it('treats a cancelled step as a stop, not a success', async () => {
    vi.mocked(api.getOperationStatus).mockImplementation(async (id: string) =>
      id === 'op-embed' ? done(id, 'canceled') : done(id)
    );
    await expect(runDedupPipeline({ pollIntervalMs: 0 })).rejects.toBeInstanceOf(PipelineStepError);
    expect(api.triggerDedupAcoustID).not.toHaveBeenCalled();
  });

  it('stops when a step cannot be started', async () => {
    vi.mocked(api.triggerDedupScan).mockRejectedValue(new Error('503'));
    await expect(runDedupPipeline({ pollIntervalMs: 0 })).rejects.toBeInstanceOf(PipelineStepError);
    expect(api.triggerDedupLLM).not.toHaveBeenCalled();
  });

  it('refuses to continue when the server returns no operation id to wait on', async () => {
    vi.mocked(api.triggerEmbedScan).mockResolvedValue({} as api.Operation);
    await expect(runDedupPipeline({ pollIntervalMs: 0 })).rejects.toThrow(/operation id/);
    expect(api.triggerDedupAcoustID).not.toHaveBeenCalled();
  });

  it('waits while an operation is still running, then moves on', async () => {
    let reads = 0;
    vi.mocked(api.getOperationStatus).mockImplementation(async (id: string) => {
      if (id === 'op-embed' && reads++ < 2) return done(id, 'running');
      return done(id);
    });
    await runDedupPipeline({ pollIntervalMs: 0 });
    expect(reads).toBe(3);
    expect(api.triggerDedupAcoustID).toHaveBeenCalled();
  });

  it('rides out a couple of failed status reads instead of ending the run', async () => {
    let failures = 0;
    vi.mocked(api.getOperationStatus).mockImplementation(async (id: string) => {
      if (id === 'op-find' && failures < MAX_CONSECUTIVE_POLL_FAILURES - 1) {
        failures++;
        throw new Error('network blip');
      }
      return done(id);
    });
    await expect(runDedupPipeline({ pollIntervalMs: 0 })).resolves.toEqual(RESCORE);
    expect(api.triggerDedupLLM).toHaveBeenCalled();
  });

  it('gives up on a step when its status stays unreadable', async () => {
    vi.mocked(api.getOperationStatus).mockImplementation(async (id: string) => {
      if (id === 'op-find') throw new Error('server gone');
      return done(id);
    });
    const err = await runDedupPipeline({ pollIntervalMs: 0 }).catch((e) => e);
    expect(err).toBeInstanceOf(PipelineStepError);
    expect((err as PipelineStepError).step.id).toBe('find');
    expect(api.getOperationStatus).toHaveBeenCalledTimes(2 + MAX_CONSECUTIVE_POLL_FAILURES);
    expect(api.triggerDedupLLM).not.toHaveBeenCalled();
  });

  it('leaves out skipped steps and still runs the rest in order', async () => {
    await runDedupPipeline({ pollIntervalMs: 0, skip: ['embeddings'] });
    expect(api.triggerEmbedScan).not.toHaveBeenCalled();
    expect(calls[0]).toBe('acoustic');
    expect(calls.at(-1)).toBe('rescore:false');
  });

  it('stops before the next step when asked to', async () => {
    let stop = false;
    vi.mocked(api.getOperationStatus).mockImplementation(async (id: string) => {
      stop = true;
      return done(id);
    });
    await expect(
      runDedupPipeline({ pollIntervalMs: 0, shouldStop: () => stop })
    ).rejects.toBeInstanceOf(PipelineStoppedError);
    expect(api.triggerEmbedScan).toHaveBeenCalledTimes(1);
    expect(api.triggerDedupAcoustID).not.toHaveBeenCalled();
  });
});

describe('autoMergeRisks', () => {
  const dedup = (over: Partial<api.DedupConfig>) =>
    ({
      dedup: { auto_merge_enabled: false, llm_auto_merge_high_confidence: false, ...over },
    }) as Pick<api.Config, 'dedup'>;

  it('is empty when neither automatic merge is on', () => {
    expect(autoMergeRisks(dedup({}))).toEqual([]);
  });

  it('names each automatic merge that is on', () => {
    expect(autoMergeRisks(dedup({ auto_merge_enabled: true }))).toHaveLength(1);
    expect(
      autoMergeRisks(dedup({ auto_merge_enabled: true, llm_auto_merge_high_confidence: true }))
    ).toHaveLength(2);
  });

  it('treats missing settings as a risk, because the server default merges', () => {
    expect(autoMergeRisks({} as Pick<api.Config, 'dedup'>)).toHaveLength(1);
  });
});
