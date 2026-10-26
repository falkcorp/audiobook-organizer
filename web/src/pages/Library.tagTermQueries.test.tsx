// file: web/src/pages/Library.tagTermQueries.test.tsx
// version: 1.0.2
// guid: ba4d9425-349f-45d9-94a5-c8b56faf4856
// last-edited: 2026-10-26

import { render, screen, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router-dom';
import { describe, it, expect, vi, beforeEach } from 'vitest';
import { Library } from './Library';
import { OperationTrackerProvider } from '../components/operations/OperationTrackerProvider';
import * as api from '../services/api';
import { useLibraryCache } from '../stores/useLibraryCache';

vi.mock('../services/api', () => {
  class ApiError extends Error {
    status: number;
    data?: unknown;
    constructor(message: string, status: number, data?: unknown) {
      super(message);
      this.name = 'ApiError';
      this.status = status;
      this.data = data;
    }
  }
  return {
    ApiError,
    getOperationLogsTail: vi.fn().mockResolvedValue([]),
    getOperationTimeline: vi.fn().mockResolvedValue([]),
    getActiveOperations: vi.fn().mockResolvedValue([]),
    getBooks: vi.fn(),
    searchBooks: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    searchBooksPage: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    getImportPaths: vi.fn().mockResolvedValue([]),
    countBooks: vi.fn().mockResolvedValue(1),
    getBookFacets: vi.fn().mockResolvedValue({ genres: [], languages: [] }),
    getAuthors: vi.fn().mockResolvedValue([]),
    getSeries: vi.fn().mockResolvedValue([
      {
        id: 42,
        name: 'The Expanse',
        created_at: '2026-01-01T00:00:00Z',
        book_count: 9,
        file_count: 9,
      },
    ]),
    getSystemStatus: vi.fn().mockResolvedValue({
      status: 'ok',
      library: { path: '/tmp', book_count: 1, total_size: 0 },
      import_paths: { book_count: 0, folder_count: 0, total_size: 0 },
      memory: {},
      runtime: {},
      operations: { recent: [] },
    }),
    getHomeDirectory: vi.fn().mockResolvedValue('/tmp'),
    getSoftDeletedBooks: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    getUserColumnConfig: vi.fn().mockResolvedValue(null),
    saveUserColumnConfig: vi.fn().mockResolvedValue(undefined),
    listAllUserTags: vi.fn().mockResolvedValue([
      { tag: 'fantasy', count: 12306 },
      { tag: 'epic', count: 4000 },
      { tag: 'romance', count: 3000 },
    ]),
    // Scoped answer: depends on the request, like the server's.
    getScopedTagFacets: vi.fn().mockResolvedValue({ tags: [], total: 0 }),
    getSavedFilterPresets: vi.fn().mockResolvedValue([]),
    saveSavedFilterPresets: vi.fn().mockResolvedValue(undefined),
  };
});

// What the server says to a tag: term it cannot apply exactly (internal/search
// TagTermError); the Library must put it in front of the person.
const SHAPE =
  'invalid search: tag: cannot be used inside an OR group or an alternation, or under NOT or -, yet: a tag filter is exact, so it has to apply to the whole search. Write it at the top level, ANDed with the rest (read_status:finished tag:"noir"), or pick the tag chip';
const VALUE =
  'invalid search: tag: takes one whole tag name, with no *, ~, ^ or comparison: it matches that tag exactly. Put the name in quotes if it contains one of those characters';

// The server stand-in: a whole-sent query with a tag the exact filter cannot apply is
// a 400 with the server's sentence; anything else is a book.
function serverAnswer(search?: string) {
  const text = search ?? '';
  if (/(^|\s)-?tag:[^\s"]*\*/.test(text)) {
    return Promise.reject(new api.ApiError(VALUE, 400));
  }
  if (/(^|\s)(OR|AND)(\s|$)|(^|\s)(-|NOT\s+)tag:/.test(text)) {
    return Promise.reject(new api.ApiError(SHAPE, 400));
  }
  return Promise.resolve({
    items: [
      {
        id: 'id-1',
        title: 'Hush',
        file_path: '/tmp/book.m4b',
        created_at: '2026-01-01T00:00:00Z',
        updated_at: '2026-01-01T00:00:00Z',
        author_name: 'Jeph Loeb',
      },
    ],
    count: 1,
  });
}

const lastBooksCall = () => vi.mocked(api.getBooks).mock.calls.at(-1)?.[2];

function open(search: string) {
  return render(
    <MemoryRouter initialEntries={[`/library?search=${encodeURIComponent(search)}`]}>
      <OperationTrackerProvider>
        <Library />
      </OperationTrackerProvider>
    </MemoryRouter>
  );
}

describe('the Library sends a query with boolean structure whole and shows the 400', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useLibraryCache.getState().clear();
    try {
      localStorage.clear();
    } catch {
      // ignore
    }
    vi.mocked(api.getBooks).mockImplementation((async (
      _page: number,
      _size: number,
      opts?: { search?: string }
    ) => serverAnswer(opts?.search)) as unknown as typeof api.getBooks);
  });

  it.each([
    ['Batman -tag:"to-read"', SHAPE],
    ['-tag:noir', SHAPE],
    ['tag:noir OR tag:gotham', SHAPE],
    ['tag:noir*', VALUE],
  ])(
    '%%s: search= is the typed text, nothing in filters= or tags=, and the message shows',
    async (typed, message) => {
      open(typed);
      await waitFor(() => expect(lastBooksCall()?.search).toBe(typed), { timeout: 3000 });
      expect(lastBooksCall()?.filters).toBeUndefined();
      expect(lastBooksCall()?.tags).toBeUndefined();
      // the server's sentence reaches the screen (under the search box and in place of
      // the stale list), not only the console
      const shown = await screen.findAllByText(message, {}, { timeout: 3000 });
      expect(shown.length).toBeGreaterThan(0);
    }
  );

  it('a plain tag term is still the flat request (tags[] plus the rest as search=) and shows no error', async () => {
    open('Hush tag:"to-read"');
    await waitFor(() => expect(lastBooksCall()?.search).toBe('Hush'), { timeout: 3000 });
    expect(lastBooksCall()?.tags).toEqual(['to-read']);
    expect(await screen.findByText('Hush', {}, { timeout: 3000 })).toBeInTheDocument();
    expect(screen.queryByText(SHAPE)).toBeNull();
    expect(screen.queryByText(VALUE)).toBeNull();
  });

  // B1: the parentheses and pipes inside a regex are text. The Library sends the term as a
  // filter, where the filter layer matches the regex, not whole to a search index that has
  // none.
  it.each(['title:/^(the|a) /', 'title:/hush|knight/', 'title:Batman(1989)'])(
    '%s is a filter, not a whole send, and shows no error',
    async (typed) => {
      open(typed);
      await waitFor(() => expect(lastBooksCall()?.filters).toBeDefined(), { timeout: 3000 });
      expect(lastBooksCall()?.search).toBeUndefined();
      expect(JSON.parse(lastBooksCall()?.filters ?? '[]')).toEqual([
        expect.objectContaining({ field: 'title', negated: false }),
      ]);
      expect(await screen.findByText('Hush', {}, { timeout: 3000 })).toBeInTheDocument();
      expect(screen.queryByText(SHAPE)).toBeNull();
      expect(screen.queryByText(VALUE)).toBeNull();
    }
  );
});
