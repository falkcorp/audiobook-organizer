// file: web/src/components/review/ReviewWorkspace.test.tsx
// version: 1.4.0
// guid: 3c8f0a62-9b47-4d15-8e30-1f7a2c5b9d64
// last-edited: 2026-08-20

import { render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import userEvent from '@testing-library/user-event';
import { vi, describe, it, expect, beforeEach } from 'vitest';
import * as api from '../../services/api';
import { ReviewWorkspace } from './ReviewWorkspace';
import { ToastProvider } from '../toast/ToastProvider';

vi.mock('../../services/api');

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
  vi.mocked(api.getReviewCount).mockResolvedValue({ count: 0, byKind: {} });
});

describe('lane default', () => {
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

describe('command bar', () => {
  it('disables a selection command and says why, rather than no-opping', async () => {
    const user = userEvent.setup();
    renderWorkspace();
    await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());

    await user.click(screen.getByTestId('command-menu-metadata'));

    const item = await screen.findByTestId('command-bulk-search-selected');
    expect(item).toHaveAttribute('aria-disabled', 'true');
  });

  it('labels library-wide commands as library-wide', async () => {
    // PLAN.md:390-394 -- most of these routes are library-wide, and silently
    // firing one from a row-level control is the wrong kind of convenience.
    const user = userEvent.setup();
    renderWorkspace();
    await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());

    await user.click(screen.getByTestId('command-menu-dedup'));

    const item = await screen.findByTestId('command-find-duplicates');
    expect(item).toHaveTextContent(/library-wide/i);
  });

  it('starts the job behind a command', async () => {
    const user = userEvent.setup();
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
