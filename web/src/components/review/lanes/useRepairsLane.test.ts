// file: web/src/components/review/lanes/useRepairsLane.test.ts
// version: 1.0.0
// guid: 2e8c6b14-7f39-4a50-9d21-b4a7e3c9f615
// last-edited: 2026-09-27
//
// The repairs lane's data layer against a mocked API.
//
// The api module is automocked, which replaces pollOperationV2 as well, so the
// poll is stubbed per test with the terminal op it should report. The request
// body the apply sends (dry_run:false) is asserted one level down, in
// services/api.repairs.test.ts, where the real client builds it.

import { act, renderHook, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi, type Mock } from 'vitest';
import * as api from '../../../services/api';
import type {
  OperationV2,
  RepairApplyResult,
  RepairFixer,
  RepairRow,
  RepairRowsFilter,
  RepairRowsPage,
} from '../../../services/api';
import {
  REPAIRS_FIXER_STORAGE_KEY,
  repairApplyConfirmMessage,
  useRepairsLane,
} from './useRepairsLane';

vi.mock('../../../services/api');

const FIXER: RepairFixer = {
  id: 'vg-primary',
  title: 'Version group primary',
  description: 'Crown one primary per version group.',
  last_plan: {
    operation_id: 'plan-1',
    status: 'completed',
    queued_at: '2026-09-27T10:00:00Z',
    completed_at: '2026-09-27T10:01:00Z',
    total: 3,
    applicable: 2,
    skipped_by_kind: { skipped_itunes: 1 },
  },
};

function row(id: string, extra: Partial<RepairRow> = {}): RepairRow {
  return {
    row_id: id,
    book_ids: [`book-${id}`],
    title: `Title ${id}`,
    author: 'Author',
    current: { primary: 'false' },
    proposed: { primary: 'true' },
    reason: 'organized copy',
    risk: 'low',
    fingerprint: `fp-${id}`,
    ...extra,
  };
}

const APPLICABLE = [row('g1'), row('g2')];
const SKIPPED = [row('g3', { skipped: 'skipped_itunes', skip_reason: 'iTunes library path' })];

/** A rows mock that honours plan, filter, offset and limit. */
function mockRows(plans: Record<string, { applicable: RepairRow[]; skipped: RepairRow[] }>) {
  vi.mocked(api.getRepairPlanRows).mockImplementation(
    async (fixerId: string, planOpId: string, q: { filter: RepairRowsFilter; offset: number; limit: number }) => {
      const plan = plans[planOpId];
      if (!plan) throw Object.assign(new Error('repair plan not found'), { status: 404 });
      const all = q.filter === 'applicable' ? plan.applicable : plan.skipped;
      const page: RepairRowsPage = {
        plan_op_id: planOpId,
        fixer_id: fixerId,
        planned_at: '2026-09-27T10:01:00Z',
        filter: q.filter,
        offset: q.offset,
        limit: q.limit,
        total: all.length,
        applicable: plan.applicable.length,
        skipped_by_kind: { skipped_itunes: plan.skipped.length },
        rows: all.slice(q.offset, q.offset + q.limit),
      };
      return page;
    }
  );
}

function op(id: string, status: OperationV2['status'], error: string | null = null): OperationV2 {
  return { id, status, error_message: error, completed_at: '2026-09-27T11:00:00Z' } as OperationV2;
}

function applyResult(extra: Partial<RepairApplyResult> = {}): RepairApplyResult {
  return {
    fixer_id: FIXER.id,
    plan_op_id: 'plan-1',
    dry_run: false,
    requested: 1,
    by_outcome: { applied: 1 },
    applied: 1,
    changed_since_plan: 0,
    partially_applied: 0,
    failed: 0,
    standdown_held: false,
    rows: [{ row_id: 'g1', outcome: 'applied' }],
    ...extra,
  };
}

let toast: Mock<(message: string, severity?: 'success' | 'error' | 'warning' | 'info') => void>;
let confirmSpy: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  vi.resetAllMocks();
  window.localStorage.clear();
  toast = vi.fn();
  confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true);
  vi.mocked(api.listRepairFixers).mockResolvedValue([FIXER]);
  mockRows({ 'plan-1': { applicable: APPLICABLE, skipped: SKIPPED } });
});

afterEach(() => {
  confirmSpy.mockRestore();
});

async function renderLane(active = true) {
  const hook = renderHook(({ a }) => useRepairsLane(toast, a), { initialProps: { a: active } });
  if (active) {
    await waitFor(() => expect(hook.result.current.rows).toHaveLength(2));
  }
  return hook;
}

