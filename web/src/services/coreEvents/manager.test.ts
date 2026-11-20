// file: web/src/services/coreEvents/manager.test.ts
// version: 0.1.0
// guid: 8a5d279a-05ae-4745-9d86-aeabb2aadaff
// last-edited: 2026-11-19

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { createFakeServer } from '../../test/eventStreamFake';
import { createEventStream, type EventStream, type VisibilitySource } from './manager';
import { TOPIC_CLOSED, TOPIC_RESET, type TopicEvent } from './types';

const RUN = 'core.task.run-changed.v1';
const BOOK = 'books.book.changed.v1';
const LOG = 'core.task.log-appended.v1';

function fakeVisibility() {
  let visible = true;
  const listeners = new Set<() => void>();
  const src: VisibilitySource = {
    get visible() {
      return visible;
    },
    onChange(l) {
      listeners.add(l);
      return () => listeners.delete(l);
    },
  };
  return {
    src,
    set(v: boolean) {
      visible = v;
      listeners.forEach((l) => l());
    },
  };
}

const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms);

let es: EventStream;
beforeEach(() => vi.useFakeTimers());
afterEach(() => {
  es?.dispose();
  vi.useRealTimers();
});

function make() {
  const server = createFakeServer();
  const vis = fakeVisibility();
  es = createEventStream({ visibility: vis.src, deps: { fetch: server.fetch, random: () => 0 } });
  return { server, vis };
}

describe('connection lifecycle', () => {
  it('opens nothing until the first subscribe, and coalesces a burst of subscribes into one request', async () => {
    const { server } = make();
    await tick(10);
    expect(server.calls).toHaveLength(0);
    server.openStream();
    es.subscribe(RUN, () => {});
    es.subscribe(BOOK, () => {});
    es.subscribe(RUN, () => {});
    await tick();
    expect(server.calls).toHaveLength(1);
    expect(server.calls[0].url).toBe(`/api/core/v1/events?topic=${BOOK}&topic=${RUN}`);
  });

  it('opens a stream only while the tab is visible, closes it on hidden and reopens on visible, resuming', async () => {
    const { server, vis } = make();
    vis.set(false);
    es.subscribe(RUN, () => {});
    const s = server.openStream();
    server.openStream();
    await tick(10);
    expect(server.calls).toHaveLength(0);
    expect(es.getStatus().state).toBe('hidden');

    vis.set(true);
    await tick();
    expect(server.calls).toHaveLength(1);
    s.event('n-1', RUN, { id: 'r1' }, 'r1');
    await tick();

    vis.set(false);
    await tick();
    expect(server.calls[0].init.signal!.aborted).toBe(true);
    expect(es.getStatus().state).toBe('hidden');
    await tick(120000);
    expect(server.calls).toHaveLength(1);

    vis.set(true);
    await tick();
    expect(server.calls).toHaveLength(2);
    expect(server.calls[1].headers['last-event-id']).toBe('n-1');
  });

  it('closes the stream when the last subscriber leaves', async () => {
    const { server } = make();
    server.openStream();
    const off = es.subscribe(RUN, () => {});
    await tick();
    off();
    off(); // idempotent
    await tick();
    expect(server.calls[0].init.signal!.aborted).toBe(true);
    expect(es.getStatus().state).toBe('idle');
  });

  it('restarts with the wider topic set, resuming from the last id, when a subscriber is added', async () => {
    const { server } = make();
    const s = server.openStream();
    server.openStream();
    es.subscribe(RUN, () => {});
    await tick();
    s.event('n-4', RUN, {}, 'r1');
    await tick();
    es.subscribe(BOOK, () => {});
    await tick();
    expect(server.calls).toHaveLength(2);
    expect(server.calls[1].url).toBe(`/api/core/v1/events?topic=${BOOK}&topic=${RUN}`);
    expect(server.calls[1].headers['last-event-id']).toBe('n-4');
  });
});

describe('dispatch', () => {
  it('delivers by exact topic, module wildcard and *, and nothing for other topics', async () => {
    const { server } = make();
    const s = server.openStream();
    const got: Record<string, string[]> = { exact: [], mod: [], all: [], other: [] };
    es.subscribe(RUN, (e) => got.exact.push(e.id));
    es.subscribe('core.*', (e) => got.mod.push(e.id));
    es.subscribe('*', (e) => got.all.push(e.id));
    es.subscribe(BOOK, (e) => got.other.push(e.id));
    await tick();
    s.event('n-1', RUN, {}, 'r1');
    await tick();
    expect(got).toEqual({ exact: ['n-1'], mod: ['n-1'], all: ['n-1'], other: [] });
  });

  it('hands the handler a typed event with topic, key, data and the resume id', async () => {
    const { server } = make();
    const s = server.openStream();
    let ev: TopicEvent<{ kind: string; id: string }> | undefined;
    es.subscribe(BOOK, (e) => (ev = e));
    await tick();
    s.event('n-1', BOOK, { kind: 'updated', id: 'bk-dark-knight-returns' }, 'bk-dark-knight-returns');
    await tick();
    expect(ev).toMatchObject({
      topic: BOOK,
      key: 'bk-dark-knight-returns',
      id: 'n-1',
      data: { kind: 'updated', id: 'bk-dark-knight-returns' },
    });
  });

  it('a client-side key filters the shared stream', async () => {
    const { server } = make();
    const s = server.openStream();
    const seen: string[] = [];
    es.subscribe(RUN, (e) => seen.push(e.key), { key: 'run-2' });
    await tick();
    s.event('n-1', RUN, {}, 'run-1');
    s.event('n-2', RUN, {}, 'run-2');
    await tick();
    expect(seen).toEqual(['run-2']);
  });

  it('a throwing handler does not stop the others or the stream', async () => {
    const server = createFakeServer();
    const errors: unknown[] = [];
    es = createEventStream({
      visibility: fakeVisibility().src,
      deps: { fetch: server.fetch, random: () => 0 },
      onHandlerError: (e) => errors.push(e),
    });
    const s = server.openStream();
    const seen: string[] = [];
    es.subscribe(RUN, () => {
      throw new Error('boom');
    });
    es.subscribe(RUN, (e) => seen.push(e.id));
    await tick();
    s.event('n-1', RUN, {}, 'r');
    s.event('n-2', RUN, {}, 'r');
    await tick();
    expect(seen).toEqual(['n-1', 'n-2']);
    expect(errors).toHaveLength(2);
  });
});

