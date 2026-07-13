// file: web/src/utils/__tests__/tagDisplay.test.ts
// version: 1.0.0
// guid: dfcc4114-190c-4ee9-9383-8a5f614ef889
// last-edited: 2026-07-13

import { describe, it, expect } from 'vitest';
import {
  formatTagLabel,
  isSourceTag,
  shouldRenderTagChip,
  languageNameFor,
  sourceNameFor,
} from '../tagDisplay';

describe('languageNameFor', () => {
  it('resolves an ISO 639-1 code', () => {
    expect(languageNameFor('en')).toBe('English');
  });

  it('resolves an ISO 639-2 code', () => {
    expect(languageNameFor('spa')).toBe('Spanish');
  });

  it('falls back to the uppercased code for an unknown code', () => {
    expect(languageNameFor('zzzz')).toBe('ZZZZ');
  });
});

describe('sourceNameFor', () => {
  it('title-cases a simple source name', () => {
    expect(sourceNameFor('audible')).toBe('Audible');
  });

  it('special-cases openlibrary', () => {
    expect(sourceNameFor('openlibrary')).toBe('Open Library');
  });

  it('special-cases googlebooks', () => {
    expect(sourceNameFor('googlebooks')).toBe('Google Books');
  });

  it('title-cases an unmapped source with separators', () => {
    expect(sourceNameFor('some_new-source')).toBe('Some New Source');
  });
});

describe('isSourceTag', () => {
  it('is true for metadata:source:* tags', () => {
    expect(isSourceTag('metadata:source:audible')).toBe(true);
    expect(isSourceTag('metadata:source:openlibrary')).toBe(true);
  });

  it('is false for other metadata tags and plain tags', () => {
    expect(isSourceTag('metadata:language:en')).toBe(false);
    expect(isSourceTag('metadata:other')).toBe(false);
    expect(isSourceTag('favorite')).toBe(false);
    expect(isSourceTag('metadata:source:')).toBe(false);
  });
});

describe('shouldRenderTagChip', () => {
  it('is the inverse of isSourceTag', () => {
    expect(shouldRenderTagChip('metadata:source:audible')).toBe(false);
    expect(shouldRenderTagChip('metadata:language:en')).toBe(true);
    expect(shouldRenderTagChip('favorite')).toBe(true);
  });
});

describe('formatTagLabel', () => {
  it('formats metadata:language:en as English', () => {
    expect(formatTagLabel('metadata:language:en')).toBe('English');
  });

  it('formats metadata:language:spa as Spanish', () => {
    expect(formatTagLabel('metadata:language:spa')).toBe('Spanish');
  });

  it('formats metadata:language:de as German', () => {
    expect(formatTagLabel('metadata:language:de')).toBe('German');
  });

  it('formats an unknown language code as the uppercased code', () => {
    expect(formatTagLabel('metadata:language:zzzz')).toBe('ZZZZ');
  });

  it('formats metadata:source:openlibrary as Open Library', () => {
    expect(formatTagLabel('metadata:source:openlibrary')).toBe('Open Library');
  });

  it('formats metadata:source:audible as Audible', () => {
    expect(formatTagLabel('metadata:source:audible')).toBe('Audible');
  });

  it('strips the metadata: prefix for other metadata tags', () => {
    expect(formatTagLabel('metadata:something:else')).toBe('something:else');
  });

  it('leaves a plain user tag unchanged', () => {
    expect(formatTagLabel('favorite')).toBe('favorite');
    expect(formatTagLabel('to-listen')).toBe('to-listen');
  });
});
