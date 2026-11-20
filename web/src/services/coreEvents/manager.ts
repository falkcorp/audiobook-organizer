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
// Nothing connects until the first subscribe, and a stream with no subscriber is closed.

import { createStreamConnection, type ConnectionDeps, type StreamConnection } from './connection';
import {
  topicMatches,
  type ClosedListener,
  type PayloadOf,
  type ResyncHandler,
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

export interface VisibilitySource {
  readonly visible: boolean;
  /** Subscribe to changes; returns the unsubscribe. */
  onChange(listener: () => void): () => void;
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
  /** Called on core.stream.reset.v1 from any of this tab's streams: refetch over REST. */
  onResync(handler: ResyncHandler): () => void;
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
  };
}

export function createEventStream(options: EventStreamOptions = {}): EventStream {
  const keyedOnly = new Set(options.keyedOnlyTopics ?? DEFAULT_KEYED_ONLY_TOPICS);
  const visibility = options.visibility ?? documentVisibility();
  const subs = new Set<Sub>();
  const resyncHandlers = new Set<ResyncHandler>();
  const closedListeners = new Set<ClosedListener>();
  const statusListeners = new Set<StatusListener>();
  const connections = new Map<string, StreamConnection>(); // '' = shared; else the key
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
    const event: TopicEvent = { topic: env.topic, ts: env.ts, key: env.key ?? '', data: env.data, id };
    for (const s of [...subs]) {
      if (streamKey === '' ? s.keyed : !s.keyed || s.key !== streamKey) continue;
      if (!topicMatches(s.topic, env.topic)) continue;
      if (!s.keyed && s.key !== '' && s.key !== event.key) continue;
      guard(() => s.handler(event), env.topic);
    }
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
        for (const h of [...resyncHandlers]) guard(() => h({ reason, streamKey }), 'core.stream.reset.v1');
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
      deps: options.deps,
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
    });
  };
  const signatures = new Map<string, string>();

  const stopVisibility = visibility.onChange(() => {
    // A hidden stream keeps its signature so becoming visible reopens, not restarts.
    for (const k of connections.keys()) signatures.delete(k);
    sync();
  });

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
    onResync(handler) {
      resyncHandlers.add(handler);
      return () => void resyncHandlers.delete(handler);
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
      subs.clear();
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
