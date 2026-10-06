// file: web/src/components/bookdetail/BookDetailVersionGroup.test.tsx
// version: 1.1.0
// guid: 72b1d821-56f4-46e5-8f78-10e8381cd3cb
// last-edited: 2026-10-06

/**
 * TASK-169: the "other versions of this book" link.
 *
 * A nil/empty version_group_id must not render a link at all (the caution
 * item this unblocks explicitly calls out a link that silently returns the
 * whole library as worse than plain text) — and, separately, the link must
 * carry `is_primary_version=false`, because the Library page's book list
 * defaults to primary-only (api.ts getBooks) and a link without that
 * override would show at most one book: the opposite of its own purpose.
 */

import { describe, it, expect, vi } from 'vitest';
import { screen, fireEvent } from '@testing-library/react';
import { renderWithProviders } from '../../test/renderWithProviders';
import { BookDetailVersionGroup, type BookDetailVersionGroupProps } from './BookDetailVersionGroup';
import type { Book, BookFile } from '../../services/api';
import { fileDiskStatus } from './bookDetailUtils';

function makeBook(over: Partial<Book> = {}): Book {
  return { id: 'bk-1', title: 'A Book', format: 'mp3', ...over } as Book;
}

const noop = () => {};
const noopAsync = async () => {};

function renderGroup(overrides: Partial<BookDetailVersionGroupProps> = {}) {
  const book = overrides.book ?? makeBook();
  const groupVersions = overrides.groupVersions ?? [book];
  return renderWithProviders(
    <BookDetailVersionGroup
      book={book}
      groupVersions={groupVersions}
      expandedVersionIds={new Set()}
      expandedSegmentVersionIds={new Set()}
      bookFiles={[]}
      segments={[]}
      versionSegments={{}}
      versionFileTags={{}}
      selectedSegmentIds={new Set()}
      versions={overrides.versions ?? groupVersions}
      filesRefreshKey={0}
      compareSnapshotTs={null}
      splittingVersion={false}
      splittingToBooks={false}
      onToggleVersionExpanded={noop}
      onSetPrimary={noop}
      onUnlinkVersion={noop}
      onSetSelectedSegmentIds={noop}
      onSetExpandedSegmentVersionIds={noop}
      onSetActiveTab={noop}
      onSetRelocateSegment={noop}
      onMoveToVersion={noop}
      onSplitVersion={noop}
      onSplitToBooks={noop}
      onExtractTrackInfo={noopAsync}
      onClearCompareSnapshot={noop}
      {...overrides}
    />
  );
}

describe('BookDetailVersionGroup "other versions" link', () => {
  it('is absent for a book with no version_group_id', () => {
    renderGroup({ book: makeBook({ version_group_id: undefined }) });
    expect(screen.queryByRole('link', { name: /Other versions/ })).toBeNull();
  });

  it('is absent for a book with an empty-string version_group_id', () => {
    renderGroup({ book: makeBook({ version_group_id: '' }) });
    expect(screen.queryByRole('link', { name: /Other versions/ })).toBeNull();
  });

  it('links to the library filtered by version_group_id, with the primary-only default overridden', () => {
    const book = makeBook({ version_group_id: 'vg-123' });
    renderGroup({ book });

    const link = screen.getByRole('link', { name: /Other versions/ }) as HTMLAnchorElement;
    const url = new URL(link.getAttribute('href')!, 'http://localhost');
    expect(url.pathname).toBe('/library');
    expect(JSON.parse(url.searchParams.get('filters')!)).toEqual([
      { field: 'version_group_id', value: 'vg-123', negated: false },
    ]);
    // Required, not cosmetic: without this the Library page's own
    // `is_primary_version=true` default would show only the primary version.
    expect(url.searchParams.get('is_primary_version')).toBe('false');
  });

  it('does not render for a format tray that does not contain the current book', () => {
    // BookDetailVersionGroup is invoked once per format tray; only the tray
    // whose groupVersions actually contains `book` should carry the link, or
    // a multi-format group would render it once per tray.
    const book = makeBook({ id: 'bk-1', version_group_id: 'vg-123' });
    const otherFormatSibling = makeBook({ id: 'bk-2', version_group_id: 'vg-123', format: 'm4b' });
    renderGroup({
      book,
      groupVersions: [otherFormatSibling],
      versions: [book, otherFormatSibling],
    });
    expect(screen.queryByRole('link', { name: /Other versions/ })).toBeNull();
  });
});

