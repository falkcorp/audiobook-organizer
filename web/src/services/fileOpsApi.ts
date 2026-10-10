// file: web/src/services/fileOpsApi.ts
// version: 1.2.0
// guid: 7b3d5a1e-9c2f-4e80-b1a4-3d8c6e0f4a12
// last-edited: 2026-10-10

import { apiFetch } from '../utils/apiFetch';

const API_BASE = import.meta.env.VITE_API_URL || '/api/v1';

export interface PendingFileOp {
  book_id: string;
  op_type: string;
  started_at: string;
  book_title?: string;
}

export interface PendingFileOpsResponse {
  operations: PendingFileOp[];
  count: number;
}

export async function fetchPendingFileOps(): Promise<PendingFileOpsResponse> {
  const res = await apiFetch(`${API_BASE}/file-ops/pending`);
  if (!res.ok) {
    throw new Error(`fetchPendingFileOps failed: ${res.status}`);
  }
  const body = await res.json();
  return body.data;
}
