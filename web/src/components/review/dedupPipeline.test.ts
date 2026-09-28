// file: web/src/components/review/dedupPipeline.test.ts
// version: 2.1.0
// guid: 6d1a8f35-2c94-4e7b-b3f0-9a5e4c2d7b18
// last-edited: 2026-09-28

import { describe, it, expect, beforeEach, vi } from 'vitest';
import * as api from '../../services/api';
import {
  OperationGoneError,
  autoMergeRisks,
  followOperation,
  previewRunAll,
  readRunAllResult,
  startDedupRun,
} from './dedupPipeline';

vi.mock('../../services/api');

const actualApi = await vi.importActual<typeof api>('../../services/api');

function op(id: string, status: string, extra: Partial<api.Operation> = {}): api.Operation {
  return { id, status, progress: 0, total: 0, message: '', ...extra } as api.Operation;
}

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(api.isOperationTerminal).mockImplementation(actualApi.isOperationTerminal);
});

describe('startDedupRun', () => {
  it('starts ONE server op for "Find all duplicates" and chains nothing itself', async () => {
    vi.mocked(api.startDedupRunAll).mockResolvedValue(op('run-1', 'queued'));
    await expect(startDedupRun('all')).resolves.toBe('run-1');
    expect(api.startDedupRunAll).toHaveBeenCalledTimes(1);
    // dedup.run-all previews when the mode is omitted, so a real run says so.
    expect(api.startDedupRunAll).toHaveBeenCalledWith(false);
    // The browser no longer drives the individual steps.
    expect(api.triggerEmbedScan).not.toHaveBeenCalled();
    expect(api.triggerDedupAcoustID).not.toHaveBeenCalled();
    expect(api.triggerDedupScan).not.toHaveBeenCalled();
    expect(api.triggerDedupLLM).not.toHaveBeenCalled();
    expect(api.rescoreDedupCandidates).not.toHaveBeenCalled();
  });

  it('runs the Dupes-tab scan (dedup.full-scan) for "Force full rescan", not the /dedup page scan', async () => {
    vi.mocked(api.triggerDedupScan).mockResolvedValue(op('scan-1', 'queued'));
    await expect(startDedupRun('rescan')).resolves.toBe('scan-1');
    expect(api.triggerDedupScan).toHaveBeenCalledTimes(1);
    expect(api.scanBookDuplicates).not.toHaveBeenCalled();
  });

  it('fails when the server returns no operation id', async () => {
    vi.mocked(api.startDedupRunAll).mockResolvedValue({} as api.Operation);
    await expect(startDedupRun('all')).rejects.toThrow(/operation id/);
  });
});

describe('followOperation', () => {
  it('polls until the op is terminal and reports every update', async () => {
    const seq = [
      op('r', 'queued'),
      op('r', 'running', { progress: 1, total: 5 }),
      op('r', 'completed'),
    ];
    vi.mocked(api.getOperationStatus).mockImplementation(async () => seq.shift()!);
    const seen: string[] = [];
    const final = await followOperation('r', {
      pollIntervalMs: 0,
      onUpdate: (o) => seen.push(o.status),
    });
    expect(final.status).toBe('completed');
    expect(seen).toEqual(['queued', 'running', 'completed']);
  });

  it('keeps following through a server restart (interrupted_quiesced) instead of calling it failed', async () => {
    const seq = [
      op('r', 'running'),
      op('r', 'interrupted_quiesced'),
      op('r', 'queued'),
      op('r', 'completed'),
    ];
    vi.mocked(api.getOperationStatus).mockImplementation(async () => seq.shift()!);
    const final = await followOperation('r', { pollIntervalMs: 0 });
    expect(final.status).toBe('completed');
  });

  it('returns a failed op as final', async () => {
    vi.mocked(api.getOperationStatus).mockResolvedValue(op('r', 'failed', { error_message: 'x' }));
    await expect(followOperation('r', { pollIntervalMs: 0 })).resolves.toMatchObject({
      status: 'failed',
    });
  });

  it('keeps retrying through a server outage (a deploy) and reports each failed read', async () => {
    // running -> server down for 8 reads -> back, still running -> completed
    let n = 0;
    vi.mocked(api.getOperationStatus).mockImplementation(async () => {
      n++;
      if (n === 1) return op('r', 'running');
      if (n <= 9) throw new Error('fetch failed');
      return n === 10 ? op('r', 'running') : op('r', 'completed');
    });
    const failures: number[] = [];
    await expect(
      followOperation('r', { pollIntervalMs: 0, onUnreachable: (f) => failures.push(f) })
    ).resolves.toMatchObject({ status: 'completed' });
    expect(failures).toEqual([1, 2, 3, 4, 5, 6, 7, 8]);
  });

  it('stops with OperationGoneError when the op no longer exists', async () => {
    vi.mocked(api.getOperationStatus).mockRejectedValue(
      Object.assign(new Error('not found'), { status: 404 })
    );
    await expect(followOperation('r', { pollIntervalMs: 0 })).rejects.toBeInstanceOf(
      OperationGoneError
    );
    expect(api.getOperationStatus).toHaveBeenCalledTimes(1);
  });
});

describe('previewRunAll', () => {
  it('runs dedup.run-all as a preview and returns its result', async () => {
    const result = {
      dry_run: true,
      preview_skipped: [],
      steps: [],
      skipped: null,
      rescore_preview: null,
    };
    vi.mocked(api.startDedupRunAll).mockResolvedValue(op('p-1', 'queued'));
    vi.mocked(api.getOperationStatus).mockResolvedValue(op('p-1', 'completed'));
    vi.mocked(api.getOperationResult).mockResolvedValue({ result_data: result });
    await expect(previewRunAll({ pollIntervalMs: 0 })).resolves.toEqual(result);
    expect(api.startDedupRunAll).toHaveBeenCalledWith(true);
    expect(api.startDedupRunAll).not.toHaveBeenCalledWith(false);
  });

  it('returns null when the preview fails', async () => {
    vi.mocked(api.startDedupRunAll).mockResolvedValue(op('p-1', 'queued'));
    vi.mocked(api.getOperationStatus).mockResolvedValue(op('p-1', 'failed'));
    await expect(previewRunAll({ pollIntervalMs: 0 })).resolves.toBeNull();
  });
});

describe('readRunAllResult', () => {
  it('returns the op result object', async () => {
    const result = { steps: [], skipped: null, rescore_preview: { inspected: 3, changed: 1 } };
    vi.mocked(api.getOperationResult).mockResolvedValue({ result_data: result });
    await expect(readRunAllResult('r')).resolves.toEqual(result);
  });

  it('returns null rather than failing when the result cannot be read', async () => {
    vi.mocked(api.getOperationResult).mockRejectedValue(new Error('404'));
    await expect(readRunAllResult('r')).resolves.toBeNull();
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

  it('only names the identical-copy link for the rescan, which has no AI step', () => {
    expect(autoMergeRisks(dedup({ llm_auto_merge_high_confidence: true }), 'rescan')).toEqual([]);
    expect(
      autoMergeRisks(
        dedup({ auto_merge_enabled: true, llm_auto_merge_high_confidence: true }),
        'rescan'
      )
    ).toHaveLength(1);
  });

  it('treats missing settings as a risk, because the server default merges', () => {
    expect(autoMergeRisks({} as Pick<api.Config, 'dedup'>)).toHaveLength(1);
    expect(autoMergeRisks({} as Pick<api.Config, 'dedup'>, 'rescan')).toHaveLength(1);
  });
});
