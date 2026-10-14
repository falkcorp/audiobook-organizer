// file: web/src/components/dedup/__tests__/DedupReconcileTab.test.tsx
// version: 1.0.0
// guid: 4d8b2e17-6a39-4c05-9f71-3e5a0c8d2b46
// last-edited: 2026-10-14
//
// The reconcile tab shows the last scan with a readable result even when a newer
// run was canceled, failed or was interrupted, and says so. A run still in
// flight arrives beside that scan and is polled to completion.

import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../../services/api';
import { ReconcileTab } from '../DedupReconcileTab';

vi.mock('../../../services/api');

const GOOD_AT = '2026-10-13T03:00:00Z';
const ATTEMPT_AT = '2026-10-14T03:00:00Z';

const op = (over: Partial<api.Operation>): api.Operation => ({
  id: 'op',
  type: 'reconcile_scan',
  status: 'completed',
  progress: 0,
  total: 0,
  message: '',
  created_at: '2026-10-13T02:59:00Z',
  ...over,
});

const goodScan = op({ id: 'good', completed_at: GOOD_AT });

const preview = {
  broken_records: [{ book_id: 'b1', title: 'Synthetic', file_path: '/library/gone.m4b' }],
  untracked_files: [],
  matches: [],
  unmatched_books: [],
} as unknown as api.ReconcilePreview;

const renderTab = () =>
  render(
    <MemoryRouter>
      <ReconcileTab />
    </MemoryRouter>
  );

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '' } as api.Config);
});

describe('ReconcileTab newer attempt', () => {
  it('keeps the last good scan on screen and says the newer run was canceled', async () => {
    vi.mocked(api.getLatestReconcileScan).mockResolvedValue({
      operation: goodScan,
      preview,
      latest_attempt: op({ id: 'nightly', status: 'canceled', completed_at: ATTEMPT_AT }),
    });
    renderTab();

    const notice = await screen.findByTestId('reconcile-newer-attempt');
    expect(notice).toHaveTextContent(
      `The latest scan run was canceled at ${new Date(ATTEMPT_AT).toLocaleString()}; ` +
        `showing the scan from ${new Date(GOOD_AT).toLocaleString()}.`
    );
    // The good preview is still rendered, not blanked by the newer run.
    expect(screen.getByText('1 broken records')).toBeInTheDocument();
    expect(api.pollOperation).not.toHaveBeenCalled();
  });

  it.each([
    ['failed', /failed at/],
    ['interrupted_dropped', /was interrupted at/],
    ['completed', /finished but its result could not be read at/],
  ])('describes a %s newer run', async (status, wording) => {
    vi.mocked(api.getLatestReconcileScan).mockResolvedValue({
      operation: goodScan,
      preview,
      latest_attempt: op({ id: 'newer', status, completed_at: ATTEMPT_AT }),
    });
    renderTab();
    expect(await screen.findByTestId('reconcile-newer-attempt')).toHaveTextContent(wording);
    expect(screen.getByText('1 broken records')).toBeInTheDocument();
  });

  it('shows no notice when the newest run is the scan on screen', async () => {
    vi.mocked(api.getLatestReconcileScan).mockResolvedValue({
      operation: goodScan,
      preview,
      latest_attempt: null,
    });
    renderTab();
    expect(await screen.findByText('1 broken records')).toBeInTheDocument();
    expect(screen.queryByTestId('reconcile-newer-attempt')).not.toBeInTheDocument();
  });

  it('polls a run that is still in flight, beside the last good scan', async () => {
    vi.mocked(api.getLatestReconcileScan)
      .mockResolvedValueOnce({
        operation: goodScan,
        preview,
        latest_attempt: op({ id: 'live', status: 'running' }),
      })
      .mockResolvedValue({
        operation: op({ id: 'live', completed_at: ATTEMPT_AT }),
        preview,
        latest_attempt: null,
      });
    vi.mocked(api.pollOperation).mockResolvedValue(op({ id: 'live', status: 'completed' }));
    renderTab();

    await waitFor(() =>
      expect(api.pollOperation).toHaveBeenCalledWith('live', expect.any(Function))
    );
    expect(await screen.findByText('1 broken records')).toBeInTheDocument();
    expect(screen.queryByTestId('reconcile-newer-attempt')).not.toBeInTheDocument();
  });

  it('still polls a first-ever scan that arrives as the operation itself', async () => {
    vi.mocked(api.getLatestReconcileScan)
      .mockResolvedValueOnce({ operation: op({ id: 'first', status: 'running' }), preview: null })
      .mockResolvedValue({
        operation: op({ id: 'first', completed_at: ATTEMPT_AT }),
        preview,
        latest_attempt: null,
      });
    vi.mocked(api.pollOperation).mockResolvedValue(op({ id: 'first', status: 'completed' }));
    renderTab();

    await waitFor(() =>
      expect(api.pollOperation).toHaveBeenCalledWith('first', expect.any(Function))
    );
    expect(await screen.findByText('1 broken records')).toBeInTheDocument();
  });
});
