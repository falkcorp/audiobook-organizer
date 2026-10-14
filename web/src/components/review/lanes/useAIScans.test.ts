// file: web/src/components/review/lanes/useAIScans.test.ts
// version: 1.2.0
// guid: 6a3d9f20-1b84-4e57-8c02-d5f7e19a4b63
// last-edited: 2026-10-13

/**
 * The AI scans sub-view's data layer, ported from DedupAIReviewTab.test.tsx and
 * the AI part of dedupTabs.selectAll.test.tsx, then extended for what the lane
 * changes: abortable reads, four states each for the history and the results,
 * and no timer of its own. Apply queues an operation, so the apply tests follow that
 * operation (pollOperationV2 is mocked) instead of asserting a mark-only answer.
 *
 * The reads are mocked at their module (`aiScanReads`), the writes at the api
 * module (partially: `ApiError` stays real, because the hook tells a 409 from
 * other failures with `instanceof`).
 *
 * Fixtures are synthetic; no real library rows.
 */

import { act, renderHook, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../../services/api';
import type { AIScan, AIScanResult } from '../../../services/api';
import * as reads from './aiScanReads';
import { useAIScans } from './useAIScans';

vi.mock('../../../services/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../../services/api')>()),
  startAIScan: vi.fn(),
  applyAIScanResults: vi.fn(),
  pollOperationV2: vi.fn(),
  cancelAIScan: vi.fn(),
}));
vi.mock('./aiScanReads');

const toast = vi.fn();

function scan(id: number, over: Partial<AIScan> = {}): AIScan {
  return {
    id,
    status: 'complete',
    mode: 'realtime',
    models: { groups: 'g', full: 'f' },
    author_count: 3,
    created_at: '2026-10-01T00:00:00Z',
    ...over,
  };
}

/** A merge row: it keeps author id*10 and folds in id*10+1. */
function result(id: number, agreement: AIScanResult['agreement'] = 'agreed', applied = false): AIScanResult {
  return {
    id,
    scan_id: 5,
    agreement,
    applied,
    suggestion: {
      action: 'merge',
      canonical_name: `Author ${id}`,
      reason: 'same person',
      confidence: 'high',
      author_ids: [id * 10, id * 10 + 1],
      keep_id: id * 10,
      source: 'full_scan',
    },
  };
}

/** A row of another action: read-only in this view. */
function other(id: number, action: string): AIScanResult {
  const r = result(id);
  return { ...r, suggestion: { ...r.suggestion, action, keep_id: undefined } };
}

/** What the results read answers with. */
const read = (rows: AIScanResult[], authorNames: Record<number, string> = {}): reads.AIScanResultsRead => ({
  results: rows,
  authorNames,
});

/** The operation the apply queued, as POST /ai/scans/:id/apply answers. */
const started = (over: Partial<api.AIScanApplyStarted> = {}): api.AIScanApplyStarted => ({
  id: 'op-apply',
  type: 'ai.author-scan-apply',
  status: 'queued',
  scan_id: 5,
  results: 1,
  ...over,
});
const finished = (status: string, error_message: string | null = null) =>
  ({ id: 'op-apply', status, error_message }) as api.OperationV2;

const MIXED = [result(1, 'agreed'), result(2, 'agreed', true), result(3, 'disagreed'), result(4, 'groups_only')];

function mount(active = true) {
  return renderHook(({ on }) => useAIScans(toast, on), { initialProps: { on: active } });
}

async function open(h: ReturnType<typeof mount>, id: number) {
  await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
  act(() => h.result.current.selectScan(id));
  await waitFor(() => expect(h.result.current.resultsStatus).toBe('populated'));
}

