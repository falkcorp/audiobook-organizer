// file: web/src/components/review/ReviewWorkspace.authors.test.tsx
// version: 1.2.2
// guid: 0b6d94c3-f7a2-4e18-9d05-62c8e1a7b350
// last-edited: 2026-10-18

/**
 * The Authors & series lane inside the workspace: how it is reached, what the
 * switcher shows, and that it is silent while another lane is showing.
 *
 * A file of its own rather than a block in ReviewWorkspace.test.tsx, which is a
 * hotspot every lane edits; this keeps the lane's wiring tests out of that
 * merge conflict.
 */

import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../services/api';
import { ReviewWorkspace } from './ReviewWorkspace';
import { ToastProvider } from '../toast/ToastProvider';
import * as reads from './lanes/authorsReads';
import { QueryWrapper } from '../../test/queryWrapper';

vi.mock('../../services/api');
vi.mock('./lanes/authorsReads');

function LocationProbe() {
  const location = useLocation();
  return <div data-testid="location">{`${location.pathname}${location.search}`}</div>;
}

function renderWorkspace(entry: string) {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <ToastProvider>
        <ReviewWorkspace />
        <LocationProbe />
      </ToastProvider>
    </MemoryRouter>,
    { wrapper: QueryWrapper }
  );
}

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(reads.fetchAuthorGroups).mockResolvedValue({
    groups: [
      {
        canonical: { id: 1, name: 'Author A', created_at: '' },
        variants: [{ id: 101, name: 'Author A.', created_at: '' }],
        book_count: 2,
      },
      {
        canonical: { id: 2, name: 'Author B', created_at: '' },
        variants: [{ id: 201, name: 'Author B.', created_at: '' }],
        book_count: 4,
      },
    ],
    needsRefresh: false,
  });
  vi.mocked(reads.fetchSeriesGroups).mockResolvedValue({
    groups: [
      {
        name: 'Series A',
        count: 2,
        match_type: 'exact',
        series: [
          { id: 11, name: 'Series A', created_at: '' },
          { id: 12, name: 'series a', created_at: '' },
        ],
      },
    ],
    totalSeries: 2,
    needsRefresh: false,
  });
  vi.mocked(api.getDedupCandidates).mockResolvedValue({ candidates: [], total: 0 });
  vi.mocked(api.getDedupStats).mockResolvedValue({ stats: [] });
  vi.mocked(api.getCachedReviewResults).mockResolvedValue({
    results: [],
    total_count: 0,
    matched: 0,
    no_match: 0,
    errors: 0,
    stale: 0,
  } as unknown as Awaited<ReturnType<typeof api.getCachedReviewResults>>);
  vi.mocked(api.getReviewItems).mockResolvedValue({ items: [], count: 0, limit: 500, offset: 0, total: 0 });
  vi.mocked(api.getReviewCount).mockResolvedValue({
    applying: 0,
    needs_attention: 0,
    count: 0,
    by_kind: {},
  });
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '' } as api.Config);
  vi.mocked(api.listRepairFixers).mockResolvedValue([]);
});

