// file: web/src/components/audiobooks/BulkMetadataSearchDialog.test.tsx
// version: 1.2.0
// guid: ec4cb47b-6f18-4083-ab37-a05af679a097
// last-edited: 2026-09-12

import { useState } from 'react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, fireEvent, waitFor, act } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import { BulkMetadataSearchDialog } from './BulkMetadataSearchDialog';
import type { Audiobook } from '../../types';
import type { MetadataCandidate } from '../../services/api';

vi.mock('../../services/api', () => ({
  searchMetadataForBook: vi.fn(),
  applyMetadataCandidate: vi.fn(),
  getBookFiles: vi.fn(),
  undoLastApply: vi.fn(),
  markNoMatch: vi.fn(),
}));

import {
  searchMetadataForBook,
  applyMetadataCandidate,
  getBookFiles,
  undoLastApply,
} from '../../services/api';

const mockSearch = vi.mocked(searchMetadataForBook);
const mockApply = vi.mocked(applyMetadataCandidate);
const mockGetBookFiles = vi.mocked(getBookFiles);
const mockUndo = vi.mocked(undoLastApply);

const candidate: MetadataCandidate = {
  title: 'Candidate Match',
  author: 'Someone Else',
  source: 'openlibrary',
  score: 0.9,
};

function book(id: string, overrides: Partial<Audiobook> = {}): Audiobook {
  return {
    id,
    title: `Book ${id.toUpperCase()}`,
    author: 'Test Author',
    file_path: `/library/${id}.m4b`,
    ...overrides,
  } as Audiobook;
}

const toast = vi.fn();
const onClose = vi.fn();
const onComplete = vi.fn();
const onLibraryChanged = vi.fn();

function dialog(books: Audiobook[], open = true) {
  return (
    <BulkMetadataSearchDialog
      open={open}
      books={books}
      onClose={onClose}
      onComplete={onComplete}
      onLibraryChanged={onLibraryChanged}
      toast={toast}
    />
  );
}

function renderDialog(books: Audiobook[]) {
  return renderWithProviders(dialog(books));
}

type ApplyResult = Awaited<ReturnType<typeof applyMetadataCandidate>>;

// An apply request the test settles by hand, to act while it is in flight.
function deferredApply() {
  let resolve!: (v: ApplyResult) => void;
  let reject!: (e: Error) => void;
  mockApply.mockReturnValueOnce(
    new Promise<ApplyResult>((res, rej) => {
      resolve = res;
      reject = rej;
    })
  );
  return { resolve, reject };
}

const applyOk: ApplyResult = { message: 'ok', book: {} as never, source: 'openlibrary' };

// The Apply button only renders once the per-book search has resolved.
async function waitForBook(title: string) {
  await screen.findByText(title);
  return screen.findByRole('button', { name: 'Apply' });
}

function header() {
  return screen.getByText(/^Search Metadata — Book \d+ of \d+/).textContent;
}

function determinateProgress() {
  const bar = screen
    .getAllByRole('progressbar')
    .find((el) => el.getAttribute('aria-valuenow') !== null);
  return bar?.getAttribute('aria-valuenow');
}

beforeEach(() => {
  vi.clearAllMocks();
  mockSearch.mockResolvedValue({ results: [candidate] } as Awaited<
    ReturnType<typeof searchMetadataForBook>
  >);
  mockGetBookFiles.mockResolvedValue({ files: [], count: 0 });
  mockApply.mockResolvedValue({
    message: 'ok',
    book: {} as never,
    source: 'openlibrary',
  });
  mockUndo.mockResolvedValue({ message: 'ok', undone_fields: ['title'] });
});

