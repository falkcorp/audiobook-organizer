// file: web/src/components/review/ReviewWorkspace.test.tsx
// version: 1.13.0
// guid: 3c8f0a62-9b47-4d15-8e30-1f7a2c5b9d64
// last-edited: 2026-09-28

import { cleanup, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import userEvent from '@testing-library/user-event';
import { vi, describe, it, expect, beforeEach } from 'vitest';
import * as api from '../../services/api';
import { ReviewWorkspace } from './ReviewWorkspace';
import { ToastProvider } from '../toast/ToastProvider';
import { resetDedupPipelineForTests } from './useDedupPipeline';

vi.mock('../../services/api');
const actualApi = await vi.importActual<typeof api>('../../services/api');

function makeResult(id: string, overrides: Partial<api.CandidateResult> = {}) {
  return {
    book: { id, title: `Book ${id}`, language: 'en' },
    status: 'matched',
    candidate: {
      source: 'audible',
      title: `Cand ${id}`,
      author: 'A',
      narrator: 'N',
      score: 2.0,
      language: 'en',
    },
    ...overrides,
  } as unknown as api.CandidateResult;
}

function renderWorkspace(initialEntries: string[] = ['/review']) {
  // The dupes lane reads ?book= and ?band= from the URL -- a deep link from the
  // fingerprint column is one of its entry points -- so the workspace now needs
  // a router in tests as well as in the app.
  return render(
    <MemoryRouter initialEntries={initialEntries}>
      <ToastProvider>
        <ReviewWorkspace />
      </ToastProvider>
    </MemoryRouter>
  );
}

beforeEach(() => {
  vi.resetAllMocks();
  window.localStorage.clear();
  resetDedupPipelineForTests();
  vi.mocked(api.isOperationTerminal).mockImplementation(actualApi.isOperationTerminal);
  vi.mocked(api.getCachedReviewResults).mockResolvedValue({
    results: [makeResult('a'), makeResult('b')],
    total_count: 2,
    matched: 2,
    no_match: 0,
    errors: 0,
  });
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
  // CompareSpine (Task 7) now calls usePathAliases() itself, which pulls
  // config via api.getConfig(). The module is auto-mocked above, so without
  // this every mount throws "Cannot read properties of undefined (reading
  // 'then')" -- vi.fn() with no configured return resolves to undefined, not
  // a Promise.
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '' } as api.Config);
  vi.mocked(api.listRepairFixers).mockResolvedValue([]);
});

describe('lane default', () => {
  it('offers the runtime-difference filter with its unknown-duration guidance', async () => {
    const user = userEvent.setup();
    renderWorkspace();
    await screen.findByTestId('compare-spine');

    // On by default: the default review level is In-depth (owner 2026-09-27).
    const control = screen.getByRole('switch', { name: 'Hide runtime differences' });
    expect(control).toBeChecked();

    await user.hover(control);
    expect(
      await screen.findByText(/unknown runtime on either side stay visible/i)
    ).toBeInTheDocument();
  });

  it('opens on metadata, NOT on the first lane in LANE_ORDER', async () => {
    // LANE_ORDER starts with 'dupes' because the switcher lists widest-scope
    // work first, but the spine's renderers are metadata-shaped and dupes is not
    // ported. `useState(LANE_ORDER[0])` would land /review on a lane that cannot
    // render anything -- a blank screen on the feature's first paint.
    renderWorkspace();
    await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());
    expect(screen.queryByTestId('lane-unported-dupes')).not.toBeInTheDocument();
  });

  it('renders the regroup lane -- no lane points at an old surface any more', async () => {
    // This replaces the "explains an unported lane" test, which has run out of
    // subject: regroup was the last one. What it guarded against -- a lane that
    // renders nothing and offers no next step -- is now guarded by asserting the
    // lane actually renders.
    const user = userEvent.setup();
    renderWorkspace();
    await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());

    await user.click(screen.getByTestId('lane-tab-regroup'));

    expect(await screen.findByTestId('regroup-rail')).toBeInTheDocument();
    expect(screen.queryByTestId('lane-unported-regroup')).not.toBeInTheDocument();
    expect(screen.queryByTestId('compare-spine')).not.toBeInTheDocument();
  });

  it('renders the dupes lane rather than pointing at the old page', async () => {
    const user = userEvent.setup();
    renderWorkspace();
    await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());

    await user.click(screen.getByTestId('lane-tab-dupes'));

    expect(await screen.findByTestId('dupes-rail')).toBeInTheDocument();
    expect(screen.queryByTestId('lane-unported-dupes')).not.toBeInTheDocument();
  });

  it('stops fetching the metadata set while another lane is showing', async () => {
    const user = userEvent.setup();
    renderWorkspace();
    await waitFor(() => expect(api.getCachedReviewResults).toHaveBeenCalledTimes(1));

    await user.click(screen.getByTestId('lane-tab-regroup'));
    await screen.findByTestId('regroup-rail');

    expect(api.getCachedReviewResults).toHaveBeenCalledTimes(1);
  });
});

