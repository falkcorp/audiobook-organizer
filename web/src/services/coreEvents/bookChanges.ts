// file: web/src/services/coreEvents/bookChanges.ts
// version: 0.1.0
// guid: a2b46131-c2e2-45fe-9153-3dd6857b7f1a
// last-edited: 2026-11-20

// Book-change adapter (F6-03 E2). The legacy hub sent one batched `books.changed`
// `{kind, ids[]}` per 250 ms window (internal/realtime/book_changes.go); the core topic
// `books.book.changed.v1` is one frame per book, `{kind, id}` keyed by the book ID
// (05-realtime 4.7). This folds the frames back into the batches Library's
// applyBooksChanged takes, with the coalescer's rules:
//  - a fixed window armed by the first change (a sliding debounce would never flush
//    during a bulk organize that changes a book every few ms);
//  - deleted supersedes created and updated; updated is dropped for an id already
//    created or deleted in the window;
//  - flush order deleted, created, updated; ids sorted; at most BOOK_CHANGE_MAX_IDS per
//    batch.
// What is pending when the consumer refetches over REST is already in the refetch, so the
// consumer drops it with clear() as it issues that request; the adapter does not guess
// the moment from a resync, which reaches the consumer up to 5 s later (#4813 r1 m3).
//
// UNWIRED: nothing in the running app imports this until the F6 flip.

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef } from 'react';

import { getEventStream, type EventStream } from './manager';
import type { BookChangeKind, TopicEvent, TopicPayloads } from './types';

export const BOOK_CHANGED_TOPIC = 'books.book.changed.v1';
/** The legacy coalescer's window (internal/server: NewBookChangeCoalescer(250ms)). */
export const BOOK_CHANGE_WINDOW_MS = 250;
/** The legacy coalescer's batch bound (BooksChangedMaxIDs). */
export const BOOK_CHANGE_MAX_IDS = 500;

/** One kind, many ids: the shape Library's applyBooksChanged takes (BooksChangedEvent). */
export interface BookChangeBatch {
  kind: BookChangeKind;
  ids: string[];
}

export type BookChangeBatchHandler = (batch: BookChangeBatch) => void;

export interface BookChangeBatcherOptions {
  windowMs?: number;
  maxIds?: number;
  /** A batch handler threw; the remaining batches are still delivered. Defaults to console.error. */
  onError?: (err: unknown, batch: BookChangeBatch) => void;
}

export interface BookChangeBatcher {
  /** Record one book's change; arms the window on the first change of a window. */
  add(kind: BookChangeKind, id: string): void;
  /** Deliver everything pending now and re-open the window. */
  flush(): void;
  /** Drop everything pending without delivering it (a refetch now covers it). */
  clear(): void;
  /** clear() and stop: later adds are ignored and no timer is left behind. */
  dispose(): void;
}

const defaultOnError = (err: unknown) => console.error('book change batch handler threw', err);
const defaultOnInvalid = (e: TopicEvent) => console.warn(`${BOOK_CHANGED_TOPIC}: malformed frame ${e.id}`, e.data);

const FLUSH_ORDER: readonly BookChangeKind[] = ['deleted', 'created', 'updated'];
const KINDS: ReadonlySet<string> = new Set<BookChangeKind>(FLUSH_ORDER);

export function createBookChangeBatcher(
  onBatch: BookChangeBatchHandler,
  options: BookChangeBatcherOptions = {},
): BookChangeBatcher {
  const windowMs = options.windowMs ?? BOOK_CHANGE_WINDOW_MS;
  const maxIds = Math.max(1, options.maxIds ?? BOOK_CHANGE_MAX_IDS);
  const onError = options.onError ?? defaultOnError;
  let pending = new Map<BookChangeKind, Set<string>>();
  let timer: ReturnType<typeof setTimeout> | null = null;
  let disposed = false;

  const setOf = (kind: BookChangeKind) => {
    let set = pending.get(kind);
    if (!set) {
      set = new Set();
      pending.set(kind, set);
    }
    return set;
  };

  const cancelTimer = () => {
    if (timer) clearTimeout(timer);
    timer = null;
  };

  const flush = () => {
    cancelTimer();
    const batch = pending;
    pending = new Map();
    for (const kind of FLUSH_ORDER) {
      const ids = [...(batch.get(kind) ?? [])].sort();
      for (let start = 0; start < ids.length; start += maxIds) {
        if (disposed) return; // a handler disposed it: deliver nothing more
        const b: BookChangeBatch = { kind, ids: ids.slice(start, start + maxIds) };
        try {
          onBatch(b);
        } catch (err) {
          onError(err, b);
        }
      }
    }
  };

  return {
    add(kind, id) {
      if (disposed || id === '') return;
      if (kind === 'deleted') {
        pending.get('created')?.delete(id);
        pending.get('updated')?.delete(id);
      } else if (kind === 'updated') {
        if (pending.get('created')?.has(id) || pending.get('deleted')?.has(id)) return;
      }
      setOf(kind).add(id);
      timer ??= setTimeout(flush, windowMs);
    },
    flush,
    clear() {
      cancelTimer();
      pending = new Map();
    },
    dispose() {
      disposed = true;
      cancelTimer();
      pending = new Map();
    },
  };
}

