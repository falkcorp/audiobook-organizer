// file: web/src/modules/books/layout/operationsIndicator.equivalence.test.tsx
// version: 0.1.0
// guid: 5d9b3e72-4a18-4c06-b7f3-e1a0c8d2f465
// last-edited: 2026-11-21

// F9-02: the operations bell behaves the same before and after it is cut into an
// audiobook-free frame plus books slots. The seven OperationsIndicator.*.test
// files beside the component cover ordering, the completed cap, grouping,
// queued rows, the terminal classification, the load error and the refused
// Undo; this file pins what they leave out: the bell's tooltip and badge, the
// running row (name, progress, elapsed, ETA, parsed details, cancel), the
// pending row, the collapse state and its saved key, the completed chips, the
// Review and Undo row actions, and the navigation. Expected values are written
// out by hand from the component as it was before the cut.

import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from 'vitest';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { OperationsIndicator } from '../../../components/layout/OperationsIndicator';
import { useOperationsStore, type ActiveOperation } from '../../../stores/useOperationsStore';
import { cancelOperation } from '../../../services/api';
import { getUndoPreflight, revertOperation } from '../../../services/versionApi';

const navigateSpy = vi.fn();
vi.mock('react-router', async () => {
  const actual = await vi.importActual<typeof import('react-router')>('react-router');
  return { ...actual, useNavigate: () => navigateSpy };
});
vi.mock('../../../services/api', async (importOriginal) => ({ ...(await importOriginal<typeof import('../../../services/api')>()),  cancelOperation: vi.fn() }));
vi.mock('../../../services/versionApi', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../../../services/versionApi')>()),
  getUndoPreflight: vi.fn(),
  revertOperation: vi.fn(),
}));
vi.mock('../../../components/OperationActivityPanel', () => ({
  OperationActivityPanel: ({ operationId }: { operationId: string }) => (
    <div data-testid="activity-panel">{operationId}</div>
  ),
}));

const NOW = Date.UTC(2026, 9, 26, 12, 0, 0);
const COLLAPSE_ITEM = 'ops-indicator-collapse-v1';

function op(over: Partial<ActiveOperation> & { id: string }): ActiveOperation {
  return {
    type: 'scan',
    status: 'running',
    progress: 0,
    total: 0,
    message: '',
    ...over,
  } as ActiveOperation;
}

function seed(ops: ActiveOperation[], extra: Record<string, unknown> = {}) {
  useOperationsStore.setState({
    operations: Object.fromEntries(ops.map((o) => [o.id, o])),
    activeOperations: ops,
    alertOperations: ops,
    groupedOperations: ops,
    loadError: null,
    ...extra,
  });
}

function mountBell() {
  render(
    <MemoryRouter>
      <OperationsIndicator />
    </MemoryRouter>,
  );
}
const bell = () => screen.getByTestId('operations-bell');
function openBell() {
  mountBell();
  fireEvent.click(bell());
}
const popover = () => screen.getByRole('presentation');

beforeEach(() => {
  navigateSpy.mockReset();
  (cancelOperation as Mock).mockReset();
  (getUndoPreflight as Mock).mockReset();
  (revertOperation as Mock).mockReset();
  localStorage.clear();
  seed([]);
  vi.useFakeTimers({ toFake: ['Date'] });
  vi.setSystemTime(NOW);
});
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe('operations bell equivalence: the closed bell', () => {
  it('says there are no active operations when nothing runs', () => {
    mountBell();
    expect(bell()).toHaveAttribute('aria-label', 'No active operations');
  });

  it.each([
    [1, '1 active operation'],
    [2, '2 active operations'],
  ])('counts %i running operation(s) in its tooltip and badge', (n, label) => {
    seed(Array.from({ length: n }, (_, i) => op({ id: `r${i}` })));
    mountBell();
    expect(bell()).toHaveAttribute('aria-label', label);
    expect(within(bell()).getByText(String(n))).toBeInTheDocument();
  });

  it('adds "last refresh failed" to the tooltip after a failed load', () => {
    seed([op({ id: 'r1' })], { loadError: 'bat-signal down' });
    mountBell();
    expect(bell()).toHaveAttribute('aria-label', '1 active operation — last refresh failed');
  });

  it('does not badge finished operations', () => {
    seed([op({ id: 'd1', status: 'completed' })]);
    mountBell();
    expect(bell()).toHaveAttribute('aria-label', 'No active operations');
  });
});

describe('operations bell equivalence: the popover', () => {
  it('View All closes the popover and goes to /operations', async () => {
    openBell();
    fireEvent.click(screen.getByRole('button', { name: /View All/ }));
    expect(navigateSpy).toHaveBeenCalledExactlyOnceWith('/operations');
    await waitFor(() => expect(screen.queryByText('Operations')).not.toBeInTheDocument());
  });

  it('shows "No operations" for a confirmed empty list', () => {
    openBell();
    expect(screen.getByTestId('operations-empty')).toHaveTextContent('No operations');
  });
});

