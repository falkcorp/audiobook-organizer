// file: core/web/packages/shell/src/tasks/TaskTracker.tsx
// version: 0.1.0
// guid: 77ce9687-ad40-49a1-a03f-2457a3508711
// last-edited: 2026-11-20

// useTask / useTaskError (07 §7.4.2): the state of a task run, polled through
// the shared store of the nearest <TaskTrackerProvider>. Realtime push wiring
// is the shell's, not this file's.

import { createContext, useCallback, useContext, useEffect, useState, useSyncExternalStore, type ReactNode } from 'react';
import type { TaskRunView } from '../contracts';
import { createTaskStore, type FetchRun, type TaskStore } from './store';

const TaskStoreContext = createContext<TaskStore | null>(null);

export function TaskTrackerProvider({ fetchRun, children }: { fetchRun: FetchRun; children: ReactNode }) {
  const [store] = useState(() => createTaskStore(fetchRun));
  useEffect(() => {
    store.setFetchRun(fetchRun);
  }, [store, fetchRun]);
  return <TaskStoreContext.Provider value={store}>{children}</TaskStoreContext.Provider>;
}

function useStore(): TaskStore {
  const store = useContext(TaskStoreContext);
  if (!store) throw new Error('useTask must be used inside <TaskTrackerProvider>');
  return store;
}

const noop = () => {};

function useWatch(runId: string | undefined): (onChange: () => void) => () => void {
  const store = useStore();
  return useCallback((onChange) => (runId === undefined ? noop : store.watch(runId, onChange)), [store, runId]);
}

/** The latest view of the run, `undefined` until the first answer (or without a `runId`). */
export function useTask(runId: string | undefined): TaskRunView | undefined {
  const store = useStore();
  const subscribe = useWatch(runId);
  return useSyncExternalStore(subscribe, () => (runId === undefined ? undefined : store.getView(runId)));
}

/** The message of the last failed poll of the run, cleared by the next good one. */
export function useTaskError(runId: string | undefined): string | undefined {
  const store = useStore();
  const subscribe = useWatch(runId);
  return useSyncExternalStore(subscribe, () => (runId === undefined ? undefined : store.getError(runId)));
}
