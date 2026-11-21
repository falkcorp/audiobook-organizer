// file: core/web/packages/shell/test/operationsStore.test.tsx
// version: 0.1.0
// guid: 3f5a9c21-6d84-4e07-b2a3-91c8e7d04b56
// last-edited: 2026-11-21

import { act, render, screen } from '@testing-library/react';
import { describe, expect, it, vi } from 'vitest';
import {
  createOperationsStore,
  OperationsStoreProvider,
  useOperationsStore,
  type TaskRunStateName,
  type TaskRunWire,
} from '../src/providers';

const T0 = Date.UTC(2026, 10, 20, 10, 0, 0);
const iso = (ms: number) => new Date(ms).toISOString();

function run(id: string, state: TaskRunStateName, over: Partial<TaskRunWire> = {}): TaskRunWire {
  return {
    id,
    kind: 'batcave.sync',
    mode: 'plain',
    state,
    held: false,
    actor: { user_id: 'wayne-1' },
    trigger: 'manual',
    created: iso(T0),
    done: 0,
    total: 0,
    ...over,
  };
}

/** A fetcher the test settles by hand, in any order. */
function deferred() {
  const calls: { resolve: (r: TaskRunWire[]) => void; reject: (e: unknown) => void; signal: AbortSignal }[] = [];
  const fetchRuns = vi.fn(
    (signal: AbortSignal) =>
      new Promise<TaskRunWire[]>((resolve, reject) => {
        calls.push({ resolve, reject, signal });
      }),
  );
  return { calls, fetchRuns };
}

