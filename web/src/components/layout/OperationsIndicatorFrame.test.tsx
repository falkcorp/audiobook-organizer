// file: web/src/components/layout/OperationsIndicatorFrame.test.tsx
// version: 0.1.0
// guid: 6e2a8c40-1d95-4b73-a0f6-c9b3d5e7f182
// last-edited: 2026-11-21

// OperationsIndicatorFrame renders the feed, status model and slots it is
// handed and names no audiobook store, service or route, so this test drives it
// with Gotham rows, core's task-run state names (succeeded, canceled) and
// slots of its own. It moves into core/web/packages/shell/src/layout/ with the
// frame.

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import {
  OperationsIndicatorFrame,
  type OperationsFeed,
  type OperationsFrameRow,
  type RunStatusModel,
} from './OperationsIndicatorFrame';

const navigateSpy = vi.fn();
vi.mock('react-router', async () => {
  const actual = await vi.importActual<typeof import('react-router')>('react-router');
  return { ...actual, useNavigate: () => navigateSpy };
});

// core's task-run states, not the legacy operations API's.
const coreStatus: RunStatusModel = {
  isTerminal: (s) => ['succeeded', 'failed', 'timed_out', 'canceled', 'interrupted_dropped'].includes(s),
  isQueued: (s) => s === 'queued',
  isSuccess: (s) => s === 'succeeded',
  isFailure: (s) => s === 'failed' || s === 'timed_out',
};

function row(over: Partial<OperationsFrameRow> & { id: string }): OperationsFrameRow {
  return { status: 'running', progress: 0, total: 0, message: '', ...over };
}

let rows: OperationsFrameRow[] = [];
let loadError: string | null = null;
const reload = vi.fn();
const useFeed = (): OperationsFeed => ({ grouped: rows, raw: rows, alerts: rows, loadError, reload });

function mount(over: Partial<React.ComponentProps<typeof OperationsIndicatorFrame>> = {}) {
  const onCancel = vi.fn().mockResolvedValue(undefined);
  render(
    <MemoryRouter>
      <OperationsIndicatorFrame
        useFeed={useFeed}
        status={coreStatus}
        nameOf={(op) => `Run ${op.id}`}
        onCancel={onCancel}
        renderActivity={(t) => <div data-testid="pane">{`${t.id}|${(t.memberIds ?? []).join(',')}`}</div>}
        allOperationsPath="/batcave/ops"
        activityPath="/batcave/log"
        {...over}
      />
    </MemoryRouter>,
  );
  return { onCancel };
}
const bell = () => screen.getByTestId('operations-bell');

beforeEach(() => {
  rows = [];
  loadError = null;
  navigateSpy.mockReset();
  reload.mockReset();
  localStorage.clear();
});

describe('OperationsIndicatorFrame', () => {
  it('counts only runs the status model calls in progress', () => {
    rows = [
      row({ id: 'a', status: 'running' }),
      row({ id: 'b', status: 'queued' }),
      row({ id: 'c', status: 'succeeded' }),
      row({ id: 'd', status: 'interrupted_dropped' }),
    ];
    mount();
    expect(bell()).toHaveAttribute('aria-label', '2 active operations');
  });

  it('names rows with nameOf, splits queued from running, and labels outcomes from the model', () => {
    rows = [
      row({ id: 'a', status: 'running' }),
      row({ id: 'b', status: 'queued', message: 'two waiting' }),
      row({ id: 'c', status: 'succeeded', finishedAt: 3 }),
      row({ id: 'd', status: 'timed_out', finishedAt: 2 }),
      row({ id: 'e', status: 'canceled', finishedAt: 1 }),
    ];
    mount();
    fireEvent.click(bell());
    const pop = screen.getByRole('presentation');
    expect(within(pop).getByText('Run a')).toBeInTheDocument();
    expect(within(pop).getByText('two waiting')).toBeInTheDocument();
    fireEvent.click(within(pop).getByText('Completed (3)'));
    expect(within(pop).getByText('success')).toBeInTheDocument();
    expect(within(pop).getByText('timed_out')).toBeInTheDocument();
    expect(within(pop).getByText('canceled')).toBeInTheDocument();
  });

  it('hands cancel to onCancel and the group members to renderActivity', () => {
    rows = [
      row({ id: 'a', status: 'running' }),
      row({ id: 'g', status: 'running', group: { count: 2, memberIds: ['m1', 'm2'] } as never }),
    ];
    const { onCancel } = mount();
    fireEvent.click(bell());
    const pop = screen.getByRole('presentation');
    expect(within(pop).getAllByRole('button', { name: 'Cancel' })).toHaveLength(1);
    fireEvent.click(within(pop).getByRole('button', { name: 'Cancel' }));
    expect(onCancel).toHaveBeenCalledExactlyOnceWith('a');
    fireEvent.click(within(pop).getByRole('button', { name: 'View merged activity' }));
    expect(screen.getByTestId('pane')).toHaveTextContent('g|m1,m2');
  });

  it('renders renderDetails under a running row and rowActions on a completed one, with a working closePopover', async () => {
    rows = [row({ id: 'a', status: 'running' }), row({ id: 'c', status: 'succeeded', finishedAt: 1 })];
    mount({
      renderDetails: (op) => <span>{`details of ${op.id}`}</span>,
      rowActions: (op, { closePopover }) => (
        <button type="button" onClick={closePopover}>{`act ${op.id}`}</button>
      ),
    });
    fireEvent.click(bell());
    const pop = screen.getByRole('presentation');
    expect(within(pop).getByText('details of a')).toBeInTheDocument();
    expect(within(pop).queryByText('details of c')).not.toBeInTheDocument();
    fireEvent.click(within(pop).getByText('Completed (1)'));
    fireEvent.click(within(pop).getByRole('button', { name: 'act c' }));
    await waitFor(() => expect(screen.queryByText('Operations')).not.toBeInTheDocument());
  });

  it('goes to the paths it is given', () => {
    rows = Array.from({ length: 16 }, (_, i) => row({ id: `d${i}`, status: 'succeeded', finishedAt: i }));
    mount();
    fireEvent.click(bell());
    fireEvent.click(screen.getByRole('button', { name: /View All/ }));
    expect(navigateSpy).toHaveBeenLastCalledWith('/batcave/ops');
    fireEvent.click(bell());
    fireEvent.click(screen.getByText('Completed (16)'));
    fireEvent.click(screen.getByTestId('operations-show-all-completed'));
    expect(navigateSpy).toHaveBeenLastCalledWith('/batcave/log');
  });

  it('calls reload from the Retry button of a failed load', () => {
    loadError = 'bat-signal down';
    mount();
    fireEvent.click(bell());
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(reload).toHaveBeenCalledTimes(1);
  });

  it('renders without the optional slots', () => {
    rows = [row({ id: 'a' }), row({ id: 'c', status: 'succeeded', finishedAt: 1 })];
    mount();
    fireEvent.click(bell());
    expect(screen.getByText('Run a')).toBeInTheDocument();
  });
});
