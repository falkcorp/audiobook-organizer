// file: web/src/pages/Library.purgeBackground.test.tsx
// version: 1.2.0
// guid: 7e1c4b92-3a68-4d05-9f27-b84d0c6a1e53
// last-edited: 2026-10-22

/**
 * Library "Purge Deleted": the purge is a background operation. Clicking
 * Purge in the dialog closes it as soon as the server has queued the run, a
 * "started" toast links to the operation, and the breakdown of what was purged
 * and what was kept arrives as a toast when the operation ends (the page keeps
 * working meanwhile, and the purge button stays disabled while it runs).
 */

import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import userEvent from '@testing-library/user-event';
import { Library } from './Library';
import { OperationTrackerProvider } from '../components/operations/OperationTrackerProvider';
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
      ],
      count: 2,
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
    getSoftDeletedBooks: vi.fn().mockResolvedValue({ items: [], count: 3 }),
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
    previewPurgeSoftDeleted: vi.fn(),
    purgeSoftDeletedBooks: vi.fn(),
    awaitPurgeResult: vi.fn(),
  };
});

const lastToast = () => {
  const list = useAppStore.getState().notifications;
  return list[list.length - 1];
};

const preview = {
  dry_run: true,
  attempted: 3,
  purged: 0,
  purgeable: 2,
  files_deleted: 0,
  skipped_owns_files: 0,
  kept_undo_possible: 1,
  errors: [],
};

describe('purge deleted in the background', () => {
  beforeEach(() => {
    useAppStore.setState({ notifications: [] });
    vi.mocked(api.previewPurgeSoftDeleted).mockReset();
    vi.mocked(api.purgeSoftDeletedBooks).mockReset();
    vi.mocked(api.awaitPurgeResult).mockReset();
    vi.mocked(api.previewPurgeSoftDeleted).mockResolvedValue({ id: 'op-prev' } as never);
    vi.mocked(api.purgeSoftDeletedBooks).mockResolvedValue({ id: 'op-purge' } as never);
  });

  it('closes after Purge is clicked, then toasts the breakdown and refreshes the trash', async () => {
    vi.mocked(api.awaitPurgeResult).mockResolvedValueOnce(preview as never);
    let finish: (r: api.PurgeResult) => void = () => {};
    vi.mocked(api.awaitPurgeResult).mockImplementationOnce(
      () =>
        new Promise<api.PurgeResult>((resolve) => {
          finish = resolve;
        })
    );
    const user = userEvent.setup();
    render(
      <MemoryRouter>
        <ToastProvider>
          <OperationTrackerProvider>
            <Library />
          </OperationTrackerProvider>
        </ToastProvider>
      </MemoryRouter>
    );
    await user.click(await screen.findByRole('button', { name: /purge deleted/i }));
    await user.click(await screen.findByRole('button', { name: 'Purge 2 books' }));

    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: /purge soft-deleted/i })).toBeNull()
    );
    expect(lastToast().message).toContain('Purge started');
    expect(lastToast().message).toContain('in the background');
    expect(lastToast().action?.label).toBe('View operation');
    // The nothing-else-blocks promise: the page is usable, and a second purge
    // cannot be started from the reopened dialog while this one runs.
    const trashLoads = vi.mocked(api.getSoftDeletedBooks).mock.calls.length;
    await user.click(screen.getByRole('button', { name: /purge deleted/i }));
    expect(await screen.findByRole('button', { name: 'Purging...' })).toBeDisabled();
    await user.click(screen.getByRole('button', { name: 'Close' }));

    finish({
      attempted: 3,
      purged: 2,
      files_deleted: 0,
      kept_undo_possible: 1,
      errors: [],
    });
    await waitFor(() => expect(lastToast().message).toMatch(/Purged 2 of 3/));
    expect(lastToast().action?.label).toBe('View operation');
    await waitFor(() =>
      expect(vi.mocked(api.getSoftDeletedBooks).mock.calls.length).toBeGreaterThan(trashLoads)
    );
  });

  const openPurgeDialog = async (user: ReturnType<typeof userEvent.setup>) => {
    render(
      <MemoryRouter>
        <ToastProvider>
          <OperationTrackerProvider>
            <Library />
          </OperationTrackerProvider>
        </ToastProvider>
      </MemoryRouter>
    );
    await user.click(await screen.findByRole('button', { name: /purge deleted/i }));
    return screen.findByRole('button', { name: 'Purge 2 books' });
  };

  it('shows a refusal once, in the dialog while it is open, with no toast repeating it', async () => {
    vi.mocked(api.awaitPurgeResult).mockResolvedValueOnce(preview as never);
    vi.mocked(api.purgeSoftDeletedBooks).mockRejectedValue(
      new api.ApiError('The undo ledger index is not ready yet.', 409)
    );
    const user = userEvent.setup();
    await user.click(await openPurgeDialog(user));

    const alert = await screen.findByTestId('purge-error');
    expect(alert).toHaveTextContent('The undo ledger index is not ready yet.');
    // The dialog is open and says it; no toast says it again.
    expect(
      useAppStore.getState().notifications.filter((n) => n.message.includes('undo ledger index'))
    ).toHaveLength(0);
    expect(screen.getByRole('dialog', { name: /purge soft-deleted/i })).toBeInTheDocument();
  });

  it('toasts a failed outcome when the dialog is closed', async () => {
    vi.mocked(api.awaitPurgeResult).mockResolvedValueOnce(preview as never);
    let fail: (e: Error) => void = () => {};
    vi.mocked(api.awaitPurgeResult).mockImplementationOnce(
      () =>
        new Promise<api.PurgeResult>((_resolve, reject) => {
          fail = reject;
        })
    );
    const user = userEvent.setup();
    await user.click(await openPurgeDialog(user));
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: /purge soft-deleted/i })).toBeNull()
    );
    fail(new Error('The purge ended as failed and no result was stored (operation op-purge).'));
    await waitFor(() => expect(lastToast().message).toContain('no result was stored'));
    expect(lastToast().severity).toBe('error');
  });

  it('a lost connection is not a failed purge: it says "still checking" and keeps the run in progress', async () => {
    vi.mocked(api.awaitPurgeResult).mockResolvedValueOnce(preview as never);
    let finish: (r: api.PurgeResult) => void = () => {};
    vi.mocked(api.awaitPurgeResult).mockImplementationOnce(
      (_id, _onProgress, opts) =>
        new Promise<api.PurgeResult>((resolve) => {
          finish = resolve;
          // What the real awaitPurgeResult does when a poll fails: say so
          // once and keep polling.
          opts?.onTrouble?.(new Error('network down'));
        })
    );
    const user = userEvent.setup();
    await user.click(await openPurgeDialog(user));
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: /purge soft-deleted/i })).toBeNull()
    );
    await waitFor(() => expect(lastToast().message).toContain('Still checking on the purge'));
    expect(lastToast().severity).toBe('warning');
    expect(lastToast().message).not.toMatch(/failed/);

    // Still running: the reopened dialog cannot start another purge and says
    // it is still checking.
    await user.click(screen.getByRole('button', { name: /purge deleted/i }));
    expect(await screen.findByRole('button', { name: 'Purging...' })).toBeDisabled();
    expect(screen.getByTestId('purge-still-checking')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Close' }));

    finish({ attempted: 3, purged: 2, files_deleted: 0, kept_undo_possible: 1, errors: [] });
    await waitFor(() => expect(lastToast().message).toMatch(/Purged 2 of 3/));
  });
});
