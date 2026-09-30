// file: web/src/components/review/ReviewWorkspace.refetchStale.test.tsx
// version: 1.5.0
// guid: 4d91c7a3-6b28-4e50-9f13-8a26c5b407de
// last-edited: 2026-09-30
//
// The refetch path from /review. Before this the stale chip's tooltip ended
// "refetch to be sure", naming a remedy the workspace had no way to reach --
// the only fetch entry point was a dialog on the Library page.
//
// The property worth guarding hardest is that the number the dialog shows and
// the set the button sends are the SAME set. They were not: the chip showed the
// server's count (every stale cache row, reviewable or not) while the button
// sent ids derived on the client from the reviewable bucket alone -- "3,511
// stale" on the chip, "Refetch 10 stale books?" in the dialog. The bulk path
// now shows `summary.stale` and POSTs {stale: true}, so the server resolves the
// set with the count's own predicate.
//
// The per-row path still sends one explicit id.

import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import userEvent from '@testing-library/user-event';
import { vi, describe, it, expect, beforeEach } from 'vitest';
import * as api from '../../services/api';
import { ReviewWorkspace } from './ReviewWorkspace';
import { ToastProvider } from '../toast/ToastProvider';

vi.mock('../../services/api');

function makeResult(id: string, overrides: Partial<api.CandidateResult> = {}) {
  return {
    book: { id, title: `Book ${id}`, language: 'en' },
    status: 'matched',
    candidate: {
      source: 'audible',
      title: `Cand ${id}`,
      author: 'A',
      narrator: 'N',
      score: 2.0,
      language: 'en',
    },
    ...overrides,
  } as unknown as api.CandidateResult;
}

function renderWorkspace() {
  return render(
    <MemoryRouter initialEntries={['/review']}>
      <ToastProvider>
        <ReviewWorkspace />
      </ToastProvider>
    </MemoryRouter>
  );
}

/** Seeds the review set and the collaborators the workspace touches on mount. */
function seed(results: api.CandidateResult[], stale: number) {
  vi.mocked(api.getCachedReviewResults).mockResolvedValue({
    results,
    total_count: results.length,
    matched: results.length,
    no_match: 0,
    errors: 0,
    stale,
  } as unknown as Awaited<ReturnType<typeof api.getCachedReviewResults>>);
  vi.mocked(api.getDedupCandidates).mockResolvedValue({ candidates: [], total: 0 });
  vi.mocked(api.getDedupStats).mockResolvedValue({ stats: [] });
  vi.mocked(api.getReviewItems).mockResolvedValue({
    items: [],
    count: 0,
    limit: 500,
    offset: 0,
    total: 0,
  });
  vi.mocked(api.getReviewCount).mockResolvedValue({ count: 0, by_kind: {} });
  // The STARTED shape, as `batchFetchCandidates` hands it back after unwrapping
  // the server's `{data:...}` envelope. This is flat on purpose: the mock stands
  // in for the api function at its own boundary, and that function's contract is
  // the unwrapped body, not the wire frame.
  //
  // What keeps this fixture from certifying a bug again is the declared return
  // type. `batchFetchCandidates` used to promise a flat shape while returning
  // the envelope; now that it promises `BatchFetchStartResponse` and delivers
  // it, a fixture written as `{data:{operation_id:'op-1'}}` is a type error
  // rather than a silently-passing lie.
  vi.mocked(api.batchFetchCandidates).mockResolvedValue({
    operation_id: 'op-1',
    total_books: 2,
    book_count: 2,
    skipped: 0,
    message: 'metadata candidate fetch started',
  });
  // CompareSpine (Task 7) now calls usePathAliases() itself, which pulls
  // config via api.getConfig(). The module is auto-mocked above, so without
  // this every mount throws "Cannot read properties of undefined (reading
  // 'then')" -- vi.fn() with no configured return resolves to undefined, not
  // a Promise.
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '' } as api.Config);
}

beforeEach(() => {
  vi.resetAllMocks();
  window.localStorage.clear();
});

async function openWorkspace() {
  renderWorkspace();
  await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());
}

