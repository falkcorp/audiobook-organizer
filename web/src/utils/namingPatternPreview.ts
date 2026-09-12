// file: web/src/utils/namingPatternPreview.ts
// version: 1.0.0
// guid: d5ce6991-63b7-426f-af98-ad1270c6f0c9
// last-edited: 2026-09-12

// Client-side preview of the folder/file naming patterns shown on the Settings
// page. It is an approximation for the operator's benefit only -- the real
// target path is computed by internal/organizer/pathbuild.go (BuildPath), which
// also drops empty " - " segments and connector words. Keep the token names in
// step with that file's replacement map.

export interface PatternExampleData {
  title: string;
  author: string;
  narrator: string;
  series: string;
  series_number: string;
  print_year: number;
  audiobook_release_year: number;
  year: number;
  publisher: string;
  edition: string;
  language: string;
  isbn13: string;
  isbn10: string;
  track_number: number;
  total_tracks: number;
  bitrate?: string;
  codec?: string;
  quality?: string;
}

// Help text listing the placeholders a naming pattern may use.
export const NAMING_PATTERN_HELP_TEXT =
  'Available: {title}, {author}, {series}, {series_number}, ' +
  '{print_year}, {audiobook_release_year}, {year}, ' +
  '{publisher}, {edition}, {edition_suffix}, {narrator}, {language}, ' +
  '{isbn10}, {isbn13}, {track_number}, {total_tracks}. ' +
  '{edition_suffix} renders " (Edition)", or nothing when a book has no edition.';

// {edition_suffix} carries its own leading space and parens, mirroring the
// backend: " (Unabridged)" when there is an edition, "" when there is not.
export function editionSuffix(edition: string): string {
  const trimmed = edition.trim();
  return trimmed === '' ? '' : ` (${trimmed})`;
}

export function generatePatternExample(
  pattern: string,
  exampleData: PatternExampleData,
  isFolder: boolean = false
): string {
  let result = pattern;
  const replacements: Record<string, string> = {
    '{title}': exampleData.title,
    '{author}': exampleData.author,
    '{narrator}': exampleData.narrator,
    '{series}': exampleData.series || '',
    '{series_number}': exampleData.series_number || '',
    '{print_year}': exampleData.print_year.toString(),
    '{audiobook_release_year}': exampleData.audiobook_release_year.toString(),
    '{year}': exampleData.year.toString(),
    '{publisher}': exampleData.publisher,
    '{edition}': exampleData.edition,
    '{edition_suffix}': editionSuffix(exampleData.edition),
    '{language}': exampleData.language,
    '{isbn13}': exampleData.isbn13,
    '{isbn10}': exampleData.isbn10,
    '{track_number}': exampleData.track_number.toString().padStart(2, '0'),
    '{total_tracks}': exampleData.total_tracks.toString(),
    '{bitrate}': exampleData.bitrate || '',
    '{codec}': exampleData.codec || '',
    '{quality}': exampleData.quality || '',
  };

  Object.entries(replacements).forEach(([key, value]) => {
    result = result.split(key).join(value);
  });

  if (isFolder) {
    result = result
      .split('/')
      .filter((segment) => segment.trim() !== '')
      .join('/');
    return result + '/';
  }

  return result + '.m4b';
}
