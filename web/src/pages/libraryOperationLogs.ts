// file: web/src/pages/libraryOperationLogs.ts
// version: 1.0.0
// guid: 07a1b2c3-d4e5-4f60-8a7b-8c9d0e1f2a3b
// last-edited: 2026-07-03

// Per-op log entries in Library.tsx are already capped (200 lines each), but
// the map of op-id -> entries itself was never bounded, so it grew for the
// lifetime of the page as operations came and went. Cap the number of
// retained op keys and evict the oldest-inserted one (by object key
// insertion order) when adding a new key beyond the cap.
export const MAX_OPERATION_LOG_KEYS = 20;

export function evictOldestOpLogKey<T>(
  logs: Record<string, T>,
  newOpId: string,
  maxKeys: number
): Record<string, T> {
  if (Object.prototype.hasOwnProperty.call(logs, newOpId)) {
    return logs;
  }
  const keys = Object.keys(logs);
  if (keys.length < maxKeys) {
    return logs;
  }
  const oldestKey = keys[0];
  const next = { ...logs };
  delete next[oldestKey];
  return next;
}