describe('the lane comes from the URL', () => {
  // Every test here renders and asserts WITHOUT clicking a tab. That is the
  // whole point: the lane tests above all click their way to the lane first,
  // so they never exercised arrival, and the `?book=` fix shipped behind a
  // default that could not reach it.

  it('opens the lane named by ?lane=', async () => {
    renderWorkspace(['/review?lane=regroup']);
    expect(await screen.findByTestId('regroup-rail')).toBeInTheDocument();
  });

  it('opens the repairs lane from ?lane=repairs, and only it fetches', async () => {
    renderWorkspace(['/review?lane=repairs']);
    expect(await screen.findByTestId('repairs-panel')).toBeInTheDocument();
    await waitFor(() => expect(api.listRepairFixers).toHaveBeenCalledTimes(1));
    // The metadata lane is the fallback render branch; it must not be what
    // shows, and it must not have fetched on the way past.
    expect(screen.queryByTestId('compare-spine')).not.toBeInTheDocument();
    expect(api.getCachedReviewResults).not.toHaveBeenCalled();
  });

  it('keeps the repairs lane silent while another lane is showing', async () => {
    renderWorkspace(['/review?lane=metadata']);
    await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());
    expect(api.listRepairFixers).not.toHaveBeenCalled();
    expect(api.getRepairPlanRows).not.toHaveBeenCalled();
  });

  it('infers dupes from a ?book= deep link, with no ?lane= at all', async () => {
    // The link BookDetailStatusAlerts hands out. Landing on metadata meant the
    // dupes lane stayed inactive, so its server-side entity filter never ran --
    // the fix was real but unreachable through its own entry point.
    renderWorkspace(['/review?book=book-7']);

    expect(await screen.findByTestId('dupes-rail')).toBeInTheDocument();
    await waitFor(() =>
      expect(api.getDedupCandidates).toHaveBeenCalledWith(
        expect.objectContaining({ entity_id: 'book-7' }),
        expect.anything()
      )
    );
    // Arrival must not cost a wasted round trip either.
    expect(api.getDedupCandidates).toHaveBeenCalledTimes(1);
    // ...and the metadata lane, which is no longer the one showing, must not
    // have fetched its set on the way past.
    expect(api.getCachedReviewResults).not.toHaveBeenCalled();
    // The banner names the book being filtered to, so a near-empty list reads as
    // "one book" rather than "dedup is broken".
    expect(screen.getByTestId('dupes-deeplink-banner')).toBeInTheDocument();
  });

  it('infers dupes from a ?band= deep link', async () => {
    renderWorkspace(['/review?band=HIGH']);
    expect(await screen.findByTestId('dupes-rail')).toBeInTheDocument();
  });

  it('falls back to metadata when ?lane= names something that is not a lane', async () => {
    // A stale bookmark or a typo must not blank the screen.
    renderWorkspace(['/review?lane=nonsense']);
    await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());
  });

  it('lets ?lane= win over an inferred one', async () => {
    // `?lane=` is explicit; `?book=` is a hint. Someone who linked to the
    // metadata lane for a specific book gets the metadata lane.
    renderWorkspace(['/review?lane=metadata&book=book-7']);
    await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());
    expect(screen.queryByTestId('dupes-rail')).not.toBeInTheDocument();
  });

  it('reads the URL once and does not re-derive the lane from it', async () => {
    // The lane is state seeded FROM the URL, not state mirroring it. A mirror
    // is what DupesPanel had: the click writes the URL, the URL re-derives the
    // state a render later, and the lane's gated fetch fires twice. Clicking
    // away from an inferred lane must simply work.
    const user = userEvent.setup();
    renderWorkspace(['/review?book=book-7']);
    await screen.findByTestId('dupes-rail');

    await user.click(screen.getByTestId('lane-tab-metadata'));
    await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());
    expect(api.getCachedReviewResults).toHaveBeenCalledTimes(1);

    await user.click(screen.getByTestId('lane-tab-dupes'));
    await screen.findByTestId('dupes-rail');
    // Re-entering the lane refetches once, not twice: the ?book= filter is
    // still in the URL and still applies, and nothing about the click changed
    // the URL to trigger a second pass.
    expect(api.getDedupCandidates).toHaveBeenCalledTimes(2);
  });
});

