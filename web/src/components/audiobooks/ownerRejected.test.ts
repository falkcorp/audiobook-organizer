// file: web/src/components/audiobooks/ownerRejected.test.ts
// version: 1.0.0
// guid: 4d7a1c93-5e28-4b60-9f14-2a8c6e3b7d05
// last-edited: 2026-11-20

import { describe, it, expect } from 'vitest';
import type { MetadataCandidate } from '../../services/api';
import { clearUnrejected } from './ownerRejected';

const rejected = { owner_rejected: true };

const make = (title: string, source: string, asin?: string): MetadataCandidate => ({
  title,
  author: 'Bruce Wayne',
  source,
  score: 0.9,
  asin,
  apply_check: rejected,
});

const flagged = (c: MetadataCandidate) => c.apply_check?.owner_rejected === true;

describe('clearUnrejected', () => {
  it('clears the clicked row', () => {
    const a = make('Year One', 'audible', 'B00BATMAN1');
    const out = clearUnrejected([a], a);
    expect(flagged(out[0])).toBe(false);
  });

  it('clears a same-ASIN sibling under another source', () => {
    const a = make('Year One', 'audnexus', 'B00BATMAN1');
    const b = make('Year One', 'audible', 'B00BATMAN1');
    const out = clearUnrejected([a, b], a);
    expect(out.map(flagged)).toEqual([false, false]);
  });

  it('leaves a different-ASIN row untouched', () => {
    const a = make('Year One', 'audible', 'B00BATMAN1');
    const b = make('The Long Halloween', 'audible', 'B00BATMAN2');
    const out = clearUnrejected([a, b], a);
    expect(out[1]).toBe(b);
    expect(flagged(out[1])).toBe(true);
  });

  it('does not let two rows without an ASIN clear each other', () => {
    const a = make('Hush', 'openlibrary');
    const b = make('Knightfall', 'openlibrary');
    const out = clearUnrejected([a, b], a);
    expect(flagged(out[0])).toBe(false);
    expect(out[1]).toBe(b);
  });

  it('keeps the clicked row marked when it is still rejected, but clears siblings', () => {
    const a = make('Year One', 'audnexus', 'B00BATMAN1');
    const b = make('Year One', 'audible', 'B00BATMAN1');
    const out = clearUnrejected([a, b], a, { stillRejected: true });
    expect(out[0]).toBe(a);
    expect(flagged(out[1])).toBe(false);
  });
});
