// file: web/src/services/coreEvents/connection.ts
// version: 0.1.0
// guid: 3e8010f5-99ae-4c8d-b82e-a1f163b4ca4a
// last-edited: 2026-11-19

// One event stream: a fetch + ReadableStream reader over GET /api/core/v1/events
// (05-realtime 4.8). It owns the reconnect loop for ONE server-side stream, i.e. one
// topic set and at most one key. The manager (manager.ts) runs one of these for the
// shared stream and one per keyed stream.
//
// Rules this file enforces, each with a test in connection.test.ts:
//  - redirect: 'manual'; an opaqueredirect response is an SSO bounce and is raised as
//    the API client's ApiAuthRedirectError. response.redirected is never read (it is
//    never true under manual, and a followed cross-origin redirect fails CORS first).
//  - Last-Event-ID (header) on every reconnect once an id has been seen.
//  - Refusals (problem+json before the first byte) surface with status, code and
//    Retry-After, and the next attempt waits at least that long.
//  - 3 s -> 60 s jittered backoff, no give-up.
//  - core.stream.reset.v1 -> resync handlers; core.stream.closed.v1 -> reconnect.
//  - Every wait on the network has a deadline (connect and idle), so a hung server is a
//    visible reconnect, not an infinite 'connecting'.

import { ApiAuthRedirectError, decodeProblem } from '@mediacore/api-client';

import { parseRetryAfter, reconnectDelay } from './backoff';
import { createSseParser } from './sseParser';
import {
  StreamProtocolError,
  StreamRefusedError,
  TOPIC_CLOSED,
  TOPIC_OPENED,
  TOPIC_RESET,
  type ClosedReason,
  type ResetReason,
  type StreamEnvelope,
  type StreamState,
  type StreamStatus,
} from './types';

export const EVENTS_PATH = '/api/core/v1/events';
/** Headers must arrive within this long or the attempt is abandoned and retried. */
export const DEFAULT_CONNECT_TIMEOUT_MS = 15000;
/** The server heartbeats every 15 s (max 60 s); three missed beats is a dead stream. */
export const DEFAULT_IDLE_TIMEOUT_MS = 45000;

export interface ConnectionDeps {
  fetch: typeof fetch;
  random: () => number;
}

export interface ConnectionOptions {
  /** Server-side key narrowing this stream to one entity ('' for none). */
  key: string;
  /** The topic patterns to stream, read fresh on every (re)connect. */
  topics: () => readonly string[];
  /** Called for each event frame. */
  onEvent: (env: StreamEnvelope, id: string) => void;
  onReset: (reason: ResetReason) => void;
  onClosed: (reason: ClosedReason) => void;
  onStatus: (status: StreamStatus) => void;
  baseUrl?: string;
  connectTimeoutMs?: number;
  idleTimeoutMs?: number;
  deps?: Partial<ConnectionDeps>;
}

export interface StreamConnection {
  /** Allow the stream to run (a subscriber exists and the tab is visible). Idempotent. */
  open(): void;
  /** Close the stream and cancel any pending retry, for `state` (idle or hidden). */
  close(state: 'idle' | 'hidden'): void;
  /** The topic set changed: restart now, resuming from the last id, with no backoff. */
  restart(): void;
  readonly status: StreamStatus;
}

/** Builds the stream URL: repeated `topic`, and `key` when the stream is keyed. */
export function buildStreamUrl(baseUrl: string, topics: readonly string[], key: string): string {
  const q = new URLSearchParams();
  for (const t of topics) q.append('topic', t);
  if (key !== '') q.set('key', key);
  const qs = q.toString();
  return `${baseUrl}${EVENTS_PATH}${qs ? `?${qs}` : ''}`;
}

function isEnvelope(v: unknown): v is StreamEnvelope {
  return (
    typeof v === 'object' &&
    v !== null &&
    typeof (v as { topic?: unknown }).topic === 'string' &&
    typeof (v as { ts?: unknown }).ts === 'string'
  );
}