// file_exists is a live stat; null means it could not answer. Null must not
// render as present: the stored `missing` flag is the fallback for the red
// row, the missing badge and the relocate prompt.
describe('BookDetailVersionGroup file disk state', () => {
  function file(id: string, over: Partial<BookFile>): BookFile {
    return {
      id,
      book_id: 'bk-1',
      file_path: `/lib/${id}.mp3`,
      missing: false,
      ...over,
    } as BookFile;
  }
  function renderFiles(bookFiles: BookFile[], onSetRelocateSegment = vi.fn()) {
    const book = makeBook();
    renderGroup({
      book,
      bookFiles,
      expandedVersionIds: new Set([book.id]),
      onSetRelocateSegment,
    });
    return onSetRelocateSegment;
  }
  function row(path: string) {
    const inRow = screen
      .getAllByText(path)
      .map((el) => el.closest('tbody tr'))
      .filter((el): el is HTMLElement => el !== null);
    expect(inRow.length).toBe(1);
    return inRow[0];
  }

  it('null + stored missing: shows missing and offers relocate', () => {
    const relocate = renderFiles([file('a', { file_exists: null, missing: true })]);
    expect(screen.getByText(/1 of 1 file\s+missing on disk/)).toBeTruthy();
    expect(screen.getByLabelText('file missing')).toBeTruthy();
    fireEvent.click(row('/lib/a.mp3'));
    expect(relocate).toHaveBeenCalledTimes(1);
  });

  it('null + not stored missing: shows unknown, not missing, no relocate', () => {
    const relocate = renderFiles([file('b', { file_exists: null, missing: false })]);
    expect(screen.queryByText(/missing on disk/)).toBeNull();
    expect(screen.getByText(/could not be checked on disk/)).toBeTruthy();
    expect(screen.getByLabelText('disk status unknown')).toBeTruthy();
    expect(screen.queryByLabelText('file missing')).toBeNull();
    fireEvent.click(row('/lib/b.mp3'));
    expect(relocate).not.toHaveBeenCalled();
  });

  it('false: missing even when the stored flag says present', () => {
    const relocate = renderFiles([file('c', { file_exists: false, missing: false })]);
    expect(screen.getByText(/1 of 1 file\s+missing on disk/)).toBeTruthy();
    expect(screen.queryByText(/could not be checked/)).toBeNull();
    fireEvent.click(row('/lib/c.mp3'));
    expect(relocate).toHaveBeenCalledTimes(1);
  });

  it('true: present even when the stored flag says missing', () => {
    const relocate = renderFiles([file('d', { file_exists: true, missing: true })]);
    expect(screen.queryByText(/missing on disk/)).toBeNull();
    expect(screen.queryByLabelText('file missing')).toBeNull();
    expect(screen.queryByLabelText('disk status unknown')).toBeNull();
    fireEvent.click(row('/lib/d.mp3'));
    expect(relocate).not.toHaveBeenCalled();
  });
});

describe('fileDiskStatus', () => {
  it.each([
    [{ file_exists: null, missing: true }, 'unknown-missing'],
    [{ file_exists: null, missing: false }, 'unknown'],
    [{ file_exists: false, missing: false }, 'missing'],
    [{ file_exists: true, missing: true }, 'present'],
    [{ active: false }, 'unknown-missing'],
    [{}, 'unknown'],
  ] as const)('%o -> %s', (input, want) => {
    expect(fileDiskStatus(input)).toBe(want);
  });
});