beforeEach(() => {
  vi.resetAllMocks();
  toast.mockReset();
  vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(5), scan(6)]);
  vi.mocked(reads.fetchAIScanResults).mockResolvedValue(read(MIXED));
});

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe('the history', () => {
  it('reads nothing while the sub-view is not showing, and reads once when it opens', async () => {
    const h = mount(false);
    await act(async () => {});
    expect(reads.fetchAIScans).not.toHaveBeenCalled();
    h.rerender({ on: true });
    await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
    expect(reads.fetchAIScans).toHaveBeenCalledTimes(1);
  });

  it('lists newest first, nightly scans included', async () => {
    vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(5), scan(9, { status: 'superseded', superseded_by: 12 }), scan(12)]);
    const h = mount();
    await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
    expect(h.result.current.scans.map((s) => s.id)).toEqual([12, 9, 5]);
  });

  it('is loading, then empty when the server has no scans', async () => {
    vi.mocked(reads.fetchAIScans).mockResolvedValue([]);
    const h = mount();
    expect(h.result.current.historyStatus).toBe('loading');
    await waitFor(() => expect(h.result.current.historyStatus).toBe('empty'));
  });

  it('a failed read is an error with the message, not an empty list, and Retry reads again', async () => {
    vi.mocked(reads.fetchAIScans).mockRejectedValueOnce(new Error('boom: 500'));
    const h = mount();
    await waitFor(() => expect(h.result.current.historyStatus).toBe('error'));
    expect(h.result.current.historyError).toBe('boom: 500');
    act(() => h.result.current.retryHistory());
    await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
    expect(h.result.current.historyError).toBeNull();
  });

  it('leaving the sub-view aborts a read that is still in flight', async () => {
    vi.mocked(reads.fetchAIScans).mockImplementation(() => new Promise(() => {}));
    const h = mount();
    await waitFor(() => expect(reads.fetchAIScans).toHaveBeenCalledTimes(1));
    const signal = vi.mocked(reads.fetchAIScans).mock.calls[0][0];
    expect(signal.aborted).toBe(false);
    h.rerender({ on: false });
    expect(signal.aborted).toBe(true);
  });
});

describe('the open scan', () => {
  it('a running scan has no results to read: none are requested', async () => {
    vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(7, { status: 'scanning' })]);
    const h = mount();
    await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
    act(() => h.result.current.selectScan(7));
    expect(h.result.current.resultsStatus).toBe('idle');
    await act(async () => {});
    expect(reads.fetchAIScanResults).not.toHaveBeenCalled();
  });

  it('a results read that fails is an error with Retry, not an empty list', async () => {
    vi.mocked(reads.fetchAIScanResults).mockRejectedValueOnce(new Error('results: 500'));
    const h = mount();
    await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
    act(() => h.result.current.selectScan(5));
    await waitFor(() => expect(h.result.current.resultsStatus).toBe('error'));
    expect(h.result.current.resultsError).toBe('results: 500');
    act(() => h.result.current.retryResults());
    await waitFor(() => expect(h.result.current.resultsStatus).toBe('populated'));
  });

  it('a finished scan with no results is empty, which is not an error', async () => {
    vi.mocked(reads.fetchAIScanResults).mockResolvedValue(read([]));
    const h = mount();
    await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
    act(() => h.result.current.selectScan(5));
    await waitFor(() => expect(h.result.current.resultsStatus).toBe('empty'));
  });

  it('never shows the previous scan\'s rows while the next scan\'s are loading', async () => {
    const h = mount();
    await open(h, 5);
    vi.mocked(reads.fetchAIScanResults).mockImplementation(() => new Promise(() => {}));
    act(() => h.result.current.selectScan(6));
    expect(h.result.current.results).toEqual([]);
    expect(h.result.current.resultsStatus).toBe('loading');
  });
});

describe('agreement filter', () => {
  it('agreement counts filter results', async () => {
    const h = mount();
    await open(h, 5);
    expect(h.result.current.counts).toEqual({ all: 4, agreed: 2, groups_only: 1, full_only: 0, disagreed: 1 });

    for (const f of ['all', 'agreed', 'groups_only', 'full_only', 'disagreed'] as const) {
      act(() => h.result.current.setFilter(f));
      // A chip's number is exactly the rows it shows.
      expect(h.result.current.filtered).toHaveLength(h.result.current.counts[f]);
    }
    act(() => h.result.current.setFilter('agreed'));
    expect(h.result.current.filtered.map((r) => r.id)).toEqual([1, 2]);
  });
});

