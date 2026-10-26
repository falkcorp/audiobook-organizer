// file: web/src/components/layout/OperationsIndicator.undoRefused.test.tsx
// version: 1.0.0
// guid: 6d1b9e43-27a8-4c05-b3f6-8a0e5d7c2f19
// last-edited: 2026-10-26

// The Undo button of a finished organize run asks the server what an undo would
// do before it offers one. The server refuses that, with a 403, to a caller who
// may not undo the operation (it carries one user's own data and the caller may
// not manage users). A refusal that will repeat on every click must not leave
// the button in place: it is shown once, with the server's reason, and removed.

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { OperationsIndicator } from './OperationsIndicator';
import { useOperationsStore, type ActiveOperation } from '../../stores/useOperationsStore';
import { getUndoPreflight } from '../../services/versionApi';

vi.mock('../../services/api', () => ({ cancelOperation: vi.fn() }));
vi.mock('../../services/versionApi', () => ({
  getUndoPreflight: vi.fn(),
  revertOperation: vi.fn(),
}));
vi.mock('../OperationActivityPanel', () => ({
  OperationActivityPanel: () => null,
}));

const mockedGetUndoPreflight = vi.mocked(getUndoPreflight);

function organizeRun(): ActiveOperation {
  return {
    id: 'op-year-one',
    type: 'organize',
    status: 'completed',
    progress: 1,
    total: 1,
    message: '',
    finishedAt: Date.UTC(2026, 9, 26, 12, 0, 0),
  } as ActiveOperation;
}

function openBellAndExpandCompleted() {
  const o = organizeRun();
  useOperationsStore.setState({
    operations: { [o.id]: o },
    activeOperations: [o],
    alertOperations: [o],
    groupedOperations: [o],
  });
  render(
    <MemoryRouter>
      <OperationsIndicator />
    </MemoryRouter>
  );
  fireEvent.click(screen.getByRole('button', { name: /operation/i }));
  fireEvent.click(screen.getByText(/COMPLETED/i));
}

describe('OperationsIndicator Undo when the server refuses it', () => {
  beforeEach(() => {
    useOperationsStore.setState({
      operations: {},
      activeOperations: [],
      alertOperations: [],
      groupedOperations: [],
    });
    localStorage.clear();
    mockedGetUndoPreflight.mockReset();
    vi.spyOn(window, 'alert').mockImplementation(() => {});
  });

  it('removes the Undo button after a 403 and says why once', async () => {
    mockedGetUndoPreflight.mockRejectedValue(
      Object.assign(new Error('permission denied: users.manage'), { response: { status: 403, data: {} } })
    );
    openBellAndExpandCompleted();

    fireEvent.click(screen.getByRole('button', { name: 'Undo' }));

    await waitFor(() => expect(window.alert).toHaveBeenCalledWith('permission denied: users.manage'));
    expect(screen.queryByRole('button', { name: 'Undo' })).not.toBeInTheDocument();
  });

  it('keeps the Undo button after a failure that is not a refusal', async () => {
    mockedGetUndoPreflight.mockRejectedValue(
      Object.assign(new Error('internal error'), { response: { status: 500, data: {} } })
    );
    openBellAndExpandCompleted();

    fireEvent.click(screen.getByRole('button', { name: 'Undo' }));

    await waitFor(() => expect(window.alert).toHaveBeenCalledWith('internal error'));
    expect(screen.getByRole('button', { name: 'Undo' })).toBeInTheDocument();
  });
});
