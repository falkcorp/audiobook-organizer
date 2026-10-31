// file: web/src/components/review/AuthorsPanel.test.tsx
// version: 1.3.1
// guid: e3b70c58-1d92-4a6f-8c45-7f20b9d6a184
// last-edited: 2026-10-30

/**
 * The Authors sub-view as a reviewer meets it: the hook and the panel together,
 * with the reads and the writes mocked at their module boundaries so a test
 * decides when each answer lands.
 *
 * Ported from DedupAuthorTab.test.tsx (the books popover) and the Author part of
 * dedupTabs.selectAll.test.tsx, then extended for what the lane adds: the four
 * states, the kind chips, the candidates section, and the verbs on a group card.
 *
 * Fixtures are synthetic; no real library rows.
 */

import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../services/api';
import type { Book } from '../../services/api';
import { AuthorsPanel } from './AuthorsPanel';
import { useAuthorsLane } from './lanes/useAuthorsLane';
import { useAIScans } from './lanes/useAIScans';
import * as reads from './lanes/authorsReads';

vi.mock('../../services/api');
vi.mock('./lanes/authorsReads');

const toast = vi.fn();

function Harness({ active = true, view = null }: { active?: boolean; view?: string | null }) {
  const authors = useAuthorsLane(toast, active, { authorId: null, view });
  // The workspace owns this hook; the panel only renders it. Inactive here, so
  // these tests (which never open AI scans) make no AI scan reads.
  const aiScans = useAIScans(toast, active && authors.view === 'ai');
  return <AuthorsPanel authors={authors} aiScans={aiScans} />;
}

function renderLane(active = true, view: string | null = null) {
  return render(
    <MemoryRouter initialEntries={['/review']}>
      <Routes>
        <Route path="/review" element={<Harness active={active} view={view} />} />
        <Route path="/library/:id" element={<div data-testid="book-detail">detail</div>} />
      </Routes>
    </MemoryRouter>
  );
}

function wire(
  id: number,
  name: string,
  variants: string[] = [],
  extra: Partial<api.AuthorDedupGroup> = {}
): api.AuthorDedupGroup {
  return {
    canonical: { id, name, created_at: '' },
    variants: variants.map((v, i) => ({ id: id * 100 + i + 1, name: v, created_at: '' })),
    book_count: 3,
    ...extra,
  };
}

function serve(groups: api.AuthorDedupGroup[], needsRefresh = false) {
  vi.mocked(reads.fetchAuthorGroups).mockResolvedValue({ groups, needsRefresh });
}

function book(id: string): Book {
  return {
    id,
    title: `Populated Title ${id}`,
    author_name: 'Author A',
    file_path: `/library/${id}.m4b`,
    created_at: '',
    updated_at: '',
  };
}

const op = (id: string) => ({ id, type: 'x', status: 'queued' }) as api.Operation;
const done = (status = 'completed', error_message?: string) =>
  ({ id: 'op', status, error_message: error_message ?? null }) as unknown as api.OperationV2;

beforeEach(() => {
  vi.resetAllMocks();
  toast.mockReset();
  vi.mocked(api.getDedupCandidates).mockResolvedValue({ candidates: [], total: 0 });
  vi.mocked(api.mergeAuthors).mockImplementation(async (id: number) => op(`m${id}`));
  vi.mocked(api.pollOperationV2).mockResolvedValue(done());
  vi.mocked(api.reclassifyAuthorAsNarrator).mockImplementation(async (id: number) => op(`r${id}`));
  vi.mocked(reads.fetchAuthorName).mockResolvedValue('');
  vi.mocked(api.mergeSeriesGroup).mockImplementation(async (keep: number) => op(`s${keep}`));
  vi.mocked(reads.fetchSeriesGroups).mockResolvedValue({ groups: [], totalSeries: 0, needsRefresh: false });
});

