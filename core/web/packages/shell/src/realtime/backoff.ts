// file: core/web/packages/shell/src/realtime/backoff.ts
// version: 0.1.0
// guid: 7ce90452-7d23-4e59-88d4-bd7edf2d8a67
// last-edited: 2026-11-20

// Reconnect timing (07 §7.9.2: "3 s doubling to 60 s", today's
// eventSourceManager.ts:25-29; 05-realtime §4.8 item 3: "and jitter, and never
// sooner than a Retry-After").

export const BACKOFF_MIN_MS = 3_000;
export const BACKOFF_MAX_MS = 60_000;

/**
 * The delay before reconnect number `attempt` (0 is the first). The ceiling
 * doubles from 3 s and stops at 60 s; the jitter only ever subtracts, up to a
 * quarter of the ceiling, so the delay never exceeds the spec's numbers and a
 * crowd of tabs that lost one server together do not retry in step.
 * `random` returns [0, 1).
 */
export function backoffDelay(attempt: number, random: () => number): number {
  const ceiling = Math.min(BACKOFF_MAX_MS, BACKOFF_MIN_MS * 2 ** attempt);
  return Math.round(ceiling * (0.75 + 0.25 * random()));
}

/**
 * A `Retry-After` header as milliseconds from `nowMs`: delta-seconds or an
 * HTTP-date. Absent or unparseable is 0 (no extra wait).
 */
export function retryAfterMs(header: string | null, nowMs: number): number {
  if (header === null) return 0;
  const value = header.trim();
  if (/^\d+$/.test(value)) return Number(value) * 1000;
  const at = Date.parse(value);
  return Number.isNaN(at) ? 0 : Math.max(0, at - nowMs);
}
