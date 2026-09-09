// file: web/src/components/layout/OperationsIndicator.grouping.test.tsx
// version: 1.0.0
// guid: 2a6e94b1-7d38-4c05-9f61-0b3e5c8a72d4
// last-edited: 2026-09-08

// The bell shows GROUPED rows and counts UNGROUPED operations.
//
// Those are two different things and the popover needs both. A run of twelve
// identical background jobs should occupy one line, not twelve — but the line
// above it says "COMPLETED (n)", and n has to be twelve. Counting the rendered
// rows instead would print "COMPLETED (1)" directly above a row whose own chip
// reads "×12", and would quietly restore the under-reporting that grouping was
// added to fix.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter } from 'react-router-dom';
import { OperationsIndicator } from './OperationsIndicator';
import { useOperationsStore, type ActiveOperation } from '../../stores/useOperationsStore';
import { groupOperations } from '../../stores/operationGrouping';

vi.mock('../../services/api', () => ({ cancelOperation: vi.fn() }));
vi.mock('../../services/versionApi', () => ({
  getUndoPreflight: vi.fn(),
  revertOperation: vi.fn(),
}));
vi.mock('../OperationActivityPanel', () => ({
  OperationActivityPanel: () => null,
}));

const T0 = Date.UTC(2026, 8, 8, 12, 0, 0);

function aiParse(i: number): ActiveOperation {
  return {
    id: `ai-${i}`,
    def_id: 'library.ai-parse',
    type: 'ai-parse',
    displayName: 'AI Filename Parsing',
    status: 'completed',
    progress: 0,
    total: 5,
    message: '0/5 book(s) parsed',
    finishedAt: T0 + i * 20_000,
    parent_id: null,
  } as ActiveOperation;
}

/** seed writes both arrays the way deriveOperationArrays does, with the REAL
 *  fold — a hand-built groupedOperations would let this file assert a shape the
 *  store never produces. */
function seed(ops: ActiveOperation[]) {
  useOperationsStore.setState({
    operations: Object.fromEntries(ops.map((o) => [o.id, o])),
    activeOperations: ops,
    groupedOperations: groupOperations(ops),
    alertOperations: [],
  });
}

async function openBell() {
  render(
    <MemoryRouter>
      <OperationsIndicator />
    </MemoryRouter>
  );
  await userEvent.click(screen.getByRole('button'));
}

describe('OperationsIndicator grouping', () => {
  beforeEach(() => {
    localStorage.clear();
    useOperationsStore.setState({
      operations: {},
      activeOperations: [],
      groupedOperations: [],
      alertOperations: [],
    });
  });

  it('counts operations, not rows, in the section heading', async () => {
    seed(Array.from({ length: 12 }, (_, i) => aiParse(i)));

    await openBell();

    expect(screen.getByText('Completed (12)')).toBeInTheDocument();
  });

  it('lists the run as one row that says how many it stands for', async () => {
    seed(Array.from({ length: 12 }, (_, i) => aiParse(i)));

    await openBell();

    // The Completed section is collapsed by default, so open it before looking
    // for the row — a passing assertion against an unmounted Collapse would
    // prove nothing.
    await userEvent.click(screen.getByText('Completed (12)'));

    // This popover labels a row with formatOperationType(op.type), so the type
    // is what to look for, not the display name.
    const rows = screen.getAllByText(/Ai Parse/);
    expect(rows).toHaveLength(1);
    expect(rows[0].textContent).toContain('×12');
  });

  it('leaves an ungrouped operation counted and listed as one', async () => {
    // The over-correction guard: if the count came from somewhere that always
    // inflates, this would read higher than 1.
    seed([aiParse(0)]);

    await openBell();

    expect(screen.getByText('Completed (1)')).toBeInTheDocument();
  });
});
