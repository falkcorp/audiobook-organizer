// file: web/src/services/coreEvents/index.ts
// version: 0.1.0
// guid: 7d837d93-c9ea-4dfa-ad43-2cdba85eb90a
// last-edited: 2026-11-19

// Public surface of the core event-stream client (F6-03 SPA pre-slice).
// UNWIRED: no running code imports this until the F6 flip replaces eventSourceManager.

export { reconnectDelay, parseRetryAfter, BASE_DELAY_MS, MAX_DELAY_MS } from './backoff';
export { buildStreamUrl, EVENTS_PATH } from './connection';
export {
  createEventStream,
  getEventStream,
  resetSharedEventStream,
  DEFAULT_RESYNC_COALESCE_MS,
  DEFAULT_RESYNC_MIN_INTERVAL_MS,
} from './manager';
export type { EventStream, EventStreamOptions, ResyncOptions, SubscribeOptions, VisibilitySource } from './manager';
export { useResync, useStreamClosed, useStreamStatus, useTopic } from './hooks';
export type { UseResyncOptions, UseTopicOptions } from './hooks';
export {
  BOOK_CHANGED_TOPIC,
  BOOK_CHANGE_MAX_IDS,
  BOOK_CHANGE_WINDOW_MS,
  createBookChangeBatcher,
  parseBookChange,
  subscribeBookChanges,
  useBookChanges,
} from './bookChanges';
export type {
  BookChangeBatch,
  BookChangeBatchHandler,
  BookChangeBatcher,
  BookChangeBatcherOptions,
  BookChangeSubscription,
  BookChangeSubscriptionOptions,
  UseBookChangesOptions,
  UseBookChangesResult,
} from './bookChanges';
export { useServerShutdown, watchServerShutdown } from './shutdown';
export type { ServerShutdownInfo, ServerShutdownListener } from './shutdown';
export { createSseParser } from './sseParser';
export type { SseFrame, SseParser } from './sseParser';
export * from './types';
