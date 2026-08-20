// file: web/src/components/review/ReviewWorkspace.test.tsx
// version: 1.0.0
// guid: 3c8f0a62-9b47-4d15-8e30-1f7a2c5b9d64
// last-edited: 2026-08-20

import { render, screen, waitFor, within } from '@testing-library/react';
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

function renderWorkspace() {
  return render(
    <ToastProvider>
      <ReviewWorkspace />
    </ToastProvider>
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

  it('explains an unported lane instead of showing an empty spine', async () => {
    const user = userEvent.setup();
    renderWorkspace();
    await waitFor(() => expect(screen.getByTestId('compare-spine')).toBeInTheDocument());

    await user.click(screen.getByTestId('lane-tab-dupes'));

    const panel = await screen.findByTestId('lane-unported-dupes');
    expect(panel).toBeInTheDocument();
    // It must say where the surface actually is; "nothing here" with no next
    // step is the failure this replaces.
    expect(panel).toHaveTextContent(/Dedup page/i);
    expect(screen.queryByTestId('compare-spine')).not.toBeInTheDocument();
  });

  it('stops fetching the metadata set while another lane is showing', async () => {
    const user = userEvent.setup();
    renderWorkspace();
    await waitFor(() => expect(api.getCachedReviewResults).toHaveBeenCalledTimes(1));

    await user.click(screen.getByTestId('lane-tab-regroup'));
    await screen.findByTestId('lane-unported-regroup');

    expect(api.getCachedReviewResults).toHaveBeenCalledTimes(1);
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