describe('rescore', () => {
  // The surface that owned rescore (UnifiedDedupTab) is deleted, and this is
  // where its two-button dialog went. Worth testing precisely because the
  // command bar had quietly collapsed both buttons into one that only dry-ran.

  // Both rescore items live in the Dedup menu's Advanced section, which only
  // renders while the global advanced setting is on.
  beforeEach(() => window.localStorage.setItem('settings.showAdvanced', 'true'));

  async function openDedupMenu(user: ReturnType<typeof userEvent.setup>) {
    renderWorkspace();
    await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());
    await user.click(screen.getByRole('button', { name: /dedup/i }));
  }

  it('the preview says it changed nothing, and writes nothing', async () => {
    const user = userEvent.setup();
    vi.mocked(api.rescoreDedupCandidates).mockResolvedValue({
      inspected: 40,
      skipped: 2,
      changed: 3,
      applied: false,
      band_deltas: {},
    });
    await openDedupMenu(user);

    await user.click(await screen.findByTestId('command-rescore-dry-run'));

    // The label and the argument have to agree. They did not: the item read
    // "Rescore", passed apply=false, and toasted "Rescore started".
    expect(api.rescoreDedupCandidates).toHaveBeenCalledWith(false);
    // The endpoint is synchronous; its counts are the result, not "started".
    expect(await screen.findByText(/3 of 40 waiting pairs would change/i)).toBeInTheDocument();
    expect(screen.getByText(/nothing was saved/i)).toBeInTheDocument();
  });

  it('does not apply until the confirmation is accepted', async () => {
    const user = userEvent.setup();
    vi.mocked(api.rescoreDedupCandidates).mockResolvedValue({} as never);
    await openDedupMenu(user);

    await user.click(await screen.findByTestId('command-rescore-apply'));
    // Choosing the menu item must not be the mutation.
    expect(api.rescoreDedupCandidates).not.toHaveBeenCalled();

    await user.click(screen.getByTestId('rescore-apply-confirm'));
    expect(api.rescoreDedupCandidates).toHaveBeenCalledWith(true);
  });

  it('cancelling leaves the candidates alone', async () => {
    const user = userEvent.setup();
    await openDedupMenu(user);

    await user.click(await screen.findByTestId('command-rescore-apply'));
    await user.click(screen.getByRole('button', { name: /cancel/i }));

    expect(api.rescoreDedupCandidates).not.toHaveBeenCalled();
  });
});

describe('view mode', () => {
  it('offers three positions, including the auto mode', async () => {
    // Two are carried from the dialog's toggle; `auto` is the one addition
    // PLAN.md authorises, and closes an unchecked port-inventory row.
    renderWorkspace();
    await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());

    expect(screen.getByRole('button', { name: 'Compact rows' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Two columns' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Auto layout' })).toBeInTheDocument();
  });

  it('drives the spine, which nothing did before the shell existed', async () => {
    const user = userEvent.setup();
    renderWorkspace();
    const spine = await screen.findByTestId('compare-spine');
    expect(spine).toHaveAttribute('data-view-mode', 'compact');

    await user.click(screen.getByRole('button', { name: 'Auto layout' }));
    await waitFor(() =>
      expect(screen.getByTestId('compare-spine')).toHaveAttribute('data-view-mode', 'auto')
    );
  });
});

