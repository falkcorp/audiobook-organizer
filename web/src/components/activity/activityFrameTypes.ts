// file: web/src/components/activity/activityFrameTypes.ts
// version: 0.1.0
// guid: 3c8e51a7-9d24-4f60-b1a3-72e0d4c6958f
// last-edited: 2026-11-22

// What the Activity frame (ActivityLogFrame.tsx) is handed and what it hands back,
// declared structurally so the frame names no audiobook store, service or route.
// Every type here is a shape the legacy services already return, so
// pages/ActivityLog.tsx binds them without adapters; F9-03 swaps the bindings for
// the shell's native ones.

import type { ReactNode } from 'react';
import type { GroupableOperation } from '@mediacore/shell';

/** One entry of the activity feed (what GET /activity returns per row). */
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
  /** The server stopped at its scan budget: older matches were not searched. */
  partial?: boolean;
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

export interface ActivityRequestOptions {
  signal?: AbortSignal;
  timeoutMs?: number;
}

/** The activity feed endpoints. */
export interface ActivityFeedService {
  fetch(filter?: ActivityFilter, options?: ActivityRequestOptions): Promise<ActivityResponse>;
  fetchSources(
    filter?: Partial<ActivityFilter>,
    options?: ActivityRequestOptions
  ): Promise<SourcesResponse>;
  /** Starts the compaction run; the work is watched in the operations list. */
  compact(olderThanDays: number): Promise<{ operation_id: string }>;
}

/**
 * A row of the operations panel: the grouping row shape plus the two fields the
 * panel reads beyond it. ABO's ActiveOperation and the shell's OperationRow both fit.
 */
export interface ActivityOpRow extends GroupableOperation {
  current_item?: string | null;
  /** The row is waiting for a decision and can be finished without resuming. */
  canFinish?: boolean;
}

/** A log line pushed for a run (the live tail of an open row). */
export interface ActivityLogEvent {
  op_id: string;
  message: string;
}

/**
 * What the page reads from the operations store, as one hook's result. `raw` is
 * the whole window ungrouped (every count comes from it), `grouped` is the same
 * window with runs folded (it is only ever rendered), `live` is the subset still
 * running, `loadError` is set when the last refresh failed, and `reload` must keep
 * one identity across renders: it is an effect dependency.
 */
export interface ActivityOpsFeed {
  grouped: readonly ActivityOpRow[];
  raw: readonly ActivityOpRow[];
  live: readonly ActivityOpRow[];
  loadError: string | null;
  latestLogEvent: ActivityLogEvent | null;
  reload: () => unknown;
}

/** The server's answer to Retry: resumed in place, or requeued as a new run. */
export interface ActivityRetryResult {
  id: string;
  mode?: string;
}

/** The per-row actions of the operations panel. */
export interface ActivityOperationActions {
  cancel(id: string): Promise<unknown>;
  discard(id: string): Promise<unknown>;
  clearStale(): Promise<unknown>;
  retry(id: string): Promise<ActivityRetryResult>;
  finish(id: string): Promise<unknown>;
  /** The log lines of a run, oldest first. */
  logs(id: string): Promise<Array<{ message?: string }>>;
}

/** What a run's status means. */
export interface ActivityStatusModel {
  isTerminal(status: string): boolean;
  isInterrupted(status: string): boolean;
  isRetryable(status: string): boolean;
}

/** How a tag renders as a chip. */
export interface ActivityTagChip {
  color: 'default' | 'primary' | 'secondary' | 'error' | 'info' | 'success' | 'warning';
  sx?: Record<string, unknown>;
  label: string;
}

/** The undo path of a feed entry. */
export interface ActivityUndoBinding {
  /** True when this kind of entry can be undone; the frame has already checked that it names an operation. */
  offers(entry: ActivityEntry): boolean;
  /** Asks the server what an undo would do; `message` is shown either way. */
  preflight(operationId: string): Promise<{ canUndo: boolean; message: string }>;
  /** True when the error means the server refuses this caller every time. */
  isRefused(error: unknown): boolean;
  /** Runs the undo and returns what to tell the user. */
  revert(operationId: string): Promise<string>;
}

/** A prop-less pane the page shows above the operations panel. */
export interface ActivityPane {
  id: string;
  /** The tab title it takes when the shell's Activity page lists panes as tabs. */
  label: string;
  Component: () => ReactNode;
}

/** Everything the frame needs from its host. Pass one module-level object: identity matters. */
export interface ActivityBindings {
  useFeed: () => ActivityOpsFeed;
  activity: ActivityFeedService;
  operations: ActivityOperationActions;
  status: ActivityStatusModel;
  undo: ActivityUndoBinding;
  /** A run's display name. */
  nameOf: (op: ActivityOpRow) => string;
  tagChip: (tag: string) => ActivityTagChip;
  /** The event types the Type filter offers and `?type=` accepts. */
  eventTypes: readonly string[];
  /** Where a feed row's book lives. */
  bookHref: (bookId: string) => string;
  /** How a batched feed entry renders. */
  renderBatchedEntry: (entry: ActivityEntry, tierColor: string) => ReactNode;
  /** Extra detail under an open run (the trash-cleanup result); null for none. */
  renderOpDetails?: (op: ActivityOpRow) => ReactNode;
  /** Prop-less panes shown above the operations panel, in order. */
  panes: readonly ActivityPane[];
}
