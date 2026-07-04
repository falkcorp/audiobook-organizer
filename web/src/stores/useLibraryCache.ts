// file: web/src/stores/useLibraryCache.ts
// version: 1.1.0
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890
// last-edited: 2026-07-03

import { create } from 'zustand';
import type { Audiobook } from '../types';
import type { ImportPath } from '../pages/libraryTypes';

interface LibraryCacheEntry {
  audiobooks: Audiobook[];
  totalCount: number;
  totalPages: number;
  importPaths: ImportPath[];
  timestamp: number;
}

interface LibraryStore {
  cache: Map<string, LibraryCacheEntry>;
  getCached: (key: string, maxAgeMs?: number) => LibraryCacheEntry | null;
  setCached: (key: string, entry: Omit<LibraryCacheEntry, 'timestamp'>) => void;
  clear: () => void;
}

const CACHE_TTL_MS = 1 * 60 * 1000; // 1 minute cache

// Hard cap on the number of cached pages. Without this the cache grows
// unbounded as the user paginates/searches/filters (each combination is a
// distinct key), retaining up to ~1000 books per entry indefinitely since
// eviction previously only happened lazily on read of that exact key.
const MAX_ENTRIES = 50;

/** Removes all TTL-expired entries from the map (mutates and returns it). */
function sweepExpired(cache: Map<string, LibraryCacheEntry>, maxAgeMs: number): void {
  const now = Date.now();
  for (const [key, entry] of cache) {
    if (now - entry.timestamp > maxAgeMs) {
      cache.delete(key);
    }
  }
}

/** Evicts the oldest (by timestamp) entries until the map is under the cap. */
function evictOldestUntilUnderCap(cache: Map<string, LibraryCacheEntry>, maxEntries: number): void {
  while (cache.size >= maxEntries) {
    let oldestKey: string | undefined;
    let oldestTimestamp = Infinity;
    for (const [key, entry] of cache) {
      if (entry.timestamp < oldestTimestamp) {
        oldestTimestamp = entry.timestamp;
        oldestKey = key;
      }
    }
    if (oldestKey === undefined) break;
    cache.delete(oldestKey);
  }
}

export const useLibraryCache = create<LibraryStore>((set, get) => ({
  cache: new Map(),

  getCached: (key: string, maxAgeMs = CACHE_TTL_MS) => {
    const entry = get().cache.get(key);
    if (!entry) return null;

    const age = Date.now() - entry.timestamp;
    if (age > maxAgeMs) {
      get().cache.delete(key);
      return null;
    }

    return entry;
  },

  setCached: (key: string, entry) => {
    set((state) => {
      const newCache = new Map(state.cache);
      sweepExpired(newCache, CACHE_TTL_MS);
      if (!newCache.has(key)) {
        evictOldestUntilUnderCap(newCache, MAX_ENTRIES);
      }
      newCache.set(key, { ...entry, timestamp: Date.now() });
      return { cache: newCache };
    });
  },

  clear: () => {
    set({ cache: new Map() });
  },
}));

export const buildCacheKey = (
  page: number,
  itemsPerPage: number,
  searchQuery: string,
  filters: string,
  sortBy: string,
  sortOrder: string
): string => {
  return `${page}:${itemsPerPage}:${searchQuery}:${filters}:${sortBy}:${sortOrder}`;
};
