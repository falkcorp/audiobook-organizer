// file: web/src/components/dedup/__tests__/DedupAuthorTab.test.tsx
// version: 1.1.0
// guid: 7f3a1c9e-4b2d-4e6f-8a1b-9c2d3e4f5a6b
// last-edited: 2026-09-10

import { render, screen, fireEvent, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { AuthorDedupTab } from '../DedupAuthorTab';
import * as api from '../../../services/api';
import type { AuthorDedupGroup, Book } from '../../../services/api';

// Only the calls AuthorDedupTab makes on mount / on popover open are stubbed;
// everything else keeps its real implementation.
vi.mock('../../../services/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../../../services/api')>();
  return {
    ...actual,
    getAuthorDuplicates: vi.fn(),
    getBooksByAuthor: vi.fn(),
  };
});

function makeGroup(): AuthorDedupGroup {
  return {
    canonical: { id: 101, name: 'Prolific Author', created_at: '2026-09-01T00:00:00Z' },
    variants: [],
    book_count: 3,
  };
}

function makeBook(id: string): Book {
  return {
    id,
    title: `Populated Title ${id}`,
    author_name: 'Prolific Author',
    file_path: `/library/${id}.m4b`,
    created_at: '2026-09-01T00:00:00Z',
    updated_at: '2026-09-01T00:00:00Z',
  };
}

function renderTab() {
  return render(
    <MemoryRouter>
      <AuthorDedupTab />
    </MemoryRouter>
  );
}

async function openBooksPopover() {
  const chip = await screen.findByText('3 book(s)');
  fireEvent.click(chip);
}

describe('AuthorDedupTab books popover', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('shows a distinct fetch-error state, not an empty list, when getBooksByAuthor rejects', async () => {
    vi.mocked(api.getAuthorDuplicates).mockResolvedValue({ groups: [makeGroup()] });
    vi.mocked(api.getBooksByAuthor).mockRejectedValue(new Error('boom: 500'));

    renderTab();
    await openBooksPopover();

    // The defect: a rejected fetch used to render "No books found" -- the same
    // text a genuinely-empty author gets -- biasing a human merge decision.
    // The fix must show a visible error instead, and never show the empty-state
    // copy when the fetch actually failed.
    await waitFor(() => {
      expect(screen.getByText(/could not load/i)).toBeInTheDocument();
    });
    expect(screen.queryByText('No books found')).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: /retry/i })).toBeInTheDocument();
  });

  it('Retry actually re-fetches and clears the error state on success', async () => {
    vi.mocked(api.getAuthorDuplicates).mockResolvedValue({ groups: [makeGroup()] });
    vi.mocked(api.getBooksByAuthor)
      .mockRejectedValueOnce(new Error('boom: 500'))
      .mockResolvedValue([makeBook('b1')]);

    renderTab();
    await openBooksPopover();

    await waitFor(() => {
      expect(screen.getByText(/could not load/i)).toBeInTheDocument();
    });

    fireEvent.click(screen.getByRole('button', { name: /retry/i }));

    // A retry that only re-renders the same failed state (or does nothing)
    // is a recovery affordance that lies to the user -- worse than the
    // original bug this task fixes. Assert the actual outcome, not just
    // that the button exists.
    await waitFor(() => {
      expect(screen.getByText('Populated Title b1')).toBeInTheDocument();
    });
    expect(screen.queryByText(/could not load/i)).not.toBeInTheDocument();
    expect(api.getBooksByAuthor).toHaveBeenCalledTimes(2);
  });

  it('still shows the populated book list on a successful fetch (happy path)', async () => {
    vi.mocked(api.getAuthorDuplicates).mockResolvedValue({ groups: [makeGroup()] });
    vi.mocked(api.getBooksByAuthor).mockResolvedValue([makeBook('b1'), makeBook('b2')]);

    renderTab();
    await openBooksPopover();

    await waitFor(() => {
      expect(screen.getByText('Populated Title b1')).toBeInTheDocument();
    });
    expect(screen.getByText('Populated Title b2')).toBeInTheDocument();
    expect(screen.queryByText(/could not load/i)).not.toBeInTheDocument();
  });
});
