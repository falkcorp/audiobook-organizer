// file: core/web/packages/shell/src/providers/operationsStore.tsx
// version: 0.1.0
// guid: e8b1c3d7-4a52-4f96-b0e1-7d29a6c5f318
// last-edited: 2026-11-21

// The shell's operations store (07 §7.11, FE5-6): the task runs the operations
// indicator and the Activity page list, as flat and grouped rows. New code over
// core's task-run wire shape; it does not port ABO's legacy store, which reads
// the books operations API and polls with books' operation-polling helpers.
//
// It is an external store read through useSyncExternalStore (the same pattern
// as tasks/store.ts), so the shell takes no state-library dependency. It does
// no I/O of its own: the host injects `fetchRuns` (GET /api/core/v1/tasks/runs
// or /timeline) and pushes live updates with applyRun (the
// core.task.run-changed.v1 topic). Polling and stream wiring are the shell's
// provider code, added when the layout and the realtime client consume it.

import { createContext, useContext, useSyncExternalStore, type ReactNode } from 'react';
import { byNewestFirst, groupOperations, type GroupableOperation } from './operationGrouping';
import { isAwaitingDecision, isLiveRunState, runTimeMs, type TaskRunStateName, type TaskRunWire } from './taskRuns';

/** A task run as the indicator and Activity page render it. */
export interface OperationRow extends GroupableOperation {
  /** The run's kind, e.g. "books.scan"; also `def_id` and `type`. Absent on a synthetic group row. */
  kind?: string;
  /** The run's state. Absent on a synthetic group row (its `status` carries the members' state). */
  state?: TaskRunStateName;
  /** Waiting for a resume, finish or drop decision. */
  awaitingDecision?: boolean;
}

export interface OperationsState {
  /** Every row of the loaded window, keyed by run ID. */
  operations: Readonly<Record<string, OperationRow>>;
  /** Every row of the window, finished ones included, newest first. */
  activeOperations: readonly OperationRow[];
  /** The rows whose run has not reached a terminal state. */
  liveOperations: readonly OperationRow[];
  /** Rows with notify_level 0: what the bell badge counts. */
  alertOperations: readonly OperationRow[];
  /** activeOperations with runs of consecutive same-kind rows folded under synthetic parents. Render this; resolve ids against activeOperations. */
  groupedOperations: readonly OperationRow[];
  /**
   * The message of the most recent FAILED load, null once a load has succeeded
   * since. It is set alongside the previous rows, never instead of them: a
   * refresh that fails keeps what the UI already had and flags it as possibly
   * stale, so "could not refresh" never reads as "no operations".
   */
  loadError: string | null;
}

export interface OperationsStoreOptions {
  /** Loads the window of runs. Must honour `signal`; it is aborted when a newer load starts. */
  fetchRuns: (signal: AbortSignal) => Promise<readonly TaskRunWire[]>;
  /** A kind's display label (a module's taskKinds entry); the kind itself when it returns undefined. */
  label?: (kind: string) => string | undefined;
  /**
   * 0 = alert, 1 = activity-only. Core's run payload carries no notify level;
   * the default raises an alert only for a run waiting on a decision, which
   * nothing else will move forward.
   */
  notifyLevel?: (run: TaskRunWire) => 0 | 1;
}

export interface OperationsStore {
  getState(): OperationsState;
  subscribe(listener: () => void): () => void;
  /**
   * Replaces the window with a fresh load. A failure keeps the rows and sets
   * loadError. Only the newest started load may apply. The loaded snapshot wins
   * over any applyRun frame received while it was in flight, so a host reloads
   * on a stream reset (useResync) and not on every frame.
   */
  loadFromServer(): Promise<void>;
  /** Inserts or replaces one run (a stream frame). A run absent from the window is added. */
  applyRun(run: TaskRunWire): void;
  /** Drops a run from the window (the run was discarded). */
  removeOperation(runId: string): void;
  /** Aborts an in-flight load; the store stays readable. */
  dispose(): void;
}

