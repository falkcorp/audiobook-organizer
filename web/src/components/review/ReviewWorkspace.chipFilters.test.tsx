// file: web/src/components/review/ReviewWorkspace.chipFilters.test.tsx
// version: 1.1.0
// guid: 0d6c2e8a-94b1-4f37-8a5e-2c71b9e04f36
// last-edited: 2026-09-30
//
// Owner, 2026-09-27: "the 11324 with no candidates let me click on the chips
// at the left bar in the review page". Every summary chip filters the list to
// exactly the books it counts, the no-candidate books are listed and
// selectable, and "Search again" searches all the selected ones at once.
//
// The property asserted throughout is chip count == rows shown. The fixture
// deliberately includes rows the default filters hide (a low-score match, a
// no-match row) so a chip that only filtered the already-filtered list would
// come up short.

import { render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import userEvent from '@testing-library/user-event';
import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest';
import * as api from '../../services/api';
import { ReviewWorkspace } from './ReviewWorkspace';
import { ToastProvider } from '../toast/ToastProvider';

vi.mock('../../services/api');

function row(id: string, overrides: Partial<api.CandidateResult> = {}, score = 2.0) {
  return {
    book: { id, title: `Book ${id}`, author: 'Author', language: 'en' },
    status: 'matched',
    candidate: {
      source: 'audible',
      title: `Cand ${id}`,
      author: 'A',
      narrator: 'N',
      score,
      language: 'en',
    },
    candidate_hash: `h-${id}`,
    is_fresh: true,
    ...overrides,
  } as unknown as api.CandidateResult;
}

function emptyRow(
  id: string,
  status: api.UnreviewableStatus,
  overrides: Partial<api.CandidateResult> = {}
) {
  return {
    book: { id, title: `Book ${id}`, author: 'Author', file_path: `/books/${id}` },
    status,
    is_fresh: true,
    ...overrides,
  } as unknown as api.CandidateResult;
}

const reviewable = [
  row('m1', { is_fresh: false }),
  row('m2', {}, 0.5), // below every preset's confidence floor
  row('n1', { status: 'no_match' }), // hidden by Hide no-match
];
const unreviewable = [
  emptyRow('e1', 'no_candidates', { is_fresh: false }),
  emptyRow('e2', 'no_candidates'),
  emptyRow('r1', 'resolved_no_candidates', { review_status: 'no_match' }),
  emptyRow('d1', 'decode_error', { error_message: 'stored candidate will not decode' }),
];
const summary = {
  matched: 2,
  no_match: 1,
  errors: 1,
  stale: 2, // m1 + e1
  unreviewable: 6,
  unreviewable_by_cause: { orphaned: 3, no_candidates: 2, decode_errors: 1 },
  resolved_no_candidates: 1,
};

function seed() {
  vi.mocked(api.getCachedReviewResults).mockImplementation(
    async (_limit, _offset, _all, bucket) =>
      ({
        ...summary,
        results: bucket === 'unreviewable' ? unreviewable : reviewable,
        total_count: bucket === 'unreviewable' ? unreviewable.length : reviewable.length,
      }) as unknown as Awaited<ReturnType<typeof api.getCachedReviewResults>>
  );
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
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '' } as api.Config);
  vi.mocked(api.batchFetchCandidates).mockResolvedValue({
    operation_id: 'op-search',
    total_books: 2,
    message: 'metadata candidate fetch started',
  });
  vi.mocked(api.pollOperationV2).mockResolvedValue(
    undefined as unknown as Awaited<ReturnType<typeof api.pollOperationV2>>
  );
  vi.mocked(api.clearMetadataNoMatch).mockResolvedValue(
    undefined as unknown as Awaited<ReturnType<typeof api.clearMetadataNoMatch>>
  );
}

async function openWorkspace() {
  render(
    <MemoryRouter initialEntries={['/review']}>
      <ToastProvider>
        <ReviewWorkspace />
      </ToastProvider>
    </MemoryRouter>
  );
  await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());
}

/** Book ids in the rail's list, in order. */
function listedIds(): string[] {
  const list = screen.getByTestId('queue-list');
  return within(list)
    .queryAllByRole('checkbox')
    .map((cb) => (cb.getAttribute('aria-label') ?? '').replace(/^Select Book /, ''));
}

beforeEach(() => {
  vi.resetAllMocks();
  window.localStorage.clear();
  seed();
});

afterEach(() => {
  vi.restoreAllMocks();
});

