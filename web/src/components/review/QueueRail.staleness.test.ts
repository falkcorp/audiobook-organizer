// file: web/src/components/review/QueueRail.staleness.test.ts
// version: 1.0.0
// guid: 7e3f10a4-2c68-4b91-85d7-a0c9e21f4b36
// last-edited: 2026-08-20

/**
 * The per-row stale marker.
 *
 * MetadataCacheTTL's contract is that entries past it stay readable and the UI
 * flags them. The review listing sent no age at all, so nothing could be
 * flagged: on the live library 5,771 of 5,774 reviewable rows were past the
 * TTL -- the oldest three months old -- and every one looked freshly fetched.
 */

import { describe, it, expect, vi, afterEach } from 'vitest';
import { daysSince, staleRowTitle } from './QueueRail';

afterEach(() => {
  vi.useRealTimers();
});

function freezeAt(iso: string) {
  vi.useFakeTimers();
  vi.setSystemTime(new Date(iso));
}

describe('daysSince', () => {
  it('counts whole days back from now', () => {
    freezeAt('2026-08-20T12:00:00Z');
    expect(daysSince('2026-05-15T12:00:00Z')).toBe(97);
    expect(daysSince('2026-08-19T12:00:00Z')).toBe(1);
  });

  it('returns null instead of inventing an age', () => {
    // The row already knows it is stale from is_fresh. A made-up number is
    // worse than an unspecified one.
    expect(daysSince(undefined)).toBeNull();
    expect(daysSince('not a date')).toBeNull();
  });

  it('never reports a negative age for a clock-skewed future timestamp', () => {
    freezeAt('2026-08-20T12:00:00Z');
    expect(daysSince('2026-09-01T12:00:00Z')).toBe(0);
  });
});

describe('staleRowTitle', () => {
  it('names the actual age when the row carries one', () => {
    freezeAt('2026-08-20T12:00:00Z');
    const text = staleRowTitle('2026-05-15T12:00:00Z');

    expect(text).toContain('97 days ago');
    expect(text).toMatch(/may have changed/);
    expect(text).toMatch(/[Rr]efetch/);
  });

  it('falls back to the TTL bound when there is no timestamp', () => {
    // Still true -- the marker only renders when the server called it stale --
    // and it does not fabricate a day count to say it.
    const text = staleRowTitle(undefined);

    expect(text).toContain('more than 30 days ago');
    // The TTL bound is allowed to name 30; a COMPUTED age is what must not
    // appear, so match the "Fetched <n> days ago" shape rather than any digit.
    expect(text).not.toMatch(/Fetched \d+ days ago/);
  });
});
