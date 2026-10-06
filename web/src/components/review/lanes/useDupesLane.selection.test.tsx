// file: web/src/components/review/lanes/useDupesLane.selection.test.tsx
// version: 1.3.0
// guid: 7b5601fc-d9e3-408e-80f4-8fd4bba7ce60
// last-edited: 2026-10-06
//
// Select-page, "Select all N matching" across pages, and shift-range selection
// on the dedup review lane (owner request 2026-10-06), through the lane hook
// and through the rendered DupesPanel. The cross-page bulk actions must reach
// the FILTER-scoped endpoints (bulk-link / bulk-reject) with the exact filter
// on screen -- a missing field widens an irreversible action.

import { act, render, renderHook, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../../services/api';
import { DupesPanel } from '../DupesPanel';
import {
  filterChangedMessage,
  MERGE_ALL_BLOCKED_REASON,
  SELECT_ALL_MATCHING_PENDING_ONLY_REASON,
  useDupesLane,
  type DupesUrlFilters,
} from './useDupesLane';

vi.mock('../../../services/api');

const toast = vi.fn();
const TOTAL = 137;
// The server's bulk count differs from the list total (dead-book rows and the
// like are paging noise in the list): the dialog must show and send THIS.
const COUNT = 133;

function cand(id: number): api.DedupCandidate {
  return {
    id,
    entity_type: 'book',
    entity_a_id: `a${id}`,
    entity_b_id: `b${id}`,
    layer: 'embedding',
    status: 'pending',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    band: 'HIGH',
    book_a: { id: `a${id}`, title: `Book A${id}` },
    book_b: { id: `b${id}`, title: `Book B${id}` },
  } as api.DedupCandidate;
}

// Five rows per page out of TOTAL, page given by offset.
function mockPaged() {
  vi.mocked(api.getDedupCandidates).mockImplementation(async (params) => {
    const offset = params?.offset ?? 0;
    return { candidates: [1, 2, 3, 4, 5].map((i) => cand(offset + i)), total: TOTAL };
  });
}

beforeEach(() => {
  vi.resetAllMocks();
  toast.mockClear();
  mockPaged();
  vi.mocked(api.getDedupStats).mockResolvedValue({ stats: [] });
  vi.mocked(api.getConfig).mockResolvedValue({ root_dir: '' } as api.Config);
  vi.mocked(api.bulkLinkDedupCandidates).mockResolvedValue({
    attempted: TOTAL,
    merged: TOTAL,
    failed: 0,
  });
  vi.mocked(api.countBulkDedupCandidates).mockResolvedValue(COUNT);
  vi.mocked(api.bulkRejectDedupCandidates).mockResolvedValue({
    attempted: TOTAL,
    rejected: TOTAL - 1,
    failed: 1,
  });
});

async function renderLane(uf: DupesUrlFilters = { band: null, entityId: null }) {
  const view = renderHook(({ u }) => useDupesLane(toast, true, u), { initialProps: { u: uf } });
  await waitFor(() => expect(view.result.current.loading).toBe(false));
  await waitFor(() => expect(view.result.current.candidates).toHaveLength(5));
  return view;
}

describe('useDupesLane selection', () => {
  it('header select selects the page; select-all-matching counts every page', async () => {
    const { result } = await renderLane();
    act(() => result.current.selection.togglePage());
    expect(result.current.selection.selectedCount).toBe(5);
    expect(result.current.selection.showSelectAllMatching).toBe(true);

    act(() => result.current.selection.selectAllMatching());
    expect(result.current.selection.allMatching).toBe(true);
    expect(result.current.selection.selectedCount).toBe(TOTAL);
  });

  it('a page turn keeps an all-matching selection but clears an explicit one', async () => {
    const { result } = await renderLane();
    act(() => result.current.selection.togglePage());
    act(() => result.current.setPage(2));
    await waitFor(() => expect(result.current.candidates[0]?.id).toBe(51));
    expect(result.current.selection.selectedCount).toBe(0);

    act(() => result.current.selection.togglePage());
    act(() => result.current.selection.selectAllMatching());
    act(() => result.current.setPage(3));
    await waitFor(() => expect(result.current.candidates[0]?.id).toBe(101));
    expect(result.current.selection.allMatching).toBe(true);
    expect(result.current.selection.isSelected(101)).toBe(true);
  });

  it('a filter change or page-size change clears the cross-page selection', async () => {
    const { result, rerender } = await renderLane();
    act(() => result.current.selection.togglePage());
    act(() => result.current.selection.selectAllMatching());
    rerender({ u: { band: 'REVIEW', entityId: null } });
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.selection.allMatching).toBe(false);
    expect(result.current.selection.selectedCount).toBe(0);

    act(() => result.current.selection.togglePage());
    act(() => result.current.selection.selectAllMatching());
    act(() => result.current.setPageSize(100));
    expect(result.current.selection.allMatching).toBe(false);
    expect(result.current.selection.selectedCount).toBe(0);
  });

  it('shift range selects forward and deselects from an unchecked anchor', async () => {
    const { result } = await renderLane();
    act(() => result.current.toggleSelect(2, 1));
    act(() => result.current.toggleSelect(5, 4, true));
    expect([...result.current.selectedIds].sort()).toEqual([2, 3, 4, 5]);

    act(() => result.current.toggleSelect(4, 3)); // anchor 4, now unchecked
    act(() => result.current.toggleSelect(2, 1, true));
    expect([...result.current.selectedIds].sort()).toEqual([5]);
  });

  it('is not offered when the bulk endpoints cannot express the filter', async () => {
    const { result } = await renderLane();
    act(() => result.current.setFilters({ bothUnmatched: true }));
    await waitFor(() => expect(result.current.loading).toBe(false));
    act(() => result.current.selection.togglePage());
    expect(result.current.selectAllMatchingDisabledReason).toBe(MERGE_ALL_BLOCKED_REASON);
    expect(result.current.selection.showSelectAllMatching).toBe(false);

    act(() => result.current.setFilters({ bothUnmatched: false, status: '' }));
    await waitFor(() => expect(result.current.loading).toBe(false));
    act(() => result.current.selection.togglePage());
    expect(result.current.selectAllMatchingDisabledReason).toBe(
      SELECT_ALL_MATCHING_PENDING_ONLY_REASON
    );
    expect(result.current.selection.showSelectAllMatching).toBe(false);
  });

  it('toggleSelect keeps its identity across clicks, so memoised rows do not all re-render', async () => {
    // DupesSpine builds every row's handlers from onToggleSelect. A new
    // reference per click re-renders the whole page (up to 100 rows) per click.
    const { result } = await renderLane();
    const before = result.current.toggleSelect;
    act(() => result.current.toggleSelect(1, 0));
    act(() => result.current.toggleSelect(3, 2, true));
    expect(result.current.selectedIds.size).toBe(3);
    expect(result.current.toggleSelect).toBe(before);
  });

  it('dismissAllFiltered sends the exact on-screen filter to bulk-reject and reports its counts', async () => {
    const { result } = await renderLane({ band: 'REVIEW', entityId: 'book-7' });
    await act(async () => {
      result.current.dispatch({ lane: 'dupes', type: 'dismissAllFiltered', expectedTotal: TOTAL });
    });
    expect(api.bulkRejectDedupCandidates).toHaveBeenCalledWith({
      entity_type: 'book',
      status: 'pending',
      band: 'REVIEW',
      entity_id: 'book-7',
      q: undefined,
      expected_total: TOTAL,
    });
    expect(toast).toHaveBeenCalledWith(
      `Bulk dismiss: ${TOTAL - 1} dismissed, 1 failed of ${TOTAL}`,
      'warning'
    );
  });

  it('dismissAllFiltered is refused in dispatch when the filter is not Pending', async () => {
    const { result } = await renderLane();
    act(() => result.current.setFilters({ status: 'merged' }));
    await waitFor(() => expect(result.current.loading).toBe(false));
    act(() =>
      result.current.dispatch({ lane: 'dupes', type: 'dismissAllFiltered', expectedTotal: TOTAL })
    );
    expect(api.bulkRejectDedupCandidates).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledWith(SELECT_ALL_MATCHING_PENDING_ONLY_REASON, 'warning');
  });
});