describe('Authors & series in the workspace', () => {
  it('the lane switcher lists Duplicates, Metadata, Review queue, Authors & series, Repairs, in that order', async () => {
    renderWorkspace('/review?lane=metadata');
    const tabs = await screen.findAllByRole('tab');
    expect(tabs.map((t) => t.textContent)).toEqual([
      'Duplicates',
      'Metadata',
      'Review queue',
      'Authors & series',
      'Repairs',
    ]);
  });

  it('?lane=authors opens the lane with its Authors sub-view, groups and candidates section', async () => {
    renderWorkspace('/review?lane=authors');
    expect(await screen.findByTestId('authors-panel')).toBeInTheDocument();
    expect(screen.getByTestId('authors-subview-authors')).toHaveTextContent('Authors');
    expect(await screen.findByRole('heading', { name: 'Author A' })).toBeInTheDocument();
    expect(screen.getByTestId('author-candidates')).toBeInTheDocument();
    // Not the fall-through metadata lane, and no comparison-layout toggle: the
    // lane has one layout.
    expect(screen.queryByTestId('compare-spine')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Compact rows' })).not.toBeInTheDocument();
  });

  it('is silent while another lane is showing, and stops when it is left', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=regroup');
    await screen.findByTestId('regroup-rail');
    expect(reads.fetchAuthorGroups).not.toHaveBeenCalled();
    expect(vi.mocked(api.getDedupCandidates).mock.calls.filter((c) => c[0]?.entity_type === 'author')).toHaveLength(0);

    await user.click(screen.getByTestId('lane-tab-authors'));
    await screen.findByRole('heading', { name: 'Author A' });
    expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(1);
    const signal = vi.mocked(reads.fetchAuthorGroups).mock.calls[0][0];
    expect(signal.aborted).toBe(false);

    await user.click(screen.getByTestId('lane-tab-dupes'));
    await screen.findByTestId('dupes-rail');
    expect(signal.aborted).toBe(true);
    expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(1);
  });

  it('does not write the lane back to the URL', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=authors');
    await screen.findByTestId('authors-panel');
    await user.click(screen.getByTestId('lane-tab-regroup'));
    expect(await screen.findByTestId('regroup-rail')).toBeInTheDocument();
    expect(within(screen.getByTestId('review-workspace')).queryByTestId('authors-panel')).not.toBeInTheDocument();
    // `?lane=` is how you ARRIVE at a lane, not a record of the one you are on:
    // moving to another lane leaves the URL exactly as it was.
    expect(screen.getByTestId('location')).toHaveTextContent('/review?lane=authors');
  });

  it("?author= focuses that author's group, not just the first one", async () => {
    renderWorkspace('/review?lane=authors&author=201');
    const card = await screen.findByTestId('author-group-2');
    await waitFor(() => expect(card).toHaveAttribute('data-focused', 'true'));
    expect(screen.getByTestId('author-group-1')).not.toHaveAttribute('data-focused');
  });

  it('?lane=authors&view=series opens the Series sub-view once; the Authors sub-view is one click away and nothing is written back', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=authors&view=series');
    expect(await screen.findByTestId('series-panel')).toBeInTheDocument();
    expect(await screen.findByTestId('series-group-s11-12')).toBeInTheDocument();
    expect(screen.getByTestId('authors-subview-series')).toHaveAttribute('aria-pressed', 'true');
    expect(reads.fetchAuthorGroups).not.toHaveBeenCalled();
    expect(reads.fetchSeriesGroups).toHaveBeenCalledTimes(1);

    await user.click(screen.getByTestId('authors-subview-authors'));
    expect(await screen.findByRole('heading', { name: 'Author A' })).toBeInTheDocument();
    expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId('location')).toHaveTextContent('/review?lane=authors&view=series');
  });

  it('?view=series is for the authors lane only: it does not open Series when the URL landed on another lane', async () => {
    const user = userEvent.setup();
    // `view` is also the Duplicates lane's parameter.
    renderWorkspace('/review?lane=dupes&view=series');
    await screen.findByTestId('dupes-rail');
    await user.click(screen.getByTestId('lane-tab-authors'));
    expect(await screen.findByTestId('authors-panel')).toBeInTheDocument();
    expect(await screen.findByRole('heading', { name: 'Author A' })).toBeInTheDocument();
    expect(reads.fetchSeriesGroups).not.toHaveBeenCalled();
  });

  it('the lane makes no series request while another lane is showing, and stops when the lane is left', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=authors&view=series');
    await screen.findByTestId('series-group-s11-12');
    const signal = vi.mocked(reads.fetchSeriesGroups).mock.calls[0][0];
    expect(signal.aborted).toBe(false);
    await user.click(screen.getByTestId('lane-tab-regroup'));
    await screen.findByTestId('regroup-rail');
    expect(signal.aborted).toBe(true);
    expect(reads.fetchSeriesGroups).toHaveBeenCalledTimes(1);
  });
});
