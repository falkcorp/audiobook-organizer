// file: web/src/services/coreEvents/hooks.ts
// version: 0.1.0
// guid: daa7d3a7-97a0-41a0-b5e7-be17178a0384
// last-edited: 2026-11-19

// React bindings over the event-stream manager (05-realtime 4.8 item 6; the brief's
// `useTopic<T>(topic, onEvent, {key})` and `useResync(handler)`). The handler is held in
// a ref, so passing an inline arrow never resubscribes or reconnects.

import { useEffect, useLayoutEffect, useRef, useSyncExternalStore, type RefObject } from 'react';

import { getEventStream, type EventStream, type SubscribeOptions } from './manager';
import type {
  ClosedListener,
  PayloadOf,
  ResyncHandler,
  StreamStatus,
  TopicEvent,
} from './types';

/** Holds the latest value in a ref, written in a layout effect (never during render). */
function useLatest<T>(value: T): RefObject<T> {
  const ref = useRef(value);
  useLayoutEffect(() => {
    ref.current = value;
  });
  return ref;
}

export interface UseTopicOptions extends SubscribeOptions {
  /** Defaults to the tab's shared EventStream. */
  stream?: SubscribeOptions['stream'];
  /** Which EventStream to use (tests); defaults to the shared instance. */
  source?: EventStream;
  /** Set false to hold the subscription off (a panel that is closed). */
  enabled?: boolean;
}

/**
 * Subscribe while mounted. `key` filters on the client for the shared stream; with
 * `stream: 'keyed'` it opens a dedicated server-narrowed stream (a run's log lines).
 */
export function useTopic<T extends string>(
  topic: T,
  onEvent: (event: TopicEvent<PayloadOf<T>>) => void,
  opts: UseTopicOptions = {},
): void {
  const ref = useLatest(onEvent);
  const { stream, key, source, enabled = true } = opts;
  useEffect(() => {
    if (!enabled) return;
    const es = source ?? getEventStream();
    return es.subscribe(topic, (e) => ref.current(e as TopicEvent<PayloadOf<T>>), { stream, key });
  }, [topic, stream, key, source, enabled, ref]);
}

/** Run `handler` when the server says the stream lost events: refetch over REST. */
export function useResync(handler: ResyncHandler, source?: EventStream): void {
  const ref = useLatest(handler);
  useEffect(() => (source ?? getEventStream()).onResync((i) => ref.current(i)), [source, ref]);
}

/** Run `listener` when the server closes the stream (shutdown: show the backdrop). */
export function useStreamClosed(listener: ClosedListener, source?: EventStream): void {
  const ref = useLatest(listener);
  useEffect(() => (source ?? getEventStream()).onClosed((i) => ref.current(i)), [source, ref]);
}

/** The shared stream's status, for a status chip. */
export function useStreamStatus(source?: EventStream): StreamStatus {
  const es = source ?? getEventStream();
  return useSyncExternalStore(
    (cb) => es.onStatus(cb),
    () => es.getStatus(),
  );
}
