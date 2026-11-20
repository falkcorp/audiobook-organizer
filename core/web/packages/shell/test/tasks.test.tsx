// file: core/web/packages/shell/test/tasks.test.tsx
// version: 0.1.0
// guid: f09327a4-9620-4ab7-8bdd-9e85d9fcd3c5
// last-edited: 2026-11-20

import { act, render, screen } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import type { TaskRunState, TaskRunView } from '../src/contracts';
import { TaskTrackerProvider, useTask, useTaskError } from '../src/tasks';

const run = (id: string, state: TaskRunState): TaskRunView => ({ id, kind: 'batcave.scan', state });

function Probe({ id, label }: { id: string | undefined; label: string }) {
  const view = useTask(id);
  const err = useTaskError(id);
  return <div data-testid={label}>{`${view?.state ?? 'none'}|${err ?? ''}`}</div>;
}

const advance = (ms: number) => act(async () => { await vi.advanceTimersByTimeAsync(ms); });
const flush = () => advance(0);

beforeEach(() => {
  vi.useFakeTimers();
});
afterEach(() => {
  vi.useRealTimers();
});

describe('task tracker', () => {
  it('shares one poll between hooks on the same run ID', async () => {
    const fetchRun = vi.fn(async (id: string) => run(id, 'running'));
    render(
      <TaskTrackerProvider fetchRun={fetchRun}>
        <Probe id="gadgets" label="a" />
        <Probe id="gadgets" label="b" />
      </TaskTrackerProvider>,
    );
    await flush();
    expect(fetchRun).toHaveBeenCalledTimes(1);
    expect(screen.getByTestId('a').textContent).toBe('running|');
    expect(screen.getByTestId('b').textContent).toBe('running|');
    await advance(2000);
    expect(fetchRun).toHaveBeenCalledTimes(2);
    await advance(1999);
    expect(fetchRun).toHaveBeenCalledTimes(2);
    await advance(1);
    expect(fetchRun).toHaveBeenCalledTimes(3);
  });

  it('polls different run IDs separately', async () => {
    const fetchRun = vi.fn(async (id: string) => run(id, 'running'));
    render(
      <TaskTrackerProvider fetchRun={fetchRun}>
        <Probe id="gadgets" label="a" />
        <Probe id="cowl" label="b" />
      </TaskTrackerProvider>,
    );
    await flush();
    expect(fetchRun.mock.calls.map(([id]) => id).sort()).toEqual(['cowl', 'gadgets']);
  });

  it('stops polling on a terminal state', async () => {
    const states: TaskRunState[] = ['queued', 'running', 'succeeded'];
    const fetchRun = vi.fn(async (id: string) => run(id, states.shift() ?? 'running'));
    render(
      <TaskTrackerProvider fetchRun={fetchRun}>
        <Probe id="gadgets" label="a" />
      </TaskTrackerProvider>,
    );
    await flush();
    await advance(2000);
    await advance(2000);
    expect(screen.getByTestId('a').textContent).toBe('succeeded|');
    expect(fetchRun).toHaveBeenCalledTimes(3);
    await advance(20000);
    expect(fetchRun).toHaveBeenCalledTimes(3);
  });

  it('aborts the in-flight request on unmount', async () => {
    let signal: AbortSignal | undefined;
    const fetchRun = vi.fn((_id: string, s: AbortSignal) => {
      signal = s;
      return new Promise<TaskRunView>(() => {});
    });
    const { unmount } = render(
      <TaskTrackerProvider fetchRun={fetchRun}>
        <Probe id="gadgets" label="a" />
      </TaskTrackerProvider>,
    );
    await flush();
    expect(signal?.aborted).toBe(false);
    unmount();
    expect(signal?.aborted).toBe(true);
  });

  it('clears the pending timer on unmount', async () => {
    const fetchRun = vi.fn(async (id: string) => run(id, 'running'));
    const { unmount } = render(
      <TaskTrackerProvider fetchRun={fetchRun}>
        <Probe id="gadgets" label="a" />
      </TaskTrackerProvider>,
    );
    await flush();
    expect(vi.getTimerCount()).toBe(1);
    unmount();
    expect(vi.getTimerCount()).toBe(0);
    await advance(10000);
    expect(fetchRun).toHaveBeenCalledTimes(1);
  });

  it('keeps polling while one of two hooks remains, and stops when the last leaves', async () => {
    const fetchRun = vi.fn(async (id: string) => run(id, 'running'));
    const tree = (both: boolean) => (
      <TaskTrackerProvider fetchRun={fetchRun}>
        <Probe id="gadgets" label="a" />
        {both && <Probe id="gadgets" label="b" />}
      </TaskTrackerProvider>
    );
    const { rerender, unmount } = render(tree(true));
    await flush();
    rerender(tree(false));
    await advance(2000);
    expect(fetchRun).toHaveBeenCalledTimes(2);
    unmount();
    await advance(10000);
    expect(fetchRun).toHaveBeenCalledTimes(2);
  });

  it('aborts the old run and polls the new one when the run ID changes', async () => {
    const signals = new Map<string, AbortSignal>();
    const fetchRun = vi.fn(async (id: string, s: AbortSignal) => {
      signals.set(id, s);
      return run(id, 'running');
    });
    const tree = (id: string) => (
      <TaskTrackerProvider fetchRun={fetchRun}>
        <Probe id={id} label="a" />
      </TaskTrackerProvider>
    );
    const { rerender } = render(tree('gadgets'));
    await flush();
    rerender(tree('cowl'));
    await flush();
    expect(signals.get('gadgets')?.aborted).toBe(true);
    expect(signals.get('cowl')?.aborted).toBe(false);
    expect(screen.getByTestId('a').textContent).toBe('running|');
    await advance(2000);
    expect(fetchRun.mock.calls.filter(([id]) => id === 'gadgets')).toHaveLength(1);
  });

  it('does not poll without a run ID', async () => {
    const fetchRun = vi.fn(async (id: string) => run(id, 'running'));
    render(
      <TaskTrackerProvider fetchRun={fetchRun}>
        <Probe id={undefined} label="a" />
      </TaskTrackerProvider>,
    );
    await advance(10000);
    expect(fetchRun).not.toHaveBeenCalled();
    expect(screen.getByTestId('a').textContent).toBe('none|');
  });

  it('exposes a fetch error, keeps the last view, keeps polling, and clears it on the next good answer', async () => {
    const answers: (() => Promise<TaskRunView>)[] = [
      async () => run('gadgets', 'running'),
      async () => { throw new Error('batcomputer offline'); },
      async () => run('gadgets', 'succeeded'),
    ];
    const fetchRun = vi.fn(() => answers.shift()!());
    render(
      <TaskTrackerProvider fetchRun={fetchRun}>
        <Probe id="gadgets" label="a" />
      </TaskTrackerProvider>,
    );
    await flush();
    expect(screen.getByTestId('a').textContent).toBe('running|');
    await advance(2000);
    expect(screen.getByTestId('a').textContent).toBe('running|batcomputer offline');
    await advance(2000);
    expect(screen.getByTestId('a').textContent).toBe('succeeded|');
  });

  it('exposes an error before any view exists', async () => {
    const fetchRun = vi.fn(async (): Promise<TaskRunView> => { throw new Error('no such run'); });
    render(
      <TaskTrackerProvider fetchRun={fetchRun}>
        <Probe id="gadgets" label="a" />
      </TaskTrackerProvider>,
    );
    await flush();
    expect(screen.getByTestId('a').textContent).toBe('none|no such run');
  });

  it('throws when used outside the provider', () => {
    const spy = vi.spyOn(console, 'error').mockImplementation(() => {});
    expect(() => render(<Probe id="gadgets" label="a" />)).toThrow(/TaskTrackerProvider/);
    spy.mockRestore();
  });
});
