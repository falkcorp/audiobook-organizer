// file: web/src/services/versionApi.ts
// version: 1.11.1
// guid: 9e7f8a3b-0c1d-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-10-28

import type { RevertOperationResult } from './api';
import { apiFetch } from '../utils/apiFetch';
import { unwrapData } from '../utils/apiResponse';

const API_BASE = '/api/v1';

/** One file a purge left on disk: why a purge kept it, or why it could not remove it. */
export interface PurgeFileNote {
  path: string;
  reason: string;
}

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
  /** When the version entered the trash; the 14-day purge window counts from here. Unset outside the trash. */
  trashed_at?: string;
  /** Files a finished purge did NOT remove (another row or book names them, a protected path, a folder that still holds files). */
  purge_kept?: PurgeFileNote[];
  /** Status purge_failed: the files a purge could not remove and why. A retry (purge-now) or a hard delete are the ways out. */
  purge_failed?: PurgeFileNote[];
  purge_failed_at?: string;
  /** Set only on a retired version: the trashed or merged book whose files it holds. */
  retired_from_book_id?: string;
  retire_op_id?: string;
  retired_at?: string;
  created_at: string;
  updated_at: string;
  version: number;
}

/** One retired file of a retired version, as GET /audiobooks/:id/retired-versions lists it. */
export interface RetiredVersionFile {
  id: string;
  file_path: string;
  original_filename?: string;
  format?: string;
  track_number?: number;
  disc_number?: number;
  /** Seconds. */
  duration?: number;
  file_size?: number;
  missing: boolean;
}

/** A retired version the book holds, with its rows. */
export interface RetiredVersion {
  id: string;
  /** The holder: the live book the rows were moved onto (the book being viewed). */
  book_id: string;
  retired_from_book_id?: string;
  retired_from_title?: string;
  /** False when the book it was retired out of has been purged; a restore needs it to exist. */
  shell_exists: boolean;
  /** True when that book still exists and is in the trash, the state a restore needs it in. */
  shell_in_trash: boolean;
  retire_op_id?: string;
  retired_at?: string;
  source?: string;
  source_original_path?: string;
  ingest_date: string;
  file_count: number;
  total_size: number;
  /** Seconds. */
  total_duration: number;
  files: RetiredVersionFile[];
}

export interface UndoConflictReport {
  total_changes: number;
  already_reverted: number;
  content_changed: Array<{ change_id: string; book_id: string; reason: string }>;
  /** Rows whose book is soft-deleted; the revert still restores them. */
  book_deleted: Array<{ change_id: string; book_id: string; reason: string }>;
  re_organized: Array<{ change_id: string; book_id: string; reason: string }>;
  // The revert refuses every row in the five groups below each time it runs; they are not restorable.
  /** Rows whose book no longer exists (hard-deleted). */
  book_missing?: Array<{ change_id: string; book_id: string; reason: string }>;
  /** series_id / series_rename rows whose series no longer exists. */
  series_deleted?: Array<{ change_id: string; book_id: string; reason: string }>;
  /** series_rename rows whose series was renamed again after the operation. */
  series_renamed_since?: Array<{ change_id: string; book_id: string; reason: string }>;
  /** series_rename rows whose old name now belongs to another series. */
  series_name_taken?: Array<{ change_id: string; book_id: string; reason: string }>;
  /** Rows whose book or series could not be read, or whose recorded value is malformed. */
  check_failed?: Array<{ change_id: string; book_id: string; reason: string }>;
  safe: number;
  /** Rows the revert endpoint cannot reverse; in no other bucket. */
  not_restorable?: number;
  /** not_restorable by label: a change type, or "metadata_update:<field>". */
  not_restorable_types?: Record<string, number>;
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
  return resp.json();
}

export async function trashVersion(bookId: string, versionId: string): Promise<BookVersion> {
  const resp = await jsonFetch(`${API_BASE}/audiobooks/${bookId}/versions/${versionId}`, { method: 'DELETE' });
  return resp.version;
}

export async function restoreVersion(bookId: string, versionId: string): Promise<BookVersion> {
  const resp = await jsonFetch(`${API_BASE}/audiobooks/${bookId}/versions/${versionId}/restore`, { method: 'POST' });
  return resp.version;
}

export async function purgeVersion(bookId: string, versionId: string): Promise<BookVersion> {
  const resp = await jsonFetch(`${API_BASE}/audiobooks/${bookId}/versions/${versionId}/purge-now`, { method: 'POST' });
  return resp.version;
}

export async function hardDeleteVersion(versionId: string): Promise<void> {
  await jsonFetch(`${API_BASE}/purged-versions/${versionId}`, { method: 'DELETE' });
}

/** The retired versions bookId holds (files of trashed or merged books moved onto it). */
export async function listRetiredVersions(bookId: string): Promise<RetiredVersion[]> {
  const body = await jsonFetch(`${API_BASE}/audiobooks/${bookId}/retired-versions`);
  return unwrapData<{ versions?: RetiredVersion[] }>(body).versions ?? [];
}

export async function getUndoPreflight(operationId: string): Promise<UndoConflictReport> {
  return jsonFetch(`${API_BASE}/operations/${operationId}/undo/preflight`);
}

export async function revertOperation(operationId: string): Promise<RevertOperationResult> {
  const body = await jsonFetch(`${API_BASE}/operations/${operationId}/revert`, { method: 'POST' });
  return (body?.data ?? body) as RevertOperationResult;
}