describe('operations store', () => {
  it('maps a run to a row: kind, label, progress, times and state', async () => {
    const store = createOperationsStore({
      fetchRuns: async () => [
        run('r1', 'running', {
          kind: 'batcave.scan',
          done: 3,
          total: 10,
          current: 'Arkham wing',
          started: iso(T0 + 5_000),
        }),
        run('r2', 'succeeded', { created: iso(T0 + 1_000), started: iso(T0 + 2_000), finished: iso(T0 + 9_000) }),
      ],
      label: (k) => (k === 'batcave.scan' ? 'Scan the cave' : undefined),
    });
    await store.loadFromServer();
    const { operations } = store.getState();
    expect(operations.r1).toMatchObject({
      kind: 'batcave.scan',
      def_id: 'batcave.scan',
      displayName: 'Scan the cave',
      state: 'running',
      status: 'running',
      progress: 3,
      total: 10,
      message: 'Arkham wing',
      startedAt: T0 + 5_000,
      finishedAt: undefined,
      parent_id: null,
    });
    // No label registered: the kind itself. finishedAt only for the run that ended.
    expect(operations.r2).toMatchObject({ displayName: 'batcave.sync', finishedAt: T0 + 9_000, startedAt: T0 + 2_000 });
  });

  it('orders a queued run by its creation time, which stands in for a start', async () => {
    const store = createOperationsStore({
      fetchRuns: async () => [run('r1', 'queued', { created: iso(T0 + 7_000) })],
    });
    await store.loadFromServer();
    expect(store.getState().operations.r1.startedAt).toBe(T0 + 7_000);
  });

  it('splits live, alert and all rows, newest first', async () => {
    const store = createOperationsStore({
      fetchRuns: async () => [
        run('a', 'succeeded', { finished: iso(T0 + 1_000), kind: 'batcave.scan' }),
        run('b', 'running', { started: iso(T0 + 3_000), kind: 'batcave.audit' }),
        run('c', 'awaiting_decision', { started: iso(T0 + 2_000), kind: 'batcave.repair' }),
        run('d', 'timed_out', { finished: iso(T0 + 4_000), kind: 'batcave.patrol' }),
      ],
    });
    await store.loadFromServer();
    const s = store.getState();
    expect(s.activeOperations.map((r) => r.id)).toEqual(['d', 'b', 'c', 'a']);
    // timed_out is terminal: not live. awaiting_decision is not terminal.
    expect(s.liveOperations.map((r) => r.id)).toEqual(['b', 'c']);
    // Only the run that needs a decision raises an alert by default.
    expect(s.alertOperations.map((r) => r.id)).toEqual(['c']);
  });

  it('takes the alert level from the host when it supplies one', async () => {
    const store = createOperationsStore({
      fetchRuns: async () => [run('a', 'failed', { finished: iso(T0) }), run('b', 'succeeded', { finished: iso(T0) })],
      notifyLevel: (r) => (r.state === 'failed' ? 0 : 1),
    });
    await store.loadFromServer();
    expect(store.getState().alertOperations.map((r) => r.id)).toEqual(['a']);
  });

  it('folds a run of same-kind rows into a group in groupedOperations only', async () => {
    const runs = Array.from({ length: 4 }, (_, i) =>
      run(`s${i}`, 'succeeded', { started: iso(T0 + i * 1_000), finished: iso(T0 + i * 1_000 + 500) }),
    );
    const store = createOperationsStore({ fetchRuns: async () => runs });
    await store.loadFromServer();
    const s = store.getState();
    expect(s.activeOperations).toHaveLength(4);
    expect(s.activeOperations.some((r) => r.group)).toBe(false);
    const parents = s.groupedOperations.filter((r) => r.group);
    expect(parents).toHaveLength(1);
    expect(parents[0].group?.count).toBe(4);
    // The synthetic row never reaches the bell.
    expect(s.alertOperations.some((r) => r.group)).toBe(false);
  });

  it('keeps a declared parent instead of regrouping the child', async () => {
    const runs = [
      run('p', 'running', { started: iso(T0) }),
      ...Array.from({ length: 4 }, (_, i) => run(`c${i}`, 'running', { parent: 'p', started: iso(T0 + i * 1_000) })),
    ];
    const store = createOperationsStore({ fetchRuns: async () => runs });
    await store.loadFromServer();
    const s = store.getState();
    expect(s.groupedOperations.filter((r) => r.group)).toHaveLength(0);
    expect(s.operations.c0.parent_id).toBe('p');
  });

  it('keeps the previous rows and flags the failure when a refresh fails, then clears it', async () => {
    let fail = false;
    const store = createOperationsStore({
      fetchRuns: async () => {
        if (fail) throw new Error('HTTP 503');
        return [run('a', 'running', { started: iso(T0) })];
      },
    });
    await store.loadFromServer();
    expect(store.getState().loadError).toBeNull();
    fail = true;
    await store.loadFromServer();
    expect(store.getState().loadError).toBe('HTTP 503');
    expect(store.getState().activeOperations.map((r) => r.id)).toEqual(['a']);
    fail = false;
    await store.loadFromServer();
    expect(store.getState().loadError).toBeNull();
  });

  it('lets only the newest load apply, whatever order the responses arrive in', async () => {
    const { calls, fetchRuns } = deferred();
    const store = createOperationsStore({ fetchRuns });
    const first = store.loadFromServer();
    const second = store.loadFromServer();
    expect(calls[0].signal.aborted).toBe(true);
    expect(calls[1].signal.aborted).toBe(false);
    calls[1].resolve([run('new', 'running', { started: iso(T0) })]);
    await second;
    // The superseded load answers late, with stale data, and then fails.
    calls[0].resolve([run('old', 'running', { started: iso(T0) })]);
    await first;
    expect(store.getState().activeOperations.map((r) => r.id)).toEqual(['new']);
    expect(store.getState().loadError).toBeNull();
  });

  it('does not report a superseded load as an error', async () => {
    const { calls, fetchRuns } = deferred();
    const store = createOperationsStore({ fetchRuns });
    const first = store.loadFromServer();
    const second = store.loadFromServer();
    calls[0].reject(new DOMException('aborted', 'AbortError'));
    calls[1].resolve([]);
    await Promise.all([first, second]);
    expect(store.getState().loadError).toBeNull();
  });

  it('ignores a load that finishes after dispose', async () => {
    const { calls, fetchRuns } = deferred();
    const store = createOperationsStore({ fetchRuns });
    const pending = store.loadFromServer();
    store.dispose();
    expect(calls[0].signal.aborted).toBe(true);
    calls[0].resolve([run('late', 'running')]);
    await pending;
    expect(store.getState().activeOperations).toEqual([]);
  });

  it('applies a stream frame: adds a run, then replaces it, then drops it', async () => {
    const store = createOperationsStore({ fetchRuns: async () => [] });
    await store.loadFromServer();
    store.applyRun(run('a', 'running', { started: iso(T0), done: 1, total: 4 }));
    expect(store.getState().operations.a.progress).toBe(1);
    store.applyRun(run('a', 'succeeded', { started: iso(T0), finished: iso(T0 + 2_000), done: 4, total: 4 }));
    expect(store.getState().activeOperations).toHaveLength(1);
    expect(store.getState().operations.a).toMatchObject({ status: 'succeeded', progress: 4, finishedAt: T0 + 2_000 });
    expect(store.getState().liveOperations).toHaveLength(0);
    store.removeOperation('a');
    expect(store.getState().activeOperations).toHaveLength(0);
    // Removing a run that is not there leaves the state object alone.
    const before = store.getState();
    store.removeOperation('absent');
    expect(store.getState()).toBe(before);
  });

  it('keeps the arrays stable between changes, so a selector on them does not loop', async () => {
    const store = createOperationsStore({ fetchRuns: async () => [run('a', 'running', { started: iso(T0) })] });
    await store.loadFromServer();
    const a = store.getState();
    expect(store.getState().activeOperations).toBe(a.activeOperations);
  });

  it('re-renders a component that selects from it', async () => {
    const store = createOperationsStore({ fetchRuns: async () => [] });
    function Probe() {
      const n = useOperationsStore((s) => s.liveOperations.length);
      const err = useOperationsStore((s) => s.loadError);
      return <div data-testid="p">{`${n}|${err ?? ''}`}</div>;
    }
    render(
      <OperationsStoreProvider store={store}>
        <Probe />
      </OperationsStoreProvider>,
    );
    expect(screen.getByTestId('p').textContent).toBe('0|');
    await act(async () => {
      store.applyRun(run('a', 'running', { started: iso(T0) }));
    });
    expect(screen.getByTestId('p').textContent).toBe('1|');
  });

  it('refuses to be read outside a provider', () => {
    function Probe() {
      useOperationsStore((s) => s.loadError);
      return null;
    }
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {});
    expect(() => render(<Probe />)).toThrow(/OperationsStoreProvider/);
    spy.mockRestore();
  });
});
