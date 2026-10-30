// file: web/src/components/layout/OperationsIndicator.cap.test.tsx
// version: 1.0.0
// guid: 4c1e7a92-3b58-4d06-9a2f-e8b5d0c36f17
// last-edited: 2026-10-30

// The bell's Completed list is capped at the newest 15 rows, with a link to the
// Activity page for the rest. Running and queued rows are never capped.

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

const T0 = Date.UTC(2026, 9, 30, 12, 0, 0);

function completed(i: number): ActiveOperation {
  return {
    id: `done-${i}`,
    type: 'maintenance',
    status: 'completed',
    progress: 1,
    total: 1,
    message: '',
    displayName: `Batman Job ${i}`,
    finishedAt: T0 - i * 1000,
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

function openBellAndCompleted() {
  render(
    <MemoryRouter>
      <OperationsIndicator />
    </MemoryRouter>
  );
  fireEvent.click(screen.getByRole('button', { name: /operation/i }));
  fireEvent.click(screen.getByText(/COMPLETED/i));
}

describe('OperationsIndicator completed cap', () => {
  beforeEach(() => {
    useOperationsStore.setState({
      operations: {},
      activeOperations: [],
      alertOperations: [],
      groupedOperations: [],
    });
    localStorage.clear();
  });

  it('shows the newest 15 of 40 completed ops and a link naming all 40', () => {
    seed(Array.from({ length: 40 }, (_, i) => completed(i)));
    openBellAndCompleted();
    const popover = screen.getByRole('presentation');
    expect(within(popover).getAllByText(/^Batman Job \d+$/)).toHaveLength(15);
    // Newest kept, oldest dropped.
    expect(within(popover).getByText('Batman Job 0')).toBeInTheDocument();
    expect(within(popover).queryByText('Batman Job 15')).not.toBeInTheDocument();
    const link = within(popover).getByTestId('operations-show-all-completed');
    expect(link).toHaveTextContent('Show all 40 in Activity');
  });

  it('shows every op and no link when 10 are completed', () => {
    seed(Array.from({ length: 10 }, (_, i) => completed(i)));
    openBellAndCompleted();
    const popover = screen.getByRole('presentation');
    expect(within(popover).getAllByText(/^Batman Job \d+$/)).toHaveLength(10);
    expect(within(popover).queryByTestId('operations-show-all-completed')).not.toBeInTheDocument();
  });
});
