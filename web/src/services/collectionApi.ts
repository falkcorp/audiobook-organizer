// file: web/src/services/collectionApi.ts
// version: 1.2.0
// guid: 36e44f0d-8a86-4830-8fd9-c0a4fbabc018
// last-edited: 2026-10-25

import { apiFetch } from '../utils/apiFetch';

const API_BASE = '/api/v1';

/** A request that gets no answer in this long is reported, never an endless spinner. */
export const COLLECTION_TIMEOUT_MS = 30_000;

export type CollectionType = 'static' | 'dynamic';

/** One sort directive; `sort_json` is the JSON encoding of a list of these. */
export interface CollectionSort {
  field: string;
  direction: 'asc' | 'desc';
}

/** Mirrors database.Collection's json tags. The create/GET responses carry it bare (not wrapped). */
export interface Collection {
  id: string;
  name: string;
  description?: string;
  type: CollectionType;
  book_ids?: string[];
  query?: string;
  sort_json?: string;
  limit?: number;
  /**
   * For a dynamic collection, the members for the person asking. A query on
   * read_status, progress_pct or last_played is evaluated against the
   * viewer's own listening state on every read, so two people can see
   * different members of the same collection. created_by_user_id is
   * attribution only and never decides membership.
   */
  materialized_book_ids?: string[];
  /**
   * True when the members for this viewer could not be worked out (the search
   * index is not ready, the query no longer parses, or the viewer's state could
   * not be read). materialized_book_ids is then empty, and that emptiness means
   * "unknown", not "nothing matches". Sent only for such a collection.
   */
  members_unavailable?: boolean;
  /** Why, in words for the viewer; present with members_unavailable. */
  members_unavailable_message?: string;
  created_at: string;
  updated_at: string;
  created_by_user_id?: string;
  version: number;
}

/** POST /collections. Static carries book_ids and never a query; dynamic the reverse. */
export type CollectionCreateInput =
  | { type: 'static'; name: string; description?: string; book_ids?: string[] }
  | {
      type: 'dynamic';
      name: string;
      description?: string;
      query: string;
      sort_json?: string;
      limit?: number;
    };

/**
 * PUT /collections/:id. There is deliberately no book_ids here: membership of a
 * static collection changes only through addBooksToCollection and
 * removeBookFromCollection, which are atomic on the server.
 */
export interface CollectionUpdateInput {
  name?: string;
  description?: string;
  query?: string;
  sort_json?: string;
  limit?: number;
}

/** Any non-2xx answer. `code` is the server's machine code when it sent one. */
export class CollectionApiError extends Error {
  readonly status: number;
  readonly code?: string;
  readonly data?: unknown;
  constructor(message: string, status: number, code?: string, data?: unknown) {
    super(message);
    this.name = 'CollectionApiError';
    this.status = status;
    this.code = code;
    this.data = data;
  }
}

/** 409: name in use, version conflict, or dynamic collection on a membership call. */
export class CollectionConflictError extends CollectionApiError {
  constructor(message: string, code?: string, data?: unknown) {
    super(message, 409, code, data);
    this.name = 'CollectionConflictError';
  }
}

/** 503: the search index is not ready (materialize), or membership is unavailable. */
export class CollectionUnavailableError extends CollectionApiError {
  constructor(message: string, code?: string, data?: unknown) {
    super(message, 503, code, data);
    this.name = 'CollectionUnavailableError';
  }
}

async function jsonFetch<T>(url: string, opts: RequestInit = {}): Promise<T> {
  const resp = await apiFetch(url, {
    ...opts,
    headers: { 'Content-Type': 'application/json', ...opts.headers },
    timeoutMs: COLLECTION_TIMEOUT_MS,
  });
  if (!resp.ok) {
    const body = (await resp.json().catch(() => ({}))) as { error?: string; code?: string };
    const message = body.error || resp.statusText || `Request failed (${resp.status})`;
    if (resp.status === 409) throw new CollectionConflictError(message, body.code, body);
    if (resp.status === 503) throw new CollectionUnavailableError(message, body.code, body);
    throw new CollectionApiError(message, resp.status, body.code, body);
  }
  const body = await resp.json();
  return (body?.data ?? body) as T;
}

export async function listCollections(
  type?: CollectionType,
  limit = 200,
  offset = 0,
  signal?: AbortSignal
): Promise<{ collections: Collection[]; count: number }> {
  const params = new URLSearchParams({ limit: String(limit), offset: String(offset) });
  if (type) params.set('type', type);
  const data = await jsonFetch<{ items?: Collection[]; count?: number }>(
    `${API_BASE}/collections?${params}`,
    { signal }
  );
  const collections = data.items ?? [];
  return { collections, count: data.count ?? collections.length };
}

/** For a dynamic collection this RE-EVALUATES the query and returns the live members. */
export function getCollection(id: string, signal?: AbortSignal): Promise<Collection> {
  return jsonFetch<Collection>(`${API_BASE}/collections/${encodeURIComponent(id)}`, { signal });
}

export function createCollection(input: CollectionCreateInput): Promise<Collection> {
  return jsonFetch<Collection>(`${API_BASE}/collections`, {
    method: 'POST',
    body: JSON.stringify(input),
  });
}

export function updateCollection(id: string, patch: CollectionUpdateInput): Promise<Collection> {
  return jsonFetch<Collection>(`${API_BASE}/collections/${encodeURIComponent(id)}`, {
    method: 'PUT',
    body: JSON.stringify(patch),
  });
}

export async function deleteCollection(id: string): Promise<void> {
  await jsonFetch<unknown>(`${API_BASE}/collections/${encodeURIComponent(id)}`, {
    method: 'DELETE',
  });
}

/** Re-evaluate and store a dynamic collection's members. 503 while the index is closed. */
export function materializeCollection(id: string): Promise<Collection> {
  return jsonFetch<Collection>(`${API_BASE}/collections/${encodeURIComponent(id)}/materialize`, {
    method: 'POST',
  });
}

/** One atomic POST for all picked books (1 to 500). 409 for a dynamic collection. */
export function addBooksToCollection(id: string, bookIds: string[]): Promise<Collection> {
  return jsonFetch<Collection>(`${API_BASE}/collections/${encodeURIComponent(id)}/books`, {
    method: 'POST',
    body: JSON.stringify({ book_ids: bookIds }),
  });
}

/** 404 when the book is not a member, 409 for a dynamic collection. */
export function removeBookFromCollection(id: string, bookId: string): Promise<Collection> {
  return jsonFetch<Collection>(
    `${API_BASE}/collections/${encodeURIComponent(id)}/books/${encodeURIComponent(bookId)}`,
    { method: 'DELETE' }
  );
}
