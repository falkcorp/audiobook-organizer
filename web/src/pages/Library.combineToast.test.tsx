// file: web/src/pages/Library.combineToast.test.tsx
// version: 1.2.0
// guid: b7c3d543-d1c8-4e7a-8782-7a65d6d29167
// last-edited: 2026-10-22

/**
 * The Library "Combine into One Book" flow. The combine is a background
 * operation: the click handler closes the dialog on the 202 and the outcome
 * toast comes when the operation ends. The toasts: journal-pending text keeps
 * the override warnings, every outcome that has an operation id carries an Undo
 * action wired to api.revertOperation, a combine that stopped part-way offers
 * Undo, a failed one is never described as combined, and a result with no
 * operation id gets no Undo action.
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
    pollOperationV2: vi.fn(),
    getOperationResult: vi.fn(),
    revertOperation: vi.fn(),
  };
});

const QUEUED = { id: 'op-c', type: 'book-combine', status: 'queued' };

/** The poll of the combine operation, settled by the test. */
function deferPoll() {
  const ctl = { finish: (_status: string, _err?: string) => {}, fail: (_e: Error) => {} };
  vi.mocked(api.pollOperationV2).mockImplementation(
    () =>
      new Promise((resolve, reject) => {
        ctl.finish = (status, err) => resolve({ id: 'op-c', status, error_message: err } as never);
        ctl.fail = reject;
      })
  );
  return ctl;
}

/** Renders the Library, selects both books and opens the combine dialog. */
async function openCombineDialog() {
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
  return { user, dialog };
}

async function clickCombine(user: ReturnType<typeof userEvent.setup>, dialog: HTMLElement) {
  const confirm = Array.from(dialog.querySelectorAll('button')).find(
    (b) => b.textContent === 'Combine'
  );
  await user.click(confirm as HTMLElement);
  await waitFor(() => expect(vi.mocked(api.combineBooks)).toHaveBeenCalled());
}

/** Combines and waits for the operation's outcome toast (not the "started" one). */
async function combineSelected() {
  const { user, dialog } = await openCombineDialog();
  await clickCombine(user, dialog);
  await waitFor(() => expect(lastToast()?.message).toBeDefined());
  await waitFor(() => expect(lastToast().message).not.toMatch(/started/));
}

const lastToast = () => {
  const list = useAppStore.getState().notifications;
  return list[list.length - 1];
};

const combined = (extra: object = {}) => ({
  result_data: { primary_id: 'id-1', files_moved: 2, books_deleted: 1, ...extra },
});

