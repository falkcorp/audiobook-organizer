// file: web/src/components/review/ReviewWorkspace.aiScans.test.tsx
// version: 1.4.0
// guid: 91d5a7c3-2e68-4f10-b3a4-86c0f5e7d219
// last-edited: 2026-10-19

/**
 * The AI scans sub-view inside the workspace: how it is reached, the two lane
 * menu commands (Start AI author scan, Cancel scan) that act on it, and Apply
 * selected, which goes through the same startJob.
 *
 * A file of its own rather than a block in ReviewWorkspace.test.tsx, which is a
 * hotspot every lane edits; this keeps the sub-view's wiring tests out of that
 * merge conflict.
 */

import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, useLocation } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../services/api';
import { serveReviewList } from '../../test/reviewServer';
import { ReviewWorkspace } from './ReviewWorkspace';
import { ToastProvider } from '../toast/ToastProvider';
import * as authorsReads from './lanes/authorsReads';
import * as reads from './lanes/aiScanReads';

vi.mock('../../services/api');
vi.mock('./lanes/authorsReads');
vi.mock('./lanes/aiScanReads');

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

function scan(id: number, over: Partial<api.AIScan> = {}): api.AIScan {
  return {
    id,
    status: 'complete',
    mode: 'realtime',
    models: { groups: 'g', full: 'f' },
    author_count: 3,
    created_at: '2026-10-01T00:00:00Z',
    ...over,
  };
}

async function openMenu(user: ReturnType<typeof userEvent.setup>) {
  await user.click(screen.getByTestId('command-menu-authors'));
}

const start = () => screen.findByTestId('command-ai-author-scan-start');
const cancel = () => screen.findByTestId('command-ai-author-scan-cancel');

beforeEach(() => {
  vi.resetAllMocks();
  vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(5), scan(6)]);
  vi.mocked(reads.fetchAIScanResults).mockResolvedValue({ results: [], authorNames: {} });
  vi.mocked(authorsReads.fetchAuthorGroups).mockResolvedValue({ groups: [], needsRefresh: false });
  vi.mocked(authorsReads.fetchSeriesGroups).mockResolvedValue({ groups: [], totalSeries: 0, needsRefresh: false });
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

afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe('reaching AI scans', () => {
  it('?lane=authors&view=ai opens AI scans, reads the history once and no author or series list, and writes nothing back', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=authors&view=ai');
    expect(await screen.findByTestId('ai-scans-panel')).toBeInTheDocument();
    expect(await screen.findByTestId('ai-history-list')).toBeInTheDocument();
    expect(screen.getByTestId('authors-subview-ai')).toHaveAttribute('aria-pressed', 'true');
    expect(reads.fetchAIScans).toHaveBeenCalledTimes(1);
    expect(authorsReads.fetchAuthorGroups).not.toHaveBeenCalled();
    expect(authorsReads.fetchSeriesGroups).not.toHaveBeenCalled();

    // One click to Authors; the URL is how you ARRIVE, not a record.
    await user.click(screen.getByTestId('authors-subview-authors'));
    await waitFor(() => expect(authorsReads.fetchAuthorGroups).toHaveBeenCalledTimes(1));
    expect(screen.getByTestId('location')).toHaveTextContent('/review?lane=authors&view=ai');
  });

  it('?view=ai is for the authors lane only: it does not open AI scans when the URL landed on another lane', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=dupes&view=ai');
    await screen.findByTestId('dupes-rail');
    await user.click(screen.getByTestId('lane-tab-authors'));
    expect(await screen.findByTestId('authors-panel')).toBeInTheDocument();
    expect(reads.fetchAIScans).not.toHaveBeenCalled();
  });

  it('reads nothing while another lane is showing, and stops when the sub-view is left', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=authors&view=ai');
    await screen.findByTestId('ai-history-list');
    const signal = vi.mocked(reads.fetchAIScans).mock.calls[0][0];
    expect(signal.aborted).toBe(false);
    await user.click(screen.getByTestId('lane-tab-regroup'));
    await screen.findByTestId('regroup-rail');
    expect(signal.aborted).toBe(true);
    expect(reads.fetchAIScans).toHaveBeenCalledTimes(1);
  });
});

