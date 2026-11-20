// file: core/web/packages/shell/src/tasks/store.ts
// version: 0.1.0
// guid: 86a46d6e-7dd1-4c8d-a8bf-4854d73a5faa
// last-edited: 2026-11-20

// The shared task poll behind useTask: one poll per run ID however many hooks
// watch it. It polls an unfinished run every POLL_INTERVAL_MS, stops on a
// terminal state, and aborts plus clears its timer when the last watcher leaves.

import { TERMINAL_TASK_STATES, type TaskRunView } from '../contracts';

export const POLL_INTERVAL_MS = 2000;

export type FetchRun = (runId: string, signal: AbortSignal) => Promise<TaskRunView>;

interface Entry {
  view: TaskRunView | undefined;
  error: string | undefined;
  listeners: Set<() => void>;
  timer: ReturnType<typeof setTimeout> | undefined;
  controller: AbortController | undefined;
}

export interface TaskStore {
  setFetchRun(fetchRun: FetchRun): void;
  /** Registers a watcher of `runId` (starting its poll if it is the first); returns the unwatch function. */
  watch(runId: string, onChange: () => void): () => void;
  getView(runId: string): TaskRunView | undefined;
  getError(runId: string): string | undefined;
}

function messageOf(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export function createTaskStore(initial: FetchRun): TaskStore {
  let fetchRun = initial;
  const entries = new Map<string, Entry>();

  const emit = (e: Entry) => {
    for (const l of [...e.listeners]) l();
  };

  async function tick(runId: string, e: Entry): Promise<void> {
    const controller = new AbortController();
    e.controller = controller;
    e.timer = undefined;
    try {
      const view = await fetchRun(runId, controller.signal);
      if (controller.signal.aborted) return;
      e.view = view;
      e.error = undefined;
      emit(e);
      if (TERMINAL_TASK_STATES.has(view.state)) return;
    } catch (err) {
      if (controller.signal.aborted) return;
      // Kept and exposed (useTaskError); the poll carries on, the last view stays.
      e.error = messageOf(err);
      emit(e);
    }
    e.timer = setTimeout(() => void tick(runId, e), POLL_INTERVAL_MS);
  }

  return {
    setFetchRun(next) {
      fetchRun = next;
    },
    watch(runId, onChange) {
      let e = entries.get(runId);
      if (!e) {
        e = { view: undefined, error: undefined, listeners: new Set(), timer: undefined, controller: undefined };
        entries.set(runId, e);
        void tick(runId, e);
      }
      e.listeners.add(onChange);
      const entry = e;
      return () => {
        entry.listeners.delete(onChange);
        if (entry.listeners.size > 0) return;
        entry.controller?.abort();
        clearTimeout(entry.timer);
        if (entries.get(runId) === entry) entries.delete(runId);
      };
    },
    getView: (runId) => entries.get(runId)?.view,
    getError: (runId) => entries.get(runId)?.error,
  };
}
