// file: web/src/components/audiobooks/MetadataSearchDialog.test.tsx
// version: 2.1.0
// guid: b2cf5226-9131-4f1a-9fd2-5c3286e4f800
// last-edited: 2026-10-10

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, fireEvent, waitFor } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import { MetadataSearchDialog } from './MetadataSearchDialog';
import type { Book, MetadataCandidate } from '../../services/api';

vi.mock('../../services/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../services/api')>();
  return {
    ApiError: actual.ApiError,
    asinConflictOf: actual.asinConflictOf,
    searchMetadataForBook: vi.fn(),
    applyMetadataCandidate: vi.fn(),
    pollOperationV2: vi.fn(),
    getBook: vi.fn(),
    undoLastApply: vi.fn(),
    markNoMatch: vi.fn(),
  };
});

import {
  ApiError,
  searchMetadataForBook,
  applyMetadataCandidate,
  pollOperationV2,
  getBook,
  markNoMatch,
} from '../../services/api';
import type { OperationV2 } from '../../services/api';
import { submitStagedApply } from './stagedMetadataApply';

const mockSearch = vi.mocked(searchMetadataForBook);
const mockApply = vi.mocked(applyMetadataCandidate);
const mockPoll = vi.mocked(pollOperationV2);
const mockGetBook = vi.mocked(getBook);
const mockNoMatch = vi.mocked(markNoMatch);

const theBook = { id: 'b1', title: 'A Title', author_name: 'An Author' } as Book;
const toast = vi.fn();
const onClose = vi.fn();
const onApplied = vi.fn();

// A kept candidate naming another ASIN than the book carries.
const conflicting: MetadataCandidate = {
  title: 'Other Record',
  author: 'Someone',
  source: 'audible',
  score: 0.9,
  asin: 'B00OTHERAS',
  apply_check: {
    asin_conflict: true,
    book_asin: 'B00BOOKASI',
    detail: 'book ASIN B00BOOKASI, candidate B00OTHERAS',
  },
};

// A kept candidate with no ASIN, fetched for an ASIN the book no longer has.
const staleOne: MetadataCandidate = {
  title: 'No ASIN',
  author: 'Someone',
  source: 'openlibrary',
  score: 0.8,
  apply_check: { identity_stale: true, book_asin: 'B00BOOKASI', detail: 'fetched for B00OLDASIN' },
};

// A kept candidate the owner rejected for this book: the server flags it.
const rejectedOne: MetadataCandidate = {
  title: 'Rejected One',
  author: 'Someone',
  source: 'audible',
  // Scored above every other fixture so it renders first (the dialog sorts
  // by score): the case that matters is a rejected candidate on top.
  score: 0.99,
  apply_check: { owner_rejected: true, detail: 'rejected by the owner' },
};

const plain: MetadataCandidate = {
  title: 'Plain',
  author: 'An Author',
  source: 'audible',
  score: 0.95,
};

function renderDialog() {
  return renderWithProviders(
    <MetadataSearchDialog
      open
      book={theBook}
      onClose={onClose}
      onApplied={onApplied}
      toast={toast}
    />
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  // The background path: 202 with the op that runs the apply.
  mockApply.mockResolvedValue({
    message: 'applying',
    book: theBook,
    source: '',
    queued: true,
    background: true,
    operation_id: 'op-1',
  });
  mockPoll.mockResolvedValue({ id: 'op-1', status: 'completed', error_message: null } as OperationV2);
  mockGetBook.mockResolvedValue({ ...theBook, title: 'Applied Title' } as Book);
});

/** The footer's apply-and-close button, whatever its count says. */
function applyAndClose() {
  return screen.getByRole('button', { name: /^Apply \d+ fields? & close$/ });
}

function searchReturns(...results: MetadataCandidate[]) {
  mockSearch.mockResolvedValue({ results, query: '' });
}

describe('MetadataSearchDialog — candidate checks', () => {
  it('shows the ASIN conflict and the stale-ASIN warning on their candidates only', async () => {
    searchReturns(conflicting, staleOne, plain);
    renderDialog();
    expect(await screen.findByText('ASIN conflict')).toBeInTheDocument();
    expect(screen.getByText('Fetched for another ASIN')).toBeInTheDocument();
    expect(screen.getAllByText('ASIN conflict')).toHaveLength(1);
    expect(screen.getAllByText('Fetched for another ASIN')).toHaveLength(1);
  });

  it('marks an owner-rejected candidate and refuses to stage it', async () => {
    searchReturns(rejectedOne, plain);
    renderDialog();
    await screen.findByText('Rejected One');
    expect(screen.getAllByTestId('owner-rejected-chip')).toHaveLength(1);

    const picks = () => screen.getAllByRole('button', { name: /^(Pick|Picked)$/ });
    fireEvent.click(picks()[0]);
    expect(toast).toHaveBeenCalledWith(
      'You rejected this candidate for this book. Un-reject it to apply it.',
      'warning'
    );
    expect(screen.queryByTestId('staged-pick')).not.toBeInTheDocument();

    // The other candidate still stages normally.
    fireEvent.click(picks()[1]);
    expect(await screen.findByTestId('staged-pick')).toHaveTextContent('Plain');
    expect(mockApply).not.toHaveBeenCalled();
  });

});

