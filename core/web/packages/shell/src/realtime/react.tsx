// file: core/web/packages/shell/src/realtime/react.tsx
// version: 0.1.0
// guid: a6104402-2cc1-4d8a-94fa-dddefe599058
// last-edited: 2026-11-20

// RealtimeProvider, useTopic and useResync (07 §7.4.2 signatures). The provider
// consumes a RealtimeSource (the real client, or @mediacore/shell/testing's
// FakeRealtime), keeps a registry of what the mounted components listen to and
// tells the source the union of the topics in use.

import { createContext, useContext, useEffect, useRef, useState, type ReactNode } from 'react';

import type { CloseReason, RealtimeEvent, RealtimeSource } from '../contracts';

interface TopicSub {
  topic: string;
  key: string | undefined;
  deliver: (data: unknown) => void;
}

/** What the hooks register with; outlives re-renders and source swaps. */
class Registry {
  private readonly subs = new Set<TopicSub>();
  private readonly resyncs = new Set<() => void>();
  /** Set by the provider while a source is attached. */
  onTopicsChanged: ((topics: readonly string[]) => void) | null = null;
  private sent: string | null = null;

  topics(): string[] {
    return [...new Set([...this.subs].map((s) => s.topic))];
  }

  addTopic(sub: TopicSub): () => void {
    this.subs.add(sub);
    this.publish();
    return () => {
      this.subs.delete(sub);
      this.publish();
    };
  }

  /** Sends the union to the source, but only when it differs from what the source already has. */
  publish(force = false): void {
    const topics = this.topics().sort();
    const signature = JSON.stringify(topics);
    if (!force && signature === this.sent) return;
    this.sent = signature;
    this.onTopicsChanged?.(topics);
  }

  addResync(fn: () => void): () => void {
    this.resyncs.add(fn);
    return () => {
      this.resyncs.delete(fn);
    };
  }

  dispatch(event: RealtimeEvent): void {
    for (const sub of [...this.subs]) {
      if (sub.topic !== event.topic) continue;
      if (sub.key !== undefined && sub.key !== event.key) continue;
      sub.deliver(event.data);
    }
  }

  resync(): void {
    for (const fn of [...this.resyncs]) fn();
  }
}

const RealtimeContext = createContext<Registry | null>(null);

function useRegistry(hook: string): Registry {
  const registry = useContext(RealtimeContext);
  if (registry === null) throw new Error(`${hook} must be rendered inside a RealtimeProvider`);
  return registry;
}

export interface RealtimeProviderProps {
  source: RealtimeSource;
  children?: ReactNode;
  /** A core.stream.closed.v1 frame: the shell's auth rule and shutdown backdrop hang here. */
  onClosed?: (reason: CloseReason) => void;
  /** A transport failure (the client is retrying); a status chip hangs here. */
  onError?: (status?: number) => void;
}

/**
 * Starts the source on mount and stops it on unmount, so a source is owned by at most one provider
 * at a time. Calls `source.setTopics` with the union of the topics in use.
 */
export function RealtimeProvider({ source, children, onClosed, onError }: RealtimeProviderProps) {
  const [registry] = useState(() => new Registry());
  // The latest callbacks, so a parent re-render never tears the subscription down.
  const handlers = useRef({ onClosed, onError });
  useEffect(() => {
    handlers.current = { onClosed, onError };
  });

  useEffect(() => {
    registry.onTopicsChanged = (topics) => source.setTopics(topics);
    registry.publish(true); // a new source has been told nothing yet
    const unsubscribe = source.subscribe({
      onEvent: (e) => registry.dispatch(e),
      onReset: () => registry.resync(),
      onClosed: (reason) => handlers.current.onClosed?.(reason),
      onError: (status) => handlers.current.onError?.(status),
    });
    source.start();
    return () => {
      registry.onTopicsChanged = null;
      unsubscribe();
      source.stop();
    };
  }, [registry, source]);

  return <RealtimeContext.Provider value={registry}>{children}</RealtimeContext.Provider>;
}

/**
 * Calls `onEvent` with the payload of every `topic` event (and, when `opts.key` is given, only those
 * for that key). The latest `onEvent` is always the one called; a re-render does not resubscribe.
 */
export function useTopic<T>(topic: string, onEvent: (data: T) => void, opts?: { key?: string }): void {
  const registry = useRegistry('useTopic');
  const latest = useRef(onEvent);
  useEffect(() => {
    latest.current = onEvent;
  });
  const key = opts?.key;
  useEffect(
    () => registry.addTopic({ topic, key, deliver: (data) => latest.current(data as T) }),
    [registry, topic, key],
  );
}

/** Calls `onResync` on `core.stream.reset.v1` and after a reconnect that could not resume. */
export function useResync(onResync: () => void): void {
  const registry = useRegistry('useResync');
  const latest = useRef(onResync);
  useEffect(() => {
    latest.current = onResync;
  });
  useEffect(() => registry.addResync(() => latest.current()), [registry]);
}