describe('useRepairsLane: loading', () => {
  it('makes no request while inactive', async () => {
    renderHook(() => useRepairsLane(toast, false));
    await new Promise((r) => setTimeout(r, 10));
    expect(api.listRepairFixers).not.toHaveBeenCalled();
    expect(api.getRepairPlanRows).not.toHaveBeenCalled();
    expect(api.pollOperationV2).not.toHaveBeenCalled();
  });

  it('loads the fixers, selects the first, and pages its last completed plan', async () => {
    const { result } = await renderLane();
    expect(result.current.selectedFixerId).toBe('vg-primary');
    expect(result.current.planOpId).toBe('plan-1');
    expect(api.getRepairPlanRows).toHaveBeenCalledTimes(1);
    expect(api.getRepairPlanRows).toHaveBeenCalledWith(
      'vg-primary',
      'plan-1',
      { filter: 'applicable', offset: 0, limit: 50 },
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    );
    expect(result.current.page?.applicable).toBe(2);
  });

  it('distinguishes a failed fixer list from an empty one', async () => {
    vi.mocked(api.listRepairFixers).mockRejectedValue(new Error('boom'));
    const { result } = renderHook(() => useRepairsLane(toast, true));
    await waitFor(() => expect(result.current.fixersError).toBe('boom'));
    expect(result.current.fixers).toEqual([]);
    expect(result.current.fixersLoading).toBe(false);
  });

  it('names a 404 as a server without the repairs endpoints', async () => {
    vi.mocked(api.listRepairFixers).mockRejectedValue(
      Object.assign(new Error('Not found'), { status: 404 })
    );
    const { result } = renderHook(() => useRepairsLane(toast, true));
    await waitFor(() => expect(result.current.fixersError).toMatch(/no repairs endpoints/i));
  });

  it('reports a rows failure as an error, not as an empty plan', async () => {
    vi.mocked(api.getRepairPlanRows).mockRejectedValue(new Error('rows exploded'));
    const { result } = renderHook(() => useRepairsLane(toast, true));
    await waitFor(() => expect(result.current.rowsError).toBe('rows exploded'));
    expect(result.current.page).toBeNull();
    expect(result.current.rowsLoading).toBe(false);
  });

  it('shows no plan for a fixer that has never run a trial', async () => {
    vi.mocked(api.listRepairFixers).mockResolvedValue([{ ...FIXER, last_plan: undefined }]);
    const { result } = renderHook(() => useRepairsLane(toast, true));
    await waitFor(() => expect(result.current.selectedFixerId).toBe('vg-primary'));
    expect(result.current.planOpId).toBeNull();
    expect(api.getRepairPlanRows).not.toHaveBeenCalled();
  });

  it('restores the fixer the viewer had open', async () => {
    window.localStorage.setItem(REPAIRS_FIXER_STORAGE_KEY, 'other');
    vi.mocked(api.listRepairFixers).mockResolvedValue([
      FIXER,
      { id: 'other', title: 'Other', description: '' },
    ]);
    const { result } = renderHook(() => useRepairsLane(toast, true));
    await waitFor(() => expect(result.current.selectedFixerId).toBe('other'));
  });
});

