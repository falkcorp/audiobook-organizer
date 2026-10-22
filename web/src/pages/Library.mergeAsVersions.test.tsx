// file: web/src/pages/Library.mergeAsVersions.test.tsx
// version: 1.2.0
// guid: 4d8b2e61-0a73-4f95-b1c6-9e3f7a5d2c08
// last-edited: 2026-10-22

/**
 * Library "Merge as Versions": the dialog states which copy is kept, trashed
 * rows in the selection are left out of the request, the click returns once the
 * merge is queued (the dialog closes, the user keeps working, the books are
 * locked against a second submit), and the toast reports what the merge
 * OPERATION did when it ends (it used to say "Merged" when the request was
 * merely queued, so a failed merge or one that skipped a trashed loser looked
 * like a success).
 */

import { render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import userEvent from '@testing-library/user-event';
import { Library } from './Library';
import * as api from '../services/api';
import { ToastProvider } from '../components/toast/ToastProvider';
import { useAppStore } from '../stores/useAppStore';

vi.mock('../services/api', () => {
  class ApiError extends Error {
    status: number;
    data?: unknown;
    constructor(message: string, status: number, data?: unknown) {
      super(message);
      this.name = 'ApiError';
      this.status = status;
      this.data = data;
    }
  }
  return {
    ApiError,
    getOperationLogsTail: vi.fn().mockResolvedValue([]),
    getBooks: vi.fn().mockResolvedValue({
      items: [
        {
          id: 'id-1',
          title: 'Test Book',
          file_path: '/tmp/book.m4b',
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
          author_name: 'Author',
        },
        {
          id: 'id-2',
          title: 'Second Book',
          file_path: '/tmp/book2.m4b',
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
          author_name: 'Author',
        },
        {
          id: 'id-3',
          title: 'Trashed Book',
          file_path: '/tmp/book3.m4b',
          created_at: '2026-01-01T00:00:00Z',
          updated_at: '2026-01-01T00:00:00Z',
          author_name: 'Author',
          marked_for_deletion: true,
        },
      ],
      count: 3,
    }),
    searchBooks: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    searchBooksPage: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    getImportPaths: vi.fn().mockResolvedValue([]),
    countBooks: vi.fn().mockResolvedValue(2),
    getBookFacets: vi.fn().mockResolvedValue({ genres: [], languages: [] }),
    getAuthors: vi.fn().mockResolvedValue([]),
    getSeries: vi.fn().mockResolvedValue([]),
    getSystemStatus: vi.fn().mockResolvedValue({
      status: 'ok',
      library: { path: '/tmp', book_count: 2, total_size: 0 },
      import_paths: { book_count: 0, folder_count: 0, total_size: 0 },
      memory: {},
      runtime: {},
      operations: { recent: [] },
    }),
    getHomeDirectory: vi.fn().mockResolvedValue('/tmp'),
    getSoftDeletedBooks: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    getUserColumnConfig: vi.fn().mockResolvedValue(null),
    saveUserColumnConfig: vi.fn().mockResolvedValue(undefined),
    listAllUserTags: vi.fn().mockResolvedValue([]),
    batchFetchCandidates: vi.fn().mockResolvedValue({ operation_id: 'op-1' }),
    batchWriteBackMetadata: vi.fn().mockResolvedValue({
      written: 1,
      written_files: 1,
      renamed: 1,
      failed: 0,
      errors: [],
    }),
    getOperationTimeline: vi.fn().mockResolvedValue([]),
    getActiveOperations: vi.fn().mockResolvedValue([]),
    getSavedFilterPresets: vi.fn().mockResolvedValue([]),
    saveSavedFilterPresets: vi.fn().mockResolvedValue(undefined),
    getScopedTagFacets: vi.fn().mockResolvedValue({ facets: [] }),
    linkBooks: vi.fn(),
    pollOperationV2: vi.fn(),
    getOperationResult: vi.fn(),
    getBookFiles: vi.fn().mockResolvedValue({ files: [], count: 1 }),
    deleteBook: vi.fn(),
  };
});

async function openMergeDialog(selectIds: string[]) {
  const user = userEvent.setup();
  render(
    <MemoryRouter>
      <ToastProvider>
        <Library />
      </ToastProvider>
    </MemoryRouter>
  );
  // The grid renders rows as the query resolves; wait for every row so a
  // partial first paint cannot leave fewer than two books selected.
  await waitFor(
    () => {
      const labels = screen
        .getAllByRole('checkbox', { name: /select /i })
        .map((b) => b.getAttribute('aria-label') ?? '');
      for (const id of selectIds) {
        expect(labels.some((l) => l.includes(TITLES[id]))).toBe(true);
      }
    },
    { timeout: 8000 }
  );
  for (const box of screen.getAllByRole('checkbox', { name: /select /i })) {
    const label = box.getAttribute('aria-label') ?? '';
    if (selectIds.some((id) => label.includes(TITLES[id])) && !(box as HTMLInputElement).checked) {
      await user.click(box);
    }
  }
  await user.click(await screen.findByRole('button', { name: /merge as versions/i }));
  const dialog = await screen.findByRole('dialog', { name: /merge as versions/i });
  return { user, dialog };
}

const TITLES: Record<string, string> = {
  'id-1': 'Test Book',
  'id-2': 'Second Book',
  'id-3': 'Trashed Book',
};

async function clickMerge(user: ReturnType<typeof userEvent.setup>, dialog: HTMLElement) {
  const confirm = Array.from(dialog.querySelectorAll('button')).find(
    (b) => b.textContent === 'Merge'
  );
  await user.click(confirm as HTMLElement);
}

const lastToast = () => {
  const list = useAppStore.getState().notifications;
  return list[list.length - 1];
};

/** The merge runs in the background: wait for its outcome toast, not the "started" one. */
async function waitForOutcome() {
  await waitFor(() => expect(lastToast()?.message).toBeDefined());
  await waitFor(() => expect(lastToast().message).not.toMatch(/started/));
}

/** The poll of the merge operation, settled by the test. */
function deferPoll() {
  const ctl = { finish: (_status: string) => {}, fail: (_e: Error) => {} };
  vi.mocked(api.pollOperationV2).mockImplementation(
    () =>
      new Promise((resolve, reject) => {
        ctl.finish = (status) => resolve({ id: 'op-m', status } as never);
        ctl.fail = reject;
      })
  );
  return ctl;
}

describe('merge as versions', () => {
  beforeEach(() => {
    useAppStore.setState({ notifications: [] });
    vi.mocked(api.linkBooks).mockReset();
    vi.mocked(api.pollOperationV2).mockReset();
    vi.mocked(api.getOperationResult).mockReset();
    vi.mocked(api.linkBooks).mockResolvedValue({ id: 'op-m' } as never);
  });

  it('shows which copy is kept, with radios, and keeps the first by default', async () => {
    const { dialog } = await openMergeDialog(['id-1', 'id-2']);
    expect(within(dialog).getAllByRole('radio')).toHaveLength(2);
    expect(within(dialog).getByRole('radio', { name: 'Keep Test Book' })).toBeChecked();
    expect(within(dialog).getAllByTestId('merge-kept-chip')).toHaveLength(1);
    expect(within(dialog).getByText('/tmp/book.m4b')).toBeInTheDocument();
    expect(await within(dialog).findByTestId('merge-keeper-summary')).toHaveTextContent(
      'Keeping Test Book'
    );
  });

  it('a failed merge with no readable result never says "Nothing was merged"', async () => {
    vi.mocked(api.pollOperationV2).mockResolvedValue({
      id: 'op-m',
      status: 'failed',
      error_message: 'keep book is soft-deleted',
    } as never);
    vi.mocked(api.getOperationResult).mockRejectedValue(new Error('no result'));
    const { user, dialog } = await openMergeDialog(['id-1', 'id-2']);
    await clickMerge(user, dialog);
    await waitForOutcome();
    expect(lastToast().severity).toBe('error');
    expect(lastToast().message).toContain('keep book is soft-deleted');
    expect(lastToast().message).toContain('Check the operation to see what it did.');
    expect(lastToast().message).not.toMatch(/^Merged/);
    expect(lastToast().message).not.toContain('Nothing was merged');
    // The result is fetched for a failed op too.
    expect(vi.mocked(api.getOperationResult)).toHaveBeenCalledWith('op-m');
  });

  it('a partial failure toast says the merge may have partly applied', async () => {
    vi.mocked(api.pollOperationV2).mockResolvedValue({
      id: 'op-m',
      status: 'failed',
      error_message: 'clean-up failed',
    } as never);
    vi.mocked(api.getOperationResult).mockResolvedValue({
      result_data: {
        keep_id: 'id-1',
        merged: 0,
        failed: true,
        partial: true,
        error: 'clean-up of Second Book failed',
      },
    });
    const { user, dialog } = await openMergeDialog(['id-1', 'id-2']);
    await clickMerge(user, dialog);
    await waitForOutcome();
    expect(lastToast().severity).toBe('error');
    expect(lastToast().message).toContain('failed part-way and may have partly applied');
    expect(lastToast().message).toContain('clean-up of Second Book failed');
  });

  it('a merge refused before any write says nothing was merged, with the reason', async () => {
    vi.mocked(api.pollOperationV2).mockResolvedValue({ id: 'op-m', status: 'failed' } as never);
    vi.mocked(api.getOperationResult).mockResolvedValue({
      result_data: {
        keep_id: 'id-1',
        merged: 0,
        failed: true,
        partial: false,
        error: 'keep book is soft-deleted',
      },
    });
    const { user, dialog } = await openMergeDialog(['id-1', 'id-2']);
    await clickMerge(user, dialog);
    await waitForOutcome();
    expect(lastToast().severity).toBe('error');
    expect(lastToast().message).toBe(
      'The merge was refused before changing anything: keep book is soft-deleted. Nothing was merged.'
    );
  });

  it('names the skipped loser and why when the operation skipped one', async () => {
    vi.mocked(api.pollOperationV2).mockResolvedValue({ id: 'op-m', status: 'completed' } as never);
    vi.mocked(api.getOperationResult).mockResolvedValue({
      result_data: {
        keep_id: 'id-1',
        merged: 0,
        skipped: [
          { book_id: 'id-2', reason: 'already in the trash (soft-deleted), so it was not merged' },
        ],
        no_op: true,
      },
    });
    const { user, dialog } = await openMergeDialog(['id-1', 'id-2']);
    await clickMerge(user, dialog);
    await waitForOutcome();
    expect(lastToast().severity).toBe('info');
    expect(lastToast().message).toContain('Nothing was merged');
    expect(lastToast().message).toContain('"Second Book"');
    expect(lastToast().message).toContain('already in the trash');
  });

  it('leaves trashed rows out of the dialog and the request', async () => {
    vi.mocked(api.pollOperationV2).mockResolvedValue({ id: 'op-m', status: 'completed' } as never);
    vi.mocked(api.getOperationResult).mockResolvedValue({
      result_data: { keep_id: 'id-1', merged: 1, no_op: false },
    });
    const { user, dialog } = await openMergeDialog(['id-1', 'id-2', 'id-3']);
    expect(within(dialog).getByTestId('merge-trashed-excluded')).toHaveTextContent(
      '1 selected book is in the trash and will not be merged.'
    );
    expect(within(dialog).getAllByRole('radio')).toHaveLength(2);
    await clickMerge(user, dialog);
    await waitFor(() => expect(vi.mocked(api.linkBooks)).toHaveBeenCalledWith('id-1', ['id-2']));
    await waitFor(() => expect(lastToast().message).toBe('Merged 1 book as versions.'));
    expect(lastToast().severity).toBe('success');
  });
  it('closes the dialog on the 202 before the operation ends, then reports the real outcome', async () => {
    const poll = deferPoll();
    vi.mocked(api.getOperationResult).mockResolvedValue({
      result_data: { keep_id: 'id-1', merged: 1, no_op: false },
    });
    const { user, dialog } = await openMergeDialog(['id-1', 'id-2']);
    const loadsBefore = vi.mocked(api.getBooks).mock.calls.length;
    await clickMerge(user, dialog);

    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: /merge as versions/i })).toBeNull()
    );
    expect(lastToast().message).toContain('Merge started');
    expect(lastToast().message).toContain('in the background');
    expect(lastToast().severity).toBe('info');
    expect(lastToast().action?.label).toBe('View operation');
    expect(vi.mocked(api.getOperationResult)).not.toHaveBeenCalled();

    // Selection kept; the same books cannot be submitted again meanwhile.
    const mergeBtn = screen.getByRole('button', { name: /merge as versions/i });
    expect(mergeBtn).toBeDisabled();
    expect(screen.getByRole('button', { name: /combine into one book/i })).toBeDisabled();
    expect(mergeBtn.closest('span')).toHaveAttribute(
      'title',
      expect.stringMatching(/still running/)
    );

    poll.finish('completed');
    await waitFor(() => expect(lastToast().message).toBe('Merged 1 book as versions.'));
    expect(lastToast().severity).toBe('success');
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: /merge as versions/i })).toBeNull()
    );
    expect(vi.mocked(api.getBooks).mock.calls.length).toBeGreaterThan(loadsBefore);
  });

  it('a failed operation is toasted as an error and leaves the selection for a retry', async () => {
    vi.mocked(api.pollOperationV2).mockResolvedValue({ id: 'op-m', status: 'failed' } as never);
    vi.mocked(api.getOperationResult).mockResolvedValue({
      result_data: { keep_id: 'id-1', merged: 0, failed: true, partial: false, error: 'refused' },
    });
    const { user, dialog } = await openMergeDialog(['id-1', 'id-2']);
    await clickMerge(user, dialog);
    await waitForOutcome();
    expect(lastToast().severity).toBe('error');
    expect(lastToast().message).not.toMatch(/^Merged/);
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /merge as versions/i })).toBeEnabled()
    );
  });

  it('when the operation cannot be followed it says the outcome is unknown and frees the books', async () => {
    const poll = deferPoll();
    const { user, dialog } = await openMergeDialog(['id-1', 'id-2']);
    await clickMerge(user, dialog);
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /merge as versions/i })).toBeDisabled()
    );
    poll.fail(new Error('network down'));
    await waitFor(() => expect(lastToast().message).toContain('what it did is unknown'));
    expect(lastToast().severity).toBe('warning');
    expect(lastToast().message).toContain('op-m');
    expect(lastToast().message).not.toMatch(/Merged/);
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /merge as versions/i })).toBeEnabled()
    );
  });

  it('a refusal from the request (409) keeps the dialog open with the reason', async () => {
    vi.mocked(api.linkBooks).mockRejectedValue(
      new api.ApiError('keep book "Test Book" is in the trash', 409)
    );
    const { user, dialog } = await openMergeDialog(['id-1', 'id-2']);
    await clickMerge(user, dialog);
    await waitFor(() => expect(lastToast()?.severity).toBe('error'));
    expect(lastToast().message).toContain('is in the trash');
    expect(screen.getByRole('dialog', { name: /merge as versions/i })).toBeInTheDocument();
    expect(vi.mocked(api.pollOperationV2)).not.toHaveBeenCalled();
    await waitFor(() =>
      expect(
        Array.from(dialog.querySelectorAll('button')).find((b) => b.textContent === 'Merge')
      ).toBeEnabled()
    );
  });
});
