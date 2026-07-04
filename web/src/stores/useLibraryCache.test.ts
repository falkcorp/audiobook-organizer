// file: web/src/stores/useLibraryCache.test.ts
// version: 1.0.0
// guid: c3d4e5f6-a7b8-49c0-8d1e-2f3a4b5c6d7e
// last-edited: 2026-07-03

import { describe, it, expect, beforeEach, vi } from 'vitest';
import { useLibraryCache } from './useLibraryCache';

// Regression coverage for an unbounded-memory-growth bug: the module-level
// cache had no entry cap and only evicted lazily on read of the exact key
// that expired, so pages (up to ~1000 books / 2-4MB each) accumulated for
// the lifetime of the session as the user paginated/searched/filtered.

const makeEntry = (n: number) => ({
  audiobooks: [],
  totalCount: n,
  totalPages: 1,
  importPaths: [],
});

describe('useLibraryCache', () => {
  beforeEach(() => {
    useLibraryCache.getState().clear();
    vi.useRealTimers();
  });

  it('enforces a hard entry cap, evicting the oldest entry first', () => {
    const { setCached } = useLibraryCache.getState();
    vi.useFakeTimers();
    const base = new Date('2026-01-01T00:00:00Z').getTime();

    for (let i = 0; i < 50; i++) {
      vi.setSystemTime(base + i * 1000);
      setCached(`key-${i}`, makeEntry(i));
    }
    expect(useLibraryCache.getState().cache.size).toBe(50);

    // One more insert should evict the oldest (key-0).
    vi.setSystemTime(base + 50 * 1000);
    setCached('key-50', makeEntry(50));

    const cache = useLibraryCache.getState().cache;
    expect(cache.size).toBe(50);
    expect(cache.has('key-0')).toBe(false);
    expect(cache.has('key-1')).toBe(true);
    expect(cache.has('key-50')).toBe(true);

    vi.useRealTimers();
  });

  it('sweeps all TTL-expired entries on every write, not just the exact key', () => {
    const { setCached, getCached } = useLibraryCache.getState();
    vi.useFakeTimers();
    const base = new Date('2026-01-01T00:00:00Z').getTime();

    vi.setSystemTime(base);
    setCached('stale-a', makeEntry(1));
    setCached('stale-b', makeEntry(2));

    // Advance well past the 1-minute TTL, then write an unrelated key.
    vi.setSystemTime(base + 5 * 60 * 1000);
    setCached('fresh', makeEntry(3));

    const cache = useLibraryCache.getState().cache;
    expect(cache.has('stale-a')).toBe(false);
    expect(cache.has('stale-b')).toBe(false);
    expect(cache.has('fresh')).toBe(true);

    // Read path behavior is unchanged: fresh entry is retrievable.
    expect(getCached('fresh')?.totalCount).toBe(3);

    vi.useRealTimers();
  });

  it('leaves the read path unchanged: returns null for missing/expired, entry for fresh', () => {
    const { setCached, getCached } = useLibraryCache.getState();
    vi.useFakeTimers();
    const base = new Date('2026-01-01T00:00:00Z').getTime();

    vi.setSystemTime(base);
    setCached('a', makeEntry(42));

    expect(getCached('missing')).toBeNull();
    expect(getCached('a')?.totalCount).toBe(42);

    vi.setSystemTime(base + 5 * 60 * 1000);
    expect(getCached('a')).toBeNull();

    vi.useRealTimers();
  });

  it('updating an existing key does not count against the entry cap', () => {
    const { setCached } = useLibraryCache.getState();
    for (let i = 0; i < 50; i++) {
      setCached(`key-${i}`, makeEntry(i));
    }
    expect(useLibraryCache.getState().cache.size).toBe(50);

    // Re-setting an existing key should not evict anything else.
    setCached('key-0', makeEntry(999));
    const cache = useLibraryCache.getState().cache;
    expect(cache.size).toBe(50);
    expect(cache.get('key-0')?.totalCount).toBe(999);
  });
});
