// file: web/src/components/review/AIScansPanel.test.tsx
// version: 1.2.0
// guid: c4e18d73-9f02-4b6a-a5d1-7e30b82f6c19
// last-edited: 2026-10-13

/**
 * The AI scans sub-view as a reviewer meets it: the authors hook, the scan hook
 * and the panel together, with the reads mocked at their module boundaries so a
 * test decides when each answer lands.
 *
 * Ported from DedupAIReviewTab.test.tsx (the "superseded scan" and "apply refused
 * because superseded" cases) and the AI part of dedupTabs.selectAll.test.tsx.
 * They are in their own file rather than AuthorsPanel.test.tsx because the 409
 * case needs the REAL `ApiError` (the hook tells it from other failures with
 * `instanceof`), and that file mocks the whole api module.
 *
 * Fixtures are synthetic; no real library rows.
 */

import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../services/api';
import type { AIScan, AIScanResult } from '../../services/api';
import { AuthorsPanel } from './AuthorsPanel';
import { useAuthorsLane } from './lanes/useAuthorsLane';
import { useAIScans } from './lanes/useAIScans';
import * as authorsReads from './lanes/authorsReads';
import * as reads from './lanes/aiScanReads';

vi.mock('../../services/api', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../services/api')>()),
  getDedupCandidates: vi.fn(),
  startAIScan: vi.fn(),
  applyAIScanResults: vi.fn(),
  pollOperationV2: vi.fn(),
  cancelAIScan: vi.fn(),
}));
vi.mock('./lanes/authorsReads');
vi.mock('./lanes/aiScanReads');

const toast = vi.fn();

function Harness() {
  const authors = useAuthorsLane(toast, true, { authorId: null, view: 'ai' });
  const aiScans = useAIScans(toast, authors.view === 'ai');
  return <AuthorsPanel authors={authors} aiScans={aiScans} />;
}

const read = (rows: AIScanResult[], authorNames: Record<number, string> = {}) => ({ results: rows, authorNames });
const started = { id: 'op-apply', type: 'ai.author-scan-apply', status: 'queued', scan_id: 5, results: 1 };
const finished = (status: string, error_message: string | null = null) =>
  ({ id: 'op-apply', status, error_message }) as api.OperationV2;

function renderPanel() {
  return render(
    <MemoryRouter>
      <Harness />
    </MemoryRouter>
  );
}

function scan(id: number, over: Partial<AIScan> = {}): AIScan {
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

function result(id: number, agreement: AIScanResult['agreement'] = 'agreed', applied = false): AIScanResult {
  return {
    id,
    scan_id: 5,
    agreement,
    applied,
    suggestion: {
      action: 'merge',
      canonical_name: `Author ${id}`,
      reason: 'same person',
      confidence: 'high',
      author_ids: [id * 10, id * 10 + 1],
      keep_id: id * 10,
      source: 'full_scan',
    },
  };
}

const MIXED = [result(1, 'agreed'), result(2, 'agreed', true), result(3, 'disagreed'), result(4, 'groups_only')];

async function openScan(user: ReturnType<typeof userEvent.setup>, id: number) {
  await user.click(await screen.findByTestId(`ai-scan-${id}`));
}

beforeEach(() => {
  vi.resetAllMocks();
  toast.mockReset();
  vi.mocked(api.getDedupCandidates).mockResolvedValue({ candidates: [], total: 0 });
  vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(5), scan(6)]);
  vi.mocked(reads.fetchAIScanResults).mockResolvedValue(read(MIXED));
});

