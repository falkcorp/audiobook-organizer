// file: web/src/pages/libraryOperationLogs.test.ts
// version: 1.0.0
// guid: e5f6a7b8-c9d0-4e1f-9a2b-3c4d5e6f7a8b
// last-edited: 2026-07-03

import { describe, it, expect } from 'vitest';
import { evictOldestOpLogKey, MAX_OPERATION_LOG_KEYS } from './libraryOperationLogs';

// Regression coverage for an unbounded-memory-growth bug: per-op log entries
// were capped at 200 lines each, but the map of op-id -> entries itself was
// never bounded, so it grew for the lifetime of the page as operations came
// and went.

type Entry = { level: string; message: string; timestamp: number };

const entries = (n: number): Entry[] => [{ level: 'info', message: `msg-${n}`, timestamp: n }];

describe('evictOldestOpLogKey', () => {
  it('evicts the oldest-inserted key once the cap is reached', () => {
    let logs: Record<string, Entry[]> = {};
    for (let i = 0; i < MAX_OPERATION_LOG_KEYS; i++) {
      const opId = `op-${i}`;
      logs = evictOldestOpLogKey(logs, opId, MAX_OPERATION_LOG_KEYS);
      logs = { ...logs, [opId]: entries(i) };
    }
    expect(Object.keys(logs)).toHaveLength(MAX_OPERATION_LOG_KEYS);

    // Adding one more beyond the cap evicts op-0 (oldest inserted).
    const newOpId = `op-${MAX_OPERATION_LOG_KEYS}`;
    logs = evictOldestOpLogKey(logs, newOpId, MAX_OPERATION_LOG_KEYS);
    logs = { ...logs, [newOpId]: entries(MAX_OPERATION_LOG_KEYS) };

    expect(Object.keys(logs)).toHaveLength(MAX_OPERATION_LOG_KEYS);
    expect(logs['op-0']).toBeUndefined();
    expect(logs['op-1']).toBeDefined();
    expect(logs[newOpId]).toBeDefined();
  });

  it('does not evict when the incoming opId already exists', () => {
    const logs: Record<string, Entry[]> = {};
    for (let i = 0; i < MAX_OPERATION_LOG_KEYS; i++) {
      logs[`op-${i}`] = entries(i);
    }
    const result = evictOldestOpLogKey(logs, 'op-0', MAX_OPERATION_LOG_KEYS);
    expect(result).toBe(logs); // same reference: no-op
    expect(Object.keys(result)).toHaveLength(MAX_OPERATION_LOG_KEYS);
  });

  it('is a no-op while under the cap', () => {
    const logs = { 'op-a': entries(1), 'op-b': entries(2) };
    const result = evictOldestOpLogKey(logs, 'op-c', MAX_OPERATION_LOG_KEYS);
    expect(result).toBe(logs);
  });
});
