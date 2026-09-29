// file: web/src/utils/titleSortKey.test.ts
// version: 1.3.0
// guid: 4b9e1c62-7d3a-4f85-b2c0-6a8d3e5f1b97
// last-edited: 2026-09-29

import { describe, expect, it } from 'vitest';
import { numberedBookSortForm, titleSortKey } from './titleSortKey';

// Same table as internal/util/title_sort_test.go: the browser and the server
// must order titles identically.
describe('titleSortKey', () => {
  it('files every spelling of a numbered book under its name', () => {
    for (const t of [
      'I Corinthians',
      '1 Corinthians',
      'First Corinthians',
      'l Corinthians',
      '1st Corinthians',
      'i corinthians',
      'I. Corinthians',
    ]) {
      expect(titleSortKey(t)).toBe('corinthians 1');
    }
    for (const t of [
      'II Corinthians',
      '2 Corinthians',
      'Second Corinthians',
      'll Corinthians',
      'lI Corinthians',
    ]) {
      expect(titleSortKey(t)).toBe('corinthians 2');
    }
  });

  it.each([
    ['I Corinthians 13', 'corinthians 1 13'],
    ['II Kings - Chapter 3', 'kings 2 - chapter 3'],
    ['III John', 'john 3'],
    ['First Samuel chapter 4', 'samuel 1 chapter 4'],
    ['IV Maccabees', 'maccabees 4'],
    ['The Odyssey', 'the odyssey'],
    ['I, Robot', 'i, robot'],
    ['V for Vendetta', 'v for vendetta'],
    ["l'Étranger", "l'étranger"],
    ['First Kings of England', 'first kings of england'],
    ['I John Smith', 'i john smith'],
    ['1984', '1984'],
    ['  Dune  ', 'dune'],
    ['Corinthians', 'corinthians'],
    ['Fifth Corinthians', 'fifth corinthians'],
    ['lll', 'lll'],
    ['1. John', 'john 1'],
    ['2 - Kings', 'kings 2'],
    ['2-Peter', 'peter 2'],
    ['1 John: Commentary', 'john 1: commentary'],
    ["2 Peter's Journey", "2 peter's journey"],
    ['1. Mose', 'mose 1'],
    ['2. Mose', 'mose 2'],
    ['5. Mose', 'mose 5'],
    ['V Mose', 'mose 5'],
    ['5 John', '5 john'],
    // NBSP is not whitespace to the server's regex: no rewrite either side
    ['1\u00a0John', '1\u00a0john'],
  ])('%s -> %s', (input, want) => {
    expect(titleSortKey(input)).toBe(want);
  });

  it('treats a missing title as empty', () => {
    expect(titleSortKey(undefined)).toBe('');
    expect(titleSortKey(null)).toBe('');
  });

  it('keeps the book name as written in the sort form', () => {
    expect(numberedBookSortForm('l Corinthians')).toBe('Corinthians 1');
    expect(numberedBookSortForm('V for Vendetta')).toBeNull();
  });

  it('sorts a list the way the library does', () => {
    const titles = [
      'l Corinthians',
      'Daniel',
      'II Corinthians',
      'Colossians',
      'Isaiah',
      'First Corinthians',
    ];
    const sorted = [...titles].sort((a, b) => titleSortKey(a).localeCompare(titleSortKey(b)));
    expect(sorted.slice(0, 1)).toEqual(['Colossians']);
    expect(sorted.slice(3)).toEqual(['II Corinthians', 'Daniel', 'Isaiah']);
  });
});