function defaultNotifyLevel(run: TaskRunWire): 0 | 1 {
  return isAwaitingDecision(run.state) ? 0 : 1;
}

const EMPTY: OperationsState = {
  operations: {},
  activeOperations: [],
  liveOperations: [],
  alertOperations: [],
  groupedOperations: [],
  loadError: null,
};

export function createOperationsStore(opts: OperationsStoreOptions): OperationsStore {
  const notify = opts.notifyLevel ?? defaultNotifyLevel;
  const listeners = new Set<() => void>();
  let rows = new Map<string, OperationRow>();
  let loadError: string | null = null;
  let state: OperationsState = EMPTY;
  let generation = 0;
  let controller: AbortController | undefined;

  const toRow = (run: TaskRunWire): OperationRow => ({
    id: run.id,
    kind: run.kind,
    def_id: run.kind,
    type: run.kind,
    displayName: opts.label?.(run.kind) ?? run.kind,
    state: run.state,
    status: run.state,
    progress: run.done,
    total: run.total,
    message: run.current ?? '',
    // A queued run has no start yet; its creation time keeps it orderable.
    startedAt: runTimeMs(run.started) ?? runTimeMs(run.created),
    finishedAt: runTimeMs(run.finished),
    parent_id: run.parent ? run.parent : null,
    notify_level: notify(run),
    awaitingDecision: isAwaitingDecision(run.state),
  });

  const rebuild = () => {
    const all = [...rows.values()].sort(byNewestFirst);
    state = {
      operations: Object.fromEntries(rows),
      activeOperations: all,
      liveOperations: all.filter((r) => isLiveRunState(r.status)),
      alertOperations: all.filter((r) => r.notify_level === 0),
      groupedOperations: groupOperations(all),
      loadError,
    };
    for (const l of [...listeners]) l();
  };

  return {
    getState: () => state,
    subscribe(listener) {
      listeners.add(listener);
      return () => {
        listeners.delete(listener);
      };
    },
    async loadFromServer() {
      const mine = ++generation;
      controller?.abort();
      const ctl = new AbortController();
      controller = ctl;
      try {
        const runs = await opts.fetchRuns(ctl.signal);
        if (mine !== generation) return;
        rows = new Map(runs.map((r) => [r.id, toRow(r)]));
        loadError = null;
      } catch (err) {
        // A superseded or disposed load is not a failure of the data.
        if (mine !== generation) return;
        loadError = err instanceof Error ? err.message : String(err);
      }
      rebuild();
    },
    applyRun(run) {
      rows = new Map(rows).set(run.id, toRow(run));
      rebuild();
    },
    removeOperation(runId) {
      if (!rows.has(runId)) return;
      rows = new Map(rows);
      rows.delete(runId);
      rebuild();
    },
    dispose() {
      generation++;
      controller?.abort();
      controller = undefined;
    },
  };
}

const OperationsStoreContext = createContext<OperationsStore | null>(null);

/** Provides `store` to useOperationsStore. The store is the host's; the provider does not dispose it. */
export function OperationsStoreProvider({ store, children }: { store: OperationsStore; children: ReactNode }) {
  return <OperationsStoreContext.Provider value={store}>{children}</OperationsStoreContext.Provider>;
}

/** The store itself, for the actions (loadFromServer, applyRun). */
export function useOperationsStoreApi(): OperationsStore {
  const store = useContext(OperationsStoreContext);
  if (!store) throw new Error('useOperationsStore needs an <OperationsStoreProvider> above it');
  return store;
}

/**
 * Reads a slice of the store, re-rendering when the selected value changes by
 * reference. Select the arrays the state already holds (they are replaced only
 * when the window changes); a selector that builds a new array each call
 * re-renders forever.
 */
export function useOperationsStore<T>(selector: (state: OperationsState) => T): T {
  const store = useOperationsStoreApi();
  return useSyncExternalStore(
    store.subscribe,
    () => selector(store.getState()),
    () => selector(store.getState()),
  );
}