describe('selection', () => {
  it('only unapplied results are selectable', async () => {
    const h = mount();
    await open(h, 5);
    expect(h.result.current.selection.checkboxProps(1).disabled).toBe(false);
    expect(h.result.current.selection.checkboxProps(2).disabled).toBe(true);
    act(() => h.result.current.selection.togglePage());
    expect([...h.result.current.selection.selected].sort()).toEqual([1, 3, 4]);
  });

  it('only merge rows are selectable: rename, alias, split, reclassify and skip are read-only', async () => {
    const rows = [
      result(1),
      other(2, 'rename'),
      other(3, 'alias'),
      other(4, 'split'),
      other(5, 'reclassify'),
      other(6, 'skip'),
      { ...result(7), suggestion: { ...result(7).suggestion, keep_id: undefined } }, // a merge with no keep author
    ];
    vi.mocked(reads.fetchAIScanResults).mockResolvedValue(read(rows));
    const h = mount();
    await open(h, 5);
    expect(h.result.current.selection.checkboxProps(1).disabled).toBe(false);
    for (const id of [2, 3, 4, 5, 6, 7]) expect(h.result.current.selection.checkboxProps(id).disabled).toBe(true);
    expect(rows.filter(h.result.current.isApplicable).map((r) => r.id)).toEqual([1]);
    act(() => h.result.current.selection.togglePage());
    expect([...h.result.current.selection.selected]).toEqual([1]);
  });

  it('a disagreed merge row stays selectable, and shows the other phase\'s answer', async () => {
    const groups = { ...result(3, 'disagreed'), paired_with: 4, suggestion: { ...result(3).suggestion, source: 'groups_scan' } };
    const full = {
      ...result(4, 'disagreed'),
      paired_with: 3,
      suggestion: { ...result(4).suggestion, action: 'alias', keep_id: undefined, source: 'full_scan' },
    };
    vi.mocked(reads.fetchAIScanResults).mockResolvedValue(read([groups, full]));
    const h = mount();
    await open(h, 5);
    expect(h.result.current.selection.checkboxProps(3).disabled).toBe(false);
    expect(h.result.current.selection.checkboxProps(4).disabled).toBe(true);
    expect(h.result.current.pairOf(h.result.current.results[0])?.id).toBe(4);
    expect(h.result.current.pairOf(h.result.current.results[1])?.id).toBe(3);
    expect(h.result.current.pairOf(result(1))).toBeNull();
  });

  it('exposes the author names the read returned', async () => {
    vi.mocked(reads.fetchAIScanResults).mockResolvedValue(read(MIXED, { 10: 'Dana Fixture', 11: 'Fixture, Dana' }));
    const h = mount();
    await open(h, 5);
    expect(h.result.current.authorNames).toEqual({ 10: 'Dana Fixture', 11: 'Fixture, Dana' });
  });

  it('changing scan clears selection', async () => {
    const h = mount();
    await open(h, 5);
    act(() => h.result.current.selection.toggle(1));
    expect(h.result.current.selection.selectedCount).toBe(1);
    act(() => h.result.current.selectScan(6));
    await waitFor(() => expect(h.result.current.scan?.id).toBe(6));
    expect(h.result.current.selection.selectedCount).toBe(0);
  });

  it('changing the filter clears selection', async () => {
    const h = mount();
    await open(h, 5);
    act(() => h.result.current.selection.toggle(1));
    act(() => h.result.current.setFilter('disagreed'));
    expect(h.result.current.selection.selectedCount).toBe(0);
  });

  it('a superseded scan shows its results read-only: nothing is selectable', async () => {
    vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(9, { status: 'superseded', superseded_by: 12 })]);
    const h = mount();
    await open(h, 9);
    expect(h.result.current.superseded).toBe(true);
    expect(h.result.current.results).toHaveLength(4);
    for (const r of MIXED) expect(h.result.current.selection.checkboxProps(r.id).disabled).toBe(true);
    act(() => h.result.current.selection.togglePage());
    expect(h.result.current.selection.selectedCount).toBe(0);
  });
});

