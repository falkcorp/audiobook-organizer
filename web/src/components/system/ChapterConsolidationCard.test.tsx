// file: web/src/components/system/ChapterConsolidationCard.test.tsx
// version: 1.0.0
// guid: dad38fac-1352-4ced-9735-5811865fa668
// last-edited: 2026-09-19

import { render, screen, waitFor, fireEvent } from '@testing-library/react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { ChapterConsolidationCard } from './MaintenanceTab';
import * as api from '../../services/api';

vi.mock('../../services/api', () => ({
  runMaintenanceJob: vi.fn(),
  pollOperation: vi.fn(),
  getOperationResult: vi.fn(),
}));

const group: api.ChapterGroup = {
  primary_book_id: 'ch01',
  book_ids: ['ch01', 'ch02', 'ch03'],
  source_book_ids: ['ch02', 'ch03'],
  common_title: 'My Book',
  total_duration: 1800,
  file_count: 3,
  directory: '/lib/A/My Book',
};

function result(over: Partial<api.ChapterGroupsResult>): api.ChapterGroupsResult {
  return {
    job: 'merge-chapter-groups',
    dry_run: true,
    params: { dry_run: true, min_files: 2, max_per_file_duration: 600 },
    groups_found: 1,
    total_books_affected: 3,
    groups_skipped_unknown_duration: 0,
    books_merged: 2,
    books_skipped: 0,
    groups_failed: 0,
    groups: [{ ...group, status: 'would_merge' }],
    ...over,
  };
}

function mockRun(res: api.ChapterGroupsResult) {
  vi.mocked(api.runMaintenanceJob).mockResolvedValue({ operation_id: 'op-1' });
  vi.mocked(api.pollOperation).mockResolvedValue({
    id: 'op-1',
    status: 'completed',
  } as unknown as Awaited<ReturnType<typeof api.pollOperation>>);
  vi.mocked(api.getOperationResult).mockResolvedValue({ result_data: res });
}

describe('ChapterConsolidationCard', () => {
  beforeEach(() => {
    vi.mocked(api.runMaintenanceJob).mockReset();
    vi.mocked(api.pollOperation).mockReset();
    vi.mocked(api.getOperationResult).mockReset();
  });

  it('starts the scan job with the params and renders the structured result', async () => {
    mockRun(result({ job: 'scan-chapter-groups', groups: [group] }));
    render(<ChapterConsolidationCard />);
    fireEvent.change(screen.getByLabelText('Path prefix (optional)'), {
      target: { value: '/lib/A' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Scan for Chapter Groups' }));

    await waitFor(() => expect(screen.getByTestId('chapter-summary')).toBeInTheDocument());
    expect(api.runMaintenanceJob).toHaveBeenCalledWith('scan-chapter-groups', true, {
      min_files: 2,
      max_per_file_duration: 600,
      path_prefix: '/lib/A',
    });
    expect(api.pollOperation).toHaveBeenCalledWith('op-1', expect.any(Function));
    expect(api.getOperationResult).toHaveBeenCalledWith('op-1');
    expect(screen.getByTestId('chapter-summary')).toHaveTextContent(
      'Found 1 group(s) affecting 3 book record(s).'
    );
  });

  it('Preview Merge runs the merge job as a dry run', async () => {
    mockRun(result({}));
    render(<ChapterConsolidationCard />);
    fireEvent.click(screen.getByRole('button', { name: 'Preview Merge' }));
    await waitFor(() =>
      expect(screen.getByTestId('chapter-summary')).toHaveTextContent('[Dry run] Would merge 2')
    );
    expect(api.runMaintenanceJob).toHaveBeenCalledWith(
      'merge-chapter-groups',
      true,
      expect.any(Object)
    );
  });

  it('a real merge needs a preview and an explicit confirm naming the count', async () => {
    mockRun(result({}));
    render(<ChapterConsolidationCard />);

    // Dry Run off with no preview: the real merge button is disabled.
    fireEvent.click(screen.getByLabelText('Dry Run'));
    expect(screen.getByRole('button', { name: 'Merge Chapter Groups…' })).toBeDisabled();

    // Preview first.
    fireEvent.click(screen.getByLabelText('Dry Run'));
    fireEvent.click(screen.getByRole('button', { name: 'Preview Merge' }));
    await waitFor(() => expect(screen.getByTestId('chapter-summary')).toBeInTheDocument());
    expect(api.runMaintenanceJob).toHaveBeenCalledTimes(1);

    // Real merge opens a confirm dialog and does NOT start a job yet.
    fireEvent.click(screen.getByLabelText('Dry Run'));
    fireEvent.click(screen.getByRole('button', { name: 'Merge Chapter Groups…' }));
    const confirm = await screen.findByRole('button', { name: 'Merge 2 record(s)' });
    expect(api.runMaintenanceJob).toHaveBeenCalledTimes(1);

    mockRun(result({ dry_run: false, groups: [{ ...group, status: 'merged' }] }));
    fireEvent.click(confirm);
    await waitFor(() =>
      expect(screen.getByTestId('chapter-summary')).toHaveTextContent('Merged 2 book record(s)')
    );
    expect(api.runMaintenanceJob).toHaveBeenLastCalledWith(
      'merge-chapter-groups',
      false,
      expect.any(Object)
    );
  });

  it('cancelling the confirm dialog merges nothing', async () => {
    mockRun(result({}));
    render(<ChapterConsolidationCard />);
    fireEvent.click(screen.getByRole('button', { name: 'Preview Merge' }));
    await waitFor(() => expect(screen.getByTestId('chapter-summary')).toBeInTheDocument());
    fireEvent.click(screen.getByLabelText('Dry Run'));
    fireEvent.click(screen.getByRole('button', { name: 'Merge Chapter Groups…' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }));
    expect(api.runMaintenanceJob).toHaveBeenCalledTimes(1);
  });

  it('surfaces a failed operation instead of a result', async () => {
    vi.mocked(api.runMaintenanceJob).mockResolvedValue({ operation_id: 'op-2' });
    vi.mocked(api.pollOperation).mockResolvedValue({
      id: 'op-2',
      status: 'failed',
      error_message: 'boom',
    } as unknown as Awaited<ReturnType<typeof api.pollOperation>>);
    render(<ChapterConsolidationCard />);
    fireEvent.click(screen.getByRole('button', { name: 'Scan for Chapter Groups' }));
    expect(await screen.findByText('boom')).toBeInTheDocument();
    expect(api.getOperationResult).not.toHaveBeenCalled();
  });
});