function reasonOf(data: unknown): string {
  const r = (data as { reason?: unknown } | null | undefined)?.reason;
  return typeof r === 'string' ? r : '';
}

/** Why an attempt ended; never silent. */
class AttemptEnd {
  constructor(
    readonly error: unknown,
    readonly retryAfterMs: number | null,
  ) {}
}

export function createStreamConnection(opts: ConnectionOptions): StreamConnection {
  // Raw fetch on purpose, not apiFetch: apiFetch's auth-bounce check reads
  // response.redirected, which 05-realtime 4.8 item 4 forbids here (with
  // redirect: 'manual' it is never true; the opaqueredirect type is the signal), and the
  // stream needs its own connect and idle deadlines around a body that never ends.
  // The bounce is handled below (opaqueredirect and a text/html 200 both raise
  // ApiAuthRedirectError), which is what the lint rule exists to guarantee.
  // eslint-disable-next-line no-restricted-properties
  const doFetch: typeof fetch = opts.deps?.fetch ?? ((...a) => globalThis.fetch(...a));
  const random = opts.deps?.random ?? Math.random;
  const baseUrl = opts.baseUrl ?? '';
  const connectTimeoutMs = opts.connectTimeoutMs ?? DEFAULT_CONNECT_TIMEOUT_MS;
  const idleTimeoutMs = opts.idleTimeoutMs ?? DEFAULT_IDLE_TIMEOUT_MS;

  let status: StreamStatus = { state: 'idle', attempt: 0, streamKey: opts.key };
  let running = false;
  let generation = 0; // bumps on every close/restart so a stale loop stops itself
  let controller: AbortController | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;
  let wakeSleep: (() => void) | null = null;
  let lastEventId = '';
  let attempt = 0;
  let closedReason: ClosedReason | undefined;
  let closedThisAttempt = false;

  const setStatus = (state: StreamState, extra: Partial<StreamStatus> = {}) => {
    status = { state, attempt, streamKey: opts.key, ...(closedReason ? { closedReason } : {}), ...extra };
    opts.onStatus(status);
  };

  const sleep = (ms: number, gen: number) =>
    new Promise<void>((resolve) => {
      timer = setTimeout(() => {
        timer = null;
        wakeSleep = null;
        resolve();
      }, ms);
      wakeSleep = () => {
        if (timer) clearTimeout(timer);
        timer = null;
        wakeSleep = null;
        resolve();
      };
      if (gen !== generation) wakeSleep();
    });

  const teardown = () => {
    generation++;
    controller?.abort();
    controller = null;
    wakeSleep?.();
  };

  /** One attempt. Resolves with why it ended; throws only on our own abort. */
  const attemptOnce = async (gen: number): Promise<AttemptEnd> => {
    const ctl = new AbortController();
    controller = ctl;
    closedThisAttempt = false;
    const url = buildStreamUrl(baseUrl, opts.topics(), opts.key);
    let connectTimedOut = false;
    const connectTimer = setTimeout(() => {
      connectTimedOut = true;
      ctl.abort();
    }, connectTimeoutMs);
    const headers: Record<string, string> = { Accept: 'text/event-stream' };
    if (lastEventId !== '') headers['Last-Event-ID'] = lastEventId;

    let res: Response;
    try {
      res = await doFetch(url, {
        method: 'GET',
        headers,
        credentials: 'same-origin',
        cache: 'no-store',
        redirect: 'manual',
        signal: ctl.signal,
      });
    } catch (err) {
      clearTimeout(connectTimer);
      if (gen !== generation) throw err; // our own abort
      if (connectTimedOut) {
        return new AttemptEnd(
          new StreamProtocolError(`no response from ${EVENTS_PATH} within ${connectTimeoutMs}ms`),
          null,
        );
      }
      return new AttemptEnd(err, null);
    }
    clearTimeout(connectTimer);

    // SSO bounce: with redirect 'manual' the browser hands back an opaque response.
    if (res.type === 'opaqueredirect') {
      return new AttemptEnd(new ApiAuthRedirectError(url, url), null);
    }
    if (!res.ok) {
      const retryAfterMs = parseRetryAfter(res.headers?.get('Retry-After'));
      const problem = await decodeProblem(res).catch(() => null);
      return new AttemptEnd(
        new StreamRefusedError(res.status, problem?.code ?? '', problem?.detail ?? problem?.title ?? '', retryAfterMs),
        retryAfterMs,
      );
    }
    const contentType = (res.headers?.get('Content-Type') ?? '').toLowerCase();
    if (!contentType.includes('text/event-stream')) {
      // A same-origin SSO page answers 200 text/html: the same bounce, not a stream.
      if (contentType.includes('text/html')) {
        return new AttemptEnd(new ApiAuthRedirectError(url, url), null);
      }
      return new AttemptEnd(new StreamProtocolError(`unexpected content type "${contentType}" from ${EVENTS_PATH}`), null);
    }
    if (!res.body) return new AttemptEnd(new StreamProtocolError('event stream response has no body'), null);

    const reader = res.body.getReader();
    const decoder = new TextDecoder();
    const parser = createSseParser();
    let idleTimedOut = false;
    let idleTimer: ReturnType<typeof setTimeout> | null = null;
    const armIdle = () => {
      if (idleTimer) clearTimeout(idleTimer);
      idleTimer = setTimeout(() => {
        idleTimedOut = true;
        ctl.abort();
      }, idleTimeoutMs);
    };
    armIdle();
    try {
      for (;;) {
        const { done, value } = await reader.read();
        if (done) break;
        armIdle();
        for (const frame of parser.push(decoder.decode(value, { stream: true }))) {
          if (gen !== generation) return new AttemptEnd(null, null);
          handleFrame(frame.id, frame.data);
        }
      }
      // After a closed frame the end is the announced one, not a fault.
      return new AttemptEnd(closedThisAttempt ? null : new StreamProtocolError('event stream ended by the server'), null);
    } catch (err) {
      if (gen !== generation) throw err;
      if (idleTimedOut) {
        return new AttemptEnd(new StreamProtocolError(`event stream silent for ${idleTimeoutMs}ms`), null);
      }
      return new AttemptEnd(err, null);
    } finally {
      if (idleTimer) clearTimeout(idleTimer);
      reader.cancel().catch(() => {});
    }
  };

  /** Control frames never carry an id: and so never move lastEventId. */
  const handleFrame = (id: string, raw: string) => {
    let parsed: unknown;
    try {
      parsed = JSON.parse(raw);
    } catch {
      return; // a frame that is not JSON is withheld by the server; skip a stray one
    }
    if (!isEnvelope(parsed)) return;
    switch (parsed.topic) {
      case TOPIC_OPENED:
        attempt = 0;
        closedReason = undefined;
        setStatus('open');
        return;
      case TOPIC_RESET:
        opts.onReset(reasonOf(parsed.data));
        return;
      case TOPIC_CLOSED: {
        const reason = reasonOf(parsed.data);
        closedReason = reason;
        closedThisAttempt = true;
        opts.onClosed(reason);
        return;
      }
      default:
        if (id !== '') lastEventId = id;
        opts.onEvent(parsed, id);
    }
  };

  const loop = async (gen: number) => {
    try {
      for (;;) {
        if (gen !== generation) return;
        setStatus('connecting');
        const end = await attemptOnce(gen);
        if (gen !== generation) return;
        attempt += 1;
        const delayMs = reconnectDelay(attempt, end.retryAfterMs, random);
        setStatus('reconnecting', { delayMs, error: end.error ?? undefined });
        await sleep(delayMs, gen);
      }
    } catch {
      // our own abort (close/restart); the new generation, if any, runs its own loop
    }
  };

  return {
    open() {
      if (running) return;
      running = true;
      const gen = generation;
      void loop(gen);
    },
    close(state) {
      running = false;
      teardown();
      attempt = 0;
      setStatus(state);
    },
    restart() {
      if (!running) return;
      teardown();
      attempt = 0;
      void loop(generation);
    },
    get status() {
      return status;
    },
  };
}