describe('apply', () => {
  it('queues the merge for the selected ids, follows the operation, then re-reads the rows and history', async () => {
    vi.mocked(api.applyAIScanResults).mockResolvedValue(started({ results: 3 }));
    vi.mocked(api.pollOperationV2).mockResolvedValue(finished('completed'));
    const h = mount();
    await open(h, 5);
    act(() => h.result.current.selection.togglePage());
    await act(() => h.result.current.applySelected());
    expect(api.applyAIScanResults).toHaveBeenCalledTimes(1);
    expect(vi.mocked(api.applyAIScanResults).mock.calls[0][0]).toBe(5);
    expect([...vi.mocked(api.applyAIScanResults).mock.calls[0][1]].sort()).toEqual([1, 3, 4]);
    // The operation is the one the POST named.
    await waitFor(() => expect(api.pollOperationV2).toHaveBeenCalledWith('op-apply', undefined, expect.any(Number), expect.anything()));
    await waitFor(() =>
      expect(h.result.current.notice).toEqual({ severity: 'success', text: 'Merged the authors of 3 selected results.' })
    );
    expect(h.result.current.selection.selectedCount).toBe(0);
    expect(h.result.current.applying).toBe(false);
    // Rows (Applied shows) and history are read again once it has ended, not before.
    await waitFor(() => expect(reads.fetchAIScanResults).toHaveBeenCalledTimes(2));
    await waitFor(() => expect(reads.fetchAIScans).toHaveBeenCalledTimes(2));
  });

  it('Apply stays busy until the operation ends', async () => {
    vi.mocked(api.applyAIScanResults).mockResolvedValue(started());
    const run = deferred<api.OperationV2>();
    vi.mocked(api.pollOperationV2).mockReturnValue(run.promise);
    const h = mount();
    await open(h, 5);
    act(() => h.result.current.selection.toggle(1));
    await act(() => h.result.current.applySelected());
    expect(h.result.current.applying).toBe(true);
    // No second request while the first is merging, and no re-read yet.
    act(() => h.result.current.selection.toggle(3));
    await act(() => h.result.current.applySelected());
    expect(api.applyAIScanResults).toHaveBeenCalledTimes(1);
    expect(reads.fetchAIScanResults).toHaveBeenCalledTimes(1);

    await act(async () => run.resolve(finished('completed')));
    await waitFor(() => expect(h.result.current.applying).toBe(false));
  });

  it('a run that did not finish shows the operation\'s own failure text, naming the results not applied', async () => {
    const failure = 'AI scan #5: 1 of 2 result(s) applied, 1 not applied: result 3: book b1: author name is user-locked';
    vi.mocked(api.applyAIScanResults).mockResolvedValue(started({ results: 2 }));
    vi.mocked(api.pollOperationV2).mockResolvedValue(finished('failed', failure));
    const h = mount();
    await open(h, 5);
    act(() => h.result.current.selection.toggle(1));
    act(() => h.result.current.selection.toggle(3));
    await act(() => h.result.current.applySelected());
    await waitFor(() => expect(h.result.current.notice).toEqual({ severity: 'warning', text: failure }));
    // The merged result is marked in the rows, so they are read again either way.
    await waitFor(() => expect(reads.fetchAIScanResults).toHaveBeenCalledTimes(2));
  });

  it('an operation that cannot be followed says to refresh, and does not claim success', async () => {
    vi.mocked(api.applyAIScanResults).mockResolvedValue(started());
    vi.mocked(api.pollOperationV2).mockRejectedValue(new Error('server went away'));
    const h = mount();
    await open(h, 5);
    act(() => h.result.current.selection.toggle(1));
    await act(() => h.result.current.applySelected());
    await waitFor(() => expect(h.result.current.notice?.severity).toBe('warning'));
    expect(h.result.current.notice?.text).toContain('server went away');
    expect(h.result.current.notice?.text).toContain('Refresh');
    expect(h.result.current.applying).toBe(false);
  });

  it('leaving the sub-view stops following the operation', async () => {
    vi.mocked(api.applyAIScanResults).mockResolvedValue(started());
    vi.mocked(api.pollOperationV2).mockImplementation(() => new Promise(() => {}));
    const h = mount();
    await open(h, 5);
    act(() => h.result.current.selection.toggle(1));
    await act(() => h.result.current.applySelected());
    const signal = vi.mocked(api.pollOperationV2).mock.calls[0][3]?.signal;
    expect(signal?.aborted).toBe(false);
    h.unmount();
    expect(signal?.aborted).toBe(true);
  });

  it('apply is refused for a superseded scan: the server\'s message is shown, the call rejects, and the scan reloads as superseded', async () => {
    const message = 'scan 5 was superseded by scan 12; apply from scan 12 instead';
    vi.mocked(api.applyAIScanResults).mockRejectedValue(new api.ApiError(message, 409));
    const h = mount();
    await open(h, 5);
    // Re-reads now find it superseded.
    vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(5, { status: 'superseded', superseded_by: 12 }), scan(6)]);
    act(() => h.result.current.selection.toggle(1));
    // Rejected, so the caller's startJob can show the refusal as a toast too.
    await act(async () => {
      await expect(h.result.current.applySelected()).rejects.toThrow(message);
    });

    expect(h.result.current.notice).toEqual({ severity: 'error', text: message });
    expect(h.result.current.selection.selectedCount).toBe(0);
    await waitFor(() => expect(h.result.current.superseded).toBe(true));
    expect(h.result.current.selection.checkboxProps(1).disabled).toBe(true);
    expect(api.pollOperationV2).not.toHaveBeenCalled();
  });

  it('another failure keeps the selection so Apply can be tried again', async () => {
    vi.mocked(api.applyAIScanResults).mockRejectedValue(new Error('network down'));
    const h = mount();
    await open(h, 5);
    act(() => h.result.current.selection.toggle(1));
    await act(async () => {
      await expect(h.result.current.applySelected()).rejects.toThrow('network down');
    });
    expect(h.result.current.notice).toEqual({ severity: 'error', text: 'network down' });
    expect(h.result.current.selection.selectedCount).toBe(1);
    expect(h.result.current.applying).toBe(false);
  });

  it('a second click while the first POST is in flight sends nothing', async () => {
    const releases: Array<(v: api.AIScanApplyStarted) => void> = [];
    vi.mocked(api.applyAIScanResults).mockImplementation(() => new Promise((resolve) => releases.push(resolve)));
    vi.mocked(api.pollOperationV2).mockResolvedValue(finished('completed'));
    const h = mount();
    await open(h, 5);
    act(() => h.result.current.selection.toggle(1));
    let first: Promise<void> = Promise.resolve();
    act(() => {
      first = h.result.current.applySelected();
    });
    expect(h.result.current.applying).toBe(true);
    let second: Promise<void> = Promise.resolve();
    act(() => {
      second = h.result.current.applySelected();
    });
    expect(api.applyAIScanResults).toHaveBeenCalledTimes(1);
    await act(async () => {
      releases.forEach((release) => release(started()));
      await Promise.all([first, second]);
    });
    await waitFor(() => expect(h.result.current.applying).toBe(false));
  });
});

