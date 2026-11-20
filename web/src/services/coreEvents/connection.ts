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
//  - core.stream.reset.v1 -> resync handlers, every one: a reset can follow the open (owed
//    by the subscribe) or come mid-stream (a gap after withheld events), and only the
//    manager's coalescing may merge them; core.stream.closed.v1 -> reconnect.
//  - Waits are measured on a monotonic clock (deps.now), so a wall-clock step never
//    stretches a backoff or a server hold.
//  - Every wait on the network has a deadline (connect and idle), so a hung server is a
//    visible reconnect, not an infinite 'connecting'.

import { ApiAuthRedirectError, decodeProblem } from '@mediacore/api-client';

import { clampRetryAfter, parseRetryAfter, reconnectDelay } from './backoff';
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
  /** A monotonic clock in ms (performance.now): backoff and hold deadlines are on it. */
  now: () => number;
}

/** The default clock: monotonic, so an NTP step or a resumed laptop moves no deadline. */
export const monotonicNow = (): number => performance.now();

/**
 * Reconnect state that outlives one connection object. The manager keeps one per
 * stream key, so a stream that is closed for having no subscriber and then wanted again
 * (a route change, a run's log reopened) resumes its backoff instead of starting over
 * and reconnecting at once under a standing wait (#4785 r2 m1).
 */
export interface BackoffState {
  /**
   * Consecutive failed attempts since the last good open. The manager starts a stream
   * that was closed and is wanted again from zero once its wait has lapsed.
   */
  attempt: number;
  /** Never connect before this (deps.now ms); zeroed by a good open. */
  notBefore: number;
}

/**
 * A wait that holds back every stream of the tab. The server's 429 (too many streams for
 * this user) and 503 (too many streams, not ready, closing) are about the user or the
 * server, not one stream (core/realtime/handler.go), so their Retry-After applies to a
 * new keyed stream as much as to the shared one that was refused.
 */
export interface ServerHold {
  /** The time (deps.now ms) before which no stream may connect (0 for none). */
  until(): number;
  /** A refusal named a Retry-After: hold every stream until `untilMs` (never shortens). */
  extend(untilMs: number): void;
}

/** Statuses whose Retry-After is about the user or the server, not this one stream. */
const SERVER_WIDE_REFUSALS = new Set([429, 503]);

export interface ConnectionOptions {
  /** Server-side key narrowing this stream to one entity ('' for none). */
  key: string;
  /** The topic patterns to stream, read fresh on every (re)connect. */
  topics: () => readonly string[];
  /** Topic patterns to exclude (the `exclude` query parameter), read on every (re)connect. */
  exclude?: () => readonly string[];
  /** Called for each event frame. */
  onEvent: (env: StreamEnvelope, id: string) => void;
  onReset: (reason: ResetReason) => void;
  onClosed: (reason: ClosedReason) => void;
  onStatus: (status: StreamStatus) => void;
  baseUrl?: string;
  connectTimeoutMs?: number;
  idleTimeoutMs?: number;
  deps?: Partial<ConnectionDeps>;
  /** Backoff state to read and write (the manager's, per stream key); a private one when absent. */
  backoff?: BackoffState;
  /** The tab-wide hold from a server-wide refusal; none when absent. */
  serverHold?: ServerHold;
}

export interface StreamConnection {
  /** Allow the stream to run (a subscriber exists and the tab is visible). Idempotent. */
  open(): void;
  /** Close the stream and cancel any pending retry, for `state` (idle or hidden). */
  close(state: 'idle' | 'hidden'): void;
  /** The topic set changed: restart, resuming from the last id, but still never before notBefore. */
  restart(): void;
  readonly status: StreamStatus;
}

