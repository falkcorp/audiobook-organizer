// file: web/src/components/review/ReviewWorkspace.chipFilters.test.tsx
// version: 1.15.0
// guid: 0d6c2e8a-94b1-4f37-8a5e-2c71b9e04f36
// last-edited: 2026-10-19
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

import { act, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import userEvent from '@testing-library/user-event';
import { vi, describe, it, expect, beforeEach, afterEach } from 'vitest';
import * as api from '../../services/api';
import { serveReviewList } from '../../test/reviewServer';
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
  row('m1', { is_fresh: false, stale: true, fallback_deferred: true }),
  row('m2', {}, 0.5), // below every preset's confidence floor
  // Old, but the server does not flag it stale (owner-marked no match: the
  // fetch never searches it). The chip reads the server's flag, never is_fresh.
  row('n1', { status: 'no_match', is_fresh: false, stale: false }), // hidden by Hide no-match
];
const unreviewable = [
  emptyRow('e1', 'no_candidates', { is_fresh: false, stale: true }),
  emptyRow('e2', 'no_candidates', { fallback_deferred: true }),
  emptyRow('r1', 'resolved_no_candidates', {
    review_status: 'no_match',
    is_fresh: false,
    stale: false,
  }),
  emptyRow('d1', 'decode_error', { error_message: 'stored candidate will not decode' }),
  // Retitled after its candidates were fetched: no candidate is served.
  emptyRow('s1', 'identity_stale'),
];
const summary = {
  matched: 2,
  no_match: 1,
  errors: 1,
  stale: 2, // m1 + e1, the rows the server flags stale; n1 and r1 are old but not flagged
  unreviewable: 7,
  unreviewable_by_cause: { orphaned: 3, no_candidates: 2, decode_errors: 1, identity_stale: 1 },
  resolved_no_candidates: 1,
  deferred: 2, // m1 + e2, the rows whose fallback lookup was deferred
};

/** The books each chip lists: the server owns the filtering, so the fake answers by chip. */
const CHIP_IDS: Record<string, string[]> = {
  matched: ['m1', 'm2'],
  no_match: ['n1'],
  total: ['m1', 'm2', 'n1'],
  no_candidates: ['e1', 'e2'],
  resolved_no_candidates: ['r1'],
  errors: ['d1'],
  identity_stale: ['s1'],
  stale: ['m1', 'e1'],
  deferred: ['m1', 'e2'],
};
const DEFAULT_IDS = ['m1'];
const ALL_ROWS = [...reviewable, ...unreviewable];

function serveChips(extra: Record<string, unknown> = {}) {
  // The Title box (q) narrows whatever the chip lists, as the server does:
  // `/re/` is a regex over the title, anything else a substring.
  const titleMatches = (title: string, q?: string) => {
    if (!q) return true;
    const m = /^\/(.*)\/$/.exec(q);
    return m ? new RegExp(m[1]).test(title) : title.includes(q);
  };
  const rowsFor = (chip?: string, q?: string, bucket?: string) => {
    const ids = chip
      ? (CHIP_IDS[chip] ?? [])
      : bucket === 'unreviewable'
        ? unreviewable.map((r) => r.book.id)
        : bucket === 'any'
          ? ALL_ROWS.map((r) => r.book.id)
          : DEFAULT_IDS;
    return ids
      .map((id) => ALL_ROWS.find((r) => r.book.id === id)!)
      .filter((r) => titleMatches(r.book.title, q));
  };
  vi.mocked(api.getReviewPage).mockImplementation(async (params) => {
    const results = rowsFor(params?.chip, params?.q, params?.bucket);
    return {
      results,
      total_count: results.length,
      offset: 0,
      limit: 25,
      generation: 'g1',
      facets: {
        matched: summary.matched,
        no_match: summary.no_match,
        total: reviewable.length,
        errors: summary.errors,
        stale: summary.stale,
        deferred: summary.deferred,
        resolved_no_candidates: summary.resolved_no_candidates,
        unreviewable: summary.unreviewable,
        unreviewable_by_cause: summary.unreviewable_by_cause,
        sources: {},
        runtime_hidden: 0,
        ...extra,
      },
    } as unknown as api.ReviewPageResponse;
  });
  vi.mocked(api.getReviewMatchingIds).mockImplementation(async (params) => {
    const ids = rowsFor(params?.chip, params?.q, params?.bucket).map((r) => r.book.id);
    return { ids, total: ids.length, generation: 'g1' };
  });
  vi.mocked(api.getCachedReviewResults).mockImplementation(async (ids) => {
    const want = new Set(ids);
    const results = ALL_ROWS.filter((r) => want.has(r.book.id));
    return { results, total_count: results.length, matched: 0, no_match: 0, errors: 0 } as Awaited<
      ReturnType<typeof api.getCachedReviewResults>
    >;
  });
}

