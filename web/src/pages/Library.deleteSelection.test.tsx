// file: web/src/pages/Library.deleteSelection.test.tsx
// version: 1.0.0
// guid: 0a6e2d94-7b31-4c58-8f1d-5e9c3b7a2d60
// last-edited: 2026-10-22

/**
 * Library: a book deleted from its row menu leaves the selection. The
 * selection is persisted, and a trashed row left in it used to be offered to
 * Merge as Versions as a loser; the selection count must not include it.
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
    previewPurgeSoftDeleted: vi.fn(),
    purgeSoftDeletedBooks: vi.fn(),
    awaitPurgeResult: vi.fn(),
  };
});

/** Every "N selected" chip on the page (the toolbar and the grid each show one). */
const selectedChips = () => screen.queryAllByText(/^\d+ selected$/).map((e) => e.textContent);

async function renderAndSelect(titles: string[]) {
  const user = userEvent.setup();
  render(
    <MemoryRouter>
      <ToastProvider>
        <Library />
      </ToastProvider>
    </MemoryRouter>
  );
  await waitFor(
    () => {
      const labels = screen
        .getAllByRole('checkbox', { name: /select /i })
        .map((b) => b.getAttribute('aria-label') ?? '');
      for (const t of titles) expect(labels.some((l) => l.includes(t))).toBe(true);
    },
    { timeout: 8000 }
  );
  for (const t of titles) {
    const box = screen
      .getAllByRole('checkbox', { name: /select /i })
      .find((b) => (b.getAttribute('aria-label') ?? '').includes(t)) as HTMLInputElement;
    if (!box.checked) await user.click(box);
  }
  return user;
}

describe('single delete and the selection', () => {
  beforeEach(() => {
    useAppStore.setState({ notifications: [] });
    vi.mocked(api.deleteBook).mockReset();
    vi.mocked(api.deleteBook).mockResolvedValue({} as never);
  });

  it('drops the deleted book from the selection and keeps the others selected', async () => {
    const user = await renderAndSelect(['Test Book', 'Second Book']);
    await waitFor(() => expect(selectedChips()).toContain('2 selected'));
    expect(new Set(selectedChips())).toEqual(new Set(['2 selected']));

    // Cards render in list order, so the first actions button is Test Book's.
    await user.click(screen.getAllByRole('button', { name: 'Book actions' })[0]);
    await user.click(await screen.findByRole('menuitem', { name: /delete/i }));
    const dialog = await screen.findByRole('dialog', { name: /delete audiobook/i });
    await user.click(within(dialog).getByRole('button', { name: 'Soft Delete' }));

    await waitFor(() =>
      expect(vi.mocked(api.deleteBook)).toHaveBeenCalledWith(
        'id-1',
        expect.objectContaining({ softDelete: true })
      )
    );
    // 1 selected: the deleted id is gone from the selection, not just the page.
    await waitFor(() => expect(selectedChips()).toContain('1 selected'));
    expect(new Set(selectedChips())).toEqual(new Set(['1 selected']));
  });

  it('keeps the selection untouched when the delete fails', async () => {
    vi.mocked(api.deleteBook).mockRejectedValue(new Error('boom'));
    const user = await renderAndSelect(['Test Book', 'Second Book']);
    await waitFor(() => expect(selectedChips()).toContain('2 selected'));
    expect(new Set(selectedChips())).toEqual(new Set(['2 selected']));
    // Cards render in list order, so the first actions button is Test Book's.
    await user.click(screen.getAllByRole('button', { name: 'Book actions' })[0]);
    await user.click(await screen.findByRole('menuitem', { name: /delete/i }));
    const dialog = await screen.findByRole('dialog', { name: /delete audiobook/i });
    await user.click(within(dialog).getByRole('button', { name: 'Soft Delete' }));
    await waitFor(() => expect(vi.mocked(api.deleteBook)).toHaveBeenCalled());
    await waitFor(() => expect(useAppStore.getState().notifications.length).toBeGreaterThan(0));
    expect(new Set(selectedChips())).toEqual(new Set(['2 selected']));
  });
});