/** A promise the test settles by hand. */
function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

describe('Start cannot queue two paid scans', () => {
  it('while the POST is in flight Start is refused, and a second call sends nothing', async () => {
    const post = deferred<AIScan>();
    vi.mocked(api.startAIScan).mockReturnValue(post.promise);
    const h = mount();
    await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
    expect(h.result.current.startDisabledReason).toBeUndefined();

    let first: Promise<AIScan> = Promise.resolve(scan(0));
    act(() => {
      first = h.result.current.startScan();
    });
    expect(h.result.current.starting).toBe(true);
    expect(h.result.current.startDisabledReason).toBe('A scan is being started.');
    await expect(h.result.current.startScan()).rejects.toThrow('already being started');
    expect(api.startAIScan).toHaveBeenCalledTimes(1);

    await act(async () => {
      post.resolve(scan(7, { status: 'pending' }));
      await first;
    });
    expect(h.result.current.starting).toBe(false);
  });

  it('after the POST answers but before the re-read does, the new scan is already in the list and Start stays refused', async () => {
    vi.mocked(api.startAIScan).mockResolvedValue(scan(7, { status: 'pending' }));
    const h = mount();
    await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
    // The re-read after the start never answers.
    vi.mocked(reads.fetchAIScans).mockImplementation(() => new Promise(() => {}));
    await act(async () => {
      await h.result.current.startScan();
    });
    expect(h.result.current.starting).toBe(false);
    expect(h.result.current.scans.map((s) => s.id)).toEqual([7, 6, 5]);
    expect(h.result.current.scan?.id).toBe(7);
    expect(h.result.current.startDisabledReason).toBe('Scan #7 is still running. Refresh if it has finished.');
  });

  it('and when that re-read fails, Start stays refused', async () => {
    vi.mocked(api.startAIScan).mockResolvedValue(scan(7, { status: 'pending' }));
    const h = mount();
    await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
    vi.mocked(reads.fetchAIScans).mockRejectedValue(new Error('history down'));
    await act(async () => {
      await h.result.current.startScan();
    });
    await waitFor(() => expect(h.result.current.historyStatus).toBe('error'));
    // The list still holds the scan we just started, so a running scan is not ruled out.
    expect(h.result.current.scans.map((s) => s.id)).toContain(7);
    expect(h.result.current.startDisabledReason).toBe('Scan #7 is still running. Refresh if it has finished.');
  });

  it('a POST the server refuses leaves the list as it was and Start available again', async () => {
    vi.mocked(api.startAIScan).mockRejectedValue(new Error('AI scan pipeline not configured'));
    const h = mount();
    await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
    await expect(h.result.current.startScan()).rejects.toThrow('not configured');
    await waitFor(() => expect(h.result.current.starting).toBe(false));
    expect(h.result.current.scans.map((s) => s.id)).toEqual([6, 5]);
    expect(h.result.current.startDisabledReason).toBeUndefined();
  });
});