describe('summary chips filter the list to exactly the books they count', () => {
  it.each([
    ['matched', 2, ['m1', 'm2']],
    ['no_match', 1, ['n1']],
    ['total', 3, ['m1', 'm2', 'n1']],
  ] as const)('%s chip shows its %i reviewable books', async (chip, count, ids) => {
    const user = userEvent.setup();
    await openWorkspace();

    const el = screen.getByTestId(`chip-${chip}`);
    await user.click(el);

    await waitFor(() => expect(listedIds()).toEqual(ids));
    expect(listedIds()).toHaveLength(count);
    expect(el).toHaveAttribute('aria-pressed', 'true');
    expect(screen.getByTestId('chip-filter-banner')).toHaveTextContent(`Showing the ${count}`);
    // The reviewable chips need nothing more from the server.
    expect(api.getCachedReviewResults).toHaveBeenCalledTimes(1);
  });

  it('clicking the active chip again, or Show all, clears it', async () => {
    const user = userEvent.setup();
    await openWorkspace();
    const defaultIds = listedIds();

    await user.click(screen.getByTestId('chip-total'));
    await waitFor(() => expect(listedIds()).toHaveLength(3));
    await user.click(screen.getByTestId('chip-total'));
    await waitFor(() => expect(listedIds()).toEqual(defaultIds));
    expect(screen.queryByTestId('chip-filter-banner')).not.toBeInTheDocument();

    await user.click(screen.getByTestId('chip-matched'));
    await waitFor(() => expect(listedIds()).toHaveLength(2));
    await user.click(screen.getByRole('button', { name: 'Show all' }));
    await waitFor(() => expect(listedIds()).toEqual(defaultIds));
  });

  it.each([
    ['no_candidates', ['e1', 'e2']],
    ['resolved_no_candidates', ['r1']],
    ['errors', ['d1']],
    ['stale', ['m1', 'e1']],
  ] as const)(
    '%s chip loads the unreviewable bucket and shows exactly its books',
    async (chip, ids) => {
      const user = userEvent.setup();
      await openWorkspace();

      await user.click(screen.getByTestId(`chip-${chip}`));

      await waitFor(() => expect(listedIds()).toEqual(ids));
      expect(api.getCachedReviewResults).toHaveBeenCalledWith(0, 0, true, 'unreviewable');
    }
  );

  it('the no-candidates chip count equals the rows it shows, each labelled', async () => {
    const user = userEvent.setup();
    await openWorkspace();

    const chip = screen.getByTestId('chip-no_candidates');
    expect(chip).toHaveTextContent('2 no candidates');
    await user.click(chip);

    await waitFor(() => expect(listedIds()).toHaveLength(2));
    const list = screen.getByTestId('queue-list');
    expect(within(list).getAllByText(/no candidate/)).toHaveLength(2);
  });

  it('orphaned rows stay a count: there is no book to show', async () => {
    await openWorkspace();
    const chip = screen.getByTestId('chip-orphaned');
    expect(chip).toHaveTextContent('3 orphaned');
    expect(chip).not.toHaveAttribute('aria-pressed');
    expect(screen.queryByRole('button', { name: /orphaned/i })).not.toBeInTheDocument();
  });
});

