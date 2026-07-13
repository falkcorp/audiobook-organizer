// file: web/src/components/audiobooks/AudiobookCard.test.tsx
// version: 1.0.0
// guid: e1027ee2-8526-4e0c-a5ef-9e75e8a362b0
// last-edited: 2026-07-13

import { render, screen } from '@testing-library/react';
import { describe, it, expect } from 'vitest';
import { AudiobookCard } from './AudiobookCard';
import type { Audiobook } from '../../types';

function makeAudiobook(overrides: Partial<Audiobook> = {}): Audiobook {
  return {
    id: 'book-1',
    title: 'A Test Book',
    file_path: '/books/a-test-book.m4b',
    created_at: '2026-01-01T00:00:00Z',
    updated_at: '2026-01-01T00:00:00Z',
    ...overrides,
  };
}

describe('AudiobookCard tag display', () => {
  it('does not render a chip for a metadata:source:* tag but does render "English" for metadata:language:en', () => {
    const audiobook = makeAudiobook({
      tags: ['metadata:source:audible', 'metadata:language:en', 'favorite'],
    });

    render(<AudiobookCard audiobook={audiobook} />);

    // The source tag's raw form must never appear as a chip.
    expect(screen.queryByText('metadata:source:audible')).not.toBeInTheDocument();
    expect(screen.queryByText(/audible/i)).not.toBeInTheDocument();

    // The language tag renders as its display name.
    expect(screen.getByText('English')).toBeInTheDocument();
    expect(screen.queryByText('metadata:language:en')).not.toBeInTheDocument();

    // Plain user tags are unaffected.
    expect(screen.getByText('favorite')).toBeInTheDocument();
  });

  it('excludes source tags from the "+N more" overflow count', () => {
    const audiobook = makeAudiobook({
      tags: [
        'metadata:source:audible',
        'metadata:language:en',
        'one',
        'two',
        'three',
      ],
    });

    render(<AudiobookCard audiobook={audiobook} />);

    // 4 visible (non-source) tags total: language + one/two/three -> first 3
    // shown, "+1" overflow for the remainder. If the source tag were
    // counted, this would incorrectly read "+2".
    expect(screen.getByText('+1')).toBeInTheDocument();
  });
});
