// file: web/src/pages/Library.combineToast.test.tsx
// version: 1.1.0
// guid: b7c3d543-d1c8-4e7a-8782-7a65d6d29167
// last-edited: 2026-10-18

/**
 * The Library "Combine into One Book" toasts: journal-pending text keeps the
 * override warnings, every outcome that has an operation id carries an Undo
 * action wired to api.revertOperation, a COMBINE_INCOMPLETE 500 offers Undo,
 * and an old server (no operation id) gets no action.
 */

import { render, screen, waitFor } from '@testing-library/react';
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
    combineBooks: vi.fn(),
    revertOperation: vi.fn(),
  };
});

async function combineSelected() {
  const user = userEvent.setup();
  render(
    <MemoryRouter>
      <ToastProvider>
        <Library />
      </ToastProvider>
    </MemoryRouter>
  );
  const boxes = await screen.findAllByRole('checkbox', { name: /select /i });
  for (const box of boxes) {
    if (!(box as HTMLInputElement).checked) await user.click(box);
  }
  await user.click(await screen.findByRole('button', { name: /combine into one book/i }));
  const dialog = await screen.findByRole('dialog', { name: /combine into one book/i });
  const confirm = Array.from(dialog.querySelectorAll('button')).find(
    (b) => b.textContent === 'Combine'
  );
  await user.click(confirm as HTMLElement);
  await waitFor(() => expect(vi.mocked(api.combineBooks)).toHaveBeenCalled());
}

const lastToast = () => {
  const list = useAppStore.getState().notifications;
  return list[list.length - 1];
};

describe('combine into one book toasts', () => {
  beforeEach(() => {
    useAppStore.setState({ notifications: [] });
    vi.mocked(api.combineBooks).mockReset();
    vi.mocked(api.revertOperation).mockReset();
  });

  it('shows the pending-journal text and the override warnings together', async () => {
    vi.mocked(api.combineBooks).mockResolvedValue({
      primary_id: 'id-1',
      files_moved: 2,
      books_deleted: 1,
      operation_id: 'op-9',
      journal_pending: 'j-1',
      warnings: ['Title override was not applied.'],
    });
    await combineSelected();
    await waitFor(() => expect(lastToast()).toBeDefined());
    const t = lastToast();
    expect(t.severity).toBe('warning');
    expect(t.message).toContain('undo record could not be finalized');
    expect(t.message).toContain('Title override was not applied.');
    expect(t.action?.label).toBe('Undo');
  });

  it('Undo on a successful combine reverts that operation id', async () => {
    vi.mocked(api.combineBooks).mockResolvedValue({
      primary_id: 'id-1',
      files_moved: 2,
      books_deleted: 1,
      operation_id: 'op-7',
    });
    vi.mocked(api.revertOperation).mockResolvedValue({ message: 'Reverted.', partial: false });
    await combineSelected();
    await waitFor(() => expect(lastToast()?.action).toBeDefined());
    expect(lastToast().severity).toBe('success');
    lastToast().action!.onClick();
    await waitFor(() => expect(vi.mocked(api.revertOperation)).toHaveBeenCalledWith('op-7'));
    await waitFor(() => expect(lastToast().message).toBe('Reverted.'));
  });

  it('offers Undo on a COMBINE_INCOMPLETE 500', async () => {
    vi.mocked(api.combineBooks).mockRejectedValue(
      new api.ApiError('move failed', 500, {
        error: 'move failed',
        code: 'COMBINE_INCOMPLETE',
        journal_id: 'j-2',
        operation_id: 'op-5',
      })
    );
    await combineSelected();
    await waitFor(() => expect(lastToast()).toBeDefined());
    const t = lastToast();
    expect(t.severity).toBe('error');
    expect(t.message).toContain('stopped part-way');
    expect(t.action?.label).toBe('Undo');
    vi.mocked(api.revertOperation).mockResolvedValue({ message: 'Reverted.', partial: false });
    t.action!.onClick();
    await waitFor(() => expect(vi.mocked(api.revertOperation)).toHaveBeenCalledWith('op-5'));
  });

  it('a partial undo offers Retry, which runs the same revert again', async () => {
    vi.mocked(api.combineBooks).mockResolvedValue({
      primary_id: 'id-1',
      files_moved: 2,
      books_deleted: 1,
      operation_id: 'op-3',
    });
    vi.mocked(api.revertOperation).mockResolvedValueOnce({
      message: 'partly',
      partial: true,
      restored: 1,
      total: 3,
    });
    await combineSelected();
    await waitFor(() => expect(lastToast()?.action?.label).toBe('Undo'));
    lastToast().action!.onClick();
    await waitFor(() => expect(lastToast().action?.label).toBe('Retry'));
    expect(lastToast().severity).toBe('warning');
    expect(lastToast().message).toContain('1 of 3 changes reverted');
    vi.mocked(api.revertOperation).mockResolvedValueOnce({ message: 'Reverted.', partial: false });
    lastToast().action!.onClick();
    await waitFor(() => expect(vi.mocked(api.revertOperation)).toHaveBeenCalledTimes(2));
    expect(vi.mocked(api.revertOperation)).toHaveBeenLastCalledWith('op-3');
    await waitFor(() => expect(lastToast().message).toBe('Reverted.'));
  });

  it('has no Undo action and promises none when the server sent no operation id', async () => {
    vi.mocked(api.combineBooks).mockResolvedValue({
      primary_id: 'id-1',
      files_moved: 2,
      books_deleted: 1,
    });
    await combineSelected();
    await waitFor(() => expect(lastToast()).toBeDefined());
    expect(lastToast().action).toBeUndefined();
    expect(lastToast().message).not.toMatch(/undo/i);
  });
});