describe('books popover', () => {
  async function openBooks(user: ReturnType<typeof userEvent.setup>) {
    await user.click(await screen.findByText('3 credit(s)'));
  }

  it('shows a distinct fetch-error state, not an empty list, when the books read rejects', async () => {
    const user = userEvent.setup();
    serve([wire(101, 'Author A')]);
    vi.mocked(reads.fetchBooksByAuthor).mockRejectedValue(new Error('boom: 500'));
    renderLane();
    await openBooks(user);

    // The defect this guards: a rejected fetch used to render "No books found",
    // the same text a genuinely empty author gets, biasing a merge decision.
    expect(await screen.findByText(/could not load/i)).toBeInTheDocument();
    expect(screen.queryByText('No books found')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument();
  });

  it('Retry re-fetches and clears the error state on success', async () => {
    const user = userEvent.setup();
    serve([wire(101, 'Author A')]);
    vi.mocked(reads.fetchBooksByAuthor)
      .mockRejectedValueOnce(new Error('boom: 500'))
      .mockResolvedValue([book('b1')]);
    renderLane();
    await openBooks(user);
    await screen.findByText(/could not load/i);

    await user.click(screen.getByRole('button', { name: /retry/i }));

    expect(await screen.findByText('Populated Title b1')).toBeInTheDocument();
    expect(screen.queryByText(/could not load/i)).not.toBeInTheDocument();
    expect(reads.fetchBooksByAuthor).toHaveBeenCalledTimes(2);
  });

  it("lists the author's books on a successful read, across the canonical and its variants", async () => {
    const user = userEvent.setup();
    serve([wire(101, 'Author A', ['Author A.'])]);
    vi.mocked(reads.fetchBooksByAuthor).mockImplementation(async (id) =>
      id === 101 ? [book('b1')] : [book('b1'), book('b2')]
    );
    renderLane();
    await openBooks(user);

    expect(await screen.findByText('Populated Title b1')).toBeInTheDocument();
    expect(screen.getByText('Populated Title b2')).toBeInTheDocument();
    // b1 belongs to both authors and is listed once.
    expect(screen.getAllByText('Populated Title b1')).toHaveLength(1);
    expect(screen.queryByText(/could not load/i)).not.toBeInTheDocument();
  });

  it('says "No books found" only when every read succeeded and returned none', async () => {
    const user = userEvent.setup();
    serve([wire(101, 'Author A')]);
    vi.mocked(reads.fetchBooksByAuthor).mockResolvedValue([]);
    renderLane();
    await openBooks(user);
    expect(await screen.findByText('No books found')).toBeInTheDocument();
  });

  it('a book in the popover opens its detail page', async () => {
    const user = userEvent.setup();
    serve([wire(101, 'Author A')]);
    vi.mocked(reads.fetchBooksByAuthor).mockResolvedValue([book('b1')]);
    renderLane();
    await openBooks(user);
    await user.click(await screen.findByText('Populated Title b1'));
    expect(await screen.findByTestId('book-detail')).toBeInTheDocument();
  });

  it('opens from the keyboard (Enter on the focused group)', async () => {
    const user = userEvent.setup();
    serve([wire(101, 'Author A')]);
    vi.mocked(reads.fetchBooksByAuthor).mockResolvedValue([book('b1')]);
    renderLane();
    await screen.findByText('3 credit(s)');
    await user.keyboard('{Enter}');
    expect(await screen.findByText('Populated Title b1')).toBeInTheDocument();
  });
});

describe('the four states, and the one a cold cache needs', () => {
  it('loading', async () => {
    vi.mocked(reads.fetchAuthorGroups).mockReturnValue(new Promise(() => {}));
    renderLane();
    expect(await screen.findByTestId('authors-loading')).toBeInTheDocument();
    expect(screen.queryByTestId('authors-empty')).not.toBeInTheDocument();
  });

  it('error names the failure, offers Retry, and is not the empty state', async () => {
    const user = userEvent.setup();
    vi.mocked(reads.fetchAuthorGroups).mockRejectedValueOnce(new Error('Internal server error'));
    serve([wire(101, 'Author A', ['Author A.'])]);
    renderLane();
    const alert = await screen.findByTestId('authors-error');
    expect(alert).toHaveTextContent('Internal server error');
    expect(screen.queryByTestId('authors-empty')).not.toBeInTheDocument();
    await user.click(within(alert).getByRole('button', { name: 'Retry' }));
    expect(await screen.findByRole('heading', { name: 'Author A' })).toBeInTheDocument();
    expect(screen.queryByTestId('authors-error')).not.toBeInTheDocument();
  });

  it('empty is said as such', async () => {
    serve([]);
    renderLane();
    expect(await screen.findByTestId('authors-empty')).toHaveTextContent('No duplicate authors found');
    expect(screen.queryByTestId('authors-error')).not.toBeInTheDocument();
    expect(screen.queryByTestId('authors-needs-scan')).not.toBeInTheDocument();
  });

  it('a cold cache is "not scanned yet" with a way to scan, not "no duplicates"', async () => {
    const user = userEvent.setup();
    serve([], true);
    vi.mocked(api.refreshAuthorDuplicates).mockResolvedValue(op('scan1'));
    renderLane();
    const panel = await screen.findByTestId('authors-needs-scan');
    expect(screen.queryByTestId('authors-empty')).not.toBeInTheDocument();
    await user.click(within(panel).getByRole('button', { name: /scan for duplicate authors/i }));
    await waitFor(() => expect(api.refreshAuthorDuplicates).toHaveBeenCalledTimes(1));
  });

  it('populated shows one card per group', async () => {
    serve([wire(1, 'Author A', ['Author A.']), wire(2, 'Author B', ['Author B.'])]);
    renderLane();
    expect(await screen.findByRole('heading', { name: 'Author A' })).toBeInTheDocument();
    expect(screen.getByRole('heading', { name: 'Author B' })).toBeInTheDocument();
    expect(screen.getAllByTestId(/^author-group-\d+$/)).toHaveLength(2);
  });

  it('makes no request while the lane is not the visible one', async () => {
    serve([wire(1, 'Author A', ['Author A.'])]);
    renderLane(false);
    await Promise.resolve();
    expect(reads.fetchAuthorGroups).not.toHaveBeenCalled();
    expect(api.getDedupCandidates).not.toHaveBeenCalled();
  });
});

describe('the sub-view control and the rail', () => {
  it('shows the Authors sub-view, selected', async () => {
    serve([wire(1, 'Author A', ['Author A.'])]);
    renderLane();
    const authorsView = await screen.findByTestId('authors-subview-authors');
    expect(authorsView).toHaveTextContent('Authors');
    expect(authorsView).toHaveAttribute('aria-pressed', 'true');
  });

  it('the Refresh icon starts the author scan and reloads the groups when it finishes', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author A', ['Author A.'])]);
    vi.mocked(api.refreshAuthorDuplicates).mockResolvedValue(op('scan1'));
    renderLane();
    await screen.findByRole('heading', { name: 'Author A' });
    await user.click(screen.getByRole('button', { name: 'Refresh' }));
    await waitFor(() => expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(2));
    expect(api.refreshAuthorDuplicates).toHaveBeenCalledTimes(1);
    expect(toast).toHaveBeenCalledWith('Author scan started — watch the bell for progress.', 'info');
  });

  it('every kind chip counts exactly the cards it shows, and the counts add up', async () => {
    const user = userEvent.setup();
    serve([
      wire(1, 'Author A', ['Author A.']),
      wire(2, 'Author B', ['Author B.']),
      wire(3, 'Author C / Author D', [], { split_names: ['Author C', 'Author D'] }),
      wire(4, 'Studio One', [], { is_production_company: true }),
    ]);
    renderLane();
    await screen.findByRole('heading', { name: 'Author A' });
    const counts = async (label: string) => {
      const chip = screen.getByTestId(label);
      return Number(/\((\d+)\)/.exec(chip.textContent ?? '')?.[1]);
    };
    const all = await counts('authors-kind-all');
    const variants = await counts('authors-kind-variants');
    const composite = await counts('authors-kind-composite');
    const production = await counts('authors-kind-production');
    expect([all, variants, composite, production]).toEqual([4, 2, 1, 1]);
    expect(variants + composite + production).toBe(all);

    for (const [id, n] of [
      ['variants', variants],
      ['composite', composite],
      ['production', production],
      ['all', all],
    ] as const) {
      await user.click(screen.getByTestId(`authors-kind-${id}`));
      await waitFor(() => expect(screen.getAllByTestId(/^author-group-\d+$/)).toHaveLength(n));
    }
  });
});