describe('BulkMetadataSearchDialog — applied books', () => {
  it('removes an applied book from the list and advances to the book after it', async () => {
    renderDialog([book('a'), book('b'), book('c')]);
    await waitForBook('Book A');
    expect(header()).toBe('Search Metadata — Book 1 of 3');

    fireEvent.click(screen.getByRole('button', { name: /next/i }));
    const apply = await waitForBook('Book B');
    fireEvent.click(apply);

    // The book after B, not the book that slid into B's old index slot.
    await waitForBook('Book C');
    expect(mockApply).toHaveBeenCalledWith('b', candidate, undefined, true);
    expect(screen.queryByText('Book B')).not.toBeInTheDocument();
    expect(header()).toBe('Search Metadata — Book 2 of 2 (1 filtered)');
    expect(screen.getByText('1 applied')).toBeInTheDocument();
    // Progress is measured against the 3-book work set, not the shrinking list.
    expect(Number(determinateProgress())).toBeCloseTo(100 / 3, 5);
  });

  it('keeps a book whose apply failed in the list and surfaces the error', async () => {
    mockApply.mockRejectedValueOnce(new Error('provider timed out'));
    renderDialog([book('a'), book('b'), book('c')]);
    fireEvent.click(await waitForBook('Book A'));

    await waitFor(() => expect(toast).toHaveBeenCalledWith('provider timed out', 'error'));
    expect(screen.getByText('Book A')).toBeInTheDocument();
    expect(header()).toBe('Search Metadata — Book 1 of 3');
    expect(screen.getByRole('button', { name: 'Apply' })).toBeEnabled();
    expect(screen.queryByText('1 applied')).not.toBeInTheDocument();
  });

  it('shows the empty state after the last book is applied, with undo still available', async () => {
    renderDialog([book('a')]);
    fireEvent.click(await waitForBook('Book A'));

    expect(await screen.findByText(/All 1 book\(s\) have metadata applied/)).toBeInTheDocument();
    const undo = screen.getByRole('button', { name: /Undo Last \(1\)/ });
    fireEvent.click(undo);

    await waitFor(() => expect(mockUndo).toHaveBeenCalledWith('a'));
    await waitForBook('Book A');
    expect(header()).toBe('Search Metadata — Book 1 of 1');
  });

  it('hides books the server already reports as matched by default', async () => {
    renderDialog([book('a', { metadata_review_status: 'matched' }), book('b')]);
    await waitForBook('Book B');
    expect(header()).toBe('Search Metadata — Book 1 of 1 (1 filtered)');
    expect(screen.getByLabelText('Skip applied')).toBeChecked();
  });

  it('with "Skip applied" off, keeps the applied book in the list marked Applied', async () => {
    renderDialog([book('a'), book('b'), book('c')]);
    await waitForBook('Book A');
    fireEvent.click(screen.getByLabelText('Skip applied'));
    fireEvent.click(await screen.findByRole('button', { name: 'Apply' }));

    await waitForBook('Book B');
    expect(header()).toBe('Search Metadata — Book 2 of 3');

    fireEvent.click(screen.getByRole('button', { name: /previous/i }));
    await screen.findByText('Book A');
    await waitFor(() =>
      expect(screen.getAllByRole('button', { name: 'Applied' })[0]).toBeDisabled()
    );
    expect(screen.getByText('Applied', { selector: '.MuiChip-label' })).toBeInTheDocument();
  });

  it('applying the last of several books moves back to the book before it', async () => {
    renderDialog([book('a'), book('b'), book('c')]);
    await waitForBook('Book A');
    fireEvent.click(screen.getByRole('button', { name: /next/i }));
    await waitForBook('Book B');
    fireEvent.click(screen.getByRole('button', { name: /next/i }));
    fireEvent.click(await waitForBook('Book C'));

    // No book after C, so the successor is the one before it, not the first
    // book in the list and not the empty state.
    await waitForBook('Book B');
    expect(mockApply).toHaveBeenCalledWith('c', candidate, undefined, true);
    expect(screen.queryByText('Book C')).not.toBeInTheDocument();
    expect(header()).toBe('Search Metadata — Book 2 of 2 (1 filtered)');
    expect(Number(determinateProgress())).toBeCloseTo(100 / 3, 5);
  });
});

describe('BulkMetadataSearchDialog — closed while an apply is in flight', () => {
  const books = [book('a'), book('b'), book('c')];

  async function applyThenClose() {
    const pending = deferredApply();
    const view = renderDialog(books);
    fireEvent.click(await waitForBook('Book A'));
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    // Nothing had been applied yet when the user closed, so no refresh.
    expect(onComplete).not.toHaveBeenCalled();
    view.rerender(dialog(books, false));
    return { ...pending, view };
  }

  async function reopenAndExpectFreshSession(view: ReturnType<typeof renderDialog>) {
    view.rerender(dialog(books, true));
    const apply = await waitForBook('Book A');
    expect(header()).toBe('Search Metadata — Book 1 of 3');
    expect(screen.queryByRole('button', { name: /Undo Last/ })).not.toBeInTheDocument();
    expect(screen.queryByText('1 applied')).not.toBeInTheDocument();
    expect(apply).toBeEnabled();
  }

  it('a late success refreshes the list but leaks nothing into the next session', async () => {
    const { resolve, view } = await applyThenClose();
    await act(async () => resolve(applyOk));

    expect(mockApply).toHaveBeenCalledWith('a', candidate, undefined, true);
    expect(toast).not.toHaveBeenCalled();
    // The server did change the book, so the list behind the dialog reloads.
    // Only the list: onComplete also clears the selection, which by now may
    // belong to a new session (see the "reopened on a new selection" tests).
    expect(onLibraryChanged).toHaveBeenCalledTimes(1);
    expect(onComplete).not.toHaveBeenCalled();
    await reopenAndExpectFreshSession(view);
  });

  it('a late failure shows no error toast and leaks nothing into the next session', async () => {
    const { reject, view } = await applyThenClose();
    await act(async () => reject(new Error('provider timed out')));

    expect(toast).not.toHaveBeenCalled();
    expect(onComplete).not.toHaveBeenCalled();
    expect(onLibraryChanged).not.toHaveBeenCalled();
    await reopenAndExpectFreshSession(view);
  });

  it('a request that settles after unmount neither toasts nor refreshes', async () => {
    const pending = deferredApply();
    const view = renderDialog(books);
    fireEvent.click(await waitForBook('Book A'));
    view.unmount();
    await act(async () => pending.resolve(applyOk));

    expect(toast).not.toHaveBeenCalled();
    expect(onComplete).not.toHaveBeenCalled();
    expect(onLibraryChanged).not.toHaveBeenCalled();
  });
});