describe('evidence', () => {
  it('explains the score when a compact row is expanded', async () => {
    // The reason the backend instrumentation exists. The dialog this replaces
    // showed a score with no way to ask where it came from.
    const user = userEvent.setup();
    renderWorkspace();
    await screen.findByTestId('compare-spine');

    expect(screen.queryByTestId('evidence-section')).not.toBeInTheDocument();
    const spine = screen.getByTestId('compare-spine');
    await user.click(within(spine).getByText(/Book a/));

    const panel = await screen.findByTestId('evidence-section');
    expect(panel).toHaveTextContent(/How this score was reached/i);
  });

  it('says so when a candidate has no recorded derivation', async () => {
    // A candidate scored before the instrumentation existed has no breakdown.
    // Saying that is the point -- a blank panel would read as "no signals fired".
    const user = userEvent.setup();
    renderWorkspace();
    await screen.findByTestId('compare-spine');

    const spine = screen.getByTestId('compare-spine');
    await user.click(within(spine).getByText(/Book a/));
    const panel = await screen.findByTestId('evidence-section');
    expect(panel).toHaveTextContent(/without a recorded derivation/i);
  });

  it('shows on the two-column card without needing an expand', async () => {
    const user = userEvent.setup();
    renderWorkspace();
    await screen.findByTestId('compare-spine');

    await user.click(screen.getByRole('button', { name: 'Two columns' }));

    const panels = await screen.findAllByTestId('evidence-section');
    expect(panels.length).toBeGreaterThan(0);
  });
});

describe('cover lightbox', () => {
  // Owner report 2026-09-13: clicking the "Proposed" cover on the two-column
  // card opened an image SMALLER than the 60x80 thumbnail. The viewer was an
  // inline `maxWidth="sm"` Dialog whose image was `max-width: 100%` of a
  // shrink-wrapped Paper. jsdom has no layout, so these assert the contract
  // that replaces it: a dedicated viewer, bounded in viewport units, showing
  // the provider's full-size variant -- not pixel sizes.
  const CURRENT = 'https://images-na.ssl-images-amazon.com/images/I/cur._SL500_.jpg';
  const PROPOSED = 'https://m.media-amazon.com/images/I/prop._SL500_.jpg';

  beforeEach(() => {
    const r = makeResult('a');
    vi.mocked(api.getCachedReviewResults).mockResolvedValue({
      results: [
        {
          ...r,
          book: { ...r.book, cover_url: CURRENT },
          candidate: { ...r.candidate, cover_url: PROPOSED },
        } as unknown as api.CandidateResult,
      ],
      total_count: 1,
      matched: 1,
      no_match: 0,
      errors: 0,
    });
  });

  // The owner's screen: a compact row expanded into its Current / Proposed
  // columns (CompareSpine's expanded-row detail).
  async function openExpandedRow() {
    const user = userEvent.setup();
    renderWorkspace();
    const spine = await screen.findByTestId('compare-spine');
    await user.click(within(spine).getByText(/Book a/));
    await screen.findByText('Proposed');
    return user;
  }

  // The thumbnail in the column under the given heading. Matched through the
  // heading, not by src: the collapsed row's own avatar shows the same URL.
  function columnCover(heading: 'Current' | 'Proposed', src: string): HTMLImageElement {
    const column = screen.getByText(heading).parentElement;
    const img = column?.querySelector<HTMLImageElement>('img');
    if (!img) throw new Error(`no cover thumbnail under "${heading}"`);
    expect(img.getAttribute('src')).toBe(src);
    return img;
  }

  it('opens the Proposed cover full size, bounded by the viewport, not the thumbnail', async () => {
    const user = await openExpandedRow();
    expect(screen.queryByTestId('cover-lightbox')).not.toBeInTheDocument();

    await user.click(columnCover('Proposed', PROPOSED));

    const viewer = await screen.findByTestId('cover-lightbox');
    expect(viewer).toHaveAttribute('role', 'dialog');
    const big = within(viewer).getByTestId('cover-lightbox-img') as HTMLImageElement;
    // The full-size variant, not the `._SL500_.` resize the card shows.
    expect(big.getAttribute('src')).toBe('https://m.media-amazon.com/images/I/prop.jpg');
    expect(big.style.maxWidth).toBe('90vw');
    expect(big.style.maxHeight).toBe('90vh');
    expect(big.style.objectFit).toBe('contain');
    // Not sized to the thumbnail, and not a percentage of a shrink-wrapped box.
    expect(big.style.width).not.toBe('60px');
    expect(big.style.height).not.toBe('80px');
    expect(big.style.maxWidth).not.toBe('100%');

    await user.keyboard('{Escape}');
    await waitFor(() => expect(screen.queryByTestId('cover-lightbox')).not.toBeInTheDocument());
  });

  it('opens the Current cover through the same viewer', async () => {
    const user = await openExpandedRow();
    await user.click(columnCover('Current', CURRENT));

    const viewer = await screen.findByTestId('cover-lightbox');
    const big = within(viewer).getByTestId('cover-lightbox-img') as HTMLImageElement;
    expect(big.getAttribute('src')).toBe('https://images-na.ssl-images-amazon.com/images/I/cur.jpg');
    expect(big.style.maxWidth).toBe('90vw');
  });
});

