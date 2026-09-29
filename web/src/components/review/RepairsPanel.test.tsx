// file: web/src/components/review/RepairsPanel.test.tsx
// version: 1.4.0
// guid: 3a7e0c95-4d21-4b8f-b6e3-8f1c2d9a5e47
// last-edited: 2026-09-29
//
// The repairs surface, rendered over the real lane hook with a mocked API, so
// the clicks go through the same dispatch the workspace uses.

import { render, screen, within } from '@testing-library/react';
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
const SKIPPED = [
  row('g3', { skipped: 'skipped_itunes', skip_reason: 'Lives in the iTunes library' }),
  row('g4', { skipped: 'skipped_fragment', skip_reason: 'fragment — use the consolidation fixer: 3 books share the folder' }),
];

function skippedByKind(): Record<string, number> {
  const out: Record<string, number> = {};
  for (const r of SKIPPED) out[r.skipped!] = (out[r.skipped!] ?? 0) + 1;
  return out;
}

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
      const all =
        q.filter === 'applicable'
          ? APPLICABLE
          : q.filter === 'skipped'
            ? SKIPPED
            : SKIPPED.filter((r) => `skipped:${r.skipped}` === q.filter);
      return {
        plan_op_id: planOpId,
        fixer_id: fixerId,
        planned_at: '2026-09-27T10:01:00Z',
        filter: q.filter,
        offset: q.offset,
        limit: q.limit,
        total: all.length,
        applicable: APPLICABLE.length,
        skipped_by_kind: skippedByKind(),
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

  it('gives each skip kind a count that opens exactly its rows', async () => {
    const user = userEvent.setup();
    renderPanel();
    await screen.findByTestId('repairs-row-g1');
    await user.click(screen.getByTestId('repairs-tab-skipped'));
    await screen.findByTestId('repairs-row-g4');
    const fragChip = screen.getByTestId('repairs-skip-kind-skipped_fragment');
    expect(fragChip).toHaveTextContent('Fragment — use the consolidation fixer (1)');
    expect(screen.getByTestId('repairs-skip-kind-all')).toHaveTextContent('All skipped (2)');

    await user.click(fragChip);
    expect(api.getRepairPlanRows).toHaveBeenLastCalledWith(
      'vg-primary',
      'plan-1',
      expect.objectContaining({ filter: 'skipped:skipped_fragment', offset: 0 }),
      expect.anything()
    );
    await screen.findByTestId('repairs-row-g4');
    expect(screen.queryByTestId('repairs-row-g3')).not.toBeInTheDocument();
    // Still the skipped tab: no checkboxes, the skip reason shown.
    expect(screen.queryByTestId('repairs-apply-selected')).not.toBeInTheDocument();

    await user.click(screen.getByTestId('repairs-skip-kind-all'));
    await screen.findByTestId('repairs-row-g3');
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

// The fragment-consolidation fixer's rows carry a class, their books and the
// evidence; every count on screen must open its rows and books.
describe('RepairsPanel — classified rows', () => {
  const MOVED = row('moved:P', {
    class: 'moved',
    book_ids: ['P', 'F1', 'F2'],
    title: 'Eldest',
    members: [
      { book_id: 'P', title: 'Eldest', role: 'parent', files: 349, missing_files: 2 },
      { book_id: 'F1', title: '98', role: 'fragment', files: 1 },
      { book_id: 'F2', title: '99', role: 'fragment', files: 1 },
    ],
    evidence: ['a ← row 1: import path', 'b ← row 2: import path', 'c', 'd'],
  });
  const NOPARENT = row('no-parent:x', { class: 'no-parent', book_ids: ['A', 'B', 'C'], title: 'Loose' });

  beforeEach(() => {
    vi.mocked(api.getRepairPlanRows).mockImplementation(
      async (
        fixerId: string,
        planOpId: string,
        q: { filter: RepairRowsFilter; offset: number; limit: number; rowClass?: string }
      ) => {
        const all = q.filter === 'applicable' ? [MOVED, NOPARENT] : [];
        const rows = q.rowClass ? all.filter((r) => r.class === q.rowClass) : all;
        return {
          plan_op_id: planOpId,
          fixer_id: fixerId,
          planned_at: '2026-09-28T10:01:00Z',
          filter: q.filter,
          class: q.rowClass,
          offset: q.offset,
          limit: q.limit,
          total: rows.length,
          applicable: 2,
          skipped_by_kind: {},
          by_class: { moved: 1, 'no-parent': 1 },
          by_class_in_filter: (q.filter === 'applicable' ? { moved: 1, 'no-parent': 1 } : {}) as Record<string, number>,
          rows,
        };
      }
    );
  });

  it('shows a chip per class with its count, and a chip click lists exactly that class', async () => {
    const user = userEvent.setup();
    renderPanel();
    await screen.findByTestId('repairs-row-moved:P');
    expect(screen.getByTestId('repairs-class-moved')).toHaveTextContent('Moved (1)');
    expect(screen.getByTestId('repairs-class-no-parent')).toHaveTextContent('No parent (1)');
    expect(screen.getByTestId('repairs-class-all')).toHaveTextContent('All (2)');

    await user.click(screen.getByTestId('repairs-class-moved'));
    expect(api.getRepairPlanRows).toHaveBeenLastCalledWith(
      'vg-primary',
      'plan-1',
      expect.objectContaining({ rowClass: 'moved' }),
      expect.anything()
    );
    await screen.findByTestId('repairs-row-moved:P');
    await vi.waitFor(() => expect(screen.queryByTestId('repairs-row-no-parent:x')).not.toBeInTheDocument());

    await user.click(screen.getByTestId('repairs-class-all'));
    expect(await screen.findByTestId('repairs-row-no-parent:x')).toBeInTheDocument();
  });

  it('lists every book of a row as a link with its role and file counts', async () => {
    const user = userEvent.setup();
    renderPanel();
    const r = await screen.findByTestId('repairs-row-moved:P');
    const toggle = within(r).getByTestId('repairs-row-members-moved:P');
    expect(toggle).toHaveTextContent('3 books in this row');
    await user.click(toggle);
    expect(within(r).getByRole('link', { name: '98' })).toHaveAttribute('href', '/library/F1');
    expect(within(r).getByRole('link', { name: '99' })).toHaveAttribute('href', '/library/F2');
    expect(within(r).getByText(/parent · 349 files · 2 missing/)).toBeInTheDocument();
    expect(within(r).getAllByText(/fragment · 1 file$/)).toHaveLength(2);
    // A row with no member detail still links every book id.
    const np = screen.getByTestId('repairs-row-no-parent:x');
    await user.click(within(np).getByTestId('repairs-row-members-no-parent:x'));
    expect(within(np).getByRole('link', { name: 'C' })).toHaveAttribute('href', '/library/C');
  });

  it('shows the evidence, folding a long list behind a toggle', async () => {
    const user = userEvent.setup();
    renderPanel();
    const ev = await screen.findByTestId('repairs-row-evidence-moved:P');
    expect(within(ev).getByText('a ← row 1: import path')).toBeInTheDocument();
    expect(within(ev).queryByText('d')).not.toBeInTheDocument();
    await user.click(within(ev).getByRole('button', { name: 'Show all 4' }));
    expect(within(ev).getByText('d')).toBeInTheDocument();
    expect(within(screen.getByTestId('repairs-row-moved:P')).getByText('Moved')).toBeInTheDocument();
  });
});

describe('RepairsPanel — skip kinds under a selected class', () => {
  const S_MOVED = row('s-moved', { class: 'moved', skipped: 'skipped_itunes', skip_reason: 'iTunes' });
  const S_COPY = row('s-copy', { class: 'copy', skipped: 'skipped_itunes', skip_reason: 'iTunes' });
  const A_MOVED = row('a-moved', { class: 'moved' });

  beforeEach(() => {
    vi.mocked(api.getRepairPlanRows).mockImplementation(
      async (
        fixerId: string,
        planOpId: string,
        q: { filter: RepairRowsFilter; offset: number; limit: number; rowClass?: string }
      ) => {
        const skipped = [S_MOVED, S_COPY];
        const applicableRows = [A_MOVED];
        const inFilter =
          q.filter === 'applicable'
            ? applicableRows
            : skipped.filter((r) => q.filter === 'skipped' || q.filter === `skipped:${r.skipped}`);
        const rows = q.rowClass ? inFilter.filter((r) => r.class === q.rowClass) : inFilter;
        const byClass: Record<string, number> = {};
        for (const r of inFilter) byClass[r.class!] = (byClass[r.class!] ?? 0) + 1;
        const kindsInClass: Record<string, number> = {};
        for (const r of skipped.filter((r) => r.class === q.rowClass)) {
          kindsInClass[r.skipped!] = (kindsInClass[r.skipped!] ?? 0) + 1;
        }
        return {
          plan_op_id: planOpId,
          fixer_id: fixerId,
          planned_at: '2026-09-29T10:01:00Z',
          filter: q.filter,
          class: q.rowClass,
          offset: q.offset,
          limit: q.limit,
          total: rows.length,
          applicable: applicableRows.length,
          skipped_by_kind: { skipped_itunes: 2 },
          by_class: { moved: 2, copy: 1 },
          by_class_in_filter: byClass,
          ...(q.rowClass
            ? {
                skipped_by_kind_in_class: kindsInClass,
                applicable_in_class: applicableRows.filter((r) => r.class === q.rowClass).length,
              }
            : {}),
          rows,
        };
      }
    );
  });

  it('labels the Skipped and Applicable tabs with the selected class\'s counts', async () => {
    const user = userEvent.setup();
    renderPanel();
    await user.click(await screen.findByTestId('repairs-tab-skipped'));
    await screen.findByTestId('repairs-row-s-copy');
    expect(screen.getByTestId('repairs-tab-skipped')).toHaveTextContent('Skipped (2)');
    expect(screen.getByTestId('repairs-tab-applicable')).toHaveTextContent('Applicable (1)');

    await user.click(screen.getByTestId('repairs-class-moved'));
    await vi.waitFor(() => expect(screen.queryByTestId('repairs-row-s-copy')).not.toBeInTheDocument());
    // One skipped row listed (s-moved); the chip under the tabs says 1, and
    // so does the tab the user is on.
    expect(screen.getByTestId('repairs-skip-kind-all')).toHaveTextContent('All skipped (1)');
    expect(screen.getByTestId('repairs-tab-skipped')).toHaveTextContent('Skipped (1)');
    expect(screen.getByTestId('repairs-tab-applicable')).toHaveTextContent('Applicable (1)');

    // copy has no applicable row: its Applicable tab says 0, not the plan's 1.
    await user.click(screen.getByTestId('repairs-class-moved'));
    await screen.findByTestId('repairs-row-s-copy');
    await user.click(screen.getByTestId('repairs-class-copy'));
    await vi.waitFor(() => expect(screen.queryByTestId('repairs-row-s-moved')).not.toBeInTheDocument());
    expect(screen.getByTestId('repairs-tab-skipped')).toHaveTextContent('Skipped (1)');
    expect(screen.getByTestId('repairs-tab-applicable')).toHaveTextContent('Applicable (0)');
  });

  it('labels each kind chip, and All skipped, with the selected class\'s count', async () => {
    const user = userEvent.setup();
    renderPanel();
    await user.click(await screen.findByTestId('repairs-tab-skipped'));
    await screen.findByTestId('repairs-row-s-copy');
    expect(screen.getByTestId('repairs-skip-kind-skipped_itunes')).toHaveTextContent('(2)');
    expect(screen.getByTestId('repairs-skip-kind-all')).toHaveTextContent('All skipped (2)');

    await user.click(screen.getByTestId('repairs-class-moved'));
    await vi.waitFor(() => expect(screen.queryByTestId('repairs-row-s-copy')).not.toBeInTheDocument());
    const chip = screen.getByTestId('repairs-skip-kind-skipped_itunes');
    expect(chip).toHaveTextContent('iTunes library (hands-off) (1)');
    expect(screen.getByTestId('repairs-skip-kind-all')).toHaveTextContent('All skipped (1)');

    // The chip opens exactly the one row it counts.
    await user.click(chip);
    expect(api.getRepairPlanRows).toHaveBeenLastCalledWith(
      'vg-primary',
      'plan-1',
      expect.objectContaining({ filter: 'skipped:skipped_itunes', rowClass: 'moved' }),
      expect.anything()
    );
    await screen.findByTestId('repairs-row-s-moved');
    expect(screen.queryByTestId('repairs-row-s-copy')).not.toBeInTheDocument();
  });
});
