// file: web/src/utils/coverUrl.test.ts
// version: 1.0.0
// guid: 8e2d5f14-3a7b-4c90-b1e6-7f0a9c4d2b58
// last-edited: 2026-09-13

import { describe, expect, it } from 'vitest';
import { coverFullSizeUrl } from './coverUrl';

describe('coverFullSizeUrl', () => {
  it.each([
    ['https://m.media-amazon.com/images/I/51abc._SL500_.jpg', 'https://m.media-amazon.com/images/I/51abc.jpg'],
    ['https://m.media-amazon.com/images/I/51abc._SX342_.jpg', 'https://m.media-amazon.com/images/I/51abc.jpg'],
    ['https://m.media-amazon.com/images/I/51abc._SL500_AC_.png', 'https://m.media-amazon.com/images/I/51abc.png'],
    [
      'https://images-na.ssl-images-amazon.com/images/I/51abc._SL128_.jpg',
      'https://images-na.ssl-images-amazon.com/images/I/51abc.jpg',
    ],
  ])('strips the Amazon size directive from %s', (input, want) => {
    expect(coverFullSizeUrl(input)).toBe(want);
  });

  it.each([
    'https://m.media-amazon.com/images/I/51abc.jpg',
    'https://books.google.com/books/content?id=x&printsec=frontcover&img=1&zoom=1',
    'https://covers.openlibrary.org/b/id/123-L.jpg',
    '/api/v1/covers/local/book-1',
    // Not an Amazon host: a `._SL500_.` lookalike elsewhere is left alone.
    'https://example.invalid/images/51abc._SL500_.jpg',
  ])('leaves %s unchanged', (input) => {
    expect(coverFullSizeUrl(input)).toBe(input);
  });
});