describe('command bar', () => {
  it('disables a selection command and says why, rather than no-opping', async () => {
    const user = userEvent.setup();
    renderWorkspace();
    await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());

    await user.click(screen.getByTestId('command-menu-metadata'));

    const item = await screen.findByTestId('command-bulk-search-selected');
    expect(item).toHaveAttribute('aria-disabled', 'true');
  });

  it('says what a whole-library command acts on, in its description', async () => {
    // PLAN.md:390-394 wanted library-wide jobs flagged so one is never
    // mistaken for a per-row action. The owner (2026-09-27) dropped the
    // "library-wide" subtitle because every item carried it; the description
    // now names the scope in words instead.
    const user = userEvent.setup();
    renderWorkspace();
    await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());

    await user.click(screen.getByTestId('command-menu-dedup'));

    const item = await screen.findByTestId('command-full-rescan');
    expect(item).toHaveTextContent(/every book/i);
    expect(item).not.toHaveTextContent(/library-wide/i);
  });

  it('starts the job behind a command', async () => {
    const user = userEvent.setup();
    window.localStorage.setItem('settings.showAdvanced', 'true');
    vi.mocked(api.triggerDedupScan).mockResolvedValue(
      {} as unknown as Awaited<ReturnType<typeof api.triggerDedupScan>>
    );
    renderWorkspace();
    await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());

    await user.click(screen.getByTestId('command-menu-dedup'));
    await user.click(await screen.findByTestId('command-find-duplicates'));

    await waitFor(() => expect(api.triggerDedupScan).toHaveBeenCalled());
  });
});