describe('operations bell equivalence: a running row', () => {
  it('shows the curated name, the counts and a determinate bar', () => {
    seed([op({ id: 'r1', type: 'scan', progress: 50, total: 100 })]);
    openBell();
    const pop = popover();
    expect(within(pop).getByText('Library Scan')).toBeInTheDocument();
    expect(within(pop).getByText('50 / 100 (50%)')).toBeInTheDocument();
    expect(within(pop).getByRole('progressbar', { name: '' })).toHaveAttribute('aria-valuenow', '50');
  });

  it('shows "N processed" and an indeterminate bar without a total', () => {
    seed([op({ id: 'r1', progress: 1234, total: 0 })]);
    openBell();
    const pop = popover();
    expect(within(pop).getByText('1,234 processed')).toBeInTheDocument();
    expect(within(pop).getByRole('progressbar', { name: '' })).not.toHaveAttribute('aria-valuenow');
  });

  it('says "Starting..." with no progress', () => {
    seed([op({ id: 'r1' })]);
    openBell();
    expect(within(popover()).getByText('Starting...')).toBeInTheDocument();
  });

  it('shows elapsed time and an ETA after 5 seconds of progress', () => {
    seed([op({ id: 'r1', progress: 25, total: 100, startedAt: NOW - 60_000 })]);
    openBell();
    expect(within(popover()).getByText('1m 0s')).toBeInTheDocument();
    expect(within(popover()).getByText('~3m left')).toBeInTheDocument();
  });

  it('shows no ETA in the first 5 seconds', () => {
    seed([op({ id: 'r1', progress: 25, total: 100, startedAt: NOW - 4_000 })]);
    openBell();
    expect(within(popover()).getByText('4s')).toBeInTheDocument();
    expect(within(popover()).queryByText(/left/)).not.toBeInTheDocument();
  });

  it('shows an ETA in hours and minutes for a long run', () => {
    // 1 of 100 in 1000 s: remaining 99000 s = 27 h 30 m.
    seed([op({ id: 'r1', progress: 1, total: 100, startedAt: NOW - 1_000_000 })]);
    openBell();
    expect(within(popover()).getByText('~27h 30m left')).toBeInTheDocument();
  });

  it('parses imported / skipped / failed counts and the current title from the message', () => {
    seed([
      op({
        id: 'r1',
        type: 'itunes_import',
        message: 'Importing — Batman: Year One (imported 3, skipped 2, failed 1)',
      }),
    ]);
    openBell();
    const pop = popover();
    expect(within(pop).getByText('3 imported')).toBeInTheDocument();
    expect(within(pop).getByText('2 skipped')).toBeInTheDocument();
    expect(within(pop).getByText('1 failed')).toBeInTheDocument();
    expect(within(pop).getByTitle('Batman: Year One (imported 3, skipped 2, failed 1)')).toBeInTheDocument();
  });

  it('hides zero skipped and failed counts', () => {
    seed([op({ id: 'r1', message: 'x (imported 5, skipped 0, failed 0)' })]);
    openBell();
    expect(within(popover()).getByText('5 imported')).toBeInTheDocument();
    expect(within(popover()).queryByText(/skipped|failed/)).not.toBeInTheDocument();
  });

  it('shows an Open Library import line', () => {
    seed([op({ id: 'r1', type: 'ol_dump_import', message: 'Importing editions: 1234k records' })]);
    openBell();
    expect(within(popover()).getByText('editions: 1234k records')).toBeInTheDocument();
  });

  it('cancels a running operation by id and disables the button while it is pending', async () => {
    let finish: () => void = () => undefined;
    (cancelOperation as Mock).mockReturnValue(new Promise<void>((r) => (finish = r)));
    seed([op({ id: 'r1' })]);
    openBell();
    const cancel = within(popover()).getByRole('button', { name: 'Cancel' });
    fireEvent.click(cancel);
    expect(cancelOperation).toHaveBeenCalledExactlyOnceWith('r1');
    await waitFor(() => expect(within(popover()).getByRole('button', { name: 'Cancel' })).toBeDisabled());
    await act(async () => finish());
    await waitFor(() => expect(within(popover()).getByRole('button', { name: 'Cancel' })).toBeEnabled());
  });

  it('survives a failed cancel and re-enables the button', async () => {
    (cancelOperation as Mock).mockRejectedValue(new Error('too late'));
    seed([op({ id: 'r1' })]);
    openBell();
    fireEvent.click(within(popover()).getByRole('button', { name: 'Cancel' }));
    await waitFor(() => expect(within(popover()).getByRole('button', { name: 'Cancel' })).toBeEnabled());
  });

  it('offers "View activity" for a real run, which opens its panel', () => {
    seed([op({ id: 'r1' })]);
    openBell();
    fireEvent.click(within(popover()).getByRole('button', { name: 'View activity' }));
    expect(screen.getByText('Operation Activity')).toBeInTheDocument();
    expect(screen.getByTestId('activity-panel')).toHaveTextContent('r1');
  });
});

