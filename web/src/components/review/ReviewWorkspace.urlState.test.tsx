// file: web/src/components/review/ReviewWorkspace.urlState.test.tsx
// version: 1.1.0
// guid: c7a05e2b-19d4-4f83-a6b0-3e8d51f9c274
// last-edited: 2026-11-05

/**
 * The Review workspace's deep links: the URL seeds the screen, what you click is
 * written back with `replace`, a lane switch is still ONE fetch with the mirror
 * running, and a URL change the workspace did not make (a link, back/forward)
 * moves the screen.
 *
 * A file of its own, for the same reason as the authors and AI-scans files: the
 * main workspace test is a hotspot every lane edits.
 */

import { useEffect } from 'react';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation, useNavigate, useNavigationType } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../services/api';
import { serveReviewList } from '../../test/reviewServer';
import { repairsRowsPage } from '../../test/repairsFixtures';
import { ReviewWorkspace } from './ReviewWorkspace';
import { resetDedupPipelineForTests } from './useDedupPipeline';
import { ToastProvider } from '../toast/ToastProvider';
import * as reads from './lanes/authorsReads';

vi.mock('../../services/api');
vi.mock('./lanes/authorsReads');

// Every location the page rendered, in order: how many URLs a gesture wrote.
const visited: string[] = [];

function Probe() {
  const location = useLocation();
  const type = useNavigationType();
  useEffect(() => {
    visited.push(`${location.pathname}${location.search}`);
  }, [location]);
  return (
    <>
      <div data-testid="location">{`${location.pathname}${location.search}`}</div>
      <div data-testid="nav-type">{type}</div>
    </>
  );
}

function Links() {
  const navigate = useNavigate();
  return (
    <>
      <button onClick={() => navigate('/review?lane=repairs&fixer=fx-a&filter=skipped')}>
        link-repairs
      </button>
      <button onClick={() => navigate('/review?lane=authors&view=series')}>link-series</button>
      <button onClick={() => navigate('/review')}>link-bare</button>
      <button onClick={() => navigate('/review?lane=repairs&fixer=nope')}>link-bad-fixer</button>
      <button onClick={() => navigate(-1)}>history-back</button>
    </>
  );
}

function renderWorkspace(entry: string) {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <ToastProvider>
        <ReviewWorkspace />
        <Probe />
        <Links />
      </ToastProvider>
    </MemoryRouter>
  );
}

const loc = () => screen.getByTestId('location').textContent;
// Let every effect and the mirror's trailing write finish before a fetch count is
// read: a duplicate fetch caused by the URL echo arrives after the click, not with it.
const settle = () =>
  act(async () => {
    await new Promise((r) => setTimeout(r, 600));
  });
// The metadata lane with no rows renders an empty state, not the spine, so the
// selected lane tab is the readiness signal.
const metadataReady = () =>
  waitFor(() =>
    expect(screen.getByTestId('lane-tab-metadata')).toHaveAttribute('aria-selected', 'true')
  );

const plan = (id: string) => ({
  operation_id: `plan-${id}`,
  status: 'completed' as const,
  queued_at: '2026-10-01T00:00:00Z',
  total: 1,
  applicable: 1,
});

