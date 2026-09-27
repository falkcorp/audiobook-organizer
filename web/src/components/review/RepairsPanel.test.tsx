// file: web/src/components/review/RepairsPanel.test.tsx
// version: 1.0.0
// guid: 3a7e0c95-4d21-4b8f-b6e3-8f1c2d9a5e47
// last-edited: 2026-09-27
//
// The repairs surface, rendered over the real lane hook with a mocked API, so
// the clicks go through the same dispatch the workspace uses.

import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import * as api from '../../services/api';
import type { OperationV2, RepairFixer, RepairRow, RepairRowsFilter } from '../../services/api';
import { RepairsPanel } from './RepairsPanel';
import { useRepairsLane } from './lanes/useRepairsLane';

vi.mock('../../services/api');

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
    reason: `reason ${id}`,
    risk: 'low',
    fingerprint: `fp-${id}`,
    ...extra,
  };
}

const APPLICABLE = [row('g1'), row('g2', { risk: 'review' })];
const SKIPPED = [row('g3', { skipped: 'skipped_itunes', skip_reason: 'Lives in the iTunes library' })];

const toast = vi.fn();

function Harness() {
  const repairs = useRepairsLane(toast, true);
  return <RepairsPanel repairs={repairs} />;
}

function renderPanel() {
  return render(
    <MemoryRouter>
      <Harness />
    </MemoryRouter>
  );
}

let confirmSpy: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
  vi.resetAllMocks();
  window.localStorage.clear();
  confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true);
  vi.mocked(api.listRepairFixers).mockResolvedValue([FIXER]);
  vi.mocked(api.getRepairPlanRows).mockImplementation(
    async (fixerId: string, planOpId: string, q: { filter: RepairRowsFilter; offset: number; limit: number }) => {
      const all = q.filter === 'applicable' ? APPLICABLE : SKIPPED;
      return {
        plan_op_id: planOpId,
        fixer_id: fixerId,
        planned_at: '2026-09-27T10:01:00Z',
        filter: q.filter,
        offset: q.offset,
        limit: q.limit,
        total: all.length,
        applicable: APPLICABLE.length,
        skipped_by_kind: { skipped_itunes: SKIPPED.length },
        rows: all.slice(q.offset, q.offset + q.limit),
      };
    }
  );
  vi.mocked(api.startRepairApply).mockResolvedValue({
    operation_id: 'apply-1',
    def_id: 'repairs.apply',
    fixer_id: 'vg-primary',
    status: 'queued',
  });
  vi.mocked(api.pollOperationV2).mockResolvedValue({
    id: 'apply-1',
    status: 'completed',
    error_message: null,
  } as OperationV2);
  vi.mocked(api.getRepairApplyResult).mockResolvedValue({
    fixer_id: 'vg-primary',
    plan_op_id: 'plan-1',
    dry_run: false,
    requested: 1,
    by_outcome: { applied: 1 },
    applied: 1,
    changed_since_plan: 0,
    partially_applied: 0,
    failed: 0,
    standdown_held: true,
    rows: [{ row_id: 'g2', outcome: 'applied' }],
  });
});

afterEach(() => {
  confirmSpy.mockRestore();
});

