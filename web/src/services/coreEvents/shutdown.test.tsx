// file: web/src/services/coreEvents/shutdown.test.tsx
// version: 0.1.0
// guid: c99d2748-e849-40ed-a488-502700448e8b
// last-edited: 2026-11-20

import { act, renderHook } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { createFakeServer } from '../../test/eventStreamFake';
import { createEventStream, type EventStream } from './manager';
import { useServerShutdown, watchServerShutdown } from './shutdown';
import { TOPIC_CLOSED, TOPIC_OPENED } from './types';

const LOG = 'core.task.log-appended.v1';
const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms);

let es: EventStream;
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

describe('watchServerShutdown', () => {
  it('opens the shared stream by itself and fires on closed{shutdown}', async () => {
    const server = make();
    const s = server.openStream();
    const seen: string[] = [];
    watchServerShutdown(es, (i) => seen.push(i.streamKey));
    await tick();
    expect(server.calls).toHaveLength(1);
    expect(server.calls[0].url).toBe(`/api/core/v1/events?topic=*&exclude=${LOG}`);
    s.control(TOPIC_CLOSED, { reason: 'shutdown' });
    await tick();
    expect(seen).toEqual(['']);
  });

  it('ignores every other closed reason', async () => {
    const server = make();
    const s = server.openStream();
    const seen: string[] = [];
    watchServerShutdown(es, (i) => seen.push(i.streamKey));
    await tick();
    s.control(TOPIC_CLOSED, { reason: 'credential_revoked' });
    s.control(TOPIC_CLOSED, { reason: 'permission_revoked' });
    await tick();
    expect(seen).toEqual([]);
  });

  it('fires once when the shared and a keyed stream both announce it, and re-arms on the next shared open', async () => {
    const server = make();
    const shared = server.openStream();
    const keyed = server.openStream();
    const seen: string[] = [];
    watchServerShutdown(es, (i) => seen.push(i.streamKey));
    es.subscribe(LOG, () => {}, { stream: 'keyed', key: 'run-1' });
    await tick();
    shared.control(TOPIC_CLOSED, { reason: 'shutdown' });
    keyed.control(TOPIC_CLOSED, { reason: 'shutdown' });
    await tick();
    expect(seen).toEqual(['']);

    const next = server.openStream();
    shared.end();
    await tick(3000);
    next.control(TOPIC_OPENED, {});
    next.control(TOPIC_CLOSED, { reason: 'shutdown' });
    await tick();
    expect(seen).toEqual(['', '']);
  });

  it('a keyed stream opening does not re-arm it', async () => {
    const server = make();
    const shared = server.openStream();
    const keyed = server.openStream();
    const seen: string[] = [];
    watchServerShutdown(es, (i) => seen.push(i.streamKey));
    await tick();
    shared.control(TOPIC_CLOSED, { reason: 'shutdown' });
    await tick();
    es.subscribe(LOG, () => {}, { stream: 'keyed', key: 'run-2' });
    await tick();
    keyed.control(TOPIC_OPENED, {});
    keyed.control(TOPIC_CLOSED, { reason: 'shutdown' });
    await tick();
    expect(seen).toEqual(['']);
  });

  it('the detach is silent afterwards and releases the stream', async () => {
    const server = make();
    const s = server.openStream();
    const seen: string[] = [];
    const detach = watchServerShutdown(es, (i) => seen.push(i.streamKey));
    await tick();
    detach();
    detach(); // idempotent
    await tick();
    expect(server.calls[0].init.signal!.aborted).toBe(true);
    s.control(TOPIC_CLOSED, { reason: 'shutdown' });
    await tick();
    expect(seen).toEqual([]);
  });

  it('counts its registrations to zero on detach (the leak check)', () => {
    const live = { closed: 0, status: 0, retain: 0 };
    const reg = (k: keyof typeof live) => () => {
      live[k]++;
      return () => void live[k]--;
    };
    const fake = { onClosed: reg('closed'), onStatus: reg('status'), retain: reg('retain') } as unknown as EventStream;
    const detach = watchServerShutdown(fake, () => {});
    expect(live).toEqual({ closed: 1, status: 1, retain: 1 });
    detach();
    detach();
    expect(live).toEqual({ closed: 0, status: 0, retain: 0 });
  });
});

describe('useServerShutdown', () => {
  it('mounted alone it opens the stream, runs the latest listener, and unmount detaches', async () => {
    const server = make();
    const s = server.openStream();
    const seen: string[] = [];
    const { rerender, unmount } = renderHook(({ tag }) => useServerShutdown(() => seen.push(tag), es), {
      initialProps: { tag: 'a' },
    });
    await act(() => tick());
    expect(server.calls).toHaveLength(1);
    rerender({ tag: 'b' });
    s.control(TOPIC_CLOSED, { reason: 'shutdown' });
    await act(() => tick());
    expect(seen).toEqual(['b']);
    expect(server.calls).toHaveLength(1);
    unmount();
    await act(() => tick());
    expect(server.calls[0].init.signal!.aborted).toBe(true);
  });
});
