// file: web/src/components/dedup/__tests__/DedupAcousticTab.selectAll.test.tsx
// version: 1.0.0
// guid: 3f0b6c1e-8a24-4d5e-9b71-2c6e4a8d0f35
// last-edited: 2026-10-06
//
// Acoustic tab: select page, "Select all N matching", shift range, and the
// cross-page bulk path that pages the list query to resolve per-pair sides.
// Also the load-error state, which used to render as "no candidates".

import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../../services/api';
import { AcousticDedupTab } from '../DedupAcousticTab';
import { CROSS_PAGE_FETCH_PAGE, fetchAllMatchingCandidates } from '../crossPageCandidates';

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

beforeEach(() => {
  vi.resetAllMocks();
  // The rendered page holds 3 rows; a cross-page read (limit 500) gets all 7.
  vi.mocked(api.getDedupCandidates).mockImplementation(async (params) => ({
    candidates: params?.limit === CROSS_PAGE_FETCH_PAGE ? ALL : ALL.slice(0, 3),
    total: TOTAL,
  }));
  vi.mocked(api.linkDedupCandidate).mockResolvedValue(undefined);
  vi.mocked(api.rejectDedupCandidate).mockResolvedValue(undefined);
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '' } as api.Config);
});

function renderTab() {
  return render(
    <MemoryRouter>
      <AcousticDedupTab />
    </MemoryRouter>
  );
}

const box = (id: number) => screen.getByRole('checkbox', { name: `Select candidate ${id}` });

describe('AcousticDedupTab selection', () => {
  it('select page -> select all matching -> confirmed Keep A pages the list and links every pair', async () => {
    const user = userEvent.setup();
    renderTab();
    await screen.findByRole('checkbox', { name: 'Select candidate 3' });

    await user.click(screen.getByRole('checkbox', { name: 'Select all 3 on this page' }));
    expect(screen.getByTestId('acoustic-select-all-banner')).toHaveTextContent(
      'All 3 candidates on this page are selected.'
    );
    await user.click(screen.getByTestId('acoustic-select-all-matching'));
    expect(screen.getByTestId('acoustic-select-all-banner')).toHaveTextContent(
      `All ${TOTAL} candidates matching this filter are selected.`
    );

    await user.click(screen.getByRole('button', { name: `Keep A on ${TOTAL}` }));
    const dialog = await screen.findByTestId('acoustic-bulk-confirm');
    expect(api.linkDedupCandidate).not.toHaveBeenCalled();
    await user.click(within(dialog).getByTestId('acoustic-bulk-confirm-btn'));

    await waitFor(() => expect(api.linkDedupCandidate).toHaveBeenCalledTimes(TOTAL));
    for (const c of ALL) expect(api.linkDedupCandidate).toHaveBeenCalledWith(c.id, c.entity_a_id);
    expect(await screen.findByText(`Keep A: ${TOTAL} candidate(s) processed`)).toBeInTheDocument();
  });

  it('shift-click selects a range; page-only dismiss does not confirm or page the list', async () => {
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
    expect(api.getDedupCandidates).not.toHaveBeenCalledWith(
      expect.objectContaining({ limit: CROSS_PAGE_FETCH_PAGE }),
      expect.anything()
    );
  });

  it('a failed load shows an error, not the empty state', async () => {
    vi.mocked(api.getDedupCandidates).mockRejectedValue(new Error('server said no'));
    renderTab();
    expect(await screen.findByTestId('acoustic-load-error')).toHaveTextContent('server said no');
    expect(screen.queryByText(/No acoustic duplicate candidates found/)).not.toBeInTheDocument();
  });
});

describe('fetchAllMatchingCandidates', () => {
  it('refuses over the cap without fetching', async () => {
    await expect(
      fetchAllMatchingCandidates({ layer: 'acoustid' }, 11, undefined, undefined, 10)
    ).rejects.toThrow(/over the 10/);
    expect(api.getDedupCandidates).not.toHaveBeenCalled();
  });

  it('refuses when the total changes mid-walk', async () => {
    vi.mocked(api.getDedupCandidates).mockResolvedValue({ candidates: ALL, total: TOTAL + 1 });
    await expect(fetchAllMatchingCandidates({ layer: 'acoustid' }, TOTAL)).rejects.toThrow(
      /changed/
    );
  });
});