describe('operations bell equivalence: a pending row', () => {
  it('shows the name, the server message, and a Cancel button', () => {
    seed([op({ id: 'q1', type: 'organize', status: 'queued', message: '12 books waiting' })]);
    openBell();
    const pop = popover();
    expect(within(pop).getByText('Pending (1)')).toBeInTheDocument();
    expect(within(pop).getByText('Organize')).toBeInTheDocument();
    expect(within(pop).getByText('12 books waiting')).toBeInTheDocument();
    fireEvent.click(within(pop).getByRole('button', { name: 'Cancel' }));
    expect(cancelOperation).toHaveBeenCalledExactlyOnceWith('q1');
  });
});

describe('operations bell equivalence: section collapse', () => {
  const all = () => [
    op({ id: 'r1', type: 'scan' }),
    op({ id: 'q1', type: 'organize', status: 'queued' }),
    op({ id: 'd1', type: 'metadata_fetch', status: 'completed', finishedAt: NOW }),
  ];

  it('opens Running and Pending and collapses Completed by default', () => {
    seed(all());
    openBell();
    const pop = popover();
    expect(within(pop).getByText('Library Scan')).toBeInTheDocument();
    expect(within(pop).getByText('Organize')).toBeInTheDocument();
    expect(within(pop).queryByText('Metadata Fetch')).not.toBeInTheDocument();
  });

  it('saves a toggle under the layout key and restores it on the next mount', () => {
    seed(all());
    openBell();
    fireEvent.click(within(popover()).getByText('Completed (1)'));
    expect(within(popover()).getByText('Metadata Fetch')).toBeInTheDocument();
    expect(JSON.parse(localStorage.getItem(COLLAPSE_ITEM) ?? 'null')).toEqual({
      running: true,
      pending: true,
      completed: true,
    });
  });

  it('starts from the saved state', () => {
    localStorage.setItem(COLLAPSE_ITEM, JSON.stringify({ running: false, completed: true }));
    seed(all());
    openBell();
    const pop = popover();
    expect(within(pop).queryByText('Library Scan')).not.toBeInTheDocument();
    expect(within(pop).getByText('Organize')).toBeInTheDocument();
    expect(within(pop).getByText('Metadata Fetch')).toBeInTheDocument();
  });

  it('falls back to the defaults for an unreadable saved value', () => {
    localStorage.setItem(COLLAPSE_ITEM, '{not json');
    seed(all());
    openBell();
    expect(within(popover()).getByText('Library Scan')).toBeInTheDocument();
    expect(within(popover()).queryByText('Metadata Fetch')).not.toBeInTheDocument();
  });
});

