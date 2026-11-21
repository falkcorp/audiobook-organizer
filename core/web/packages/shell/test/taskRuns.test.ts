// file: core/web/packages/shell/test/taskRuns.test.ts
// version: 0.1.0
// guid: 0b875fb7-9ce9-43a7-ac5b-6526c36563f4
// last-edited: 2026-11-21

import { describe, expect, it } from 'vitest';
import {
  isAwaitingDecision,
  isInterruptedRunState,
  isLiveRunState,
  isTerminalRunState,
  runTimeMs,
  TASK_RUN_STATES,
} from '../src/providers/taskRuns';

describe('task run states', () => {
  // The terminal set is core's tasks.RunState.Terminal: succeeded, failed,
  // timed_out, canceled and interrupted_dropped, nothing else.
  it('treats exactly the five terminal states as terminal', () => {
    const terminal = TASK_RUN_STATES.filter(isTerminalRunState);
    expect([...terminal].sort()).toEqual(['canceled', 'failed', 'interrupted_dropped', 'succeeded', 'timed_out']);
  });

  it('keeps a run in every non-terminal state live', () => {
    for (const s of ['queued', 'waiting_deps', 'running', 'stopping', 'interrupted', 'interrupted_quiesced', 'awaiting_decision']) {
      expect(isLiveRunState(s), s).toBe(true);
    }
  });

  it('does not call a state from a newer server terminal', () => {
    expect(isTerminalRunState('rebased')).toBe(false);
    expect(isLiveRunState('rebased')).toBe(true);
  });

  it('spells canceled with one l, as core does', () => {
    expect(isTerminalRunState('canceled')).toBe(true);
    expect(isTerminalRunState('cancelled')).toBe(false);
  });

  it('puts the interrupted family together, dropped included', () => {
    expect(isInterruptedRunState('interrupted')).toBe(true);
    expect(isInterruptedRunState('interrupted_quiesced')).toBe(true);
    expect(isInterruptedRunState('interrupted_dropped')).toBe(true);
    expect(isInterruptedRunState('running')).toBe(false);
  });

  it('recognises a run waiting for a decision', () => {
    expect(isAwaitingDecision('awaiting_decision')).toBe(true);
    expect(isAwaitingDecision('interrupted')).toBe(false);
  });

  it('parses a wire timestamp to epoch ms and refuses junk', () => {
    expect(runTimeMs('2026-11-20T10:00:00Z')).toBe(Date.UTC(2026, 10, 20, 10, 0, 0));
    expect(runTimeMs(undefined)).toBeUndefined();
    expect(runTimeMs('')).toBeUndefined();
    expect(runTimeMs('not a time')).toBeUndefined();
  });
});