describe('Search again on the no-candidate books', () => {
  it('selects the listed books and searches all of them, forced, in one op', async () => {
    const user = userEvent.setup();
    await openWorkspace();
    await user.click(screen.getByTestId('chip-no_candidates'));
    await waitFor(() => expect(listedIds()).toHaveLength(2));

    await user.click(screen.getByLabelText('Select Book e1'));
    await user.click(screen.getByLabelText('Select Book e2'));

    const search = screen.getByTestId('search-selected');
    expect(search).toHaveTextContent('Search again (2)');
    // Nothing to apply: neither book has a candidate.
    expect(screen.getByTestId('apply-selected')).toBeDisabled();
    expect(screen.getByTestId('apply-selected')).toHaveTextContent('(0)');

    await user.click(search);

    await waitFor(() =>
      expect(api.batchFetchCandidates).toHaveBeenCalledWith({
        book_ids: ['e1', 'e2'],
        force: true,
      })
    );
    expect(api.clearMetadataNoMatch).not.toHaveBeenCalled();
    // The op is watched, and the lane reloads when it finishes.
    await waitFor(() => expect(api.pollOperationV2).toHaveBeenCalledWith('op-search'));
    await waitFor(() =>
      expect(
        vi.mocked(api.getCachedReviewResults).mock.calls.filter((c) => c[3] !== 'unreviewable')
          .length
      ).toBeGreaterThanOrEqual(2)
    );
  });

  it('asks before clearing a no-match mark, and clears it only on yes', async () => {
    const user = userEvent.setup();
    const confirm = vi.spyOn(window, 'confirm').mockReturnValue(false);
    await openWorkspace();
    await user.click(screen.getByTestId('chip-resolved_no_candidates'));
    await waitFor(() => expect(listedIds()).toEqual(['r1']));
    await user.click(screen.getByLabelText('Select Book r1'));

    await user.click(screen.getByTestId('search-selected'));
    expect(confirm).toHaveBeenCalledWith(expect.stringMatching(/marked no-match/));
    expect(api.clearMetadataNoMatch).not.toHaveBeenCalled();
    expect(api.batchFetchCandidates).not.toHaveBeenCalled();

    confirm.mockReturnValue(true);
    await user.click(screen.getByTestId('search-selected'));
    await waitFor(() => expect(api.clearMetadataNoMatch).toHaveBeenCalledWith('r1'));
    await waitFor(() =>
      expect(api.batchFetchCandidates).toHaveBeenCalledWith({ book_ids: ['r1'], force: true })
    );
  });

  it('Cancel still searches the books that are not marked no-match', async () => {
    const user = userEvent.setup();
    vi.spyOn(window, 'confirm').mockReturnValue(false);
    await openWorkspace();
    await user.click(screen.getByTestId('chip-total'));
    await waitFor(() => expect(listedIds()).toEqual(['m1', 'm2', 'n1']));
    await user.click(screen.getByLabelText('Select Book m1'));
    await user.click(screen.getByLabelText('Select Book n1')); // no_match == marked

    await user.click(screen.getByTestId('search-selected'));

    await waitFor(() =>
      expect(api.batchFetchCandidates).toHaveBeenCalledWith({ book_ids: ['m1'], force: true })
    );
    expect(api.clearMetadataNoMatch).not.toHaveBeenCalled();
  });

  it('selects every row on the page with one box', async () => {
    const user = userEvent.setup();
    await openWorkspace();
    await user.click(screen.getByTestId('chip-no_candidates'));
    await waitFor(() => expect(listedIds()).toHaveLength(2));

    await user.click(screen.getByTestId('select-page'));
    expect(screen.getByTestId('search-selected')).toHaveTextContent('Search again (2)');
    expect(screen.getByTestId('select-page')).toBeChecked();

    await user.click(screen.getByTestId('select-page'));
    expect(screen.getByTestId('search-selected')).toHaveTextContent('Search again (0)');
  });

  it('leaves Apply selected unchanged for ordinary rows', async () => {
    const user = userEvent.setup();
    await openWorkspace();
    await user.click(screen.getByLabelText('Select Book m1'));
    expect(screen.getByTestId('apply-selected')).toHaveTextContent('(1)');
    expect(screen.getByTestId('search-selected')).toHaveTextContent('Search again (1)');
  });
});

describe('review level slider in the rail', () => {
  it('starts at In-depth and moves by keyboard, persisting the level', async () => {
    const user = userEvent.setup();
    await openWorkspace();
    const slider = screen.getByRole('slider', { name: 'Review level' });
    expect(slider).toHaveAttribute('aria-valuetext', 'In-depth review');
    expect(screen.getByRole('switch', { name: 'Hide runtime differences' })).toBeChecked();
    expect(screen.getByRole('switch', { name: 'Has transcription' })).not.toBeChecked();

    slider.focus();
    await user.keyboard('{ArrowRight}');

    await waitFor(() => expect(slider).toHaveAttribute('aria-valuetext', 'Strict review'));
    expect(screen.getByRole('switch', { name: 'Has transcription' })).toBeChecked();
    expect(screen.getByRole('switch', { name: 'Transcription matched' })).toBeChecked();
    expect(window.localStorage.getItem('metadata-review-level')).toBe('strict');
  });
});

describe('runtime differences', () => {
  it('says how many rows the default level is hiding for runtime', async () => {
    const withRuntime = (id: string, book: number, cand: number) => {
      const r = row(id);
      return {
        ...r,
        book: { ...r.book, duration_seconds: book },
        // duration_delta_sec as the server computes it: |book - candidate|.
        candidate: {
          ...r.candidate,
          duration_sec: cand,
          duration_delta_sec: Math.abs(book - cand),
        },
      } as api.CandidateResult;
    };
    vi.mocked(api.getCachedReviewResults).mockResolvedValue({
      ...summary,
      results: [withRuntime('ok', 36000, 35700), withRuntime('off', 36000, 18000)],
      total_count: 2,
    } as unknown as Awaited<ReturnType<typeof api.getCachedReviewResults>>);
    await openWorkspace();

    await waitFor(() => expect(listedIds()).toEqual(['ok']));
    expect(screen.getByTestId('runtime-hidden-count')).toHaveTextContent(
      '1 hidden by runtime differences'
    );
  });
});
