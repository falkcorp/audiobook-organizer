// file: web/src/pages/Library.coreEvents.test.tsx
// version: 0.1.0
// guid: 5d1f7a3e-82c4-4b69-9a0e-c3f6d2b81e47
// last-edited: 2026-11-22

// Library on the core event stream (F6-03 flip): books.book.changed.v1 frames patch the
// rows on screen (batched per 250 ms by useBookChanges), a reset or reconnect does one
// same-query refetch, and the connection toasts follow the shared stream's own state.
// The stream is a live fake the test writes frames to, the way core's hub does.

import { act, render, waitFor } from '@testing-library/react';
import { MemoryRouter } from 'react-router';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { Library } from './Library';
import { OperationTrackerProvider } from '../components/operations/OperationTrackerProvider';
import { ToastProvider } from '../components/toast/ToastProvider';
import * as api from '../services/api';
import { resetSharedEventStream, TOPIC_OPENED, TOPIC_RESET } from '../services/coreEvents';
import { sseStream, type FakeStream } from '../test/eventStreamFake';

const book = (id: string, title: string) => ({
  id,
  title,
  file_path: `/tmp/${id}.m4b`,
  created_at: '2026-01-01T00:00:00Z',
  updated_at: '2026-01-01T00:00:00Z',
  author_name: 'Author',
});

vi.mock('../services/api', async (importOriginal) => {
  const actual = await importOriginal<typeof import('../services/api')>();
  return {
    ...actual,
    getOperationLogsTail: vi.fn().mockResolvedValue([]),
    getOperationTimeline: vi.fn().mockResolvedValue([]),
    getActiveOperations: vi.fn().mockResolvedValue([]),
    getBooks: vi.fn(),
    getBooksByIds: vi.fn(),
    searchBooks: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    searchBooksPage: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    getImportPaths: vi.fn().mockResolvedValue([]),
    countBooks: vi.fn().mockResolvedValue(2),
    getBookFacets: vi.fn().mockResolvedValue({ genres: [], languages: [] }),
    getAuthors: vi.fn().mockResolvedValue([]),
    getSeries: vi.fn().mockResolvedValue([]),
    getHomeDirectory: vi.fn().mockResolvedValue('/tmp'),
    getSoftDeletedBooks: vi.fn().mockResolvedValue({ items: [], count: 0 }),
    getUserColumnConfig: vi.fn().mockResolvedValue(null),
    saveUserColumnConfig: vi.fn().mockResolvedValue(undefined),
    listAllUserTags: vi.fn().mockResolvedValue([]),
    getSavedFilterPresets: vi.fn().mockResolvedValue([]),
    saveSavedFilterPresets: vi.fn().mockResolvedValue(undefined),
  };
});

// Every stream the app opened on the events path, oldest first.
let opened: FakeStream[] = [];

function stubStream() {
  vi.stubGlobal(
    'fetch',
    vi.fn(async (url: string, init?: RequestInit) => {
      if (String(url).includes('/api/core/v1/events')) {
        const s = sseStream(init?.signal);
        opened.push(s);
        s.control(TOPIC_OPENED, { topics: ['*'] }); // the server writes it first
        return s.response;
      }
      return new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } });
    }),
  );
}

function renderLibrary() {
  return render(
    <MemoryRouter initialEntries={['/library']}>
      <ToastProvider>
        <OperationTrackerProvider>
          <Library />
        </OperationTrackerProvider>
      </ToastProvider>
    </MemoryRouter>,
  );
}

const shared = () => opened[0];
const changed = (s: FakeStream, id: string, kind: string, n: string) =>
  s.event(n, 'books.book.changed.v1', { kind, id }, id);

beforeEach(() => {
  opened = [];
  resetSharedEventStream();
  stubStream();
  vi.mocked(api.getBooks).mockResolvedValue({
    items: [book('id-1', 'Test Book'), book('id-2', 'Second Book')],
    count: 2,
  } as Awaited<ReturnType<typeof api.getBooks>>);
  vi.mocked(api.getBooksByIds).mockResolvedValue([]);
  vi.mocked(api.getBooksByIds).mockClear();
});

afterEach(() => {
  resetSharedEventStream();
  vi.unstubAllGlobals();
});

async function loaded() {
  const view = renderLibrary();
  await waitFor(() => expect(api.getBooks).toHaveBeenCalled());
  await waitFor(() => expect(opened.length).toBeGreaterThan(0));
  await view.findByText('Test Book');
  // A stream with no id to resume from raises a "reconnect" resync on its first open (the
  // REST load may have missed what was sent before the stream existed), so Library reloads
  // once (or folds it into the first load) and clears the batcher. Let that settle; the tests act on the stream after it.
  await new Promise((r) => setTimeout(r, 700));
  vi.mocked(api.getBooksByIds).mockClear();
  return view;
}

describe('Library on the core event stream', () => {
  it('refetches only the on-screen rows an updated frame names, batched into one call', async () => {
    await loaded();
    await act(async () => {
      changed(shared(), 'id-1', 'updated', 'n-1');
      changed(shared(), 'id-2', 'updated', 'n-2');
      changed(shared(), 'not-on-screen', 'updated', 'n-3');
    });
    await waitFor(() => expect(api.getBooksByIds).toHaveBeenCalledTimes(1));
    expect(vi.mocked(api.getBooksByIds).mock.calls[0][0]).toEqual(['id-1', 'id-2']);
  });

  it('removes a deleted row from the screen without reloading the list', async () => {
    const view = await loaded();
    const loads = vi.mocked(api.getBooks).mock.calls.length;
    await act(async () => {
      changed(shared(), 'id-1', 'deleted', 'n-1');
    });
    await waitFor(() => expect(view.queryByText('Test Book')).toBeNull());
    expect(view.getByText('Second Book')).toBeInTheDocument();
    expect(vi.mocked(api.getBooks).mock.calls.length).toBe(loads);
  });

  it('does one same-query refetch when the server resets the stream', async () => {
    await loaded();
    const loads = vi.mocked(api.getBooks).mock.calls.length;
    await act(async () => {
      shared().control(TOPIC_RESET, { reason: 'gap' });
    });
    await waitFor(() => expect(vi.mocked(api.getBooks).mock.calls.length).toBe(loads + 1));
    // Merged and paced by useResync: one reload, not a storm.
    await new Promise((r) => setTimeout(r, 400));
    expect(vi.mocked(api.getBooks).mock.calls.length).toBe(loads + 1);
  });

  it('toasts a lost connection and its restoration, and only for the shared stream', async () => {
    const view = await loaded();
    await act(async () => {
      shared().end();
    });
    await view.findByText('Connection lost. Reconnecting...');
    await view.findByText('Connection restored.', undefined, { timeout: 15_000 });
  }, 25_000);
});
