// file: web/src/utils/apiKeyExpiry.test.ts
// version: 1.0.0
// guid: 0d4f6a2b-8c1e-4b7a-9f35-6e2d8a1c4b90
// last-edited: 2026-10-07

import { describe, it, expect } from 'vitest';
import {
  API_KEY_EXPIRY_OPTIONS,
  API_KEY_MAX_TTL_DAYS,
  customExpiryBounds,
  customExpiryDays,
  daysUntilDate,
  expiryAfterDays,
  formatKeyExpiry,
} from './apiKeyExpiry';

const NOW = Date.UTC(2026, 9, 7, 12, 0, 0);
const HOUR = 3_600_000;
const at = (ms: number) => new Date(NOW + ms).toISOString();

describe('formatKeyExpiry', () => {
  it('formats a bootstrap key 8h out as hours, not "-0d ago"', () => {
    expect(formatKeyExpiry(at(8 * HOUR), NOW)).toBe('in 8h');
  });

  it('formats a default 30-day key in days', () => {
    expect(formatKeyExpiry(at(30 * 24 * HOUR), NOW)).toBe('in 30d');
  });

  it('formats a year-long key in months', () => {
    expect(formatKeyExpiry(at(365 * 24 * HOUR), NOW)).toBe('in 12mo');
  });

  it('formats under an hour in minutes', () => {
    expect(formatKeyExpiry(at(25 * 60_000), NOW)).toBe('in 25m');
  });

  it('says Expired for a past expiry', () => {
    expect(formatKeyExpiry(at(-HOUR), NOW)).toBe('Expired');
  });

  it('returns a dash for an unparseable date', () => {
    expect(formatKeyExpiry('not-a-date', NOW)).toBe('—');
  });
});

describe('API_KEY_EXPIRY_OPTIONS', () => {
  it('offers no "Never" (0) option: the server gives 0 its 30-day default', () => {
    expect(API_KEY_EXPIRY_OPTIONS.some((o) => o.value === 0)).toBe(false);
    expect(API_KEY_EXPIRY_OPTIONS.some((o) => /never/i.test(o.label))).toBe(false);
  });

  it('stays within the server maximum and labels no preset the maximum', () => {
    expect(Math.max(...API_KEY_EXPIRY_OPTIONS.map((o) => o.value))).toBeLessThanOrEqual(
      API_KEY_MAX_TTL_DAYS
    );
    expect(API_KEY_EXPIRY_OPTIONS.some((o) => /maximum/i.test(o.label))).toBe(false);
  });
});

// 2026-11-20 at noon local: three calendar years on, 2029-11-20, is 1096
// days away (2028 has a leap day), so the 1095-day maximum is 2029-11-19.
const LOCAL_NOW = new Date(2026, 10, 20, 12, 0, 0);

describe('customExpiryBounds', () => {
  it('runs from tomorrow to today plus 1095 days', () => {
    expect(customExpiryBounds(LOCAL_NOW)).toEqual({ min: '2026-11-21', max: '2029-11-19' });
  });

  it('crosses a month and year end', () => {
    expect(customExpiryBounds(new Date(2026, 11, 31, 23, 30))).toEqual({
      min: '2027-01-01',
      max: '2029-12-30',
    });
  });

  it('counts the same bounds at any time of the day', () => {
    expect(customExpiryBounds(new Date(2026, 10, 20, 0, 0, 1))).toEqual(
      customExpiryBounds(new Date(2026, 10, 20, 23, 59, 59))
    );
  });
});

describe('customExpiryDays', () => {
  it('accepts tomorrow as one day', () => {
    expect(customExpiryDays('2026-11-21', LOCAL_NOW)).toBe(1);
  });

  it('accepts the last date as exactly the maximum', () => {
    expect(customExpiryDays('2029-11-19', LOCAL_NOW)).toBe(API_KEY_MAX_TTL_DAYS);
  });

  it('refuses one day past the maximum (three calendar years)', () => {
    expect(customExpiryDays('2029-11-20', LOCAL_NOW)).toBeNull();
  });

  it('refuses today and the past', () => {
    expect(customExpiryDays('2026-11-20', LOCAL_NOW)).toBeNull();
    expect(customExpiryDays('2026-11-19', LOCAL_NOW)).toBeNull();
  });

  it('refuses empty, malformed and impossible dates', () => {
    for (const v of ['', '2027-1-5', '2027-02-30', 'tomorrow', '2027-13-01']) {
      expect(customExpiryDays(v, LOCAL_NOW)).toBeNull();
    }
  });

  it('agrees with the bounds it offers', () => {
    const { min, max } = customExpiryBounds(LOCAL_NOW);
    expect(customExpiryDays(min, LOCAL_NOW)).toBe(1);
    expect(customExpiryDays(max, LOCAL_NOW)).toBe(API_KEY_MAX_TTL_DAYS);
  });
});

describe('daysUntilDate', () => {
  it('counts calendar days, not 24-hour spans, across a clock change', () => {
    // Late evening before the US spring-forward: still whole days.
    expect(daysUntilDate('2027-03-15', new Date(2027, 2, 13, 23, 30))).toBe(2);
  });
});

describe('expiryAfterDays', () => {
  it('is exactly that many 24-hour days after now', () => {
    expect(expiryAfterDays(1095, NOW).getTime()).toBe(NOW + 1095 * 24 * HOUR);
  });
});
