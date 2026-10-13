// file: web/src/components/review/lanes/useAIScans.test.ts
// version: 1.0.0
// guid: 6a3d9f20-1b84-4e57-8c02-d5f7e19a4b63
// last-edited: 2026-10-13

/**
 * The AI scans sub-view's data layer, ported from DedupAIReviewTab.test.tsx and
 * the AI part of dedupTabs.selectAll.test.tsx, then extended for what the lane
 * changes: abortable reads, four states each for the history and the results,
 * and no timer.
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
      author_ids: [id],
      source: 'full_scan',
    },
  };
}

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
  vi.mocked(reads.fetchAIScanResults).mockResolvedValue(MIXED);
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
    vi.mocked(reads.fetchAIScanResults).mockResolvedValue([]);
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
  it('sends the selected ids and re-reads the rows, so Applied shows', async () => {
    vi.mocked(api.applyAIScanResults).mockResolvedValue({ applied: 2, errors: [] });
    const h = mount();
    await open(h, 5);
    act(() => h.result.current.selection.togglePage());
    await act(() => h.result.current.applySelected());
    expect(api.applyAIScanResults).toHaveBeenCalledTimes(1);
    expect(vi.mocked(api.applyAIScanResults).mock.calls[0][0]).toBe(5);
    expect([...vi.mocked(api.applyAIScanResults).mock.calls[0][1]].sort()).toEqual([1, 3, 4]);
    expect(h.result.current.notice).toEqual({ severity: 'success', text: 'Applied 2 of 3 selected results.' });
    expect(h.result.current.selection.selectedCount).toBe(0);
    await waitFor(() => expect(reads.fetchAIScanResults).toHaveBeenCalledTimes(2));
  });

  it('says so when the server could not mark some of the results', async () => {
    vi.mocked(api.applyAIScanResults).mockResolvedValue({ applied: 1, errors: ['result 3: boom'] });
    const h = mount();
    await open(h, 5);
    act(() => h.result.current.selection.toggle(1));
    act(() => h.result.current.selection.toggle(3));
    await act(() => h.result.current.applySelected());
    expect(h.result.current.notice?.severity).toBe('warning');
    expect(h.result.current.notice?.text).toContain('Applied 1 of 2');
    expect(h.result.current.notice?.text).toContain('result 3: boom');
  });

  it('apply is refused for a superseded scan: the server\'s message is shown and the scan reloads as superseded', async () => {
    const message = 'scan 5 was superseded by scan 12; apply from scan 12 instead';
    vi.mocked(api.applyAIScanResults).mockRejectedValue(new api.ApiError(message, 409));
    const h = mount();
    await open(h, 5);
    // Re-reads now find it superseded.
    vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(5, { status: 'superseded', superseded_by: 12 }), scan(6)]);
    act(() => h.result.current.selection.toggle(1));
    await act(() => h.result.current.applySelected());

    expect(h.result.current.notice).toEqual({ severity: 'error', text: message });
    expect(h.result.current.selection.selectedCount).toBe(0);
    await waitFor(() => expect(h.result.current.superseded).toBe(true));
    expect(h.result.current.selection.checkboxProps(1).disabled).toBe(true);
  });

  it('another failure keeps the selection so Apply can be tried again', async () => {
    vi.mocked(api.applyAIScanResults).mockRejectedValue(new Error('network down'));
    const h = mount();
    await open(h, 5);
    act(() => h.result.current.selection.toggle(1));
    await act(() => h.result.current.applySelected());
    expect(h.result.current.notice).toEqual({ severity: 'error', text: 'network down' });
    expect(h.result.current.selection.selectedCount).toBe(1);
    expect(h.result.current.applying).toBe(false);
  });

  it('a second click while the first is in flight sends nothing', async () => {
    const releases: Array<(v: { applied: number; errors: string[] }) => void> = [];
    vi.mocked(api.applyAIScanResults).mockImplementation(
      () => new Promise((resolve) => releases.push(resolve))
    );
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
      releases.forEach((release) => release({ applied: 1, errors: [] }));
      await Promise.all([first, second]);
    });
    expect(h.result.current.applying).toBe(false);
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
    expect(h.result.current.startDisabledReason).toBe('Scan #7 is still running.');

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
