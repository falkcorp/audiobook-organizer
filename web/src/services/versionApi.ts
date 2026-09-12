// file: web/src/services/versionApi.ts
// version: 1.4.0
// guid: 9e7f8a3b-0c1d-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-09-12

import type { RevertOperationResult } from './api';

const API_BASE = '/api/v1';

export interface BookVersion {
  id: string;
  book_id: string;
  status: string;
  format: string;
  source: string;
  source_original_path?: string;
  torrent_hash?: string;
  ingest_date: string;
  purged_date?: string;
  created_at: string;
  updated_at: string;
  version: number;
}

export interface UndoConflictReport {
  total_changes: number;
  already_reverted: number;
  content_changed: Array<{ change_id: string; book_id: string; reason: string }>;
  book_deleted: Array<{ change_id: string; book_id: string; reason: string }>;
  re_organized: Array<{ change_id: string; book_id: string; reason: string }>;
  /** series_id / series_rename rows whose series is gone or unreadable; the revert refuses each. */
  series_deleted?: Array<{ change_id: string; book_id: string; reason: string }>;
  /** series_rename rows whose series was renamed again after the operation. */
  series_renamed_since?: Array<{ change_id: string; book_id: string; reason: string }>;
  /** series_rename rows whose old name now belongs to another series. */
  series_name_taken?: Array<{ change_id: string; book_id: string; reason: string }>;
  safe: number;
  /** Rows the revert endpoint cannot reverse; in no other bucket. */
  not_restorable?: number;
  /** not_restorable by label: a change type, or "metadata_update:<field>". */
  not_restorable_types?: Record<string, number>;
}

async function jsonFetch(url: string, opts?: RequestInit) {
  const resp = await fetch(url, {
    headers: { 'Content-Type': 'application/json', ...opts?.headers },
    ...opts,
  });
  if (!resp.ok) {
    const body = await resp.json().catch(() => ({}));
    throw Object.assign(new Error(body.error || resp.statusText), { response: { data: body, status: resp.status } });
  }
  return resp.json();
}

export async function trashVersion(bookId: string, versionId: string): Promise<BookVersion> {
  const resp = await jsonFetch(`${API_BASE}/books/${bookId}/versions/${versionId}`, { method: 'DELETE' });
  return resp.version;
}

export async function restoreVersion(bookId: string, versionId: string): Promise<BookVersion> {
  const resp = await jsonFetch(`${API_BASE}/books/${bookId}/versions/${versionId}/restore`, { method: 'POST' });
  return resp.version;
}

export async function purgeVersion(bookId: string, versionId: string): Promise<BookVersion> {
  const resp = await jsonFetch(`${API_BASE}/books/${bookId}/versions/${versionId}/purge-now`, { method: 'POST' });
  return resp.version;
}

export async function hardDeleteVersion(versionId: string): Promise<void> {
  await jsonFetch(`${API_BASE}/purged-versions/${versionId}`, { method: 'DELETE' });
}

export async function getUndoPreflight(operationId: string): Promise<UndoConflictReport> {
  return jsonFetch(`${API_BASE}/operations/${operationId}/undo/preflight`);
}

export async function revertOperation(operationId: string): Promise<RevertOperationResult> {
  const body = await jsonFetch(`${API_BASE}/operations/${operationId}/revert`, { method: 'POST' });
  return (body?.data ?? body) as RevertOperationResult;
}