describe('the sub-view control', () => {
  it('reads Authors | Series | AI scans, with AI scans selected, and has no Books tab', async () => {
    renderPanel();
    expect(await screen.findByTestId('ai-scans-panel')).toBeInTheDocument();
    const control = screen.getByRole('group', { name: 'Authors & series sub-view' });
    expect(within(control).getAllByRole('button').map((b) => b.textContent)).toEqual(['Authors', 'Series', 'AI scans']);
    expect(screen.getByTestId('authors-subview-ai')).toHaveAttribute('aria-pressed', 'true');
    expect(screen.queryByText(/^Books$/)).not.toBeInTheDocument();
    expect(screen.queryByText(/coming soon/i)).not.toBeInTheDocument();
    // Opening the sub-view reads no author or series list.
    expect(authorsReads.fetchAuthorGroups).not.toHaveBeenCalled();
    expect(authorsReads.fetchSeriesGroups).not.toHaveBeenCalled();
  });

  it('the Scan mode selector shows both options and their costs at once, and picks one', async () => {
    const user = userEvent.setup();
    renderPanel();
    const group = await screen.findByRole('group', { name: 'Scan mode' });
    const realtime = within(group).getByRole('button', { name: 'Realtime (faster, more expensive)' });
    const batch = within(group).getByRole('button', { name: 'Batch (cheaper, hours)' });
    expect(realtime).toHaveAttribute('aria-pressed', 'true');
    expect(batch).toHaveAttribute('aria-pressed', 'false');
    await user.click(batch);
    expect(batch).toHaveAttribute('aria-pressed', 'true');
    expect(realtime).toHaveAttribute('aria-pressed', 'false');
    // Clicking the chosen one again keeps it chosen.
    await user.click(batch);
    expect(batch).toHaveAttribute('aria-pressed', 'true');
  });
});

describe('the history', () => {
  it('lists every scan on screen, not in a drawer, with its date and mode', async () => {
    vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(5, { mode: 'batch' }), scan(6)]);
    renderPanel();
    const list = await screen.findByTestId('ai-history-list');
    const first = within(list).getByTestId('ai-scan-6');
    expect(first).toHaveTextContent('Scan #6');
    expect(first).toHaveTextContent('realtime');
    expect(first).toHaveTextContent('scanned 3 authors');
    expect(within(list).getByTestId('ai-scan-5')).toHaveTextContent('batch');
    // Newest first.
    expect(within(list).getAllByTestId(/^ai-scan-\d+$/).map((e) => e.getAttribute('data-testid'))).toEqual([
      'ai-scan-6',
      'ai-scan-5',
    ]);
  });

  it('superseded scan shows the marker', async () => {
    const user = userEvent.setup();
    vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(9, { status: 'superseded', superseded_by: 12, mode: 'batch' })]);
    renderPanel();
    expect(await screen.findByTestId('ai-scan-superseded-9')).toHaveTextContent('superseded');
    // The explanation is text on the page, not a hover tooltip a keyboard cannot reach.
    expect(screen.getByTestId('ai-scan-replaced-by-9')).toHaveTextContent('Replaced by scan #12');
    await openScan(user, 9);

    // Readable, but read-only, and it names the scan that replaced it.
    expect(await screen.findByText('Author 1')).toBeInTheDocument();
    expect(screen.getByTestId('ai-scan-superseded')).toHaveTextContent('superseded by scan #12');
    const card = screen.getByTestId('ai-result-1');
    const boxes = card.querySelectorAll('input[type="checkbox"]');
    expect(boxes.length).toBeGreaterThan(0);
    boxes.forEach((box) => expect(box).toBeDisabled());
    expect(reads.fetchAIScanResults).toHaveBeenCalledWith(9, expect.anything());
  });

  it('is loading, then a distinct empty state', async () => {
    vi.mocked(reads.fetchAIScans).mockResolvedValue([]);
    renderPanel();
    expect(screen.getByTestId('ai-history-loading')).toBeInTheDocument();
    expect(await screen.findByTestId('ai-history-empty')).toBeInTheDocument();
    expect(screen.queryByTestId('ai-history-loading')).not.toBeInTheDocument();
    expect(screen.queryByTestId('ai-history-error')).not.toBeInTheDocument();
  });

  it('a failed read is an error with Retry, not the empty state', async () => {
    const user = userEvent.setup();
    vi.mocked(reads.fetchAIScans).mockRejectedValueOnce(new Error('Internal server error'));
    renderPanel();
    const alert = await screen.findByTestId('ai-history-error');
    expect(alert).toHaveTextContent('Internal server error');
    expect(screen.queryByTestId('ai-history-empty')).not.toBeInTheDocument();
    await user.click(within(alert).getByRole('button', { name: 'Retry' }));
    expect(await screen.findByTestId('ai-history-list')).toBeInTheDocument();
    expect(screen.queryByTestId('ai-history-error')).not.toBeInTheDocument();
  });
});

