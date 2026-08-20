// file: web/src/components/bookdetail/BookDetailStatusAlerts.test.tsx
// version: 1.0.0
// guid: e4c2794b-191c-4201-a406-0ccf738b32b8
// last-edited: 2026-08-20

/**
 * The duplicate alert's link, which was dead.
 *
 * There was no test file here at all, and `/dedup/candidates` -- a route App.tsx
 * never registered -- sat in the duplicate warning long enough that nobody
 * noticed it 404. A rendered link that goes nowhere looks exactly like a
 * rendered link that works, which is why this asserts the target rather than the
 * text.
 */

import { render, screen } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, it, expect } from 'vitest';
import type { Book } from '../../services/api';
import { BookDetailStatusAlerts } from './BookDetailStatusAlerts';

function renderAlerts(book: Partial<Book>) {
  return render(
    <MemoryRouter>
      <BookDetailStatusAlerts
        book={{ id: 42, title: 'T', ...book } as Book}
        actionLoading={false}
        onRestore={() => {}}
        onUnquarantine={() => {}}
        onOpenPurgeDialog={() => {}}
        onQuarantine={() => {}}
      />
    </MemoryRouter>
  );
}

describe('the duplicate-metadata alert', () => {
  it('points at a route that exists, carrying the book it is about', () => {
    renderAlerts({ metadata_source_hash: 'abc', metadata_source_hash_duplicate_count: 3 });

    const link = screen.getByRole('link', { name: /view duplicates/i });
    // App.tsx registers /dedup, /dedup/labels and /review. It has never
    // registered /dedup/candidates, which is where this pointed.
    expect(link).toHaveAttribute('href', '/review?lane=dupes&book=42');
  });

  it('is a router link, not a full page load', () => {
    // A raw <a href> in a SPA throws away the whole app and reboots it. The
    // route being real is only half of a working link.
    renderAlerts({ metadata_source_hash: 'abc', metadata_source_hash_duplicate_count: 1 });

    const link = screen.getByRole('link', { name: /view duplicates/i });
    const ev = new MouseEvent('click', { bubbles: true, cancelable: true });
    link.dispatchEvent(ev);
    // React Router calls preventDefault and navigates in-process; a plain anchor
    // leaves the event alone for the browser to act on.
    expect(ev.defaultPrevented).toBe(true);
  });

  it('says nothing when the book shares its metadata with nothing', () => {
    renderAlerts({ metadata_source_hash: 'abc', metadata_source_hash_duplicate_count: 0 });
    expect(screen.queryByRole('link', { name: /view duplicates/i })).not.toBeInTheDocument();
  });
});
