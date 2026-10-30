// file: web/src/utils/namingPatternPreview.ts
// version: 1.1.0
// guid: d5ce6991-63b7-426f-af98-ad1270c6f0c9
// last-edited: 2026-10-30

// Client-side preview of the folder/file naming patterns shown on the Settings
// page. It is an approximation for the operator's benefit only -- the real
// target path is computed by internal/organizer/pathbuild.go (BuildPath), which
// also drops empty " - " segments and connector words.
//
// The backend is the single source of truth for which tokens exist: GET /config
// serves them as `naming_pattern_tokens` (organizer.NamingPatternTokens, read off
// the resolver's own map) and the Settings page renders its help text and unknown
// token warning from that. FALLBACK_NAMING_PATTERN_TOKENS below is only what shows
// while that response is loading or if it is missing; it must list tokens the
// backend resolves and nothing else.

export interface PatternExampleData {
  title: string;
  author: string;
  narrator: string;
  series: string;
  series_number: string;
  print_year: number;
  year: number;
  publisher: string;
  edition: string;
  language: string;
  isbn13: string;
  isbn10: string;
  track: number;
  total_tracks: number;
  bitrate?: string;
  codec?: string;
  quality?: string;
}

// Shown only until GET /config answers (or if it carries no list). Mirrors
// organizer.NamingPatternTokens at the time of writing.
export const FALLBACK_NAMING_PATTERN_TOKENS: readonly string[] = [
  '{author}',
  '{bitrate}',
  '{codec}',
  '{edition}',
  '{edition_suffix}',
  '{ext}',
  '{isbn10}',
  '{isbn13}',
  '{lang}',
  '{language}',
  '{narrator}',
  '{print_year}',
  '{publisher}',
  '{quality}',
  '{series}',
  '{series_number}',
  '{series_position}',
  '{series_prefix}',
  '{title}',
  '{total_tracks}',
  '{track}',
  '{track_title}',
  '{year}',
];

// Help text listing the placeholders a naming pattern may use.
export function namingPatternHelpText(tokens: readonly string[]): string {
  return (
    `Available: ${tokens.join(', ')}. ` +
    '{edition_suffix} renders " (Edition)", or nothing when a book has no edition; ' +
    '{series_prefix} renders "Series N - ", or nothing when a book has no series. ' +
    'Pad numbers with a spec such as {track:02d}.'
  );
}

export const NAMING_PATTERN_HELP_TEXT = namingPatternHelpText(FALLBACK_NAMING_PATTERN_TOKENS);

// Matches "{name}" and the "{name:02d}" format-spec form; the backend
// lowercases names, so matching is case-insensitive too.
const PATTERN_TOKEN_REGEX = /\{([A-Za-z_]+)(?::[^}]*)?\}/g;

// Returns the distinct "{token}" strings in `pattern` that are not in `tokens`.
// BuildPath fails such a pattern with an unresolved-placeholder error, so the
// Settings page warns before the operator saves one.
export function unknownPatternTokens(pattern: string, tokens: readonly string[]): string[] {
  const known = new Set(tokens.map((t) => t.toLowerCase()));
  const unknown: string[] = [];
  for (const match of pattern.matchAll(PATTERN_TOKEN_REGEX)) {
    const token = `{${match[1].toLowerCase()}}`;
    if (!known.has(token) && !unknown.includes(token)) {
      unknown.push(token);
    }
  }
  return unknown;
}

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
  // {track:02d}-style specs, as the backend accepts for the two numeric tokens.
  let result = pattern.replace(
    /\{(track|total_tracks):0?(\d*)d\}/gi,
    (_match, name: string, width: string) => {
      const value = name.toLowerCase() === 'track' ? exampleData.track : exampleData.total_tracks;
      return value.toString().padStart(Number(width || 0), '0');
    }
  );
  const replacements: Record<string, string> = {
    '{title}': exampleData.title,
    '{author}': exampleData.author,
    '{narrator}': exampleData.narrator,
    '{series}': exampleData.series || '',
    '{series_number}': exampleData.series_number || '',
    '{print_year}': exampleData.print_year.toString(),
    '{year}': exampleData.year.toString(),
    '{publisher}': exampleData.publisher,
    '{edition}': exampleData.edition,
    '{edition_suffix}': editionSuffix(exampleData.edition),
    '{language}': exampleData.language,
    '{isbn13}': exampleData.isbn13,
    '{isbn10}': exampleData.isbn10,
    '{track}': exampleData.track.toString(),
    '{series_position}': exampleData.series_number || '',
    '{series_prefix}': exampleData.series
      ? `${exampleData.series}${exampleData.series_number ? ` ${exampleData.series_number}` : ''} - `
      : '',
    '{lang}': exampleData.language,
    '{track_title}': `${exampleData.title} ${exampleData.track}`,
    '{ext}': 'm4b',
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