/** Builds the stream URL: repeated `topic` and `exclude`, and `key` when the stream is keyed. */
export function buildStreamUrl(
  baseUrl: string,
  topics: readonly string[],
  key: string,
  exclude: readonly string[] = [],
): string {
  const q = new URLSearchParams();
  for (const t of topics) q.append('topic', t);
  for (const t of exclude) q.append('exclude', t);
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
  const now = opts.deps?.now ?? monotonicNow;
  const baseUrl = opts.baseUrl ?? '';
  const connectTimeoutMs = opts.connectTimeoutMs ?? DEFAULT_CONNECT_TIMEOUT_MS;
  const idleTimeoutMs = opts.idleTimeoutMs ?? DEFAULT_IDLE_TIMEOUT_MS;

  let running = false;
  let generation = 0; // bumps on every close/restart so a stale loop stops itself
  let controller: AbortController | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;
  let wakeSleep: (() => void) | null = null;
  let lastEventId = '';
  // backoff.notBefore: never sooner than this (now() ms). Set from every failed attempt's
  // backoff and Retry-After, honoured by open() and restart() too, zeroed by a good open
  // (and by the manager once a closed stream's wait has lapsed).
  const backoff: BackoffState = opts.backoff ?? { attempt: 0, notBefore: 0 };
  const serverHold = opts.serverHold;
  let status: StreamStatus = { state: 'idle', attempt: backoff.attempt, streamKey: opts.key };
  let closedReason: ClosedReason | undefined;
  let closedThisAttempt = false;
  let lastError: unknown;
  // Missed events cannot be replayed when the attempt sent no Last-Event-ID (the server
  // sends no reset then, sse/broker.go), nor after the tab was hidden: tell the resync
  // handlers on the next good open (reason 'reconnect').
  let sentLastId = false;
  let resyncOnOpen = false;
  // A reopen can raise two resyncs: 'reconnect' at the open and the reset the subscribe
  // owed right after it (core/internal/sse/serve.go). Both are passed on: the same 'gap'
  // also comes mid-stream after withheld events (core/realtime/stream.go), and with no
  // Last-Event-ID the subscribe owes none, so no frame here can be told apart as a
  // repeat. The manager's resync coalescing merges the pair into one refetch (#4813 r1 M1).

  const setStatus = (state: StreamState, extra: Partial<StreamStatus> = {}) => {
    status = { state, attempt: backoff.attempt, streamKey: opts.key, ...(closedReason ? { closedReason } : {}), ...extra };
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
    const url = buildStreamUrl(baseUrl, opts.topics(), opts.key, opts.exclude?.() ?? []);
    let connectTimedOut = false;
    const connectTimer = setTimeout(() => {
      connectTimedOut = true;
      ctl.abort();
    }, connectTimeoutMs);
    const headers: Record<string, string> = { Accept: 'text/event-stream' };
    if (lastEventId !== '') headers['Last-Event-ID'] = lastEventId;
    sentLastId = lastEventId !== '';

    let res: Response;
    try {
      res = await doFetch(url, {
        method: 'GET',
        headers,
        credentials: 'include',
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
        backoff.attempt = 0;
        backoff.notBefore = 0;
        lastError = undefined;
        closedReason = undefined;
        setStatus('open');
        if (resyncOnOpen || !sentLastId) {
          resyncOnOpen = false;
          opts.onReset('reconnect');
        }
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

  const loop = async (gen: number, initialWait: number) => {
    try {
      let wait = initialWait;
      for (;;) {
        if (wait > 0) {
          setStatus('reconnecting', { delayMs: wait, error: lastError });
          await sleep(wait, gen);
        }
        if (gen !== generation) return;
        // Another stream of this tab may have been refused server-wide while this one
        // slept: wait out that hold too before connecting.
        const held = holdRemaining();
        if (held > 0) {
          wait = held;
          continue;
        }
        setStatus('connecting');
        const end = await attemptOnce(gen);
        if (gen !== generation) return;
        backoff.attempt += 1;
        wait = reconnectDelay(backoff.attempt, end.retryAfterMs, random);
        backoff.notBefore = now() + wait;
        if (
          serverHold &&
          end.retryAfterMs != null &&
          end.error instanceof StreamRefusedError &&
          SERVER_WIDE_REFUSALS.has(end.error.status)
        ) {
          serverHold.extend(now() + end.retryAfterMs);
        }
        lastError = end.error ?? undefined;
      }
    } catch {
      // our own abort (close/restart); the new generation, if any, runs its own loop
    }
  };

  // The server hold is checked by the loop before every attempt, the first included.
  const holdRemaining = () => (serverHold ? clampRetryAfter(serverHold.until() - now()) : 0);
  const remaining = () => clampRetryAfter(backoff.notBefore - now());

  return {
    open() {
      if (running) return;
      running = true;
      void loop(generation, remaining());
    },
    close(state) {
      running = false;
      teardown();
      if (state === 'hidden') resyncOnOpen = true;
      setStatus(state);
    },
    restart() {
      if (!running) return;
      teardown();
      void loop(generation, remaining());
    },
    get status() {
      return status;
    },
  };
}
