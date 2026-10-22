// file: web/src/components/review/lanes/useRepairsLane.test.ts
// version: 1.9.0
// guid: 2e8c6b14-7f39-4a50-9d21-b4a7e3c9f615
// last-edited: 2026-10-22
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
import { repairsApplyResult, repairsRowsPage } from '../../../test/repairsFixtures';
import {
  REPAIRS_FIXER_STORAGE_KEY,
  SETTLED_OUTCOMES,
  applySeverity,
  repairApplyConfirmMessage,
  repairOwnerApplyConfirmMessage,
  summarizeAppliedRows,
  summarizeApply,
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

/** The target the confirm and banner name for a plan row built by `row`. */
function target(id: string, members = 1) {
  return { rowId: id, title: `Title ${id}`, members };
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
      const page: RepairRowsPage = repairsRowsPage({
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
      });
      return page;
    }
  );
}

function op(id: string, status: OperationV2['status'], error: string | null = null): OperationV2 {
  return { id, status, error_message: error, completed_at: '2026-09-27T11:00:00Z' } as OperationV2;
}

function applyResult(extra: Partial<RepairApplyResult> = {}): RepairApplyResult {
  return repairsApplyResult({
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
  });
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

  it('flags a rows failure that only a re-run can cure', async () => {
    vi.mocked(api.getRepairPlanRows).mockRejectedValue(
      Object.assign(new Error('run the plan again'), { status: 409, data: { code: 'REPAIR_PLAN_NEEDS_RERUN' } })
    );
    const { result } = renderHook(() => useRepairsLane(toast, true));
    await waitFor(() => expect(result.current.rowsError).toBe('run the plan again'));
    expect(result.current.rowsNeedRerun).toBe(true);
  });

  it('does not flag an ordinary rows failure, or a 409 with another code, as needing a re-run', async () => {
    vi.mocked(api.getRepairPlanRows).mockRejectedValue(
      Object.assign(new Error('busy'), { status: 409, data: { code: 'CONFLICT' } })
    );
    const { result } = renderHook(() => useRepairsLane(toast, true));
    await waitFor(() => expect(result.current.rowsError).toBe('busy'));
    expect(result.current.rowsNeedRerun).toBe(false);
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

  describe('?fixer= seed', () => {
    const OTHER = { id: 'other', title: 'Other', description: '' };
    const THIRD = { id: 'third', title: 'Third', description: '' };

    it('preselects the seeded fixer over the one the viewer had open', async () => {
      window.localStorage.setItem(REPAIRS_FIXER_STORAGE_KEY, 'other');
      vi.mocked(api.listRepairFixers).mockResolvedValue([FIXER, OTHER, THIRD]);
      const { result } = renderHook(() => useRepairsLane(toast, true, 'third'));
      await waitFor(() => expect(result.current.selectedFixer?.id).toBe('third'));
      // A seed is never saved as the viewer's own choice.
      expect(window.localStorage.getItem(REPAIRS_FIXER_STORAGE_KEY)).toBe('other');
    });

    it('ignores an unknown id and falls back to the stored fixer', async () => {
      window.localStorage.setItem(REPAIRS_FIXER_STORAGE_KEY, 'other');
      vi.mocked(api.listRepairFixers).mockResolvedValue([FIXER, OTHER]);
      const { result } = renderHook(() => useRepairsLane(toast, true, 'no.such-fixer'));
      await waitFor(() => expect(result.current.selectedFixer?.id).toBe('other'));
      expect(result.current.selectedFixerId).toBe('other');
    });

    it('ignores an unknown id and falls back to the first fixer when nothing is stored', async () => {
      vi.mocked(api.listRepairFixers).mockResolvedValue([FIXER, OTHER]);
      const { result } = renderHook(() => useRepairsLane(toast, true, 'no.such-fixer'));
      await waitFor(() => expect(result.current.selectedFixer?.id).toBe('vg-primary'));
    });
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
    expect(confirmSpy).toHaveBeenCalledWith(
      repairApplyConfirmMessage(1, 'Version group primary', [target('g1')])
    );
    expect(api.startRepairApply).toHaveBeenCalledTimes(1);
    expect(api.startRepairApply).toHaveBeenCalledWith('vg-primary', 'plan-1', ['g1']);
    expect(result.current.rowOutcomes.get('g1')?.outcome).toBe('applied');
    expect(result.current.selectedRowIds.size).toBe(0);
    expect(toast).toHaveBeenCalledWith(
      'Applied 1 · changed since trial 0 · failed 0',
      'success'
    );
  });

  it('drops an apply dispatched for a preview-only fixer: no confirm, no request', async () => {
    vi.mocked(api.listRepairFixers).mockResolvedValue([{ ...FIXER, plan_only: true }]);
    const { result } = await renderLane();
    stubApply();
    act(() =>
      result.current.dispatch({
        lane: 'repairs',
        type: 'applyRows',
        fixerId: 'vg-primary',
        planOpId: 'plan-1',
        rowIds: ['g1'],
      })
    );
    act(() =>
      result.current.dispatch({ lane: 'repairs', type: 'applyAllApplicable', fixerId: 'vg-primary', planOpId: 'plan-1' })
    );
    // Let any (wrongly) started apply reach the API before asserting it did not.
    await act(async () => {
      await Promise.resolve();
    });
    expect(confirmSpy).not.toHaveBeenCalled();
    expect(api.startRepairApply).not.toHaveBeenCalled();
  });

  it('still runs a trial for a preview-only fixer', async () => {
    vi.mocked(api.listRepairFixers).mockResolvedValue([{ ...FIXER, plan_only: true }]);
    const { result } = await renderLane();
    vi.mocked(api.startRepairPlan).mockResolvedValue({
      operation_id: 'plan-2',
      def_id: 'repairs.plan',
      fixer_id: 'vg-primary',
      status: 'queued',
    });
    vi.mocked(api.pollOperationV2).mockResolvedValue(op('plan-2', 'completed'));
    act(() => result.current.dispatch({ lane: 'repairs', type: 'runTrial', fixerId: 'vg-primary' }));
    await waitFor(() => expect(api.startRepairPlan).toHaveBeenCalledTimes(1));
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
    // Apply-all asks BEFORE collecting ids: a cancel costs one small read (the
    // first rows, so the confirm can name them), not a walk of the whole plan.
    expect(vi.mocked(api.getRepairPlanRows).mock.calls.length).toBe(rowsCalls + 1);
    expect(vi.mocked(api.getRepairPlanRows).mock.calls.at(-1)?.[2]).toMatchObject({
      filter: 'applicable',
      offset: 0,
      limit: 10,
    });
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
    expect(confirmSpy).toHaveBeenCalledWith(
      repairApplyConfirmMessage(
        620,
        'Version group primary',
        many.slice(0, 10).map((r) => target(r.row_id))
      )
    );
    expect(confirmSpy.mock.calls[0][0]).toContain('and 610 more');
    const ids = vi.mocked(api.startRepairApply).mock.calls[0][2];
    expect(ids).toHaveLength(620);
    expect(new Set(ids).size).toBe(620);
    expect(ids).toEqual(many.map((r) => r.row_id));
  });

  it('apply-all ignores a second click while its pre-read is in flight: one confirm, buttons off', async () => {
    const { result } = await renderLane();
    stubApply(applyResult({ requested: 2 }));
    const base = vi.mocked(api.getRepairPlanRows).getMockImplementation()!;
    let release!: () => void;
    const gate = new Promise<void>((r) => {
      release = r;
    });
    vi.mocked(api.getRepairPlanRows).mockImplementationOnce(
      async (...args: Parameters<typeof api.getRepairPlanRows>) => {
        await gate;
        return base(...args);
      }
    );
    const applyAll = () =>
      result.current.dispatch({
        lane: 'repairs',
        type: 'applyAllApplicable',
        fixerId: 'vg-primary',
        planOpId: 'plan-1',
      });

    act(() => {
      applyAll();
      applyAll(); // a double click lands before the pre-read returns
    });
    await waitFor(() => expect(result.current.applyPreparing).toBe(true));
    expect(confirmSpy).not.toHaveBeenCalled();
    act(() => applyAll()); // and a third one, after the flag is visible

    release();
    await waitFor(() => expect(api.startRepairApply).toHaveBeenCalledTimes(1));
    expect(confirmSpy).toHaveBeenCalledTimes(1);
    expect(api.startRepairApply).toHaveBeenCalledTimes(1);
    await waitFor(() => expect(result.current.applyPreparing).toBe(false));
  });

  it('apply-all whose pre-read fails confirms with the count alone, then still applies', async () => {
    const { result } = await renderLane();
    stubApply(applyResult({ requested: 2 }));
    // Only the pre-read (the next call) fails; collecting the ids afterwards works.
    vi.mocked(api.getRepairPlanRows).mockRejectedValueOnce(new Error('rows unavailable'));
    act(() =>
      result.current.dispatch({
        lane: 'repairs',
        type: 'applyAllApplicable',
        fixerId: 'vg-primary',
        planOpId: 'plan-1',
      })
    );
    await waitFor(() => expect(api.startRepairApply).toHaveBeenCalledTimes(1));
    expect(confirmSpy).toHaveBeenCalledTimes(1);
    expect(confirmSpy).toHaveBeenCalledWith(repairApplyConfirmMessage(2, 'Version group primary'));
    expect(confirmSpy.mock.calls[0][0]).not.toContain('  - ');
    expect(vi.mocked(api.startRepairApply).mock.calls[0][2]).toEqual(['g1', 'g2']);
    expect(result.current.applyPreparing).toBe(false);
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
    await waitFor(() =>
      expect(result.current.applyError).toEqual({
        severity: 'error',
        message: 'Apply failed: plan not completed',
      })
    );
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
    await waitFor(() =>
      expect(result.current.applyError).toEqual({
        severity: 'error',
        message: 'Apply not started: repairs: plan not completed',
      })
    );
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

  it('does not call an apply it lost sight of a failed apply', async () => {
    // The op was enqueued and may be writing; a poll that times out knows
    // nothing about its outcome. Calling it "failed" invites a second apply.
    const { result } = await renderLane();
    vi.mocked(api.startRepairApply).mockResolvedValue({
      operation_id: 'apply-1',
      def_id: 'repairs.apply',
      fixer_id: 'vg-primary',
      status: 'queued',
    });
    vi.mocked(api.pollOperationV2).mockRejectedValue(new Error('Request timed out after 30000ms'));
    const listCalls = vi.mocked(api.listRepairFixers).mock.calls.length;
    act(() =>
      result.current.dispatch({
        lane: 'repairs',
        type: 'applyRows',
        fixerId: 'vg-primary',
        planOpId: 'plan-1',
        rowIds: ['g1'],
      })
    );
    await waitFor(() => expect(result.current.applyError).not.toBeNull());
    expect(result.current.applyError?.severity).toBe('warning');
    expect(result.current.applyError?.message).toMatch(/Lost track of apply apply-1/);
    expect(result.current.applyError?.message).toMatch(/may still be writing/);
    expect(result.current.applyError?.message).not.toMatch(/failed/i);
    // The rail reloads so its last-apply summary can show what really happened.
    await waitFor(() =>
      expect(vi.mocked(api.listRepairFixers).mock.calls.length).toBe(listCalls + 1)
    );
  });

  it('does not re-send rows an earlier apply settled, and keeps both applies\' badges', async () => {
    const { result } = await renderLane();
    stubApply(applyResult({ rows: [{ row_id: 'g1', outcome: 'applied' }] }));
    act(() =>
      result.current.dispatch({
        lane: 'repairs',
        type: 'applyRows',
        fixerId: 'vg-primary',
        planOpId: 'plan-1',
        rowIds: ['g1'],
      })
    );
    await waitFor(() => expect(result.current.settledRowIds.has('g1')).toBe(true));
    expect(result.current.remainingApplicable).toBe(1);

    // select page skips the settled row
    act(() => result.current.selectPage());
    expect([...result.current.selectedRowIds]).toEqual(['g2']);
    act(() => result.current.clearSelection());

    stubApply(applyResult({ rows: [{ row_id: 'g2', outcome: 'failed', error: 'x' }], failed: 1, applied: 0 }));
    confirmSpy.mockClear();
    act(() =>
      result.current.dispatch({
        lane: 'repairs',
        type: 'applyAllApplicable',
        fixerId: 'vg-primary',
        planOpId: 'plan-1',
      })
    );
    await waitFor(() => expect(api.startRepairApply).toHaveBeenCalledTimes(2));
    expect(confirmSpy).toHaveBeenCalledWith(
      repairApplyConfirmMessage(1, 'Version group primary', [target('g2')])
    );
    expect(vi.mocked(api.startRepairApply).mock.calls[1][2]).toEqual(['g2']);
    await waitFor(() => expect(result.current.rowOutcomes.get('g2')?.outcome).toBe('failed'));
    expect(result.current.rowOutcomes.get('g1')?.outcome).toBe('applied');
    // A failed row stays retryable.
    expect(result.current.settledRowIds.has('g2')).toBe(false);
  });
});

describe('useRepairsLane: naming the rows an apply writes', () => {
  it('formats the confirm: title and book count per row, ten at most, then "and N more"', () => {
    const t = Array.from({ length: 12 }, (_, i) => target(`r${i}`, i === 0 ? 1 : 3));
    const msg = repairApplyConfirmMessage(15, 'Split books', t);
    expect(msg).toContain('Apply 15 repair row(s) from "Split books"?');
    expect(msg).toContain('- "Title r0" (1 book)');
    expect(msg).toContain('- "Title r9" (3 books)');
    expect(msg).not.toContain('Title r10');
    expect(msg).toContain('and 5 more');
    // No titles known: the original one-line confirm.
    expect(repairApplyConfirmMessage(2, 'Split books')).toBe(
      'Apply 2 repair row(s) from "Split books"? This writes to the library, and each change ' +
        'is recorded in the book history. If a library scan is running it pauses briefly while ' +
        'the rows are written.'
    );
    // Exactly ten rows: nothing more to say.
    expect(repairApplyConfirmMessage(10, 'Split books', t.slice(0, 10))).not.toContain('more');
  });

  it('formats the banner naming of applied rows', () => {
    expect(summarizeAppliedRows([])).toBe('');
    expect(summarizeAppliedRows([target('a'), target('b', 4)])).toBe(
      'Applied: "Title a" (1 book), "Title b" (4 books).'
    );
    const many = Array.from({ length: 13 }, (_, i) => target(`r${i}`));
    const line = summarizeAppliedRows(many);
    expect(line).toContain('"Title r9" (1 book)');
    expect(line).not.toContain('Title r10');
    expect(line).toMatch(/, and 3 more\.$/);
  });

  it('leaves the book count out when it is unknown, never "(0 books)"', () => {
    const unknown = { rowId: 'r1', title: 'Title r1' };
    expect(summarizeAppliedRows([unknown, target('b', 2)])).toBe('Applied: "Title r1", "Title b" (2 books).');
    expect(repairApplyConfirmMessage(1, 'Split books', [unknown])).toContain('  - "Title r1"\n');
    expect(summarizeAppliedRows([unknown])).not.toContain('(0 books)');
  });

  it('names a row with several books by its member count, and carries the applied rows', async () => {
    mockRows({
      'plan-1': {
        applicable: [
          row('g1', {
            members: [
              { book_id: 'b1', files: 1 },
              { book_id: 'b2', files: 1 },
              { book_id: 'b3', files: 1 },
            ],
          }),
          row('g2'),
        ],
        skipped: SKIPPED,
      },
    });
    const { result } = renderHook(() => useRepairsLane(toast, true));
    await waitFor(() => expect(result.current.page?.applicable).toBe(2));
    vi.mocked(api.startRepairApply).mockResolvedValue({
      operation_id: 'apply-1',
      def_id: 'repairs.apply',
      fixer_id: 'vg-primary',
      status: 'queued',
    });
    vi.mocked(api.pollOperationV2).mockResolvedValue(op('apply-1', 'completed'));
    vi.mocked(api.getRepairApplyResult).mockResolvedValue(
      applyResult({
        rows: [
          { row_id: 'g1', outcome: 'applied' },
          { row_id: 'g2', outcome: 'failed', error: 'x' },
        ],
      })
    );
    act(() => {
      result.current.toggleRow('g1');
      result.current.toggleRow('g2');
    });
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
    expect(confirmSpy.mock.calls[0][0]).toContain('"Title g1" (3 books)');
    expect(confirmSpy.mock.calls[0][0]).toContain('"Title g2" (1 book)');
    // Only the row that was written is named in the banner.
    expect(result.current.appliedRows).toEqual([target('g1', 3)]);
  });
});

describe('useRepairsLane: retry_later', () => {
  it('names retry-later rows in the summary and keeps them selectable', () => {
    const r: RepairApplyResult = repairsApplyResult({
      fixer_id: 'maintenance.version-twin-metadata',
      plan_op_id: 'op-plan',
      dry_run: false,
      requested: 2,
      by_outcome: { applied: 1, retry_later: 1 },
      applied: 1,
      changed_since_plan: 0,
      partially_applied: 0,
      failed: 0,
      retry_later: 1,
      standdown_held: true,
      rows: [],
    });
    expect(summarizeApply(r)).toContain('retry later 1');
    expect(SETTLED_OUTCOMES.has('retry_later')).toBe(false);
  });
});

describe('useRepairsLane: canceled_skipped', () => {
  // A 10-row apply canceled after row 3: the server reports the other seven
  // canceled_skipped and leaves `aborted` unset (that field is the lost
  // stand-down lease, not a cancel).
  const canceled: RepairApplyResult = repairsApplyResult({
    fixer_id: 'maintenance.fragment-consolidation',
    plan_op_id: 'op-plan',
    dry_run: false,
    requested: 10,
    by_outcome: { applied: 3, canceled_skipped: 7 },
    applied: 3,
    changed_since_plan: 0,
    partially_applied: 0,
    failed: 0,
    standdown_held: true,
    rows: [],
  });

  it('counts canceled rows in the summary and reports a warning, not success', () => {
    expect(summarizeApply(canceled)).toContain('canceled before finishing 7');
    expect(applySeverity(canceled)).toBe('warning');
  });

  it('keeps canceled rows selectable, so they can be applied again', () => {
    expect(SETTLED_OUTCOMES.has('canceled_skipped')).toBe(false);
  });

  it('still reports success for an apply with nothing canceled', () => {
    const clean = { ...canceled, requested: 3, by_outcome: { applied: 3 } };
    expect(summarizeApply(clean)).not.toContain('canceled');
    expect(applySeverity(clean)).toBe('success');
  });
});

describe('repairOwnerApplyConfirmMessage', () => {
  it('says how many fragment books one click retires and that the parent is not written', () => {
    const many = repairOwnerApplyConfirmMessage('Synthetic Book', 'Fragment consolidation', 300);
    expect(many).toContain('retiring 300 fragment books into the parent');
    expect(many).toContain('The parent, every file and iTunes are not written');
    expect(many).toContain("operation's history under your name");
    expect(repairOwnerApplyConfirmMessage('Synthetic Book', 'Fragment consolidation', 1)).toContain(
      'retiring 1 fragment book into the parent'
    );
  });
});
