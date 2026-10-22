// file: web/src/pages/Library.mergeAsVersions.test.tsx
// version: 1.0.0
// guid: 4d8b2e61-0a73-4f95-b1c6-9e3f7a5d2c08
// last-edited: 2026-10-22

/**
 * Library "Merge as Versions": the dialog states which copy is kept, trashed
 * rows in the selection are left out of the request, and the toast reports
 * what the merge OPERATION did (it used to say "Merged" when the request was
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

  it('reports a failed merge operation with its reason, not "Merged"', async () => {
    vi.mocked(api.pollOperationV2).mockResolvedValue({
      id: 'op-m',
      status: 'failed',
      error_message: 'keep book is soft-deleted',
    } as never);
    const { user, dialog } = await openMergeDialog(['id-1', 'id-2']);
    await clickMerge(user, dialog);
    await waitFor(() => expect(lastToast()).toBeDefined());
    expect(lastToast().severity).toBe('error');
    expect(lastToast().message).toContain('keep book is soft-deleted');
    expect(lastToast().message).not.toMatch(/^Merged/);
    expect(vi.mocked(api.getOperationResult)).not.toHaveBeenCalled();
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
    await waitFor(() => expect(lastToast()).toBeDefined());
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
});
