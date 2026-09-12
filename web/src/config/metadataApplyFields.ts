// file: web/src/config/metadataApplyFields.ts
// version: 1.0.0
// guid: 88286cbd-829f-4bd4-9f7c-801abfd4182a
// last-edited: 2026-09-12

import type { MetadataCandidate } from '../services/api';

/**
 * The fields a metadata apply can write, in display order: the ONE web-side
 * list both apply dialogs (MetadataSearchDialog, BulkMetadataSearchDialog)
 * render as checkboxes and send as the `fields` allowlist.
 *
 * Each key is the MetadataCandidate property holding the value AND the name
 * the server's allowlist knows it by. The server's list is `ApplyFields` in
 * internal/metafetch/apply_fields.go, and TestApplyFieldsMatchWebList fails
 * when the two differ -- the dialogs used to keep two copies of an 11-field
 * list the server only partly understood ("series_position" was ignored, and
 * genre/ASIN/subtitle/runtime could not be deselected at all).
 */
export const METADATA_APPLY_FIELDS = [
  'title',
  'author',
  'narrator',
  'series',
  'series_position',
  'series_secondary',
  'year',
  'publisher',
  'isbn',
  'asin',
  'cover_url',
  'description',
  'language',
  'genre',
  'subtitle',
  'abridged',
  'page_count',
  'duration_sec',
] as const;

export type MetadataApplyField = (typeof METADATA_APPLY_FIELDS)[number];

export const METADATA_APPLY_FIELD_LABELS: Record<MetadataApplyField, string> = {
  title: 'Title',
  author: 'Author',
  narrator: 'Narrator',
  series: 'Series',
  series_position: 'Series Position',
  series_secondary: 'Secondary Series',
  year: 'Year',
  publisher: 'Publisher',
  isbn: 'ISBN',
  asin: 'ASIN',
  cover_url: 'Cover Image',
  description: 'Description',
  language: 'Language',
  genre: 'Genre',
  subtitle: 'Subtitle',
  abridged: 'Abridged',
  page_count: 'Page Count',
  duration_sec: 'Runtime',
};

/**
 * The display value of one apply field on a candidate, or undefined when the
 * candidate has nothing for it (the dialogs hide those checkboxes). "isbn"
 * covers all three ISBN members, matching the server, so a candidate that only
 * carries isbn13 still offers the checkbox.
 */
export function candidateApplyFieldValue(
  candidate: MetadataCandidate,
  field: MetadataApplyField
): string | undefined {
  let v: unknown;
  switch (field) {
    case 'isbn':
      v = candidate.isbn || candidate.isbn13 || candidate.isbn10;
      break;
    case 'series_secondary':
      v = candidate.series_secondary
        ? `${candidate.series_secondary}${
            candidate.series_secondary_position ? ` #${candidate.series_secondary_position}` : ''
          }`
        : undefined;
      break;
    case 'abridged':
      v = candidate.abridged === undefined || candidate.abridged === null
        ? undefined
        : candidate.abridged
          ? 'Yes'
          : 'No';
      break;
    case 'duration_sec':
      v = candidate.duration_sec ? `${Math.round(candidate.duration_sec / 60)} min` : undefined;
      break;
    default:
      v = candidate[field];
  }
  if (v === undefined || v === null || v === '' || v === 0) return undefined;
  return String(v);
}
