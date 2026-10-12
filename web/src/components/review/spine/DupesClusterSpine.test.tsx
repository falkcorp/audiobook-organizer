// file: web/src/components/review/spine/DupesClusterSpine.test.tsx
// version: 1.1.0
// guid: 0f9c2e68-4b17-4d53-a8e0-7c1d5b3a9e24
// last-edited: 2026-10-12
//
// The Clusters view of the Duplicates lane: cards, primary choice, the three
// cluster verbs, the other-pages line, what the counts open, and the card
// checkbox that rides on the lane's cross-page selection (the contract the
// retired Version Groups tab's selection tests asserted).

import { useState } from 'react';
import { render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../../services/api';
import type { Book, Config, DedupCandidate } from '../../../services/api';
import { __resetPathVarsCacheForTests } from '../../../utils/formatPath';
import { __resetPathAliasesCacheForTests } from '../../common/PathLinks';
import { buildClusters } from '../lanes/dupesClusters';
import type { ClusterOverflow } from '../lanes/useDupesClusterData';
import type { DupesAction } from '../reviewActions';
import { DupesClusterSpine, type DupesClusterSpineProps } from './DupesClusterSpine';

vi.mock('../../../services/api');

beforeEach(() => {
  __resetPathAliasesCacheForTests();
  __resetPathVarsCacheForTests();
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '', path_aliases: [] } as unknown as Config);
});

const RICH: Partial<Book> = { asin: 'B000000001', isbn13: '9780000000002', cover_url: 'c.jpg' };

function book(id: string, extra: Partial<Book> = {}): Book {
  return { id, title: `Title ${id}`, file_path: `/lib/${id}.m4b`, ...extra } as Book;
}

function pair(id: number, a: string, b: string, extra: Partial<DedupCandidate> = {}) {
  return {
    id,
    entity_type: 'book',
    entity_a_id: a,
    entity_b_id: b,
    layer: 'embedding',
    status: 'pending',
    created_at: '',
    updated_at: '',
    book_a: book(a),
    book_b: book(b),
    ...extra,
  } as DedupCandidate;
}

// Cluster 1: b1-b2-b3 (pairs 1, 2). Cluster 2: b4-b5 (pair 3).
const CANDS = [pair(1, 'b1', 'b2'), pair(2, 'b2', 'b3'), pair(3, 'b4', 'b5')];

function renderSpine(over: Partial<DupesClusterSpineProps> = {}) {
  const onAction = vi.fn<(a: DupesAction) => void>();
  const onSetPairsSelected = vi.fn();
  const onOpenCompare = vi.fn();
  const candidates = over.candidates ?? CANDS;
  const props: DupesClusterSpineProps = {
    clusters: buildClusters(candidates),
    candidates,
    overflow: () => ({ state: 'ready', count: 0, perBook: [] }),
    emptyMessage: 'Nothing here',
    busy: false,
    isSelected: () => false,
    onSetPairsSelected,
    onAction,
    onOpenCompare,
    ...over,
  };
  render(
    <MemoryRouter>
      <DupesClusterSpine {...props} />
    </MemoryRouter>
  );
  return { onAction, onSetPairsSelected, onOpenCompare };
}

const card = (key: string) => screen.getByTestId(`dupes-cluster-${key}`);

