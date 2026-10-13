// file: web/src/components/review/lanes/authorsReads.ts
// version: 1.0.0
// guid: 7b31c5e8-92a4-4d6f-b0e3-5f18a96d2c70
// last-edited: 2026-10-13
//
// The Authors lane's reads, each taking an AbortSignal and a deadline.
//
// WHY THESE ARE NOT THE api.ts WRAPPERS
//
// `api.getAuthorDuplicates`, `api.getBooksByAuthor` and `api.validateDedupEntry`
// take no signal, so a caller cannot cancel them: leaving the lane (or opening
// another author's popover) would leave the request running, and the lane's
// "no author requests while another lane is active" rule would be a promise the
// wrapper cannot keep. api.ts is a shared hotspot that this change is told not
// to touch, so the three requests are made here with `apiFetch`, the same thing
// the wrappers call. Once api.ts is free, adding `opts?: { signal }` to those
// three and deleting this file's copies is the follow-up; the wire shapes below
// are copied from them and must move together.
//
// The writes (merge, rename, split, reclassify, resolve-production, scan) DO use
// the api.ts wrappers: a write that has been sent must not be abandoned
// half-way by a re-render, and `pollOperationV2` already takes a signal for the
// wait that follows.

import type { AuthorDedupGroup, Book, ValidationResult } from '../../../services/api';
import { apiFetch } from '../../../utils/apiFetch';

const API_BASE = '/api/v1';

/** A read that has not answered by now is an error, not a spinner forever. */
export const AUTHORS_READ_TIMEOUT_MS = 30_000;

/** Page size for the books read: the server's per-request cap. */
const BOOKS_PAGE_SIZE = 1000;
/** Hard stop so a server that misreports `count` cannot spin the loop forever. */
const BOOKS_MAX_PAGES = 100;

async function failure(response: Response, fallback: string): Promise<Error> {
  const body = await response.json().catch(() => ({}));
  const message = typeof (body as { error?: unknown }).error === 'string' ? (body as { error: string }).error : '';
  return new Error(message || `${fallback} (HTTP ${response.status})`);
}

export interface AuthorGroupsResponse {
  groups: AuthorDedupGroup[];
  /** True when the server has no cached scan: the groups are empty because nobody has scanned. */
  needsRefresh: boolean;
}

/** GET /authors/duplicates. */
export async function fetchAuthorGroups(signal: AbortSignal): Promise<AuthorGroupsResponse> {
  const response = await apiFetch(`${API_BASE}/authors/duplicates`, {
    signal,
    timeoutMs: AUTHORS_READ_TIMEOUT_MS,
  });
  if (!response.ok) throw await failure(response, 'Failed to fetch author duplicates');
  const data = (await response.json()).data ?? {};
  return { groups: data.groups ?? [], needsRefresh: Boolean(data.needs_refresh) };
}

/**
 * Every book credited to one author, page by page.
 *
 * Throws on a failed page rather than returning a partial list: the caller
 * renders this inside a merge decision, where "this author has fewer books"
 * and "the request failed" must never look alike.
 */
export async function fetchBooksByAuthor(authorId: number, signal: AbortSignal): Promise<Book[]> {
  const all: Book[] = [];
  let count: number | undefined;
  for (let page = 0; page < BOOKS_MAX_PAGES; page++) {
    const params = new URLSearchParams({
      author_id: String(authorId),
      limit: String(BOOKS_PAGE_SIZE),
      offset: String(all.length),
    });
    const response = await apiFetch(`${API_BASE}/audiobooks?${params}`, {
      signal,
      timeoutMs: AUTHORS_READ_TIMEOUT_MS,
    });
    if (!response.ok) throw await failure(response, 'Failed to fetch books for author');
    const body = await response.json();
    const data = body.data ?? body;
    const items: Book[] = data.items ?? [];
    all.push(...items);
    if (typeof data.count === 'number') count = data.count;
    if (items.length === 0 || (count !== undefined && all.length >= count)) return all;
  }
  throw new Error(
    `Stopped after ${BOOKS_MAX_PAGES} pages with ${all.length} of ${count ?? 'an unreported number of'} books`
  );
}

/** POST /dedup/validate for an author name: what the metadata providers know under it. */
export async function validateAuthorName(
  query: string,
  signal: AbortSignal
): Promise<{ results: ValidationResult[]; query: string }> {
  const response = await apiFetch(`${API_BASE}/dedup/validate`, {
    method: 'POST',
    body: JSON.stringify({ query, type: 'author' }),
    signal,
    timeoutMs: AUTHORS_READ_TIMEOUT_MS,
  });
  if (!response.ok) throw await failure(response, 'Failed to validate the name');
  const data = (await response.json()).data ?? {};
  return { results: data.results ?? [], query: data.query ?? query };
}

/** GET /authors/:id, for the name only. */
export async function fetchAuthorName(authorId: number, signal: AbortSignal): Promise<string> {
  const response = await apiFetch(`${API_BASE}/authors/${authorId}`, {
    signal,
    timeoutMs: AUTHORS_READ_TIMEOUT_MS,
  });
  if (!response.ok) throw await failure(response, 'Failed to fetch the author');
  const body = await response.json();
  return String(body.data?.author?.name ?? '');
}