function seed() {
  serveChips();
  vi.mocked(api.getDedupCandidates).mockResolvedValue({ candidates: [], total: 0 });
  vi.mocked(api.getDedupStats).mockResolvedValue({ stats: [] });
  vi.mocked(api.getReviewItems).mockResolvedValue({
    items: [],
    count: 0,
    limit: 500,
    offset: 0,
    total: 0,
  });
  vi.mocked(api.getReviewCount).mockResolvedValue({
    applying: 0,
    needs_attention: 0,
    count: 0,
    by_kind: {},
  });
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

/**
 * Puts `text` in the Title filter in ONE input event. `user.type` sends a
 * keystroke per character and the workspace re-renders in full on each one
 * (the filter lives in the lane hook), so an 11-character string cost about
 * 11 full renders, roughly 4x a single paste, and that multiplies under CI
 * load. These tests are about what a filter value does, not about typing, and
 * paste also leaves `{`, `[` and `/` literal where `type` parses them.
 */
async function enterTitleFilter(user: ReturnType<typeof userEvent.setup>, text: string) {
  await user.click(screen.getByLabelText('Title filter'));
  await user.paste(text);
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
    // One page request for the chip, on top of the opening load.
    expect(api.getReviewPage).toHaveBeenLastCalledWith(expect.objectContaining({ chip }), expect.anything());
  });

  it('the chip banner says it is loading, not the previous list\'s total, while the next page loads', async () => {
    const user = userEvent.setup();
    await openWorkspace();
    await user.click(screen.getByTestId('chip-total'));
    await waitFor(() => expect(screen.getByTestId('chip-filter-banner')).toHaveTextContent('Showing the 3'));

    // The next chip's page is held in flight: until it lands, the lane still
    // holds the total chip's 3, which is not what the matched chip holds.
    const served = vi.mocked(api.getReviewPage).getMockImplementation()!;
    let land: () => void = () => {};
    const gate = new Promise<void>((r) => {
      land = r;
    });
    vi.mocked(api.getReviewPage).mockImplementation(async (params, signal) => {
      await gate;
      return served(params, signal);
    });
    await user.click(screen.getByTestId('chip-matched'));
    await waitFor(() =>
      expect(screen.getByTestId('chip-filter-banner')).toHaveTextContent(/^Loading the .*books/)
    );
    expect(screen.getByTestId('chip-filter-banner')).not.toHaveTextContent('Showing the');

    land();
    await waitFor(() => expect(screen.getByTestId('chip-filter-banner')).toHaveTextContent('Showing the 2'));
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
    ['identity_stale', ['s1']],
    ['stale', ['m1', 'e1']],
    ['deferred', ['m1', 'e2']],
  ] as const)(
    '%s chip asks the server for its books and shows exactly them',
    async (chip, ids) => {
      const user = userEvent.setup();
      await openWorkspace();

      await user.click(screen.getByTestId(`chip-${chip}`));

      await waitFor(() => expect(listedIds()).toEqual(ids));
      expect(api.getReviewPage).toHaveBeenCalledWith(expect.objectContaining({ chip }), expect.anything());
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

  it('the retitled chip counts the identity-stale books and labels each', async () => {
    const user = userEvent.setup();
    await openWorkspace();

    const chip = screen.getByTestId('chip-identity_stale');
    expect(chip).toHaveTextContent('1 retitled, awaiting refetch');
    await user.click(chip);

    await waitFor(() => expect(listedIds()).toEqual(['s1']));
    const list = screen.getByTestId('queue-list');
    expect(within(list).getByText(/retitled since its search/)).toBeInTheDocument();
  });

  it('the deferred chip counts the rows the server flags fallback_deferred', async () => {
    await openWorkspace();
    expect(screen.getByTestId('chip-deferred')).toHaveTextContent('2 deferred');
  });

  it('orphaned rows stay a count: there is no book to show', async () => {
    await openWorkspace();
    const chip = screen.getByTestId('chip-orphaned');
    expect(chip).toHaveTextContent('3 orphaned');
    expect(chip).not.toHaveAttribute('aria-pressed');
    expect(screen.queryByRole('button', { name: /orphaned/i })).not.toBeInTheDocument();
  });
});