describe('useRepairsLane: trials', () => {
  it('runs a trial, polls it, then pages the NEW plan', async () => {
    const { result } = await renderLane();
    mockRows({
      'plan-1': { applicable: APPLICABLE, skipped: SKIPPED },
      'plan-2': { applicable: [row('n1')], skipped: [] },
    });
    vi.mocked(api.startRepairPlan).mockResolvedValue({
      operation_id: 'plan-2',
      def_id: 'repairs.plan',
      fixer_id: 'vg-primary',
      status: 'queued',
    });
    let finish!: (o: OperationV2) => void;
    vi.mocked(api.pollOperationV2).mockReturnValue(new Promise((r) => (finish = r)));

    act(() => result.current.dispatch({ lane: 'repairs', type: 'runTrial', fixerId: 'vg-primary' }));
    await waitFor(() => expect(result.current.trial?.phase).toBe('running'));
    expect(api.startRepairPlan).toHaveBeenCalledWith('vg-primary');
    expect(api.pollOperationV2).toHaveBeenCalledWith(
      'plan-2',
      undefined,
      expect.any(Number),
      expect.objectContaining({ signal: expect.any(AbortSignal) })
    );
    // The previous plan's rows stay on screen while the trial runs.
    expect(result.current.planOpId).toBe('plan-1');
    expect(result.current.rows).toHaveLength(2);

    await act(async () => finish(op('plan-2', 'completed')));
    await waitFor(() => expect(result.current.planOpId).toBe('plan-2'));
    await waitFor(() => expect(result.current.rows.map((r) => r.row_id)).toEqual(['n1']));
    expect(result.current.trial).toBeNull();
  });

  it('reports a failed trial with the operation error', async () => {
    const { result } = await renderLane();
    vi.mocked(api.startRepairPlan).mockResolvedValue({
      operation_id: 'plan-2',
      def_id: 'repairs.plan',
      fixer_id: 'vg-primary',
      status: 'queued',
    });
    vi.mocked(api.pollOperationV2).mockResolvedValue(op('plan-2', 'failed', 'store unavailable'));
    act(() => result.current.dispatch({ lane: 'repairs', type: 'runTrial', fixerId: 'vg-primary' }));
    await waitFor(() =>
      expect(result.current.trial).toEqual({ opId: 'plan-2', phase: 'failed', message: 'store unavailable' })
    );
    // The completed plan is still the one shown.
    expect(result.current.planOpId).toBe('plan-1');
    expect(toast).toHaveBeenCalledWith('Trial failed: store unavailable', 'error');
  });

  it('follows a trial the server reports as already running', async () => {
    vi.mocked(api.listRepairFixers).mockResolvedValue([
      { ...FIXER, last_plan: { operation_id: 'plan-9', status: 'running', queued_at: '2026-09-27T10:00:00Z' } },
    ]);
    vi.mocked(api.pollOperationV2).mockReturnValue(new Promise(() => {}));
    const { result } = renderHook(() => useRepairsLane(toast, true));
    await waitFor(() => expect(result.current.trial?.phase).toBe('running'));
    expect(api.pollOperationV2).toHaveBeenCalledTimes(1);
    expect(vi.mocked(api.pollOperationV2).mock.calls[0][0]).toBe('plan-9');
  });

  it('stops polling when the lane goes inactive', async () => {
    vi.mocked(api.listRepairFixers).mockResolvedValue([
      { ...FIXER, last_plan: { operation_id: 'plan-9', status: 'running', queued_at: '2026-09-27T10:00:00Z' } },
    ]);
    vi.mocked(api.pollOperationV2).mockReturnValue(new Promise(() => {}));
    const { result, rerender } = renderHook(({ a }) => useRepairsLane(toast, a), {
      initialProps: { a: true },
    });
    await waitFor(() => expect(api.pollOperationV2).toHaveBeenCalled());
    const signal = vi.mocked(api.pollOperationV2).mock.calls[0][3]?.signal;
    expect(signal?.aborted).toBe(false);
    rerender({ a: false });
    expect(signal?.aborted).toBe(true);
    expect(result.current.active).toBe(false);
  });
});

