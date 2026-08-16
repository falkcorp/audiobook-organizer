// file: web/src/components/audiobooks/MetadataReviewDialog.test.tsx
// version: 1.0.0

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { screen, fireEvent, waitFor, within } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import type { CandidateResult } from '../../services/api';
import { MetadataReviewDialog } from './MetadataReviewDialog';

vi.mock('../../services/api', async () => {
  const actual = await vi.importActual<typeof import('../../services/api')>('../../services/api');
  return {
    ...actual,
    getCachedReviewResults: vi.fn(),
    batchApplyFromCache: vi.fn(),
    markNoMatch: vi.fn(),
    clearMetadataNoMatch: vi.fn(),
    pollOperationV2: vi.fn(),
  };
});

import * as api from '../../services/api';

const mockGet = vi.mocked(api.getCachedReviewResults);
const mockApply = vi.mocked(api.batchApplyFromCache);

// Two books sharing one ASIN — this is what renders as a grouped card with
// "Skip All" — plus one book with a candidate all to itself.
function makeResult(
  bookId: string,
  title: string,
  candidate: Partial<{ asin: string; title: string; source: string; score: number }>
): CandidateResult {
  return {
    book: { id: bookId, title, author: 'Some Author', file_path: `/lib/${bookId}.m4b` },
    status: 'matched',
    candidate: {
      title: candidate.title ?? title,
      author: 'Some Author',
      source: candidate.source ?? 'audible',
      score: candidate.score ?? 0.95,
      asin: candidate.asin,
    },
  };
}

const GROUPED_A = makeResult('dup-1', 'Long Book Part 1', { asin: 'B00SHARED', title: 'Long Book' });
const GROUPED_B = makeResult('dup-2', 'Long Book Part 2', { asin: 'B00SHARED', title: 'Long Book' });
const STANDALONE = makeResult('solo-1', 'A Normal Book', { asin: 'B00UNIQUE' });

const noop = () => {};
const toast = vi.fn();

function renderDialog() {
  return renderWithProviders(
    <MetadataReviewDialog open onClose={noop} onComplete={noop} toast={toast} />
  );
}

async function toggleHideMultiBook() {
  const label = await screen.findByText(/Hide Multi-Book Matches/);
  // The Switch is the input inside the same FormControlLabel as this text.
  const control = label.closest('label') as HTMLElement;
  fireEvent.click(within(control).getByRole('checkbox'));
}

function setResults(results: CandidateResult[]) {
  mockGet.mockResolvedValue({
    results,
    matched: results.length,
    no_match: 0,
    errors: 0,
    total: results.length,
    total_count: results.length,
  } as Awaited<ReturnType<typeof api.getCachedReviewResults>>);
}

beforeEach(() => {
  vi.clearAllMocks();
  setResults([GROUPED_A, GROUPED_B, STANDALONE]);
  mockApply.mockResolvedValue({ op_id: 'op-1' } as Awaited<
    ReturnType<typeof api.batchApplyFromCache>
  >);
});

describe('MetadataReviewDialog — Hide Multi-Book Matches', () => {
  it('shows books that share a candidate until the toggle is turned on', async () => {
    renderDialog();

    // Both halves of the shared-ASIN book are present by default.
    expect(await screen.findByText('Long Book Part 1')).toBeInTheDocument();
    expect(screen.getByText('Long Book Part 2')).toBeInTheDocument();
    expect(screen.getByText('A Normal Book')).toBeInTheDocument();

    await toggleHideMultiBook();

    await waitFor(() => {
      expect(screen.queryByText('Long Book Part 1')).not.toBeInTheDocument();
    });
    expect(screen.queryByText('Long Book Part 2')).not.toBeInTheDocument();
    // The unambiguous one-book-one-candidate row survives — that is the point.
    expect(screen.getByText('A Normal Book')).toBeInTheDocument();
  });

  // The wiring test. Asserting the list shrinks proves the filter; it does NOT
  // prove hidden books stay out of an apply, because selectedIds is a Set the
  // user builds by hand and a Set does not shrink when a filter hides its
  // members.
  //
  // Reaching that leak takes a specific arrangement, which is itself the reason
  // the hide is computed globally rather than per page. Grouped CARDS render
  // without per-row checkboxes, so a book cannot be selected while it is drawn
  // as part of a group. But rendering-grouping is per-page: put the two halves
  // of one book on different pages and each is a singleton on its own page, so
  // each draws as a standalone row WITH a checkbox — selectable, while still
  // globally part of a multi-book group. Page size floors at 25, so this needs
  // 26 rows: dup-1 first, 24 fillers, dup-2 alone on page 2.
  //
  // Without the deselect effect, dup-1 stays in selectedIds and gets applied.
  // The test above passes either way.
  it('does not apply a grouped book selected while it was alone on its page', async () => {
    const fillers = Array.from({ length: 24 }, (_, i) =>
      makeResult(`solo-${i}`, `Filler Book ${i}`, { asin: `B00FILL${i}` })
    );
    setResults([GROUPED_A, ...fillers, GROUPED_B]);

    renderDialog();
    // Page 1 shows dup-1 as an ordinary standalone row. A compact row renders
    // "<book title> → <candidate title>" inside ONE Typography, so an exact
    // string match cannot hit it — match a substring.
    const titleEl = await screen.findByText(/Long Book Part 1/);

    // Tick dup-1's own row checkbox. getAllByRole('checkbox') also matches the
    // filter Switches — MUI renders those as checkbox inputs — so scope to this
    // row rather than clicking everything on the page.
    const row = titleEl.closest('.MuiStack-root') as HTMLElement;
    fireEvent.click(within(row).getByRole('checkbox'));

    const applyButton = await screen.findByRole('button', { name: /Apply Selected \(1\)/i });
    expect(applyButton).toBeInTheDocument();

    await toggleHideMultiBook();

    // dup-1 is now hidden, so it must also be deselected — the count goes to 0
    // and the button disables rather than silently applying a hidden book.
    await waitFor(() => {
      expect(screen.queryByText(/Long Book Part 1/)).not.toBeInTheDocument();
    });
    const afterToggle = screen.getByRole('button', { name: /Apply Selected/i });
    expect(afterToggle).toBeDisabled();
    expect(afterToggle).toHaveTextContent('Apply Selected (0)');
    expect(mockApply).not.toHaveBeenCalled();
  });
});