describe('MetadataSearchDialog — staging, one apply on close', () => {
  it('never greys out after a pick: picks can be changed freely, nothing is sent yet', async () => {
    searchReturns(plain, staleOne);
    renderDialog();
    await screen.findByText('Plain');
    const picks = () => screen.getAllByRole('button', { name: /^(Pick|Picked)$/ });

    fireEvent.click(picks()[0]);
    expect(await screen.findByTestId('staged-pick')).toHaveTextContent('Plain');
    // Every pick button is still live, including the staged one.
    picks().forEach((b) => expect(b).toBeEnabled());

    fireEvent.click(picks()[1]);
    expect(screen.getByTestId('staged-pick')).toHaveTextContent('No ASIN');
    picks().forEach((b) => expect(b).toBeEnabled());

    fireEvent.click(picks()[0]);
    expect(screen.getByTestId('staged-pick')).toHaveTextContent('Plain');
    expect(mockApply).not.toHaveBeenCalled();
    expect(onClose).not.toHaveBeenCalled();
  });

  it('closing submits exactly ONE background request carrying the last pick', async () => {
    searchReturns(plain, staleOne);
    renderDialog();
    await screen.findByText('Plain');
    const picks = () => screen.getAllByRole('button', { name: /^(Pick|Picked)$/ });
    fireEvent.click(picks()[0]);
    fireEvent.click(picks()[1]);

    fireEvent.click(applyAndClose());
    expect(onClose).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(mockApply).toHaveBeenCalledTimes(1));
    expect(mockApply).toHaveBeenCalledWith('b1', staleOne, undefined, true, undefined, {
      background: true,
    });
    // Followed to the end: the fresh book is handed back and success toasted.
    await waitFor(() =>
      expect(onApplied).toHaveBeenCalledWith(expect.objectContaining({ title: 'Applied Title' }))
    );
    expect(mockPoll).toHaveBeenCalledWith('op-1', undefined, 1500, { requestTimeoutMs: 15000 });
    expect(toast).toHaveBeenCalledWith(
      expect.stringContaining('Metadata applied'),
      'success',
      expect.objectContaining({ label: 'Undo' })
    );
  });

  it('stages selected fields of one candidate and shows the staged count', async () => {
    searchReturns(plain);
    renderDialog();
    await screen.findByText('Plain');
    fireEvent.click(screen.getByRole('button', { name: /Select fields/ }));
    fireEvent.click(await screen.findByLabelText(/^Title:/));
    fireEvent.click(screen.getByLabelText(/^Author:/));
    fireEvent.click(screen.getByRole('button', { name: 'Stage selected' }));
    expect(screen.getByTestId('staged-pick')).toHaveTextContent('Staged: 2 fields');

    fireEvent.click(screen.getByRole('button', { name: 'Apply 2 fields & close' }));
    await waitFor(() => expect(mockApply).toHaveBeenCalledTimes(1));
    expect(mockApply.mock.calls[0][2]).toEqual(['title', 'author']);
  });

  it('closing by Escape applies the staged pick too', async () => {
    searchReturns(plain);
    renderDialog();
    fireEvent.click(await screen.findByRole('button', { name: 'Pick' }));
    fireEvent.keyDown(screen.getAllByRole('dialog')[0], { key: 'Escape' });
    expect(onClose).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(mockApply).toHaveBeenCalledTimes(1));
  });

  it('Discard drops the pick: closing afterwards sends nothing', async () => {
    searchReturns(plain);
    renderDialog();
    fireEvent.click(await screen.findByRole('button', { name: 'Pick' }));
    fireEvent.click(screen.getByRole('button', { name: 'Discard' }));
    expect(screen.queryByTestId('staged-pick')).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(onClose).toHaveBeenCalledTimes(1);
    await Promise.resolve();
    expect(mockApply).not.toHaveBeenCalled();
  });

  it('Discard & close closes without sending anything', async () => {
    searchReturns(plain);
    renderDialog();
    fireEvent.click(await screen.findByRole('button', { name: 'Pick' }));
    fireEvent.click(screen.getByRole('button', { name: 'Discard & close' }));
    expect(onClose).toHaveBeenCalledTimes(1);
    await Promise.resolve();
    expect(mockApply).not.toHaveBeenCalled();
  });

  it('the dialog closes at once; it does not wait for the apply to run', async () => {
    searchReturns(plain);
    mockApply.mockReturnValue(new Promise(() => {})); // never settles
    renderDialog();
    fireEvent.click(await screen.findByRole('button', { name: 'Pick' }));
    fireEvent.click(applyAndClose());
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(mockApply).toHaveBeenCalledTimes(1);
    expect(onApplied).not.toHaveBeenCalled();
  });

  it('No Match Found is held while a pick is staged', async () => {
    searchReturns(plain);
    renderDialog();
    fireEvent.click(await screen.findByRole('button', { name: 'Pick' }));
    expect(screen.getByRole('button', { name: 'No Match Found' })).toBeDisabled();
    fireEvent.click(screen.getByRole('button', { name: 'Discard' }));
    expect(screen.getByRole('button', { name: 'No Match Found' })).toBeEnabled();
    expect(mockNoMatch).not.toHaveBeenCalled();
  });

  it('asks before staging a conflicting candidate, then sends the override it was shown', async () => {
    searchReturns(conflicting);
    renderDialog();
    fireEvent.click(await screen.findByRole('button', { name: 'Pick' }));
    expect(await screen.findByText('Pick over an ASIN conflict?')).toBeInTheDocument();
    fireEvent.click(screen.getByRole('button', { name: 'Stage anyway' }));
    expect(await screen.findByTestId('staged-pick')).toHaveTextContent('over the ASIN conflict');
    expect(mockApply).not.toHaveBeenCalled();
    // The confirmation hides the dialog beneath it from the a11y tree until
    // its exit transition ends.
    await waitFor(() =>
      expect(screen.queryByText('Pick over an ASIN conflict?')).not.toBeInTheDocument()
    );

    fireEvent.click(applyAndClose());
    await waitFor(() =>
      expect(mockApply).toHaveBeenCalledWith('b1', conflicting, undefined, true, 'B00BOOKASI', {
        background: true,
      })
    );
  });

  it('stages nothing when the conflict confirmation is cancelled', async () => {
    searchReturns(conflicting);
    renderDialog();
    fireEvent.click(await screen.findByRole('button', { name: 'Pick' }));
    const dialogTitle = await screen.findByText('Pick over an ASIN conflict?');
    const cancel = screen
      .getAllByRole('button', { name: 'Cancel' })
      .find((b) => b.closest('[role="dialog"]')?.contains(dialogTitle));
    expect(cancel).toBeDefined();
    fireEvent.click(cancel!);
    await waitFor(() =>
      expect(screen.queryByText('Pick over an ASIN conflict?')).not.toBeInTheDocument()
    );
    expect(screen.queryByTestId('staged-pick')).not.toBeInTheDocument();
  });
});

