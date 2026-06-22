// file: web/src/services/readingApi.ts
// version: 1.3.0
// guid: 6b4c5d0e-7f8a-4a70-b8c5-3d7e0f1b9a99
// last-edited: 2026-06-22

import { apiFetch } from '../utils/apiFetch';

const API_BASE = '/api/v1';

export interface UserPosition {
  user_id: string;
  book_id: string;
  segment_id: string;
  position_seconds: number;
  updated_at: string;
}

export interface UserBookState {
  user_id: string;
  book_id: string;
  status: string;
  status_manual: boolean;
  last_activity_at: string;
  last_segment_id?: string;
  total_listened_seconds?: number;
  progress_pct: number;
  updated_at: string;
}

export type ReadStatus = 'unstarted' | 'in_progress' | 'finished' | 'abandoned';

export const READ_STATUS_LABELS: Record<ReadStatus, string> = {
  unstarted: 'Unstarted',
  in_progress: 'In Progress',
  finished: 'Finished',
  abandoned: 'Abandoned',
};

export const READ_STATUS_COLORS: Record<ReadStatus, string> = {
  unstarted: '#9e9e9e',
  in_progress: '#2196f3',
  finished: '#4caf50',
  abandoned: '#ff9800',
};

export async function getBookState(bookId: string): Promise<UserBookState | null> {
  const resp = await apiFetch(`${API_BASE}/books/${bookId}/state`);
  if (!resp.ok) return null;
  const body = await resp.json();
  return body?.data ?? null;
}

export async function getBookPosition(bookId: string): Promise<UserPosition | null> {
  const resp = await apiFetch(`${API_BASE}/books/${bookId}/position`);
  if (!resp.ok) return null;
  const body = await resp.json();
  return body?.data ?? null;
}

export async function setBookPosition(
  bookId: string,
  segmentId: string,
  positionSeconds: number
): Promise<UserBookState> {
  const resp = await apiFetch(`${API_BASE}/books/${bookId}/position`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ segment_id: segmentId, position_seconds: positionSeconds }),
  });
  const body = await resp.json();
  return body.data;
}

export async function setBookStatus(
  bookId: string,
  status: ReadStatus
): Promise<UserBookState> {
  const resp = await apiFetch(`${API_BASE}/books/${bookId}/status`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ status }),
  });
  const body = await resp.json();
  return body.data;
}

export async function clearBookStatus(bookId: string): Promise<UserBookState | null> {
  const resp = await apiFetch(`${API_BASE}/books/${bookId}/status`, { method: 'DELETE' });
  if (!resp.ok) return null;
  const body = await resp.json();
  return body?.data ?? null;
}

export async function listByStatus(
  status: ReadStatus,
  limit = 50,
  offset = 0
): Promise<{ states: UserBookState[]; count: number }> {
  const resp = await apiFetch(`${API_BASE}/me/${status}?limit=${limit}&offset=${offset}`);
  const body = await resp.json();
  return body.data;
}
