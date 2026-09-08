// file: web/src/components/layout/OperationsIndicator.terminal.test.tsx
// version: 1.0.0
// guid: 5f2a8d16-9c47-4b03-8e15-6d2b7a4f01c9
// last-edited: 2026-09-08

// The indicator must classify an op by isTerminal, not by a hardcoded
// ['completed','failed','canceled'] list.
//
// The backend mints a family of interrupted_* statuses, one per ResumePolicy,
// and none of them appeared in that list — so an op that had FINISHED as
// interrupted_dropped stayed "in progress" here forever: the bell badge counted
// it, the panel listed it under Running with a dead progress bar, and it never
// reached the Completed section. operationPolling.ts's isTerminal already
// handles the whole family and its own comment warns against enumerating; these
// call sites predated it.
//
// The activity log's Pebble→SQLite migration is what surfaced this: it closes
// out at interrupted_dropped on every restart, so the badge would have gained a
// permanent +1 per reboot.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { render, screen } from '@testing-library/react';
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

function op(overrides: Partial<ActiveOperation>): ActiveOperation {
  return {
    id: 'op-1',
    type: 'sql-migration',
    status: 'running',
    progress: 4_739_375,
    total: 0,
    message: 'tier 1/7 "change"',
    startedAt: Date.now() - 60_000,
    ...overrides,
  } as ActiveOperation;
}

function seed(o: ActiveOperation) {
  useOperationsStore.setState({
    operations: { [o.id]: o },
    activeOperations: [o],
    alertOperations: [o],
  });
}

describe('OperationsIndicator terminal classification', () => {
  beforeEach(() => {
    useOperationsStore.setState({ operations: {}, activeOperations: [], alertOperations: [] });
  });

  it('does not count an interrupted_dropped op in the bell badge', () => {
    seed(op({ status: 'interrupted_dropped' }));
    render(
      <MemoryRouter>
        <OperationsIndicator />
      </MemoryRouter>
    );
    // A finished op contributes nothing to the badge. Before isTerminal this
    // rendered "1", permanently, once per restart of the migration.
    expect(screen.queryByText('1')).not.toBeInTheDocument();
  });

  it('still counts a genuinely running op', () => {
    seed(op({ status: 'running' }));
    render(
      <MemoryRouter>
        <OperationsIndicator />
      </MemoryRouter>
    );
    // Guards the obvious over-correction: treating everything as terminal would
    // silence the badge entirely and leave every test above green.
    expect(screen.getByText('1')).toBeInTheDocument();
  });
});
