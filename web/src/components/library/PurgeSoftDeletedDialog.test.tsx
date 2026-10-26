// file: web/src/components/library/PurgeSoftDeletedDialog.test.tsx
// version: 1.9.0
// guid: 2f9a6c04-7d83-4e15-b0a8-4c1e5d9b3f72
// last-edited: 2026-10-26

import { useState } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import type { PurgeResult } from '../../services/api';

const api = vi.hoisted(() => ({
  previewPurgeSoftDeleted: vi.fn(),
  purgeSoftDeletedBooks: vi.fn(),
  awaitPurgeResult: vi.fn(),
  forgetWaitingFileRecords: vi.fn(),
  cancelOperation: vi.fn(),
}));
vi.mock('../../services/api', async (orig) => ({
  ...(await orig<typeof import('../../services/api')>()),
  ...api,
}));

import { PurgeSoftDeletedDialog } from './PurgeSoftDeletedDialog';
import { OperationTrackerProvider } from '../operations/OperationTrackerProvider';
import { usePurgeRun, type PurgeRunEnd } from '../../hooks/usePurgeRun';

/** The page's role: owns the run, renders the dialog, can close and reopen it. */
function Harness({
  onFinished,
  onStarted,
  onForgotten,
}: {
  onFinished: (end: PurgeRunEnd) => void;
  onStarted?: (operationId: string) => void;
  onForgotten?: () => void;
}) {
  const run = usePurgeRun(onFinished, onStarted);
  const [open, setOpen] = useState(true);
  return (
    <>
      <button onClick={() => setOpen(true)}>reopen</button>
      <span data-testid="in-progress">{String(run.running)}</span>
      <PurgeSoftDeletedDialog
        open={open}
        onClose={() => setOpen(false)}
        run={run}
        onFileRecordsForgotten={onForgotten}
      />
    </>
  );
}

// Nanosecond precision on purpose: the dialog must hand the server's string back
// unchanged, and a trip through Date would round it to milliseconds.
const PREVIEW_AT = '2026-10-23T12:00:00.123456789Z';

const preview: PurgeResult = {
  dry_run: true,
  attempted: 40,
  purged: 0,
  purgeable: 3,
  files_deleted: 0,
  skipped_owns_files: 30,
  kept_undo_possible: 5,
  kept_has_progress: 2,
  trashed_before: PREVIEW_AT,
  errors: [],
};

function setup(onFinished = vi.fn(), onStarted = vi.fn(), onForgotten = vi.fn()) {
  api.previewPurgeSoftDeleted.mockResolvedValue({ id: 'op-prev', dry_run: true });
  api.purgeSoftDeletedBooks.mockResolvedValue({ id: 'op-run', dry_run: false });
  api.cancelOperation.mockResolvedValue(undefined);
  renderWithProviders(
    <OperationTrackerProvider>
      <Harness onFinished={onFinished} onStarted={onStarted} onForgotten={onForgotten} />
    </OperationTrackerProvider>
  );
  return onFinished;
}

/**
 * Makes the purge operation (the second awaitPurgeResult call, after the
 * preview) hang until the test settles it, reporting `progress` first.
 */
function deferRun(progress?: { done: number; total: number; message: string }) {
  const ctl = {
    resolve: (_r: PurgeResult) => {},
    reject: (_e: Error) => {},
  };
  api.awaitPurgeResult.mockImplementationOnce(
    (_id: string, onProgress: (p: { done: number; total: number; message: string }) => void) => {
      if (progress) onProgress(progress);
      return new Promise<PurgeResult>((resolve, reject) => {
        ctl.resolve = resolve;
        ctl.reject = reject;
      });
    }
  );
  return ctl;
}

/**
 * Clicks Purge: the dialog closes as soon as the server has queued the purge,
 * so the run is then only reachable by reopening the dialog.
 */
