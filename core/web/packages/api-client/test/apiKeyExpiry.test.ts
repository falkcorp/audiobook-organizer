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

// 2026-11-20 at noon local: the anniversary three years on, 2029-11-20, is
// 1096 days away (2028 has a leap day), which is exactly the maximum.
const LOCAL_NOW = new Date(2026, 10, 20, 12, 0, 0);

describe('customExpiryBounds', () => {
  it('runs from tomorrow to today plus 1096 days, the anniversary across a leap day', () => {
    expect(customExpiryBounds(LOCAL_NOW)).toEqual({ min: '2026-11-21', max: '2029-11-20' });
  });

  it('runs one day past the anniversary when the three years hold no leap day', () => {
    // 2028-03-01 to 2031-03-01 misses both 2028-02-29 and 2032-02-29: 1095 days.
    const now = new Date(2028, 2, 1, 12, 0);
    expect(customExpiryBounds(now).max).toBe('2031-03-02');
    expect(customExpiryDays('2031-03-01', now)).toBe(1095);
    expect(customExpiryDays('2031-03-02', now)).toBe(API_KEY_MAX_TTL_DAYS);
  });

  it('crosses a month and year end', () => {
    expect(customExpiryBounds(new Date(2026, 11, 31, 23, 30))).toEqual({
      min: '2027-01-01',
      max: '2029-12-31',
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

  it('accepts the anniversary as exactly the maximum', () => {
    expect(customExpiryDays('2029-11-20', LOCAL_NOW)).toBe(API_KEY_MAX_TTL_DAYS);
  });

  it('refuses one day past the maximum', () => {
    expect(customExpiryDays('2029-11-21', LOCAL_NOW)).toBeNull();
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

// The suite runs in America/New_York (vitest.config.ts), where 2027's clocks
// go forward on 14 March and back on 7 November.
const landsOn = (days: number, now: Date) => {
  const d = expiryAfterDays(days, now.getTime());
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
};

describe('the pinned test time zone', () => {
  it('observes daylight saving time, so the DST cells below are not blind', () => {
    expect(new Date(2027, 0, 15).getTimezoneOffset()).not.toBe(
      new Date(2027, 6, 15).getTimezoneOffset()
    );
  });
});

describe('daysUntilDate', () => {
  it('counts calendar days, not 24-hour spans, across a clock change', () => {
    expect(daysUntilDate('2027-03-15', new Date(2027, 2, 13, 23, 30))).toBe(2);
  });
});

describe('customExpiryDays across a daylight-saving change', () => {
  it('lands on the picked date when spring-forward would carry it past midnight', () => {
    const now = new Date(2027, 2, 13, 23, 30); // 23:30 EST, the night before
    // The calendar count, 7, would land at 00:30 on 21 March.
    expect(landsOn(7, now)).toBe('2027-03-21');
    expect(customExpiryDays('2027-03-20', now)).toBe(6);
    expect(landsOn(6, now)).toBe('2027-03-20');
  });

  it('lands on the picked date when fall-back would leave it short of midnight', () => {
    const now = new Date(2027, 10, 6, 0, 30); // 00:30 EDT, the day before
    // The calendar count, 4, would land at 23:30 on 9 November.
    expect(landsOn(4, now)).toBe('2027-11-09');
    expect(customExpiryDays('2027-11-10', now)).toBe(5);
    expect(landsOn(5, now)).toBe('2027-11-10');
  });

  it('keeps the calendar count away from a clock change', () => {
    expect(customExpiryDays('2026-12-01', LOCAL_NOW)).toBe(11);
  });

  it('keeps a key alive through the spring-forward day when no count lands on it', () => {
    // From 23:30 EST no whole-day count reaches the 23-hour 14 March: one
    // day lands at 00:30 on the 15th, which outlives the picked date.
    const now = new Date(2027, 2, 13, 23, 30);
    expect(customExpiryDays('2027-03-14', now)).toBe(1);
    expect(landsOn(1, now)).toBe('2027-03-15');
    const earlier = new Date(2027, 0, 1, 23, 30);
    const days = customExpiryDays('2027-03-14', earlier)!;
    expect(landsOn(days - 1, earlier)).toBe('2027-03-13');
    expect(landsOn(days, earlier)).toBe('2027-03-15');
  });

  it('never passes the maximum at the last date before a fall-back', () => {
    // 2027-11-06 00:30 EDT; the last date, 2030-11-06, is EST (clocks go
    // back on 3 November 2030), so 1096 × 24 h lands at 23:30 on the 5th and
    // one more day would pass the maximum: it stays 1096, an hour short.
    const now = new Date(2027, 10, 6, 0, 30);
    const { max } = customExpiryBounds(now);
    expect(max).toBe('2030-11-06');
    expect(customExpiryDays(max, now)).toBe(API_KEY_MAX_TTL_DAYS);
    expect(landsOn(API_KEY_MAX_TTL_DAYS, now)).toBe('2030-11-05');
  });

  it('lands every other date of a year on the picked date at any hour', () => {
    for (const hour of [0, 1, 12, 23]) {
      const now = new Date(2027, 0, 1, hour, 30);
      for (let n = 1; n < 366; n++) {
        const pick = landsOn(0, new Date(2027, 0, 1 + n, 12));
        if (pick === '2027-03-14' && hour === 23) continue; // the cell above
        const days = customExpiryDays(pick, now);
        expect(days, `${pick} from ${now.toString()}`).not.toBeNull();
        expect(landsOn(days!, now), `${pick} from ${now.toString()}`).toBe(pick);
      }
    }
  });
});

describe('expiryAfterDays', () => {
  it('is exactly that many 24-hour days after now', () => {
    expect(expiryAfterDays(1096, NOW).getTime()).toBe(NOW + 1096 * 24 * HOUR);
  });
});
