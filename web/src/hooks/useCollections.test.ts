// file: web/src/hooks/useCollections.test.ts
// version: 1.0.0
// guid: fb9cf85e-0729-4a40-8208-719340a191c1
// last-edited: 2026-10-18

import { describe, it, expect, vi, beforeEach } from 'vitest';
import { renderHook, act, waitFor } from '@testing-library/react';

vi.mock('../services/collectionApi', async (orig) => ({
  ...(await orig<typeof import('../services/collectionApi')>()),
  listCollections: vi.fn(),
  getCollection: vi.fn(),
  createCollection: vi.fn(),
  updateCollection: vi.fn(),
  deleteCollection: vi.fn(),
  materializeCollection: vi.fn(),
  addBooksToCollection: vi.fn(),
  removeBookFromCollection: vi.fn(),
}));
vi.mock('../contexts/AuthContext', () => ({ useAuth: vi.fn() }));

import { useAuth } from '../contexts/AuthContext';
import * as api from '../services/collectionApi';
import { useCanManageCollections, useCollection, useCollections } from './useCollections';

const m = vi.mocked(api);
const mockAuth = vi.mocked(useAuth);

function auth(over: { initialized?: boolean; requiresAuth?: boolean; permissions?: string[] | undefined }) {
  const { permissions = ['collections.manage'] } = over;
  mockAuth.mockReturnValue({
    initialized: over.initialized ?? true,
    requiresAuth: over.requiresAuth ?? true,
    user: { id: 'u1', permissions } as never,
  } as never);
}

const c1 = { id: 'c1', name: 'Test One', type: 'static', version: 1, created_at: '', updated_at: '' } as api.Collection;
const c2 = { ...c1, id: 'c2', name: 'Test Two', type: 'dynamic' } as api.Collection;

beforeEach(() => {
  vi.resetAllMocks();
  auth({});
  m.listCollections.mockResolvedValue({ collections: [c1, c2], count: 2 });
});

describe('useCanManageCollections', () => {
  it('is true only with the permission when auth is on', () => {
    auth({});
    expect(renderHook(() => useCanManageCollections()).result.current).toBe(true);
    auth({ permissions: ['library.view'] });
    expect(renderHook(() => useCanManageCollections()).result.current).toBe(false);
    auth({ permissions: [] });
    expect(renderHook(() => useCanManageCollections()).result.current).toBe(false);
    // a user object with no permissions field at all (an older server)
    mockAuth.mockReturnValue({ initialized: true, requiresAuth: true, user: { id: 'u1' } } as never);
    expect(renderHook(() => useCanManageCollections()).result.current).toBe(false);
  });

  it('is false before auth initialises and with no user', () => {
    auth({ initialized: false });
    expect(renderHook(() => useCanManageCollections()).result.current).toBe(false);
    mockAuth.mockReturnValue({ initialized: true, requiresAuth: true, user: null } as never);
    expect(renderHook(() => useCanManageCollections()).result.current).toBe(false);
  });

  it('is true on an install with authentication off (the server gate is a no-op there)', () => {
    mockAuth.mockReturnValue({ initialized: true, requiresAuth: false, user: null } as never);
    expect(renderHook(() => useCanManageCollections()).result.current).toBe(true);
  });
});