describe('DupesClusterSpine', () => {
  it('shows connected books as one card, each with a primary radio and Remove from cluster', async () => {
    renderSpine();
    const c1 = card('b1');
    expect(within(c1).getByText('Title b1')).toBeInTheDocument();
    expect(within(c1).getByText('Title b3')).toBeInTheDocument();
    expect(within(c1).getAllByRole('radio', { name: /as the primary/ })).toHaveLength(3);
    expect(within(c1).getAllByRole('button', { name: 'Remove from cluster' })).toHaveLength(3);
    expect(within(c1).getByRole('button', { name: 'Link cluster' })).toBeInTheDocument();
    expect(within(c1).getByRole('button', { name: 'Reject cluster' })).toBeInTheDocument();
    // The second component is its own card.
    expect(within(card('b4')).getAllByRole('radio', { name: /as the primary/ })).toHaveLength(2);
  });

  it('link cluster asks first, then sends the chosen primary', async () => {
    const user = userEvent.setup();
    const { onAction } = renderSpine();
    const c1 = card('b1');
    await user.click(within(c1).getByRole('radio', { name: 'Keep Title b3 as the primary' }));
    await user.click(within(c1).getByRole('button', { name: 'Link cluster' }));
    const dialog = await screen.findByTestId('cluster-link-confirm');
    expect(dialog).toHaveTextContent('Link 3 books into one group?');
    expect(dialog).toHaveTextContent('"Title b3" is kept as the primary');
    expect(onAction).not.toHaveBeenCalled();
    await user.click(within(dialog).getByTestId('cluster-link-confirm-btn'));
    expect(onAction).toHaveBeenCalledWith({
      lane: 'dupes',
      type: 'linkCluster',
      ids: ['b1', 'b2', 'b3'],
      primaryId: 'b3',
    });
  });

  it('cancelling the link dialog sends nothing', async () => {
    const user = userEvent.setup();
    const { onAction } = renderSpine();
    await user.click(within(card('b4')).getByRole('button', { name: 'Link cluster' }));
    await user.click(within(await screen.findByTestId('cluster-link-confirm')).getByRole('button', { name: 'Cancel' }));
    expect(onAction).not.toHaveBeenCalled();
  });

  it('pre-selects the richest member and omits primaryId when the reviewer picks "let the server pick"', async () => {
    const user = userEvent.setup();
    const cands = [pair(1, 'b1', 'b2', { book_b: book('b2', RICH) })];
    const { onAction } = renderSpine({ candidates: cands, clusters: buildClusters(cands) });
    const c = card('b1');
    expect(within(c).getByRole('radio', { name: 'Keep Title b2 as the primary' })).toBeChecked();

    await user.click(within(c).getByRole('radio', { name: /server pick/i }));
    await user.click(within(c).getByRole('button', { name: 'Link cluster' }));
    await user.click(within(await screen.findByTestId('cluster-link-confirm')).getByTestId('cluster-link-confirm-btn'));
    expect(onAction).toHaveBeenCalledWith({ lane: 'dupes', type: 'linkCluster', ids: ['b1', 'b2'] });
  });

  it('reject cluster asks first, names the pairs it dismisses, and sends exactly those', async () => {
    const user = userEvent.setup();
    const { onAction } = renderSpine();
    await user.click(within(card('b1')).getByRole('button', { name: 'Reject cluster' }));
    const dialog = await screen.findByTestId('cluster-reject-confirm');
    expect(dialog).toHaveTextContent('Mark 2 pairs as not a duplicate?');
    expect(dialog).toHaveTextContent('hidden by the current filter, are left alone');
    expect(onAction).not.toHaveBeenCalled();
    await user.click(within(dialog).getByTestId('cluster-reject-confirm-btn'));
    expect(onAction).toHaveBeenCalledWith({
      lane: 'dupes',
      type: 'rejectCluster',
      ids: ['b1', 'b2', 'b3'],
      candidateIds: [1, 2],
    });
  });

  it('cancelling the reject dialog sends nothing', async () => {
    const user = userEvent.setup();
    const { onAction } = renderSpine();
    await user.click(within(card('b4')).getByRole('button', { name: 'Reject cluster' }));
    await user.click(
      within(await screen.findByTestId('cluster-reject-confirm')).getByRole('button', { name: 'Cancel' })
    );
    expect(onAction).not.toHaveBeenCalled();
  });

  it('a primary picked before Remove from cluster is dropped once that book leaves the card', async () => {
    const user = userEvent.setup();
    const onAction = vi.fn<(a: DupesAction) => void>();
    const full = buildClusters(CANDS);
    const props = (clusters: typeof full): DupesClusterSpineProps => ({
      clusters,
      candidates: CANDS,
      overflow: () => ({ state: 'ready', count: 0, perBook: [] }),
      emptyMessage: '',
      busy: false,
      isSelected: () => false,
      onSetPairsSelected: vi.fn(),
      onAction,
      onOpenCompare: vi.fn(),
    });
    const view = render(
      <MemoryRouter>
        <DupesClusterSpine {...props(full)} />
      </MemoryRouter>
    );
    await user.click(
      within(card('b1')).getByRole('radio', { name: 'Keep Title b3 as the primary' })
    );
    // b3 is removed: the same card (same key) now holds b1 and b2 only.
    const after = buildClusters([CANDS[0], CANDS[2]]);
    view.rerender(
      <MemoryRouter>
        <DupesClusterSpine {...props(after)} />
      </MemoryRouter>
    );
    await user.click(within(card('b1')).getByRole('button', { name: 'Link cluster' }));
    await user.click(
      within(await screen.findByTestId('cluster-link-confirm')).getByTestId('cluster-link-confirm-btn')
    );
    const sent = onAction.mock.calls.at(-1)![0] as { ids: string[]; primaryId?: string };
    expect(sent.ids).toEqual(['b1', 'b2']);
    expect(sent.primaryId).toBeUndefined();
  });

  it('remove from cluster dispatches the one member against the whole cluster', async () => {
    const user = userEvent.setup();
    const { onAction } = renderSpine();
    await user.click(within(card('b1')).getAllByRole('button', { name: 'Remove from cluster' })[2]);
    expect(onAction).toHaveBeenCalledWith({
      lane: 'dupes',
      type: 'removeFromCluster',
      ids: ['b1', 'b2', 'b3'],
      removeIds: ['b3'],
    });
  });

  it('disables the verbs while something is in flight', () => {
    renderSpine({ busy: true });
    expect(within(card('b1')).getByRole('button', { name: 'Link cluster' })).toBeDisabled();
    expect(within(card('b1')).getByRole('button', { name: 'Reject cluster' })).toBeDisabled();
  });

  it('a decided cluster shows its status and no verbs', () => {
    const cands = [pair(1, 'b1', 'b2', { status: 'merged' })];
    renderSpine({ candidates: cands, clusters: buildClusters(cands) });
    const c = card('b1');
    expect(within(c).getByText('merged')).toBeInTheDocument();
    expect(within(c).queryByRole('button', { name: 'Link cluster' })).not.toBeInTheDocument();
    expect(within(c).queryByRole('radio')).not.toBeInTheDocument();
  });

  it('a cluster spanning two pages shows the other-pages line, and it opens the books concerned', async () => {
    const user = userEvent.setup();
    const overflow = (): ClusterOverflow => ({
      state: 'ready',
      count: 4,
      perBook: [{ bookId: 'b2', count: 4 }],
    });
    renderSpine({ overflow, bookLinkQuery: '&layer=embedding&band=HIGH' });
    const line = within(card('b1')).getByTestId('cluster-other-pages-b1');
    expect(line).toHaveTextContent('4 more pairs for these books are on other pages');
    await user.click(line);
    const other = await screen.findByTestId('cluster-detail-other');
    expect(within(other).getByRole('link', { name: 'Title b2' })).toHaveAttribute(
      'href',
      '/review?lane=dupes&book=b2&layer=embedding&band=HIGH'
    );
    // The label says the link opens ALL of the book's pairs, not just the 4.
    expect(screen.getByTestId('cluster-detail')).toHaveTextContent('all pairs for that book');
    expect(other).toHaveTextContent('4 pairs off this page');
  });

  it('says nothing about other pages when there are none', () => {
    renderSpine({ overflow: () => ({ state: 'ready', count: 0, perBook: [] }) });
    expect(screen.queryByTestId('cluster-other-pages-b1')).not.toBeInTheDocument();
  });

  it('shows a loading note instead of a count', () => {
    renderSpine({ overflow: () => ({ state: 'loading' }) });
    expect(screen.getAllByTestId('cluster-other-loading')).toHaveLength(2);
  });

  it('reports a failed other-pages count', () => {
    renderSpine({ overflow: () => ({ state: 'error', message: 'boom' }) });
    expect(screen.getAllByTestId('cluster-other-error')[0]).toHaveTextContent('boom');
  });

  it('the book count and the pair count both open the member list', async () => {
    const user = userEvent.setup();
    const { onOpenCompare } = renderSpine();
    await user.click(screen.getByTestId('cluster-books-count-b1'));
    const books = await screen.findByTestId('cluster-detail-books');
    expect(within(books).getAllByRole('link')).toHaveLength(3);
    expect(within(screen.getByTestId('cluster-detail-pairs')).getAllByRole('listitem')).toHaveLength(2);
    await user.click(within(screen.getByTestId('cluster-detail-pairs')).getAllByRole('button', { name: 'Compare' })[0]);
    expect(onOpenCompare).toHaveBeenCalledWith(1);

    await user.click(screen.getByTestId('cluster-pairs-count-b4'));
    expect(await screen.findByTestId('cluster-detail-books')).toHaveTextContent('Title b4');
  });

  it('shows the empty message', () => {
    renderSpine({ candidates: [], clusters: [] });
    expect(screen.getByText('Nothing here')).toBeInTheDocument();
  });

  it('says so when a deep-linked book has no pairs', () => {
    renderSpine({ candidates: [], clusters: [], deepLinkedBookId: 'b1' });
    expect(screen.getByText('No duplicate candidates for this book.')).toBeInTheDocument();
  });
});