describe('identification counts in the rail', () => {
  it('shows each ident_counts value as a link that opens the Library on its ident: search', async () => {
    const identCounts = {
      unasked: 7,
      asked_empty: 4,
      has_candidates: 2,
      applied: 1,
      held: 1,
      missing: 11,
      ready: true,
    };
    serveChips({ ident_counts: identCounts });
    await openWorkspace();
    const missing = await screen.findByTestId('ident-chip-missing');
    expect(missing).toHaveAttribute('href', '/library?search=ident%3Amissing');
    expect(missing).toHaveTextContent('11 missing metadata');
    expect(screen.getByTestId('ident-chip-held')).toHaveAttribute(
      'href',
      '/library?search=ident%3Aheld'
    );
  });

  it('shows nothing from an older server that sends no ident_counts', async () => {
    await openWorkspace();
    expect(screen.queryByTestId('ident-counts')).toBeNull();
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
      expect(vi.mocked(api.getReviewPage).mock.calls.length).toBeGreaterThanOrEqual(3)
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
      expect(api.batchFetchCandidates).toHaveBeenCalledWith({
        book_ids: ['r1'],
        force: true,
        interactive: true,
      })
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
      expect(api.batchFetchCandidates).toHaveBeenCalledWith({
        book_ids: ['m1'],
        force: true,
        interactive: true,
      })
    );
    expect(api.clearMetadataNoMatch).not.toHaveBeenCalled();
  });

  it('selects every row on the page with one button, and the same button deselects', async () => {
    const user = userEvent.setup();
    await openWorkspace();
    await user.click(screen.getByTestId('chip-no_candidates'));
    await waitFor(() => expect(listedIds()).toHaveLength(2));

    expect(screen.getByTestId('select-page')).toHaveTextContent('Select page (2)');
    await user.click(screen.getByTestId('select-page'));
    expect(screen.getByTestId('search-selected')).toHaveTextContent('Search again (2)');
    expect(screen.getByTestId('select-page')).toHaveAttribute('aria-pressed', 'true');
    expect(screen.getByTestId('select-page')).toHaveTextContent('Deselect page (2)');

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

    // focus() updates MUI Slider's focus-visible state; outside act() React
    // logs a "not wrapped in act" warning.
    act(() => slider.focus());
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
    serveReviewList({
      ...summary,
      // The server hid 'off' (36000s book, 18000s candidate) and counts it.
      results: [withRuntime('ok', 36000, 35700)],
      total_count: 1,
      runtime_hidden: 1,
    });
    await openWorkspace();

    await waitFor(() => expect(listedIds()).toEqual(['ok']));
    expect(screen.getByTestId('runtime-hidden-count')).toHaveTextContent(
      '1 hidden by runtime differences'
    );
  });
});

describe('selection bar: always on screen', () => {
  const VIEW_MODES = [
    ['Compact rows', 'compact'],
    ['Two columns', 'two-column'],
    ['Candidates', 'candidates'],
  ] as const;

  it.each(VIEW_MODES)('is above the spine in the %s view', async (label, mode) => {
    const user = userEvent.setup();
    await openWorkspace();
    await user.click(screen.getByRole('button', { name: label }));
    await waitFor(() =>
      expect(screen.getByTestId('compare-spine')).toHaveAttribute('data-view-mode', mode)
    );
    const bar = screen.getByTestId('main-selection-bar');
    expect(within(bar).getByTestId('main-select-page')).toBeInTheDocument();
    expect(within(bar).getByTestId('main-select-all-matching')).toBeInTheDocument();
    expect(within(bar).getByTestId('main-selection-clear')).toBeInTheDocument();
    expect(within(bar).getByTestId('main-selection-count')).toHaveTextContent('0 selected');
    // The rail's copy is there too.
    expect(screen.getByTestId('selection-bar')).toBeInTheDocument();
  });

  it('stays, disabled, in a chip view with zero rows', async () => {
    const user = userEvent.setup();
    await openWorkspace();
    await user.click(screen.getByTestId('chip-matched'));
    await enterTitleFilter(user, 'zzz-nothing');
    await waitFor(() => expect(listedIds()).toHaveLength(0));

    expect(screen.getByTestId('main-selection-bar')).toBeInTheDocument();
    expect(screen.getByTestId('main-select-page')).toBeDisabled();
    expect(screen.getByTestId('main-select-page')).toHaveTextContent('Select page (0)');
    expect(screen.getByTestId('main-select-all-matching')).toBeDisabled();
    expect(screen.getByTestId('main-select-all-matching')).toHaveTextContent(
      'Select all 0 matching'
    );
    expect(screen.getByTestId('main-selection-clear')).toBeDisabled();
    expect(screen.getByTestId('select-page')).toBeDisabled();
  });

  it('select page, select all matching and clear drive one selection', async () => {
    const user = userEvent.setup();
    await openWorkspace();
    await user.click(screen.getByTestId('chip-no_candidates'));
    await waitFor(() => expect(listedIds()).toHaveLength(2));

    await user.click(screen.getByTestId('main-select-page'));
    expect(screen.getByTestId('main-selection-count')).toHaveTextContent('2 selected');
    expect(screen.getByLabelText('Select Book e1')).toBeChecked();

    await user.click(screen.getByTestId('main-selection-clear'));
    expect(screen.getByTestId('main-selection-count')).toHaveTextContent('0 selected');
    expect(screen.getByLabelText('Select Book e1')).not.toBeChecked();

    await user.click(screen.getByTestId('main-select-all-matching'));
    expect(screen.getByTestId('main-selection-count')).toHaveTextContent(
      '2 selected (all 2 matching)'
    );
    expect(screen.getByTestId('main-select-all-matching')).toBeDisabled();
    // The rail copy reads the same selection.
    expect(screen.getByTestId('selection-count')).toHaveTextContent('2 selected');
  });
});

