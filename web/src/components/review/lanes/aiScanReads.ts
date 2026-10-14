// file: web/src/components/review/lanes/aiScanReads.ts
// version: 1.1.0
// guid: 5c2e8a14-b7d3-4f69-91a0-3e6d84c2f7b5
// last-edited: 2026-10-13
//
// The AI scans sub-view's reads, each taking an AbortSignal and a deadline.
//
// WHY THESE ARE NOT THE api.ts WRAPPERS
//
// `api.listAIScans` and `api.getAIScanResults` take no signal, so leaving the
// sub-view (or opening another scan) would leave the request running, and a slow
// server would be an infinite spinner. api.ts is a shared hotspot that this change
// is told not to touch, so the requests are made here with `apiFetch`, the same
// thing the wrappers call: the same reason, and the same follow-up, as
// `authorsReads.ts` (add `opts?: { signal }` to the wrappers, then delete these).
// The wire shapes below are copied from the wrappers and must move together.
//
// The writes (start, apply, cancel) DO use the api.ts wrappers: a write that has
// been sent must not be abandoned half-way by a re-render.

import type { AIScan, AIScanResult } from '../../../services/api';
import { apiFetch } from '../../../utils/apiFetch';

const API_BASE = '/api/v1';

/** A read that has not answered by now is an error, not a spinner forever. */
export const AI_SCAN_READ_TIMEOUT_MS = 30_000;

async function failure(response: Response, fallback: string): Promise<Error> {
  const body = await response.json().catch(() => ({}));
  const message = typeof (body as { error?: unknown }).error === 'string' ? (body as { error: string }).error : '';
  return new Error(message || `${fallback} (HTTP ${response.status})`);
}

/** GET /ai/scans: every scan, nightly ones included. */
export async function fetchAIScans(signal: AbortSignal): Promise<AIScan[]> {
  const response = await apiFetch(`${API_BASE}/ai/scans`, { signal, timeoutMs: AI_SCAN_READ_TIMEOUT_MS });
  if (!response.ok) throw await failure(response, 'Failed to list AI scans');
  const data = (await response.json()).data ?? {};
  return data.scans ?? [];
}

/** One scan's results, and the name of every author they name (by id; a merged-away author is absent). */
export interface AIScanResultsRead {
  results: AIScanResult[];
  authorNames: Record<number, string>;
}

/** GET /ai/scans/:id/results: every result of one scan. Filtering by agreement is the caller's. */
export async function fetchAIScanResults(id: number, signal: AbortSignal): Promise<AIScanResultsRead> {
  const response = await apiFetch(`${API_BASE}/ai/scans/${id}/results`, {
    signal,
    timeoutMs: AI_SCAN_READ_TIMEOUT_MS,
  });
  if (!response.ok) throw await failure(response, 'Failed to get scan results');
  const data = (await response.json()).data ?? {};
  const authorNames: Record<number, string> = {};
  for (const [key, name] of Object.entries((data.author_names ?? {}) as Record<string, string>)) {
    authorNames[Number(key)] = name;
  }
  return { results: data.results ?? [], authorNames };
}
