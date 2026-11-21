// file: web/src/components/audiobooks/editOverrides.test.ts
// version: 1.0.0
// guid: 0b9d3f62-5a17-4c8e-b3d4-6e2a91c7f850
// last-edited: 2026-11-20

import { describe, it, expect } from 'vitest';
import { buildOverrides, FIELD_TO_API } from './editOverrides';
import type { Audiobook } from '../../types';

const book = {
  id: 'b1',
  title: 'The Long Halloween',
  author: 'Bruce Wayne',
  series: 'Year One',
  series_number: 2,
  year: 1996,
} as unknown as Audiobook;

describe('buildOverrides', () => {
  it('sends a lock-only override for a toggled field that was not edited', () => {
    const out = buildOverrides(book, new Set(), { title: true });
    expect(out).toEqual({ title: { locked: true } });
    expect('value' in out.title).toBe(false);
  });

  it('sends a lock-only unlock', () => {
    expect(buildOverrides(book, new Set(), { author: false })).toEqual({
      author_name: { locked: false },
    });
  });

  it('sends locked: false with the value for an edited, explicitly unlocked field', () => {
    const edited = { ...book, title: 'Dark Victory' } as Audiobook;
    expect(buildOverrides(edited, new Set(['title']), { title: false })).toEqual({
      title: { value: 'Dark Victory', locked: false },
    });
  });

  it('locks an edited field by default', () => {
    const edited = { ...book, title: 'Dark Victory' } as Audiobook;
    expect(buildOverrides(edited, new Set(['title']), {})).toEqual({
      title: { value: 'Dark Victory', locked: true },
    });
  });

  it('ignores a field with no API mapping', () => {
    expect(buildOverrides(book, new Set(['track_number']), { disk_number: true })).toEqual({});
  });

  it('keeps the year and series number value mapping', () => {
    const edited = { ...book, year: 2001, series_number: undefined } as unknown as Audiobook;
    expect(buildOverrides(edited, new Set(['year', 'series_number']), {})).toEqual({
      audiobook_release_year: { value: 2001, locked: true },
      series_position: { value: null, locked: true },
    });
  });

  it('maps every lockable form field to a distinct API key', () => {
    const keys = Object.values(FIELD_TO_API);
    expect(new Set(keys).size).toBe(keys.length);
    expect(Object.keys(FIELD_TO_API)).toEqual(
      expect.arrayContaining(['title', 'author', 'series', 'series_number', 'year', 'description'])
    );
  });
});