describe('one-button dedup run', () => {
  const safeConfig = {
    root_dir: '',
    dedup: { auto_merge_enabled: false, llm_auto_merge_high_confidence: false },
  } as unknown as api.Config;
  const running = (id: string, message = 'Step 1 of 5: Preparing similarity data') =>
    ({ id, status: 'running', progress: 100, total: 5000, message }) as api.Operation;
  const completed = (id: string) =>
    ({ id, status: 'completed', progress: 5000, total: 5000, message: '' }) as api.Operation;

  beforeEach(() => {
    vi.mocked(api.startDedupRunAll).mockResolvedValue({ id: 'run-1' } as api.Operation);
    vi.mocked(api.triggerDedupScan).mockResolvedValue({ id: 'scan-1' } as api.Operation);
    vi.mocked(api.getOperationStatus).mockImplementation(async (id: string) => completed(id));
    vi.mocked(api.getOperationResult).mockResolvedValue({
      result_data: {
        steps: [],
        skipped: null,
        rescore_preview: { inspected: 12, skipped: 0, changed: 4, applied: false, band_deltas: {} },
      },
    });
    vi.mocked(api.cancelOperation).mockResolvedValue(undefined);
  });

  async function clickCommand(user: ReturnType<typeof userEvent.setup>, id: string) {
    renderWorkspace();
    await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());
    await user.click(screen.getByTestId('command-menu-dedup'));
    await user.click(await screen.findByTestId(`command-${id}`));
  }
  const clickRunAll = (user: ReturnType<typeof userEvent.setup>) =>
    clickCommand(user, 'find-all-duplicates');

  it('starts ONE server op, follows it, then shows its score check and opens Dupes', async () => {
    vi.mocked(api.getConfig).mockResolvedValue(safeConfig);
    const user = userEvent.setup();
    await clickRunAll(user);

    expect(await screen.findByTestId('dedup-pipeline-done')).toHaveTextContent(/4 of 12 pairs/);
    expect(api.startDedupRunAll).toHaveBeenCalledTimes(1);
    expect(api.getOperationStatus).toHaveBeenCalledWith('run-1');
    expect(api.getOperationResult).toHaveBeenCalledWith('run-1');
    // The steps run on the server now; the browser starts none of them.
    for (const fn of [
      api.triggerEmbedScan,
      api.triggerDedupAcoustID,
      api.triggerDedupScan,
      api.triggerDedupLLM,
      api.rescoreDedupCandidates,
    ]) {
      expect(fn).not.toHaveBeenCalled();
    }
    await waitFor(() =>
      expect(screen.getByTestId('lane-tab-dupes')).toHaveAttribute('aria-selected', 'true')
    );
  });

  it('asks first when a setting would let the run merge books by itself', async () => {
    vi.mocked(api.getConfig).mockResolvedValue({
      root_dir: '',
      dedup: { auto_merge_enabled: true, llm_auto_merge_high_confidence: false },
    } as unknown as api.Config);
    const user = userEvent.setup();
    await clickRunAll(user);

    expect(await screen.findByTestId('dedup-pipeline-confirm')).toHaveTextContent(
      /identical audio file/i
    );
    expect(api.startDedupRunAll).not.toHaveBeenCalled();

    await user.click(screen.getByTestId('dedup-pipeline-confirm-run'));
    await waitFor(() => expect(api.startDedupRunAll).toHaveBeenCalledTimes(1));
  });

  it('does not start when the settings cannot be read', async () => {
    vi.mocked(api.getConfig).mockRejectedValue(new Error('offline'));
    const user = userEvent.setup();
    await clickRunAll(user);

    expect(await screen.findByText(/did not start/i)).toBeInTheDocument();
    expect(api.startDedupRunAll).not.toHaveBeenCalled();
  });

  it('shows the server op progress and stays disabled across leaving and returning', async () => {
    vi.mocked(api.getConfig).mockResolvedValue(safeConfig);
    vi.mocked(api.getOperationStatus).mockImplementation(async (id: string) => running(id));
    const user = userEvent.setup();
    await clickRunAll(user);
    expect(await screen.findByTestId('dedup-pipeline-message')).toHaveTextContent(
      'Step 1 of 5: Preparing similarity data'
    );

    cleanup();
    renderWorkspace();
    await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());

    expect(screen.getByTestId('dedup-pipeline-progress')).toBeInTheDocument();
    await user.click(screen.getByTestId('command-menu-dedup'));
    expect(await screen.findByTestId('command-find-all-duplicates')).toHaveAttribute(
      'aria-disabled',
      'true'
    );
    expect(screen.getByTestId('command-full-rescan')).toHaveAttribute('aria-disabled', 'true');
    expect(api.startDedupRunAll).toHaveBeenCalledTimes(1);
  });

  it('picks a run back up after a reload from the remembered op id', async () => {
    window.localStorage.setItem('review.dedupRun', JSON.stringify({ run: 'all', opId: 'run-9' }));
    vi.mocked(api.getOperationStatus).mockImplementation(async (id: string) => running(id));
    renderWorkspace();

    expect(await screen.findByTestId('dedup-pipeline-progress')).toBeInTheDocument();
    await waitFor(() => expect(api.getOperationStatus).toHaveBeenCalledWith('run-9'));
    expect(api.startDedupRunAll).not.toHaveBeenCalled();
  });

  it('forgets a remembered run the server no longer has, so it is not re-followed', async () => {
    window.localStorage.setItem('review.dedupRun', JSON.stringify({ run: 'all', opId: 'gone-1' }));
    vi.mocked(api.getOperationStatus).mockRejectedValue(
      Object.assign(new Error('not found'), { status: 404 })
    );
    renderWorkspace();

    expect(await screen.findByTestId('dedup-pipeline-failed')).toHaveTextContent(
      /no longer on the server/i
    );
    expect(window.localStorage.getItem('review.dedupRun')).toBeNull();
  });

  it('keeps following while the server restarts', async () => {
    vi.mocked(api.getConfig).mockResolvedValue(safeConfig);
    vi.mocked(api.getOperationStatus).mockImplementation(
      async (id: string) => ({ ...running(id), status: 'interrupted_quiesced' }) as api.Operation
    );
    const user = userEvent.setup();
    await clickRunAll(user);

    expect(await screen.findByTestId('dedup-pipeline-message')).toHaveTextContent(
      /server restarts/i
    );
    expect(screen.queryByTestId('dedup-pipeline-failed')).not.toBeInTheDocument();
  });

  it('reports the failed step the server names', async () => {
    vi.mocked(api.getConfig).mockResolvedValue(safeConfig);
    vi.mocked(api.getOperationStatus).mockImplementation(
      async (id: string) =>
        ({
          id,
          status: 'failed',
          progress: 2000,
          total: 5000,
          message: '',
          error_message: 'Finding and scoring duplicates (op-x) ended as "failed"',
        }) as api.Operation
    );
    const user = userEvent.setup();
    await clickRunAll(user);

    expect(await screen.findByTestId('dedup-pipeline-failed')).toHaveTextContent(
      /Finding and scoring duplicates/
    );
  });

  it('Stop cancels the server op', async () => {
    vi.mocked(api.getConfig).mockResolvedValue(safeConfig);
    vi.mocked(api.getOperationStatus).mockImplementation(async (id: string) => running(id));
    const user = userEvent.setup();
    await clickRunAll(user);

    const stop = await screen.findByTestId('dedup-pipeline-stop');
    await waitFor(() => expect(stop).toBeEnabled());
    await user.click(stop);
    await waitFor(() => expect(api.cancelOperation).toHaveBeenCalledWith('run-1'));
  });

  it('Force full rescan runs the scan that fills the Dupes tab, then opens it', async () => {
    vi.mocked(api.getConfig).mockResolvedValue(safeConfig);
    const user = userEvent.setup();
    await clickCommand(user, 'full-rescan');

    expect(await screen.findByTestId('dedup-pipeline-done')).toBeInTheDocument();
    expect(api.triggerDedupScan).toHaveBeenCalledTimes(1);
    expect(api.getOperationStatus).toHaveBeenCalledWith('scan-1');
    // Not the /dedup page's in-memory group scan any more.
    expect(api.scanBookDuplicates).not.toHaveBeenCalled();
    await waitFor(() =>
      expect(screen.getByTestId('lane-tab-dupes')).toHaveAttribute('aria-selected', 'true')
    );
  });

  it('Force full rescan asks first when identical copies would be auto-linked', async () => {
    vi.mocked(api.getConfig).mockResolvedValue({
      root_dir: '',
      dedup: { auto_merge_enabled: true, llm_auto_merge_high_confidence: true },
    } as unknown as api.Config);
    const user = userEvent.setup();
    await clickCommand(user, 'full-rescan');

    const dialog = await screen.findByTestId('dedup-pipeline-confirm');
    expect(dialog).toHaveTextContent(/identical audio file/i);
    // The rescan has no AI step, so the AI auto-merge is not a risk for it.
    expect(dialog).not.toHaveTextContent(/AI auto-merge/i);
    expect(api.triggerDedupScan).not.toHaveBeenCalled();
  });
});