describe('useCollections', () => {
  it('loads once on mount and exposes canManage', async () => {
    const { result } = renderHook(() => useCollections());
    expect(result.current.loading).toBe(true);
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.collections).toEqual([c1, c2]);
    expect(result.current.count).toBe(2);
    expect(result.current.error).toBeNull();
    expect(result.current.canManage).toBe(true);
    expect(m.listCollections).toHaveBeenCalledTimes(1);
  });

  it('exposes canManage false but still loads the reads', async () => {
    auth({ permissions: ['library.view'] });
    const { result } = renderHook(() => useCollections());
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.canManage).toBe(false);
    expect(result.current.collections).toHaveLength(2);
  });

  it('keeps error, empty and loaded distinguishable', async () => {
    m.listCollections.mockRejectedValueOnce(new Error('boom'));
    const failed = renderHook(() => useCollections());
    await waitFor(() => expect(failed.result.current.loading).toBe(false));
    expect(failed.result.current.error).toBe('boom');
    expect(failed.result.current.collections).toEqual([]);

    m.listCollections.mockResolvedValueOnce({ collections: [], count: 0 });
    const empty = renderHook(() => useCollections());
    await waitFor(() => expect(empty.result.current.loading).toBe(false));
    expect(empty.result.current.error).toBeNull();
    expect(empty.result.current.collections).toEqual([]);
  });

  it('passes the type filter and refetches when it changes', async () => {
    const { result, rerender } = renderHook(({ t }: { t?: api.CollectionType }) => useCollections(t), {
      initialProps: { t: undefined as api.CollectionType | undefined },
    });
    await waitFor(() => expect(result.current.loading).toBe(false));
    rerender({ t: 'dynamic' });
    await waitFor(() => expect(m.listCollections).toHaveBeenCalledTimes(2));
    expect(m.listCollections.mock.calls[1][0]).toBe('dynamic');
  });

  it('never lets a slow earlier response overwrite a newer one', async () => {
    let releaseFirst!: (v: { collections: api.Collection[]; count: number }) => void;
    m.listCollections.mockReset();
    m.listCollections.mockImplementationOnce(
      () => new Promise((res) => (releaseFirst = res))
    );
    m.listCollections.mockResolvedValueOnce({ collections: [c2], count: 1 });
    const { result } = renderHook(() => useCollections());
    await act(async () => {
      await result.current.reload();
    });
    expect(result.current.collections).toEqual([c2]);
    await act(async () => {
      releaseFirst({ collections: [c1], count: 1 });
    });
    expect(result.current.collections).toEqual([c2]);
  });

  it('aborts the request on unmount', async () => {
    let signal: AbortSignal | undefined;
    m.listCollections.mockImplementationOnce((_t, _l, _o, s) => {
      signal = s;
      return new Promise(() => {});
    });
    const { unmount } = renderHook(() => useCollections());
    await waitFor(() => expect(signal).toBeDefined());
    unmount();
    expect(signal?.aborted).toBe(true);
  });

  it('refetches after each write and returns the server answer', async () => {
    m.createCollection.mockResolvedValue(c1);
    m.updateCollection.mockResolvedValue(c1);
    m.deleteCollection.mockResolvedValue();
    m.materializeCollection.mockResolvedValue(c2);
    const { result } = renderHook(() => useCollections());
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(m.listCollections).toHaveBeenCalledTimes(1);

    await act(async () => {
      await result.current.create({ type: 'static', name: 'Test One' });
    });
    expect(m.createCollection).toHaveBeenCalledWith({ type: 'static', name: 'Test One' });
    await act(async () => {
      await result.current.update('c1', { name: 'x' });
    });
    expect(m.updateCollection).toHaveBeenCalledWith('c1', { name: 'x' });
    await act(async () => {
      await result.current.remove('c1');
    });
    await act(async () => {
      expect(await result.current.materialize('c2')).toBe(c2);
    });
    expect(m.listCollections).toHaveBeenCalledTimes(5);
  });

  it('rejects with the typed 409 and 503 errors and does not refetch on failure', async () => {
    m.createCollection.mockRejectedValueOnce(new api.CollectionConflictError('name in use'));
    m.materializeCollection.mockRejectedValueOnce(
      new api.CollectionUnavailableError('search index is not ready, try again')
    );
    const { result } = renderHook(() => useCollections());
    await waitFor(() => expect(result.current.loading).toBe(false));
    await act(async () => {
      await expect(result.current.create({ type: 'static', name: 'dup' })).rejects.toBeInstanceOf(
        api.CollectionConflictError
      );
      await expect(result.current.materialize('c2')).rejects.toBeInstanceOf(api.CollectionUnavailableError);
    });
    expect(m.listCollections).toHaveBeenCalledTimes(1);
  });
});

describe('useCollection', () => {
  it('loads one collection, then stores each write answer', async () => {
    m.getCollection.mockResolvedValue(c2);
    m.materializeCollection.mockResolvedValue({ ...c2, materialized_book_ids: ['b1'] });
    m.addBooksToCollection.mockResolvedValue({ ...c1, book_ids: ['b1', 'b2'] });
    m.removeBookFromCollection.mockResolvedValue({ ...c1, book_ids: ['b2'] });
    m.updateCollection.mockResolvedValue({ ...c1, name: 'Renamed' });
    m.deleteCollection.mockResolvedValue();
    const { result } = renderHook(() => useCollection('c2'));
    expect(result.current.loading).toBe(true);
    await waitFor(() => expect(result.current.collection).toEqual(c2));
    expect(m.getCollection).toHaveBeenCalledTimes(1);
    expect(m.getCollection.mock.calls[0][0]).toBe('c2');

    await act(async () => {
      await result.current.materialize();
    });
    expect(result.current.collection?.materialized_book_ids).toEqual(['b1']);
    expect(m.materializeCollection).toHaveBeenCalledWith('c2');
    await act(async () => {
      await result.current.addBooks(['b1', 'b2']);
    });
    expect(m.addBooksToCollection).toHaveBeenCalledWith('c2', ['b1', 'b2']);
    await act(async () => {
      await result.current.removeBook('b1');
    });
    expect(m.removeBookFromCollection).toHaveBeenCalledWith('c2', 'b1');
    expect(result.current.collection?.book_ids).toEqual(['b2']);
    await act(async () => {
      await result.current.update({ name: 'Renamed' });
    });
    expect(result.current.collection?.name).toBe('Renamed');
    await act(async () => {
      await result.current.remove();
    });
    expect(m.deleteCollection).toHaveBeenCalledWith('c2');
  });

  it('reports a failed load as an error, not as an empty collection', async () => {
    m.getCollection.mockRejectedValue(new Error('not found'));
    const { result } = renderHook(() => useCollection('nope'));
    await waitFor(() => expect(result.current.loading).toBe(false));
    expect(result.current.error).toBe('not found');
    expect(result.current.collection).toBeNull();
  });

  it('surfaces a 503 from materialize to the caller and keeps the stored collection', async () => {
    m.getCollection.mockResolvedValue(c2);
    m.materializeCollection.mockRejectedValue(new api.CollectionUnavailableError('search index is not ready'));
    const { result } = renderHook(() => useCollection('c2'));
    await waitFor(() => expect(result.current.collection).toEqual(c2));
    await act(async () => {
      await expect(result.current.materialize()).rejects.toBeInstanceOf(api.CollectionUnavailableError);
    });
    expect(result.current.collection).toEqual(c2);
  });
});