describe('refetching stale rows from /review', () => {
  it('shows the server stale count and sends {stale: true}, only after the confirm', async () => {
    const user = userEvent.setup();
    // The server counts 3,511 stale rows; the client holds only two of them
    // (the rest are in the unreviewable bucket or simply not loaded). The
    // dialog must show the server's number and must not send the client's ids.
    seed(
      [
        makeResult('a', { is_fresh: false }),
        makeResult('b', { is_fresh: true }),
        makeResult('c', { is_fresh: false }),
      ] as api.CandidateResult[],
      3511
    );
    await openWorkspace();

    await user.click(screen.getByLabelText(/Refetch 3,511 stale books/i));

    // Nothing may leave for the providers on the strength of a chip click.
    expect(api.batchFetchCandidates).not.toHaveBeenCalled();
    expect(await screen.findByText(/Refetch 3,511 stale books\?/i)).toBeInTheDocument();
    expect(screen.getByTestId('refetch-stale-confirm')).toHaveTextContent('Refetch 3,511');

    await user.click(screen.getByTestId('refetch-stale-confirm'));

    await waitFor(() => expect(api.batchFetchCandidates).toHaveBeenCalledTimes(1));
    expect(api.batchFetchCandidates).toHaveBeenCalledWith({ stale: true });
  });

  it("reports the server's book_count and skipped, not a client count", async () => {
    const user = userEvent.setup();
    seed([makeResult('a', { is_fresh: false })] as api.CandidateResult[], 3511);
    vi.mocked(api.batchFetchCandidates).mockResolvedValue({
      operation_id: 'op-1',
      total_books: 3500,
      book_count: 3500,
      skipped: 11,
      message: 'metadata candidate fetch started',
    });
    await openWorkspace();

    await user.click(screen.getByLabelText(/Refetch 3,511 stale books/i));
    await user.click(screen.getByTestId('refetch-stale-confirm'));

    expect(
      await screen.findByText(/Refetching metadata for 3,500 books \(11 already being fetched\)/i)
    ).toBeInTheDocument();
  });

  // Regression, 2026-09-07. `batchFetchCandidates` returned the raw `{data:...}`
  // envelope, so `resp.operation_id` was ALWAYS undefined and this success path
  // fell into the `if (!resp.operation_id)` branch. Every refetch that the
  // server had actually enqueued told the reviewer their books were "already
  // being fetched" -- the operation ran, and the UI said nothing had started.
  //
  // Asserting the absence of that toast is the half that would have caught it:
  // the call-count assertions above all passed throughout the bug.
  it('reports a started refetch as started, not as already running', async () => {
    const user = userEvent.setup();
    seed(
      [makeResult('a', { is_fresh: false }), makeResult('c', { is_fresh: false })] as
        api.CandidateResult[],
      2
    );
    await openWorkspace();

    await user.click(screen.getByLabelText(/Refetch 2 stale books/i));
    await user.click(screen.getByTestId('refetch-stale-confirm'));

    await waitFor(() => expect(api.batchFetchCandidates).toHaveBeenCalledTimes(1));

    expect(await screen.findByText(/Refetching metadata for 2 books/i)).toBeInTheDocument();
    expect(screen.queryByText(/already being fetched/i)).not.toBeInTheDocument();
  });

  // The other side of the same guard. The server declines by sending an EMPTY
  // operation_id, and when it does the toast is correct -- so the fix above must
  // not be "delete the guard".
  it('still says so when the server declines because a fetch is already running', async () => {
    const user = userEvent.setup();
    seed(
      [makeResult('a', { is_fresh: false }), makeResult('c', { is_fresh: false })] as
        api.CandidateResult[],
      2
    );
    vi.mocked(api.batchFetchCandidates).mockResolvedValue({
      operation_id: '',
      book_count: 0,
      skipped: 2,
      message: 'All 2 books are already being fetched in another operation',
    });
    await openWorkspace();

    await user.click(screen.getByLabelText(/Refetch 2 stale books/i));
    await user.click(screen.getByTestId('refetch-stale-confirm'));

    await waitFor(() => expect(api.batchFetchCandidates).toHaveBeenCalledTimes(1));

    expect(await screen.findByText(/already being fetched/i)).toBeInTheDocument();
    expect(screen.queryByText(/Refetching metadata for/i)).not.toBeInTheDocument();
  });

  it('cancelling the confirm starts nothing', async () => {
    const user = userEvent.setup();
    seed([makeResult('a', { is_fresh: false })] as api.CandidateResult[], 1);
    await openWorkspace();

    await user.click(screen.getByLabelText(/Refetch 1 stale book/i));
    await user.click(screen.getByRole('button', { name: 'Cancel' }));

    expect(api.batchFetchCandidates).not.toHaveBeenCalled();
  });

  it('offers no refetch affordance when nothing is stale', async () => {
    seed([makeResult('a', { is_fresh: true })] as api.CandidateResult[], 0);
    await openWorkspace();

    expect(screen.queryByLabelText(/Refetch .* stale book/i)).not.toBeInTheDocument();
  });

  // The chip and the action now read the SAME number. An older design gated
  // the action on a client-derived id list, so a server that counted stale
  // rows the client could not see (unreviewable ones, or rows with no
  // per-row age) showed a chip with no action. The server resolves the set
  // now, so the count alone decides.
  it('offers the action whenever the server counts stale rows, even with no row ages', async () => {
    const user = userEvent.setup();
    seed([makeResult('a'), makeResult('b')] as api.CandidateResult[], 2);
    await openWorkspace();

    expect(screen.getByText('2 stale')).toBeInTheDocument();
    await user.click(screen.getByLabelText(/Refetch 2 stale books/i));
    await user.click(screen.getByTestId('refetch-stale-confirm'));

    await waitFor(() => expect(api.batchFetchCandidates).toHaveBeenCalledTimes(1));
    expect(api.batchFetchCandidates).toHaveBeenCalledWith({ stale: true });
  });

  // One row is not worth a dialog. It must also leave the row's selection
  // alone: the marker sits inside the row's bounds, next to its checkbox.
  it('refetches a single row straight through, without selecting it', async () => {
    const user = userEvent.setup();
    // TWO stale rows, deliberately: with only one, `[bookId]` and the whole
    // stale set are the same array and the test cannot tell them apart.
    seed(
      [makeResult('a', { is_fresh: false }), makeResult('b', { is_fresh: false })] as
        api.CandidateResult[],
      2
    );
    await openWorkspace();

    const checkbox = screen.getByLabelText('Select Book a') as HTMLInputElement;
    expect(checkbox.checked).toBe(false);

    await user.click(screen.getByLabelText(/Refetch metadata for Book a/i));

    await waitFor(() => expect(api.batchFetchCandidates).toHaveBeenCalledTimes(1));
    expect(api.batchFetchCandidates).toHaveBeenCalledWith({ book_ids: ['a'] });
    expect(checkbox.checked).toBe(false);
    // No confirm for a single book.
    expect(screen.queryByTestId('refetch-stale-confirm')).not.toBeInTheDocument();
  });
});