describe('an apply that is still out when another scan is opened', () => {
  it('reports by toast and touches nothing of the other scan: its notice, selection and rows', async () => {
    const post = deferred<api.AIScanApplyStarted>();
    vi.mocked(api.applyAIScanResults).mockReturnValue(post.promise);
    const h = mount();
    await open(h, 5);
    act(() => h.result.current.selection.toggle(1));
    let applying: Promise<void> = Promise.resolve();
    act(() => {
      applying = h.result.current.applySelected();
    });
    expect(h.result.current.applying).toBe(true);

    // Open scan 6 and tick a row there while scan 5's apply is still out.
    act(() => h.result.current.selectScan(6));
    await waitFor(() => expect(h.result.current.scan?.id).toBe(6));
    await waitFor(() => expect(h.result.current.resultsStatus).toBe('populated'));
    act(() => h.result.current.selection.toggle(3));
    const reads6 = vi.mocked(reads.fetchAIScanResults).mock.calls.length;

    vi.mocked(api.pollOperationV2).mockResolvedValue(finished('completed'));
    await act(async () => {
      post.resolve(started());
      await applying;
    });
    await waitFor(() =>
      expect(toast).toHaveBeenCalledWith('Scan #5: Merged the authors of 1 selected result.', 'success')
    );
    expect(h.result.current.notice).toBeNull();
    expect([...h.result.current.selection.selected]).toEqual([3]);
    // Scan 6's rows were not re-read on scan 5's account.
    expect(vi.mocked(reads.fetchAIScanResults).mock.calls.length).toBe(reads6);
  });

  it('a 409 for the scan that was left still re-reads the history, but shows no error on the open scan', async () => {
    const post = deferred<api.AIScanApplyStarted>();
    vi.mocked(api.applyAIScanResults).mockReturnValue(post.promise);
    const h = mount();
    await open(h, 5);
    act(() => h.result.current.selection.toggle(1));
    let applying: Promise<void> = Promise.resolve();
    act(() => {
      // Rejected: the caller reports it, so the test absorbs it here.
      applying = h.result.current.applySelected().catch(() => undefined);
    });
    act(() => h.result.current.selectScan(6));
    await waitFor(() => expect(h.result.current.resultsStatus).toBe('populated'));
    act(() => h.result.current.selection.toggle(3));
    const historyReads = vi.mocked(reads.fetchAIScans).mock.calls.length;

    await act(async () => {
      post.reject(new api.ApiError('scan 5 was superseded by scan 12; apply from scan 12 instead', 409));
      await applying;
    });
    expect(h.result.current.notice).toBeNull();
    expect([...h.result.current.selection.selected]).toEqual([3]);
    await waitFor(() => expect(vi.mocked(reads.fetchAIScans).mock.calls.length).toBe(historyReads + 1));
    expect(toast).toHaveBeenCalledWith(
      'Scan #5: scan 5 was superseded by scan 12; apply from scan 12 instead',
      'error'
    );
  });
});

