// file: web/src/components/review/ReviewWorkspace.refetchStale.test.tsx
// version: 1.1.0
// guid: 4d91c7a3-6b28-4e50-9f13-8a26c5b407de
// last-edited: 2026-08-20
//
// The refetch path from /review. Before this the stale chip's tooltip ended
// "refetch to be sure", naming a remedy the workspace had no way to reach --
// the only fetch entry point was a dialog on the Library page.
//
// The property worth guarding hardest is which rows count as stale. The payload
// distinguishes three states, not two: `is_fresh: false` (stale), `is_fresh:
// true` (fresh), and ABSENT (this row has no age at all). Treating absent as
// stale would sweep rows into a bulk provider fetch on a claim the server never
// made, which is why the predicate is `=== false` and not falsy.

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
  vi.mocked(api.getReviewCount).mockResolvedValue({ count: 0, byKind: {} });
  vi.mocked(api.batchFetchCandidates).mockResolvedValue({ operation_id: 'op-1' });
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
  it('sends exactly the stale book ids, and only after the confirm', async () => {
    const user = userEvent.setup();
    seed(
      [
        makeResult('a', { is_fresh: false }),
        makeResult('b', { is_fresh: true }),
        makeResult('c', { is_fresh: false }),
      ] as api.CandidateResult[],
      2
    );
    await openWorkspace();

    await user.click(screen.getByLabelText(/Refetch 2 stale books/i));

    // Nothing may leave for the providers on the strength of a chip click.
    expect(api.batchFetchCandidates).not.toHaveBeenCalled();
    expect(await screen.findByText(/Refetch 2 stale books\?/i)).toBeInTheDocument();

    await user.click(screen.getByTestId('refetch-stale-confirm'));

    await waitFor(() => expect(api.batchFetchCandidates).toHaveBeenCalledTimes(1));
    expect(api.batchFetchCandidates).toHaveBeenCalledWith({ book_ids: ['a', 'c'] });
  });

  it('cancelling the confirm starts nothing', async () => {
    const user = userEvent.setup();
    seed([makeResult('a', { is_fresh: false })] as api.CandidateResult[], 1);
    await openWorkspace();

    await user.click(screen.getByLabelText(/Refetch 1 stale book/i));
    await user.click(screen.getByRole('button', { name: 'Cancel' }));

    expect(api.batchFetchCandidates).not.toHaveBeenCalled();
  });

  // The three-state property. A row the server sent no age for is not a stale
  // row: `is_fresh` absent means "this result has no age", which is a different
  // claim from "this result is stale".
  it('never sweeps a row with no age into the refetch', async () => {
    const user = userEvent.setup();
    seed(
      [
        makeResult('a', { is_fresh: false }),
        makeResult('no-age'), // no is_fresh key at all
      ] as api.CandidateResult[],
      1
    );
    await openWorkspace();

    await user.click(screen.getByLabelText(/Refetch 1 stale book/i));
    await user.click(screen.getByTestId('refetch-stale-confirm'));

    await waitFor(() => expect(api.batchFetchCandidates).toHaveBeenCalled());
    expect(api.batchFetchCandidates).toHaveBeenCalledWith({ book_ids: ['a'] });
  });

  it('offers no refetch affordance when nothing is stale', async () => {
    seed([makeResult('a', { is_fresh: true })] as api.CandidateResult[], 0);
    await openWorkspace();

    expect(screen.queryByLabelText(/Refetch .* stale book/i)).not.toBeInTheDocument();
  });

  // The chip renders on the SERVER's stale count and the action runs on the
  // CLIENT's derived set. Those are two independent gates over two different
  // numbers, and an older server that counts staleness but sends no per-row
  // `is_fresh` makes them disagree. When they do, the chip must not offer an
  // action that would send an empty fetch.
  it('does not offer the action when the server counts stale rows but sends no ages', async () => {
    seed([makeResult('a'), makeResult('b')] as api.CandidateResult[], 2);
    await openWorkspace();

    // The chip still reports what the server said -- that is not this feature's
    // to contradict.
    expect(screen.getByText('2 stale')).toBeInTheDocument();
    // But there is nothing to act on, so there is no action.
    expect(screen.queryByLabelText(/Refetch .* stale book/i)).not.toBeInTheDocument();
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