describe('combine into one book', () => {
  beforeEach(() => {
    useAppStore.setState({ notifications: [] });
    vi.mocked(api.combineBooks).mockReset();
    vi.mocked(api.pollOperationV2).mockReset();
    vi.mocked(api.getOperationResult).mockReset();
    vi.mocked(api.revertOperation).mockReset();
    vi.mocked(api.combineBooks).mockResolvedValue(QUEUED as never);
    vi.mocked(api.pollOperationV2).mockResolvedValue({ id: 'op-c', status: 'completed' } as never);
  });

  it('closes the dialog on the 202 before the operation ends, then reports the real outcome', async () => {
    const poll = deferPoll();
    vi.mocked(api.getOperationResult).mockResolvedValue(combined());
    const { user, dialog } = await openCombineDialog();
    const loadsBefore = vi.mocked(api.getBooks).mock.calls.length;
    await clickCombine(user, dialog);

    // The click handler returned: dialog closed, started toast with a link to
    // the operation, and nothing about the outcome yet.
    await waitFor(() =>
      expect(screen.queryByRole('dialog', { name: /combine into one book/i })).toBeNull()
    );
    expect(lastToast().message).toContain('Combine started');
    expect(lastToast().message).toContain('in the background');
    expect(lastToast().severity).toBe('info');
    expect(lastToast().action?.label).toBe('View operation');
    expect(vi.mocked(api.getOperationResult)).not.toHaveBeenCalled();

    // Selection kept, and the same books cannot be submitted again.
    const combineBtn = screen.getByRole('button', { name: /combine into one book/i });
    expect(combineBtn).toBeDisabled();
    expect(screen.getByRole('button', { name: /merge as versions/i })).toBeDisabled();
    expect(combineBtn.closest('span')).toHaveAttribute(
      'title',
      expect.stringMatching(/still running/)
    );

    poll.finish('completed');
    await waitFor(() =>
      expect(lastToast().message).toBe('Combined 2 files into one book; moved 1 entries to trash.')
    );
    expect(lastToast().severity).toBe('success');
    expect(vi.mocked(api.getOperationResult)).toHaveBeenCalledWith('op-c');
    // Selection cleared, list refreshed.
    await waitFor(() =>
      expect(screen.queryByRole('button', { name: /combine into one book/i })).toBeNull()
    );
    expect(vi.mocked(api.getBooks).mock.calls.length).toBeGreaterThan(loadsBefore);
  });

  it('shows the pending-journal text and the override warnings together', async () => {
    vi.mocked(api.getOperationResult).mockResolvedValue(
      combined({
        operation_id: 'op-9',
        journal_pending: 'j-1',
        warnings: ['Title override was not applied.'],
      })
    );
    await combineSelected();
    const t = lastToast();
    expect(t.severity).toBe('warning');
    expect(t.message).toContain('undo record could not be finalized');
    expect(t.message).toContain('Title override was not applied.');
    expect(t.action?.label).toBe('Undo');
  });

  it('Undo on a successful combine reverts that operation id', async () => {
    vi.mocked(api.getOperationResult).mockResolvedValue(combined({ operation_id: 'op-7' }));
    vi.mocked(api.revertOperation).mockResolvedValue({ message: 'Reverted.', partial: false });
    await combineSelected();
    expect(lastToast().severity).toBe('success');
    expect(lastToast().action?.label).toBe('Undo');
    lastToast().action!.onClick();
    await waitFor(() => expect(vi.mocked(api.revertOperation)).toHaveBeenCalledWith('op-7'));
    await waitFor(() => expect(lastToast().message).toBe('Reverted.'));
  });

  it('a combine that stopped part-way is an error that offers Undo, never "Combined"', async () => {
    vi.mocked(api.pollOperationV2).mockResolvedValue({
      id: 'op-c',
      status: 'failed',
      error_message: 'move failed',
    } as never);
    vi.mocked(api.getOperationResult).mockResolvedValue(
      combined({
        failed: true,
        incomplete: true,
        error: 'move failed',
        journal_id: 'j-2',
        operation_id: 'op-5',
      })
    );
    await combineSelected();
    const t = lastToast();
    expect(t.severity).toBe('error');
    expect(t.message).toContain('stopped part-way');
    expect(t.message).not.toMatch(/Combined/);
    expect(t.action?.label).toBe('Undo');
    vi.mocked(api.revertOperation).mockResolvedValue({ message: 'Reverted.', partial: false });
    t.action!.onClick();
    await waitFor(() => expect(vi.mocked(api.revertOperation)).toHaveBeenCalledWith('op-5'));
  });

  it('a combine refused before any write is an error with the reason, and keeps the selection', async () => {
    vi.mocked(api.pollOperationV2).mockResolvedValue({ id: 'op-c', status: 'failed' } as never);
    vi.mocked(api.getOperationResult).mockResolvedValue(
      combined({ files_moved: 0, books_deleted: 0, failed: true, error: 'survivor is gone' })
    );
    await combineSelected();
    expect(lastToast().severity).toBe('error');
    expect(lastToast().message).toBe(
      'The combine was refused before changing anything: survivor is gone. Nothing was combined.'
    );
    expect(lastToast().action?.label).toBe('View operation');
    // The books are free again for a retry, and still selected.
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /combine into one book/i })).toBeEnabled()
    );
  });

  it('a failed operation with no readable result says the outcome is unknown, never combined', async () => {
    vi.mocked(api.pollOperationV2).mockResolvedValue({
      id: 'op-c',
      status: 'failed',
      error_message: 'boom',
    } as never);
    vi.mocked(api.getOperationResult).mockRejectedValue(new Error('no result'));
    await combineSelected();
    expect(lastToast().severity).toBe('error');
    expect(lastToast().message).toContain('did not complete (failed): boom');
    expect(lastToast().message).not.toMatch(/^Combined/);
  });

  it('when the operation cannot be followed it says the outcome is unknown and frees the books', async () => {
    const poll = deferPoll();
    const { user, dialog } = await openCombineDialog();
    await clickCombine(user, dialog);
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /combine into one book/i })).toBeDisabled()
    );
    poll.fail(new Error('network down'));
    await waitFor(() => expect(lastToast().message).toContain('what it did is unknown'));
    expect(lastToast().severity).toBe('warning');
    expect(lastToast().message).toContain('op-c');
    expect(lastToast().message).not.toMatch(/Combined/);
    await waitFor(() =>
      expect(screen.getByRole('button', { name: /combine into one book/i })).toBeEnabled()
    );
  });

  it('a refusal from the request (409) keeps the dialog open with the reason', async () => {
    vi.mocked(api.combineBooks).mockRejectedValue(
      new api.ApiError('book "Test Book" is in the trash', 409)
    );
    const { user, dialog } = await openCombineDialog();
    await clickCombine(user, dialog);
    await waitFor(() => expect(lastToast()?.severity).toBe('error'));
    expect(lastToast().message).toBe('book "Test Book" is in the trash');
    expect(screen.getByRole('dialog', { name: /combine into one book/i })).toBeInTheDocument();
    expect(vi.mocked(api.pollOperationV2)).not.toHaveBeenCalled();
    // Nothing is in flight, so the dialog's button is usable again.
    await waitFor(() =>
      expect(
        Array.from(dialog.querySelectorAll('button')).find((b) => b.textContent === 'Combine')
      ).toBeEnabled()
    );
  });

  it('a partial undo offers Retry, which runs the same revert again', async () => {
    vi.mocked(api.getOperationResult).mockResolvedValue(combined({ operation_id: 'op-3' }));
    vi.mocked(api.revertOperation).mockResolvedValueOnce({
      message: 'partly',
      partial: true,
      restored: 1,
      total: 3,
    });
    await combineSelected();
    expect(lastToast().action?.label).toBe('Undo');
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

  it('has no Undo action and promises none when the result carries no operation id', async () => {
    vi.mocked(api.getOperationResult).mockResolvedValue(combined());
    await combineSelected();
    expect(lastToast().action?.label).not.toBe('Undo');
    expect(lastToast().message).not.toMatch(/undo/i);
  });
});