beforeEach(() => {
  vi.resetAllMocks();
  window.localStorage.clear();
  resetDedupPipelineForTests();
  vi.mocked(reads.fetchAuthorGroups).mockResolvedValue({
    groups: [
      {
        canonical: { id: 1, name: 'Bruce Wayne', created_at: '' },
        variants: [{ id: 101, name: 'B. Wayne', created_at: '' }],
        book_count: 2,
      },
      {
        canonical: { id: 2, name: 'Selina Kyle', created_at: '' },
        variants: [{ id: 201, name: 'S. Kyle', created_at: '' }],
        book_count: 4,
      },
    ],
    needsRefresh: false,
  });
  vi.mocked(reads.fetchSeriesGroups).mockResolvedValue({
    groups: [],
    totalSeries: 0,
    needsRefresh: false,
  });
  vi.mocked(api.getDedupCandidates).mockResolvedValue({ candidates: [], total: 0 });
  vi.mocked(api.getDedupStats).mockResolvedValue({ stats: [] });
  serveReviewList({
    results: [],
    total_count: 0,
    matched: 0,
    no_match: 0,
    errors: 0,
    stale: 0,
  });
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
  vi.mocked(api.listRepairFixers).mockResolvedValue([
    { id: 'fx-a', title: 'Fixer A', description: '', last_plan: plan('a') },
    { id: 'fx-b', title: 'Fixer B', description: '', last_plan: plan('b') },
  ]);
  vi.mocked(api.getRepairPlanRows).mockImplementation(async (fixerId, planOpId, q) =>
    repairsRowsPage({
      plan_op_id: planOpId,
      fixer_id: fixerId,
      planned_at: '2026-10-01T00:00:00Z',
      filter: q.filter,
      offset: q.offset,
      limit: q.limit,
      total: 0,
      applicable: 0,
      skipped_by_kind: {},
      rows: [],
    })
  );
  vi.mocked(api.getDedupCandidateBreakdown).mockResolvedValue({
    candidate: { id: 42 } as api.DedupCandidate,
    book_a: null,
    book_b: null,
  });
});

describe('lane', () => {
  it('a lane click writes ?lane= with replace, and the default lane leaves the URL', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review');
    await metadataReady();
    await user.click(screen.getByTestId('lane-tab-regroup'));
    await screen.findByTestId('regroup-rail');
    expect(loc()).toBe('/review?lane=regroup');
    expect(screen.getByTestId('nav-type').textContent).toBe('REPLACE');
    await user.click(screen.getByTestId('lane-tab-metadata'));
    await waitFor(() => expect(loc()).toBe('/review'));
  });

  it('a lane switch is still ONE fetch per lane with the mirror running', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review');
    await metadataReady();
    await user.click(screen.getByTestId('lane-tab-repairs'));
    await screen.findByTestId('repairs-panel');
    await waitFor(() => expect(loc()).toBe('/review?lane=repairs'));
    await settle();
    expect(api.listRepairFixers).toHaveBeenCalledTimes(1);
    await user.click(screen.getByTestId('lane-tab-authors'));
    await screen.findByTestId('authors-panel');
    await waitFor(() => expect(loc()).toBe('/review?lane=authors'));
    await settle();
    expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(1);
    await user.click(screen.getByTestId('lane-tab-dupes'));
    await screen.findByTestId('dupes-rail');
    await waitFor(() => expect(loc()).toBe('/review?lane=dupes'));
    await settle();
    // The authors lane reads its own (author) candidates through the same call.
    const bookQueueReads = () =>
      vi.mocked(api.getDedupCandidates).mock.calls.filter((c) => c[0]?.entity_type !== 'author');
    expect(bookQueueReads()).toHaveLength(1);
    // Nothing refetched on the way past, and the URL echoes did not refetch either.
    expect(api.listRepairFixers).toHaveBeenCalledTimes(1);
    expect(reads.fetchAuthorGroups).toHaveBeenCalledTimes(1);
  });

  it('keeps ?band across lanes and then always names the lane, even metadata', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=dupes&band=HIGH');
    await screen.findByTestId('dupes-rail');
    await user.click(screen.getByTestId('lane-tab-metadata'));
    await metadataReady();
    // A bare `?band=HIGH` would reopen on the dupes lane.
    expect(loc()).toBe('/review?lane=metadata&band=HIGH');
  });

  it('the spine layout mode is written and read back', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=dupes');
    await screen.findByTestId('dupes-rail');
    await user.click(screen.getByRole('button', { name: 'Two columns' }));
    expect(loc()).toBe('/review?lane=dupes&mode=two-column');
  });
});

