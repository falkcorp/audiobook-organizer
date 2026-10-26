// file: web/src/components/operations/OperationTrackerProvider.test.tsx
// version: 1.0.1
// guid: 9d2f6b83-0a41-4c75-8e19-5b3a7c1d4e60
// last-edited: 2026-10-26

import { useEffect } from 'react';
import { act, render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { MemoryRouter, Route, Routes, useNavigate } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';

const api = vi.hoisted(() => ({
  pollOperationV2: vi.fn(),
  getOperationResult: vi.fn(),
  awaitPurgeResult: vi.fn(),
  revertOperation: vi.fn(),
}));
vi.mock('../../services/api', async (orig) => ({
  ...(await orig<typeof import('../../services/api')>()),
  ...api,
}));

import { ApiError } from '../../services/api';
import { ApiTimeoutError } from '../../utils/apiFetch';
import { useAppStore } from '../../stores/useAppStore';
import { TRACKED_OPS_STORAGE_KEY } from '../../utils/trackedOperations';
import { ToastProvider } from '../toast/ToastProvider';
import {
  OperationTrackerProvider,
  useOperationTracker,
  type TrackedOpEnd,
} from './OperationTrackerProvider';

const lastToast = () => {
  const list = useAppStore.getState().notifications;
  return list[list.length - 1];
};

/** A page that shows what is locked and can start a merge or combine. */
function LibraryStandIn({ onEnd }: { onEnd?: (end: TrackedOpEnd) => void }) {
  const tracker = useOperationTracker();
  const { subscribe } = tracker;
  const navigate = useNavigate();
  // Subscribed while the page is showing, like the Library page.
  useEffect(() => (onEnd ? subscribe(onEnd) : undefined), [subscribe, onEnd]);
  return (
    <div>
      <span data-testid="locked">{[...tracker.pendingIds].sort().join(',')}</span>
      <button
        onClick={() =>
          tracker.track({
            operationId: 'op-1',
            kind: 'merge',
            bookIds: ['a', 'b'],
            titles: { a: 'Book A', b: 'Book B' },
            requested: 1,
          })
        }
      >
        merge
      </button>
      <button
        onClick={() => tracker.track({ operationId: 'op-2', kind: 'combine', bookIds: ['b', 'c'] })}
      >
        combine
      </button>
      <button
        onClick={() => {
          tracker.track({ operationId: 'op-1', kind: 'merge', bookIds: ['a', 'b'] });
          tracker.track({ operationId: 'op-1', kind: 'merge', bookIds: ['a', 'b'] });
        }}
      >
        merge twice
      </button>
      <button onClick={() => navigate('/elsewhere')}>leave</button>
    </div>
  );
}

function Elsewhere() {
  const navigate = useNavigate();
  return <button onClick={() => navigate('/library')}>back</button>;
}

function App({ onEnd }: { onEnd?: (end: TrackedOpEnd) => void }) {
  return (
    <MemoryRouter initialEntries={['/library']}>
      <ToastProvider>
        <OperationTrackerProvider>
          <Routes>
            <Route path="/library" element={<LibraryStandIn onEnd={onEnd} />} />
            <Route path="/elsewhere" element={<Elsewhere />} />
          </Routes>
        </OperationTrackerProvider>
      </ToastProvider>
    </MemoryRouter>
  );
}

/** A poll the test settles by hand. */
function deferPoll(id = 'op-1') {
  const ctl = {
    resolve: (_status: string) => {},
    reject: (_e: unknown) => {},
  };
  api.pollOperationV2.mockImplementation((opId: string) => {
    if (opId !== id) return new Promise(() => {});
    return new Promise((resolve, reject) => {
      ctl.resolve = (status: string) => resolve({ id, status });
      ctl.reject = reject;
    });
  });
  return ctl;
}

describe('OperationTrackerProvider', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useAppStore.setState({ notifications: [] });
    api.getOperationResult.mockResolvedValue({
      result_data: { merged: 1, skipped: [], primary_id: 'a' },
    });
  });

  it('locks the books of a running operation and releases them when it ends, with an outcome toast', async () => {
    const poll = deferPoll();
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByText('merge'));
    expect(screen.getByTestId('locked')).toHaveTextContent('a,b');

    await act(async () => poll.resolve('completed'));
    await waitFor(() => expect(screen.getByTestId('locked')).toHaveTextContent(''));
    expect(lastToast().action?.label).toBe('View operation');
    expect(lastToast().message).not.toBe('');
  });

  it('keeps following when the page goes away: the outcome toast still comes and the locks survive the round trip', async () => {
    const poll = deferPoll();
    const onEnd = vi.fn();
    const user = userEvent.setup();
    render(<App onEnd={onEnd} />);
    await user.click(screen.getByText('merge'));
    await user.click(screen.getByText('leave'));
    expect(screen.queryByTestId('locked')).toBeNull();

    // Back on the page: the books are still locked, the operation still followed.
    await user.click(screen.getByText('back'));
    expect(screen.getByTestId('locked')).toHaveTextContent('a,b');
    expect(api.pollOperationV2).toHaveBeenCalledTimes(1);

    // Leave again and let it end while away: nobody is subscribed, the toast comes anyway.
    await user.click(screen.getByText('leave'));
    await act(async () => poll.resolve('completed'));
    await waitFor(() => expect(lastToast()?.action?.label).toBe('View operation'));
    await user.click(screen.getByText('back'));
    expect(screen.getByTestId('locked')).toHaveTextContent('');
    expect(onEnd).not.toHaveBeenCalled();
  });

  it('tells a page that is showing when an operation ends', async () => {
    const poll = deferPoll();
    const onEnd = vi.fn();
    const user = userEvent.setup();
    render(<App onEnd={onEnd} />);
    await user.click(screen.getByText('merge'));
    await act(async () => poll.resolve('completed'));
    await waitFor(() => expect(onEnd).toHaveBeenCalledTimes(1));
    expect(onEnd.mock.calls[0][0]).toMatchObject({
      kind: 'merge',
      status: 'completed',
      lost: false,
      descriptor: { operationId: 'op-1', bookIds: ['a', 'b'] },
    });
  });

  it('persists the in-flight set and a reload resumes it: locks come back and the outcome toast appears', async () => {
    deferPoll();
    const user = userEvent.setup();
    const first = render(<App />);
    await user.click(screen.getByText('merge'));
    expect(JSON.parse(sessionStorage.getItem(TRACKED_OPS_STORAGE_KEY) ?? '[]')).toHaveLength(1);

    // "Reload": the whole app goes away, storage stays.
    first.unmount();
    expect(sessionStorage.getItem(TRACKED_OPS_STORAGE_KEY)).not.toBeNull();
    api.pollOperationV2.mockClear();
    const poll2 = deferPoll();
    render(<App />);
    await waitFor(() => expect(screen.getByTestId('locked')).toHaveTextContent('a,b'));
    expect(api.pollOperationV2).toHaveBeenCalledTimes(1);

    await act(async () => poll2.resolve('completed'));
    await waitFor(() => expect(screen.getByTestId('locked')).toHaveTextContent(''));
    expect(lastToast().action?.label).toBe('View operation');
    expect(sessionStorage.getItem(TRACKED_OPS_STORAGE_KEY)).toBeNull();
  });

  it('ignores a malformed or stale persisted set', async () => {
    sessionStorage.setItem(TRACKED_OPS_STORAGE_KEY, '{not json');
    render(<App />);
    expect(screen.getByTestId('locked')).toHaveTextContent('');
    sessionStorage.setItem(
      TRACKED_OPS_STORAGE_KEY,
      JSON.stringify([
        { operationId: 'old', kind: 'merge', bookIds: ['z'], startedAt: Date.now() - 48 * 3600e3 },
      ])
    );
    render(<App />);
    expect(api.pollOperationV2).not.toHaveBeenCalled();
  });

  it('a duplicate track of the same operation starts one follower and cannot release early', async () => {
    const poll = deferPoll();
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByText('merge twice'));
    expect(api.pollOperationV2).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId('locked')).toHaveTextContent('a,b');
    await act(async () => poll.resolve('completed'));
    await waitFor(() => expect(screen.getByTestId('locked')).toHaveTextContent(''));
  });

  it('keys the locks per operation: a book shared by two operations stays locked until both end', async () => {
    let finishFirst: (s: string) => void = () => {};
    api.pollOperationV2.mockImplementation((opId: string) =>
      opId === 'op-1'
        ? new Promise((resolve) => (finishFirst = (status) => resolve({ id: 'op-1', status })))
        : new Promise(() => {})
    );
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByText('merge')); // a,b
    await user.click(screen.getByText('combine')); // b,c
    expect(screen.getByTestId('locked')).toHaveTextContent('a,b,c');
    await act(async () => finishFirst('completed'));
    await waitFor(() => expect(screen.getByTestId('locked')).toHaveTextContent('b,c'));
  });

  it('a combine that was refused while the library loads ends as a warning to try again: no Undo, the books are released', async () => {
    const poll = deferPoll('op-2');
    api.getOperationResult.mockResolvedValue({
      result_data: {
        primary_id: 'b',
        files_moved: 0,
        books_deleted: 0,
        retry_later: true,
        retry_reason: 'try again once the library index has finished loading',
      },
    });
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByText('combine'));
    expect(screen.getByTestId('locked')).toHaveTextContent('b,c');
    await act(async () => poll.resolve('completed'));
    await waitFor(() => expect(screen.getByTestId('locked')).toHaveTextContent(''));
    expect(lastToast().severity).toBe('warning');
    expect(lastToast().message).toContain(
      'The library is still loading; try the combine again shortly'
    );
    expect(lastToast().action?.label).toBe('View operation');
    expect(api.revertOperation).not.toHaveBeenCalled();
  });

  it('stops following an operation the server no longer knows: says the outcome is unknown and releases the books', async () => {
    const poll = deferPoll();
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByText('merge'));
    await act(async () => poll.reject(new ApiError('not found', 404)));
    await waitFor(() => expect(screen.getByTestId('locked')).toHaveTextContent(''));
    expect(lastToast().severity).toBe('warning');
    expect(lastToast().message).toContain('Lost track of operation op-1');
    expect(lastToast().message).toContain('see Activity');
    expect(api.pollOperationV2).toHaveBeenCalledTimes(1);
  });

  it('a timed-out poll is retryable: it says why in its own words and keeps the books locked', async () => {
    api.pollOperationV2.mockRejectedValueOnce(new ApiTimeoutError('/x', 30000));
    api.pollOperationV2.mockImplementation(() => new Promise(() => {}));
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByText('merge'));
    await waitFor(() => expect(lastToast()?.message).toContain('Still checking on the merge'));
    expect(lastToast().message).toContain('the server did not answer in time');
    expect(lastToast().message).not.toContain('could not be reached');
    expect(screen.getByTestId('locked')).toHaveTextContent('a,b');
  });

  it('gives every poll a request deadline', async () => {
    deferPoll();
    const user = userEvent.setup();
    render(<App />);
    await user.click(screen.getByText('merge'));
    expect(api.pollOperationV2.mock.calls[0][3]).toMatchObject({ requestTimeoutMs: 30000 });
  });
});