async function purgeThenReopen() {
  fireEvent.click(await screen.findByRole('button', { name: 'Purge 3 books' }));
  await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
  fireEvent.click(screen.getByText('reopen'));
  await screen.findByTestId('purge-progress');
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
      '5 are the leftover copies of a combine, merge or other change that can still be undone'
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

  it('with nothing purgeable but files an earlier purge left waiting, offers to finish removing them with delete files on', async () => {
    api.awaitPurgeResult.mockResolvedValueOnce({
      ...preview,
      attempted: 0,
      purgeable: 0,
      skipped_owns_files: 0,
      kept_undo_possible: 0,
      kept_has_progress: 0,
      files_pending_from_earlier_pass: 4,
    });
    deferRun();
    setup();
    expect(await screen.findByTestId('purge-earlier-only')).toHaveTextContent(
      '4 files of books an earlier purge removed are still waiting to be deleted from disk'
    );
    // No delete-files box: nothing is purgeable, so there is nothing to choose.
    expect(screen.queryByLabelText('Also delete files from disk (if they still exist)')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Nothing to purge' })).toBeNull();
    const finish = screen.getByRole('button', { name: 'Finish removing 4 files' });
    expect(finish).toBeEnabled();

    fireEvent.click(finish);
    await waitFor(() => expect(api.purgeSoftDeletedBooks).toHaveBeenCalledWith(PREVIEW_AT, true));
  });

  describe('records waiting for a folder that is missing or empty', () => {
    const waitingOnly: PurgeResult = {
      ...preview,
      attempted: 0,
      purgeable: 0,
      skipped_owns_files: 0,
      kept_undo_possible: 0,
      kept_has_progress: 0,
      files_pending_from_earlier_pass: 0,
      files_waiting_for_folder: 3,
    };

    it('says they could not be checked, offers no "Finish removing", and states what forgetting does', async () => {
      api.awaitPurgeResult.mockResolvedValueOnce(waitingOnly);
      setup();
      const alert = await screen.findByTestId('purge-waiting-for-folder');
      expect(alert).toHaveTextContent(
        '3 files of books an earlier purge removed could not be checked'
      );
      expect(alert).toHaveTextContent('folder is missing or empty');
      expect(alert).toHaveTextContent('"Finish removing" does not either');
      expect(alert).toHaveTextContent('deletes only the records, never a file or a book');
      // They are not the pending group: nothing to finish, nothing to purge.
      expect(screen.queryByTestId('purge-earlier-only')).toBeNull();
      expect(screen.queryByRole('button', { name: /Finish removing/ })).toBeNull();
      expect(screen.getByRole('button', { name: 'Nothing to purge' })).toBeDisabled();
      expect(screen.getByRole('button', { name: 'Forget 3 records' })).toBeEnabled();
    });

    it('with pending files too, finishes only the pending ones', async () => {
      api.awaitPurgeResult.mockResolvedValueOnce({
        ...waitingOnly,
        files_pending_from_earlier_pass: 4,
      });
      setup();
      expect(await screen.findByTestId('purge-earlier-only')).toHaveTextContent('4 files');
      expect(screen.getByTestId('purge-waiting-for-folder')).toHaveTextContent('3 files');
      expect(screen.getByRole('button', { name: 'Finish removing 4 files' })).toBeEnabled();
    });

    it('shows nothing about them when none are waiting', async () => {
      api.awaitPurgeResult.mockResolvedValueOnce({ ...waitingOnly, files_waiting_for_folder: 0 });
      setup();
      await screen.findByTestId('purge-preview-headline');
      expect(screen.queryByTestId('purge-waiting-for-folder')).toBeNull();
    });

    it('asks first: nothing is forgotten until the owner confirms, and "Keep them" backs out', async () => {
      api.awaitPurgeResult.mockResolvedValueOnce(waitingOnly);
      setup();
      fireEvent.click(await screen.findByRole('button', { name: 'Forget 3 records' }));
      expect(screen.getByTestId('purge-forget-confirm')).toHaveTextContent(
        'its files stay on disk, and a library scan would import them as new books'
      );
      expect(api.forgetWaitingFileRecords).not.toHaveBeenCalled();
      fireEvent.click(screen.getByRole('button', { name: 'Keep them' }));
      expect(screen.queryByTestId('purge-forget-confirm')).toBeNull();
      expect(api.forgetWaitingFileRecords).not.toHaveBeenCalled();
    });

    it('forgets on confirm, tells the page, re-checks, and reports what it did', async () => {
      api.awaitPurgeResult
        .mockResolvedValueOnce(waitingOnly)
        .mockResolvedValueOnce({ ...waitingOnly, files_waiting_for_folder: 0 });
      api.forgetWaitingFileRecords.mockResolvedValue({
        records_forgotten: 3,
        paths_forgotten: 2,
      });
      const onForgotten = vi.fn();
      setup(vi.fn(), vi.fn(), onForgotten);
      fireEvent.click(await screen.findByRole('button', { name: 'Forget 3 records' }));
      fireEvent.click(screen.getByRole('button', { name: 'Yes, forget them' }));
      await waitFor(() => expect(api.forgetWaitingFileRecords).toHaveBeenCalledTimes(1));
      expect(onForgotten).toHaveBeenCalledTimes(1);
      // Re-checked, so the dialog shows what is left, not what was.
      await waitFor(() => expect(api.previewPurgeSoftDeleted).toHaveBeenCalledTimes(2));
      expect(await screen.findByTestId('purge-forget-note')).toHaveTextContent(
        'Forgot 3 records of 2 files. No file and no book was touched.'
      );
      expect(screen.queryByTestId('purge-waiting-for-folder')).toBeNull();
    });

    it('shows a warning the server returned with the result', async () => {
      api.awaitPurgeResult.mockResolvedValue(waitingOnly);
      api.forgetWaitingFileRecords.mockResolvedValue({
        records_forgotten: 3,
        paths_forgotten: 2,
        warnings: ['the activity log row was not written'],
      });
      setup();
      fireEvent.click(await screen.findByRole('button', { name: 'Forget 3 records' }));
      fireEvent.click(screen.getByRole('button', { name: 'Yes, forget them' }));
      expect(await screen.findByTestId('purge-forget-note')).toHaveTextContent(
        'the activity log row was not written'
      );
    });

    it('when the server refuses, says nothing was forgotten and keeps the choice open', async () => {
      api.awaitPurgeResult.mockResolvedValue(waitingOnly);
      api.forgetWaitingFileRecords.mockRejectedValue(new Error('permission denied'));
      const onForgotten = vi.fn();
      setup(vi.fn(), vi.fn(), onForgotten);
      fireEvent.click(await screen.findByRole('button', { name: 'Forget 3 records' }));
      fireEvent.click(screen.getByRole('button', { name: 'Yes, forget them' }));
      expect(await screen.findByTestId('purge-forget-error')).toHaveTextContent(
        'permission denied The records were not forgotten.'
      );
      expect(onForgotten).not.toHaveBeenCalled();
      expect(api.previewPurgeSoftDeleted).toHaveBeenCalledTimes(1);
      expect(screen.getByRole('button', { name: 'Yes, forget them' })).toBeEnabled();
    });
  });

  describe('a preview nobody will read is cancelled on the server', () => {
    const closeDialog = () => fireEvent.keyDown(screen.getByRole('dialog'), { key: 'Escape' });

    it('closing the dialog while the check runs cancels it', async () => {
      api.awaitPurgeResult.mockImplementationOnce(() => new Promise<PurgeResult>(() => {}));
      setup();
      await screen.findByTestId('purge-previewing');
      await waitFor(() => expect(api.awaitPurgeResult).toHaveBeenCalled());
      closeDialog();
      await waitFor(() => expect(api.cancelOperation).toHaveBeenCalledWith('op-prev'));
      expect(api.cancelOperation).toHaveBeenCalledTimes(1);
    });

    it('a check whose polling failed is cancelled too, and the failure is shown', async () => {
      api.awaitPurgeResult.mockRejectedValueOnce(new Error('network down'));
      setup();
      expect(await screen.findByTestId('purge-error')).toHaveTextContent('network down');
      expect(api.cancelOperation).toHaveBeenCalledWith('op-prev');
    });

    it('a check that finished is never cancelled', async () => {
      api.awaitPurgeResult.mockResolvedValueOnce(preview);
      setup();
      await screen.findByTestId('purge-preview-headline');
      closeDialog();
      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
      expect(api.cancelOperation).not.toHaveBeenCalled();
    });

    it('a check queued after the dialog was already closed is cancelled as soon as its id is known', async () => {
      let queue: (h: { id: string; dry_run: boolean }) => void = () => {};
      api.previewPurgeSoftDeleted.mockImplementationOnce(
        () => new Promise((resolve) => (queue = resolve))
      );
      api.cancelOperation.mockResolvedValue(undefined);
      api.purgeSoftDeletedBooks.mockResolvedValue({ id: 'op-run', dry_run: false });
      renderWithProviders(
        <OperationTrackerProvider>
          <Harness onFinished={vi.fn()} />
        </OperationTrackerProvider>
      );
      await screen.findByTestId('purge-previewing');
      closeDialog();
      await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
      expect(api.cancelOperation).not.toHaveBeenCalled();
      queue({ id: 'op-late', dry_run: true });
      await waitFor(() => expect(api.cancelOperation).toHaveBeenCalledWith('op-late'));
      expect(api.awaitPurgeResult).not.toHaveBeenCalled();
    });
  });

  it('with nothing purgeable and nothing waiting, does not offer to finish anything', async () => {
    api.awaitPurgeResult.mockResolvedValueOnce({
      ...preview,
      purgeable: 0,
      files_pending_from_earlier_pass: 0,
    });
    setup();
    await screen.findByTestId('purge-preview-headline');
    expect(screen.queryByTestId('purge-earlier-only')).toBeNull();
    expect(screen.getByRole('button', { name: 'Nothing to purge' })).toBeDisabled();
  });

  it('closes right after the purge is queued, then reports the outcome from the background run', async () => {
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
    const run = deferRun({
      done: 20,
      total: 40,
      message: 'Purging soft-deleted books: 20 of 40 decided',
    });
    const onStarted = vi.fn();
    const onFinished = setup(vi.fn(), onStarted);
    fireEvent.click(await screen.findByRole('button', { name: 'Purge 3 books' }));

    // The dialog does not wait for the purge.
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    expect(api.purgeSoftDeletedBooks).toHaveBeenCalledWith(PREVIEW_AT, false);
    expect(onStarted).toHaveBeenCalledWith('op-run');
    expect(screen.getByTestId('in-progress')).toHaveTextContent('true');
    expect(onFinished).not.toHaveBeenCalled();

    run.resolve(result);
    await waitFor(() =>
      expect(onFinished).toHaveBeenCalledWith({ result, error: null, operationId: 'op-run' })
    );
    expect(screen.getByTestId('in-progress')).toHaveTextContent('false');
  });

  it('reopened mid-run it shows progress with the purge button disabled, then the full breakdown', async () => {
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
    const run = deferRun({
      done: 20,
      total: 40,
      message: 'Purging soft-deleted books: 20 of 40 decided',
    });
    setup();
    await purgeThenReopen();
    expect(screen.getByTestId('purge-progress')).toHaveTextContent(
      'Purging soft-deleted books: 20 of 40 decided'
    );
    expect(screen.getByRole('button', { name: 'Purging...' })).toBeDisabled();
    expect(screen.queryByRole('button', { name: 'Purge 3 books' })).toBeNull();

    run.resolve(result);
    expect(await screen.findByTestId('purge-result-headline')).toHaveTextContent(
      'Purged 3 of 40 soft-deleted books, and deleted 2 files from disk.'
    );
    expect(screen.getByTestId('purge-kept-owns_files')).toHaveTextContent('30');
    expect(screen.getByTestId('purge-kept-undo_possible')).toHaveTextContent('5');
    expect(screen.getByText(/1 book had listening progress, which was moved/)).toBeInTheDocument();
  });

  it('stays open and shows the server reason when the purge is refused', async () => {
    api.awaitPurgeResult.mockResolvedValueOnce(preview);
    const onFinished = setup();
    api.purgeSoftDeletedBooks.mockRejectedValueOnce(
      new Error('a purge is already running (operation op-nightly)')
    );
    fireEvent.click(await screen.findByRole('button', { name: 'Purge 3 books' }));
    expect(await screen.findByTestId('purge-error')).toHaveTextContent(
      'a purge is already running (operation op-nightly)'
    );
    expect(screen.getByRole('dialog')).toBeInTheDocument();
    expect(screen.getByTestId('in-progress')).toHaveTextContent('false');
    await waitFor(() =>
      expect(onFinished).toHaveBeenCalledWith({
        result: null,
        error: expect.stringContaining('already running'),
        operationId: null,
      })
    );
  });

  it('passes the delete-files choice to the purge', async () => {
    api.awaitPurgeResult.mockResolvedValueOnce(preview);
    // Ticking the box previews again, with delete files on.
    api.awaitPurgeResult.mockResolvedValueOnce(preview);
    api.awaitPurgeResult.mockResolvedValueOnce({
      attempted: 40,
      purged: 3,
      files_deleted: 3,
      errors: [],
    });
    const onFinished = setup();
    await screen.findByTestId('purge-preview-headline');
    fireEvent.click(screen.getByLabelText('Also delete files from disk (if they still exist)'));
    fireEvent.click(await screen.findByRole('button', { name: 'Purge 3 books' }));
    await waitFor(() => expect(onFinished).toHaveBeenCalled());
    expect(api.purgeSoftDeletedBooks).toHaveBeenCalledWith(PREVIEW_AT, true);
  });

  it('previews again with delete files on when the box is ticked, and off when it is cleared, so the preview is the run', async () => {
    api.awaitPurgeResult.mockResolvedValue(preview);
    setup();
    await screen.findByTestId('purge-preview-headline');
    expect(api.previewPurgeSoftDeleted).toHaveBeenCalledTimes(1);
    expect(api.previewPurgeSoftDeleted).toHaveBeenLastCalledWith(false);
    const box = 'Also delete files from disk (if they still exist)';
    fireEvent.click(screen.getByLabelText(box));
    await waitFor(() => expect(api.previewPurgeSoftDeleted).toHaveBeenCalledTimes(2));
    expect(api.previewPurgeSoftDeleted).toHaveBeenLastCalledWith(true);
    fireEvent.click(await screen.findByLabelText(box));
    await waitFor(() => expect(api.previewPurgeSoftDeleted).toHaveBeenCalledTimes(3));
    expect(api.previewPurgeSoftDeleted).toHaveBeenLastCalledWith(false);
  });

  it('keeps the delete-files box while it is ticked even when that preview purges nothing, so it can be cleared again', async () => {
    // Ticking the box previews again with files on; the server then refuses every
    // book (a library that is not mounted, say), so nothing is purgeable. The box
    // must still be there: without it the owner is stuck with files on.
    api.awaitPurgeResult
      .mockResolvedValueOnce(preview)
      .mockResolvedValueOnce({ ...preview, purgeable: 0, files_deleted: 0 })
      .mockResolvedValueOnce(preview);
    setup();
    await screen.findByTestId('purge-preview-headline');
    const box = 'Also delete files from disk (if they still exist)';
    fireEvent.click(screen.getByLabelText(box));
    await waitFor(() => expect(api.previewPurgeSoftDeleted).toHaveBeenLastCalledWith(true));
    expect(await screen.findByRole('button', { name: 'Nothing to purge' })).toBeDisabled();
    const ticked = screen.getByLabelText(box);
    expect(ticked).toBeChecked();
    fireEvent.click(ticked);
    await waitFor(() => expect(api.previewPurgeSoftDeleted).toHaveBeenLastCalledWith(false));
    expect(await screen.findByRole('button', { name: 'Purge 3 books' })).toBeEnabled();
  });

  it('cannot purge from a preview that did not say what it covered', async () => {
    api.awaitPurgeResult.mockResolvedValueOnce({ ...preview, trashed_before: undefined });
    setup();
    await screen.findByTestId('purge-preview-headline');
    expect(screen.getByRole('button', { name: 'Purge 3 books' })).toBeDisabled();
    expect(screen.getByText(/did not say which books this check covered/)).toBeInTheDocument();
    expect(api.purgeSoftDeletedBooks).not.toHaveBeenCalled();
  });

  it('with delete files ticked, lists the files a purge would remove and how many stay', async () => {
    api.awaitPurgeResult.mockResolvedValue({
      ...preview,
      files_to_delete: ['/library/A/one.m4b', '/library/B/two.m4b'],
      files_to_delete_count: 12,
      files_kept: 1,
    });
    setup();
    await screen.findByTestId('purge-preview-headline');
    // Nothing about files until the box is ticked.
    expect(screen.queryByTestId('purge-files-preview')).toBeNull();
    fireEvent.click(screen.getByLabelText('Also delete files from disk (if they still exist)'));
    const box = await screen.findByTestId('purge-files-preview');
    expect(box).toHaveTextContent('12 files would be deleted from disk');
    expect(box).toHaveTextContent('1 file stays where it is');
    expect(box).toHaveTextContent('/library/A/one.m4b');
    expect(box).toHaveTextContent('and 10 more');
  });

  it('with delete files ticked, says how many files an earlier purge left waiting', async () => {
    api.awaitPurgeResult.mockResolvedValue({
      ...preview,
      files_to_delete: [],
      files_to_delete_count: 0,
      files_pending_from_earlier_pass: 2,
    });
    setup();
    await screen.findByTestId('purge-preview-headline');
    expect(screen.queryByTestId('purge-files-earlier')).toBeNull();
    fireEvent.click(screen.getByLabelText('Also delete files from disk (if they still exist)'));
    expect(await screen.findByTestId('purge-files-earlier')).toHaveTextContent(
      '2 files of books an earlier purge removed are also waiting to be deleted'
    );
  });

  it('says so when no file would be deleted', async () => {
    api.awaitPurgeResult.mockResolvedValue({
      ...preview,
      files_to_delete: [],
      files_to_delete_count: 0,
      files_kept: 3,
    });
    setup();
    await screen.findByTestId('purge-preview-headline');
    fireEvent.click(screen.getByLabelText('Also delete files from disk (if they still exist)'));
    expect(await screen.findByTestId('purge-files-preview')).toHaveTextContent(
      'No file would be deleted from disk.'
    );
  });

  it('shows the failure reason when the preview failed, and offers to try again', async () => {
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
    const run = deferRun();
    setup();
    await purgeThenReopen();
    run.resolve({
      attempted: 40,
      purged: 1,
      files_deleted: 0,
      failed: 7,
      errors: Array.from({ length: 7 }, (_, i) => `b${i}: could not read`),
    });
    const errors = await screen.findByTestId('purge-result-errors');
    expect(errors).toHaveTextContent('b0: could not read');
    expect(errors).toHaveTextContent('and 2 more');
    expect(screen.getByText(/7 books could not be decided/)).toBeInTheDocument();
  });

  it('keeps running after the dialog closed, and reopening shows the running state, not a new preview', async () => {
    api.awaitPurgeResult.mockResolvedValueOnce(preview);
    deferRun({ done: 5, total: 40, message: 'Purging: 5 of 40 decided' });
    setup();
    await purgeThenReopen();
    expect(screen.getByTestId('purge-progress')).toHaveTextContent('Purging: 5 of 40 decided');
    expect(api.previewPurgeSoftDeleted).toHaveBeenCalledTimes(1);
  });

  it('reopening after the run ended starts a fresh preview, not a stale outcome', async () => {
    const result: PurgeResult = { attempted: 40, purged: 3, files_deleted: 0, errors: [] };
    api.awaitPurgeResult.mockResolvedValueOnce(preview);
    const run = deferRun();
    const onFinished = setup();
    fireEvent.click(await screen.findByRole('button', { name: 'Purge 3 books' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
    run.resolve(result);
    await waitFor(() => expect(onFinished).toHaveBeenCalled());
    api.awaitPurgeResult.mockResolvedValueOnce(preview);
    fireEvent.click(screen.getByText('reopen'));
    expect(await screen.findByTestId('purge-preview-headline')).toBeInTheDocument();
  });

  it('never moves the progress backwards', async () => {
    api.awaitPurgeResult.mockResolvedValueOnce(preview);
    api.awaitPurgeResult.mockImplementationOnce(
      (_id: string, onProgress: (p: { done: number; total: number; message: string }) => void) => {
        onProgress({ done: 30, total: 40, message: 'step 30' });
        onProgress({ done: 12, total: 40, message: 'step 12 (stale)' });
        return new Promise<PurgeResult>(() => {});
      }
    );
    setup();
    await purgeThenReopen();
    const progress = screen.getByTestId('purge-progress');
    await waitFor(() => expect(progress).toHaveTextContent('step 12 (stale)'));
    expect(progress.querySelector('[role="progressbar"]')).toHaveAttribute('aria-valuenow', '75');
  });

  it('shows a stopped-early banner with the exact counts decided so far', async () => {
    api.awaitPurgeResult.mockResolvedValueOnce(preview);
    const run = deferRun();
    const onFinished = setup();
    await purgeThenReopen();
    run.resolve({
      attempted: 40,
      purged: 3,
      files_deleted: 0,
      skipped_owns_files: 10,
      errors: [],
      stopped_early: true,
      stop_reason: 'cancelled',
    });
    const banner = await screen.findByTestId('purge-result-headline');
    expect(banner).toHaveTextContent('Stopped early (cancelled)');
    expect(banner).toHaveTextContent('13 of 40 books in the trash were decided');
    expect(screen.getByTestId('purge-kept-owns_files')).toHaveTextContent('10');
    await waitFor(() => expect(onFinished).toHaveBeenCalled());
  });

  it('says the outcome is unknown, pointing at the operation, when no result can be fetched', async () => {
    api.awaitPurgeResult.mockResolvedValueOnce(preview);
    const run = deferRun();
    const onFinished = setup();
    await purgeThenReopen();
    run.reject(
      new Error(
        'The purge operation ended as failed and no result was stored, so what it did is unknown. Check operation op-run in the Operations page'
      )
    );
    const err = await screen.findByTestId('purge-error');
    expect(err).toHaveTextContent('what it did is unknown');
    expect(err).toHaveTextContent('Check operation op-run');
    await waitFor(() =>
      expect(onFinished).toHaveBeenCalledWith({
        result: null,
        error: expect.stringContaining('unknown'),
        operationId: 'op-run',
      })
    );
    expect(screen.getByTestId('in-progress')).toHaveTextContent('false');
  });

  it('a preview with undecided books says so, with capped details, so the numbers add up', async () => {
    api.awaitPurgeResult.mockResolvedValueOnce({
      dry_run: true,
      attempted: 20,
      purged: 0,
      purgeable: 5,
      files_deleted: 0,
      skipped_owns_files: 4,
      failed: 11,
      errors: Array.from({ length: 7 }, (_, i) => `b${i}: could not read`),
    });
    setup();
    expect(await screen.findByTestId('purge-preview-note')).toHaveTextContent(
      '11 books could not be decided'
    );
    const details = screen.getByTestId('purge-result-errors');
    expect(details).toHaveTextContent('b4: could not read');
    expect(details).not.toHaveTextContent('b5: could not read');
    expect(details).toHaveTextContent('and 2 more');
  });

  it('a stopped preview is flagged as covering only part of the trash', async () => {
    api.awaitPurgeResult.mockResolvedValueOnce({
      ...preview,
      stopped_early: true,
      stop_reason: 'timed out',
    });
    setup();
    expect(await screen.findByTestId('purge-preview-headline')).toHaveTextContent(
      'Stopped early (timed out)'
    );
  });
});