describe('a late results answer', () => {
  it('for a scan that was left never replaces the open scan\'s rows', async () => {
    const slowA = deferred<reads.AIScanResultsRead>();
    vi.mocked(reads.fetchAIScanResults).mockImplementation((id: number) =>
      // A transport that ignores its signal: scan 5's answer arrives last.
      id === 5 ? slowA.promise : Promise.resolve(read([result(11, 'agreed'), result(12, 'disagreed')]))
    );
    const h = mount();
    await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
    act(() => h.result.current.selectScan(5));
    await waitFor(() => expect(reads.fetchAIScanResults).toHaveBeenCalledWith(5, expect.anything()));
    act(() => h.result.current.selectScan(6));
    await waitFor(() => expect(h.result.current.resultsStatus).toBe('populated'));
    expect(h.result.current.results.map((r) => r.id)).toEqual([11, 12]);

    await act(async () => {
      slowA.resolve(read([result(1, 'agreed'), result(2, 'agreed')]));
      await Promise.resolve();
    });
    expect(h.result.current.scan?.id).toBe(6);
    expect(h.result.current.results.map((r) => r.id)).toEqual([11, 12]);
    expect(h.result.current.counts.all).toBe(2);
  });

  it('a late failure for a scan that was left shows no error on the open scan', async () => {
    const slowA = deferred<reads.AIScanResultsRead>();
    vi.mocked(reads.fetchAIScanResults).mockImplementation((id: number) =>
      id === 5 ? slowA.promise : Promise.resolve(read([result(11, 'agreed')]))
    );
    const h = mount();
    await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
    act(() => h.result.current.selectScan(5));
    await waitFor(() => expect(reads.fetchAIScanResults).toHaveBeenCalledWith(5, expect.anything()));
    act(() => h.result.current.selectScan(6));
    await waitFor(() => expect(h.result.current.resultsStatus).toBe('populated'));
    await act(async () => {
      slowA.reject(new Error('late failure'));
      await Promise.resolve();
    });
    expect(h.result.current.resultsStatus).toBe('populated');
    expect(h.result.current.resultsError).toBeNull();
  });
});

describe('Cancel', () => {
  it('while the POST is in flight Cancel is refused and a second call sends nothing', async () => {
    vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(7, { status: 'scanning' }), scan(5)]);
    const post = deferred<void>();
    vi.mocked(api.cancelAIScan).mockReturnValue(post.promise);
    const h = mount();
    await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
    expect(h.result.current.cancelDisabledReason).toBeUndefined();

    let first: Promise<void> = Promise.resolve();
    act(() => {
      first = h.result.current.cancelScan();
    });
    expect(h.result.current.canceling).toBe(true);
    expect(h.result.current.cancelDisabledReason).toBe('A cancel is in progress.');
    await act(() => h.result.current.cancelScan());
    expect(api.cancelAIScan).toHaveBeenCalledTimes(1);

    await act(async () => {
      post.resolve();
      await first;
    });
    expect(h.result.current.canceling).toBe(false);
  });

  it('with nothing running says so, and says to refresh if a scan was just started', async () => {
    const h = mount();
    expect(h.result.current.cancelDisabledReason).toBe('The scan history is still loading.');
    await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
    expect(h.result.current.cancelDisabledReason).toBe('No AI author scan is running. Refresh if one was just started.');
  });
});

