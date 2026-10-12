// file: web/src/services/api.dedupExportUrl.test.ts
// version: 1.1.0
// guid: 4b7e2d19-8c35-4f60-a1d8-6e92c0b35f47
// last-edited: 2026-10-12

import { describe, it, expect } from 'vitest';
import { dedupCandidatesExportUrl } from './api';

function paramsOf(url: string): URLSearchParams {
  return new URL(url, 'http://localhost').searchParams;
}

describe('dedupCandidatesExportUrl', () => {
  it('export url includes layer band status q entity_id', () => {
    const url = dedupCandidatesExportUrl(
      { layer: 'acoustid', band: 'high', status: 'pending', q: 'Book One', entity_id: 'B1', entity_type: 'book' },
      'csv'
    );
    expect(url.startsWith('/api/v1/dedup/candidates/export?')).toBe(true);
    const p = paramsOf(url);
    expect(p.get('format')).toBe('csv');
    expect(p.get('layer')).toBe('acoustid');
    expect(p.get('band')).toBe('high');
    expect(p.get('status')).toBe('pending');
    expect(p.get('q')).toBe('Book One');
    expect(p.get('entity_id')).toBe('B1');
    expect(p.get('entity_type')).toBe('book');
  });

  it('export url omits empty filters', () => {
    const url = dedupCandidatesExportUrl(
      { layer: null, band: '', status: undefined, q: '   ', entity_id: null, entity_type: '' },
      'json'
    );
    expect(url).toBe('/api/v1/dedup/candidates/export?format=json');
  });

  it('trims the search term', () => {
    expect(paramsOf(dedupCandidatesExportUrl({ q: '  Author A ' }, 'csv')).get('q')).toBe(
      'Author A'
    );
  });
});
