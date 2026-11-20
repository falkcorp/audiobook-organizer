// file: core/web/packages/shell/src/realtime/connection.ts
// version: 0.1.0
// guid: d7144c27-ea8a-4c37-8ba2-0e69dce84bfc
// last-edited: 2026-11-20

// One fetch()-based SSE connection with resume and backoff (07 §7.9.2,
// 05-realtime §4.8 items 1-6). It knows nothing about tabs: the client decides
// whether a tab may run one (leader election) and what to do with the frames.

import { ApiAuthRedirectError } from '@mediacore/api-client';

import type { CloseReason, RealtimeEvent, ResetReason } from '../contracts';
import { backoffDelay, retryAfterMs } from './backoff';
import { createSseParser, type SseMessage } from './sse';

/** The reserved control topics of 05-realtime §4.2 (never in the events catalog). */
export const CONTROL_OPENED = 'core.stream.opened.v1';
export const CONTROL_RESET = 'core.stream.reset.v1';
export const CONTROL_CLOSED = 'core.stream.closed.v1';

const RESET_REASONS: readonly ResetReason[] = ['restart', 'gap', 'overflow'];
const CLOSE_REASONS: readonly CloseReason[] = ['shutdown', 'credential_revoked', 'permission_revoked'];

export interface ConnectionDeps {
  url: string;
  fetch: typeof fetch;
  setTimeout(fn: () => void, ms: number): unknown;
  clearTimeout(handle: unknown): void;
  random(): number;
  now(): number;
}

export interface ConnectionHandlers {
  /** A catalog event. `id` is the frame's `id:` line, which moved the resume point. */
  onEvent(event: RealtimeEvent, id: string | undefined): void;
  onReset(reason: ResetReason): void;
  /** `credential_revoked` and `permission_revoked` also stop the connection for good. */
  onClosed(reason: CloseReason): void;
  /** A core.stream.opened.v1 frame: the server accepted this connection. */
  onOpened(): void;
  /** A transport failure: an HTTP status, or undefined for a network or protocol failure. */
  onError(status?: number): void;
  /** An opaque redirect: an SSO bounce (05-realtime §4.8 item 4). */
  onAuthRedirect(error: ApiAuthRedirectError): void;
}

function reasonOf<T extends string>(data: unknown, allowed: readonly T[], fallback: T): T {
  const reason = (data as { reason?: unknown } | null | undefined)?.reason;
  return allowed.find((r) => r === reason) ?? fallback;
}

export class StreamConnection {
  /** The resume point: sent as `Last-Event-ID` on every connect that has one. */
  lastEventId: string | undefined;

  private running = false;
  private abort: AbortController | null = null;
  private reader: ReadableStreamDefaultReader<Uint8Array> | null = null;
  private timer: unknown = null;
  private attempt = 0;
  /**
   * True once this connection has been live, or when the owner took over a stream that was. A connect
   * with that history and no resume point cannot be replayed, so it reports a reset.
   */
  continuing = false;

  constructor(
    private readonly deps: ConnectionDeps,
    private readonly handlers: ConnectionHandlers,
  ) {}

  get active(): boolean {
    return this.running;
  }

  start(): void {
    if (this.running) return;
    this.running = true;
    this.attempt = 0;
    void this.connect();
  }

  /** Aborts the in-flight request and cancels the reconnect timer. Idempotent. */
  stop(): void {
    this.running = false;
    if (this.timer !== null) {
      this.deps.clearTimeout(this.timer);
      this.timer = null;
    }
    this.abort?.abort();
    this.abort = null;
    // A body that is not tied to the signal (a test double, a proxy) still has to be released.
    void this.reader?.cancel().catch(() => undefined);
    this.reader = null;
  }

  private scheduleReconnect(minDelayMs: number): void {
    if (!this.running) return;
    const delay = Math.max(backoffDelay(this.attempt, this.deps.random), minDelayMs);
    this.attempt++;
    this.timer = this.deps.setTimeout(() => {
      this.timer = null;
      void this.connect();
    }, delay);
  }

  private async connect(): Promise<void> {
    if (!this.running) return;
    const ac = new AbortController();
    this.abort = ac;
    const headers: Record<string, string> = { Accept: 'text/event-stream' };
    if (this.lastEventId !== undefined) headers['Last-Event-ID'] = this.lastEventId;

    let res: Response;
    try {
      res = await this.deps.fetch(this.deps.url, {
        method: 'GET',
        headers,
        credentials: 'same-origin',
        redirect: 'manual',
        cache: 'no-store',
        signal: ac.signal,
      });
    } catch {
      if (ac.signal.aborted) return;
      this.handlers.onError(undefined);
      this.scheduleReconnect(0);
      return;
    }
    if (ac.signal.aborted) {
      void res.body?.cancel().catch(() => undefined);
      return;
    }

    // 05-realtime §4.8 item 4: only the response type is consulted, never `redirected`.
    if (res.type === 'opaqueredirect') {
      this.handlers.onAuthRedirect(new ApiAuthRedirectError(this.deps.url, res.url));
      this.scheduleReconnect(0);
      return;
    }
    if (!res.ok || res.body === null) {
      void res.body?.cancel().catch(() => undefined);
      this.handlers.onError(res.status);
      this.scheduleReconnect(retryAfterMs(res.headers.get('Retry-After'), this.deps.now()));
      return;
    }

    // A reconnect with no resume point cannot be replayed: tell the app to refetch.
    if (this.continuing && this.lastEventId === undefined) this.handlers.onReset('gap');
    this.continuing = true;

    const reader = res.body.getReader();
    this.reader = reader;
    const decoder = new TextDecoder();
    const parser = createSseParser((message) => this.dispatch(message, ac));
    try {
      while (!ac.signal.aborted) {
        const { done, value } = await reader.read();
        if (done) break;
        parser.push(decoder.decode(value, { stream: true }));
      }
    } catch {
      if (!ac.signal.aborted) this.handlers.onError(undefined);
    }
    if (this.reader === reader) this.reader = null;
    if (ac.signal.aborted) return;
    this.scheduleReconnect(0);
  }

  private dispatch(message: SseMessage, ac: AbortController): void {
    if (ac.signal.aborted) return;
    let topic: string;
    let key: string | undefined;
    let data: unknown;
    try {
      const envelope = JSON.parse(message.data) as { topic?: unknown; key?: unknown; data?: unknown } | null;
      if (envelope === null || typeof envelope !== 'object' || typeof envelope.topic !== 'string') {
        throw new Error('frame has no topic');
      }
      topic = envelope.topic;
      key = typeof envelope.key === 'string' && envelope.key !== '' ? envelope.key : undefined;
      data = envelope.data;
    } catch {
      this.handlers.onError(undefined);
      return;
    }
    this.attempt = 0; // a live stream: the next failure starts the backoff over

    switch (topic) {
      case CONTROL_OPENED:
        this.handlers.onOpened();
        return;
      case CONTROL_RESET:
        this.handlers.onReset(reasonOf(data, RESET_REASONS, 'gap'));
        return;
      case CONTROL_CLOSED: {
        const reason = reasonOf(data, CLOSE_REASONS, 'shutdown');
        // A revoked credential or permission is not retried: the auth rule (§7.9.1) takes over.
        if (reason !== 'shutdown') this.stop();
        this.handlers.onClosed(reason);
        return;
      }
      default: {
        // Control frames carry no id, so only catalog events move the resume point.
        if (message.id !== undefined) this.lastEventId = message.id;
        const event: RealtimeEvent = key === undefined ? { topic, data } : { topic, key, data };
        this.handlers.onEvent(event, message.id);
      }
    }
  }
}
