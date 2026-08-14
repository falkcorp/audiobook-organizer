// file: web/src/components/audiobooks/fieldRangeSelect.test.ts
// version: 1.0.0
// guid: 3d9b7e52-1c48-4f06-a2e7-8b5d0c3f6a29
// last-edited: 2026-08-14

import { describe, it, expect } from 'vitest';
import { applyFieldClick } from './fieldRangeSelect';

const VISIBLE = ['title', 'author', 'year', 'series', 'narrator'];

describe('applyFieldClick', () => {
  it('plain click toggles and sets the anchor', () => {
    const r1 = applyFieldClick(new Set(), 'author', false, null, VISIBLE);
    expect([...r1.next]).toEqual(['author']);
    expect(r1.anchor).toBe('author');
    const r2 = applyFieldClick(r1.next, 'author', false, r1.anchor, VISIBLE);
    expect(r2.next.size).toBe(0);
  });

  it('shift-click selects the inclusive range in either direction', () => {
    const r1 = applyFieldClick(new Set(), 'author', false, null, VISIBLE);
    const r2 = applyFieldClick(r1.next, 'series', true, r1.anchor, VISIBLE);
    expect([...r2.next].sort()).toEqual(['author', 'series', 'year']);
    // Reverse direction from the new anchor
    const r3 = applyFieldClick(new Set(), 'series', false, null, VISIBLE);
    const r4 = applyFieldClick(r3.next, 'title', true, r3.anchor, VISIBLE);
    expect([...r4.next].sort()).toEqual(['author', 'series', 'title', 'year']);
  });

  it('shift-click never deselects already-selected fields in the range', () => {
    const pre = new Set(['year']);
    const r1 = applyFieldClick(pre, 'title', false, null, VISIBLE);
    const r2 = applyFieldClick(r1.next, 'author', true, r1.anchor, VISIBLE);
    expect(r2.next.has('year')).toBe(true); // untouched outside range
    expect(r2.next.has('title')).toBe(true);
    expect(r2.next.has('author')).toBe(true);
  });

  it('shift-click with no anchor falls back to a plain toggle', () => {
    const r = applyFieldClick(new Set(), 'year', true, null, VISIBLE);
    expect([...r.next]).toEqual(['year']);
  });
});