// Mirrors how LibraryDialogs wires the dialog: the selection lives in the
// parent, and onComplete clears it. A write from a closed session that lands
// after the dialog was reopened on a new selection must not reach onComplete,
// or it empties the new session's books mid-use.
function Harness({ open, selection }: { open: boolean; selection: Audiobook[] }) {
  // The selection onComplete cleared; a new `selection` prop is a new, uncleared one.
  const [cleared, setCleared] = useState<Audiobook[] | null>(null);
  const books = cleared === selection ? [] : selection;
  return (
    <BulkMetadataSearchDialog
      open={open}
      books={books}
      onClose={onClose}
      onComplete={() => {
        onComplete();
        setCleared(selection);
      }}
      onLibraryChanged={onLibraryChanged}
      toast={toast}
    />
  );
}

describe('BulkMetadataSearchDialog — reopened on a new selection before a late write', () => {
  const first = [book('a'), book('b')];
  const second = [book('x'), book('y')];

  // Close the first session, reopen on `second`, and check the new session is
  // showing its own books. Resets the callback mocks so the caller asserts
  // only what the late write does.
  async function reopenOnSecond(view: ReturnType<typeof renderWithProviders>) {
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    view.rerender(<Harness open={false} selection={first} />);
    view.rerender(<Harness open selection={second} />);
    await waitForBook('Book X');
    expect(header()).toBe('Search Metadata — Book 1 of 2');
    onComplete.mockClear();
    onLibraryChanged.mockClear();
    toast.mockClear();
  }

  function expectSecondSessionIntact() {
    // The new session still has its books: nothing cleared the selection.
    expect(onComplete).not.toHaveBeenCalled();
    expect(screen.getByText('Book X')).toBeInTheDocument();
    expect(header()).toBe('Search Metadata — Book 1 of 2');
    // The server did change a book, so the list behind the dialog reloads.
    expect(onLibraryChanged).toHaveBeenCalledTimes(1);
  }

  it('a late apply reloads the list and leaves the new selection alone', async () => {
    const pending = deferredApply();
    const view = renderWithProviders(<Harness open selection={first} />);
    fireEvent.click(await waitForBook('Book A'));
    await reopenOnSecond(view);

    await act(async () => pending.resolve(applyOk));

    expect(mockApply).toHaveBeenCalledWith('a', candidate, undefined, true);
    expectSecondSessionIntact();
  });

  it('a late "Undo Last" reloads the list and leaves the new selection alone', async () => {
    const view = renderWithProviders(<Harness open selection={first} />);
    fireEvent.click(await waitForBook('Book A'));
    await waitForBook('Book B');
    let resolveUndo!: (v: Awaited<ReturnType<typeof undoLastApply>>) => void;
    mockUndo.mockReturnValueOnce(new Promise((res) => (resolveUndo = res)));
    // The Tooltip names the button, so target its label text.
    fireEvent.click(screen.getByText('Undo Last (1)'));
    await reopenOnSecond(view);

    await act(async () => resolveUndo({ message: 'ok', undone_fields: ['title'] }));

    expect(mockUndo).toHaveBeenCalledWith('a');
    expectSecondSessionIntact();
  });

  it('an Undo clicked on a toast that outlived its dialog leaves the new selection alone', async () => {
    const view = renderWithProviders(<Harness open selection={first} />);
    fireEvent.click(await waitForBook('Book A'));
    await waitForBook('Book B');
    const undoAction = toast.mock.calls.find((c) => c[2]?.label === 'Undo')?.[2];
    expect(undoAction).toBeDefined();
    await reopenOnSecond(view);

    await act(async () => undoAction.onClick());

    expect(mockUndo).toHaveBeenCalledWith('a');
    expectSecondSessionIntact();
  });
});
