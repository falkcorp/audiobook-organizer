// file: web/src/components/layout/OperationsIndicator.order.test.tsx
// version: 1.0.0
// guid: 9e73c1b4-05af-4d28-b6c7-1f3a82d95e40
// last-edited: 2026-09-20

// The bell must list the NEWEST run first.
//
// groupOperations emits its rows bucket by bucket — every run of one
// def_id+status, then the next kind — so the array it returns carries no time
// order at all. The popover rendered it as-is, which put a just-finished job
// wherever its kind's bucket happened to land: in practice at the very bottom,
// under a screen of older successes, where nobody scrolls. The Activity page
// had always sorted its own history sections by finishedAt; the bell never did.
//
// This is a COMPONENT test on purpose. A unit test of the byNewestFirst
// comparator passes whether or not the component ever calls it, so it cannot
// catch the regression this fix is for.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { render, screen, within, fireEvent } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { OperationsIndicator } from './OperationsIndicator';
import { useOperationsStore, type ActiveOperation } from '../../stores/useOperationsStore';

vi.mock('../../services/api', () => ({ cancelOperation: vi.fn() }));
vi.mock('../../services/versionApi', () => ({
  getUndoPreflight: vi.fn(),
  revertOperation: vi.fn(),
}));
vi.mock('../OperationActivityPanel', () => ({
  OperationActivityPanel: () => null,
}));

const T0 = Date.UTC(2026, 8, 20, 19, 0, 0);

function op(overrides: Partial<ActiveOperation> & { id: string }): ActiveOperation {
  return {
    type: 'maintenance',
    status: 'completed',
    progress: 1,
    total: 1,
    message: '',
    finishedAt: T0,
    ...overrides,
  } as ActiveOperation;
}

function seed(ops: ActiveOperation[]) {
  useOperationsStore.setState({
    operations: Object.fromEntries(ops.map((o) => [o.id, o])),
    activeOperations: ops,
    alertOperations: ops,
    groupedOperations: ops,
  });
}

function openBell() {
  render(
    <MemoryRouter>
      <OperationsIndicator />
    </MemoryRouter>
  );
  fireEvent.click(screen.getByRole('button', { name: /operation/i }));
}

describe('OperationsIndicator ordering', () => {
  beforeEach(() => {
    useOperationsStore.setState({
      operations: {},
      activeOperations: [],
      alertOperations: [],
      groupedOperations: [],
    });
    localStorage.clear();
  });

  it('renders the newest completed run first, not in grouping order', () => {
    // Deliberately handed over newest-LAST, the way the bucketed grouping
    // output arrives.
    seed([
      op({ id: 'B', displayName: 'Oldest Job', finishedAt: T0 - 600_000 }),
      op({ id: 'C', displayName: 'Middle Job', finishedAt: T0 - 300_000 }),
      op({ id: 'A', displayName: 'Newest Job', finishedAt: T0 }),
    ]);
    openBell();

    // Completed starts collapsed; expand it.
    fireEvent.click(screen.getByText(/COMPLETED/i));

    const popover = screen.getByRole('presentation');
    const text = within(popover).getByText('Newest Job').closest('div')?.parentElement;
    expect(text).toBeTruthy();

    const rendered = ['Newest Job', 'Middle Job', 'Oldest Job'].map((name) =>
      within(popover).getByText(name)
    );
    // Compare DOM document order: each node must precede the next.
    for (let i = 0; i < rendered.length - 1; i++) {
      const rel = rendered[i].compareDocumentPosition(rendered[i + 1]);
      // eslint-disable-next-line no-bitwise
      expect(rel & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    }
  });
});
