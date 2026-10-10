// file: web/src/services/playlistApi.ts
// version: 2.1.0
// guid: 8d6e7f2a-9b0c-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-10-10

import { apiFetch } from '../utils/apiFetch';

const API_BASE = '/api/v1';

export interface UserPlaylist {
  id: string;
  name: string;
  description?: string;
  type: 'static' | 'smart';
  book_ids?: string[];
  query?: string;
  sort_json?: string;
  limit?: number;
  materialized_book_ids?: string[];
  itunes_persistent_id?: string;
  created_at: string;
  updated_at: string;
  created_by_user_id?: string;
  dirty: boolean;
  version: number;
}

async function jsonFetch(url: string, opts?: RequestInit) {
  const resp = await apiFetch(url, {
    headers: { 'Content-Type': 'application/json', ...opts?.headers },
    ...opts,
  });
  if (!resp.ok) {
    const body = await resp.json().catch(() => ({}));
    throw Object.assign(new Error(body.error || resp.statusText), { response: { data: body, status: resp.status } });
  }
  const body = await resp.json();
  return body.data || body;
}

export async function listPlaylists(
  type?: 'static' | 'smart',
  limit = 50,
  offset = 0
): Promise<{ playlists: UserPlaylist[]; count: number }> {
  const params = new URLSearchParams({ limit: String(limit), offset: String(offset) });
  if (type) params.set('type', type);
  const data = await jsonFetch(`${API_BASE}/playlists?${params}`);
  return { playlists: data.items || [], count: data.count };
}

export async function getPlaylist(id: string): Promise<{ playlist: UserPlaylist; book_ids: string[] }> {
  return jsonFetch(`${API_BASE}/playlists/${id}`);
}

export async function createPlaylist(data: {
  name: string;
  type: 'static' | 'smart';
  description?: string;
  book_ids?: string[];
  query?: string;
  sort_json?: string;
  limit?: number;
}): Promise<UserPlaylist> {
  const resp = await jsonFetch(`${API_BASE}/playlists`, {
    method: 'POST',
    body: JSON.stringify(data),
  });
  return resp.playlist;
}

export async function updatePlaylist(
  id: string,
  data: Partial<Pick<UserPlaylist, 'name' | 'description' | 'book_ids' | 'query' | 'sort_json' | 'limit'>>
): Promise<UserPlaylist> {
  const resp = await jsonFetch(`${API_BASE}/playlists/${id}`, {
    method: 'PUT',
    body: JSON.stringify(data),
  });
  return resp.playlist;
}

export async function deletePlaylist(id: string): Promise<void> {
  await jsonFetch(`${API_BASE}/playlists/${id}`, { method: 'DELETE' });
}

export async function addBooksToPlaylist(id: string, bookIds: string[]): Promise<UserPlaylist> {
  const resp = await jsonFetch(`${API_BASE}/playlists/${id}/books`, {
    method: 'POST',
    body: JSON.stringify({ book_ids: bookIds }),
  });
  return resp.playlist;
}

export async function removeBookFromPlaylist(id: string, bookId: string): Promise<UserPlaylist> {
  const resp = await jsonFetch(`${API_BASE}/playlists/${id}/books/${bookId}`, { method: 'DELETE' });
  return resp.playlist;
}

export async function materializePlaylist(id: string): Promise<UserPlaylist> {
  const resp = await jsonFetch(`${API_BASE}/playlists/${id}/materialize`, { method: 'POST' });
  return resp.playlist;
}
