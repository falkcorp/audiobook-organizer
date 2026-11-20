// file: web/src/services/coreEvents/backoff.test.ts
// version: 0.1.0
// guid: 1f2a1981-44c6-4464-9b4d-980e46500bce
// last-edited: 2026-11-19

import { describe, expect, it } from 'vitest';

import { MAX_DELAY_MS, parseRetryAfter, reconnectDelay } from './backoff';

describe('reconnectDelay', () => {
  it('doubles from 3 s to a 60 s ceiling and stays there', () => {
    const got = [1, 2, 3, 4, 5, 6, 7, 40].map((n) => reconnectDelay(n, null, () => 0));
    expect(got).toEqual([3000, 6000, 12000, 24000, 48000, 60000, 60000, 60000]);
  });

  it('jitters downward only: never above the nominal, never below 75 percent of it', () => {
    for (const n of [1, 3, 6, 9]) {
      const nominal = reconnectDelay(n, null, () => 0);
      const lo = reconnectDelay(n, null, () => 0.999999);
      expect(lo).toBeLessThanOrEqual(nominal);
      expect(lo).toBeGreaterThanOrEqual(Math.floor(nominal * 0.75));
      expect(reconnectDelay(n, null, () => 0.5)).toBeGreaterThan(lo - 1);
    }
  });

  it('is never sooner than Retry-After, even above the ceiling', () => {
    expect(reconnectDelay(1, 30000, () => 0.9)).toBe(30000);
    expect(reconnectDelay(1, 5 * MAX_DELAY_MS, () => 0)).toBe(5 * MAX_DELAY_MS);
    expect(reconnectDelay(6, 1000, () => 0)).toBe(60000);
  });
});

describe('parseRetryAfter', () => {
  it('reads delta-seconds and HTTP dates, and ignores junk', () => {
    expect(parseRetryAfter('30')).toBe(30000);
    expect(parseRetryAfter('0')).toBe(0);
    const now = Date.parse('2026-11-19T00:00:00Z');
    expect(parseRetryAfter('Thu, 19 Nov 2026 00:01:00 GMT', now)).toBe(60000);
    expect(parseRetryAfter('Wed, 18 Nov 2026 00:00:00 GMT', now)).toBe(0);
    expect(parseRetryAfter('soon')).toBeNull();
    expect(parseRetryAfter('')).toBeNull();
    expect(parseRetryAfter(null)).toBeNull();
  });
});
