// file: web/src/utils/namingPatternPreview.test.ts
// version: 1.1.0
// guid: db7d305a-aade-47b0-908a-3431d55562a2
// last-edited: 2026-10-30

import { describe, expect, it } from 'vitest';
import {
  FALLBACK_NAMING_PATTERN_TOKENS,
  NAMING_PATTERN_HELP_TEXT,
  PatternExampleData,
  editionSuffix,
  generatePatternExample,
  namingPatternHelpText,
  unknownPatternTokens,
} from './namingPatternPreview';

const book: PatternExampleData = {
  title: 'Dune',
  author: 'Frank Herbert',
  narrator: 'Scott Brick',
  series: '',
  series_number: '',
  print_year: 1965,
  year: 1965,
  publisher: 'Macmillan Audio',
  edition: 'Unabridged',
  language: 'English',
  isbn13: '9781427201430',
  isbn10: '1427201439',
  track: 3,
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

  it('no longer advertises tokens the backend cannot resolve', () => {
    expect(NAMING_PATTERN_HELP_TEXT).not.toContain('{track_number}');
    expect(NAMING_PATTERN_HELP_TEXT).not.toContain('{audiobook_release_year}');
    expect(NAMING_PATTERN_HELP_TEXT).toContain('{series_prefix}');
    expect(FALLBACK_NAMING_PATTERN_TOKENS).toContain('{track}');
  });

  it('builds the help text from whatever token list it is given', () => {
    expect(namingPatternHelpText(['{title}', '{batarang}'])).toContain(
      'Available: {title}, {batarang}.'
    );
  });

  it('flags brace tokens that are not in the list, once each, case-insensitively', () => {
    const tokens = FALLBACK_NAMING_PATTERN_TOKENS;
    expect(unknownPatternTokens('{Author}/{title} {track:02d}', tokens)).toEqual([]);
    expect(
      unknownPatternTokens('{title} {track_number} {audiobook_release_year} {track_number}', tokens)
    ).toEqual(['{track_number}', '{audiobook_release_year}']);
    expect(unknownPatternTokens('no braces here', tokens)).toEqual([]);
  });

  it('previews {track}, {track:02d} and {series_prefix} like the backend', () => {
    const series: PatternExampleData = {
      ...book,
      title: 'Year One',
      series: 'Gotham Chronicles',
      series_number: '2',
    };
    expect(generatePatternExample('{series_prefix}{title} {track}', series)).toBe(
      'Gotham Chronicles 2 - Year One 3.m4b'
    );
    expect(generatePatternExample('{title} {track:02d}', series)).toBe('Year One 03.m4b');
    expect(generatePatternExample('{series_prefix}{title}', book)).toBe('Dune.m4b');
  });
});