describe('a group card', () => {
  it('shows the pencil, the suggested-name chip, and the variants with role toggle and remove', async () => {
    serve([wire(1, 'Author A', ['Author A.', 'A. Author'], { suggested_name: 'Author A Suggested' })]);
    renderLane();
    expect(await screen.findByRole('button', { name: 'Edit canonical name' })).toBeInTheDocument();
    expect(screen.getByText('Author A Suggested')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Remove "Author A." from this merge' })).toBeInTheDocument();
    expect(screen.getAllByText('Author')).toHaveLength(2); // one role toggle per variant
    expect(screen.getByText('3 credit(s)')).toBeInTheDocument();
  });

  it('the pencil opens an editor with the canonical name; Enter renames', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author A', ['Author A.'])]);
    vi.mocked(api.renameAuthor).mockResolvedValue({ id: 1, name: 'Author A Edited' });
    renderLane();
    await user.click(await screen.findByRole('button', { name: 'Edit canonical name' }));
    const input = screen.getByRole('textbox', { name: 'Canonical name' });
    expect(input).toHaveValue('Author A');
    await user.clear(input);
    await user.type(input, 'Author A Edited{Enter}');
    await waitFor(() => expect(api.renameAuthor).toHaveBeenCalledWith(1, 'Author A Edited'));
    expect(await screen.findByRole('heading', { name: 'Author A Edited' })).toBeInTheDocument();
  });

  it('the suggested chip starts an edit from the suggestion', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author A', ['Author A.'], { suggested_name: 'Author A Suggested' })]);
    renderLane();
    await user.click(await screen.findByText('Author A Suggested'));
    expect(screen.getByRole('textbox', { name: 'Canonical name' })).toHaveValue('Author A Suggested');
  });

  it('a failed rename keeps the editor open and says why', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author A', ['Author A.'])]);
    vi.mocked(api.renameAuthor).mockRejectedValue(new Error('name already taken'));
    renderLane();
    await user.click(await screen.findByRole('button', { name: 'Edit canonical name' }));
    await user.type(screen.getByRole('textbox', { name: 'Canonical name' }), 'x{Enter}');
    await waitFor(() => expect(toast).toHaveBeenCalledWith('name already taken', 'error'));
    expect(screen.getByRole('textbox', { name: 'Canonical name' })).toBeInTheDocument();
  });

  it('toggling a variant to Narrator reclassifies it before the merge and merges the rest', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author A', ['Author A.', 'A. Author'])]);
    renderLane();
    const variant = await screen.findByTestId('author-variant-101');
    await user.click(within(variant).getByText('Author'));
    expect(within(variant).getByText('Narrator')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Merge into "Author A"' }));
    await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledWith(1, [102]));
    expect(api.reclassifyAuthorAsNarrator).toHaveBeenCalledWith(101);
    expect(vi.mocked(api.reclassifyAuthorAsNarrator).mock.invocationCallOrder[0]).toBeLessThan(
      vi.mocked(api.mergeAuthors).mock.invocationCallOrder[0]
    );
    await waitFor(() => expect(screen.queryByTestId('author-group-1')).not.toBeInTheDocument());
  });

  it('Remove excludes a variant from the merge, and the button then offers to put it back', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author A', ['Author A.', 'A. Author'])]);
    renderLane();
    await user.click(await screen.findByRole('button', { name: 'Remove "Author A." from this merge' }));
    expect(screen.getByText(/1 variant\(s\) will be merged into it \(1 left out\)/)).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Put "Author A." back in this merge' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Merge into "Author A"' }));
    await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledWith(1, [102]));
  });

  it('clicking a variant uses it as the canonical spelling', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author A', ['Author A.'])]);
    vi.mocked(api.renameAuthor).mockResolvedValue({ id: 1, name: 'Author A.' });
    renderLane();
    await user.click(await screen.findByText('Author A.'));
    await waitFor(() => expect(api.renameAuthor).toHaveBeenCalledWith(1, 'Author A.'));
  });

  it('Validate shows what the providers returned, and says so when there is nothing', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author A', ['Author A.']), wire(2, 'Author B', ['Author B.'])]);
    vi.mocked(reads.validateAuthorName).mockImplementation(async (q) =>
      q === 'Author A'
        ? { query: q, results: [{ source: 'Provider One', title: 'Book One', author: 'Author A' }] }
        : { query: q, results: [] }
    );
    renderLane();
    const cardA = (await screen.findByTestId('author-group-1')) as HTMLElement;
    await user.click(within(cardA).getByRole('button', { name: 'Validate' }));
    expect(await within(cardA).findByText('Provider One: Author A')).toBeInTheDocument();
    const cardB = screen.getByTestId('author-group-2');
    await user.click(within(cardB).getByRole('button', { name: 'Validate' }));
    expect(await within(cardB).findByTestId('author-validation-empty')).toBeInTheDocument();
  });

  it('evidence for a name is hidden once the group is renamed, rather than shown under the new name', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author A', ['Author A.'])]);
    vi.mocked(reads.validateAuthorName).mockResolvedValue({
      query: 'Author A',
      results: [{ source: 'Provider One', title: 'Book One', author: 'Author A' }],
    });
    vi.mocked(api.renameAuthor).mockResolvedValue({ id: 1, name: 'Author A Edited' });
    renderLane();
    await user.click(await screen.findByRole('button', { name: 'Validate' }));
    expect(await screen.findByText('Provider One: Author A')).toBeInTheDocument();

    await user.click(screen.getByRole('button', { name: 'Edit canonical name' }));
    await user.type(screen.getByRole('textbox', { name: 'Canonical name' }), ' Edited{Enter}');
    await screen.findByRole('heading', { name: 'Author A Edited' });
    expect(screen.queryByText('Provider One: Author A')).not.toBeInTheDocument();
  });

  it('a failed Validate is an error line, not "No external matches"', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author A', ['Author A.'])]);
    vi.mocked(reads.validateAuthorName).mockRejectedValue(new Error('providers down'));
    renderLane();
    await user.click(await screen.findByRole('button', { name: 'Validate' }));
    expect(await screen.findByTestId('author-validation-error')).toHaveTextContent('providers down');
    expect(screen.queryByTestId('author-validation-empty')).not.toBeInTheDocument();
  });

  it('a production company shows its chip and Find Real Author, which resolves and drops the group without re-reading', async () => {
    const user = userEvent.setup();
    serve([wire(4, 'Studio One', [], { is_production_company: true })]);
    vi.mocked(api.resolveProductionAuthor).mockResolvedValue(op('r1'));
    renderLane();
    expect(await screen.findByText('Production Company')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /find real author/i }));
    await waitFor(() => expect(api.resolveProductionAuthor).toHaveBeenCalledWith(4));
    await waitFor(() => expect(screen.queryByTestId('author-group-4')).not.toBeInTheDocument());
    // Every author write clears the server's list; re-reading it would return
    // "not scanned yet" and replace the other groups.
    expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(1);
  });

  it('a composite shows its names and "Split into N authors"', async () => {
    const user = userEvent.setup();
    serve([wire(3, 'Author C / Author D', [], { split_names: ['Author C', 'Author D'] })]);
    vi.mocked(api.splitCompositeAuthor).mockResolvedValue({
      authors: [
        { id: 31, name: 'Author C' },
        { id: 32, name: 'Author D' },
      ],
      books_updated: 2,
    });
    renderLane();
    expect(await screen.findByText('Author C')).toBeInTheDocument();
    expect(screen.getByText('Author D')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Split into 2 authors' }));
    await waitFor(() => expect(api.splitCompositeAuthor).toHaveBeenCalledWith(3));
  });
});

describe('a group with a write in flight', () => {
  function hangingPoll() {
    const gate = { release: () => {} };
    vi.mocked(api.pollOperationV2).mockImplementation(
      () =>
        new Promise((resolve) => {
          gate.release = () => resolve(done());
        })
    );
    return gate;
  }

  it('locks its own controls and cannot be merged twice, while other groups stay usable', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author 1', ['Author 1.']), wire(2, 'Author 2', ['Author 2.'])]);
    const gate = hangingPoll();
    renderLane();
    const card1 = await screen.findByTestId('author-group-1');
    const merge1 = within(card1).getByRole('button', { name: 'Merge into "Author 1"' });
    await user.click(merge1);
    await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledTimes(1));

    expect(merge1).toBeDisabled();
    expect(within(card1).getByRole('button', { name: 'Edit canonical name' })).toBeDisabled();
    expect(within(card1).getByRole('checkbox')).toBeDisabled();
    expect(card1).toHaveAttribute('aria-busy', 'true');
    const card2 = screen.getByTestId('author-group-2');
    expect(within(card2).getByRole('button', { name: 'Merge into "Author 2"' })).toBeEnabled();
    // Bulk verbs wait for the write.
    expect(screen.getByRole('button', { name: /^Merge all/ })).toBeDisabled();

    gate.release();
    await waitFor(() => expect(screen.queryByTestId('author-group-1')).not.toBeInTheDocument());
    expect(api.mergeAuthors).toHaveBeenCalledTimes(1);
  });

  it('freezes every card while the author scan runs', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author 1', ['Author 1.'])]);
    vi.mocked(api.refreshAuthorDuplicates).mockResolvedValue(op('scan1'));
    hangingPoll();
    renderLane();
    await user.click(await screen.findByRole('button', { name: 'Refresh' }));
    const card = await screen.findByTestId('author-group-1');
    await waitFor(() =>
      expect(within(card).getByRole('button', { name: 'Merge into "Author 1"' })).toBeDisabled()
    );
    expect(screen.getByTestId('authors-scanning')).toBeInTheDocument();
  });
});