describe('repairs lane', () => {
  it('?fixer, ?filter and ?class seed the lane and stay in the address bar through the load', async () => {
    renderWorkspace('/review?lane=repairs&fixer=fx-b&filter=skipped&class=robin');
    await waitFor(() =>
      expect(api.getRepairPlanRows).toHaveBeenCalledWith(
        'fx-b',
        'plan-b',
        expect.objectContaining({ filter: 'skipped', rowClass: 'robin' }),
        expect.anything()
      )
    );
    // Exactly one rows read for the seeded view: no applicable-then-skipped flicker.
    await settle();
    expect(api.getRepairPlanRows).toHaveBeenCalledTimes(1);
    expect(loc()).toBe('/review?lane=repairs&fixer=fx-b&filter=skipped&class=robin');
  });

  it('an unknown filter falls back to applicable and is not sent to the server', async () => {
    renderWorkspace('/review?lane=repairs&fixer=fx-a&filter=bogus');
    await waitFor(() =>
      expect(api.getRepairPlanRows).toHaveBeenCalledWith(
        'fx-a',
        'plan-a',
        expect.objectContaining({ filter: 'applicable' }),
        expect.anything()
      )
    );
  });

  it('picking a fixer and a row tab writes them with replace; applicable leaves the URL', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=repairs&fixer=fx-a');
    await screen.findByTestId('repairs-fixer-fx-b');
    await user.click(screen.getByTestId('repairs-fixer-fx-b'));
    await waitFor(() => expect(loc()).toBe('/review?lane=repairs&fixer=fx-b'));
    expect(screen.getByTestId('nav-type').textContent).toBe('REPLACE');
    await user.click(await screen.findByTestId('repairs-tab-skipped'));
    await waitFor(() => expect(loc()).toBe('/review?lane=repairs&fixer=fx-b&filter=skipped'));
    await user.click(screen.getByTestId('repairs-tab-applicable'));
    await waitFor(() => expect(loc()).toBe('/review?lane=repairs&fixer=fx-b'));
  });

  it('leaving the lane removes its parameters', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=repairs&fixer=fx-a&filter=skipped');
    await screen.findByTestId('repairs-fixer-fx-a');
    await user.click(screen.getByTestId('lane-tab-regroup'));
    await screen.findByTestId('regroup-rail');
    expect(loc()).toBe('/review?lane=regroup');
  });

  it('a link to another fixer and filter while on the page re-seeds, then back re-seeds again', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=repairs&fixer=fx-b');
    await screen.findByTestId('repairs-fixer-fx-a');
    await user.click(screen.getByText('link-repairs'));
    await waitFor(() =>
      expect(api.getRepairPlanRows).toHaveBeenCalledWith(
        'fx-a',
        'plan-a',
        expect.objectContaining({ filter: 'skipped' }),
        expect.anything()
      )
    );
    expect(loc()).toBe('/review?lane=repairs&fixer=fx-a&filter=skipped');
    await user.click(screen.getByText('history-back'));
    await waitFor(() =>
      expect(api.getRepairPlanRows).toHaveBeenLastCalledWith(
        'fx-b',
        'plan-b',
        expect.objectContaining({ filter: 'applicable' }),
        expect.anything()
      )
    );
    expect(loc()).toBe('/review?lane=repairs&fixer=fx-b');
  });
});

