// file: web/src/services/coreEvents/backoff.ts
// version: 0.1.0
// guid: 5a17afef-e995-43c3-8caa-daed457c7b31
// last-edited: 2026-11-19

// Reconnect timing (05-realtime 4.8 item 3): 3 s doubling to 60 s, jittered, never
// sooner than a Retry-After, and no attempt limit. The previous manager gave up after
// five failures; realtime is a hint, so this one never does.

export const BASE_DELAY_MS = 3000;
export const MAX_DELAY_MS = 60000;
/**
 * Ceiling on a server Retry-After (10 minutes). Honouring a header above the 60 s
 * backoff cap is right (05-realtime 4.8 item 3), but an unbounded value overflows
 * setTimeout (a delay above 2^31-1 ms fires after ~1 ms: a hot reconnect loop) and a
 * non-finite one is nonsense.
 */
export const MAX_RETRY_AFTER_MS = 10 * 60 * 1000;
/** Jitter shaves up to this fraction off the nominal delay (so it never exceeds the cap). */
export const JITTER_FRACTION = 0.25;

/**
 * Delay before attempt number `attempt` (1 = the first retry). `random` returns a
 * value in [0, 1). The result is in [0.75 * nominal, nominal], and at least
 * `retryAfterMs` when the server named one (a Retry-After above the cap is honoured).
 */
export function reconnectDelay(
  attempt: number,
  retryAfterMs: number | null = null,
  random: () => number = Math.random,
): number {
  const exp = Math.max(attempt - 1, 0);
  const nominal = Math.min(BASE_DELAY_MS * 2 ** Math.min(exp, 16), MAX_DELAY_MS);
  const jittered = Math.round(nominal * (1 - JITTER_FRACTION * random()));
  return Math.max(jittered, clampRetryAfter(retryAfterMs ?? 0));
}

/** Clamps a wait to [0, MAX_RETRY_AFTER_MS]; non-finite values become the ceiling. */
export function clampRetryAfter(ms: number): number {
  if (Number.isNaN(ms)) return 0;
  return Math.min(Math.max(ms, 0), MAX_RETRY_AFTER_MS);
}

/**
 * Parses a Retry-After header: delta-seconds or an HTTP date. Null when absent or
 * unreadable (a bad header must not shorten the wait, it just adds nothing).
 */
export function parseRetryAfter(value: string | null | undefined, now: number = Date.now()): number | null {
  if (value == null) return null;
  const v = value.trim();
  if (v === '') return null;
  if (/^\d+$/.test(v)) return clampRetryAfter(Number(v) * 1000);
  const at = Date.parse(v);
  if (Number.isNaN(at)) return null;
  return clampRetryAfter(at - now);
}