describe('submitStagedApply — the detached background apply', () => {
  const args = (over: Partial<Parameters<typeof submitStagedApply>[0]> = {}) => ({
    book: theBook,
    pick: { candidate: plain },
    writeToFiles: true,
    toast,
    onApplied,
    ...over,
  });

  it('reports a failed operation and does not hand back a book', async () => {
    mockPoll.mockResolvedValue({
      id: 'op-1',
      status: 'failed',
      error_message: 'edited since',
    } as OperationV2);
    await submitStagedApply(args());
    expect(onApplied).not.toHaveBeenCalled();
    expect(toast).toHaveBeenLastCalledWith(expect.stringContaining('failed: edited since'), 'error');
  });

  it('turns a server ASIN refusal into a toast whose action resubmits with the override', async () => {
    mockApply.mockRejectedValueOnce(
      new ApiError('conflict', 409, {
        reason: 'asin_conflict',
        book_asin: 'B00BOOKASI',
        candidate_asin: 'B00OTHERAS',
        detail: 'book ASIN B00BOOKASI, candidate B00OTHERAS',
      })
    );
    await submitStagedApply(args());
    const call = toast.mock.calls.find((c) => c[1] === 'warning');
    expect(call?.[2]?.label).toBe('Apply anyway');
    expect(onApplied).not.toHaveBeenCalled();

    call![2]!.onClick();
    await waitFor(() =>
      expect(mockApply).toHaveBeenLastCalledWith('b1', plain, undefined, true, 'B00BOOKASI', {
        background: true,
      })
    );
  });

  it('a server that applied inline (no operation id) is reported at once', async () => {
    mockApply.mockResolvedValueOnce({ message: 'ok', book: theBook, source: 'audible' });
    await submitStagedApply(args());
    expect(mockPoll).not.toHaveBeenCalled();
    expect(onApplied).toHaveBeenCalledWith(theBook);
  });
});
