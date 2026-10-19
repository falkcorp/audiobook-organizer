// file: web/src/services/collectionApi.test.ts
// version: 1.0.0
// guid: f04e7178-b734-4103-8673-0fda84e37bb8
// last-edited: 2026-10-18

import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import {
  CollectionApiError,
  CollectionConflictError,
  CollectionUnavailableError,
  addBooksToCollection,
  createCollection,
  deleteCollection,
  getCollection,
  listCollections,
  materializeCollection,
  removeBookFromCollection,
  updateCollection,
} from './collectionApi';

let fetchMock: ReturnType<typeof vi.fn>;

function reply(status: number, body: unknown) {
  fetchMock.mockResolvedValueOnce({
    ok: status >= 200 && status < 300,
    status,
    statusText: 'status text',
    json: async () => body,
  });
}

function lastCall() {
  const [url, init] = fetchMock.mock.calls.at(-1) as [string, RequestInit];
  return {
    url,
    method: init.method ?? 'GET',
    body: init.body ? JSON.parse(String(init.body)) : undefined,
  };
}

beforeEach(() => {
  fetchMock = vi.fn();
  vi.stubGlobal('fetch', fetchMock);
});
afterEach(() => vi.unstubAllGlobals());

const col = { id: 'c1', name: 'Test Collection', type: 'static', version: 1 };

describe('collectionApi', () => {
  it('lists with the type filter and unwraps items', async () => {
    reply(200, { items: [col], count: 1, limit: 200, offset: 0 });
    const res = await listCollections('dynamic');
    expect(lastCall()).toMatchObject({ method: 'GET' });
    expect(lastCall().url).toBe('/api/v1/collections?limit=200&offset=0&type=dynamic');
    expect(res).toEqual({ collections: [col], count: 1 });
  });

  it('lists with no type filter and tolerates a missing items array', async () => {
    reply(200, { count: 0 });
    const res = await listCollections();
    expect(lastCall().url).toBe('/api/v1/collections?limit=200&offset=0');
    expect(res).toEqual({ collections: [], count: 0 });
  });

  it('gets one collection (bare data, not wrapped)', async () => {
    reply(200, { data: col });
    expect(await getCollection('c1')).toEqual(col);
    expect(lastCall()).toMatchObject({ url: '/api/v1/collections/c1', method: 'GET' });
  });

  it('creates a static collection with the exact payload', async () => {
    reply(201, { data: col });
    await createCollection({ type: 'static', name: 'Test Collection', book_ids: ['b1', 'b2'] });
    expect(lastCall()).toEqual({
      url: '/api/v1/collections',
      method: 'POST',
      body: { type: 'static', name: 'Test Collection', book_ids: ['b1', 'b2'] },
    });
  });

  it('creates a dynamic collection with the exact payload', async () => {
    reply(201, { data: { ...col, type: 'dynamic' } });
    await createCollection({
      type: 'dynamic',
      name: 'Test Collection',
      query: 'read_status:finished last_played:>-30d',
      sort_json: '[{"field":"title","direction":"asc"}]',
      limit: 10,
    });
    expect(lastCall().body).toEqual({
      type: 'dynamic',
      name: 'Test Collection',
      query: 'read_status:finished last_played:>-30d',
      sort_json: '[{"field":"title","direction":"asc"}]',
      limit: 10,
    });
  });

  it('updates with PUT and sends only the given fields', async () => {
    reply(200, { data: col });
    await updateCollection('c1', { name: 'Renamed' });
    expect(lastCall()).toEqual({
      url: '/api/v1/collections/c1',
      method: 'PUT',
      body: { name: 'Renamed' },
    });
  });

  it('deletes and materializes', async () => {
    reply(200, { data: { id: 'c1', deleted: true } });
    await deleteCollection('c1');
    expect(lastCall()).toMatchObject({ url: '/api/v1/collections/c1', method: 'DELETE' });
    reply(200, { data: col });
    await materializeCollection('c1');
    expect(lastCall()).toMatchObject({ url: '/api/v1/collections/c1/materialize', method: 'POST' });
  });

  it('adds books with one POST and removes one with DELETE, never a PUT', async () => {
    reply(200, { data: col });
    await addBooksToCollection('c1', ['b1', 'b2']);
    expect(lastCall()).toEqual({
      url: '/api/v1/collections/c1/books',
      method: 'POST',
      body: { book_ids: ['b1', 'b2'] },
    });
    reply(200, { data: col });
    await removeBookFromCollection('c1', 'b1');
    expect(lastCall()).toMatchObject({ url: '/api/v1/collections/c1/books/b1', method: 'DELETE' });
    expect(fetchMock.mock.calls.every(([, init]) => (init as RequestInit).method !== 'PUT')).toBe(
      true
    );
  });

  it('maps 409 to CollectionConflictError with the server message and code', async () => {
    reply(409, { error: 'name in use', code: 'CONFLICT' });
    const err = await createCollection({ type: 'static', name: 'x' }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(CollectionConflictError);
    expect(err).toBeInstanceOf(CollectionApiError);
    expect(err).toMatchObject({ message: 'name in use', status: 409, code: 'CONFLICT' });
  });

  it('maps 503 to CollectionUnavailableError', async () => {
    reply(503, { error: 'search index is not ready' });
    const err = await materializeCollection('c1').catch((e: unknown) => e);
    expect(err).toBeInstanceOf(CollectionUnavailableError);
    expect(err).toMatchObject({ message: 'search index is not ready', status: 503 });
  });

  it('maps other failures to CollectionApiError and keeps a 400 body verbatim', async () => {
    reply(400, { error: 'last_played:30d — date offset needs a sign' });
    const err = await updateCollection('c1', { query: 'last_played:30d' }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(CollectionApiError);
    expect(err).not.toBeInstanceOf(CollectionConflictError);
    expect(err).toMatchObject({
      status: 400,
      message: 'last_played:30d — date offset needs a sign',
    });
  });

  it('falls back to the status text when an error body is not JSON', async () => {
    fetchMock.mockResolvedValueOnce({
      ok: false,
      status: 500,
      statusText: 'Internal Server Error',
      json: async () => {
        throw new Error('not json');
      },
    });
    await expect(getCollection('c1')).rejects.toMatchObject({
      message: 'Internal Server Error',
      status: 500,
    });
  });
});
