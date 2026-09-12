// file: web/src/hooks/useLibraryFilters.test.ts
// version: 1.1.0
// guid: 6b2d9f14-3a7c-4e80-9c51-2f8e0d413a6b
// last-edited: 2026-09-11

import { describe, it, expect, vi } from 'vitest';
import { renderHook } from '@testing-library/react';
import { shallowEqualFilters, useLibraryFilters } from './useLibraryFilters';
import type { FilterOptions } from '../types';

// useLibraryFilters fires several fetches on mount (tags/facets/authors/
// series) unrelated to URL parsing; stub them so the hook mounts cleanly.
vi.mock('../services/api', () => ({
  listAllUserTags: vi.fn().mockResolvedValue([]),
  getBookFacets: vi.fn().mockResolvedValue({ genres: [], languages: [] }),
  getAuthors: vi.fn().mockResolvedValue([]),
  getSeries: vi.fn().mockResolvedValue([]),
}));

// Regression coverage for the "later page bounces back to page 1" bug. The
// filters-sync effect rebuilds `filters` on every searchParams change (page
// navigation included); returning a new object reference each time re-triggered
// the page-reset effect. shallowEqualFilters lets the effect keep a stable
// reference when nothing actually changed, so page navigation no longer churns
// `filters` and no longer bounces the user to page 1.
describe('shallowEqualFilters', () => {
  it('treats value-identical filters as equal (stable reference path)', () => {
    const a: FilterOptions = { author: 'Sanderson', libraryState: 'active' };
    const b: FilterOptions = { author: 'Sanderson', libraryState: 'active' };
    expect(shallowEqualFilters(a, b)).toBe(true);
  });

  it('detects a changed filter value', () => {
    const a: FilterOptions = { author: 'Sanderson' };
    const b: FilterOptions = { author: 'Tolkien' };
    expect(shallowEqualFilters(a, b)).toBe(false);
  });

  it('detects an added/removed key', () => {
    const a: FilterOptions = { author: 'Sanderson' };
    const b: FilterOptions = { author: 'Sanderson', genre: 'Fantasy' };
    expect(shallowEqualFilters(a, b)).toBe(false);
  });

  it('treats undefined vs missing key as equal', () => {
    const a: FilterOptions = { author: 'Sanderson', genre: undefined };
    const b: FilterOptions = { author: 'Sanderson' };
    expect(shallowEqualFilters(a, b)).toBe(true);
  });

  it('preserves a tags array by reference (spread keeps the same ref → equal)', () => {
    const tags = ['fantasy', 'scifi'];
    const a: FilterOptions = { tags };
    const b: FilterOptions = { tags }; // same reference, as the ...prev spread guarantees
    expect(shallowEqualFilters(a, b)).toBe(true);
  });

  it('two arrays with equal contents but different references are NOT equal', () => {
    // Documents the reference-compare semantics: correctness relies on the
    // sync effect preserving prev.tags via `...prev`, not on deep array equality.
    const a: FilterOptions = { tags: ['fantasy'] };
    const b: FilterOptions = { tags: ['fantasy'] };
    expect(shallowEqualFilters(a, b)).toBe(false);
  });
});

// TASK-169: the "other versions" link (BookDetailVersionGroup.tsx) targets
// `/library?filters=[...]&is_primary_version=false`. These pin the URL-side
// half of that contract — that useLibraryFilters actually reads both params
// back out — since a link whose target never parses its own query string
// would pass a component test asserting the href and still show the whole
// (or wrong) library once clicked.
describe('useLibraryFilters version_group_id / is_primary_version', () => {
  it('parses version_group_id and an explicit is_primary_version=false from the URL', () => {
    const searchParams = new URLSearchParams('version_group_id=vg-1&is_primary_version=false');
    const { result } = renderHook(() => useLibraryFilters({ searchParams }));
    expect(result.current.filters.versionGroupId).toBe('vg-1');
    expect(result.current.filters.isPrimaryVersion).toBe(false);
  });

  it('leaves both undefined (preserving the primary-only default) when absent from the URL', () => {
    const searchParams = new URLSearchParams();
    const { result } = renderHook(() => useLibraryFilters({ searchParams }));
    expect(result.current.filters.versionGroupId).toBeUndefined();
    expect(result.current.filters.isPrimaryVersion).toBeUndefined();
  });
});