describe('RepairsPanel', () => {
  it('lists the fixer with its last trial summary and shows the plan rows', async () => {
    renderPanel();
    const fixer = await screen.findByTestId('repairs-fixer-vg-primary');
    expect(within(fixer).getByText('Version group primary')).toBeInTheDocument();
    expect(screen.getByTestId('repairs-fixer-plan-vg-primary')).toHaveTextContent(
      /2 applicable of 3/
    );
    const r1 = await screen.findByTestId('repairs-row-g1');
    expect(within(r1).getByRole('link', { name: 'Title g1' })).toHaveAttribute(
      'href',
      '/library/book-g1'
    );
    expect(within(r1).getByText('reason g1')).toBeInTheDocument();
    expect(within(screen.getByTestId('repairs-row-g2')).getByText('Review')).toBeInTheDocument();
    // The scan note is present and says "pauses", not "blocked".
    expect(screen.getAllByText(/pauses briefly/).length).toBeGreaterThan(0);
    expect(screen.queryByText(/blocked/i)).not.toBeInTheDocument();
  });

  it('applies exactly the checked row with dry_run off, then shows its outcome', async () => {
    const user = userEvent.setup();
    renderPanel();
    await screen.findByTestId('repairs-row-g2');
    await user.click(screen.getByRole('checkbox', { name: 'Select Title g2' }));
    expect(screen.getByTestId('repairs-selected-count')).toHaveTextContent('1 selected');

    await user.click(screen.getByTestId('repairs-apply-selected'));

    await screen.findByTestId('repairs-apply-result');
    expect(confirmSpy).toHaveBeenCalledTimes(1);
    expect(confirmSpy.mock.calls[0][0]).toMatch(/^Apply 1 repair row/);
    expect(api.startRepairApply).toHaveBeenCalledTimes(1);
    expect(api.startRepairApply).toHaveBeenCalledWith('vg-primary', 'plan-1', ['g2']);
    expect(screen.getByTestId('repairs-outcome-g2')).toHaveTextContent('Applied');
    expect(screen.queryByTestId('repairs-outcome-g1')).not.toBeInTheDocument();
    expect(screen.getByTestId('repairs-apply-result')).toHaveTextContent(/Re-run trial/);
  });

  it('sends nothing when the confirm is cancelled', async () => {
    const user = userEvent.setup();
    confirmSpy.mockReturnValue(false);
    renderPanel();
    await screen.findByTestId('repairs-row-g1');
    await user.click(screen.getByRole('checkbox', { name: 'Select Title g1' }));
    await user.click(screen.getByTestId('repairs-apply-selected'));
    await user.click(screen.getByTestId('repairs-apply-all'));
    expect(confirmSpy).toHaveBeenCalledTimes(2);
    expect(confirmSpy.mock.calls[1][0]).toMatch(/^Apply 2 repair row/);
    expect(api.startRepairApply).not.toHaveBeenCalled();
    expect(screen.queryByTestId('repairs-apply-result')).not.toBeInTheDocument();
  });

  it('shows skipped rows with their reason and no checkbox', async () => {
    const user = userEvent.setup();
    renderPanel();
    await screen.findByTestId('repairs-row-g1');
    await user.click(screen.getByTestId('repairs-tab-skipped'));
    const skipped = await screen.findByTestId('repairs-row-g3');
    expect(within(skipped).getByText('Lives in the iTunes library')).toBeInTheDocument();
    expect(within(skipped).queryByRole('checkbox')).not.toBeInTheDocument();
    expect(screen.queryByTestId('repairs-apply-selected')).not.toBeInTheDocument();
  });

  it('renders a fixer-list failure as an error with retry', async () => {
    vi.mocked(api.listRepairFixers).mockRejectedValue(new Error('server said no'));
    renderPanel();
    const err = await screen.findByTestId('repairs-fixers-error');
    expect(err).toHaveTextContent('server said no');
    expect(screen.queryByTestId('repairs-no-fixers')).not.toBeInTheDocument();
  });

  it('renders an empty fixer list as empty, not as an error', async () => {
    vi.mocked(api.listRepairFixers).mockResolvedValue([]);
    renderPanel();
    expect(await screen.findByTestId('repairs-no-fixers')).toBeInTheDocument();
    expect(screen.queryByTestId('repairs-fixers-error')).not.toBeInTheDocument();
  });

  it('renders a rows failure as an error, distinct from an empty trial', async () => {
    vi.mocked(api.getRepairPlanRows).mockRejectedValue(new Error('rows exploded'));
    renderPanel();
    expect(await screen.findByTestId('repairs-rows-error')).toHaveTextContent('rows exploded');
    expect(screen.queryByTestId('repairs-rows-empty')).not.toBeInTheDocument();
  });

  it('says so when a trial found nothing to repair', async () => {
    vi.mocked(api.getRepairPlanRows).mockImplementation(async (fixerId, planOpId, q) => ({
      plan_op_id: planOpId,
      fixer_id: fixerId,
      planned_at: '2026-09-27T10:01:00Z',
      filter: q.filter,
      offset: 0,
      limit: q.limit,
      total: 0,
      applicable: 0,
      skipped_by_kind: {},
      rows: [],
    }));
    renderPanel();
    expect(await screen.findByTestId('repairs-rows-empty')).toHaveTextContent(/nothing to repair/);
    expect(screen.getByTestId('repairs-apply-all')).toBeDisabled();
  });

  it('offers Run trial when the fixer has no trial yet', async () => {
    const user = userEvent.setup();
    vi.mocked(api.listRepairFixers).mockResolvedValue([{ ...FIXER, last_plan: undefined }]);
    vi.mocked(api.startRepairPlan).mockResolvedValue({
      operation_id: 'plan-2',
      def_id: 'repairs.plan',
      fixer_id: 'vg-primary',
      status: 'queued',
    });
    vi.mocked(api.pollOperationV2).mockReturnValue(new Promise(() => {}));
    renderPanel();
    const empty = await screen.findByTestId('repairs-no-trial');
    await user.click(within(empty).getByRole('button', { name: 'Run trial' }));
    expect(await screen.findByTestId('repairs-trial-running')).toBeInTheDocument();
    expect(api.startRepairPlan).toHaveBeenCalledWith('vg-primary');
    expect(screen.getByTestId('repairs-run-trial-vg-primary')).toBeDisabled();
  });
});
