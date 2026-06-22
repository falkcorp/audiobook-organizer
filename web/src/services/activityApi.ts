// file: web/src/services/activityApi.ts
// version: 2.4.0
// last-edited: 2026-06-22
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890

import { apiFetch } from '../utils/apiFetch';

const API_BASE = import.meta.env.VITE_API_URL || '/api/v1';

export interface ActivityEntry {
  id: string;
  timestamp: string;
  tier: 'audit' | 'change' | 'debug' | 'digest';
  type: string;
  level: string;
  source: string;
  operation_id?: string;
  book_id?: string;
  summary: string;
  details?: Record<string, unknown>;
  tags?: string[];
  pruned_at?: string;
}

export interface ActivityResponse {
  entries: ActivityEntry[];
  total: number;
}

export interface ActivityFilter {
  limit?: number;
  offset?: number;
  type?: string;
  tier?: string;
  level?: string;
  operation_id?: string;
  book_id?: string;
  since?: string;
  until?: string;
  tags?: string;
  search?: string;
  source?: string;
  exclude_sources?: string;
  exclude_tiers?: string;
  exclude_tags?: string;
}

export interface SourceCount {
  source: string;
  count: number;
}

export interface SourcesResponse {
  sources: SourceCount[];
}

export async function fetchActivity(filter?: ActivityFilter): Promise<ActivityResponse> {
  const params = new URLSearchParams();
  if (filter) {
    if (filter.limit !== undefined) params.set('limit', String(filter.limit));
    if (filter.offset !== undefined) params.set('offset', String(filter.offset));
    if (filter.type) params.set('type', filter.type);
    if (filter.tier) params.set('tier', filter.tier);
    if (filter.level) params.set('level', filter.level);
    if (filter.operation_id) params.set('operation_id', filter.operation_id);
    if (filter.book_id) params.set('book_id', filter.book_id);
    if (filter.since) params.set('since', filter.since);
    if (filter.until) params.set('until', filter.until);
    if (filter.tags) params.set('tags', filter.tags);
    if (filter.search) params.set('search', filter.search);
    if (filter.source) params.set('source', filter.source);
    if (filter.exclude_sources) params.set('exclude_sources', filter.exclude_sources);
    if (filter.exclude_tiers) params.set('exclude_tiers', filter.exclude_tiers);
    if (filter.exclude_tags) params.set('exclude_tags', filter.exclude_tags);
  }
  const query = params.toString();
  const response = await apiFetch(`${API_BASE}/activity${query ? `?${query}` : ''}`);
  if (!response.ok) {
    throw new Error(`Failed to fetch activity: ${response.status}`);
  }
  const body = await response.json();
  return body.data;
}

export async function fetchActivitySources(filter: Partial<ActivityFilter> = {}): Promise<SourcesResponse> {
  const params = new URLSearchParams();
  if (filter.tier) params.set('tier', filter.tier);
  if (filter.level) params.set('level', filter.level);
  if (filter.since) params.set('since', filter.since);
  if (filter.until) params.set('until', filter.until);
  const url = `${API_BASE}/activity/sources?${params.toString()}`;
  const res = await apiFetch(url);
  if (!res.ok) throw new Error(`Sources API error: ${res.status}`);
  const body = await res.json();
  return body.data;
}

export interface CompactResult {
  days_compacted: number;
  entries_deleted: number;
}

/** Single entry in the per-operation activity stream emitted by
 *  `GET /api/v1/operations/:id/activity`. Distinct from the global
 *  `ActivityEntry` because the backend route shapes a leaner payload
 *  scoped to one op. */
export interface OperationActivityEntry {
  timestamp: string;
  level: 'info' | 'warn' | 'error' | string;
  operation_id: string;
  operation_type: string;
  message: string;
  details?: string;
  tags?: string[];
}

export interface OperationActivityResponse {
  operation_id: string;
  entries: OperationActivityEntry[];
  total: number;
}

export async function fetchOperationActivity(
  opID: string,
  limit?: number,
): Promise<OperationActivityResponse> {
  const params = new URLSearchParams();
  if (limit !== undefined) params.set('limit', String(limit));
  const query = params.toString();
  const url = `${API_BASE}/operations/${encodeURIComponent(opID)}/activity${query ? `?${query}` : ''}`;
  const response = await apiFetch(url);
  if (!response.ok) {
    throw new Error(`Failed to fetch operation activity: ${response.status}`);
  }
  const body = await response.json();
  // The standard envelope wraps data in `data`; fall back to the raw body
  // for robustness in case the envelope is dropped (matches the pattern in
  // getOperationLogs).
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const data = (body as any).data ?? body;
  return data as OperationActivityResponse;
}

export async function compactActivityLog(olderThanDays: number): Promise<CompactResult> {
  const response = await apiFetch(`${API_BASE}/activity/compact`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ older_than_days: olderThanDays }),
  });
  if (!response.ok) {
    throw new Error(`Failed to compact activity log: ${response.status}`);
  }
  const body = await response.json();
  return body.data;
}