describe('useRepairsLane: selection and apply', () => {
  function stubApply(result: RepairApplyResult = applyResult()) {
    vi.mocked(api.startRepairApply).mockResolvedValue({
      operation_id: 'apply-1',
      def_id: 'repairs.apply',
      fixer_id: 'vg-primary',
      status: 'queued',
    });
    vi.mocked(api.pollOperationV2).mockResolvedValue(op('apply-1', 'completed'));
    vi.mocked(api.getRepairApplyResult).mockResolvedValue(result);
  }

  it('applies exactly the selected rows, after one confirm naming the count', async () => {
    const { result } = await renderLane();
    stubApply();
    act(() => result.current.toggleRow('g1'));
    expect([...result.current.selectedRowIds]).toEqual(['g1']);

    act(() =>
      result.current.dispatch({
        lane: 'repairs',
        type: 'applyRows',
        fixerId: 'vg-primary',
        planOpId: 'plan-1',
        rowIds: [...result.current.selectedRowIds],
      })
    );
    await waitFor(() => expect(result.current.applyResult).not.toBeNull());

    expect(confirmSpy).toHaveBeenCalledTimes(1);
    expect(confirmSpy).toHaveBeenCalledWith(repairApplyConfirmMessage(1, 'Version group primary'));
    expect(api.startRepairApply).toHaveBeenCalledTimes(1);
    expect(api.startRepairApply).toHaveBeenCalledWith('vg-primary', 'plan-1', ['g1']);
    expect(result.current.rowOutcomes.get('g1')?.outcome).toBe('applied');
    expect(result.current.selectedRowIds.size).toBe(0);
    expect(toast).toHaveBeenCalledWith(
      'Applied 1 · changed since trial 0 · failed 0',
      'success'
    );
  });

  it('sends nothing when the confirm is cancelled', async () => {
    const { result } = await renderLane();
    stubApply();
    confirmSpy.mockReturnValue(false);
    act(() => result.current.toggleRow('g1'));
    act(() =>
      result.current.dispatch({
        lane: 'repairs',
        type: 'applyRows',
        fixerId: 'vg-primary',
        planOpId: 'plan-1',
        rowIds: ['g1'],
      })
    );
    const rowsCalls = vi.mocked(api.getRepairPlanRows).mock.calls.length;
    act(() =>
      result.current.dispatch({
        lane: 'repairs',
        type: 'applyAllApplicable',
        fixerId: 'vg-primary',
        planOpId: 'plan-1',
      })
    );
    await new Promise((r) => setTimeout(r, 10));
    expect(confirmSpy).toHaveBeenCalledTimes(2);
    expect(api.startRepairApply).not.toHaveBeenCalled();
    // Apply-all asks BEFORE resolving ids, so a cancel costs no request either.
    expect(vi.mocked(api.getRepairPlanRows).mock.calls.length).toBe(rowsCalls);
    expect(result.current.selectedRowIds.has('g1')).toBe(true);
  });

  it('apply-all confirms with the plan count and sends every applicable id across pages', async () => {
    const many = Array.from({ length: 620 }, (_, i) => row(`r${i}`));
    mockRows({ 'plan-1': { applicable: many, skipped: SKIPPED } });
    const { result } = renderHook(() => useRepairsLane(toast, true));
    await waitFor(() => expect(result.current.page?.applicable).toBe(620));
    stubApply(applyResult({ requested: 620 }));

    act(() =>
      result.current.dispatch({
        lane: 'repairs',
        type: 'applyAllApplicable',
        fixerId: 'vg-primary',
        planOpId: 'plan-1',
      })
    );
    await waitFor(() => expect(api.startRepairApply).toHaveBeenCalledTimes(1));
    expect(confirmSpy).toHaveBeenCalledWith(repairApplyConfirmMessage(620, 'Version group primary'));
    const ids = vi.mocked(api.startRepairApply).mock.calls[0][2];
    expect(ids).toHaveLength(620);
    expect(new Set(ids).size).toBe(620);
    expect(ids).toEqual(many.map((r) => r.row_id));
  });

  it('never selects a skipped row with select page', async () => {
    const { result } = await renderLane();
    act(() => result.current.setFilter('skipped'));
    await waitFor(() => expect(result.current.rows.map((r) => r.row_id)).toEqual(['g3']));
    act(() => result.current.selectPage());
    expect(result.current.selectedRowIds.size).toBe(0);

    act(() => result.current.setFilter('applicable'));
    await waitFor(() => expect(result.current.rows).toHaveLength(2));
    act(() => result.current.selectPage());
    expect([...result.current.selectedRowIds].sort()).toEqual(['g1', 'g2']);
  });

  it('surfaces a failed apply as an error with the operation message', async () => {
    const { result } = await renderLane();
    vi.mocked(api.startRepairApply).mockResolvedValue({
      operation_id: 'apply-1',
      def_id: 'repairs.apply',
      fixer_id: 'vg-primary',
      status: 'queued',
    });
    vi.mocked(api.pollOperationV2).mockResolvedValue(op('apply-1', 'failed', 'plan not completed'));
    vi.mocked(api.getRepairApplyResult).mockRejectedValue(new Error('no result'));
    act(() =>
      result.current.dispatch({
        lane: 'repairs',
        type: 'applyRows',
        fixerId: 'vg-primary',
        planOpId: 'plan-1',
        rowIds: ['g1'],
      })
    );
    await waitFor(() => expect(result.current.applyError).toBe('plan not completed'));
    expect(result.current.applying).toBe(false);
    expect(result.current.applyResult).toBeNull();
    expect(toast).toHaveBeenCalledWith('Apply failed: plan not completed', 'error');
  });

  it('surfaces a rejected apply request (e.g. 409) as an error', async () => {
    const { result } = await renderLane();
    vi.mocked(api.startRepairApply).mockRejectedValue(new Error('repairs: plan not completed'));
    act(() =>
      result.current.dispatch({
        lane: 'repairs',
        type: 'applyRows',
        fixerId: 'vg-primary',
        planOpId: 'plan-1',
        rowIds: ['g1'],
      })
    );
    await waitFor(() => expect(result.current.applyError).toBe('repairs: plan not completed'));
    expect(api.pollOperationV2).not.toHaveBeenCalled();
  });

  it('reports changed-since-trial and failed counts, warning when any row did not apply', async () => {
    const { result } = await renderLane();
    stubApply(
      applyResult({
        requested: 2,
        applied: 0,
        changed_since_plan: 1,
        failed: 1,
        by_outcome: { changed_since_plan: 1, failed: 1 },
        rows: [
          { row_id: 'g1', outcome: 'changed_since_plan' },
          { row_id: 'g2', outcome: 'failed', error: 'write refused' },
        ],
      })
    );
    act(() =>
      result.current.dispatch({
        lane: 'repairs',
        type: 'applyRows',
        fixerId: 'vg-primary',
        planOpId: 'plan-1',
        rowIds: ['g1', 'g2'],
      })
    );
    await waitFor(() => expect(result.current.applyResult).not.toBeNull());
    expect(toast).toHaveBeenCalledWith('Applied 0 · changed since trial 1 · failed 1', 'warning');
    expect(result.current.rowOutcomes.get('g2')?.error).toBe('write refused');
  });
});
