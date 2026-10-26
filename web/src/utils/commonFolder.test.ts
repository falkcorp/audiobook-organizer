// file: web/src/utils/commonFolder.test.ts
// version: 1.0.0
// guid: 6b0d4e83-2c75-4a19-9e38-d7f1a5c2b046
// last-edited: 2026-10-26

import { describe, expect, it } from 'vitest';
import { commonFolder } from './commonFolder';

describe('commonFolder', () => {
  it('is the folder of a single file', () => {
    expect(commonFolder(['/lib/Author/Book/part1.mp3'])).toBe('/lib/Author/Book');
  });
  it('is the shared folder of several files', () => {
    expect(
      commonFolder([
        '/lib/Author/Book/CD1/a.mp3',
        '/lib/Author/Book/CD2/b.mp3',
        '/lib/Author/Book/c.mp3',
      ])
    ).toBe('/lib/Author/Book');
  });
  it('compares whole segments, not characters', () => {
    expect(commonFolder(['/lib/Book/a.mp3', '/lib/Book2/b.mp3'])).toBe('/lib');
  });
  it('keeps the root when files only share it, and is empty for none', () => {
    expect(commonFolder(['/a/x.mp3', '/b/y.mp3'])).toBe('/');
    expect(commonFolder([])).toBe('');
  });
  it('handles backslash paths', () => {
    expect(commonFolder(['C:\\lib\\Book\\a.mp3', 'C:\\lib\\Book\\b.mp3'])).toBe('C:\\lib\\Book');
  });
});
