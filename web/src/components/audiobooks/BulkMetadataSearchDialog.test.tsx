// file: web/src/components/audiobooks/BulkMetadataSearchDialog.test.tsx
// version: 2.0.1
// guid: ec4cb47b-6f18-4083-ab37-a05af679a097
// last-edited: 2026-10-10

import { useState } from 'react';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, fireEvent, waitFor, act, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import { BulkMetadataSearchDialog } from './BulkMetadataSearchDialog';
import type { Audiobook } from '../../types';
import type { MetadataCandidate, OperationV2 } from '../../services/api';

vi.mock('../../services/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../services/api')>();
  return {
    // The real error type and its reader: the background submit tells an
    // ASIN-conflict refusal from any other failure with them.
    ApiError: actual.ApiError,
    asinConflictOf: actual.asinConflictOf,
    searchMetadataForBook: vi.fn(),
    applyMetadataCandidate: vi.fn(),
    getBookFiles: vi.fn(),
    undoLastApply: vi.fn(),
    markNoMatch: vi.fn(),
    pollOperationV2: vi.fn(),
    getBook: vi.fn(),
  };
});

import {
  searchMetadataForBook,
  applyMetadataCandidate,
  getBookFiles,
  undoLastApply,
  markNoMatch,
  pollOperationV2,
  getBook,
} from '../../services/api';

const mockSearch = vi.mocked(searchMetadataForBook);
const mockApply = vi.mocked(applyMetadataCandidate);
const mockGetBookFiles = vi.mocked(getBookFiles);
const mockUndo = vi.mocked(undoLastApply);
const mockNoMatch = vi.mocked(markNoMatch);
const mockPoll = vi.mocked(pollOperationV2);
const mockGetBook = vi.mocked(getBook);

