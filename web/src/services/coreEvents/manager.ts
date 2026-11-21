// file: web/src/services/coreEvents/manager.ts
// version: 0.1.0
// guid: 75d43e62-9a25-443d-b433-db8a3540088e
// last-edited: 2026-11-19

// The topic-subscription API over the core event stream (05-realtime 4.8).
//
// Per-tab mode: this tab opens its own stream, only while it is visible. (Leader
// election across tabs, 05-realtime 4.8 item 7, is out of scope here and arrives with
// the shell.) The manager multiplexes every subscriber onto:
//  - one SHARED stream: `topic=*` with `exclude=core.task.log-appended.v1`, filtered on the
//    client per subscriber. ORDERING: the server answers 400 unknown_topic for an exclude
//    naming a topic that is not in the catalog (core/realtime/handler.go match()), so the
//    flip must land after F5-18 declares core.task.log-appended.v1; until then this
//    stream is refused (visibly, in the status), and
//  - one KEYED stream per key (`topic=...&key=<id>`), opened for a subscriber that asked
//    for `stream: 'keyed'`, e.g. a run's log lines.
// Nothing connects until the first subscribe (or retain), and a stream with no subscriber
// is closed.

import {
  createStreamConnection,
  type BackoffState,
  type ConnectionDeps,
  type ServerHold,
  type StreamConnection,
} from './connection';
import { createAwakeClock } from './clock';
import {
  topicMatches,
  type ClosedListener,
  type PayloadOf,
  type ResyncHandler,
  type ResyncInfo,
  type StatusListener,
  type StreamEnvelope,
  type StreamStatus,
  type TopicEvent,
  type TopicHandler,
} from './types';

/** A topic the shared stream refuses to carry: with one stream for everything, every
 * visible run's log would reach every subscriber (05-realtime 4.8 item 7). */
export const DEFAULT_KEYED_ONLY_TOPICS: readonly string[] = ['core.task.log-appended.v1'];

export interface SubscribeOptions {
  /**
   * 'shared' (default): the event rides the tab's one shared stream; `key`, if given,
   * filters on the client. 'keyed': a dedicated stream narrowed on the server to `key`
   * (required), opened while this subscription lives.
   */
  stream?: 'shared' | 'keyed';
  key?: string;
}

/** Resets arriving within this long of the first are one call (a reopen's open + reset). */
export const DEFAULT_RESYNC_COALESCE_MS = 250;
/** At least this long between two calls of one resync handler (a flapping server). */
export const DEFAULT_RESYNC_MIN_INTERVAL_MS = 5000;

export interface ResyncOptions {
  /**
   * Whose resets to take: '' (default) the shared stream's, a key for that keyed stream's,
   * '*' every stream's. A new keyed stream opens with no id to resume from, so it always
   * raises 'reconnect'; a view that does not own the key must not refetch for it
   * (#4785 r2 m2). Coalescing is per stream: a '*' handler gets one call for each stream
   * that reset in the window, so a handler that routes on `streamKey` never loses one.
   */
  streamKey?: string;
  /** Merge every reset within this long of the first into one call per stream (trailing),
   * which is what turns a reopen's 'reconnect' and the reset the subscribe owed into one
   * refetch. 0 with minIntervalMs 0 calls synchronously, once per reset. */
  coalesceMs?: number;
  /** At least this long between two calls; resets in between are merged into the next. */
  minIntervalMs?: number;
}

export interface VisibilitySource {
  readonly visible: boolean;
  /** Subscribe to changes; returns the unsubscribe. */
  onChange(listener: () => void): () => void;
  /**
   * Subscribe to "the machine or its network is back" (the document source uses window
   * 'online'); returns the unsubscribe. A stream that is waiting out a backoff or a hold
   * on a timer is woken to re-read its deadlines, because the timer did not run while a
   * laptop slept (a hidden tab needs no wake: becoming visible reopens it). The holds
   * themselves are never dropped, only re-read on the awake clock (clock.ts).
   */
  onResume?(listener: () => void): () => void;
}

export interface EventStreamOptions {
  baseUrl?: string;
  connectTimeoutMs?: number;
  idleTimeoutMs?: number;
  keyedOnlyTopics?: readonly string[];
  /** Defaults to the document's visibilityState; a non-browser host is always visible. */
  visibility?: VisibilitySource;
  deps?: Partial<ConnectionDeps>;
  /** Called when a subscriber's handler throws (it never stops the stream). */
  onHandlerError?: (err: unknown, topic: string) => void;
}

