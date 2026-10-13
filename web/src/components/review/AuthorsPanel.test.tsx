// file: web/src/components/review/AuthorsPanel.test.tsx
// version: 1.0.0
// guid: e3b70c58-1d92-4a6f-8c45-7f20b9d6a184
// last-edited: 2026-10-13

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
import * as reads from './lanes/authorsReads';

vi.mock('../../services/api');
vi.mock('./lanes/authorsReads');

const toast = vi.fn();

function Harness({ active = true }: { active?: boolean }) {
  const authors = useAuthorsLane(toast, active, { authorId: null });
  return <AuthorsPanel authors={authors} />;
}

function renderLane(active = true) {
  return render(
    <MemoryRouter initialEntries={['/review']}>
      <Routes>
        <Route path="/review" element={<Harness active={active} />} />
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
  vi.mocked(api.reclassifyAuthorAsNarrator).mockResolvedValue({ narrator_id: 1, books_updated: 1 });
  vi.mocked(reads.fetchAuthorName).mockResolvedValue('');
});

describe('books popover', () => {
  async function openBooks(user: ReturnType<typeof userEvent.setup>) {
    await user.click(await screen.findByText('3 book(s)'));
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
    await screen.findByText('3 book(s)');
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
    expect(screen.getByText('3 book(s)')).toBeInTheDocument();
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

  it('a production company shows its chip and Find Real Author, which resolves and reloads', async () => {
    const user = userEvent.setup();
    serve([wire(4, 'Studio One', [], { is_production_company: true })]);
    vi.mocked(api.resolveProductionAuthor).mockResolvedValue(op('r1'));
    renderLane();
    expect(await screen.findByText('Production Company')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: /find real author/i }));
    await waitFor(() => expect(api.resolveProductionAuthor).toHaveBeenCalledWith(4));
    await waitFor(() => expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(2));
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
    expect(screen.getByRole('button', { name: /^Merge All/ })).toBeDisabled();

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
        'Merged 1 of 2 selected group(s); 1 failed: "Author 2": store write failed',
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
    expect(await screen.findByRole('button', { name: 'Merge All (1)' })).toBeInTheDocument();
    expect(screen.getByRole('checkbox', { name: 'Select group Studio One' })).toBeDisabled();
    expect(screen.getByRole('checkbox', { name: 'Select group Author 1' })).toBeEnabled();
  });

  it('merge all asks for confirmation, and nothing is merged on Cancel', async () => {
    const user = userEvent.setup();
    serve([wire(1, 'Author 1', ['Author 1.']), wire(2, 'Author 2', ['Author 2.'])]);
    renderLane();
    await user.click(await screen.findByRole('button', { name: 'Merge All (2)' }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByRole('heading', { name: 'Confirm Merge All' })).toBeInTheDocument();
    expect(dialog).toHaveTextContent('This will merge 2 groups.');
    await user.click(within(dialog).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(api.mergeAuthors).not.toHaveBeenCalled();

    await user.click(screen.getByRole('button', { name: 'Merge All (2)' }));
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
