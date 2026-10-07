// file: web/src/utils/apiKeyExpiry.test.ts
// version: 1.0.0
// guid: 0d4f6a2b-8c1e-4b7a-9f35-6e2d8a1c4b90
// last-edited: 2026-10-07

import { describe, it, expect } from 'vitest';
import { API_KEY_EXPIRY_OPTIONS, formatKeyExpiry } from './apiKeyExpiry';

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

  it('tops out at the server maximum of 365 days', () => {
    expect(Math.max(...API_KEY_EXPIRY_OPTIONS.map((o) => o.value))).toBe(365);
  });
});
