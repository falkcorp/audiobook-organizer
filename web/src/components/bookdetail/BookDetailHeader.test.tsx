// file: web/src/components/bookdetail/BookDetailHeader.test.tsx
// version: 1.0.0
// guid: c3001306-2cc8-41e3-a255-8a85dc834570
// last-edited: 2026-08-23

/**
 * The two version-group signals in the header.
 *
 * There was no test file here at all. The "Version Group" chip shipped with a
 * bare "Version Group Linked" label -- present but silent about size -- while
 * the iTunes chip two lines below it has always shown its count. The count is
 * back; these tests pin both it and the primary-version marker, because a chip
 * that renders unconditionally and a chip that renders for the right book look
 * identical in a screenshot.
 *
 * The count assertions deliberately go through the same array
 * BookDetailVersionGroup renders. A count computed from a different source than
 * the list underneath it can drift without any test noticing -- so the empty
 * case below asserts the header agrees with that component's own
 * `versions.length > 0 ? versions : [book]` fallback, not merely that some
 * number appears.
 */

import { render, screen } from '@testing-library/react';
import { describe, it, expect, vi } from 'vitest';
import type { Book, BookFile, BookSegment } from '../../services/api';
import { BookDetailHeader } from './BookDetailHeader';

// ReadStatusChip fires a real getBookState on mount. It is unrelated to
// anything here and would leave an unhandled promise per test.
vi.mock('../../services/readingApi', async () => {
  const actual = await vi.importActual<typeof import('../../services/readingApi')>(
    '../../services/readingApi'
  );
  return { ...actual, getBookState: vi.fn().mockResolvedValue(null) };
});

function makeBook(over: Partial<Book> = {}): Book {
  return { id: 'bk-1', title: 'A Book', ...over } as Book;
}

function renderHeader(book: Book, versions: Book[] = []) {
  return render(
    <BookDetailHeader
      book={book}
      bookFiles={[] as BookFile[]}
      segments={[] as BookSegment[]}
      itunesLinked={false}
      itunesPidCount={0}
      versions={versions}
      activeTab="info"
      onBack={() => {}}
      onSetActiveTab={() => {}}
    />
  );
}

describe('the version-group chip', () => {
  it('is absent entirely for a book with no version group', () => {
    renderHeader(makeBook({ version_group_id: undefined }));
    expect(screen.queryByText(/Version Group/)).toBeNull();
    // Positive control: the header did render, so the negative above is about
    // the chip and not about a component that failed to mount.
    expect(screen.getByText('A Book')).toBeTruthy();
  });

  it('carries the number of versions, not a bare "Linked"', () => {
    const book = makeBook({ version_group_id: 'grp-1' });
    renderHeader(book, [book, makeBook({ id: 'bk-2' }), makeBook({ id: 'bk-3' })]);
    expect(screen.getByText('Version Group (3)')).toBeTruthy();
    expect(screen.queryByText('Version Group Linked')).toBeNull();
  });

  it('says (1), not (0), when versions has not loaded -- matching the tray fallback', () => {
    // BookDetailVersionGroup renders `versions.length > 0 ? versions : [book]`,
    // so an empty array still lists one row. A header reading "(0)" directly
    // above a one-row list is the drift this asserts against.
    renderHeader(makeBook({ version_group_id: 'grp-1' }), []);
    expect(screen.getByText('Version Group (1)')).toBeTruthy();
    expect(screen.queryByText('Version Group (0)')).toBeNull();
  });
});

describe('the primary-version marker', () => {
  it('marks the book that is the current version', () => {
    renderHeader(makeBook({ version_group_id: 'grp-1', is_primary_version: true }));
    expect(screen.getByText('Primary Version')).toBeTruthy();
  });

  it('leaves an alternate version unmarked', () => {
    renderHeader(makeBook({ version_group_id: 'grp-1', is_primary_version: false }));
    expect(screen.queryByText('Primary Version')).toBeNull();
    // Positive control: this book IS in a group, it is just not the primary --
    // so the absence above is the flag doing its job, not a missing group.
    expect(screen.getByText('Version Group (1)')).toBeTruthy();
  });
});
