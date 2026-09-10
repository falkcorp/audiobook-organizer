// file: web/src/services/activityApi.ts
// version: 2.7.0
// last-edited: 2026-09-10
// guid: a1b2c3d4-e5f6-7890-abcd-ef1234567890

import { apiFetch } from '../utils/apiFetch';

const API_BASE = import.meta.env.VITE_API_URL || '/api/v1';

/**
 * Deadline for the two activity read endpoints.
 *
 * Why 15s specifically:
 *   - A healthy `/activity` page query answers in well under a second, so 15s
 *     is roughly two orders of magnitude of headroom — it can only fire when
 *     something is genuinely wrong, never on a merely slow day.
 *   - It has to be BELOW the Activity page's idle auto-refresh interval (30s).
 *     The page now skips a poll tick while a request is still outstanding, so a
 *     timeout longer than the interval would mean a single wedged request
 *     silently disables auto-refresh for more than a full cycle.
 *   - The server sets `WriteTimeout: 0`, i.e. nothing on the server side ever
 *     cuts a request off. Before this deadline existed, a query that never
 *     completed left the tab spinning forever while the server kept the work
 *     (and its memory) alive. The client is the only place a bound can be
 *     enforced, so it has to be enforced here.
 */
export const ACTIVITY_REQUEST_TIMEOUT_MS = 15_000;

/** Per-call overrides for the activity read endpoints. */
export interface ActivityRequestOptions {
  /** Caller's abort signal — used to supersede an older in-flight request. */
  signal?: AbortSignal;
  /** Override the default deadline. Pass 0 to disable it. */
  timeoutMs?: number;
}

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

export async function fetchActivity(
  filter?: ActivityFilter,
  options: ActivityRequestOptions = {},
): Promise<ActivityResponse> {
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
  const response = await apiFetch(`${API_BASE}/activity${query ? `?${query}` : ''}`, {
    signal: options.signal,
    timeoutMs: options.timeoutMs ?? ACTIVITY_REQUEST_TIMEOUT_MS,
  });
  if (!response.ok) {
    throw new Error(`Failed to fetch activity: ${response.status}`);
  }
  const body = await response.json();
  return body.data;
}

export async function fetchActivitySources(
  filter: Partial<ActivityFilter> = {},
  options: ActivityRequestOptions = {},
): Promise<SourcesResponse> {
  const params = new URLSearchParams();
  if (filter.tier) params.set('tier', filter.tier);
  if (filter.level) params.set('level', filter.level);
  if (filter.since) params.set('since', filter.since);
  if (filter.until) params.set('until', filter.until);
  const url = `${API_BASE}/activity/sources?${params.toString()}`;
  // Same deadline as the feed: /activity/sources aggregates counts over the
  // same unbounded range and is polled on the same tick, so it can wedge in
  // exactly the same way.
  const res = await apiFetch(url, {
    signal: options.signal,
    timeoutMs: options.timeoutMs ?? ACTIVITY_REQUEST_TIMEOUT_MS,
  });
  if (!res.ok) throw new Error(`Sources API error: ${res.status}`);
  const body = await res.json();
  return body.data;
}

/**
 * Response of POST /activity/compact since 2026-09-10: the endpoint no longer
 * compacts inside the request (which timed out on a production-sized log) but
 * enqueues the `maintenance.compact-activity-log` operation and hands back its
 * id. The counters are read from that operation's log and result.
 */
export interface CompactStarted {
  operation_id: string;
  def_id: string;
  status: string;
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

/** Response of `POST /api/v1/operations/activity/merged`: one chronological
 *  timeline over a GROUP of operations. `total` is the sum of the members'
 *  own totals (what exists), `truncated` says the server dropped the oldest
 *  entries to honour `limit`. */
export interface MergedOperationActivityResponse {
  operation_ids: string[];
  entries: OperationActivityEntry[];
  total: number;
  truncated: boolean;
}

/**
 * fetchMergedOperationActivity reads the merged transcript of several
 * operations — the bell's synthetic group rows (operationGrouping.ts) have no
 * server record of their own, so opening one means asking for its members.
 * POST only because a group can hold several hundred ids; it writes nothing.
 */
export async function fetchMergedOperationActivity(
  opIDs: string[],
  limit?: number,
): Promise<MergedOperationActivityResponse> {
  const response = await apiFetch(`${API_BASE}/operations/activity/merged`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(limit !== undefined ? { ids: opIDs, limit } : { ids: opIDs }),
  });
  if (!response.ok) {
    throw new Error(`Failed to fetch merged operation activity: ${response.status}`);
  }
  const body = await response.json();
  // eslint-disable-next-line @typescript-eslint/no-explicit-any
  const data = (body as any).data ?? body;
  return data as MergedOperationActivityResponse;
}

export async function compactActivityLog(olderThanDays: number): Promise<CompactStarted> {
  const response = await apiFetch(`${API_BASE}/activity/compact`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ older_than_days: olderThanDays }),
  });
  if (!response.ok) {
    // A 409 carries the id of the compaction already running; surface the
    // server's message so the user sees that rather than a bare status code.
    let detail = '';
    try {
      const errBody = await response.json();
      detail = errBody?.error?.message ?? errBody?.message ?? errBody?.error ?? '';
    } catch {
      /* non-JSON error body */
    }
    throw new Error(
      detail
        ? `Failed to start activity compaction: ${detail}`
        : `Failed to start activity compaction: ${response.status}`
    );
  }
  const body = await response.json();
  return body.data as CompactStarted;
}
