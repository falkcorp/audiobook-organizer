// file: core/web/packages/shell/src/testing/FakeRealtime.ts
// version: 0.1.0
// guid: 4f8c2a91-7d36-4e05-b1a9-3c6e0d8f2b57
// last-edited: 2026-11-20

import type { CloseReason, RealtimeListener, RealtimeSource, ResetReason } from '../contracts';

/**
 * A RealtimeSource a test drives by hand (07 §7.4.4). The control vocabulary
 * is 05-realtime §4.2's (FE8-9): reset and close are the two control frames,
 * fail a transport error.
 */
export class FakeRealtime implements RealtimeSource {
  private listeners = new Set<RealtimeListener>();
  /** The topics the mounted useTopic hooks asked for, as the provider last set them. */
  topics: readonly string[] = [];
  started = false;

  subscribe(listener: RealtimeListener): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }
  setTopics(topics: readonly string[]): void {
    this.topics = [...topics];
  }
  start(): void {
    this.started = true;
  }
  stop(): void {
    this.started = false;
  }

  emit<T>(topic: string, data: T, key?: string): void {
    for (const l of [...this.listeners]) l.onEvent({ topic, data, ...(key === undefined ? {} : { key }) });
  }
  /** A core.stream.reset.v1 frame. */
  reset(reason: ResetReason): void {
    for (const l of [...this.listeners]) l.onReset(reason);
  }
  /** A core.stream.closed.v1 frame. */
  close(reason: CloseReason): void {
    for (const l of [...this.listeners]) l.onClosed(reason);
  }
  /** A transport error, not a control frame. */
  fail(status?: number): void {
    for (const l of [...this.listeners]) l.onError?.(status);
  }
}