describe('selection and the bulk verbs', () => {
  function thirty(): api.AuthorDedupGroup[] {
    return Array.from({ length: 30 }, (_, i) => wire(i + 1, `Author ${i + 1}`, [`Author ${i + 1}.`]));
  }

  it('select page -> all 30 -> confirmed merge of every group', async () => {
    const user = userEvent.setup();
    serve(thirty());
    renderLane();
    await user.click(await screen.findByRole('checkbox', { name: 'Select all 25 groups on this page' }));
    await user.click(screen.getByTestId('author-groups-select-all-matching'));
    expect(screen.getByTestId('author-groups-select-all-banner')).toHaveTextContent(
      'All 30 groups matching this filter are selected.'
    );
    await user.click(screen.getByRole('button', { name: 'Merge Selected (30)' }));
    const dialog = await screen.findByTestId('author-merge-selected-confirm');
    expect(api.mergeAuthors).not.toHaveBeenCalled();
    await user.click(within(dialog).getByTestId('author-merge-selected-confirm-btn'));
    await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledTimes(30));
    await waitFor(() => expect(screen.queryByTestId('authors-bulk-progress')).not.toBeInTheDocument());
  });

  it('a selection within the page merges without asking, and reports a partial failure', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author 1', ['Author 1.']), wire(2, 'Author 2', ['Author 2.'])]);
    vi.mocked(api.pollOperationV2).mockImplementation(async (id: string) =>
      id === 'm2' ? done('failed', 'store write failed') : done()
    );
    renderLane();
    await user.click(await screen.findByRole('checkbox', { name: 'Select all 2 groups on this page' }));
    await user.click(screen.getByRole('button', { name: 'Merge Selected (2)' }));
    await waitFor(() =>
      expect(toast).toHaveBeenCalledWith(
        'Merged 1 of 2 selected group(s); 1 failed: "Author 2": store write failed.',
        'error'
      )
    );
    // The count follows the rows: the merged group left the list and the tick with it.
    expect(screen.queryByTestId('author-group-1')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Merge Selected (1)' })).toBeInTheDocument();
  });

  it('production companies and composites are not selectable, so the count is what a merge would do', async () => {
    serve([
      wire(1, 'Author 1', ['Author 1.']),
      wire(3, 'Author C / Author D', [], { split_names: ['Author C', 'Author D'] }),
      wire(4, 'Studio One', [], { is_production_company: true }),
    ]);
    renderLane();
    expect(await screen.findByRole('button', { name: 'Merge all 1 name-variant group' })).toBeInTheDocument();
    expect(screen.getByRole('checkbox', { name: 'Select group Studio One' })).toBeDisabled();
    expect(screen.getByRole('checkbox', { name: 'Select group Author 1' })).toBeEnabled();
  });

  it('merge all asks for confirmation, and nothing is merged on Cancel', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author 1', ['Author 1.']), wire(2, 'Author 2', ['Author 2.'])]);
    renderLane();
    await user.click(await screen.findByRole('button', { name: 'Merge all 2 name-variant groups' }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByRole('heading', { name: 'Confirm Merge All' })).toBeInTheDocument();
    expect(dialog).toHaveTextContent('This will merge 2 name-variant groups');
    expect(within(dialog).getByTestId('authors-merge-all-warning')).toHaveTextContent('leaving it stops the remaining merges');
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(api.mergeAuthors).not.toHaveBeenCalled();

    await user.click(screen.getByRole('button', { name: 'Merge all 2 name-variant groups' }));
    await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Confirm' }));
    await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledTimes(2));
  });

  it('pages the groups client-side and says how many per page', async () => {
    const user = userEvent.setup();
    serve(thirty());
    renderLane();
    expect(await screen.findByRole('heading', { name: 'Author 1', level: 3 })).toBeInTheDocument();
    expect(screen.getAllByTestId(/^author-group-\d+$/)).toHaveLength(25);
    await user.click(screen.getByRole('button', { name: /next page/i }));
    await waitFor(() => expect(screen.getAllByTestId(/^author-group-\d+$/)).toHaveLength(5));
  });
});

describe('author candidates', () => {
  const pair = (id: number, a: string, b: string): api.DedupCandidate => ({
    id,
    entity_type: 'author',
    entity_a_id: a,
    entity_b_id: b,
    layer: 'embedding',
    similarity: 0.91,
    status: 'pending',
    created_at: '',
    updated_at: '',
  });

  it('the count opens exactly the pairs it counts, named from the groups', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author A', ['Author A.']), wire(2, 'Author B', ['Author B.'])]);
    vi.mocked(api.getDedupCandidates).mockResolvedValue({
      total: 2,
      candidates: [pair(7, '1', '2'), pair(8, '101', '2')],
    });
    renderLane();
    const toggle = await screen.findByRole('button', { name: 'Author candidates (2)' });
    await user.click(toggle);
    const rows = await screen.findAllByTestId(/^author-candidate-\d+$/);
    expect(rows).toHaveLength(2);
    expect(rows[0]).toHaveTextContent('Author A ↔ Author B');
    expect(rows[1]).toHaveTextContent('Author A. ↔ Author B');
    expect(rows[0]).toHaveTextContent('91% similar');
  });

  it('Show group focuses the group a candidate names, whichever page it is on', async () => {
    const user = userEvent.setup();
    const groups = Array.from({ length: 30 }, (_, i) => wire(i + 1, `Author ${i + 1}`, [`Author ${i + 1}.`]));
    serve(groups);
    vi.mocked(api.getDedupCandidates).mockResolvedValue({ total: 1, candidates: [pair(7, '28', '999')] });
    vi.mocked(reads.fetchAuthorName).mockResolvedValue('Author Z');
    renderLane();
    await user.click(await screen.findByRole('button', { name: 'Author candidates (1)' }));
    expect(screen.queryByTestId('author-group-28')).not.toBeInTheDocument(); // page two
    await user.click(await screen.findByRole('button', { name: 'Show group' }));
    const card = await screen.findByTestId('author-group-28');
    expect(card).toHaveAttribute('data-focused', 'true');
  });

  it('a failed read is an error with Retry, and an empty one says so', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author A', ['Author A.'])]);
    vi.mocked(api.getDedupCandidates).mockRejectedValueOnce(new Error('candidates down'));
    renderLane();
    await user.click(await screen.findByRole('button', { name: /author candidates \(unavailable\)/i }));
    const alert = await screen.findByTestId('author-candidates-error');
    expect(alert).toHaveTextContent('candidates down');
    await user.click(within(alert).getByRole('button', { name: 'Retry' }));
    expect(await screen.findByTestId('author-candidates-empty')).toBeInTheDocument();
  });
});

describe('keyboard help', () => {
  it('? opens the shortcut list and Esc closes it', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author A', ['Author A.'])]);
    renderLane();
    await screen.findByRole('heading', { name: 'Author A' });
    await user.keyboard('?');
    const help = await screen.findByTestId('authors-shortcut-help');
    expect(help).toHaveTextContent('Merge the focused group');
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByTestId('authors-shortcut-help')).not.toBeInTheDocument());
  });
});

