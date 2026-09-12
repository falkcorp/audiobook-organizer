// file: web/src/hooks/__tests__/useImportFolderHandlers.test.ts
// version: 1.0.0
// guid: 6cf220b3-e190-4e01-b7ba-d4c96eeb6f85
// last-edited: 2026-09-12

import type { SetStateAction } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../services/api';
import {
  scanErrorsFromLogs,
  useImportFolderHandlers,
} from '../useImportFolderHandlers';
import type { ScanStatus } from '../useSettingsHandlers';

vi.mock('../../services/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../services/api')>();
  return {
    ...actual,
    startScan: vi.fn(),
    getOperationV2: vi.fn(),
    getOperationLogs: vi.fn(),
  };
});

const folder = { id: 7, path: 'imports/incoming' } as api.ImportPath;
const badPath = 'imports/incoming/bad.m4b';
const badReason = 'ProcessFile: open "imports/incoming/bad.m4b": permission denied';

function op(fields: Record<string, unknown>): api.OperationV2 {
  return { id: 'op-1', def_id: 'library.scan', ...fields } as unknown as api.OperationV2;
}

function logs(entries: Array<Record<string, unknown>>): api.OperationLog[] {
  return entries as unknown as api.OperationLog[];
}

function harness() {
  let statuses: Record<number, ScanStatus> = {};
  const intervals: Record<number, number> = {};
  const setScanStatuses = vi.fn(
    (update: SetStateAction<Record<number, ScanStatus>>) => {
      statuses = typeof update === 'function' ? update(statuses) : update;
    }
  );
  const handlers = useImportFolderHandlers({
    setImportFolders: vi.fn(),
    setScanStatuses,
    setCancelScanTarget: vi.fn(),
    setScanErrorTarget: vi.fn(),
    setNewFolderPath: vi.fn(),
    setShowFolderBrowser: vi.fn(),
    setAddFolderDialogOpen: vi.fn(),
    scanIntervalsRef: { current: intervals },
    cancelScanTarget: null,
    scanStatuses: {},
    newFolderPath: '',
  });
  return { handlers, intervals, status: () => statuses[folder.id] };
}

describe('useImportFolderHandlers scan polling', () => {
  beforeEach(() => {
    vi.useFakeTimers();
    vi.mocked(api.startScan).mockResolvedValue({ id: 'op-1' });
  });

  afterEach(() => {
    vi.useRealTimers();
    vi.clearAllMocks();
  });

  it('reads per-file failures off the operation log once the scan finishes', async () => {
    vi.mocked(api.getOperationV2)
      .mockResolvedValueOnce(op({ status: 'running', progress_current: 1, progress_total: 2 }))
      .mockResolvedValueOnce(op({ status: 'completed', progress_current: 2, progress_total: 2 }));
    vi.mocked(api.getOperationLogs).mockResolvedValue(
      logs([
        { level: 'info', message: 'operation started', attrs: {} },
        {
          level: 'warn',
          message: 'scan: file failed',
          attrs: { file_path: badPath, stage: 'read', reason: badReason },
        },
        {
          level: 'warn',
          message: 'scan finished with 1 file failure(s); 1 listed individually',
          attrs: { files_failed: 1, files_listed: 1, files_omitted: 0 },
        },
      ])
    );
    const h = harness();

    await h.handlers.handleScanImportFolder(folder);
    expect(h.status()).toMatchObject({ status: 'scanning', operationId: 'op-1', errors: [] });

    await vi.advanceTimersByTimeAsync(1000);
    expect(h.status()).toMatchObject({ status: 'scanning', scanned: 1, total: 2 });
    expect(api.getOperationLogs).not.toHaveBeenCalled();

    await vi.advanceTimersByTimeAsync(1000);
    expect(h.status()).toEqual({
      status: 'complete',
      scanned: 2,
      total: 2,
      operationId: 'op-1',
      errors: [`${badPath}: ${badReason}`],
    });
    expect(api.getOperationLogs).toHaveBeenCalledWith('op-1', 200);
    expect(h.intervals[folder.id]).toBeUndefined();
  });

  it('reports a failed operation as an error, message first', async () => {
    vi.mocked(api.getOperationV2).mockResolvedValue(
      op({ status: 'failed', error_message: 'scan canceled', progress_current: 0, progress_total: 0 })
    );
    vi.mocked(api.getOperationLogs).mockResolvedValue(logs([]));
    const h = harness();

    await h.handlers.handleScanImportFolder(folder);
    await vi.advanceTimersByTimeAsync(1000);

    expect(h.status()).toMatchObject({ status: 'error', errors: ['scan canceled'] });
  });

  it('surfaces a log-read failure instead of reporting a clean scan', async () => {
    vi.mocked(api.getOperationV2).mockResolvedValue(op({ status: 'completed' }));
    vi.mocked(api.getOperationLogs).mockRejectedValue(new Error('HTTP 500'));
    const h = harness();

    await h.handlers.handleScanImportFolder(folder);
    await vi.advanceTimersByTimeAsync(1000);

    expect(h.status()?.status).toBe('complete');
    expect(h.status()?.errors).toHaveLength(1);
    expect(h.status()?.errors?.[0]).toContain('HTTP 500');
  });
});

describe('scanErrorsFromLogs', () => {
  it('lists the sample and states how many more failed', () => {
    const out = scanErrorsFromLogs(
      logs([
        { level: 'warn', message: 'scan: file failed', attrs: { file_path: 'a.mp3', reason: 'r1' } },
        { level: 'warn', message: 'scan: file failed', attrs: { file_path: 'b.mp3', reason: 'r2' } },
        {
          level: 'warn',
          message: 'scan finished with 30 file failure(s); 2 listed individually',
          attrs: { files_failed: 30, files_listed: 2, files_omitted: 28 },
        },
      ])
    );
    expect(out).toEqual(['a.mp3: r1', 'b.mp3: r2', '...and 28 more file(s) failed (not listed)']);
  });

  it('ignores lines without failure attrs', () => {
    expect(scanErrorsFromLogs(logs([{ level: 'info', message: 'started' }]))).toEqual([]);
  });
});