describe('the open scan', () => {
  it('asks for a scan before one is open', async () => {
    renderPanel();
    expect(await screen.findByTestId('ai-no-scan')).toBeInTheDocument();
  });

  it('a running scan says its progress is in the bell and shows no results', async () => {
    const user = userEvent.setup();
    vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(7, { status: 'cross_validating' })]);
    renderPanel();
    await openScan(user, 7);
    expect(screen.getByTestId('ai-scan-running')).toHaveTextContent('notification bell');
    expect(screen.queryByTestId('ai-results-empty')).not.toBeInTheDocument();
    expect(reads.fetchAIScanResults).not.toHaveBeenCalled();
  });

  it('a failed and a canceled scan each say so', async () => {
    const user = userEvent.setup();
    vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(7, { status: 'failed' }), scan(8, { status: 'canceled' })]);
    renderPanel();
    await openScan(user, 7);
    expect(screen.getByTestId('ai-scan-failed')).toHaveTextContent('Scan #7 failed');
    await openScan(user, 8);
    expect(screen.getByTestId('ai-scan-canceled')).toHaveTextContent('Scan #8 was canceled');
    expect(screen.queryByTestId('ai-scan-failed')).not.toBeInTheDocument();
  });

  it('a results read that fails is an error with Retry, and a scan with no results is empty', async () => {
    const user = userEvent.setup();
    vi.mocked(reads.fetchAIScanResults).mockRejectedValueOnce(new Error('results: 500'));
    renderPanel();
    await openScan(user, 5);
    const alert = await screen.findByTestId('ai-results-error');
    expect(alert).toHaveTextContent('results: 500');
    expect(screen.queryByTestId('ai-results-empty')).not.toBeInTheDocument();

    vi.mocked(reads.fetchAIScanResults).mockResolvedValue(read([]));
    await user.click(within(alert).getByRole('button', { name: 'Retry' }));
    expect(await screen.findByTestId('ai-results-empty')).toBeInTheDocument();
    expect(screen.queryByTestId('ai-results-error')).not.toBeInTheDocument();
  });
});

describe('agreement chips', () => {
  it('agreement counts filter results: each chip shows its count and opens exactly that many rows', async () => {
    const user = userEvent.setup();
    renderPanel();
    await openScan(user, 5);
    await screen.findByTestId('ai-result-1');

    const expected = { all: 4, agreed: 2, groups_only: 1, full_only: 0, disagreed: 1 } as const;
    expect(screen.getByTestId('ai-agreement-all')).toHaveTextContent('All (4)');
    expect(screen.getByTestId('ai-agreement-agreed')).toHaveTextContent('Agreed (2)');
    expect(screen.getByTestId('ai-agreement-groups_only')).toHaveTextContent('Groups Only (1)');
    expect(screen.getByTestId('ai-agreement-full_only')).toHaveTextContent('Full Only (0)');
    expect(screen.getByTestId('ai-agreement-disagreed')).toHaveTextContent('Disagreed (1)');

    // Full Only (0) last, so the empty-filter state is the one on screen after the loop.
    const order = ['all', 'agreed', 'groups_only', 'disagreed', 'full_only'] as const;
    for (const value of order) {
      const count = expected[value];
      await user.click(screen.getByTestId(`ai-agreement-${value}`));
      if (count === 0) {
        expect(screen.getByTestId('ai-results-filter-empty')).toBeInTheDocument();
        expect(screen.queryAllByTestId(/^ai-result-\d+$/)).toHaveLength(0);
      } else {
        expect(screen.getAllByTestId(/^ai-result-\d+$/)).toHaveLength(count);
      }
    }
    await user.click(screen.getByRole('button', { name: 'Show all results' }));
    expect(screen.getAllByTestId(/^ai-result-\d+$/)).toHaveLength(4);
  });
});