describe('DupesClusterSpine selection', () => {
  // The card checkbox selects every pair the card holds, through the same
  // selection the page header and "Select all N matching" use.
  function Harness({ initial }: { initial: number[] }) {
    const [sel, setSel] = useState<ReadonlySet<number>>(new Set(initial));
    return (
      <MemoryRouter>
        <DupesClusterSpine
          clusters={buildClusters(CANDS)}
          candidates={CANDS}
          overflow={() => ({ state: 'ready', count: 0, perBook: [] })}
          emptyMessage=""
          busy={false}
          isSelected={(id) => sel.has(id)}
          onSetPairsSelected={(ids, on) =>
            setSel((prev) => {
              const next = new Set(prev);
              for (const id of ids) {
                if (on) next.add(id);
                else next.delete(id);
              }
              return next;
            })
          }
          onAction={() => {}}
          onOpenCompare={() => {}}
        />
        <output data-testid="sel">{[...sel].sort().join(',')}</output>
      </MemoryRouter>
    );
  }

  const box = (name: RegExp) => screen.getByRole('checkbox', { name });

  it('ticking a card selects all of its pairs, and unticking clears them', async () => {
    const user = userEvent.setup();
    render(<Harness initial={[]} />);
    await user.click(box(/Select cluster Title b1/));
    expect(screen.getByTestId('sel')).toHaveTextContent('1,2');
    expect(box(/Select cluster Title b1/)).toBeChecked();
    expect(box(/Select cluster Title b4/)).not.toBeChecked();
    await user.click(box(/Select cluster Title b1/));
    expect(screen.getByTestId('sel')).toHaveTextContent('');
  });

  it('a card with only some of its pairs selected reads as indeterminate, and a click completes it', async () => {
    const user = userEvent.setup();
    render(<Harness initial={[1]} />);
    const cb = box(/Select cluster Title b1/) as HTMLInputElement;
    expect(cb).not.toBeChecked();
    expect(cb).toHaveAttribute('data-indeterminate', 'true');
    await user.click(cb);
    expect(screen.getByTestId('sel')).toHaveTextContent('1,2');
  });

  it('a selection made elsewhere (the page header, select-all-matching) reads as ticked on the card', () => {
    render(<Harness initial={[3]} />);
    expect(box(/Select cluster Title b4/)).toBeChecked();
    expect(box(/Select cluster Title b1/)).not.toBeChecked();
  });
});
