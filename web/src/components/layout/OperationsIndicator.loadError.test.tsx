// file: web/src/components/layout/OperationsIndicator.loadError.test.tsx
// version: 1.0.0
// guid: 0ab4174b-b052-4d6c-a705-bef9c60dd751
// last-edited: 2026-09-11

// The bell must show a FAILED timeline refresh as a failure, not as "No
// operations". Until 2026-09-11 getOperationTimeline returned [] on a 500 and
// on a network error, so the popover over a down server was indistinguishable
// from the popover over an idle one (WEB-05). The store now keeps the last
// confirmed list and records the error in `loadError`; these pin what the bell
// does with it.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { fireEvent, render, screen } from '@testing-library/react';
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

const runningOp: ActiveOperation = {
  id: 'op-1',
  type: 'scan',
  displayName: 'Library Scan',
  status: 'running',
  progress: 3,
  total: 10,
  message: 'scanning',
  startedAt: Date.now() - 60_000,
};

function renderBell() {
  return render(
    <MemoryRouter>
      <OperationsIndicator />
    </MemoryRouter>
  );
}

function openPopover() {
  fireEvent.click(screen.getByTestId('operations-bell'));
}

describe('OperationsIndicator load error', () => {
  const loadFromServer = vi.fn().mockResolvedValue(undefined);

  beforeEach(() => {
    loadFromServer.mockClear();
    useOperationsStore.setState({
      operations: {},
      activeOperations: [],
      liveOperations: [],
      alertOperations: [],
      groupedOperations: [],
      loadError: null,
      loadFromServer,
    });
  });

  it('renders the error, not "No operations", when the refresh failed and nothing is known', () => {
    useOperationsStore.setState({ loadError: 'HTTP 503' });
    renderBell();
    openPopover();

    const alert = screen.getByTestId('operations-load-error');
    expect(alert).toHaveTextContent('Could not load operations: HTTP 503');
    expect(screen.queryByTestId('operations-empty')).not.toBeInTheDocument();
    expect(screen.queryByText('No operations')).not.toBeInTheDocument();
  });

  it('Retry re-invokes loadFromServer', () => {
    useOperationsStore.setState({ loadError: 'HTTP 503' });
    renderBell();
    openPopover();

    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));

    expect(loadFromServer).toHaveBeenCalledTimes(1);
  });

  it('keeps showing the last confirmed list, marked stale, when a refresh fails', () => {
    useOperationsStore.setState({
      operations: { [runningOp.id]: runningOp },
      activeOperations: [runningOp],
      liveOperations: [runningOp],
      alertOperations: [runningOp],
      groupedOperations: [runningOp],
      loadError: 'HTTP 503',
    });
    renderBell();
    openPopover();

    expect(screen.getByTestId('operations-load-error')).toHaveTextContent(
      'this list may be out of date: HTTP 503'
    );
    // The previous list is still there — a transient failure does not blank it.
    expect(screen.getByText('Library Scan')).toBeInTheDocument();
  });

  it('marks the closed bell when the last refresh failed', () => {
    const { container, rerender } = renderBell();
    const dot = () => container.querySelector('.MuiBadge-dot');
    expect(dot()).toHaveClass('MuiBadge-invisible');

    useOperationsStore.setState({ loadError: 'HTTP 503' });
    rerender(
      <MemoryRouter>
        <OperationsIndicator />
      </MemoryRouter>
    );
    expect(dot()).not.toHaveClass('MuiBadge-invisible');
  });

  it('still says "No operations" when the list is confirmed empty', () => {
    renderBell();
    openPopover();

    expect(screen.getByTestId('operations-empty')).toHaveTextContent('No operations');
    expect(screen.queryByTestId('operations-load-error')).not.toBeInTheDocument();
  });
});