describe('merge rows and read-only rows', () => {
  it('a merge row says which author it keeps and which it folds in, by name, the keep first', async () => {
    const user = userEvent.setup();
    const row = result(1);
    row.suggestion.author_ids = [10, 11, 12];
    vi.mocked(reads.fetchAIScanResults).mockResolvedValue(
      read([row], { 10: 'Dana Fixture', 11: 'Fixture, Dana', 12: 'D. Fixture' })
    );
    renderPanel();
    await openScan(user, 5);
    expect(await screen.findByTestId('ai-result-plan-1')).toHaveTextContent(
      'keep Dana Fixture, merge Fixture, Dana, D. Fixture'
    );
  });

  it('an author merged away since the scan is named by id, not left blank', async () => {
    const user = userEvent.setup();
    vi.mocked(reads.fetchAIScanResults).mockResolvedValue(read([result(1)], { 10: 'Dana Fixture' }));
    renderPanel();
    await openScan(user, 5);
    expect(await screen.findByTestId('ai-result-plan-1')).toHaveTextContent('merge author #11 (no longer exists)');
  });

  it('rename, alias, split, reclassify and skip rows are read-only, each with a link to the Authors view', async () => {
    const user = userEvent.setup();
    const rows = ['rename', 'alias', 'split', 'reclassify', 'skip'].map((action, i) => {
      const r = result(i + 1);
      return { ...r, suggestion: { ...r.suggestion, action, keep_id: undefined } };
    });
    vi.mocked(reads.fetchAIScanResults).mockResolvedValue(read(rows));
    renderPanel();
    await openScan(user, 5);
    for (const r of rows) {
      expect(await screen.findByTestId(`ai-result-readonly-${r.id}`)).toHaveTextContent('Read-only here');
      expect(screen.getByRole('checkbox', { name: `Select result ${r.id}` })).toBeDisabled();
      const link = screen.getByTestId(`ai-result-authors-link-${r.id}`);
      expect(link).toHaveAttribute('href', `/review?lane=authors&author=${r.suggestion.author_ids![0]}`);
    }
    // Nothing is selectable, so there is nothing to apply.
    expect(screen.getByRole('checkbox', { name: /Select all 0 mergeable results shown/ })).toBeDisabled();
    expect(screen.queryByRole('button', { name: /apply selected/i })).not.toBeInTheDocument();
  });

  it('a disagreed row shows both phases\' answers, and a disagreed MERGE row can still be selected', async () => {
    const user = userEvent.setup();
    const groups = { ...result(3, 'disagreed'), paired_with: 4 };
    groups.suggestion = { ...groups.suggestion, source: 'groups_scan', canonical_name: 'Dana Fixture' };
    const full = { ...result(4, 'disagreed'), paired_with: 3 };
    full.suggestion = { ...full.suggestion, source: 'full_scan', action: 'alias', keep_id: undefined, canonical_name: 'D. Fixture' };
    vi.mocked(reads.fetchAIScanResults).mockResolvedValue(read([groups, full]));
    renderPanel();
    await openScan(user, 5);
    const expected = 'Groups scan said merge "Dana Fixture" · Full scan said alias "D. Fixture"';
    expect(await screen.findByTestId('ai-result-pair-3')).toHaveTextContent(expected);
    expect(screen.getByTestId('ai-result-pair-4')).toHaveTextContent(expected);
    expect(screen.getByRole('checkbox', { name: 'Select result 3' })).toBeEnabled();
    expect(screen.getByRole('checkbox', { name: 'Select result 4' })).toBeDisabled();
  });
});

