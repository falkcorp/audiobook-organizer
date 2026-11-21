// file: core/web/packages/shell/test/layout/shellBindings.test.tsx
// version: 0.1.0
// guid: 8d3e5a71-0c29-4f64-b6a8-e2f9c1d47b05
// last-edited: 2026-11-21

// The shell's own bindings of the layout frames: the operations bell over the
// shell store and core's run states, and the user menu over useAuth. Gotham runs,
// core's state names, no audiobook module.

import { act, fireEvent, render, screen, within } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { describe, expect, it, vi } from 'vitest';
import { AuthContext } from '../../src/auth';
import type { AuthState } from '../../src/contracts';
import {
  createOperationsStore,
  OperationsStoreProvider,
  type TaskRunStateName,
  type TaskRunWire,
} from '../../src/providers';
import { OperationsIndicator, shellRunStatus, UserMenu } from '../../src/layout';

const iso = (ms: number) => new Date(ms).toISOString();
const T0 = Date.UTC(2026, 10, 20, 10, 0, 0);

function run(id: string, state: TaskRunStateName, over: Partial<TaskRunWire> = {}): TaskRunWire {
  return {
    id,
    kind: 'batcave.scan',
    mode: 'plain',
    state,
    held: false,
    actor: { user_id: 'wayne-1' },
    trigger: 'manual',
    created: iso(T0),
    started: iso(T0 + 1_000),
    done: 0,
    total: 0,
    ...over,
  };
}

function mountBell(
  runs: TaskRunWire[],
  extra: {
    fetchRuns?: () => Promise<TaskRunWire[]>;
    onCancel?: (id: string) => Promise<unknown>;
    notifyLevel?: (r: TaskRunWire) => 0 | 1;
  } = {},
) {
  const fetchRuns = extra.fetchRuns ?? (async () => runs);
  const store = createOperationsStore({
    fetchRuns,
    label: (k) => (k === 'batcave.scan' ? 'Scan the cave' : undefined),
    notifyLevel: extra.notifyLevel,
  });
  const onCancel = extra.onCancel ?? vi.fn().mockResolvedValue(undefined);
  render(
    <MemoryRouter>
      <OperationsStoreProvider store={store}>
        <OperationsIndicator
          onCancel={onCancel}
          renderActivity={(t) => <div data-testid="pane">{t.id}</div>}
          allOperationsPath="/cave/ops"
          activityPath="/cave/log"
        />
      </OperationsStoreProvider>
    </MemoryRouter>,
  );
  return { store, onCancel, fetchRuns };
}

describe('shellRunStatus', () => {
  it('treats terminal and interrupted states as settled, and only unstarted ones as queued', () => {
    for (const s of ['succeeded', 'failed', 'timed_out', 'canceled', 'interrupted_dropped', 'interrupted', 'interrupted_quiesced']) {
      expect(shellRunStatus.isTerminal(s), s).toBe(true);
    }
    for (const s of ['queued', 'waiting_deps', 'running', 'stopping', 'awaiting_decision']) {
      expect(shellRunStatus.isTerminal(s), s).toBe(false);
    }
    expect(['queued', 'waiting_deps', 'running'].map(shellRunStatus.isQueued)).toEqual([true, true, false]);
    expect(['succeeded', 'failed', 'canceled'].map(shellRunStatus.isSuccess)).toEqual([true, false, false]);
    expect(['failed', 'timed_out', 'succeeded', 'canceled'].map(shellRunStatus.isFailure)).toEqual([true, true, false, false]);
  });
});

describe('shell OperationsIndicator', () => {
  it('shows the store: label as the name, in-progress count on the bell, outcomes from core states', async () => {
    const { store } = mountBell([
      run('r-run', 'running', { done: 2, total: 4 }),
      run('r-wait', 'waiting_deps', { kind: 'batcave.sweep' }),
      run('r-ok', 'succeeded', { finished: iso(T0 + 9_000) }),
      run('r-late', 'timed_out', { finished: iso(T0 + 8_000) }),
      run('r-int', 'interrupted', { finished: iso(T0 + 7_000) }),
    ],
      { notifyLevel: () => 0 },
    );
    await act(() => store.loadFromServer());
    const bell = screen.getByTestId('operations-bell');
    // running + waiting_deps are in progress; succeeded, timed_out and interrupted are settled.
    expect(bell).toHaveAttribute('aria-label', '2 active operations');
    fireEvent.click(bell);
    const pop = screen.getByRole('presentation');
    expect(within(pop).getAllByText('Scan the cave').length).toBeGreaterThan(0);
    expect(within(pop).getByText('batcave.sweep')).toBeInTheDocument();
    fireEvent.click(within(pop).getByText(/^Completed \(3\)/));
    expect(within(pop).getByText('success')).toBeInTheDocument();
    expect(within(pop).getByText('timed_out')).toBeInTheDocument();
    expect(within(pop).getByText('interrupted')).toBeInTheDocument();
  });

  it('badges only what the store raises an alert for: by default a run awaiting a decision, not a running one', async () => {
    const { store } = mountBell([run('r-run', 'running'), run('r-ask', 'awaiting_decision'), run('r-ok', 'succeeded')]);
    await act(() => store.loadFromServer());
    expect(screen.getByTestId('operations-bell')).toHaveAttribute('aria-label', '1 active operation');
  });

  it('cancels a running run by its id and reloads from the store on Retry', async () => {
    const fetchRuns = vi.fn<() => Promise<TaskRunWire[]>>().mockRejectedValue(new Error('bat-signal down'));
    const { store, onCancel } = mountBell([], { fetchRuns });
    await act(() => store.loadFromServer());
    fireEvent.click(screen.getByTestId('operations-bell'));
    expect(screen.getByText(/bat-signal down/)).toBeInTheDocument();
    fetchRuns.mockResolvedValue([run('r-run', 'running')]);
    fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
    expect(fetchRuns).toHaveBeenCalledTimes(2);
    await act(async () => {});
    fireEvent.click(await screen.findByRole('button', { name: 'Cancel' }));
    expect(onCancel).toHaveBeenCalledExactlyOnceWith('r-run');
  });
});

describe('shell UserMenu', () => {
  const auth = (over: Partial<AuthState> = {}): AuthState => ({
    status: 'authenticated',
    user: { id: 'wayne-1', username: 'bruce', display_name: 'Bruce Wayne', email: 'bruce@wayne.example' },
    permissions: [],
    method: 'session',
    ownerVerified: true,
    setupRequired: false,
    authDisabled: false,
    error: null,
    refresh: async () => {},
    logout: async () => {},
    ...over,
  });
  const mount = (state: AuthState) =>
    render(
      <MemoryRouter>
        <AuthContext.Provider value={state}>
          <UserMenu onSaveProfile={vi.fn().mockResolvedValue(undefined)} onChangePassword={vi.fn().mockResolvedValue(undefined)} />
        </AuthContext.Provider>
      </MemoryRouter>,
    );

  it('shows the signed-in user from useAuth', () => {
    mount(auth());
    expect(screen.getByText('bruce')).toBeInTheDocument();
  });

  it('renders nothing for an anonymous visitor', () => {
    const { container } = mount(auth({ status: 'anonymous', user: null }));
    expect(container).toBeEmptyDOMElement();
  });
});