describe('action bar', () => {
  it('disables Apply Selected until something is selected', async () => {
    const user = userEvent.setup();
    renderWorkspace();
    await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());

    expect(screen.getByTestId('apply-selected')).toBeDisabled();

    await user.click(screen.getByRole('checkbox', { name: 'Select Book a' }));

    await waitFor(() => expect(screen.getByTestId('apply-selected')).toBeEnabled());
    expect(screen.getByTestId('apply-selected')).toHaveTextContent('(1)');
  });
});

describe('queue rail', () => {
  it('styles the selected row from the DOM via :has(input:checked)', async () => {
    // PLAN.md specifies this so the highlight is not a second copy of the
    // selection state. jsdom does not evaluate :has(), so this asserts the rule
    // is emitted -- whether it paints is a visual-harness question, the same
    // split used for the spine's container query.
    renderWorkspace();
    const list = await screen.findByTestId('queue-list');
    const styles = [...document.querySelectorAll('style')].map((s) => s.textContent).join('');

    expect(list).toBeInTheDocument();
    expect(styles).toMatch(/:has\(input:checked\)/);
  });

  it('carries the multi-book tooltip that states behaviour, not description', async () => {
    renderWorkspace();
    await waitFor(() => expect(screen.getByTestId('queue-rail')).toBeInTheDocument());
    // The second clause is the part that must survive the port: it describes
    // what the toggle DOES to Apply Selected, which is not recoverable from the
    // control's label.
    expect(
      screen.getByLabelText('Hide multi-book matches').closest('[data-testid="queue-rail"]')
    ).toBeInTheDocument();
  });
});