describe('selection and apply', () => {
  it('header selects every unapplied merge result; shift range skips applied ones', async () => {
    const user = userEvent.setup();
    vi.mocked(api.applyAIScanResults).mockResolvedValue({ ...started, results: 3 });
    vi.mocked(api.pollOperationV2).mockResolvedValue(finished('completed'));
    renderPanel();
    await openScan(user, 5);
    await screen.findByTestId('ai-result-1');

    await user.click(screen.getByRole('checkbox', { name: 'Select result 1' }));
    await user.keyboard('{Shift>}');
    await user.click(screen.getByRole('checkbox', { name: 'Select result 3' }));
    await user.keyboard('{/Shift}');
    expect(screen.getByRole('button', { name: 'Apply selected (2)' })).toBeInTheDocument();
    // The count lives on the button; there is no second number that opens nothing.
    expect(screen.queryByText(/\d+ of \d+ selected/)).not.toBeInTheDocument();

    await user.click(screen.getByRole('checkbox', { name: 'Select all 3 mergeable results shown' }));
    await user.click(screen.getByRole('button', { name: 'Apply selected (3)' }));
    await waitFor(() => expect(api.applyAIScanResults).toHaveBeenCalledTimes(1));
    expect(vi.mocked(api.applyAIScanResults).mock.calls[0][0]).toBe(5);
    expect([...vi.mocked(api.applyAIScanResults).mock.calls[0][1]].sort()).toEqual([1, 3, 4]);
    // Not the old "Applied N of M": the merges run in an operation, and the notice
    // reports it once it has ended.
    expect(await screen.findByTestId('ai-scans-notice')).toHaveTextContent('Merged the authors of 3 selected results.');
    await waitFor(() => expect(reads.fetchAIScanResults).toHaveBeenCalledTimes(2));
  });

  it('changing scan clears selection', async () => {
    const user = userEvent.setup();
    renderPanel();
    await openScan(user, 5);
    await user.click(await screen.findByRole('checkbox', { name: 'Select result 1' }));
    expect(screen.getByRole('button', { name: 'Apply selected (1)' })).toBeInTheDocument();

    await openScan(user, 6);
    await screen.findByTestId('ai-result-1');
    expect(screen.queryByRole('button', { name: /apply selected/i })).not.toBeInTheDocument();
    expect(screen.getByRole('checkbox', { name: 'Select result 1' })).not.toBeChecked();
  });

  it('apply is refused for a superseded scan: the message is shown and Apply goes away', async () => {
    const user = userEvent.setup();
    const message = 'scan 5 was superseded by scan 12; apply from scan 12 instead';
    vi.mocked(api.applyAIScanResults).mockRejectedValue(new api.ApiError(message, 409));
    renderPanel();
    await openScan(user, 5);
    await user.click(await screen.findByRole('checkbox', { name: 'Select result 1' }));

    // By the time Apply is clicked, a newer nightly run has replaced this scan.
    vi.mocked(reads.fetchAIScans).mockResolvedValue([scan(5, { status: 'superseded', superseded_by: 12 }), scan(6)]);
    await user.click(screen.getByRole('button', { name: 'Apply selected (1)' }));

    expect(await screen.findByTestId('ai-scans-notice')).toHaveTextContent(message);
    expect(await screen.findByTestId('ai-scan-superseded-5')).toBeInTheDocument();
    expect(await screen.findByTestId('ai-scan-superseded')).toHaveTextContent('superseded by scan #12');
    expect(screen.queryByRole('button', { name: /apply selected/i })).not.toBeInTheDocument();
    expect(reads.fetchAIScans).toHaveBeenCalledTimes(2);
  });

  it('says which results the operation could not apply', async () => {
    const user = userEvent.setup();
    const failure = 'AI scan #5: 0 of 1 result(s) applied, 1 not applied: result 1: book b1: author name is user-locked';
    vi.mocked(api.applyAIScanResults).mockResolvedValue(started);
    vi.mocked(api.pollOperationV2).mockResolvedValue(finished('failed', failure));
    renderPanel();
    await openScan(user, 5);
    await user.click(await screen.findByRole('checkbox', { name: 'Select result 1' }));
    await user.click(screen.getByRole('button', { name: 'Apply selected (1)' }));
    const notice = await screen.findByTestId('ai-scans-notice');
    expect(notice).toHaveTextContent('0 of 1 result(s) applied');
    expect(notice).toHaveTextContent('result 1');
    expect(notice).toHaveTextContent('user-locked');
  });
});
