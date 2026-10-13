// file: web/src/components/review/DupesPanel.clusters.test.tsx
// version: 1.4.0
// guid: a3d8f167-92c4-4e0b-b5a1-6f2e8c0d4b79
// last-edited: 2026-10-13
//
// The Clusters view through the rendered DupesPanel: the cluster spine in place
// of the pair rows, the By-series toggle with its confirmation, and the
// cross-page selection contract carried over from the retired Version Groups tab.

import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../services/api';
import { DupesPanel } from './DupesPanel';
import { useDupesLane } from './lanes/useDupesLane';

vi.mock('../../services/api');

const toast = vi.fn();
const TOTAL = 137;

// Two-book clusters, three pairs per page: pair n joins x{n} and y{n}.
function cand(id: number): api.DedupCandidate {
  return {
    id,
    entity_type: 'book',
    entity_a_id: `x${id}`,
    entity_b_id: `y${id}`,
    layer: 'embedding',
    status: 'pending',
    created_at: '',
    updated_at: '',
    book_a: { id: `x${id}`, title: `Book X${id}` },
    book_b: { id: `y${id}`, title: `Book Y${id}` },
  } as api.DedupCandidate;
}

beforeEach(() => {
  vi.resetAllMocks();
  toast.mockClear();
  vi.mocked(api.getDedupCandidates).mockImplementation(async (params) => {
    if (params?.entity_id) return { candidates: [], total: 0 };
    const offset = params?.offset ?? 0;
    return { candidates: [1, 2, 3].map((i) => cand(offset + i)), total: TOTAL };
  });
  vi.mocked(api.getDedupStats).mockResolvedValue({ stats: [] });
  vi.mocked(api.getDedupBookPairCounts).mockResolvedValue([]);
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '' } as api.Config);
  vi.mocked(api.countBulkDedupCandidates).mockResolvedValue(TOTAL);
  vi.mocked(api.listDedupCandidateSeries).mockResolvedValue([
    { series_id: 7, series_name: 'Series A', cluster_count: 2, book_count: 5, candidate_count: 6 },
  ]);
  vi.mocked(api.linkDedupCandidateSeries).mockResolvedValue({
    series_id: 7,
    clusters_merged: 2,
    books_merged: 5,
    candidates_updated: 6,
  });
});

function Harness({ layer = null }: { layer?: 'embedding' | null }) {
  const dupes = useDupesLane(toast, true, { band: 'HIGH', entityId: null, layer }, true);
  return (
    <DupesPanel
      dupes={dupes}
      viewMode="candidates"
      expandedId={null}
      onToggleExpand={() => {}}
      subView="queue"
      onSubViewChange={() => {}}
    />
  );
}

function renderPanel(layer: 'embedding' | null = null) {
  return render(
    <MemoryRouter>
      <Harness layer={layer} />
    </MemoryRouter>
  );
}