describe('start and cancel', () => {
  it('start scan uses startJob (no local polling): one start, one re-read of the list, and no timer', async () => {
    // The old tab re-read the active scan every 5 s. Any timer of 2 s or more that
    // this feature's own code creates is a poll.
    const slow: Array<[string, unknown]> = [];
    for (const name of ['setInterval', 'setTimeout'] as const) {
      const real = globalThis[name];
      vi.spyOn(globalThis, name).mockImplementation(((fn: () => void, ms?: number, ...rest: unknown[]) => {
        // Created by this feature's own code: the first frame that is not the spy,
        // a test or a dependency is a non-test file under components/review. (A toast's
        // 5 s auto-dismiss, from the app store, is not a poll.)
        const first = (new Error().stack ?? '')
          .split('\n')
          .slice(1)
          .find((l) => /\bat /.test(l) && !/node_modules|\.test\.tsx?/.test(l));
        const ours = Boolean(first && /src\/components\/review\//.test(first));
        if ((ms ?? 0) >= 2000 && ours) slow.push([name, ms]);
        return (real as (...a: unknown[]) => unknown)(fn, ms, ...rest);
      }) as never);
    }
    vi.mocked(api.startAIScan).mockResolvedValue(scan(7, { status: 'pending' }));
    const h = mount();
    await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
    expect(reads.fetchAIScans).toHaveBeenCalledTimes(1);

    vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(7, { status: 'pending' }), scan(5), scan(6)]);
    await act(async () => {
      await h.result.current.startScan();
    });
    expect(api.startAIScan).toHaveBeenCalledWith('realtime');
    await waitFor(() => expect(h.result.current.scans.map((s) => s.id)).toEqual([7, 6, 5]));
    // The new scan is the one open.
    expect(h.result.current.scan?.id).toBe(7);
    expect(reads.fetchAIScans).toHaveBeenCalledTimes(2);
    expect(slow).toEqual([]);

    // And a quiet minute reads nothing more.
    vi.useFakeTimers();
    await act(async () => {
      await vi.advanceTimersByTimeAsync(60_000);
    });
    expect(reads.fetchAIScans).toHaveBeenCalledTimes(2);
    expect(reads.fetchAIScanResults).not.toHaveBeenCalled();
  });

  it('start scan runs in the mode the switch is on', async () => {
    vi.mocked(api.startAIScan).mockResolvedValue(scan(7, { status: 'pending', mode: 'batch' }));
    const h = mount();
    await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
    act(() => h.result.current.setMode('batch'));
    await act(async () => {
      await h.result.current.startScan();
    });
    expect(api.startAIScan).toHaveBeenCalledWith('batch');
  });

  it('start scan is refused while a scan is running, and while the history is unknown', async () => {
    vi.mocked(reads.fetchAIScans).mockResolvedValueOnce([scan(7, { status: 'enriching' }), scan(5)]);
    const h = mount();
    expect(h.result.current.startDisabledReason).toMatch(/still loading/);
    await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
    expect(h.result.current.startDisabledReason).toBe('Scan #7 is still running. Refresh if it has finished.');

    const failing = mount();
    vi.mocked(reads.fetchAIScans).mockRejectedValue(new Error('down'));
    failing.rerender({ on: false });
    failing.rerender({ on: true });
    await waitFor(() => expect(failing.result.current.historyStatus).toBe('error'));
    expect(failing.result.current.startDisabledReason).toMatch(/could not be loaded/);
  });

  it('start scan that the server refuses rejects, so startJob can report it', async () => {
    vi.mocked(api.startAIScan).mockRejectedValue(new Error('no'));
    const h = mount();
    await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
    await expect(h.result.current.startScan()).rejects.toThrow('no');
    // The next attempt is not blocked by the failed one.
    vi.mocked(api.startAIScan).mockResolvedValue(scan(7, { status: 'pending' }));
    await act(async () => {
      await h.result.current.startScan();
    });
    expect(api.startAIScan).toHaveBeenCalledTimes(2);
  });

  it('cancel scan cancels the running scan, says so, and re-reads the list', async () => {
    vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(7, { status: 'scanning' }), scan(5)]);
    vi.mocked(api.cancelAIScan).mockResolvedValue(undefined);
    const h = mount();
    await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
    expect(h.result.current.cancelTarget?.id).toBe(7);

    vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(7, { status: 'canceled' }), scan(5)]);
    await act(() => h.result.current.cancelScan());
    expect(api.cancelAIScan).toHaveBeenCalledWith(7);
    expect(toast).toHaveBeenCalledWith('Scan #7 canceled.', 'success');
    await waitFor(() => expect(h.result.current.cancelTarget).toBeNull());
  });

  it('cancel scan has nothing to cancel when every scan has finished', async () => {
    const h = mount();
    await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
    expect(h.result.current.cancelTarget).toBeNull();
    await act(() => h.result.current.cancelScan());
    expect(api.cancelAIScan).not.toHaveBeenCalled();
  });

  it('cancel scan that fails says why', async () => {
    vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(7, { status: 'scanning' })]);
    vi.mocked(api.cancelAIScan).mockRejectedValue(new Error('scan not found'));
    const h = mount();
    await waitFor(() => expect(h.result.current.historyStatus).toBe('populated'));
    await act(() => h.result.current.cancelScan());
    expect(toast).toHaveBeenCalledWith('Could not cancel scan #7: scan not found', 'error');
  });
});
