// file: web/src/services/coreEvents/hooks.test.tsx
// version: 0.1.0
// guid: 1aea078c-c253-45d0-a087-087eafc1bba5
// last-edited: 2026-11-19

import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { createFakeServer } from '../../test/eventStreamFake';
import { useResync, useStreamClosed, useStreamStatus, useTopic } from './hooks';
import { createEventStream, type EventStream } from './manager';
import { TOPIC_CLOSED, TOPIC_RESET } from './types';

const RUN = 'core.task.run-changed.v1';

let es: EventStream;
const tick = (ms = 0) => act(async () => void (await vi.advanceTimersByTimeAsync(ms)));

beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  es?.dispose();
  vi.useRealTimers();
});

function make() {
  const server = createFakeServer();
  es = createEventStream({
    visibility: { visible: true, onChange: () => () => {} },
    deps: { fetch: server.fetch, random: () => 0 },
  });
  return server;
}

describe('useTopic', () => {
  it('delivers events while mounted and aborts the stream on unmount', async () => {
    const server = make();
    const s = server.openStream();
    const seen: string[] = [];
    const { unmount } = renderHook(() => useTopic(RUN, (e) => seen.push(e.id), { source: es }));
    await tick();
    s.event('n-1', RUN, { id: 'run-1' }, 'run-1');
    await tick();
    expect(seen).toEqual(['n-1']);
    unmount();
    await tick();
    expect(server.calls[0].init.signal!.aborted).toBe(true);
  });

  it('an inline handler that changes every render does not reconnect, and the latest one runs', async () => {
    const server = make();
    const s = server.openStream();
    const seen: string[] = [];
    const { rerender } = renderHook(({ tag }) => useTopic(RUN, () => seen.push(tag), { source: es }), {
      initialProps: { tag: 'a' },
    });
    await tick();
    rerender({ tag: 'b' });
    rerender({ tag: 'c' });
    await tick();
    expect(server.calls).toHaveLength(1);
    s.event('n-1', RUN, {}, 'r');
    await tick();
    expect(seen).toEqual(['c']);
  });

  it('enabled:false holds the subscription (and the connection) off', async () => {
    const server = make();
    server.openStream();
    const { rerender } = renderHook(({ on }) => useTopic(RUN, () => {}, { source: es, enabled: on }), {
      initialProps: { on: false },
    });
    await tick(10);
    expect(server.calls).toHaveLength(0);
    rerender({ on: true });
    await tick();
    expect(server.calls).toHaveLength(1);
  });
});

describe('useResync, useStreamClosed, useStreamStatus', () => {
  it('runs the latest resync handler on reset and the closed listener on closed', async () => {
    const server = make();
    const s = server.openStream();
    server.openStream();
    const resyncs: string[] = [];
    const closes: string[] = [];
    renderHook(() => {
      useTopic(RUN, () => {}, { source: es });
      useResync((i) => resyncs.push(i.reason), es);
      useStreamClosed((i) => closes.push(i.reason), es);
    });
    await tick();
    s.control(TOPIC_RESET, { reason: 'gap' });
    s.control(TOPIC_CLOSED, { reason: 'shutdown' });
    await tick();
    expect(resyncs).toEqual(['gap']);
    expect(closes).toEqual(['shutdown']);
  });

  it('useStreamStatus re-renders with the stream state', async () => {
    const server = make();
    server.openStream();
    const { result } = renderHook(() => {
      useTopic(RUN, () => {}, { source: es });
      return useStreamStatus(es);
    });
    expect(result.current.state).toBe('idle');
    await tick();
    expect(result.current.state).toBe('connecting');
  });
});
