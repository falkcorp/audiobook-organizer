// file: web/src/utils/namingPatternPreview.test.ts
// version: 1.0.0
// guid: db7d305a-aade-47b0-908a-3431d55562a2
// last-edited: 2026-09-12

import { describe, expect, it } from 'vitest';
import {
  NAMING_PATTERN_HELP_TEXT,
  PatternExampleData,
  editionSuffix,
  generatePatternExample,
} from './namingPatternPreview';

const book: PatternExampleData = {
  title: 'Dune',
  author: 'Frank Herbert',
  narrator: 'Scott Brick',
  series: '',
  series_number: '',
  print_year: 1965,
  audiobook_release_year: 2006,
  year: 1965,
  publisher: 'Macmillan Audio',
  edition: 'Unabridged',
  language: 'English',
  isbn13: '9781427201430',
  isbn10: '1427201439',
  track_number: 3,
  total_tracks: 12,
};

describe('namingPatternPreview', () => {
  it('lists {edition_suffix} among the available tokens', () => {
    expect(NAMING_PATTERN_HELP_TEXT).toContain('{edition_suffix}');
    expect(NAMING_PATTERN_HELP_TEXT).toContain('{edition},');
  });

  it('renders {edition_suffix} as " (Edition)" when the book has an edition', () => {
    expect(generatePatternExample('{title}{edition_suffix}', book)).toBe('Dune (Unabridged).m4b');
  });

  it('collapses {edition_suffix} to nothing when the book has no edition', () => {
    const noEdition = { ...book, edition: '' };
    expect(generatePatternExample('{title}{edition_suffix}', noEdition)).toBe('Dune.m4b');
    expect(
      generatePatternExample('{author}/{title}{edition_suffix} ({print_year})', noEdition, true)
    ).toBe('Frank Herbert/Dune (1965)/');
  });

  it('treats a whitespace-only edition as absent', () => {
    expect(editionSuffix('   ')).toBe('');
    expect(editionSuffix(' Abridged ')).toBe(' (Abridged)');
  });

  it('leaves existing patterns that do not use the token unchanged', () => {
    expect(generatePatternExample('{title} - {author} - read by {narrator}', book)).toBe(
      'Dune - Frank Herbert - read by Scott Brick.m4b'
    );
    expect(generatePatternExample('{author}/{series}/{title} ({print_year})', book, true)).toBe(
      'Frank Herbert/Dune (1965)/'
    );
    expect(generatePatternExample('{title} ({edition})', book)).toBe('Dune (Unabridged).m4b');
  });
});