describe('keyed streams', () => {
  it('refuses the log topic on the shared stream and a keyed stream with no key', () => {
    make();
    expect(() => es.subscribe(LOG, () => {})).toThrow(/keyed/);
    expect(() => es.subscribe(LOG, () => {}, { stream: 'keyed' })).toThrow(/needs a key/);
  });

  it('opens a separate server-keyed stream, delivers only to its subscribers, and closes it on unsubscribe', async () => {
    const { server } = make();
    const shared = server.openStream();
    const keyed = server.openStream();
    const runSeen: string[] = [];
    const logSeen: string[] = [];
    es.subscribe(RUN, (e) => runSeen.push(e.id));
    const off = es.subscribe(LOG, (e) => logSeen.push(e.id), { stream: 'keyed', key: 'run-joker-1' });
    await tick();
    expect(server.calls.map((c) => c.url).sort()).toEqual(
      [`/api/core/v1/events?topic=${LOG}&key=run-joker-1`, `/api/core/v1/events?topic=${RUN}`].sort(),
    );
    shared.event('n-1', RUN, {}, 'run-joker-1');
    keyed.event('n-1', LOG, { line: 'he laughs' }, 'run-joker-1');
    await tick();
    expect(runSeen).toEqual(['n-1']);
    expect(logSeen).toEqual(['n-1']);
    const keyedCall = server.calls.find((c) => c.url.includes('key='))!;
    off();
    await tick();
    expect(keyedCall.init.signal!.aborted).toBe(true);
    expect(server.calls.find((c) => !c.url.includes('key='))!.init.signal!.aborted).toBe(false);
  });

  it('two subscribers on one key share one keyed stream', async () => {
    const { server } = make();
    server.openStream();
    es.subscribe(LOG, () => {}, { stream: 'keyed', key: 'run-1' });
    es.subscribe(LOG, () => {}, { stream: 'keyed', key: 'run-1' });
    await tick();
    expect(server.calls).toHaveLength(1);
  });
});

describe('resync and closed', () => {
  it('runs every resync handler with the reason on core.stream.reset.v1, from any stream', async () => {
    const { server } = make();
    const shared = server.openStream();
    const keyed = server.openStream();
    es.subscribe(RUN, () => {});
    es.subscribe(LOG, () => {}, { stream: 'keyed', key: 'run-1' });
    const a: unknown[] = [];
    const b: unknown[] = [];
    es.onResync((i) => a.push(i));
    const offB = es.onResync((i) => b.push(i));
    await tick();
    shared.control(TOPIC_RESET, { reason: 'restart' });
    keyed.control(TOPIC_RESET, { reason: 'gap' });
    await tick();
    expect(a).toEqual([
      { reason: 'restart', streamKey: '' },
      { reason: 'gap', streamKey: 'run-1' },
    ]);
    offB();
    shared.control(TOPIC_RESET, { reason: 'overflow' });
    await tick();
    expect(a).toHaveLength(3);
    expect(b).toHaveLength(2);
  });

  it('reports a closed frame to onClosed and then reconnects', async () => {
    const { server } = make();
    const s = server.openStream();
    server.openStream();
    es.subscribe(RUN, () => {});
    const closed: unknown[] = [];
    es.onClosed((i) => closed.push(i));
    await tick();
    s.control(TOPIC_CLOSED, { reason: 'shutdown' });
    s.end();
    await tick(3000);
    expect(closed).toEqual([{ reason: 'shutdown', streamKey: '' }]);
    expect(server.calls).toHaveLength(2);
  });
});

describe('status', () => {
  it('shows a refusal as reconnecting with the error, so a failed stream is not a quiet one', async () => {
    const { server } = make();
    const seen: string[] = [];
    es.onStatus((s) => seen.push(s.state));
    es.subscribe(RUN, () => {});
    await tick();
    expect(es.getStatus().state).toBe('reconnecting');
    expect((es.getStatus().error as Error).message).toMatch(/503/);
    expect(seen).toContain('connecting');
    expect(server.calls).toHaveLength(1);
  });
});

describe('dispose', () => {
  it('aborts every stream and refuses later subscribes', async () => {
    const { server } = make();
    server.openStream();
    es.subscribe(RUN, () => {});
    await tick();
    es.dispose();
    expect(server.calls[0].init.signal!.aborted).toBe(true);
    expect(() => es.subscribe(RUN, () => {})).toThrow(/dispose/);
  });
});