describe('review fixes (PR #4004)', () => {
  it('says the results are stale, and offers a rescan, when the server cleared its list after the reviewer changed something', async () => {
    const user = userEvent.setup();
    const composite = wire(3, 'Author C / Author D', [], { split_names: ['Author C', 'Author D'] });
    serve([composite, wire(1, 'Author A', ['Author A.'])]);
    vi.mocked(api.splitCompositeAuthor).mockResolvedValue({ authors: [{ id: 31, name: 'Author C' }], books_updated: 1 });
    vi.mocked(api.refreshAuthorDuplicates).mockResolvedValue(op('scan1'));
    const view = renderLane();
    await user.click(await screen.findByRole('button', { name: /split into 2 authors/i }));
    await waitFor(() => expect(screen.queryByTestId('author-group-3')).not.toBeInTheDocument());
    expect(screen.queryByTestId('authors-stale')).not.toBeInTheDocument();

    // Leave and come back: the server's list is gone, the lane's is not.
    vi.mocked(reads.fetchAuthorGroups).mockResolvedValue({ groups: [], needsRefresh: true });
    view.rerender(
      <MemoryRouter initialEntries={['/review']}>
        <Routes>
          <Route path="/review" element={<Harness active={false} />} />
        </Routes>
      </MemoryRouter>
    );
    view.rerender(
      <MemoryRouter initialEntries={['/review']}>
        <Routes>
          <Route path="/review" element={<Harness active />} />
        </Routes>
      </MemoryRouter>
    );
    const banner = await screen.findByTestId('authors-stale');
    expect(banner).toHaveTextContent('stale since your last change');
    expect(screen.getByRole('heading', { name: 'Author A' })).toBeInTheDocument();
    expect(screen.queryByTestId('authors-needs-scan')).not.toBeInTheDocument();

    await user.click(within(banner).getByRole('button', { name: 'Rescan' }));
    await waitFor(() => expect(api.refreshAuthorDuplicates).toHaveBeenCalledTimes(1));
  });

  it('the books popover says how many distinct books it lists across how many author records', async () => {
    const user = userEvent.setup();
    serve([wire(101, 'Author A', ['Author A.'])]);
    // b1 is credited to both records: two credits, one book.
    vi.mocked(reads.fetchBooksByAuthor).mockImplementation(async (id) =>
      id === 101 ? [book('b1')] : [book('b1'), book('b2')]
    );
    renderLane();
    expect(await screen.findByText('3 credit(s)')).toBeInTheDocument();
    await user.click(screen.getByText('3 credit(s)'));
    expect(await screen.findByTestId('author-books-header')).toHaveTextContent('2 books across 2 author records');
  });

  it('shows how far a bulk merge has got as "processed", not "merged"', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author 1', ['Author 1.']), wire(2, 'Author 2', ['Author 2.'])]);
    vi.mocked(api.pollOperationV2).mockImplementation(() => new Promise(() => {}));
    renderLane();
    await user.click(await screen.findByRole('checkbox', { name: 'Select all 2 groups on this page' }));
    await user.click(screen.getByRole('button', { name: 'Merge Selected (2)' }));
    expect(await screen.findByTestId('authors-bulk-progress')).toHaveTextContent('Processed 0 of 2 groups');
  });

  it('a group with every variant left out cannot be merged, and Merge all stops counting it', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author 1', ['Author 1.']), wire(2, 'Author 2', ['Author 2.'])]);
    renderLane();
    expect(await screen.findByRole('button', { name: 'Merge all 2 name-variant groups' })).toBeInTheDocument();
    await user.click(within(screen.getByTestId('author-group-2')).getByRole('button', { name: 'Remove "Author 2." from this merge' }));
    expect(within(screen.getByTestId('author-group-2')).getByRole('button', { name: 'Merge into "Author 2"' })).toBeDisabled();
    expect(screen.getByRole('button', { name: 'Merge all 1 name-variant group' })).toBeInTheDocument();
    expect(screen.getByRole('checkbox', { name: 'Select group Author 2' })).toBeDisabled();
  });

  it('Merge all asks with the groups it will merge, and says leaving the lane stops the rest', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author 1', ['Author 1.']), wire(2, 'Author 2', ['Author 2.'])]);
    renderLane();
    await user.click(await screen.findByRole('button', { name: 'Merge all 2 name-variant groups' }));
    const dialog = await screen.findByRole('dialog');
    expect(dialog).toHaveTextContent('composite and production-company groups are not included');
    expect(dialog).toHaveTextContent('Keep this lane open');
  });
});

// ---------------------------------------------------------------------------
// The Series sub-view
// ---------------------------------------------------------------------------

function srow(id: number, name: string, author?: number): api.SeriesWithBooks {
  return {
    id,
    name,
    created_at: '',
    ...(author !== undefined ? { author_id: author, author_name: `Author ${author}` } : {}),
  };
}
function swire(name: string, rows: api.SeriesWithBooks[], match_type = 'exact'): api.SeriesDupGroup {
  return { name, count: rows.length, series: rows, match_type };
}
function serveSeries(groups: api.SeriesDupGroup[], opts: { needsRefresh?: boolean; total?: number } = {}) {
  vi.mocked(reads.fetchSeriesGroups).mockResolvedValue({
    groups,
    totalSeries: opts.total ?? groups.length * 2,
    needsRefresh: opts.needsRefresh ?? false,
  });
}
/** Series N: ids N*10+1 and N*10+2, the second credited to author N. */
function series(n: number, match_type = 'exact'): api.SeriesDupGroup {
  return swire(`Series ${n}`, [srow(n * 10 + 1, `Series ${n}`), srow(n * 10 + 2, `series ${n}`, n)], match_type);
}
const seriesCard = (n: number) => screen.findByTestId(`series-group-s${n * 10 + 1}-${n * 10 + 2}`);

describe('the sub-view control', () => {
  it('lists Authors, Series then AI scans, opens on Authors, and Series is chosen from the control, not the URL', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author A', ['Author A.'])]);
    serveSeries([series(1)]);
    renderLane();
    await screen.findByRole('heading', { name: 'Author A' });
    const group = screen.getByRole('group', { name: 'Authors & series sub-view' });
    expect(within(group).getAllByRole('button').map((b) => b.textContent)).toEqual(['Authors', 'Series', 'AI scans']);
    expect(screen.getByTestId('authors-subview-authors')).toHaveAttribute('aria-pressed', 'true');
    expect(reads.fetchSeriesGroups).not.toHaveBeenCalled();

    await user.click(screen.getByTestId('authors-subview-series'));
    expect(await screen.findByTestId('series-panel')).toBeInTheDocument();
    expect(screen.getByTestId('authors-subview-series')).toHaveAttribute('aria-pressed', 'true');
    expect(screen.queryByTestId('authors-panel')).not.toBeInTheDocument();

    await user.click(screen.getByTestId('authors-subview-authors'));
    expect(await screen.findByTestId('authors-panel')).toBeInTheDocument();
    // One read per entry into each sub-view, none for the one not showing.
    expect(reads.fetchSeriesGroups).toHaveBeenCalledTimes(1);
    expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(2);
  });

  it('?view=series opens on Series with Series selected; Authors still works from the control', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author A', ['Author A.'])]);
    serveSeries([series(1)]);
    renderLane(true, 'series');
    expect(await seriesCard(1)).toBeInTheDocument();
    expect(screen.getByTestId('authors-subview-series')).toHaveAttribute('aria-pressed', 'true');
    expect(reads.fetchAuthorGroups).not.toHaveBeenCalled();
    await user.click(screen.getByTestId('authors-subview-authors'));
    expect(await screen.findByRole('heading', { name: 'Author A' })).toBeInTheDocument();
  });
});