describe('chips combine with the title regex', () => {
  it('narrows the chip rows by the regex and says so; other filters stay paused', async () => {
    const user = userEvent.setup();
    await openWorkspace();
    await user.click(screen.getByTestId('chip-matched'));
    // m2 is below every preset's score floor: a paused filter, so it shows.
    await waitFor(() => expect(listedIds().sort()).toEqual(['m1', 'm2']));

    await enterTitleFilter(user, '/m2$/');
    await waitFor(() => expect(listedIds()).toEqual(['m2']));
    expect(screen.getByTestId('chip-filter-banner')).toHaveTextContent(
      'Showing the 1 matched books matching title /m2$/. Other filters are paused.'
    );
  });

  it('keeps the plain banner text when no title filter is set', async () => {
    const user = userEvent.setup();
    await openWorkspace();
    await user.click(screen.getByTestId('chip-matched'));
    await waitFor(() => expect(listedIds()).toHaveLength(2));
    expect(screen.getByTestId('chip-filter-banner')).toHaveTextContent(
      'Showing the 2 matched books. Other filters are paused.'
    );
  });

  it('drops a selected book whose title the new regex excludes', async () => {
    const user = userEvent.setup();
    await openWorkspace();
    await user.click(screen.getByTestId('chip-matched'));
    await waitFor(() => expect(listedIds()).toHaveLength(2));
    await user.click(screen.getByTestId('main-select-page'));
    expect(screen.getByTestId('main-selection-count')).toHaveTextContent('2 selected');

    await enterTitleFilter(user, '/m1$/');
    await waitFor(() =>
      expect(screen.getByTestId('main-selection-count')).toHaveTextContent('1 selected')
    );
    expect(screen.getByLabelText('Select Book m1')).toBeChecked();
  });

  it('shows an invalid regex under the field instead of ignoring it', async () => {
    const user = userEvent.setup();
    await openWorkspace();
    await user.click(screen.getByTestId('chip-matched'));
    await waitFor(() => expect(listedIds()).toHaveLength(2));
    await enterTitleFilter(user, '/Book (/');
    expect(await screen.findByText(/invalid regex/)).toBeInTheDocument();
    expect(screen.getByLabelText('Title filter')).toHaveAttribute('aria-invalid', 'true');
    // Nothing is filtered by a pattern that does not compile.
    expect(listedIds()).toHaveLength(2);
    expect(screen.getByTestId('chip-filter-banner')).toHaveTextContent(
      'Showing the 2 matched books. Other filters are paused.'
    );
  });
});

describe('match score threshold', () => {
  it('is a unitless match score, not a confidence percentage', async () => {
    await openWorkspace();
    const label = screen.getByTestId('match-score-label');
    expect(label).toHaveTextContent(/^Min match score: \d+$/);
    expect(label.textContent).not.toContain('%');
    expect(screen.queryByText(/Min confidence/)).not.toBeInTheDocument();
    expect(screen.getByRole('slider', { name: 'Minimum match score' })).toBeInTheDocument();
    // The row chips use the same unitless number (score 2.0 -> 200).
    expect(within(screen.getByTestId('queue-list')).getAllByText('200').length).toBeGreaterThan(0);
  });
});