const candidate: MetadataCandidate = {
  title: 'Candidate Match',
  author: 'Someone Else',
  source: 'openlibrary',
  score: 0.9,
};
const other: MetadataCandidate = {
  title: 'Other Match',
  author: 'Another Writer',
  narrator: 'Some Reader',
  source: 'audible',
  score: 0.8,
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

function accepted(bookId: string): ApplyResult {
  return {
    message: 'queued',
    book: { id: bookId } as never,
    source: 'openlibrary',
    background: true,
    queued: true,
    operation_id: `op-${bookId}`,
  };
}

// A background apply the test settles by hand, to act while it is in flight.
function deferredApply() {
  let resolve!: (v: ApplyResult) => void;
  mockApply.mockReturnValueOnce(
    new Promise<ApplyResult>((res) => {
      resolve = res;
    })
  );
  return { resolve };
}

// The Pick buttons only render once the per-book search has resolved.
async function waitForBook(title: string) {
  await screen.findByText(title);
  return (await screen.findAllByRole('button', { name: /^(Pick|Picked)$/ }))[0];
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

function pickButtons() {
  return screen.getAllByRole('button', { name: /^(Pick|Picked)$/ });
}

beforeEach(() => {
  vi.clearAllMocks();
  mockSearch.mockResolvedValue({ results: [candidate, other] } as Awaited<
    ReturnType<typeof searchMetadataForBook>
  >);
  mockGetBookFiles.mockResolvedValue({ files: [], count: 0 });
  mockApply.mockImplementation(async (bookId) => accepted(bookId));
  mockPoll.mockResolvedValue({ status: 'completed' } as OperationV2);
  mockGetBook.mockImplementation(async (id) => ({ id }) as never);
  mockUndo.mockResolvedValue({ message: 'ok', undone_fields: ['title'] });
  mockNoMatch.mockResolvedValue(undefined);
});

describe('BulkMetadataSearchDialog — picks are staged, never applied while open', () => {
  it('a pick sends nothing, moves on to the next book and is counted as staged', async () => {
    renderDialog([book('a'), book('b'), book('c')]);
    fireEvent.click(await waitForBook('Book A'));

    await waitForBook('Book B');
    expect(header()).toBe('Search Metadata — Book 2 of 3');
    expect(screen.getByTestId('bulk-staged-count')).toHaveTextContent('1 staged');
    expect(mockApply).not.toHaveBeenCalled();
    // Progress counts staged books as handled.
    expect(Number(determinateProgress())).toBeCloseTo(100 / 3, 5);
  });

  it('never disables a pick: every book can be picked, and a picked book re-picked', async () => {
    renderDialog([book('a'), book('b')]);
    fireEvent.click(await waitForBook('Book A'));
    await waitForBook('Book B');
    for (const b of pickButtons()) expect(b).toBeEnabled();
    fireEvent.click(pickButtons()[0]);
    // Last book: it stays. Both picks remain enabled, one shows Picked.
    await waitFor(() => expect(screen.getByTestId('bulk-staged-count')).toHaveTextContent('2'));
    for (const b of pickButtons()) expect(b).toBeEnabled();
    expect(screen.getByRole('button', { name: 'Picked' })).toBeEnabled();
    expect(screen.getByRole('button', { name: /Apply 2 books & close/ })).toBeEnabled();
    expect(mockApply).not.toHaveBeenCalled();
  });

  it('a new pick for a book replaces its staged one, and going back shows it', async () => {
    renderDialog([book('a'), book('b')]);
    await waitForBook('Book A');
    fireEvent.click(pickButtons()[0]); // candidate
    await waitForBook('Book B');
    fireEvent.click(screen.getByRole('button', { name: /previous/i }));
    await screen.findByText('Book A');
    // The search re-ran (new objects), yet the staged card is still marked.
    const banner = await screen.findByTestId('bulk-staged-pick');
    expect(banner).toHaveTextContent('Candidate Match');
    await waitFor(() => expect(pickButtons()[0]).toHaveTextContent('Picked'));

    fireEvent.click(pickButtons()[1]); // other: replaces, still one staged
    await waitForBook('Book B');
    expect(screen.getByTestId('bulk-staged-count')).toHaveTextContent('1 staged');

    fireEvent.click(screen.getByRole('button', { name: /Apply 1 book & close/ }));
    await waitFor(() => expect(mockApply).toHaveBeenCalledTimes(1));
    expect(mockApply).toHaveBeenCalledWith('a', other, undefined, true, undefined, {
      background: true,
    });
  });

  it('Unstage drops the current book’s pick', async () => {
    renderDialog([book('a'), book('b')]);
    fireEvent.click(await waitForBook('Book A'));
    await waitForBook('Book B');
    fireEvent.click(screen.getByRole('button', { name: /previous/i }));
    const banner = await screen.findByTestId('bulk-staged-pick');
    fireEvent.click(within(banner).getByRole('button', { name: 'Unstage' }));

    await waitFor(() => expect(screen.queryByTestId('bulk-staged-pick')).not.toBeInTheDocument());
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(mockApply).not.toHaveBeenCalled();
    expect(onComplete).not.toHaveBeenCalled();
  });

  it('No Match on a staged book drops its pick', async () => {
    renderDialog([book('a'), book('b')]);
    fireEvent.click(await waitForBook('Book A'));
    await waitForBook('Book B');
    fireEvent.click(screen.getByRole('button', { name: /previous/i }));
    await screen.findByTestId('bulk-staged-pick');
    fireEvent.click(screen.getByRole('button', { name: 'No Match' }));

    await waitFor(() => expect(mockNoMatch).toHaveBeenCalledWith('a'));
    await waitFor(() => expect(screen.queryByTestId('bulk-staged-count')).not.toBeInTheDocument());
    fireEvent.click(screen.getByRole('button', { name: /^(Close|Done)$/ }));
    expect(mockApply).not.toHaveBeenCalled();
  });

  it('stages only the ticked fields of the card they were ticked on', async () => {
    renderDialog([book('a')]);
    await waitForBook('Book A');
    const selectButtons = screen.getAllByRole('button', { name: /Select fields/ });
    fireEvent.click(selectButtons[0]);
    fireEvent.click(await screen.findByRole('checkbox', { name: /Title: Candidate Match/ }));
    fireEvent.click(screen.getAllByRole('button', { name: 'Stage selected' })[0]);

    await screen.findByTestId('bulk-staged-pick');
    fireEvent.click(screen.getByRole('button', { name: /Apply 1 book & close/ }));
    await waitFor(() =>
      expect(mockApply).toHaveBeenCalledWith('a', candidate, ['title'], true, undefined, {
        background: true,
      })
    );
  });
});

describe('BulkMetadataSearchDialog — ordered by rank_score, applies the clicked row', () => {
  // Server (score) order: X first. Y names no narrator, so its score carries
  // the 0.85 penalty, but its rank_score does not.
  const x: MetadataCandidate = {
    title: 'Sample Saga 2',
    author: 'Author 07',
    source: 'audible',
    score: 1.05,
    rank_score: 1.05,
  };
  const y: MetadataCandidate = {
    title: 'Sample Saga 1',
    author: 'Author 07',
    source: 'audible',
    score: 0.9775,
    rank_score: 1.15,
  };
  const titles = () => screen.getAllByText(/^Sample Saga [12]$/).map((el) => el.textContent);

  it('lists the highest rank_score first, whatever the order or the score', async () => {
    mockSearch.mockResolvedValue({ results: [x, y] } as Awaited<ReturnType<typeof searchMetadataForBook>>);
    renderDialog([book('a')]);
    await screen.findByText('Sample Saga 1');
    expect(titles()).toEqual(['Sample Saga 1', 'Sample Saga 2']);
  });

  it('falls back to score for a row with no rank_score', async () => {
    mockSearch.mockResolvedValue({
      results: [
        { ...x, rank_score: undefined },
        { ...y, rank_score: undefined },
      ],
    } as Awaited<ReturnType<typeof searchMetadataForBook>>);
    renderDialog([book('a')]);
    await screen.findByText('Sample Saga 1');
    expect(titles()).toEqual(['Sample Saga 2', 'Sample Saga 1']);
  });

  it('picking the first displayed row applies that exact candidate', async () => {
    mockSearch.mockResolvedValue({ results: [x, y] } as Awaited<ReturnType<typeof searchMetadataForBook>>);
    renderDialog([book('a')]);
    await screen.findByText('Sample Saga 1');
    fireEvent.click(pickButtons()[0]); // displayed first = Sample Saga 1 = server index 1
    fireEvent.click(await screen.findByRole('button', { name: /Apply 1 book & close/ }));
    await waitFor(() => expect(mockApply).toHaveBeenCalledTimes(1));
    expect(mockApply).toHaveBeenCalledWith('a', y, undefined, true, undefined, {
      background: true,
    });
  });
});

describe('BulkMetadataSearchDialog — closing applies every staged pick in the background', () => {
  it('submits each staged pick exactly once and closes before any apply answers', async () => {
    const a = deferredApply();
    const b = deferredApply();
    renderDialog([book('a'), book('b'), book('c')]);
    fireEvent.click(await waitForBook('Book A'));
    fireEvent.click(await waitForBook('Book B'));
    await waitForBook('Book C');

    fireEvent.click(screen.getByRole('button', { name: /Apply 2 books & close/ }));

    // Closed and handed back at once: nothing waited on the server.
    expect(onClose).toHaveBeenCalledTimes(1);
    expect(onComplete).toHaveBeenCalledTimes(1);
    expect(toast).toHaveBeenCalledWith('Applying metadata to 2 books in the background', 'info');
    await waitFor(() => expect(mockApply).toHaveBeenCalledTimes(2));
    expect(mockApply).toHaveBeenCalledWith('a', candidate, undefined, true, undefined, {
      background: true,
    });
    expect(mockApply).toHaveBeenCalledWith('b', candidate, undefined, true, undefined, {
      background: true,
    });
    expect(onLibraryChanged).not.toHaveBeenCalled();

    await act(async () => {
      a.resolve(accepted('a'));
      b.resolve(accepted('b'));
    });
    await waitFor(() =>
      expect(toast).toHaveBeenCalledWith(
        'Metadata applied to 2 of 2 books',
        'success',
        expect.objectContaining({ label: 'Undo all (2)' })
      )
    );
    expect(mockPoll).toHaveBeenCalledWith('op-a', undefined, 1500, { requestTimeoutMs: 15000 });
    expect(mockPoll).toHaveBeenCalledWith('op-b', undefined, 1500, { requestTimeoutMs: 15000 });
    // Settled: the list reloads once, the selection is not touched again.
    expect(onLibraryChanged).toHaveBeenCalledTimes(1);
    expect(onComplete).toHaveBeenCalledTimes(1);
    expect(mockApply).toHaveBeenCalledTimes(2);
  });

  it('Escape applies what is staged too', async () => {
    renderDialog([book('a'), book('b')]);
    fireEvent.click(await waitForBook('Book A'));
    await waitForBook('Book B');
    fireEvent.keyDown(screen.getAllByRole('dialog')[0], { key: 'Escape' });

    expect(onClose).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(mockApply).toHaveBeenCalledTimes(1));
  });

  it('Discard all & close submits nothing', async () => {
    renderDialog([book('a'), book('b')]);
    fireEvent.click(await waitForBook('Book A'));
    await waitForBook('Book B');
    fireEvent.click(screen.getByRole('button', { name: /Discard all & close/ }));

    expect(onClose).toHaveBeenCalledTimes(1);
    await act(async () => {});
    expect(mockApply).not.toHaveBeenCalled();
    expect(onComplete).not.toHaveBeenCalled();
    expect(toast).not.toHaveBeenCalled();
  });

  it('closing with nothing staged sends nothing and keeps the selection', async () => {
    renderDialog([book('a')]);
    await waitForBook('Book A');
    fireEvent.click(screen.getByRole('button', { name: 'Close' }));
    expect(mockApply).not.toHaveBeenCalled();
    expect(onComplete).not.toHaveBeenCalled();
  });

  it('reopening starts a fresh session with nothing staged', async () => {
    const books = [book('a'), book('b')];
    const view = renderDialog(books);
    fireEvent.click(await waitForBook('Book A'));
    await waitForBook('Book B');
    fireEvent.click(screen.getByRole('button', { name: /Apply 1 book & close/ }));
    view.rerender(dialog(books, false));
    view.rerender(dialog(books, true));

    await waitForBook('Book A');
    expect(header()).toBe('Search Metadata — Book 1 of 2');
    expect(screen.queryByTestId('bulk-staged-count')).not.toBeInTheDocument();
  });

  it('applies that settle after unmount still report, but do not reload the list', async () => {
    const pending = deferredApply();
    const view = renderDialog([book('a'), book('b')]);
    fireEvent.click(await waitForBook('Book A'));
    await waitForBook('Book B');
    fireEvent.click(screen.getByRole('button', { name: /Apply 1 book & close/ }));
    view.unmount();
    await act(async () => pending.resolve(accepted('a')));

    await waitFor(() =>
      expect(toast).toHaveBeenCalledWith(
        'Metadata applied to 1 of 1 book',
        'success',
        expect.objectContaining({ label: 'Undo' })
      )
    );
    expect(onLibraryChanged).not.toHaveBeenCalled();
  });

  it('reports every failure in one toast', async () => {
    mockApply.mockRejectedValueOnce(new Error('provider timed out'));
    renderDialog([book('a'), book('b')]);
    fireEvent.click(await waitForBook('Book A'));
    fireEvent.click(await waitForBook('Book B'));
    fireEvent.click(screen.getByRole('button', { name: /Apply 2 books & close/ }));

    await waitFor(() =>
      expect(toast).toHaveBeenCalledWith(
        'Metadata apply failed for 1 book: provider timed out',
        'error'
      )
    );
    expect(toast).toHaveBeenCalledWith(
      'Metadata applied to 1 of 2 books',
      'success',
      expect.objectContaining({ label: 'Undo' })
    );
  });
});

// Mirrors how LibraryDialogs wires the dialog: the selection lives in the
// parent, and onComplete clears it. Background applies from a closed session
// that land after the dialog was reopened on a new selection must not reach
// onComplete, or they empty the new session's books mid-use.
function Harness({ open, selection }: { open: boolean; selection: Audiobook[] }) {
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

describe('BulkMetadataSearchDialog — reopened on a new selection before the applies land', () => {
  const first = [book('a'), book('b')];
  const second = [book('x'), book('y')];

  it('late completions and their Undo reload the list and leave the new selection alone', async () => {
    const pending = deferredApply();
    const view = renderWithProviders(<Harness open selection={first} />);
    fireEvent.click(await waitForBook('Book A'));
    await waitForBook('Book B');
    fireEvent.click(screen.getByRole('button', { name: /Apply 1 book & close/ }));
    view.rerender(<Harness open={false} selection={first} />);
    view.rerender(<Harness open selection={second} />);
    await waitForBook('Book X');
    onComplete.mockClear();

    await act(async () => pending.resolve(accepted('a')));
    await waitFor(() => expect(onLibraryChanged).toHaveBeenCalledTimes(1));
    const undo = toast.mock.calls.find((c) => c[2]?.label === 'Undo')?.[2];
    expect(undo).toBeDefined();
    await act(async () => undo.onClick());

    expect(mockUndo).toHaveBeenCalledWith('a');
    expect(onLibraryChanged).toHaveBeenCalledTimes(2);
    expect(onComplete).not.toHaveBeenCalled();
    expect(screen.getByText('Book X')).toBeInTheDocument();
    expect(header()).toBe('Search Metadata — Book 1 of 2');
  });
});

describe('BulkMetadataSearchDialog — ASIN conflict', () => {
  const conflicting: MetadataCandidate = {
    ...candidate,
    asin: 'B00OTHERAS',
    apply_check: {
      asin_conflict: true,
      book_asin: 'B00BOOKASI',
      detail: 'book ASIN B00BOOKASI, candidate B00OTHERAS',
    },
  };

  it('asks before staging a flagged candidate and sends the override it was shown', async () => {
    mockSearch.mockResolvedValue({ results: [conflicting] } as Awaited<
      ReturnType<typeof searchMetadataForBook>
    >);
    renderDialog([book('a')]);
    expect(await screen.findByText('ASIN conflict')).toBeInTheDocument();
    fireEvent.click(await waitForBook('Book A'));
    expect(await screen.findByText('Pick over an ASIN conflict?')).toBeInTheDocument();

    fireEvent.click(screen.getByRole('button', { name: 'Pick anyway' }));
    expect(await screen.findByTestId('bulk-staged-pick')).toHaveTextContent(
      'over the ASIN conflict'
    );
    expect(mockApply).not.toHaveBeenCalled();
    await waitFor(() =>
      expect(screen.queryByText('Pick over an ASIN conflict?')).not.toBeInTheDocument()
    );
    fireEvent.click(screen.getByRole('button', { name: /Apply 1 book & close/ }));
    await waitFor(() =>
      expect(mockApply).toHaveBeenCalledWith('a', conflicting, undefined, true, 'B00BOOKASI', {
        background: true,
      })
    );
  });

  it('stages nothing when the confirmation is cancelled', async () => {
    mockSearch.mockResolvedValue({ results: [conflicting] } as Awaited<
      ReturnType<typeof searchMetadataForBook>
    >);
    renderDialog([book('a')]);
    fireEvent.click(await waitForBook('Book A'));
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }));
    await waitFor(() =>
      expect(screen.queryByText('Pick over an ASIN conflict?')).not.toBeInTheDocument()
    );
    expect(screen.queryByTestId('bulk-staged-pick')).not.toBeInTheDocument();
  });

  it('a server refusal at submit is offered again from the toast with the override', async () => {
    const { ApiError } =
      await vi.importActual<typeof import('../../services/api')>('../../services/api');
    mockApply.mockRejectedValueOnce(
      new ApiError('conflict', 409, {
        reason: 'asin_conflict',
        book_asin: 'B00BOOKASI',
        candidate_asin: 'B00OTHERAS',
        detail: 'book ASIN B00BOOKASI, candidate B00OTHERAS',
      })
    );
    renderDialog([book('a')]);
    fireEvent.click(await waitForBook('Book A'));
    fireEvent.click(screen.getByRole('button', { name: /Apply 1 book & close/ }));

    await waitFor(() =>
      expect(toast).toHaveBeenCalledWith(
        'Not applied to "Book A": the candidate\'s ASIN is not the book\'s.',
        'warning',
        expect.objectContaining({ label: 'Apply anyway (1)' })
      )
    );
    const action = toast.mock.calls.find((c) => c[2]?.label === 'Apply anyway (1)')?.[2];
    await act(async () => action.onClick());
    await waitFor(() =>
      expect(mockApply).toHaveBeenLastCalledWith('a', candidate, undefined, true, 'B00BOOKASI', {
        background: true,
      })
    );
  });
});
