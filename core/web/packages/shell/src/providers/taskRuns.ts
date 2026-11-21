// file: core/web/packages/shell/src/providers/taskRuns.ts
// version: 0.1.0
// guid: e51fa8db-303a-4f49-b0fa-f53fbbb9e5a6
// last-edited: 2026-11-21

// The wire shape of a task run and the state helpers over it (07 §7.11, FE5-6:
// the shell's Activity page, operations indicator and tracker get their own
// state helpers and never import a books operation-polling module).
//
// The shape is core's `tasks.RunPayload` (core/tasks/topics.go), which is what
// GET /api/core/v1/tasks/runs, /runs/{id}, /timeline and the
// core.task.run-changed.v1 topic all carry. core/openapi/core.json has no paths
// yet, so it is hand-typed here; when the generated client carries it, replace
// this interface with the generated type and keep the helpers.
//
//
// contracts.ts's TaskRunView, TaskRunState and TERMINAL_TASK_STATES (what useTask
// polls) are aliases of what is defined here, so the shell has one vocabulary.
// test/taskRuns.core.test.ts parses core/tasks/client.go and fails if either
// list drifts from it.

/** Every state tasks.RunState can take (core/tasks/client.go). */
export const TASK_RUN_STATES = [
  'queued',
  'waiting_deps',
  'running',
  'stopping',
  'interrupted',
  'interrupted_quiesced',
  'awaiting_decision',
  'succeeded',
  'failed',
  'timed_out',
  'canceled',
  'interrupted_dropped',
] as const;

export type TaskRunStateName = (typeof TASK_RUN_STATES)[number];

export interface TaskRunActorWire {
  user_id?: string;
  auth_method?: string;
  system?: string;
}

/** tasks.RunPayload. `started` and `finished` are omitted until they happen. */
export interface TaskRunWire {
  id: string;
  kind: string;
  mode: string;
  state: TaskRunStateName;
  held: boolean;
  reason?: string;
  actor: TaskRunActorWire;
  trigger: string;
  parent?: string;
  plan_ref?: string;
  /** RFC 3339. */
  created: string;
  started?: string;
  finished?: string;
  done: number;
  total: number;
  /** Blank unless the caller holds core:users.view (the server redacts it). */
  current?: string;
  /**
   * Why a queued run is not running yet: the dispatch gate's reason token (key,
   * writeset, ready, module_cap, zombie_cap, standdown, paused, held). Computed
   * by the executor at read time from its last dispatch pass and never stored,
   * so it is absent for a run that is not queued or that pass did not defer.
   */
  deferred?: string;
  /**
   * The running run behind a `key` or `writeset` deferral (the other reasons name none).
   * Absent when the caller may not view the blocker's kind, and always absent on the
   * realtime stream (only GET /runs carries it).
   */
  blocked_by?: string;
  /** RFC 3339: when the run's unbroken deferral began. */
  deferred_since?: string;
}

/**
 * The states a run never leaves: tasks.RunState.Terminal (core/tasks/client.go),
 * verbatim. Why each is in or out, by core's own semantics:
 *
 *   in  succeeded, failed          the executor's two outcomes.
 *   in  timed_out                  the run exceeded its limit and was stopped for good.
 *   in  canceled                   a cancel finished (one l, as core spells it).
 *   in  interrupted_dropped        resume policy Drop, or Decide(Drop): it is never run again.
 *   out queued, waiting_deps       not started yet (waiting_deps: Requires/NotWhile unmet).
 *   out running, stopping          stopping is still winding down (cancel, quiesce or shutdown).
 *   out interrupted                the resume policy decides at the next boot, so it can resume.
 *   out interrupted_quiesced       stood down; core re-queues it when the hold is released.
 *   out awaiting_decision          ResumeAsk: an operator's resume, finish or drop decision moves it on.
 */
export const TERMINAL_RUN_STATES: ReadonlySet<TaskRunStateName> = new Set<TaskRunStateName>([
  'succeeded',
  'failed',
  'timed_out',
  'canceled',
  'interrupted_dropped',
]);

/**
 * True for a state a run never leaves: tasks.RunState.Terminal. An unknown
 * state is not terminal, so a newer server's extra state keeps a run on screen
 * and polled rather than hiding it.
 */
export function isTerminalRunState(state: string): boolean {
  return (TERMINAL_RUN_STATES as ReadonlySet<string>).has(state);
}

/** True for the interrupted family: interrupted, interrupted_quiesced and interrupted_dropped. */
export function isInterruptedRunState(state: string): boolean {
  return state === 'interrupted' || state.startsWith('interrupted_');
}

/** True while a run waits for an operator's resume, finish or drop decision. */
export function isAwaitingDecision(state: string): boolean {
  return state === 'awaiting_decision';
}

/** True for a run that has not reached a terminal state (it may be queued, held or interrupted). */
export function isLiveRunState(state: string): boolean {
  return !isTerminalRunState(state);
}

/** The run's wire timestamp as epoch milliseconds, or undefined when absent or unparseable. */
export function runTimeMs(iso: string | undefined): number | undefined {
  if (!iso) return undefined;
  const ms = Date.parse(iso);
  return Number.isNaN(ms) ? undefined : ms;
}