export interface EventStream {
  /** Subscribe to a topic (exact, `module.*` or `*`); returns the unsubscribe. */
  subscribe<T extends string>(topic: T, handler: TopicHandler<PayloadOf<T>>, opts?: SubscribeOptions): () => void;
  /**
   * Called when a stream must be resynced (core.stream.reset.v1, or a client-raised
   * 'reconnect'): refetch over REST. By default only the shared stream's resets, merged
   * per DEFAULT_RESYNC_COALESCE_MS and at most one call per DEFAULT_RESYNC_MIN_INTERVAL_MS
   * (with streamKey '*', one call per stream that reset).
   */
  onResync(handler: ResyncHandler, opts?: ResyncOptions): () => void;
  /**
   * Keep the shared stream open with no topic subscriber, for a listener that only needs
   * the control frames (the shutdown backdrop). Returns the release; idempotent.
   */
  retain(): () => void;
  /** Called on core.stream.closed.v1 (shutdown: show the backdrop first). */
  onClosed(listener: ClosedListener): () => void;
  /** Status changes of every stream; `status.streamKey` says which. */
  onStatus(listener: StatusListener): () => void;
  /** The shared stream's current status. */
  getStatus(): StreamStatus;
  /** Close everything and drop every subscription. */
  dispose(): void;
}

interface Sub {
  topic: string;
  handler: TopicHandler<unknown>;
  key: string; // client-side filter (shared) or the stream key (keyed)
  keyed: boolean;
}

interface ResyncSub {
  handler: ResyncHandler;
  streamKey: string;
  coalesceMs: number;
  minIntervalMs: number;
  timer: ReturnType<typeof setTimeout> | null;
  /** The latest reset per stream key since the last call. */
  pending: Map<string, ResyncInfo>;
  /** now() of the last call, for minIntervalMs. */
  lastCall: number;
}

function documentVisibility(): VisibilitySource {
  if (typeof document === 'undefined') return { visible: true, onChange: () => () => {} };
  return {
    get visible() {
      return document.visibilityState !== 'hidden';
    },
    onChange(listener) {
      document.addEventListener('visibilitychange', listener);
      return () => document.removeEventListener('visibilitychange', listener);
    },
    onResume(listener) {
      if (typeof window === 'undefined') return () => {};
      window.addEventListener('online', listener);
      return () => window.removeEventListener('online', listener);
    },
  };
}