describe('the Authors & series menu', () => {
  it('has Start AI author scan and Cancel scan, and Cancel waits for a running scan', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=authors&view=ai');
    await screen.findByTestId('ai-history-list');
    await openMenu(user);
    expect(await start()).toHaveTextContent('Start AI author scan');
    expect(await start()).not.toHaveAttribute('aria-disabled', 'true');
    expect(await cancel()).toHaveTextContent('Cancel scan');
    expect(await cancel()).toHaveAttribute('aria-disabled', 'true');
  });

  it('outside AI scans both commands are disabled, because the Realtime / Batch choice is not on screen', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=authors');
    await screen.findByTestId('authors-panel');
    await openMenu(user);
    expect(await start()).toHaveAttribute('aria-disabled', 'true');
    expect(await cancel()).toHaveAttribute('aria-disabled', 'true');
    expect(reads.fetchAIScans).not.toHaveBeenCalled();
  });

  it('once the history has loaded, leaving AI scans for Authors disables Start again', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=authors&view=ai');
    await screen.findByTestId('ai-scan-5');
    await openMenu(user);
    // On screen, with nothing running: live.
    expect(await start()).not.toHaveAttribute('aria-disabled', 'true');
    await user.keyboard('{Escape}');

    // The history is still held and shows nothing running, but the Realtime / Batch
    // choice is no longer on screen.
    await user.click(screen.getByTestId('authors-subview-authors'));
    await screen.findByTestId('authors-panel');
    await openMenu(user);
    expect(await start()).toHaveAttribute('aria-disabled', 'true');
  });

  it('once the history has loaded, leaving AI scans for Authors disables Cancel again', async () => {
    const user = userEvent.setup();
    vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(7, { status: 'scanning' }), scan(5)]);
    renderWorkspace('/review?lane=authors&view=ai');
    await screen.findByTestId('ai-scan-7');
    await openMenu(user);
    expect(await cancel()).not.toHaveAttribute('aria-disabled', 'true');
    await user.keyboard('{Escape}');

    await user.click(screen.getByTestId('authors-subview-authors'));
    await screen.findByTestId('authors-panel');
    await openMenu(user);
    // The running scan is still held, but it is no longer on screen to be cancelled.
    expect(await cancel()).toHaveAttribute('aria-disabled', 'true');
  });

  it('the Start description follows the switch', async () => {
    const user = userEvent.setup();
    renderWorkspace('/review?lane=authors&view=ai');
    await screen.findByTestId('ai-history-list');
    await openMenu(user);
    expect(await screen.findByTestId('command-ai-author-scan-start-description')).toHaveTextContent('Realtime mode');
    await user.keyboard('{Escape}');
    await user.click(screen.getByRole('button', { name: 'Batch (cheaper, hours)' }));
    await openMenu(user);
    expect(await screen.findByTestId('command-ai-author-scan-start-description')).toHaveTextContent('Batch mode');
  });

  it('start scan uses startJob (no local polling): it starts the scan, points at the bell, and re-reads once', async () => {
    // The old tab re-read the active scan every 5 s. Any timer of 2 s or more that
    // this feature's own code creates is a poll.
    const slow: Array<[string, unknown]> = [];
    for (const name of ['setInterval', 'setTimeout'] as const) {
      const real = globalThis[name];
      vi.spyOn(globalThis, name).mockImplementation(((fn: () => void, ms?: number, ...rest: unknown[]) => {
        // Created by this feature's own code: the first frame that is not the spy,
        // a test or a dependency is a non-test file under components/review. (A toast's
        // 5 s auto-dismiss, from the app store, is not a poll.)
        const first = (new Error().stack ?? '')
          .split('\n')
          .slice(1)
          .find((l) => /\bat /.test(l) && !/node_modules|\.test\.tsx?/.test(l));
        const ours = Boolean(first && /src\/components\/review\//.test(first));
        if ((ms ?? 0) >= 2000 && ours) slow.push([name, ms]);
        return (real as (...a: unknown[]) => unknown)(fn, ms, ...rest);
      }) as never);
    }
    const user = userEvent.setup();
    vi.mocked(api.startAIScan).mockResolvedValue(scan(7, { status: 'pending' }));
    renderWorkspace('/review?lane=authors&view=ai');
    await screen.findByTestId('ai-history-list');
    expect(reads.fetchAIScans).toHaveBeenCalledTimes(1);

    vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(7, { status: 'pending' }), scan(5), scan(6)]);
    await openMenu(user);
    await user.click(await start());

    await waitFor(() => expect(api.startAIScan).toHaveBeenCalledWith('realtime'));
    expect(await screen.findByText('AI author scan started — watch the bell for progress.')).toBeInTheDocument();
    expect(await screen.findByTestId('ai-scan-7')).toBeInTheDocument();
    expect(reads.fetchAIScans).toHaveBeenCalledTimes(2);
    expect(slow).toEqual([]);

    // A quiet minute reads nothing more.
    vi.useFakeTimers();
    await vi.advanceTimersByTimeAsync(60_000);
    expect(reads.fetchAIScans).toHaveBeenCalledTimes(2);
    expect(reads.fetchAIScanResults).not.toHaveBeenCalled();
  });

  it('Start runs in Batch mode when the switch is on', async () => {
    const user = userEvent.setup();
    vi.mocked(api.startAIScan).mockResolvedValue(scan(7, { status: 'pending', mode: 'batch' }));
    renderWorkspace('/review?lane=authors&view=ai');
    await screen.findByTestId('ai-history-list');
    await user.click(screen.getByRole('button', { name: 'Batch (cheaper, hours)' }));
    await openMenu(user);
    await user.click(await start());
    await waitFor(() => expect(api.startAIScan).toHaveBeenCalledWith('batch'));
  });

  it('Start that the server refuses is reported with the server\'s reason', async () => {
    const user = userEvent.setup();
    vi.mocked(api.startAIScan).mockRejectedValue(new Error('AI scan pipeline not configured'));
    renderWorkspace('/review?lane=authors&view=ai');
    await screen.findByTestId('ai-history-list');
    await openMenu(user);
    await user.click(await start());
    expect(
      await screen.findByText('Failed to start AI author scan: AI scan pipeline not configured')
    ).toBeInTheDocument();
  });

  it('Start while a scan is already running shows the server\'s own 409 text', async () => {
    const user = userEvent.setup();
    const refusal = 'an AI author scan is already running (scan #4, scanning); wait for it to finish or cancel it first';
    vi.mocked(api.startAIScan).mockRejectedValue(new Error(refusal));
    renderWorkspace('/review?lane=authors&view=ai');
    await screen.findByTestId('ai-history-list');
    await openMenu(user);
    await user.click(await start());
    expect(await screen.findByText(`Failed to start AI author scan: ${refusal}`)).toBeInTheDocument();
  });

  it('Start cannot be fired twice: once the POST has answered the scan is listed and the command is refused, even if the re-read never answers', async () => {
    const user = userEvent.setup();
    vi.mocked(api.startAIScan).mockResolvedValue(scan(7, { status: 'pending' }));
    renderWorkspace('/review?lane=authors&view=ai');
    await screen.findByTestId('ai-history-list');
    // The re-read after the start never answers.
    vi.mocked(reads.fetchAIScans).mockImplementation(() => new Promise(() => {}));
    await openMenu(user);
    await user.click(await start());
    await screen.findByTestId('ai-scan-7');

    await openMenu(user);
    expect(await start()).toHaveAttribute('aria-disabled', 'true');
    expect(api.startAIScan).toHaveBeenCalledTimes(1);
  });

  it('Start is disabled while a scan is running, and says which', async () => {
    const user = userEvent.setup();
    vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(7, { status: 'scanning' }), scan(5)]);
    renderWorkspace('/review?lane=authors&view=ai');
    await screen.findByTestId('ai-scan-7');
    await openMenu(user);
    expect(await start()).toHaveAttribute('aria-disabled', 'true');
    expect(await cancel()).not.toHaveAttribute('aria-disabled', 'true');
  });

  it('cancel scan cancels the running scan and re-reads the list', async () => {
    const user = userEvent.setup();
    vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(7, { status: 'scanning' }), scan(5)]);
    vi.mocked(api.cancelAIScan).mockResolvedValue(undefined);
    renderWorkspace('/review?lane=authors&view=ai');
    await screen.findByTestId('ai-scan-7');

    vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(7, { status: 'canceled' }), scan(5)]);
    await openMenu(user);
    await user.click(await cancel());

    await waitFor(() => expect(api.cancelAIScan).toHaveBeenCalledWith(7));
    expect(await screen.findByText('Scan #7 canceled.')).toBeInTheDocument();
    await waitFor(() => expect(within(screen.getByTestId('ai-scan-7')).getByText('canceled')).toBeInTheDocument());
  });
});

