// file: web/src/components/system/ChapterConsolidationCard.test.tsx
// version: 1.3.0
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
  fingerprint: 'fp-1',
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

// Selection is opt-in: tick every non-low-confidence group of the preview.
function selectAll() {
  fireEvent.click(screen.getByRole('button', { name: /^Select all/ }));
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

  it('a scan lists blocked groups with their reasons, confidence and gaps', async () => {
    mockRun(
      result({
        job: 'scan-chapter-groups',
        groups_blocked: 1,
        groups: [
          { ...group, confidence: 'medium', gaps: ['7'] },
          {
            ...group,
            primary_book_id: 'nb01',
            book_ids: ['nb01', 'nb02'],
            source_book_ids: ['nb02'],
            status: 'blocked',
            blockers: ['all 2 members are non-primary versions'],
          },
        ],
      })
    );
    render(<ChapterConsolidationCard />);
    fireEvent.click(screen.getByRole('button', { name: 'Scan for Chapter Groups' }));
    await waitFor(() => expect(screen.getByTestId('chapter-summary')).toBeInTheDocument());
    expect(screen.getByTestId('chapter-summary')).toHaveTextContent('1 more blocked');
    fireEvent.click(screen.getByRole('button', { name: /Show 2 group/ }));
    expect(screen.getByText(/medium confidence · missing 7/)).toBeInTheDocument();
    expect(screen.getByText(/blocked: all 2 members are non-primary versions/)).toBeInTheDocument();
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

    // Nothing is pre-ticked: the real merge stays disabled until a group is chosen.
    fireEvent.click(screen.getByLabelText('Dry Run'));
    expect(screen.getByRole('button', { name: 'Merge Chapter Groups…' })).toBeDisabled();
    selectAll();

    // Real merge opens a confirm dialog and does NOT start a job yet.
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
    selectAll();
    fireEvent.click(screen.getByLabelText('Dry Run'));
    fireEvent.click(screen.getByRole('button', { name: 'Merge Chapter Groups…' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }));
    expect(api.runMaintenanceJob).toHaveBeenCalledTimes(1);
  });

  it('the confirm count leaves out groups the preview would skip', async () => {
    mockRun(
      result({
        groups_found: 2,
        books_merged: 2,
        books_skipped: 1,
        groups: [
          { ...group, status: 'would_merge' },
          {
            ...group,
            primary_book_id: 'x01',
            source_book_ids: ['x02'],
            status: 'would_skip',
          },
        ],
      })
    );
    render(<ChapterConsolidationCard />);
    fireEvent.click(screen.getByRole('button', { name: 'Preview Merge' }));
    await waitFor(() => expect(screen.getByTestId('chapter-summary')).toBeInTheDocument());
    selectAll();
    fireEvent.click(screen.getByLabelText('Dry Run'));
    fireEvent.click(screen.getByRole('button', { name: 'Merge Chapter Groups…' }));
    expect(await screen.findByRole('button', { name: 'Merge 2 record(s)' })).toBeInTheDocument();
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

  it('confirm sends the previewed params and groups, never the form as edited since', async () => {
    mockRun(
      result({
        params: {
          dry_run: true,
          min_files: 2,
          max_per_file_duration: 600,
          path_prefix: '/lib/Foo',
        },
      })
    );
    render(<ChapterConsolidationCard />);
    fireEvent.change(screen.getByLabelText('Path prefix (optional)'), {
      target: { value: '/lib/Foo' },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Preview Merge' }));
    await waitFor(() => expect(screen.getByTestId('chapter-summary')).toBeInTheDocument());

    selectAll();
    // The operator clears the prefix after reviewing a /lib/Foo preview.
    fireEvent.change(screen.getByLabelText('Path prefix (optional)'), { target: { value: '' } });
    fireEvent.click(screen.getByLabelText('Dry Run'));
    fireEvent.click(screen.getByRole('button', { name: 'Merge Chapter Groups…' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Merge 2 record(s)' }));
    await waitFor(() => expect(api.runMaintenanceJob).toHaveBeenCalledTimes(2));
    expect(vi.mocked(api.runMaintenanceJob).mock.calls[1]).toEqual([
      'merge-chapter-groups',
      false,
      {
        min_files: 2,
        max_per_file_duration: 600,
        path_prefix: '/lib/Foo',
        groups: [
          { primary_book_id: 'ch01', book_ids: ['ch01', 'ch02', 'ch03'], fingerprint: 'fp-1' },
        ],
      },
    ]);
  });

  it('a scan result does not unlock a real merge; only a merge preview does', async () => {
    mockRun(result({ job: 'scan-chapter-groups', dry_run: true, groups: [group] }));
    render(<ChapterConsolidationCard />);
    fireEvent.click(screen.getByRole('button', { name: 'Scan for Chapter Groups' }));
    await waitFor(() => expect(screen.getByTestId('chapter-summary')).toBeInTheDocument());
    fireEvent.click(screen.getByLabelText('Dry Run'));
    expect(screen.getByRole('button', { name: 'Merge Chapter Groups…' })).toBeDisabled();
  });

  it('a deselected group is left out of the request and the count', async () => {
    mockRun(
      result({
        groups_found: 2,
        books_merged: 3,
        groups: [
          { ...group, status: 'would_merge' },
          {
            ...group,
            primary_book_id: 'x01',
            book_ids: ['x01', 'x02'],
            source_book_ids: ['x02'],
            fingerprint: 'fp-2',
            status: 'would_merge',
          },
        ],
      })
    );
    render(<ChapterConsolidationCard />);
    fireEvent.click(screen.getByRole('button', { name: 'Preview Merge' }));
    await waitFor(() => expect(screen.getByTestId('chapter-summary')).toBeInTheDocument());
    selectAll();
    fireEvent.click(screen.getByRole('button', { name: /Show 2 group/ }));
    fireEvent.click(screen.getByLabelText('Include x01'));
    fireEvent.click(screen.getByLabelText('Dry Run'));
    fireEvent.click(screen.getByRole('button', { name: 'Merge Chapter Groups…' }));
    fireEvent.click(await screen.findByRole('button', { name: 'Merge 2 record(s)' }));
    await waitFor(() => expect(api.runMaintenanceJob).toHaveBeenCalledTimes(2));
    const sent = vi.mocked(api.runMaintenanceJob).mock.calls[1][2] as { groups: unknown[] };
    expect(sent.groups).toHaveLength(1);
  });

  it('select all leaves out low-confidence groups; they can only be ticked one by one', async () => {
    mockRun(
      result({
        groups_found: 2,
        groups: [
          { ...group, status: 'would_merge', confidence: 'high' },
          {
            ...group,
            primary_book_id: 'lo01',
            book_ids: ['lo01', 'lo02'],
            source_book_ids: ['lo02'],
            fingerprint: 'fp-lo',
            status: 'would_merge',
            confidence: 'low',
          },
        ],
      })
    );
    render(<ChapterConsolidationCard />);
    fireEvent.click(screen.getByRole('button', { name: 'Preview Merge' }));
    await waitFor(() => expect(screen.getByTestId('chapter-summary')).toBeInTheDocument());
    fireEvent.click(screen.getByRole('button', { name: /Show 2 group/ }));
    // Opt-in: nothing ticked after a preview.
    expect(screen.getByLabelText('Include ch01')).not.toBeChecked();
    expect(screen.getByLabelText('Include lo01')).not.toBeChecked();
    fireEvent.click(
      screen.getByRole('button', { name: /Select all \(1, low confidence excluded\)/ })
    );
    expect(screen.getByLabelText('Include ch01')).toBeChecked();
    expect(screen.getByLabelText('Include lo01')).not.toBeChecked();
    fireEvent.click(screen.getByLabelText('Include lo01'));
    expect(screen.getByLabelText('Include lo01')).toBeChecked();

    // The individually ticked low group carries the acknowledgement; the
    // bulk-selected one does not.
    fireEvent.click(screen.getByLabelText('Dry Run'));
    fireEvent.click(screen.getByRole('button', { name: 'Merge Chapter Groups…' }));
    fireEvent.click(await screen.findByRole('button', { name: /^Merge \d+ record/ }));
    await waitFor(() => expect(api.runMaintenanceJob).toHaveBeenCalledTimes(2));
    const sent = vi.mocked(api.runMaintenanceJob).mock.calls[1][2] as {
      groups: api.ChapterGroupSelection[];
    };
    expect(sent.groups.find((g) => g.primary_book_id === 'lo01')?.allow_low_confidence).toBe(true);
    expect(
      sent.groups.find((g) => g.primary_book_id === 'ch01')?.allow_low_confidence
    ).toBeUndefined();
  });

  it('each group expands to list every member and shows the proposed title', async () => {
    mockRun(
      result({
        job: 'scan-chapter-groups',
        groups: [
          {
            ...group,
            index_labels: ['1', '2', '3'],
            member_titles: ['157', '158', '159'],
            member_files: ['Tale - 157.mp3', 'Tale - 158.mp3', 'Tale - 159.mp3'],
            member_durations: [600, 0, 1200],
          },
        ],
      })
    );
    render(<ChapterConsolidationCard />);
    fireEvent.click(screen.getByRole('button', { name: 'Scan for Chapter Groups' }));
    await waitFor(() => expect(screen.getByTestId('chapter-summary')).toBeInTheDocument());
    fireEvent.click(screen.getByRole('button', { name: /Show 1 group/ }));
    expect(screen.getByText(/proposed title "My Book"/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Members of ch01' }));
    const list = screen.getByTestId('members-ch01');
    expect(list).toHaveTextContent('#1 · "157" · 10 min · Tale - 157.mp3');
    expect(list).toHaveTextContent('#2 · "158" · duration unknown · Tale - 158.mp3');
    expect(list).toHaveTextContent('#3 · "159" · 20 min · Tale - 159.mp3');
  });
});