describe('DupesPanel Clusters view', () => {
  it('renders cluster cards instead of pair rows', async () => {
    renderPanel();
    expect(await screen.findByTestId('dupes-cluster-x1')).toBeInTheDocument();
    expect(screen.getByTestId('dupes-cluster-spine')).toBeInTheDocument();
    expect(screen.queryByTestId('dupes-spine')).not.toBeInTheDocument();
    expect(screen.getByTestId('dupes-cluster-x3')).toBeInTheDocument();
  });

  it('link cluster through the panel: confirm, then the request carries the books and primary', async () => {
    const user = userEvent.setup();
    vi.mocked(api.linkDedupCluster).mockResolvedValue({
      status: 'merged',
      merged_books: 2,
      candidates_updated: 1,
    });
    renderPanel();
    const c = await screen.findByTestId('dupes-cluster-x1');
    await user.click(within(c).getByRole('radio', { name: 'Keep Book Y1 as the primary' }));
    await user.click(within(c).getByRole('button', { name: 'Link cluster' }));
    expect(api.linkDedupCluster).not.toHaveBeenCalled();
    await user.click(within(await screen.findByTestId('cluster-link-confirm')).getByTestId('cluster-link-confirm-btn'));
    await waitFor(() => expect(api.linkDedupCluster).toHaveBeenCalledWith(['x1', 'y1'], 'y1'));
  });

  it('selection survives a page turn when it is "all matching", and the cards read as ticked', async () => {
    const user = userEvent.setup();
    renderPanel();
    await screen.findByTestId('dupes-cluster-x1');
    await user.click(screen.getByRole('checkbox', { name: 'Select all 3 on this page' }));
    await user.click(await screen.findByTestId('dupes-select-all-matching'));

    await user.click(screen.getByRole('button', { name: 'Go to page 2' }));
    await screen.findByTestId('dupes-cluster-x51');
    expect(screen.getByRole('checkbox', { name: /Select cluster Book X51/ })).toBeChecked();
    expect(screen.getByTestId('dupes-selected-count')).toHaveTextContent('(every page)');
  });

  it('an explicit selection is cleared by a page turn, with a toast', async () => {
    const user = userEvent.setup();
    renderPanel();
    await screen.findByTestId('dupes-cluster-x1');
    await user.click(screen.getByRole('checkbox', { name: /Select cluster Book X1/ }));
    expect(screen.getByTestId('dupes-selected-count')).toHaveTextContent('1 selected');
    await user.click(screen.getByRole('button', { name: 'Go to page 2' }));
    await screen.findByTestId('dupes-cluster-x51');
    expect(screen.getByTestId('dupes-selected-count')).toHaveTextContent('0 selected');
    expect(toast).toHaveBeenCalledWith(expect.stringContaining('Selection cleared'), 'info');
  });

  it('ticking one card selects its pair, which the bulk bar then counts', async () => {
    const user = userEvent.setup();
    renderPanel();
    await screen.findByTestId('dupes-cluster-x1');
    await user.click(screen.getByRole('checkbox', { name: /Select cluster Book X2/ }));
    await user.click(screen.getByRole('checkbox', { name: /Select cluster Book X3/ }));
    expect(screen.getByTestId('dupes-selected-count')).toHaveTextContent('2 selected');
  });

  it('By series lists the series, and link-all asks for confirmation naming the count', async () => {
    const user = userEvent.setup();
    renderPanel();
    await screen.findByTestId('dupes-cluster-x1');
    expect(api.listDedupCandidateSeries).not.toHaveBeenCalled();

    await user.click(screen.getByRole('switch', { name: 'By series' }));
    const row = await screen.findByTestId('dupes-series-7');
    expect(row).toHaveTextContent('Series A');
    expect(row).toHaveTextContent('2 clusters');
    expect(row).toHaveTextContent('6 pending pairs');
    expect(row).toHaveTextContent('5 books');
    expect(screen.queryByTestId('dupes-cluster-spine')).not.toBeInTheDocument();

    await user.click(within(row).getByRole('button', { name: 'Link all pending pairs in this series' }));
    const dialog = await screen.findByTestId('dupes-series-confirm');
    expect(dialog).toHaveTextContent('Link all 6 pending pairs in Series A?');
    expect(api.linkDedupCandidateSeries).not.toHaveBeenCalled();

    await user.click(within(dialog).getByTestId('dupes-series-confirm-btn'));
    await waitFor(() => expect(api.linkDedupCandidateSeries).toHaveBeenCalledWith(7));
    await waitFor(() =>
      expect(toast).toHaveBeenCalledWith('Series link: 2 cluster(s), 5 book(s) linked', 'success')
    );
  });

  it('By series cancel sends nothing', async () => {
    const user = userEvent.setup();
    renderPanel();
    await screen.findByTestId('dupes-cluster-x1');
    await user.click(screen.getByRole('switch', { name: 'By series' }));
    const row = await screen.findByTestId('dupes-series-7');
    await user.click(within(row).getByRole('button', { name: /Link all pending pairs/ }));
    await user.click(within(await screen.findByTestId('dupes-series-confirm')).getByRole('button', { name: 'Cancel' }));
    expect(api.linkDedupCandidateSeries).not.toHaveBeenCalled();
  });

  it('By series shows an empty state and an error state', async () => {
    const user = userEvent.setup();
    vi.mocked(api.listDedupCandidateSeries).mockResolvedValueOnce([]);
    renderPanel();
    await screen.findByTestId('dupes-cluster-x1');
    await user.click(screen.getByRole('switch', { name: 'By series' }));
    expect(await screen.findByText('No series have pending duplicate clusters.')).toBeInTheDocument();

    vi.mocked(api.listDedupCandidateSeries).mockRejectedValue(new Error('series broke'));
    await user.click(screen.getByRole('switch', { name: 'By series' }));
    await user.click(screen.getByRole('switch', { name: 'By series' }));
    expect(await screen.findByTestId('dupes-series-error')).toHaveTextContent('series broke');
  });

  const inSeries = (c: api.DedupCandidate) =>
    ({
      ...c,
      book_a: { ...c.book_a, series_id: 7 },
      book_b: { ...c.book_b, series_id: 7 },
    }) as api.DedupCandidate;

  function mockSeriesPairs() {
    vi.mocked(api.getDedupCandidates).mockImplementation(async (params) => {
      // The server filters by series_id; the client asks, it does not scan.
      if (params?.series_id === 7) {
        return { candidates: [inSeries(cand(1)), inSeries(cand(2))], total: 2 };
      }
      return { candidates: [cand(1), cand(2), cand(3)], total: 3 };
    });
  }

  it('the pair count opens the pairs, with Compare', async () => {
    const user = userEvent.setup();
    mockSeriesPairs();
    renderPanel();
    await screen.findByTestId('dupes-cluster-x1');
    await user.click(screen.getByRole('switch', { name: 'By series' }));
    await user.click(await screen.findByTestId('dupes-series-pairs-7'));
    const list = await screen.findByTestId('dupes-series-detail-pairs');
    expect(within(list).getAllByRole('listitem')).toHaveLength(2);
    expect(list).toHaveTextContent('Book X1 / Book Y1');
    expect(api.getDedupCandidates).toHaveBeenCalledWith(
      expect.objectContaining({ series_id: 7, status: 'pending', limit: 100, offset: 0 }),
      expect.anything()
    );
  });

  it('the cluster count opens the clusters, built from every pair of the series', async () => {
    const user = userEvent.setup();
    mockSeriesPairs();
    renderPanel();
    await screen.findByTestId('dupes-cluster-x1');
    await user.click(screen.getByRole('switch', { name: 'By series' }));
    await user.click(await screen.findByTestId('dupes-series-clusters-7'));
    const list = await screen.findByTestId('dupes-series-detail-clusters');
    // Pairs x1-y1 and x2-y2 share no book: two clusters of two books.
    expect(within(list).getAllByRole('listitem')).toHaveLength(2);
    expect(list).toHaveTextContent('2 books: Book X1 / Book Y1');
    expect(screen.getByTestId('dupes-series-detail-count')).toHaveTextContent('2 clusters');
    // It asked for the whole series in one request, not a page of it.
    expect(api.getDedupCandidates).toHaveBeenCalledWith(
      expect.objectContaining({ series_id: 7, limit: 5000 }),
      expect.anything()
    );
    expect(screen.queryByTestId('dupes-series-detail-pairs')).not.toBeInTheDocument();
  });

  it('the book count opens the books', async () => {
    const user = userEvent.setup();
    mockSeriesPairs();
    renderPanel();
    await screen.findByTestId('dupes-cluster-x1');
    await user.click(screen.getByRole('switch', { name: 'By series' }));
    await user.click(await screen.findByTestId('dupes-series-books-7'));
    const list = await screen.findByTestId('dupes-series-detail-books');
    expect(within(list).getAllByRole('link')).toHaveLength(4);
    expect(screen.getByTestId('dupes-series-detail-count')).toHaveTextContent('4 books');
  });

  it('the series pairs list pages: it shows the total and loads more on request', async () => {
    const user = userEvent.setup();
    vi.mocked(api.getDedupCandidates).mockImplementation(async (params) => {
      if (params?.series_id === 7) {
        const n = Math.min(params.limit ?? 100, 150);
        return { candidates: Array.from({ length: n }, (_, i) => cand(i + 1)), total: 150 };
      }
      if (params?.entity_id) return { candidates: [], total: 0 };
      return { candidates: [cand(1), cand(2), cand(3)], total: 3 };
    });
    renderPanel();
    await screen.findByTestId('dupes-cluster-x1');
    await user.click(screen.getByRole('switch', { name: 'By series' }));
    await user.click(await screen.findByTestId('dupes-series-pairs-7'));
    expect(await screen.findByTestId('dupes-series-detail-count')).toHaveTextContent(
      'Showing 100 of 150 pending pairs'
    );
    await user.click(screen.getByTestId('dupes-series-detail-more'));
    await waitFor(() =>
      expect(screen.getByTestId('dupes-series-detail-count')).toHaveTextContent(
        'Showing 150 of 150 pending pairs'
      )
    );
    expect(screen.queryByTestId('dupes-series-detail-more')).not.toBeInTheDocument();
  });

  it('the per-book other-pages links carry the active filters', async () => {
    const user = userEvent.setup();
    vi.mocked(api.getDedupBookPairCounts).mockImplementation(async (req) =>
      req.clusters.map((cl) => ({
        key: cl.key,
        other_pairs: 3,
        per_book: [{ book_id: cl.book_ids[0], count: 3 }],
      }))
    );
    renderPanel('embedding');
    const line = await screen.findByTestId('cluster-other-pages-x1');
    expect(line).toHaveTextContent('3 more pairs for these books are on other pages');
    await user.click(line);
    expect(
      within(await screen.findByTestId('cluster-detail-other')).getByRole('link', { name: 'Book X1' })
    ).toHaveAttribute('href', '/review?lane=dupes&book=x1&layer=embedding&band=HIGH');
  });
});