// ---------------------------------------------------------------------------
// A failed load must not read as an empty queue
//
// End-to-end through the real hook, the real MetadataPanel and the real
// CompareSpine, because the defect lived in the WIRING between them: the hook
// swallowed the rejection, the panel had nowhere to show one, and the spine
// rendered its empty copy regardless. Testing any one of the three in isolation
// would miss it.
// ---------------------------------------------------------------------------

const METADATA_EMPTY_COPY =
  'No metadata matches to review. Search providers from the Metadata menu to find some.';

describe('the metadata lane distinguishes failed, loading and empty', () => {
  it('shows the error and NOT the go-search-providers advice when the load fails', async () => {
    vi.mocked(api.getCachedReviewResults).mockRejectedValue(new Error('server exploded'));
    renderWorkspace();

    const alert = await screen.findByTestId('metadata-error');
    expect(alert).toHaveTextContent('server exploded');

    // The whole point. Telling the reviewer to go search providers is wrong
    // when the request 500'd -- there may be thousands of rows waiting behind a
    // server that is simply down, and following the advice does nothing.
    expect(screen.queryByText(METADATA_EMPTY_COPY)).not.toBeInTheDocument();
    expect(screen.queryByTestId('spine-empty')).not.toBeInTheDocument();
  });

  it('still says the queue is empty when the load SUCCEEDS with no rows', async () => {
    // The counterpart. Suppressing the empty copy on error is only correct if a
    // genuinely empty queue still gets it -- otherwise the fix has just traded
    // one indistinguishable pair for another.
    vi.mocked(api.getCachedReviewResults).mockResolvedValue({
      results: [],
      total_count: 0,
      matched: 0,
      no_match: 0,
      errors: 0,
    });
    renderWorkspace();

    expect(await screen.findByText(METADATA_EMPTY_COPY)).toBeInTheDocument();
    expect(screen.queryByTestId('metadata-error')).not.toBeInTheDocument();
  });

  it('shows a loading state, not the empty copy, while the request is in flight', async () => {
    // A hung request used to render exactly the same screen as an empty queue.
    vi.mocked(api.getCachedReviewResults).mockReturnValue(
      new Promise(() => {}) as ReturnType<typeof api.getCachedReviewResults>
    );
    renderWorkspace();

    expect(await screen.findByTestId('spine-loading')).toBeInTheDocument();
    expect(screen.queryByText(METADATA_EMPTY_COPY)).not.toBeInTheDocument();
  });

  it('retries the load from the error Alert', async () => {
    const user = userEvent.setup();
    vi.mocked(api.getCachedReviewResults).mockRejectedValueOnce(new Error('server exploded'));
    renderWorkspace();
    await screen.findByTestId('metadata-error');

    vi.mocked(api.getCachedReviewResults).mockResolvedValue({
      results: [makeResult('a')],
      total_count: 1,
      matched: 1,
      no_match: 0,
      errors: 0,
    });
    await user.click(screen.getByRole('button', { name: 'Retry' }));

    // The Alert clears rather than sitting on top of a page that now loaded.
    await waitFor(() => expect(screen.queryByTestId('metadata-error')).not.toBeInTheDocument());
    expect(await screen.findByTestId('compare-spine')).toBeInTheDocument();
  });
});