function Harness() {
  const dupes = useDupesLane(toast, true, { band: null, entityId: null });
  return (
    <DupesPanel dupes={dupes} viewMode="compact" expandedId={null} onToggleExpand={() => {}} />
  );
}

function renderPanel() {
  return render(
    <MemoryRouter>
      <Harness />
    </MemoryRouter>
  );
}

describe('DupesPanel select-all integration', () => {
  it('banner shows "counting" until the server count arrives, never the list total', async () => {
    let resolve: (n: number) => void = () => {};
    vi.mocked(api.countBulkDedupCandidates).mockImplementation(
      () => new Promise<number>((r) => (resolve = r))
    );
    const user = userEvent.setup();
    renderPanel();
    await screen.findByTestId('dupes-row-5');
    await user.click(screen.getByRole('checkbox', { name: 'Select all 5 on this page' }));
    const link = await screen.findByTestId('dupes-select-all-matching');
    expect(link).toHaveTextContent('Select all matching (counting…)');
    expect(link).not.toHaveTextContent(String(TOTAL));
    act(() => resolve(COUNT));
    await waitFor(() => expect(link).toHaveTextContent(`Select all ${COUNT} matching`));
  });

  it('a failed count labels the list total as approximate', async () => {
    vi.mocked(api.countBulkDedupCandidates).mockRejectedValue(new Error('boom'));
    const user = userEvent.setup();
    renderPanel();
    await screen.findByTestId('dupes-row-5');
    await user.click(screen.getByRole('checkbox', { name: 'Select all 5 on this page' }));
    await waitFor(() =>
      expect(screen.getByTestId('dupes-select-all-matching')).toHaveTextContent(
        `Select all about ${TOTAL} matching`
      )
    );
  });

  it('select page -> banner -> select all matching -> confirmed cross-page merge hits bulk-link', async () => {
    const user = userEvent.setup();
    renderPanel();
    await screen.findByTestId('dupes-row-5');

    await user.click(screen.getByRole('checkbox', { name: 'Select all 5 on this page' }));
    const banner = screen.getByTestId('dupes-select-all-banner');
    expect(banner).toHaveTextContent('All 5 pairs on this page are selected.');
    // The banner offers the SERVER's count -- the number the dialog will
    // show -- not the list's total.
    await waitFor(() =>
      expect(within(banner).getByTestId('dupes-select-all-matching')).toHaveTextContent(
        `Select all ${COUNT} matching`
      )
    );

    await user.click(within(banner).getByTestId('dupes-select-all-matching'));
    expect(screen.getByTestId('dupes-select-all-banner')).toHaveTextContent(
      `All ${COUNT} pairs matching this filter are selected.`
    );
    expect(screen.getByTestId('dupes-selected-count')).toHaveTextContent(`${COUNT} selected`);

    await user.click(screen.getByTestId('merge-selected'));
    // Destructive and wider than the page: confirmation with the count.
    const dialog = await screen.findByTestId('dupes-bulk-confirm');
    // The server's count, not the list total.
    await waitFor(() => expect(dialog).toHaveTextContent(`Merge all ${COUNT} matching pairs?`));
    expect(api.bulkLinkDedupCandidates).not.toHaveBeenCalled();
    await user.click(within(dialog).getByTestId('dupes-bulk-confirm-btn'));

    await waitFor(() => expect(api.bulkLinkDedupCandidates).toHaveBeenCalledTimes(1));
    expect(api.bulkLinkDedupCandidates).toHaveBeenCalledWith(
      expect.objectContaining({ entity_type: 'book', status: 'pending', expected_total: COUNT })
    );
    // The count was asked for the same book-only pending filter.
    expect(api.countBulkDedupCandidates).toHaveBeenCalledWith(
      expect.objectContaining({ entity_type: 'book', status: 'pending' }),
      expect.anything()
    );
    expect(api.linkDedupCandidate).not.toHaveBeenCalled();
  });

  it('cross-page dismiss goes to bulk-reject after confirmation; Clear selection resets', async () => {
    const user = userEvent.setup();
    renderPanel();
    await screen.findByTestId('dupes-row-5');
    await user.click(screen.getByRole('checkbox', { name: 'Select all 5 on this page' }));
    await user.click(screen.getByTestId('dupes-select-all-matching'));
    await user.click(screen.getByTestId('dismiss-selected'));
    const dialog = await screen.findByTestId('dupes-bulk-confirm');
    await waitFor(() => expect(dialog).toHaveTextContent(`Dismiss all ${COUNT} matching pairs?`));
    await user.click(within(dialog).getByTestId('dupes-bulk-confirm-btn'));
    await waitFor(() => expect(api.bulkRejectDedupCandidates).toHaveBeenCalledTimes(1));
    expect(api.rejectDedupCandidate).not.toHaveBeenCalled();
  });

  it('merge-everything is offered only under Pending, and its dialog states the count', async () => {
    const user = userEvent.setup();
    renderPanel();
    await screen.findByTestId('dupes-row-5');
    await user.click(screen.getByTestId('merge-all-filtered'));
    const d1 = await screen.findByTestId('dupes-bulk-confirm');
    await waitFor(() => expect(d1).toHaveTextContent(`Merge all ${COUNT} matching pairs?`));
    await user.click(screen.getByRole('button', { name: 'Cancel' }));

    // Under "All" (or Dismissed) a filtered merge would overturn verdicts.
    // The dialog's close transition hides the page until it ends.
    await user.click(await screen.findByRole('combobox', { name: 'Status' }));
    await user.click(await screen.findByRole('option', { name: 'Dismissed' }));
    await waitFor(() => expect(screen.getByTestId('merge-all-filtered')).toBeDisabled());
  });

  it('lists book pairs only, so pending author pairs never inflate the total', async () => {
    await renderLane();
    expect(api.getDedupCandidates).toHaveBeenCalledWith(
      expect.objectContaining({ entity_type: 'book' }),
      expect.anything()
    );
  });

  it('a 409 FILTER_CHANGED refreshes and asks to re-confirm instead of reporting failure', async () => {
    vi.mocked(api.bulkRejectDedupCandidates).mockRejectedValue(
      new api.ApiError('moved', 409, {
        code: 'FILTER_CHANGED',
        expected_total: TOTAL,
        matched: TOTAL + 3,
      })
    );
    // services/api is auto-mocked, so the real filterChangedOf (unit-tested
    // separately) is stubbed to recognise this rejection.
    vi.mocked(api.filterChangedOf).mockReturnValue({ expected: TOTAL, matched: TOTAL + 3 });
    const { result } = await renderLane();
    const fetchesBefore = vi.mocked(api.getDedupCandidates).mock.calls.length;
    await act(async () => {
      result.current.dispatch({ lane: 'dupes', type: 'dismissAllFiltered', expectedTotal: TOTAL });
    });
    expect(toast).toHaveBeenCalledWith(filterChangedMessage(TOTAL + 3), 'warning');
    await waitFor(() =>
      expect(vi.mocked(api.getDedupCandidates).mock.calls.length).toBeGreaterThan(fetchesBefore)
    );
  });

  it('bulk dismiss offers Undo, which reverts exactly the dismissed ids', async () => {
    vi.mocked(api.bulkRejectDedupCandidates).mockResolvedValue({
      attempted: 3,
      rejected: 2,
      failed: 1,
      rejected_ids: [11, 12],
    });
    vi.mocked(api.revertBulkRejectDedupCandidates).mockResolvedValue({
      attempted: 2,
      reverted: 2,
      failed: 0,
    });
    const { result } = await renderLane();
    await act(async () => {
      result.current.dispatch({ lane: 'dupes', type: 'dismissAllFiltered', expectedTotal: TOTAL });
    });
    const call = toast.mock.calls.find((c) => String(c[0]).startsWith('Bulk dismiss'));
    const action = call?.[2] as { label: string; onClick: () => void } | undefined;
    expect(action?.label).toBe('Undo');
    await act(async () => action!.onClick());
    expect(api.revertBulkRejectDedupCandidates).toHaveBeenCalledWith([11, 12]);
    await waitFor(() => expect(toast).toHaveBeenCalledWith('Undo: 2 back to pending', 'success'));
  });

  it('a failed count shows an error and cannot be confirmed', async () => {
    const user = userEvent.setup();
    vi.mocked(api.countBulkDedupCandidates).mockRejectedValue(new Error('count broke'));
    renderPanel();
    await screen.findByTestId('dupes-row-5');
    await user.click(screen.getByTestId('merge-all-filtered'));
    expect(await screen.findByTestId('dupes-bulk-count-error')).toHaveTextContent('count broke');
    expect(screen.getByTestId('dupes-bulk-confirm-btn')).toBeDisabled();
    expect(api.bulkLinkDedupCandidates).not.toHaveBeenCalled();
  });

  it('shift-click on a row checkbox selects the range; Shift+Space does too', async () => {
    const user = userEvent.setup();
    renderPanel();
    await screen.findByTestId('dupes-row-5');
    const box = (id: number) => screen.getByRole('checkbox', { name: `Select candidate ${id}` });

    await user.click(box(1));
    await user.keyboard('{Shift>}');
    await user.click(box(3));
    await user.keyboard('{/Shift}');
    expect(screen.getByTestId('dupes-selected-count')).toHaveTextContent('3 selected');

    box(5).focus();
    await user.keyboard('{Shift>}[Space]{/Shift}');
    expect(screen.getByTestId('dupes-selected-count')).toHaveTextContent('5 selected');
    expect(box(4)).toBeChecked();
  });
});
