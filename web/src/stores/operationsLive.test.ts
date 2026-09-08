// file: web/src/stores/operationsLive.test.ts
// version: 1.0.0
// guid: 7e91c40a-2db5-4863-9f27-1a5c8e30b6d4
// last-edited: 2026-09-08

// liveOperations exists because activeOperations does not mean what its name
// says: it is every op in the loaded 24-hour window, finished ones included.
// The Activity page read it for a count labelled "Active Operations" and for
// its auto-refresh interval, so a server with a day of history and nothing
// running showed "Active Operations (91)" and polled every 5 seconds forever.
//
// The statuses below are the ones that actually caused it. The backend mints an
// interrupted_* status per ResumePolicy, and the page's old hand-written
// terminal list named only two of them.

import { describe, it, expect, beforeEach } from 'vitest';
import { useOperationsStore, type ActiveOperation } from './useOperationsStore';

function op(id: string, status: string): ActiveOperation {
  return { id, type: 'scan', status, progress: 0, total: 0, message: '' };
}

function seed(ops: ActiveOperation[]) {
  useOperationsStore.setState({
    operations: Object.fromEntries(ops.map((o) => [o.id, o])),
  });
  // Re-derive through the store's own path rather than setting the arrays by
  // hand, so this tests deriveOperationArrays and not the fixture.
  useOperationsStore.getState().updateOperation(ops[0]);
}

describe('liveOperations', () => {
  beforeEach(() => {
    useOperationsStore.setState({
      operations: {},
      activeOperations: [],
      liveOperations: [],
      alertOperations: [],
    });
  });

  it('excludes every terminal status, including the whole interrupted_ family', () => {
    seed([
      op('a', 'running'),
      op('b', 'queued'),
      op('c', 'completed'),
      op('d', 'failed'),
      op('e', 'canceled'),
      op('f', 'interrupted'),
      op('g', 'interrupted_quiesced'),
      op('h', 'interrupted_dropped'),
      op('i', 'interrupted_restart'),
      op('j', 'interrupted_ask'),
    ]);

    const { activeOperations, liveOperations } = useOperationsStore.getState();
    expect(activeOperations).toHaveLength(10);
    // Only 'running' and 'queued' are live. interrupted_quiesced and
    // interrupted_ask were the ones the old list missed.
    expect(liveOperations.map((o) => o.id).sort()).toEqual(['a', 'b']);
  });

  it('keeps a genuinely running op, so the guard cannot be satisfied by returning nothing', () => {
    seed([op('a', 'running')]);
    expect(useOperationsStore.getState().liveOperations).toHaveLength(1);
  });
});
