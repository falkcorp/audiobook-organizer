// file: web/src/components/dedup/__tests__/DedupAcousticTab.selectAll.test.tsx
// version: 1.3.0
// guid: 3f0b6c1e-8a24-4d5e-9b71-2c6e4a8d0f35
// last-edited: 2026-10-06
//
// Acoustic tab: select page, "Select all N matching", shift range. A
// cross-page action goes to the filter-scoped bulk endpoint (bulk-link with
// keep_side, or bulk-reject) with the PENDING count as expected_total -- it
// never loops the single-pair endpoints, which lack the review-queue-only
// guard. Also the load-error state, which used to render as "no candidates".

import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../../services/api';
import { AcousticDedupTab } from '../DedupAcousticTab';

vi.mock('../../../services/api');

const TOTAL = 7;

function cand(id: number): api.DedupCandidate {
  return {
    id,
    entity_type: 'book',
    entity_a_id: `a${id}`,
    entity_b_id: `b${id}`,
    layer: 'acoustid',
    status: 'pending',
    created_at: '',
    updated_at: '',
  } as unknown as api.DedupCandidate;
}
const ALL = Array.from({ length: TOTAL }, (_, i) => cand(i + 1));

const PENDING = 5;

function mockLists(rows: api.DedupCandidate[] = ALL) {
  vi.mocked(api.getDedupCandidates).mockResolvedValue({
    candidates: rows.slice(0, 3),
    total: TOTAL,
  });
  vi.mocked(api.countBulkDedupCandidates).mockResolvedValue(PENDING);
}

beforeEach(() => {
  vi.resetAllMocks();
  mockLists();
  vi.mocked(api.linkDedupCandidate).mockResolvedValue(undefined);
  vi.mocked(api.rejectDedupCandidate).mockResolvedValue(undefined);
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '' } as api.Config);
  vi.mocked(api.filterChangedOf).mockReturnValue(null);
});

function renderTab() {
  return render(
    <MemoryRouter>
      <AcousticDedupTab />
    </MemoryRouter>
  );
}

const box = (id: number) => screen.getByRole('checkbox', { name: `Select candidate ${id}` });

async function selectAllMatching(user: ReturnType<typeof userEvent.setup>) {
  await screen.findByRole('checkbox', { name: 'Select candidate 3' });
  await user.click(screen.getByRole('checkbox', { name: 'Select all 3 on this page' }));
  expect(screen.getByTestId('acoustic-select-all-banner')).toHaveTextContent(
    'All 3 candidates on this page are selected.'
  );
  // The server's PENDING count -- what the dialog confirms and the bulk
  // endpoints act on -- not the list's every-status total.
  await waitFor(() =>
    expect(screen.getByTestId('acoustic-select-all-matching')).toHaveTextContent(
      `Select all ${PENDING} matching`
    )
  );
  await user.click(screen.getByTestId('acoustic-select-all-matching'));
  expect(screen.getByTestId('acoustic-select-all-banner')).toHaveTextContent(
    `All ${PENDING} candidates matching this filter are selected.`
  );
}