export function createEventStream(options: EventStreamOptions = {}): EventStream {
  const keyedOnly = new Set(options.keyedOnlyTopics ?? DEFAULT_KEYED_ONLY_TOPICS);
  // One awake clock for the manager and every connection it makes (clock.ts): the holds and
  // backoffs below are recorded on it, so all of them must read it. A wall-clock step back
  // moves no wait, and the time a laptop slept counts toward one.
  const now = options.deps?.now ?? createAwakeClock();
  const visibility = options.visibility ?? documentVisibility();
  const subs = new Set<Sub>();
  const resyncSubs = new Set<ResyncSub>();
  const closedListeners = new Set<ClosedListener>();
  const statusListeners = new Set<StatusListener>();
  const connections = new Map<string, StreamConnection>(); // '' = shared; else the key
  // Backoff per stream key, kept after its connection is closed for having no subscriber
  // while a wait is still standing (pruned once it lapses), so a resubscribe does not
  // reconnect at once (#4785 r2 m1).
  const backoffs = new Map<string, BackoffState>();
  let holdUntil = 0;
  const serverHold: ServerHold = {
    until: () => holdUntil,
    extend: (untilMs) => {
      if (untilMs > holdUntil) holdUntil = untilMs;
    },
  };
  let holds = 0;
  let sharedStatus: StreamStatus = { state: 'idle', attempt: 0, streamKey: '' };
  let pendingSync = false;
  let disposed = false;

  const guard = (fn: () => void, topic: string) => {
    try {
      fn();
    } catch (err) {
      if (options.onHandlerError) options.onHandlerError(err, topic);
      else console.error(`event stream handler for ${topic} threw`, err);
    }
  };

  const topicsFor = (streamKey: string): string[] => {
    const set = new Set<string>();
    for (const s of subs) {
      if (streamKey === '' ? !s.keyed : s.keyed && s.key === streamKey) set.add(s.topic);
    }
    return [...set].sort();
  };

  const dispatch = (streamKey: string, env: StreamEnvelope, id: string) => {
    // The shared stream asks the server to exclude these; should one arrive anyway, it
    // still reaches no shared subscriber, '*' included (#4785 r2 m3).
    if (streamKey === '' && keyedOnly.has(env.topic)) return;
    const event: TopicEvent = { topic: env.topic, ts: env.ts, key: env.key ?? '', data: env.data, id };
    for (const s of [...subs]) {
      if (streamKey === '' ? s.keyed : !s.keyed || s.key !== streamKey) continue;
      if (!topicMatches(s.topic, env.topic)) continue;
      if (!s.keyed && s.key !== '' && s.key !== event.key) continue;
      guard(() => s.handler(event), env.topic);
    }
  };

  const deliverResync = (r: ResyncSub, info: ResyncInfo) => {
    // An earlier handler of this same reset may have removed this one.
    if (!resyncSubs.has(r)) return;
    if (r.streamKey !== '*' && r.streamKey !== info.streamKey) return;
    if (r.coalesceMs <= 0 && r.minIntervalMs <= 0) {
      guard(() => r.handler(info), 'core.stream.reset.v1');
      return;
    }
    // Per stream, the latest reason wins (the handler refetches either way); streams are
    // never merged into each other.
    r.pending.set(info.streamKey, info);
    if (r.timer) return;
    const t = now();
    const at = Math.max(t + r.coalesceMs, r.lastCall + r.minIntervalMs);
    r.timer = setTimeout(() => {
      r.timer = null;
      const due = [...r.pending.values()];
      r.pending.clear();
      if (due.length === 0 || !resyncSubs.has(r)) return;
      r.lastCall = now();
      for (const p of due) {
        if (!resyncSubs.has(r)) return; // the handler unsubscribed itself
        guard(() => r.handler(p), 'core.stream.reset.v1');
      }
    }, at - t);
  };

  const stopResync = (r: ResyncSub) => {
    if (r.timer) clearTimeout(r.timer);
    r.timer = null;
    r.pending.clear();
    resyncSubs.delete(r);
  };

  // Called only when a stream's connection is created, so no connection holds the state.
  // A wait that has lapsed by then is spent: the stream starts again from attempt 0,
  // whether or not a sync happened to prune it meanwhile.
  const backoffFor = (streamKey: string): BackoffState => {
    let b = backoffs.get(streamKey);
    if (b && b.notBefore <= now()) {
      b.attempt = 0;
      b.notBefore = 0;
    }
    if (!b) {
      b = { attempt: 0, notBefore: 0 };
      backoffs.set(streamKey, b);
    }
    return b;
  };

  const connectionFor = (streamKey: string): StreamConnection => {
    let c = connections.get(streamKey);
    if (c) return c;
    c = createStreamConnection({
      key: streamKey,
      // The shared stream is topic=* minus the keyed-only topics, filtered per
      // subscriber on the client (05-realtime 4.8 item 7): one subscriber's refused
      // topic cannot refuse the stream for the rest, and a new subscriber never
      // reconnects it. A keyed stream names its own topics.
      topics: () => (streamKey === '' ? ['*'] : topicsFor(streamKey)),
      exclude: () => (streamKey === '' ? [...keyedOnly].sort() : []),
      onEvent: (env, id) => dispatch(streamKey, env, id),
      onReset: (reason) => {
        for (const r of [...resyncSubs]) deliverResync(r, { reason, streamKey });
      },
      onClosed: (reason) => {
        for (const l of [...closedListeners]) guard(() => l({ reason, streamKey }), 'core.stream.closed.v1');
      },
      onStatus: (status) => {
        if (streamKey === '') sharedStatus = status;
        for (const l of [...statusListeners]) guard(() => l(status), 'status');
      },
      baseUrl: options.baseUrl,
      connectTimeoutMs: options.connectTimeoutMs,
      idleTimeoutMs: options.idleTimeoutMs,
      deps: { ...options.deps, now },
      backoff: backoffFor(streamKey),
      serverHold,
    });
    connections.set(streamKey, c);
    return c;
  };

  // Reconcile every stream with the subscriber set and the tab's visibility. Runs in a
  // microtask so a burst of subscribes (a page mounting ten hooks) is one (re)connect.
  const sync = () => {
    if (pendingSync || disposed) return;
    pendingSync = true;
    queueMicrotask(() => {
      pendingSync = false;
      if (disposed) return;
      const wanted = new Set<string>();
      if (holds > 0) wanted.add('');
      for (const s of subs) wanted.add(s.keyed ? s.key : '');
      for (const k of wanted) {
        const c = connectionFor(k);
        const sig = k === '' ? '' : topicsFor(k).join('\n');
        if (!visibility.visible) {
          c.close('hidden');
        } else if (c.status.state === 'idle' || c.status.state === 'hidden') {
          c.open();
        } else if (signatures.get(k) !== sig) {
          c.restart();
        }
        signatures.set(k, sig);
      }
      for (const [k, c] of connections) {
        if (!wanted.has(k)) {
          c.close('idle');
          connections.delete(k);
          signatures.delete(k);
        }
      }
      // Memory only: backoffFor resets a lapsed entry anyway, so pruning it here or not
      // never changes what a reopened stream does.
      const t = now();
      for (const [k, b] of backoffs) {
        if (!connections.has(k) && b.notBefore <= t) backoffs.delete(k);
      }
    });
  };
  const signatures = new Map<string, string>();

  const stopVisibility = visibility.onChange(() => {
    // A hidden stream keeps its signature so becoming visible reopens, not restarts.
    for (const k of connections.keys()) signatures.delete(k);
    sync();
  });

  // A wait timer that was sleeping while the machine slept fires late: restart every
  // stream that is waiting so it re-reads its backoff and the tab's hold on the awake
  // clock. Nothing is cleared, so a Retry-After that has not really elapsed still holds.
  const stopResume =
    visibility.onResume?.(() => {
      if (disposed) return;
      for (const c of connections.values()) {
        if (c.status.state === 'reconnecting') c.restart();
      }
    }) ?? (() => {});

  return {
    subscribe(topic, handler, opts = {}) {
      if (disposed) throw new Error('event stream: subscribe after dispose');
      const keyed = opts.stream === 'keyed';
      const key = opts.key ?? '';
      if (keyed && key === '') throw new Error(`event stream: a keyed stream for ${topic} needs a key`);
      if (!keyed && keyedOnly.has(topic)) {
        throw new Error(`event stream: ${topic} is not carried on the shared stream; subscribe with { stream: 'keyed', key }`);
      }
      const sub: Sub = { topic, handler: handler as TopicHandler<unknown>, key, keyed };
      subs.add(sub);
      sync();
      let done = false;
      return () => {
        if (done) return;
        done = true;
        subs.delete(sub);
        sync();
      };
    },
    onResync(handler, opts = {}) {
      const r: ResyncSub = {
        handler,
        streamKey: opts.streamKey ?? '',
        coalesceMs: opts.coalesceMs ?? DEFAULT_RESYNC_COALESCE_MS,
        minIntervalMs: opts.minIntervalMs ?? DEFAULT_RESYNC_MIN_INTERVAL_MS,
        timer: null,
        pending: new Map(),
        lastCall: -Infinity,
      };
      resyncSubs.add(r);
      return () => stopResync(r);
    },
    retain() {
      if (disposed) throw new Error('event stream: retain after dispose');
      holds += 1;
      sync();
      let done = false;
      return () => {
        if (done) return;
        done = true;
        holds -= 1;
        sync();
      };
    },
    onClosed(listener) {
      closedListeners.add(listener);
      return () => void closedListeners.delete(listener);
    },
    onStatus(listener) {
      statusListeners.add(listener);
      return () => void statusListeners.delete(listener);
    },
    getStatus: () => sharedStatus,
    dispose() {
      disposed = true;
      stopVisibility();
      stopResume();
      subs.clear();
      holds = 0;
      for (const r of [...resyncSubs]) stopResync(r);
      backoffs.clear();
      for (const c of connections.values()) c.close('idle');
      connections.clear();
    },
  };
}

let shared: EventStream | null = null;

/** The tab's one EventStream, created on first use. Creating it opens nothing. */
export function getEventStream(): EventStream {
  shared ??= createEventStream();
  return shared;
}

/** Test seam: drop the shared instance (closing it) so the next getEventStream is fresh. */
export function resetSharedEventStream(): void {
  shared?.dispose();
  shared = null;
}