describe('the series four states', () => {
  it('loading', async () => {
    vi.mocked(reads.fetchSeriesGroups).mockReturnValue(new Promise(() => {}));
    renderLane(true, 'series');
    expect(await screen.findByTestId('series-loading')).toBeInTheDocument();
    expect(screen.queryByTestId('series-empty')).not.toBeInTheDocument();
  });

  it('error names the failure, offers Retry, and is not the empty state', async () => {
    const user = userEvent.setup();
    vi.mocked(reads.fetchSeriesGroups).mockRejectedValueOnce(new Error('boom: 500'));
    serveSeries([series(1)]);
    renderLane(true, 'series');
    const alert = await screen.findByTestId('series-error');
    expect(alert).toHaveTextContent('Could not load duplicate series: boom: 500');
    expect(screen.queryByTestId('series-empty')).not.toBeInTheDocument();
    await user.click(within(alert).getByRole('button', { name: 'Retry' }));
    expect(await seriesCard(1)).toBeInTheDocument();
    expect(screen.queryByTestId('series-error')).not.toBeInTheDocument();
  });

  it('empty says so, with the library total as a link to the Series page', async () => {
    serveSeries([], { total: 41 });
    renderLane(true, 'series');
    const empty = await screen.findByTestId('series-empty');
    expect(empty).toHaveTextContent('No duplicate series found');
    expect(within(empty).getByTestId('series-total-link')).toHaveTextContent('41 series in the library');
    expect(within(empty).getByTestId('series-total-link')).toHaveAttribute('href', '/series');
  });

  it('a cold cache is "not scanned yet" with a way to scan, not "no duplicates"', async () => {
    const user = userEvent.setup();
    serveSeries([], { needsRefresh: true });
    vi.mocked(api.refreshSeriesDuplicates).mockResolvedValue(op('scan1'));
    renderLane(true, 'series');
    const cold = await screen.findByTestId('series-needs-scan');
    expect(screen.queryByTestId('series-empty')).not.toBeInTheDocument();
    await user.click(within(cold).getByRole('button', { name: 'Scan for duplicate series' }));
    await waitFor(() => expect(api.refreshSeriesDuplicates).toHaveBeenCalledTimes(1));
  });

  it('populated shows one card per group', async () => {
    serveSeries([series(1), series(2)]);
    renderLane(true, 'series');
    expect(await seriesCard(1)).toBeInTheDocument();
    expect(await seriesCard(2)).toBeInTheDocument();
  });

  it('says the results are stale, and offers a rescan, once the server cleared its list after a merge', async () => {
    const user = userEvent.setup();
    serveSeries([series(1), series(2)]);
    vi.mocked(api.refreshSeriesDuplicates).mockResolvedValue(op('scan1'));
    renderLane(true, 'series');
    const card1 = await seriesCard(1);
    await user.click(within(card1).getByRole('button', { name: /^Merge into/ }));
    await waitFor(() => expect(screen.queryByTestId('series-group-s11-12')).not.toBeInTheDocument());
    expect(screen.queryByTestId('series-stale')).not.toBeInTheDocument();

    // The reviewer retries; the server's list is gone, the lane's is not.
    vi.mocked(reads.fetchSeriesGroups).mockResolvedValue({ groups: [], totalSeries: 0, needsRefresh: true });
    vi.mocked(reads.fetchAuthorGroups).mockResolvedValue({ groups: [], needsRefresh: false });
    await user.click(screen.getByTestId('authors-subview-authors'));
    await user.click(await screen.findByTestId('authors-subview-series'));
    const banner = await screen.findByTestId('series-stale');
    expect(banner).toHaveTextContent('stale since your last change');
    expect(screen.getByTestId('series-group-s21-22')).toBeInTheDocument();
    expect(screen.queryByTestId('series-needs-scan')).not.toBeInTheDocument();
    await user.click(within(banner).getByRole('button', { name: 'Rescan' }));
    await waitFor(() => expect(api.refreshSeriesDuplicates).toHaveBeenCalledTimes(1));
  });
});