describe('AcousticDedupTab selection', () => {
  it('cross-page Keep A goes to guarded bulk-link with keep_side and the pending count', async () => {
    const user = userEvent.setup();
    vi.mocked(api.bulkLinkDedupCandidates).mockResolvedValue({
      attempted: PENDING,
      merged: 3,
      failed: 2,
      failures: [{ candidate_id: 4, reason: 'manual: review queue only' }],
    });
    renderTab();
    await selectAllMatching(user);

    await user.click(screen.getByRole('button', { name: `Keep A on ${PENDING}` }));
    const dialog = await screen.findByTestId('acoustic-bulk-confirm');
    expect(dialog).toHaveTextContent(`Keep A on all ${PENDING} pending candidates?`);
    await user.click(within(dialog).getByTestId('acoustic-bulk-confirm-btn'));

    expect(api.countBulkDedupCandidates).toHaveBeenCalledWith({
      entity_type: 'book',
      status: 'pending',
      layer: 'acoustid',
    });
    await waitFor(() =>
      expect(api.bulkLinkDedupCandidates).toHaveBeenCalledWith({
        entity_type: 'book',
        status: 'pending',
        layer: 'acoustid',
        expected_total: PENDING,
        keep_side: 'a',
      })
    );
    // Never the unguarded single-pair endpoint for a cross-page selection.
    expect(api.linkDedupCandidate).not.toHaveBeenCalled();
    expect(
      await screen.findByText(/Keep A: 3 linked, 2 refused or failed of 5 \(e\.g\. #4: manual/)
    ).toBeInTheDocument();
  });

  it('cross-page Dismiss goes to bulk-reject and offers Undo by id', async () => {
    const user = userEvent.setup();
    vi.mocked(api.bulkRejectDedupCandidates).mockResolvedValue({
      attempted: PENDING,
      rejected: PENDING,
      failed: 0,
      rejected_ids: [1, 3, 4, 5, 7],
    });
    vi.mocked(api.revertBulkRejectDedupCandidates).mockResolvedValue({
      attempted: PENDING,
      reverted: PENDING,
      failed: 0,
    });
    renderTab();
    await selectAllMatching(user);
    await user.click(screen.getByRole('button', { name: `Dismiss ${PENDING}` }));
    await user.click(
      within(await screen.findByTestId('acoustic-bulk-confirm')).getByTestId(
        'acoustic-bulk-confirm-btn'
      )
    );
    await waitFor(() =>
      expect(api.bulkRejectDedupCandidates).toHaveBeenCalledWith({
        entity_type: 'book',
        status: 'pending',
        layer: 'acoustid',
        expected_total: PENDING,
      })
    );
    expect(api.rejectDedupCandidate).not.toHaveBeenCalled();
    await user.click(await screen.findByTestId('acoustic-undo-dismiss'));
    expect(api.revertBulkRejectDedupCandidates).toHaveBeenCalledWith([1, 3, 4, 5, 7]);
    expect(await screen.findByText('Undo: 5 back to pending')).toBeInTheDocument();
  });

  it('a moved filter (409) changes nothing and asks to confirm again', async () => {
    const user = userEvent.setup();
    vi.mocked(api.bulkRejectDedupCandidates).mockRejectedValue(new Error('moved'));
    vi.mocked(api.filterChangedOf).mockReturnValue({ expected: PENDING, matched: PENDING + 1 });
    renderTab();
    await selectAllMatching(user);
    await user.click(screen.getByRole('button', { name: `Dismiss ${PENDING}` }));
    await user.click(
      within(await screen.findByTestId('acoustic-bulk-confirm')).getByTestId(
        'acoustic-bulk-confirm-btn'
      )
    );
    expect(
      await screen.findByText(
        `The list changed — ${PENDING + 1} pending candidates now match, not ${PENDING}. Nothing was changed; confirm again.`
      )
    ).toBeInTheDocument();
  });

  it('shift-click selects a range; a page-only dismiss acts on the ticked rows without confirming', async () => {
    const user = userEvent.setup();
    renderTab();
    await screen.findByRole('checkbox', { name: 'Select candidate 3' });
    await user.click(box(1));
    await user.keyboard('{Shift>}');
    await user.click(box(3));
    await user.keyboard('{/Shift}');
    expect(box(2)).toBeChecked();

    await user.click(screen.getByRole('button', { name: 'Dismiss 3' }));
    await waitFor(() => expect(api.rejectDedupCandidate).toHaveBeenCalledTimes(3));
    expect(screen.queryByTestId('acoustic-bulk-confirm')).not.toBeInTheDocument();
    expect(api.bulkRejectDedupCandidates).not.toHaveBeenCalled();
  });

  it('decided rows are not selectable', async () => {
    mockLists(ALL.map((c) => (c.id === 2 ? { ...c, status: 'merged' as const } : c)));
    renderTab();
    await screen.findByRole('checkbox', { name: 'Select candidate 3' });
    expect(box(2)).toBeDisabled();
  });

  it('a failed load shows an error, not the empty state', async () => {
    vi.mocked(api.getDedupCandidates).mockRejectedValue(new Error('server said no'));
    renderTab();
    expect(await screen.findByTestId('acoustic-load-error')).toHaveTextContent('server said no');
    expect(screen.queryByText(/No acoustic duplicate candidates found/)).not.toBeInTheDocument();
  });
});
