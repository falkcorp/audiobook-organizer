// file: web/src/components/review/ReviewWorkspace.authors.test.tsx
// version: 1.0.0
// guid: 0b6d94c3-f7a2-4e18-9d05-62c8e1a7b350
// last-edited: 2026-10-13

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
    </MemoryRouter>
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
    ],
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
  vi.mocked(api.getReviewCount).mockResolvedValue({ count: 0, by_kind: {} });
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

  it('?author= focuses that author\'s group', async () => {
    renderWorkspace('/review?lane=authors&author=101');
    const card = await screen.findByTestId('author-group-1');
    await waitFor(() => expect(card).toHaveAttribute('data-focused', 'true'));
  });
});