describe('a series card', () => {
  it('shows an editable name per series, a narrator flag per author, Validate and Merge, and no Prune Series', async () => {
    serveSeries([series(1)]);
    renderLane(true, 'series');
    const card = await seriesCard(1);
    // The author-linked series is kept; the other is merged into it.
    expect(within(card).getByTestId('series-row-12')).toHaveTextContent('Kept');
    expect(within(card).getByTestId('series-row-11')).toHaveTextContent('Merged in');
    expect(within(card).getByRole('button', { name: 'Edit series name Series 1' })).toBeInTheDocument();
    expect(within(card).getByRole('button', { name: 'Edit series name series 1' })).toBeInTheDocument();
    // Only the series with an author gets the flag.
    expect(within(card).getByTestId('series-row-12')).toHaveTextContent('Author');
    expect(within(card).getAllByText('Author', { exact: true })).toHaveLength(1);
    expect(within(card).getByRole('button', { name: 'Validate' })).toBeInTheDocument();
    expect(within(card).getByRole('button', { name: 'Merge into "series 1"' })).toBeEnabled();
    expect(within(card).getByText('2 series')).toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /prune/i })).not.toBeInTheDocument();
  });

  it('the pencil opens an editor with the name; Enter renames through the undoable endpoint and an Undo is offered', async () => {
    const user = userEvent.setup();
    serveSeries([series(1)]);
    vi.mocked(api.renameSeries).mockResolvedValue({ id: 'rename-op-1' } as api.Operation);
    vi.mocked(api.undoSeriesRename).mockResolvedValue({ restored: 1 } as api.RevertOperationResult);
    renderLane(true, 'series');
    const card = await seriesCard(1);
    await user.click(within(card).getByRole('button', { name: 'Edit series name Series 1' }));
    const input = within(card).getByRole('textbox', { name: 'Series name' });
    expect(input).toHaveValue('Series 1');
    await user.clear(input);
    await user.type(input, 'Series One{Enter}');
    await waitFor(() => expect(api.renameSeries).toHaveBeenCalledWith(11, 'Series One'));
    expect(api.updateSeriesName).not.toHaveBeenCalled();
    expect(await within(card).findByText(/"Series One"/)).toBeInTheDocument();
    expect(toast).toHaveBeenCalledWith('Renamed "Series 1" to "Series One".', 'success', expect.objectContaining({ label: 'Undo' }));

    await user.click(within(card).getByRole('button', { name: 'Undo rename of Series One' }));
    await waitFor(() => expect(api.undoSeriesRename).toHaveBeenCalledWith('rename-op-1'));
    expect(await within(card).findByText(/"Series 1"/)).toBeInTheDocument();
    expect(within(card).queryByRole('button', { name: /Undo rename/ })).not.toBeInTheDocument();
  });

  it('the suggested chip renames the kept series from the suggestion', async () => {
    const user = userEvent.setup();
    serveSeries([{ ...series(1), suggested_name: 'Series One' }]);
    renderLane(true, 'series');
    const card = await seriesCard(1);
    await user.click(within(card).getByText('Suggested: "Series One"'));
    expect(within(card).getByRole('textbox', { name: 'Series name' })).toHaveValue('Series One');
  });

  it('a narrator flag is sent with the merge: the series merge goes, then the author is reclassified', async () => {
    const user = userEvent.setup();
    serveSeries([series(1)]);
    const calls: string[] = [];
    vi.mocked(api.reclassifyAuthorAsNarrator).mockImplementation(async (id) => {
      calls.push(`reclassify:${id}`);
      return op(`r${id}`);
    });
    vi.mocked(api.mergeSeriesGroup).mockImplementation(async (keep, ids) => {
      calls.push(`merge:${keep}:${ids.join(',')}`);
      return op('s12');
    });
    renderLane(true, 'series');
    const card = await seriesCard(1);
    await user.click(within(card).getByText('Author', { exact: true }));
    expect(within(card).getByText('Narrator')).toBeInTheDocument();
    await user.click(within(card).getByRole('button', { name: /^Merge into/ }));
    await waitFor(() => expect(screen.queryByTestId('series-group-s11-12')).not.toBeInTheDocument());
    await waitFor(() => expect(calls).toEqual(['merge:12:11', 'reclassify:1']));
  });

  it('leaving a series out keeps it out of the merge; with one left the Merge button says why it is off', async () => {
    const user = userEvent.setup();
    serveSeries([series(1)]);
    renderLane(true, 'series');
    const card = await seriesCard(1);
    await user.click(within(card).getByRole('checkbox', { name: 'Include series Series 1 (ID 11) in the merge' }));
    expect(within(card).getByTestId('series-row-11')).toHaveTextContent('Left out');
    expect(within(card).getByRole('button', { name: /^Merge into/ })).toBeDisabled();
    expect(within(card).getByRole('status')).toHaveTextContent('Include at least two series');
    expect(screen.getByRole('checkbox', { name: 'Select group Series 1' })).toBeDisabled();
    // Putting it back restores the merge.
    await user.click(within(card).getByRole('checkbox', { name: 'Include series Series 1 (ID 11) in the merge' }));
    expect(within(card).getByRole('button', { name: /^Merge into/ })).toBeEnabled();
  });

  it('Validate shows what the providers returned, on the group name', async () => {
    const user = userEvent.setup();
    serveSeries([series(1)]);
    vi.mocked(reads.validateSeriesName).mockResolvedValue({
      results: [{ source: 'openlibrary', title: 'Book One', author: 'Author A', series: 'Series 1', series_position: '1' }],
      query: 'Series 1',
    });
    renderLane(true, 'series');
    const card = await seriesCard(1);
    await user.click(within(card).getByRole('button', { name: 'Validate' }));
    const results = await within(card).findByTestId('series-validation-results');
    expect(results).toHaveTextContent('Book One');
    expect(results).toHaveTextContent('Series: Series 1 #1');
    expect(reads.validateSeriesName).toHaveBeenCalledWith('Series 1', expect.any(AbortSignal));
  });

  it('a failed Validate is an error line, not "No external matches"', async () => {
    const user = userEvent.setup();
    serveSeries([series(1)]);
    vi.mocked(reads.validateSeriesName).mockRejectedValue(new Error('providers down'));
    renderLane(true, 'series');
    const card = await seriesCard(1);
    await user.click(within(card).getByRole('button', { name: 'Validate' }));
    expect(await within(card).findByTestId('series-validation-error')).toHaveTextContent('Validation failed: providers down');
    expect(within(card).queryByText(/No external matches/)).not.toBeInTheDocument();
  });

  it('marks a book listed under two of the series', async () => {
    const shared = { id: 'b1', title: 'Book One' };
    serveSeries([
      swire('Series 1', [
        { ...srow(11, 'Series 1'), books: [shared, { id: 'b2', title: 'Book Two' }] },
        { ...srow(12, 'series 1'), books: [shared] },
      ]),
    ]);
    renderLane(true, 'series');
    const card = await seriesCard(1);
    const titles = within(card).getAllByText('Book One');
    expect(titles).toHaveLength(2);
    expect(titles[0].closest('[title]')).toHaveAttribute('title', expect.stringContaining('more than one'));
    expect(within(card).getByText('Book Two').closest('[title]')).toBeNull();
  });
});