describe('dupes lane', () => {
  it('?layer seeds the lane with ONE queue read and is kept; a layer chip writes it', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=dupes&layer=embedding');
    await screen.findByTestId('dupes-rail');
    await waitFor(() => expect(api.getDedupCandidates).toHaveBeenCalledTimes(1));
    expect(vi.mocked(api.getDedupCandidates).mock.calls[0][0]).toMatchObject({
      layer: 'embedding',
    });
    await settle();
    expect(api.getDedupCandidates).toHaveBeenCalledTimes(1);
    expect(loc()).toBe('/review?lane=dupes&layer=embedding');
    await user.click(screen.getByTestId('layer-chip-llm'));
    await waitFor(() => expect(loc()).toBe('/review?lane=dupes&layer=llm'));
    await settle();
    // One read for the click, none for the URL echo.
    expect(api.getDedupCandidates).toHaveBeenCalledTimes(2);
    await user.click(screen.getByTestId('layer-chip-all'));
    await waitFor(() => expect(loc()).toBe('/review?lane=dupes'));
    await settle();
    expect(api.getDedupCandidates).toHaveBeenCalledTimes(3);
  });

  it("a band chip click writes ?band and leaves another lane's focus alone", async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=authors&author=201');
    const card = await screen.findByTestId('author-group-2');
    await waitFor(() => expect(card).toHaveAttribute('data-focused', 'true'));
    await user.click(screen.getByTestId('lane-tab-dupes'));
    await screen.findByTestId('dupes-rail');
    await user.click(screen.getByTestId('band-chip-HIGH'));
    await waitFor(() => expect(loc()).toBe('/review?lane=dupes&band=HIGH'));
    await user.click(screen.getByTestId('lane-tab-authors'));
    const again = await screen.findByTestId('author-group-2');
    expect(again).toHaveAttribute('data-focused', 'true');
    // `band` is not the mirror's to remove.
    await waitFor(() => expect(loc()).toBe('/review?lane=authors&band=HIGH&author=2'));
  });

  it('Clear on the one-book banner keeps the lane when the lane was only implied by ?book', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?book=book-1');
    await screen.findByTestId('dupes-deeplink-banner');
    await user.click(screen.getByRole('button', { name: 'Clear' }));
    await waitFor(() =>
      expect(screen.queryByTestId('dupes-deeplink-banner')).not.toBeInTheDocument()
    );
    expect(screen.getByTestId('dupes-rail')).toBeInTheDocument();
    // The screen is on dupes, so the address bar says so (it must not read a bare
    // /review, which would open the metadata lane).
    await waitFor(() => expect(loc()).toBe('/review?lane=dupes'));
  });

  it('?compare opens the compare drawer on ?ctab; a tab click and closing it write the URL', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=dupes&compare=42&ctab=breakdown');
    await screen.findByTestId('candidate-compare-drawer');
    await waitFor(() =>
      expect(api.getDedupCandidateBreakdown).toHaveBeenCalledWith(42, expect.anything())
    );
    expect(screen.getByTestId('drawer-tab-breakdown')).toHaveAttribute('aria-selected', 'true');
    expect(loc()).toBe('/review?lane=dupes&compare=42&ctab=breakdown');
    await user.click(screen.getByTestId('drawer-tab-metadata'));
    await waitFor(() => expect(loc()).toBe('/review?lane=dupes&compare=42&ctab=metadata'));
    await user.keyboard('{Escape}');
    await waitFor(() => expect(loc()).toBe('/review?lane=dupes'));
  });

  it('?view=labels seeds the sub-view, a sub-view click writes it, and an external link re-seeds', async () => {
    const user = userEvent.setup();
    vi.stubGlobal(
      'fetch',
      vi.fn((input: RequestInfo | URL) => {
        const url = String(input);
        const data = url.includes('/dedup/labels/stats')
          ? { total: 0, by_label: {}, by_source: {} }
          : { labels: [], total: 0, items: [] };
        return Promise.resolve({ ok: true, status: 200, json: () => Promise.resolve({ data }) });
      })
    );
    try {
      renderWorkspace('/review?lane=dupes&view=labels');
      await screen.findByTestId('dupes-labels-view');
      expect(loc()).toBe('/review?lane=dupes&view=labels');
      await user.click(screen.getByTestId('dupes-subview-suspicious'));
      await waitFor(() => expect(loc()).toBe('/review?lane=dupes&view=suspicious'));
      await user.click(screen.getByTestId('dupes-subview-queue'));
      await waitFor(() => expect(loc()).toBe('/review?lane=dupes'));
      // `view` is shared with the authors lane: a link to ITS value must not be
      // read as a dupes sub-view.
      await user.click(screen.getByText('link-series'));
      await waitFor(() =>
        expect(screen.getByTestId('authors-subview-series')).toHaveAttribute('aria-pressed', 'true')
      );
      expect(screen.getByTestId('authors-subview-series')).toHaveAttribute('aria-pressed', 'true');
    } finally {
      vi.unstubAllGlobals();
    }
  });
});