describe('operations bell equivalence: completed rows', () => {
  function openCompleted(ops: ActiveOperation[]) {
    seed(ops);
    openBell();
    fireEvent.click(within(popover()).getByText(new RegExp(`Completed \\(${ops.length}\\)`)));
  }

  it.each([
    ['completed', 'success'],
    ['failed', 'failed'],
    ['canceled', 'canceled'],
  ])('labels a %s run "%s"', (status, label) => {
    openCompleted([op({ id: 'd1', status, finishedAt: NOW })]);
    expect(within(popover()).getByText(label)).toBeInTheDocument();
  });

  it('opens the activity panel when a completed row is clicked', () => {
    openCompleted([op({ id: 'd1', status: 'completed', finishedAt: NOW })]);
    fireEvent.click(within(popover()).getByText('success'));
    expect(screen.getByTestId('activity-panel')).toHaveTextContent('d1');
  });

  it('"Show all" goes to /activity and closes the popover', async () => {
    const many = Array.from({ length: 16 }, (_, i) =>
      op({ id: `d${i}`, status: 'completed', finishedAt: NOW - i }),
    );
    openCompleted(many);
    fireEvent.click(screen.getByTestId('operations-show-all-completed'));
    expect(navigateSpy).toHaveBeenCalledExactlyOnceWith('/activity');
    await waitFor(() => expect(screen.queryByText('Operations')).not.toBeInTheDocument());
  });

  describe('the Review button', () => {
    it('is offered for a completed metadata_candidate_fetch and goes to /review', () => {
      openCompleted([op({ id: 'd1', type: 'metadata_candidate_fetch', status: 'completed', finishedAt: NOW })]);
      fireEvent.click(within(popover()).getByRole('button', { name: 'Review' }));
      expect(navigateSpy).toHaveBeenCalledExactlyOnceWith('/review');
      expect(screen.queryByTestId('activity-panel')).not.toBeInTheDocument();
    });

    it.each([
      ['a failed metadata_candidate_fetch', 'metadata_candidate_fetch', 'failed'],
      ['a completed scan', 'scan', 'completed'],
    ])('is not offered for %s', (_l, type, status) => {
      openCompleted([op({ id: 'd1', type, status, finishedAt: NOW })]);
      expect(within(popover()).queryByRole('button', { name: 'Review' })).not.toBeInTheDocument();
    });
  });

  describe('the Undo button', () => {
    beforeEach(() => {
      vi.spyOn(window, 'alert').mockImplementation(() => undefined);
    });

    it.each(['organize', 'scan_and_organize'])('is offered for a completed %s', (type) => {
      openCompleted([op({ id: 'd1', type, status: 'completed', finishedAt: NOW })]);
      expect(within(popover()).getByRole('button', { name: 'Undo' })).toBeInTheDocument();
    });

    it.each([
      ['a failed organize', 'organize', 'failed'],
      ['a completed scan', 'scan', 'completed'],
    ])('is not offered for %s', (_l, type, status) => {
      openCompleted([op({ id: 'd1', type, status, finishedAt: NOW })]);
      expect(within(popover()).queryByRole('button', { name: 'Undo' })).not.toBeInTheDocument();
    });

    it('says so and does not ask for confirmation when nothing can be undone', async () => {
      (getUndoPreflight as Mock).mockResolvedValue({ safe: 0, already_reverted: 0 });
      const confirmSpy = vi.spyOn(window, 'confirm');
      openCompleted([op({ id: 'd1', type: 'organize', status: 'completed', finishedAt: NOW })]);
      fireEvent.click(within(popover()).getByRole('button', { name: 'Undo' }));
      await waitFor(() =>
        expect(window.alert).toHaveBeenCalledWith('Nothing in this operation can be undone automatically.'),
      );
      expect(confirmSpy).not.toHaveBeenCalled();
      expect(revertOperation).not.toHaveBeenCalled();
    });

    it('reverts after a confirmed preflight and shows the result', async () => {
      (getUndoPreflight as Mock).mockResolvedValue({ safe: 4, already_reverted: 0 });
      (revertOperation as Mock).mockResolvedValue({ message: 'restored 4 changes' });
      const confirmSpy = vi.spyOn(window, 'confirm').mockReturnValue(true);
      openCompleted([op({ id: 'd1', type: 'organize', status: 'completed', finishedAt: NOW })]);
      fireEvent.click(within(popover()).getByRole('button', { name: 'Undo' }));
      await waitFor(() => expect(window.alert).toHaveBeenCalledWith('Restored 4 changes'));
      expect(confirmSpy).toHaveBeenCalledWith('Undo 4 change(s) from this operation?');
      expect(getUndoPreflight).toHaveBeenCalledWith('d1');
      expect(revertOperation).toHaveBeenCalledExactlyOnceWith('d1');
    });

    it('does not revert when the confirmation is declined', async () => {
      (getUndoPreflight as Mock).mockResolvedValue({ safe: 4, already_reverted: 0 });
      vi.spyOn(window, 'confirm').mockReturnValue(false);
      openCompleted([op({ id: 'd1', type: 'organize', status: 'completed', finishedAt: NOW })]);
      fireEvent.click(within(popover()).getByRole('button', { name: 'Undo' }));
      await waitFor(() => expect(getUndoPreflight).toHaveBeenCalledTimes(1));
      expect(revertOperation).not.toHaveBeenCalled();
    });

    it('keeps a refused Undo removed after the Completed section is collapsed and reopened', async () => {
      (getUndoPreflight as Mock).mockRejectedValue(
        Object.assign(new Error('permission denied'), { response: { status: 403 } }),
      );
      openCompleted([op({ id: 'd1', type: 'organize', status: 'completed', finishedAt: NOW })]);
      fireEvent.click(within(popover()).getByRole('button', { name: 'Undo' }));
      await waitFor(() => expect(within(popover()).queryByRole('button', { name: 'Undo' })).not.toBeInTheDocument());
      fireEvent.click(within(popover()).getByText('Completed (1)'));
      await waitFor(() => expect(within(popover()).queryByText('success')).not.toBeInTheDocument());
      fireEvent.click(within(popover()).getByText('Completed (1)'));
      expect(await within(popover()).findByText('success')).toBeInTheDocument();
      expect(within(popover()).queryByRole('button', { name: 'Undo' })).not.toBeInTheDocument();
    });
  });
});
