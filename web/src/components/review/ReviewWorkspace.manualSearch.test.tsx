// file: web/src/components/review/ReviewWorkspace.manualSearch.test.tsx
// version: 1.0.0
// guid: 8c04b7e2-5d13-49af-b026-3f7159ea840c
// last-edited: 2026-08-21
//
// The manual-search escape hatch on the metadata lane.
//
// Automatic fetching keys off a book's OWN tags, so it cannot rescue a book
// whose tags are the problem -- and this library has plenty: author fields
// holding a release-group tag ("[PZG]", 274 books), a studio ("Big Finish
// Productions", 426), or the book's own title ("The Way of Shadows", 310).
// Those rows sit at no_match permanently, because every automatic retry asks
// the same wrong question and gets the same answer.
//
// Until this was wired the only way to type a corrected query was a dialog on
// the Library page, which is why deleting that dialog in Phase 7 would have
// left the cache able to drain but never refill.

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

function seed(results: api.CandidateResult[]) {
  vi.mocked(api.getCachedReviewResults).mockResolvedValue({
    results,
    total_count: results.length,
    matched: results.filter((r) => r.status === 'matched').length,
    no_match: results.filter((r) => r.status === 'no_match').length,
    errors: 0,
    stale: 0,
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
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '' } as api.Config);
  vi.mocked(api.getBook).mockResolvedValue({
    id: 'nm',
    title: 'Book nm',
    author: '[PZG]',
  } as unknown as api.Book);
}

beforeEach(() => {
  vi.resetAllMocks();
  window.localStorage.clear();
});

async function openWorkspace() {
  render(
    <MemoryRouter initialEntries={['/review']}>
      <ToastProvider>
        <ReviewWorkspace />
      </ToastProvider>
    </MemoryRouter>
  );
  await waitFor(() => expect(screen.getByText(/Hide no-match/i)).toBeInTheDocument());
}

/**
 * Reveal the no_match rows.
 *
 * They are hidden by TWO defaults, not one. `hideNoMatch` is true, and a
 * no_match row is additionally seeded with rowState 'rejected', which
 * `hideRejected` (also true) filters out. So the rows that most need a manual
 * search are doubly hidden until a reviewer goes looking for them.
 *
 * That is the real workflow -- you clear these filters precisely to work the
 * unmatched pile -- so the tests walk the same path rather than asserting
 * against a view the reviewer never actually sees.
 */
async function showNoMatchRows(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByLabelText(/Hide no-match/i));
  await user.click(screen.getByLabelText(/Hide rejected/i));
}

describe('manual metadata search from /review', () => {
  it('offers the search only on rows the automatic fetch could not match', async () => {
    const user = userEvent.setup();
    seed([
      makeResult('ok'),
      makeResult('nm', { status: 'no_match', candidate: undefined }),
    ] as api.CandidateResult[]);
    await openWorkspace();
    await showNoMatchRows(user);

    expect(await screen.findByLabelText(/Search metadata for Book nm/i)).toBeInTheDocument();

    // A matched row already has a candidate awaiting a decision. Offering a
    // manual search there invites re-litigating a call the reviewer has not
    // made yet.
    expect(screen.queryByLabelText(/Search metadata for Book ok/i)).not.toBeInTheDocument();
  });

  it('loads the full book before opening the dialog', async () => {
    const user = userEvent.setup();
    seed([makeResult('nm', { status: 'no_match', candidate: undefined })] as api.CandidateResult[]);
    await openWorkspace();
    await showNoMatchRows(user);

    await user.click(await screen.findByLabelText(/Search metadata for Book nm/i));

    // The rail carries CandidateBookInfo, not the Book the dialog edits, so the
    // dialog must not render until the real record has arrived.
    await waitFor(() => expect(api.getBook).toHaveBeenCalledWith('nm'));
    expect(await screen.findByRole('dialog')).toBeInTheDocument();
  });

  it('surfaces a load failure instead of opening an empty dialog', async () => {
    const user = userEvent.setup();
    seed([makeResult('nm', { status: 'no_match', candidate: undefined })] as api.CandidateResult[]);
    vi.mocked(api.getBook).mockRejectedValue(new Error('book vanished'));
    await openWorkspace();
    await showNoMatchRows(user);

    await user.click(await screen.findByLabelText(/Search metadata for Book nm/i));

    expect(await screen.findByText(/book vanished/i)).toBeInTheDocument();
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
  });
});
