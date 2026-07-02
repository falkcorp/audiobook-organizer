// file: web/src/hooks/useLibraryFilters.test.ts
// version: 1.0.0
// guid: 6b2d9f14-3a7c-4e80-9c51-2f8e0d413a6b
// last-edited: 2026-07-02

import { describe, it, expect } from 'vitest';
import { shallowEqualFilters } from './useLibraryFilters';
import type { FilterOptions } from '../types';

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