/** The frame's `{kind, id}` when it is well formed, else null (05-realtime 4.7). */
export function parseBookChange(data: unknown): TopicPayloads['books.book.changed.v1'] | null {
  if (typeof data !== 'object' || data === null) return null;
  const { kind, id } = data as { kind?: unknown; id?: unknown };
  if (typeof kind !== 'string' || !KINDS.has(kind)) return null;
  if (typeof id !== 'string' || id === '') return null;
  return { kind: kind as BookChangeKind, id };
}

export interface BookChangeSubscriptionOptions extends BookChangeBatcherOptions {
  /** A frame that is not a well-formed `{kind, id}`. Defaults to console.warn; never silent. */
  onInvalid?: (event: TopicEvent) => void;
}

export interface BookChangeSubscription {
  /**
   * Drop the pending window. Call it as the consumer issues the REST refetch that
   * replaces those changes (on a resync, say): delivered after it, a created book would
   * be counted twice.
   */
  clear(): void;
  /** Stop; leaves no listener or timer behind. Idempotent. */
  unsubscribe(): void;
}

/** Subscribe to books.book.changed.v1 on the shared stream and deliver batches. */
export function subscribeBookChanges(
  stream: EventStream,
  onBatch: BookChangeBatchHandler,
  options: BookChangeSubscriptionOptions = {},
): BookChangeSubscription {
  const batcher = createBookChangeBatcher(onBatch, options);
  const onInvalid = options.onInvalid ?? defaultOnInvalid;
  const offTopic = stream.subscribe(BOOK_CHANGED_TOPIC, (event) => {
    const change = parseBookChange(event.data);
    if (change) batcher.add(change.kind, change.id);
    else onInvalid(event);
  });
  let done = false;
  return {
    clear: () => batcher.clear(),
    unsubscribe() {
      if (done) return;
      done = true;
      offTopic();
      batcher.dispose();
    },
  };
}

export interface UseBookChangesOptions extends BookChangeSubscriptionOptions {
  /** Which EventStream to use (tests); defaults to the shared instance. */
  source?: EventStream;
  /** Set false to hold the subscription off. */
  enabled?: boolean;
}

export interface UseBookChangesResult {
  /** The live subscription's clear (see BookChangeSubscription); a no-op while none is live. Stable. */
  clear(): void;
}

/** Deliver book-change batches while mounted; inline callbacks never resubscribe. */
export function useBookChanges(onBatch: BookChangeBatchHandler, opts: UseBookChangesOptions = {}): UseBookChangesResult {
  const ref = useRef({ onBatch, onError: opts.onError, onInvalid: opts.onInvalid });
  useLayoutEffect(() => {
    ref.current = { onBatch, onError: opts.onError, onInvalid: opts.onInvalid };
  });
  const { source, enabled = true, windowMs, maxIds } = opts;
  const live = useRef<BookChangeSubscription | null>(null);
  useEffect(() => {
    if (!enabled) return;
    const sub = subscribeBookChanges(source ?? getEventStream(), (b) => ref.current.onBatch(b), {
      windowMs,
      maxIds,
      onError: (err, b) => (ref.current.onError ?? defaultOnError)(err, b),
      onInvalid: (e) => (ref.current.onInvalid ?? defaultOnInvalid)(e),
    });
    live.current = sub;
    return () => {
      sub.unsubscribe();
      if (live.current === sub) live.current = null;
    };
  }, [source, enabled, windowMs, maxIds]);
  const clear = useCallback(() => live.current?.clear(), []);
  return useMemo(() => ({ clear }), [clear]);
}
