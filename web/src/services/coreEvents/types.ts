// file: web/src/services/coreEvents/types.ts
// version: 0.1.0
// guid: 655f566c-d8fd-4dde-a944-37be157d6cc6
// last-edited: 2026-11-19

// Types of the core event-stream client: the wire envelope, the topic catalog the SPA
// consumes, the connection status and the refusal errors. Wire facts come from
// core/realtime and core/internal/sse (05-realtime 4.1, 4.2, 4.7, 6).
//
// UNWIRED: nothing in the running app imports this module until the F6 flip.

/** The envelope every frame's `data:` carries (core/internal/sse/serve.go controlFrame). */
export interface StreamEnvelope<T = unknown> {
  topic: string;
  ts: string;
  /** The entity the event is about (a book ID, a run ID); absent or empty for none. */
  key?: string | null;
  data: T;
}

/** Control topics: id-less frames the server writes to every subscriber (serve.go). */
export const TOPIC_OPENED = 'core.stream.opened.v1';
export const TOPIC_RESET = 'core.stream.reset.v1';
export const TOPIC_CLOSED = 'core.stream.closed.v1';

/** Why the server says it reset the stream: restart, gap or overflow (05-realtime 4.2). */
/** 'reconnect' is raised by the client itself: a (re)open that had no Last-Event-ID to resume from, or a reopen after the tab was hidden. */
export type ResetReason = 'restart' | 'gap' | 'overflow' | 'reconnect' | (string & {});
/** Why the server closed the stream (05-realtime 4.2). 'shutdown' means show the backdrop. */
export type ClosedReason =
  | 'shutdown'
  | 'credential_revoked'
  | 'permission_revoked'
  | (string & {});

export type BookChangeKind = 'created' | 'updated' | 'deleted';

/**
 * Payloads of the topics the SPA reads, by topic name. A topic not listed here is
 * still subscribable and its payload is `unknown`.
 *
 * Shape provenance (be aware before trusting a field):
 *  - books.book.changed.v1 is specified: `{kind, id}`, Key = book ID (05-realtime 4.7).
 *  - core.task.run-changed.v1 is tasks.RunView (05-tasks 3). The declaring topic and its
 *    publisher land with F5-18, so the JSON field names below are the spec's, not
 *    observed. The index signature keeps an extra field from being a type error.
 *  - core.task.log-appended.v1, core.log.recorded.v1, books.book.relocated.v1 and
 *    core.system.status-changed.v1 have no declared payload shape yet: they are
 *    `Record<string, unknown>` until their owners land, and the flip PR tightens them.
 */
export interface TopicPayloads {
  'books.book.changed.v1': { kind: BookChangeKind; id: string };
  'books.book.relocated.v1': Record<string, unknown>;
  'core.task.run-changed.v1': {
    id: string;
    kind?: string;
    state?: string;
    done?: number;
    total?: number;
    current?: string;
    [field: string]: unknown;
  };
  'core.task.log-appended.v1': Record<string, unknown>;
  'core.task.finished.v1': Record<string, unknown>;
  'core.log.recorded.v1': Record<string, unknown>;
  'core.system.status-changed.v1': Record<string, unknown>;
}

export type KnownTopic = keyof TopicPayloads;

/** Payload type for a topic name; unknown topics and patterns give `unknown`. */
export type PayloadOf<T extends string> = T extends KnownTopic ? TopicPayloads[T] : unknown;

/** What a topic handler receives. */
export interface TopicEvent<T = unknown> {
  topic: string;
  ts: string;
  key: string;
  data: T;
  /** The `id:` of the frame ('<bootNonce>-<seq>'), the resume point. */
  id: string;
}

export type TopicHandler<T = unknown> = (event: TopicEvent<T>) => void;

/** Passed to resync handlers on core.stream.reset.v1: refetch over REST. */
export interface ResyncInfo {
  reason: ResetReason;
  /** The stream's server-side key ('' for the shared stream). */
  streamKey: string;
}

export type ResyncHandler = (info: ResyncInfo) => void;

export type StreamState =
  /** No subscriber needs this stream. */
  | 'idle'
  /** The tab is hidden: the stream is closed and reopens on visibility. */
  | 'hidden'
  | 'connecting'
  | 'open'
  /** Waiting out a backoff after a refusal, a drop or a closed frame. */
  | 'reconnecting';

export interface StreamStatus {
  state: StreamState;
  /** Consecutive failed attempts since the last good open (0 when open). */
  attempt: number;
  /** For 'reconnecting': the wait before the next attempt. */
  delayMs?: number;
  /** For 'reconnecting': what ended the last attempt. Never swallowed. */
  error?: unknown;
  /** The reason from the last core.stream.closed.v1, until the next open. */
  closedReason?: ClosedReason;
  /** The server stream key this status is for ('' = the shared stream). */
  streamKey: string;
}

export type StatusListener = (status: StreamStatus) => void;
export type ClosedListener = (info: { reason: ClosedReason; streamKey: string }) => void;

/**
 * The server refused the stream before its first byte (problem+json: 400, 401, 403,
 * 429, 503). Carries what the UI needs to say why, and the Retry-After the backoff
 * honours.
 */
export class StreamRefusedError extends Error {
  readonly status: number;
  /** The problem's machine code ('' when the body carried none). */
  readonly code: string;
  /** Retry-After in ms, or null when the header is absent or unreadable. */
  readonly retryAfterMs: number | null;
  readonly detail: string;

  constructor(status: number, code: string, detail: string, retryAfterMs: number | null) {
    super(`event stream refused: HTTP ${status}${code ? ` ${code}` : ''}${detail ? `: ${detail}` : ''}`);
    this.name = 'StreamRefusedError';
    this.status = status;
    this.code = code;
    this.detail = detail;
    this.retryAfterMs = retryAfterMs;
  }
}

/** A 2xx answer that is not a text/event-stream, or has no body, or went silent. */
export class StreamProtocolError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'StreamProtocolError';
  }
}

/** True when `topic` matches `pattern`: exact, `module.*`, or `*` (05-realtime 3). */
export function topicMatches(pattern: string, topic: string): boolean {
  if (pattern === '*' || pattern === topic) return true;
  if (pattern.endsWith('.*')) return topic.startsWith(pattern.slice(0, -1));
  return false;
}
