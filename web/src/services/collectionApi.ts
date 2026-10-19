// file: web/src/services/collectionApi.ts
// version: 1.0.0
// guid: 36e44f0d-8a86-4830-8fd9-c0a4fbabc018
// last-edited: 2026-10-18

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
  materialized_book_ids?: string[];
  created_at: string;
  updated_at: string;
  created_by_user_id?: string;
  version: number;
  /**
   * Set by GET and PUT (never by list) when a dynamic collection was NOT
   * re-evaluated because its owner no longer exists and its query uses a
   * per-user field. The stored members are served unchanged.
   */
  evaluation_blocked?: string;
  evaluation_blocked_message?: string;
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
  /** First active admin only: take over an ownerless per-user collection. */
  adopt_owner?: boolean;
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

/** 409: name in use, version conflict, dynamic collection on a membership call, or owner missing. */
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
