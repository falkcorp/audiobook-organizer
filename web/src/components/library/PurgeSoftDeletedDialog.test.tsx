// file: web/src/components/library/PurgeSoftDeletedDialog.test.tsx
// version: 1.0.0
// guid: 2f9a6c04-7d83-4e15-b0a8-4c1e5d9b3f72
// last-edited: 2026-10-22

import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import type { PurgeResult } from '../../services/api';

const api = vi.hoisted(() => ({
  previewPurgeSoftDeleted: vi.fn(),
  purgeSoftDeletedBooks: vi.fn(),
  awaitPurgeResult: vi.fn(),
}));
vi.mock('../../services/api', async (orig) => ({
  ...(await orig<typeof import('../../services/api')>()),
  ...api,
}));

import { PurgeSoftDeletedDialog } from './PurgeSoftDeletedDialog';

const preview: PurgeResult = {
  dry_run: true,
  attempted: 40,
  purged: 0,
  purgeable: 3,
  files_deleted: 0,
  skipped_owns_files: 30,
  kept_undo_possible: 5,
  kept_has_progress: 2,
  errors: [],
};

function setup(onFinished = vi.fn()) {
  api.previewPurgeSoftDeleted.mockResolvedValue({ id: 'op-prev', dry_run: true });
  api.purgeSoftDeletedBooks.mockResolvedValue({ id: 'op-run', dry_run: false });
  renderWithProviders(<PurgeSoftDeletedDialog open onClose={vi.fn()} onFinished={onFinished} />);
  return onFinished;
}

describe('PurgeSoftDeletedDialog', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('previews on open: says how many a purge removes and keeps, never "permanently remove <trash count>"', async () => {
    api.awaitPurgeResult.mockResolvedValueOnce(preview);
    setup();
    expect(await screen.findByTestId('purge-preview-headline')).toHaveTextContent(
      'A purge would remove 3 books of the 40 in the trash right now.'
    );
    expect(api.previewPurgeSoftDeleted).toHaveBeenCalledTimes(1);
    expect(api.purgeSoftDeletedBooks).not.toHaveBeenCalled();
    expect(screen.getByTestId('purge-kept-owns_files')).toHaveTextContent('30');
    expect(screen.getByTestId('purge-kept-undo_possible')).toHaveTextContent(
      '5 are the leftover copies of a combine that can still be undone'
    );
    expect(screen.getByTestId('purge-kept-has_progress')).toHaveTextContent('2');
    expect(screen.queryByText(/permanently remove 40/i)).toBeNull();
    expect(screen.getByRole('button', { name: 'Purge 3 books' })).toBeEnabled();
  });

  it('disables the purge when nothing is purgeable', async () => {
    api.awaitPurgeResult.mockResolvedValueOnce({ ...preview, purgeable: 0 });
    setup();
    expect(await screen.findByTestId('purge-preview-headline')).toHaveTextContent(
      'would remove none of the 40 books'
    );
    expect(screen.getByRole('button', { name: 'Nothing to purge' })).toBeDisabled();
  });

  it('runs the purge as an operation, shows progress, then the full breakdown', async () => {
    const result: PurgeResult = {
      attempted: 40,
      purged: 3,
      files_deleted: 2,
      skipped_owns_files: 30,
      kept_undo_possible: 5,
      kept_has_progress: 2,
      carried_to_sibling: 1,
      errors: [],
    };
    api.awaitPurgeResult.mockResolvedValueOnce(preview);
    let finish: (r: PurgeResult) => void = () => {};
    api.awaitPurgeResult.mockImplementationOnce(
      (_id: string, onProgress: (p: { done: number; total: number; message: string }) => void) => {
        onProgress({
          done: 20,
          total: 40,
          message: 'Purging soft-deleted books: 20 of 40 decided',
        });
        return new Promise<PurgeResult>((resolve) => {
          finish = resolve;
        });
      }
    );
    const onFinished = setup();
    fireEvent.click(await screen.findByRole('button', { name: 'Purge 3 books' }));

    expect(await screen.findByTestId('purge-progress')).toHaveTextContent(
      'Purging soft-deleted books: 20 of 40 decided'
    );
    expect(api.purgeSoftDeletedBooks).toHaveBeenCalledWith(false);
    expect(onFinished).not.toHaveBeenCalled();

    finish(result);
    expect(await screen.findByTestId('purge-result-headline')).toHaveTextContent(
      'Purged 3 of 40 soft-deleted books, and deleted 2 files from disk.'
    );
    expect(screen.getByTestId('purge-kept-owns_files')).toHaveTextContent('30');
    expect(screen.getByTestId('purge-kept-undo_possible')).toHaveTextContent('5');
    expect(screen.getByText(/1 book had listening progress, which was moved/)).toBeInTheDocument();
    await waitFor(() => expect(onFinished).toHaveBeenCalledWith(result));
  });

  it('passes the delete-files choice to the purge', async () => {
    api.awaitPurgeResult.mockResolvedValueOnce(preview);
    api.awaitPurgeResult.mockResolvedValueOnce({
      attempted: 40,
      purged: 3,
      files_deleted: 3,
      errors: [],
    });
    setup();
    await screen.findByTestId('purge-preview-headline');
    fireEvent.click(screen.getByLabelText('Also delete files from disk (if they still exist)'));
    fireEvent.click(screen.getByRole('button', { name: 'Purge 3 books' }));
    await screen.findByTestId('purge-result-headline');
    expect(api.purgeSoftDeletedBooks).toHaveBeenCalledWith(true);
  });

  it('shows the failure reason when the operation failed, and offers to try again', async () => {
    api.awaitPurgeResult.mockRejectedValueOnce(new Error('purge refused: cannot list users'));
    setup();
    expect(await screen.findByTestId('purge-error')).toHaveTextContent(
      'purge refused: cannot list users'
    );
    api.awaitPurgeResult.mockResolvedValueOnce(preview);
    fireEvent.click(screen.getByRole('button', { name: 'Try again' }));
    expect(await screen.findByTestId('purge-preview-headline')).toBeInTheDocument();
  });

  it('lists the details of a purge that had errors, capped, as a warning', async () => {
    api.awaitPurgeResult.mockResolvedValueOnce(preview);
    api.awaitPurgeResult.mockResolvedValueOnce({
      attempted: 40,
      purged: 1,
      files_deleted: 0,
      failed: 7,
      errors: Array.from({ length: 7 }, (_, i) => `b${i}: could not read`),
    });
    setup();
    fireEvent.click(await screen.findByRole('button', { name: 'Purge 3 books' }));
    const errors = await screen.findByTestId('purge-result-errors');
    expect(errors).toHaveTextContent('b0: could not read');
    expect(errors).toHaveTextContent('and 2 more');
    expect(screen.getByText(/7 books could not be processed/)).toBeInTheDocument();
  });
});