describe('the series bulk verbs', () => {
  it('shows Merge All (N) with N the same-name groups, and the Same name chip opens exactly those', async () => {
    const user = userEvent.setup();
    serveSeries([series(1), series(2), series(3, 'subseries')]);
    renderLane(true, 'series');
    await seriesCard(1);
    expect(screen.getByRole('button', { name: 'Merge All (2)' })).toBeEnabled();
    expect(screen.getByTestId('series-kind-all')).toHaveTextContent('All groups (3)');
    expect(screen.getByTestId('series-kind-exact')).toHaveTextContent('Same name (2)');
    expect(screen.getByTestId('series-kind-subseries')).toHaveTextContent('Sub-series (1)');
    await user.click(screen.getByTestId('series-kind-exact'));
    expect(screen.getAllByTestId(/^series-group-s\d/)).toHaveLength(2);
    await user.click(screen.getByTestId('series-kind-subseries'));
    expect(screen.getAllByTestId(/^series-group-s\d/)).toHaveLength(1);
    expect(screen.getByText('sub-series')).toBeInTheDocument();
  });

  it('Merge All asks first, lists every group it will merge, and nothing is merged on Cancel', async () => {
    const user = userEvent.setup();
    serveSeries([series(1), series(2), series(3, 'subseries')]);
    renderLane(true, 'series');
    await seriesCard(1);
    await user.click(screen.getByRole('button', { name: 'Merge All (2)' }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByRole('heading', { name: 'Confirm Merge All' })).toBeInTheDocument();
    expect(dialog).toHaveTextContent('This will merge 2 same-name series groups');
    expect(dialog).toHaveTextContent('Sub-series groups are not included');
    expect(within(dialog).getByTestId('series-merge-all-warning')).toHaveTextContent('leaving it stops the remaining merges');
    const names = within(within(dialog).getByTestId('series-merge-all-list')).getAllByRole('listitem');
    expect(names.map((li) => li.textContent)).toEqual(['Series 1', 'Series 2']);
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(api.mergeSeriesGroup).not.toHaveBeenCalled();

    await user.click(screen.getByRole('button', { name: 'Merge All (2)' }));
    await user.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Confirm' }));
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(2));
    expect(api.deduplicateSeries).not.toHaveBeenCalled();
    await waitFor(() => expect(toast).toHaveBeenCalledWith('Merged 2 of 2 same-name group(s).', 'success'));
  });

  it('select page -> all 30 -> confirmed merge of every group (ported from the old tab)', async () => {
    const user = userEvent.setup();
    serveSeries(Array.from({ length: 30 }, (_, i) => series(i + 1)));
    renderLane(true, 'series');
    await user.click(await screen.findByRole('checkbox', { name: 'Select all 25 groups on this page' }));
    await user.click(screen.getByTestId('series-groups-select-all-matching'));
    expect(screen.getByTestId('series-groups-select-all-banner')).toHaveTextContent(
      'All 30 groups matching this filter are selected.'
    );
    await user.click(screen.getByRole('button', { name: 'Merge Selected (30)' }));
    const dialog = await screen.findByTestId('series-merge-selected-confirm');
    expect(api.mergeSeriesGroup).not.toHaveBeenCalled();
    await user.click(within(dialog).getByTestId('series-merge-selected-confirm-btn'));
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(30));
    await waitFor(() => expect(screen.queryByTestId('series-bulk-progress')).not.toBeInTheDocument());
  });

  it('merging one selected group keeps the keep choice and the selection on the right group (ported)', async () => {
    const user = userEvent.setup();
    serveSeries([series(1), series(2), series(3), series(4)]);
    renderLane(true, 'series');
    await screen.findByRole('checkbox', { name: 'Select group Series 1' });
    await user.click(screen.getByRole('checkbox', { name: 'Select group Series 3' }));
    await user.click(within(await seriesCard(1)).getByRole('button', { name: /^Merge into/ }));
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenCalledWith(12, [11]));
    await waitFor(() => expect(screen.queryByRole('checkbox', { name: 'Select group Series 1' })).toBeNull());
    // Series 4 on its own keeps Series 4's own series, not Series 3's.
    await user.click(within(await seriesCard(4)).getByRole('button', { name: /^Merge into/ }));
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenLastCalledWith(42, [41]));
    // The selection stayed on Series 3.
    await user.click(screen.getByRole('button', { name: 'Merge Selected (1)' }));
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(3));
    expect(api.mergeSeriesGroup).toHaveBeenLastCalledWith(32, [31]);
  });

  it('shows how far a bulk merge has got as "processed", on the Series bar only', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author A', ['Author A.'])]);
    serveSeries([series(1), series(2)]);
    vi.mocked(api.pollOperationV2).mockImplementation(() => new Promise(() => {}));
    renderLane(true, 'series');
    await user.click(await screen.findByRole('checkbox', { name: 'Select all 2 groups on this page' }));
    await user.click(screen.getByRole('button', { name: 'Merge Selected (2)' }));
    expect(await screen.findByTestId('series-bulk-progress')).toHaveTextContent('Processed 0 of 2 groups');
    // The Authors panel does not draw a bar for a run it did not start.
    await user.click(screen.getByTestId('authors-subview-authors'));
    await screen.findByRole('heading', { name: 'Author A' });
    expect(screen.queryByTestId('authors-bulk-progress')).not.toBeInTheDocument();
  });

  it('a group with a write in flight locks its own controls, while other groups stay usable', async () => {
    const user = userEvent.setup();
    serveSeries([series(1), series(2)]);
    vi.mocked(api.pollOperationV2).mockImplementation(() => new Promise(() => {}));
    renderLane(true, 'series');
    const card1 = await seriesCard(1);
    const merge1 = within(card1).getByRole('button', { name: /^Merge into/ });
    await user.click(merge1);
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(1));
    expect(merge1).toBeDisabled();
    expect(card1).toHaveAttribute('aria-busy', 'true');
    expect(within(card1).getByRole('button', { name: 'Edit series name Series 1' })).toBeDisabled();
    expect(within(await seriesCard(2)).getByRole('button', { name: /^Merge into/ })).toBeEnabled();
    expect(screen.getByRole('button', { name: /^Merge All/ })).toBeDisabled();
  });
});

describe('series keyboard help', () => {
  it('? opens the shortcut list and Esc closes it; the Authors list is not the one shown', async () => {
    const user = userEvent.setup();
    serveSeries([series(1)]);
    renderLane(true, 'series');
    await seriesCard(1);
    await user.keyboard('?');
    const help = await screen.findByTestId('series-shortcut-help');
    expect(help).toHaveTextContent('Merge the focused group');
    expect(help).not.toHaveTextContent("Show the focused group's books");
    expect(screen.queryByTestId('authors-shortcut-help')).not.toBeInTheDocument();
    await user.keyboard('m');
    expect(api.mergeSeriesGroup).not.toHaveBeenCalled();
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByTestId('series-shortcut-help')).not.toBeInTheDocument());
  });
});

describe('keyboard while a menu is open in front of the list', () => {
  it('m does nothing on the Series sub-view while the "Groups per page" menu is open', async () => {
    const user = userEvent.setup();
    serveSeries([series(1), series(2)]);
    renderLane(true, 'series');
    await seriesCard(1);
    await user.click(screen.getByRole('combobox', { name: /groups per page/i }));
    expect(await screen.findByRole('listbox')).toBeInTheDocument();
    await user.keyboard('m');
    await user.keyboard('j');
    expect(api.mergeSeriesGroup).not.toHaveBeenCalled();

    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('listbox')).not.toBeInTheDocument());
    await user.keyboard('m');
    await waitFor(() => expect(api.mergeSeriesGroup).toHaveBeenCalledTimes(1));
  });

  it('and on the Authors sub-view', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author 1', ['Author 1.']), wire(2, 'Author 2', ['Author 2.'])]);
    renderLane();
    await screen.findByRole('heading', { name: 'Author 1' });
    await user.click(screen.getByRole('combobox', { name: /groups per page/i }));
    expect(await screen.findByRole('listbox')).toBeInTheDocument();
    await user.keyboard('m');
    expect(api.mergeAuthors).not.toHaveBeenCalled();
    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByRole('listbox')).not.toBeInTheDocument());
    await user.keyboard('m');
    await waitFor(() => expect(api.mergeAuthors).toHaveBeenCalledTimes(1));
  });
});

describe('one write queue, two sub-views', () => {
  it('while a Series bulk run is going, the Authors sub-view says why its buttons are grey', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author A', ['Author A.'])]);
    serveSeries([series(1), series(2)]);
    vi.mocked(api.pollOperationV2).mockImplementation(() => new Promise(() => {}));
    renderLane(true, 'series');
    await user.click(await screen.findByRole('checkbox', { name: 'Select all 2 groups on this page' }));
    await user.click(screen.getByRole('button', { name: 'Merge Selected (2)' }));
    await screen.findByTestId('series-bulk-progress');

    await user.click(screen.getByTestId('authors-subview-authors'));
    await screen.findByRole('heading', { name: 'Author A' });
    expect(screen.getByTestId('authors-series-running')).toHaveTextContent('A series merge is running (0 of 2 groups)');
    expect(screen.getByRole('button', { name: /^Merge all/ })).toBeDisabled();
  });

  it('and the Series sub-view says so for an Authors bulk run', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author 1', ['Author 1.']), wire(2, 'Author 2', ['Author 2.'])]);
    serveSeries([series(1)]);
    vi.mocked(api.pollOperationV2).mockImplementation(() => new Promise(() => {}));
    renderLane();
    await user.click(await screen.findByRole('checkbox', { name: 'Select all 2 groups on this page' }));
    await user.click(screen.getByRole('button', { name: 'Merge Selected (2)' }));
    await screen.findByTestId('authors-bulk-progress');
    expect(screen.queryByTestId('authors-series-running')).not.toBeInTheDocument();

    await user.click(screen.getByTestId('authors-subview-series'));
    expect(await screen.findByTestId('series-authors-running')).toHaveTextContent('An author merge is running (0 of 2 groups)');
  });
});
