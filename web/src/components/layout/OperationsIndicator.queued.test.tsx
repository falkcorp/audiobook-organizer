// file: web/src/components/layout/OperationsIndicator.queued.test.tsx
// version: 1.0.0
// guid: c07b41e9-5a2d-4e83-9106-8f3d2a6b7c15
// last-edited: 2026-09-09

// A queued operation says how much work it is holding.
//
// The bell used to print a fixed "Waiting to start…" for every pending row, so
// a metadata.batch-apply-cached run that the queue merger had grown to twelve
// hundred books looked exactly like one holding a single book. The server now
// sends that size as the row's progress message (OperationDef.SummarizeQueued);
// the popover has to actually show it, and still has something to say for ops
// that cannot report a size.

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

function queuedApply(id: string, message: string): ActiveOperation {
  return {
    id,
    def_id: 'metadata.batch-apply-cached',
    type: 'batch-apply-cached',
    displayName: 'Apply Cached Metadata',
    status: 'queued',
    progress: 0,
    total: message ? 1204 : 0,
    message,
    parent_id: null,
  } as ActiveOperation;
}

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

describe('OperationsIndicator queued rows', () => {
  beforeEach(() => {
    localStorage.clear();
    useOperationsStore.setState({
      operations: {},
      activeOperations: [],
      groupedOperations: [],
      alertOperations: [],
    });
  });

  it('shows how many books a pending apply covers', async () => {
    seed([queuedApply('op-1', '1204 books to apply')]);

    await openBell();

    expect(screen.getByText('1204 books to apply')).toBeInTheDocument();
    // The fixed string must be GONE for this row, not merely accompanied — it
    // was what hid the number.
    expect(screen.queryByText('Waiting to start…')).not.toBeInTheDocument();
  });

  it('still explains a queued op that cannot report its size', async () => {
    // The fallback guard. An op with no SummarizeQueued hook sends no message,
    // and a row that then rendered nothing at all would be a worse regression
    // than the one being fixed.
    seed([queuedApply('op-2', '')]);

    await openBell();

    expect(screen.getByText('Waiting to start…')).toBeInTheDocument();
  });
});