describe('authors lane', () => {
  it('?view=series seeds the sub-view and a link to it while elsewhere re-seeds', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=metadata');
    await metadataReady();
    await user.click(screen.getByText('link-series'));
    await waitFor(() =>
      expect(screen.getByTestId('authors-subview-series')).toHaveAttribute('aria-pressed', 'true')
    );
    expect(loc()).toBe('/review?lane=authors&view=series');
  });

  it('moving focus to an author writes ?author=, and the first-row fallback is not a choice', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=authors');
    await screen.findByTestId('author-group-1');
    // Nothing chosen yet: the first row shows focus but is not written.
    expect(loc()).toBe('/review?lane=authors');
    await user.keyboard('j');
    await waitFor(() => expect(loc()).toBe('/review?lane=authors&author=2'));
    expect(screen.getByTestId('author-group-2')).toHaveAttribute('data-focused', 'true');
  });
});

describe('the address bar and the screen never disagree', () => {
  it('a bare /review link while on repairs leaves the screen there and says so in the URL', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=repairs&fixer=fx-a');
    await screen.findByTestId('repairs-fixer-fx-a');
    await user.click(screen.getByText('link-bare'));
    await waitFor(() => expect(loc()).toBe('/review?lane=repairs&fixer=fx-a'));
    expect(screen.getByTestId('lane-tab-repairs')).toHaveAttribute('aria-selected', 'true');
  });

  it('an unknown fixer in a link is not left in the address bar', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=repairs&fixer=fx-a');
    await screen.findByTestId('repairs-fixer-fx-a');
    await user.click(screen.getByText('link-bad-fixer'));
    await waitFor(() => expect(loc()).toBe('/review?lane=repairs&fixer=fx-a'));
  });

  it('an unknown ?band= is removed, and a known one is kept', async () => {
    const { unmount } = renderWorkspace('/review?lane=dupes&band=NONSENSE');
    await screen.findByTestId('dupes-rail');
    await waitFor(() => expect(loc()).toBe('/review?lane=dupes'));
    unmount();
    renderWorkspace('/review?lane=dupes&band=HIGH');
    await screen.findByTestId('dupes-rail');
    await settle();
    expect(loc()).toBe('/review?lane=dupes&band=HIGH');
  });

  it('a band click straight after a layer click does not revert the layer', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=dupes');
    await screen.findByTestId('dupes-rail');
    await user.click(screen.getByTestId('layer-chip-llm'));
    await user.click(screen.getByTestId('band-chip-HIGH'));
    await waitFor(() => expect(loc()).toBe('/review?lane=dupes&layer=llm&band=HIGH'));
    await settle();
    expect(loc()).toBe('/review?lane=dupes&layer=llm&band=HIGH');
    expect(screen.getByTestId('layer-chip-llm')).toBeInTheDocument();
    // The layer is still applied: one read for the seed, one for the layer.
    const last = vi.mocked(api.getDedupCandidates).mock.calls.at(-1)?.[0];
    expect(last).toMatchObject({ layer: 'llm', band: 'HIGH' });
  });

  it('holding j writes a few URLs, not one per key repeat, and ends on the right author', async () => {
    const user = userEvent.setup();
    vi.mocked(reads.fetchAuthorGroups).mockResolvedValue({
      groups: Array.from({ length: 12 }, (_, i) => ({
        canonical: { id: i + 1, name: `Gotham Resident ${i + 1}`, created_at: '' },
        variants: [{ id: 100 + i, name: `G. Resident ${i + 1}`, created_at: '' }],
        book_count: 2,
      })),
      needsRefresh: false,
    });
    renderWorkspace('/review?lane=authors');
    await screen.findByTestId('author-group-1');
    visited.length = 0;
    for (let i = 0; i < 10; i++) await user.keyboard('j');
    await waitFor(() => expect(loc()).toBe('/review?lane=authors&author=11'));
    // Ten focus moves, a leading write and a trailing one (plus at most one more
    // when the throttle window rolled over mid-burst), never one per move.
    expect(visited.filter((v) => v.includes('author=')).length).toBeLessThanOrEqual(3);
  });
});