describe('Apply selected goes through startJob', () => {
  const mergeRow = {
    id: 1,
    scan_id: 5,
    agreement: 'agreed' as const,
    applied: false,
    suggestion: {
      action: 'merge',
      canonical_name: 'Dana Fixture',
      reason: 'same person',
      confidence: 'high',
      author_ids: [10, 11],
      keep_id: 10,
      source: 'cross_validate',
    },
  };

  async function openAndSelect(user: ReturnType<typeof userEvent.setup>) {
    vi.mocked(reads.fetchAIScanResults).mockResolvedValue({
      results: [mergeRow],
      authorNames: { 10: 'Dana Fixture', 11: 'Fixture, Dana' },
    });
    renderWorkspace('/review?lane=authors&view=ai');
    await user.click(await screen.findByTestId('ai-scan-5'));
    expect(await screen.findByTestId('ai-result-plan-1')).toHaveTextContent('keep Dana Fixture, merge Fixture, Dana');
    await user.click(screen.getByRole('checkbox', { name: 'Select result 1' }));
  }

  it('queues the merge, points at the bell, then reports what the operation did and re-reads', async () => {
    const user = userEvent.setup();
    vi.mocked(api.applyAIScanResults).mockResolvedValue({
      id: 'op-apply',
      type: 'ai.author-scan-apply',
      status: 'queued',
      scan_id: 5,
      results: 1,
    });
    vi.mocked(api.pollOperationV2).mockResolvedValue({ id: 'op-apply', status: 'completed', error_message: null } as api.OperationV2);
    await openAndSelect(user);
    await user.click(screen.getByRole('button', { name: 'Apply selected (1)' }));

    await waitFor(() => expect(api.applyAIScanResults).toHaveBeenCalledWith(5, [1]));
    expect(await screen.findByText('AI scan apply started — watch the bell for progress.')).toBeInTheDocument();
    expect(await screen.findByTestId('ai-scans-notice')).toHaveTextContent('Merged the authors of 1 selected result.');
    await waitFor(() => expect(reads.fetchAIScanResults).toHaveBeenCalledTimes(2));
  });

  it('a refusal is an error toast with the server\'s own words, and nothing is polled', async () => {
    const user = userEvent.setup();
    const refusal = 'result 1 is a "alias" suggestion; only merge suggestions can be applied here';
    vi.mocked(api.applyAIScanResults).mockRejectedValue(new Error(refusal));
    await openAndSelect(user);
    await user.click(screen.getByRole('button', { name: 'Apply selected (1)' }));

    expect(await screen.findByText(`Failed to start AI scan apply: ${refusal}`)).toBeInTheDocument();
    expect(api.pollOperationV2).not.toHaveBeenCalled();
  });
});
