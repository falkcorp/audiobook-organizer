// file: web/src/config/metadataApplyFields.test.ts
// version: 1.0.0
// guid: 729b2f19-ee65-421a-a389-2eaacdb3ecc6
// last-edited: 2026-09-12

import { describe, expect, it } from 'vitest';
import type { MetadataCandidate } from '../services/api';
import {
  METADATA_APPLY_FIELDS,
  METADATA_APPLY_FIELD_LABELS,
  candidateApplyFieldValue,
} from './metadataApplyFields';

const base: MetadataCandidate = { title: 'T', author: 'A', source: 'Audible', score: 1 };

describe('metadataApplyFields', () => {
  it('labels every apply field', () => {
    for (const f of METADATA_APPLY_FIELDS) {
      expect(METADATA_APPLY_FIELD_LABELS[f]).toBeTruthy();
    }
  });

  it('offers the fields the server used to write without a checkbox', () => {
    // These were written by every apply but could not be deselected.
    for (const f of ['series_position', 'asin', 'genre', 'subtitle', 'abridged', 'page_count', 'duration_sec']) {
      expect(METADATA_APPLY_FIELDS).toContain(f);
    }
  });

  it('hides a field the candidate has no value for', () => {
    expect(candidateApplyFieldValue(base, 'genre')).toBeUndefined();
    expect(candidateApplyFieldValue(base, 'abridged')).toBeUndefined();
    expect(candidateApplyFieldValue(base, 'duration_sec')).toBeUndefined();
  });

  it('shows isbn when only isbn13 is set, like the server treats it', () => {
    expect(candidateApplyFieldValue({ ...base, isbn13: '9780000000002' }, 'isbn')).toBe('9780000000002');
  });

  it('renders abridged=false as a real value, not a missing one', () => {
    expect(candidateApplyFieldValue({ ...base, abridged: false }, 'abridged')).toBe('No');
  });

  it('renders runtime in minutes', () => {
    expect(candidateApplyFieldValue({ ...base, duration_sec: 3600 }, 'duration_sec')).toBe('60 min');
  });
});
