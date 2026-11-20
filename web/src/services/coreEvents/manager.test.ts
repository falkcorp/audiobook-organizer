// file: web/src/services/coreEvents/manager.test.ts
// version: 0.1.0
// guid: 8a5d279a-05ae-4745-9d86-aeabb2aadaff
// last-edited: 2026-11-19

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { createFakeServer, problemResponse } from '../../test/eventStreamFake';
import {
  createEventStream,
  DEFAULT_KEYED_ONLY_TOPICS,
  DEFAULT_RESYNC_COALESCE_MS,
  DEFAULT_RESYNC_MIN_INTERVAL_MS,
  type EventStream,
  type VisibilitySource,
} from './manager';
import { StreamRefusedError, TOPIC_CLOSED, TOPIC_OPENED, TOPIC_RESET, type ResyncInfo, type TopicEvent } from './types';

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
    expect(server.calls[0].url).toBe(`/api/core/v1/events?topic=*&exclude=${LOG}`);
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

  it('adding a subscriber never reconnects the shared stream (topic=* is filtered on the client)', async () => {
    const { server } = make();
    const s = server.openStream();
    es.subscribe(RUN, () => {});
    await tick();
    s.event('n-4', RUN, {}, 'r1');
    await tick();
    es.subscribe(BOOK, () => {});
    es.subscribe('*', () => {});
    await tick();
    expect(server.calls).toHaveLength(1);
    expect(server.calls[0].url).toBe(`/api/core/v1/events?topic=*&exclude=${LOG}`);
  });

  it('a wildcard on the shared stream still excludes the log topic server-side', async () => {
    const { server } = make();
    server.openStream();
    es.subscribe('core.*', () => {});
    await tick();
    expect(server.calls[0].url).toContain(`exclude=${encodeURIComponent(LOG)}`);
  });

  it('a hidden tab that reopens waits out a standing Retry-After instead of reconnecting at once', async () => {
    const { server, vis } = make();
    server.answer(() => problemResponse(429, 'rate_limited', 'x', { 'Retry-After': '30' }));
    server.openStream();
    es.subscribe(RUN, () => {});
    await tick();
    vis.set(false);
    await tick();
    vis.set(true);
    await tick(1000);
    expect(server.calls).toHaveLength(1);
    await tick(29000);
    expect(server.calls).toHaveLength(2);
  });

  it('runs the resync handlers when a hidden tab reopens, and on a first open with no resume id', async () => {
    const { server, vis } = make();
    const s1 = server.openStream();
    const s2 = server.openStream();
    const reasons: string[] = [];
    es.onResync((i) => reasons.push(i.reason), { coalesceMs: 0, minIntervalMs: 0 });
    es.subscribe(RUN, () => {});
    await tick();
    s1.control(TOPIC_OPENED, {});
    await tick();
    expect(reasons).toEqual(['reconnect']); // first open: no id to send
    s1.event('n-1', RUN, {}, 'r1');
    await tick();
    vis.set(false);
    await tick();
    vis.set(true);
    await tick();
    s2.control(TOPIC_OPENED, {});
    await tick();
    expect(server.calls[1].headers['last-event-id']).toBe('n-1');
    expect(reasons).toEqual(['reconnect', 'reconnect']); // reopen after hidden
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
      [`/api/core/v1/events?topic=${LOG}&key=run-joker-1`, `/api/core/v1/events?topic=*&exclude=${LOG}`].sort(),
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
    const raw = { streamKey: '*', coalesceMs: 0, minIntervalMs: 0 };
    es.onResync((i) => a.push(i), raw);
    const offB = es.onResync((i) => b.push(i), raw);
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

// #4785 r2 m1: the backoff outlives a connection closed for having no subscriber, and a
// server-wide refusal holds back every stream of the tab.
describe('backoff across connections', () => {
  it('unsubscribe then resubscribe under a Retry-After waits out the remainder', async () => {
    const { server } = make();
    server.answer(() => problemResponse(429, 'rate_limited', 'x', { 'Retry-After': '30' }));
    server.openStream();
    const off = es.subscribe(RUN, () => {});
    await tick();
    expect(server.calls).toHaveLength(1);
    off();
    await tick(); // sync runs: the connection is closed and dropped
    es.subscribe(RUN, () => {});
    await tick(1000);
    expect(server.calls).toHaveLength(1);
    expect(es.getStatus()).toMatchObject({ state: 'reconnecting', attempt: 1 });
    await tick(29000);
    expect(server.calls).toHaveLength(2);
  });

  it('a keyed stream reopened under its own Retry-After waits it out', async () => {
    const { server } = make();
    server.answer(() => problemResponse(503, 'unavailable', 'x', { 'Retry-After': '20' }));
    server.openStream();
    const off = es.subscribe(LOG, () => {}, { stream: 'keyed', key: 'run-bane-1' });
    await tick();
    off();
    await tick();
    es.subscribe(LOG, () => {}, { stream: 'keyed', key: 'run-bane-1' });
    await tick(19000);
    expect(server.calls).toHaveLength(1);
    await tick(1000);
    expect(server.calls).toHaveLength(2);
  });

  it('a 429 on the shared stream holds back a new keyed stream', async () => {
    const { server } = make();
    server.answer(() => problemResponse(429, 'rate_limited', 'x', { 'Retry-After': '30' }));
    server.openStream();
    es.subscribe(RUN, () => {});
    await tick();
    expect(server.calls).toHaveLength(1);
    es.subscribe(LOG, () => {}, { stream: 'keyed', key: 'run-riddler-1' });
    await tick(29000);
    expect(server.calls).toHaveLength(1);
    await tick(1000);
    expect(server.calls.some((c) => c.url.includes('key=run-riddler-1'))).toBe(true);
  });

  it('a shorter Retry-After never shortens a standing hold', async () => {
    const { server } = make();
    server.answer(() => problemResponse(429, 'rate_limited', 'x', { 'Retry-After': '40' }));
    server.answer(() => problemResponse(429, 'rate_limited', 'x', { 'Retry-After': '5' }));
    server.openStream();
    server.openStream();
    es.subscribe(RUN, () => {});
    es.subscribe(LOG, () => {}, { stream: 'keyed', key: 'run-scarecrow-1' });
    await tick();
    expect(server.calls).toHaveLength(2); // both in flight before either refusal
    await tick(39000);
    expect(server.calls).toHaveLength(2);
    await tick(1000);
    expect(server.calls).toHaveLength(4);
  });

  it("a refusal that is about one stream (400 unknown_topic) does not hold back the others", async () => {
    const { server } = make();
    server.answer(() => problemResponse(400, 'unknown_topic', 'x', { 'Retry-After': '30' }));
    server.openStream();
    es.subscribe(RUN, () => {});
    await tick();
    es.subscribe(LOG, () => {}, { stream: 'keyed', key: 'run-penguin-1' });
    await tick();
    expect(server.calls.some((c) => c.url.includes('key=run-penguin-1'))).toBe(true);
  });
});

// #4785 r2 m2: resync handlers filter by stream key, merge a burst and rate-limit a flap.
describe('resync filtering and pacing', () => {
  it('by default takes only the shared stream resets, a key takes only its own, * takes all', async () => {
    const { server } = make();
    const shared = server.openStream();
    const keyed = server.openStream();
    es.subscribe(RUN, () => {});
    es.subscribe(LOG, () => {}, { stream: 'keyed', key: 'run-1' });
    const fast = { coalesceMs: 0, minIntervalMs: 0 };
    const def: string[] = [];
    const own: string[] = [];
    const all: string[] = [];
    es.onResync((i) => def.push(`${i.streamKey}:${i.reason}`), fast);
    es.onResync((i) => own.push(`${i.streamKey}:${i.reason}`), { ...fast, streamKey: 'run-1' });
    es.onResync((i) => all.push(`${i.streamKey}:${i.reason}`), { ...fast, streamKey: '*' });
    await tick();
    shared.control(TOPIC_RESET, { reason: 'restart' });
    keyed.control(TOPIC_RESET, { reason: 'gap' });
    await tick();
    expect(def).toEqual([':restart']);
    expect(own).toEqual(['run-1:gap']);
    expect(all).toEqual([':restart', 'run-1:gap']);
  });

  it('opening a keyed stream (an id-less open) does not resync a shared-stream view', async () => {
    const { server } = make();
    server.openStream();
    const keyed = server.openStream();
    es.subscribe(RUN, () => {});
    const seen: ResyncInfo[] = [];
    es.onResync((i) => seen.push(i));
    await tick();
    es.subscribe(LOG, () => {}, { stream: 'keyed', key: 'run-2' });
    await tick();
    keyed.control(TOPIC_OPENED, {});
    await tick(DEFAULT_RESYNC_MIN_INTERVAL_MS);
    expect(seen).toEqual([]);
  });

  it('merges a burst of resets into one call with the latest reason', async () => {
    const { server } = make();
    const s = server.openStream();
    es.subscribe(RUN, () => {});
    const seen: string[] = [];
    es.onResync((i) => seen.push(i.reason));
    await tick();
    s.control(TOPIC_OPENED, {}); // first open: 'reconnect'
    s.control(TOPIC_RESET, { reason: 'overflow' });
    await tick(DEFAULT_RESYNC_COALESCE_MS - 1);
    expect(seen).toEqual([]);
    await tick(1);
    expect(seen).toEqual(['overflow']);
  });

  it('a steady run of resets does not starve the handler: the window is fixed from the first', async () => {
    const { server } = make();
    const s = server.openStream();
    es.subscribe(RUN, () => {});
    const at: number[] = [];
    const t0 = Date.now();
    es.onResync(() => at.push(Date.now() - t0), { minIntervalMs: 0 });
    await tick();
    for (let i = 0; i < 3; i++) {
      s.control(TOPIC_RESET, { reason: 'gap' });
      await tick(200);
    }
    await tick(1000);
    expect(at).toEqual([250, 650]);
  });

  it('a flapping stream refetches at most once per min interval', async () => {
    const { server } = make();
    const s = server.openStream();
    es.subscribe(RUN, () => {});
    const at: number[] = [];
    const t0 = Date.now();
    es.onResync(() => at.push(Date.now() - t0));
    await tick();
    for (let i = 0; i < 4; i++) {
      s.control(TOPIC_RESET, { reason: 'gap' });
      await tick(3000);
    }
    await tick(DEFAULT_RESYNC_MIN_INTERVAL_MS);
    // resets at 0, 3, 6, 9 s: calls at 0.25 s, then no closer than 5 s apart
    expect(at.length).toBe(3);
    for (let i = 1; i < at.length; i++) expect(at[i] - at[i - 1]).toBeGreaterThanOrEqual(DEFAULT_RESYNC_MIN_INTERVAL_MS);
  });

  it('unsubscribing cancels a pending call, and so does dispose', async () => {
    const { server } = make();
    const s = server.openStream();
    es.subscribe(RUN, () => {});
    const a: string[] = [];
    const b: string[] = [];
    const offA = es.onResync((i) => a.push(i.reason));
    es.onResync((i) => b.push(i.reason));
    await tick();
    s.control(TOPIC_RESET, { reason: 'gap' });
    await tick(10);
    const before = vi.getTimerCount();
    offA();
    expect(vi.getTimerCount()).toBe(before - 1);
    es.dispose();
    await tick(DEFAULT_RESYNC_MIN_INTERVAL_MS);
    expect(a).toEqual([]);
    expect(b).toEqual([]);
    expect(vi.getTimerCount()).toBe(0);
  });
});

// #4785 r2 m3: the log topic never rides the shared stream, and the F5-18 ordering.
describe('keyed-only topics', () => {
  it('pins the keyed-only list the shared stream excludes', () => {
    expect(DEFAULT_KEYED_ONLY_TOPICS).toEqual(['core.task.log-appended.v1']);
  });

  it('a log frame that arrives on the shared stream anyway reaches no shared subscriber, * included', async () => {
    const { server } = make();
    const s = server.openStream();
    const got: string[] = [];
    es.subscribe('*', (e) => got.push(`*:${e.topic}`));
    es.subscribe('core.*', (e) => got.push(`core:${e.topic}`));
    es.subscribe(RUN, (e) => got.push(`run:${e.topic}`));
    await tick();
    s.event('n-1', LOG, { line: 'Arkham log' }, 'run-1');
    s.event('n-2', RUN, {}, 'run-1');
    await tick();
    expect(got).toEqual([`*:${RUN}`, `core:${RUN}`, `run:${RUN}`]);
  });

  // ORDERING (F5-18): the server refuses an exclude naming an undeclared topic with
  // 400 unknown_topic. Until F5-18 declares core.task.log-appended.v1 the shared stream
  // is refused: the refusal must stay visible and retried, and keyed streams unaffected.
  // This pins the client side only; the server half is the flip's dispatch check.
  it('until the log topic is declared, the shared stream is refused visibly and retried, and keyed streams still open', async () => {
    const { server } = make();
    server.setFallback(() => problemResponse(400, 'unknown_topic', 'unknown topic core.task.log-appended.v1'));
    es.subscribe(RUN, () => {});
    await tick();
    expect(server.calls[0].url).toBe(`/api/core/v1/events?topic=*&exclude=${LOG}`);
    const status = es.getStatus();
    expect(status.state).toBe('reconnecting');
    expect(status.error).toBeInstanceOf(StreamRefusedError);
    expect((status.error as StreamRefusedError).code).toBe('unknown_topic');
    server.openStream();
    es.subscribe(LOG, () => {}, { stream: 'keyed', key: 'run-1' });
    await tick();
    expect(server.calls.at(-1)!.url).toBe(`/api/core/v1/events?topic=${LOG}&key=run-1`);
    await tick(3000);
    expect(server.calls.filter((c) => !c.url.includes('key=')).length).toBe(2);
  });
});

describe('retain', () => {
  it('holds the shared stream open with no topic subscriber, and the release closes it', async () => {
    const { server } = make();
    server.openStream();
    const release = es.retain();
    const other = es.retain();
    await tick();
    expect(server.calls).toHaveLength(1);
    expect(server.calls[0].url).toBe(`/api/core/v1/events?topic=*&exclude=${LOG}`);
    release();
    release(); // idempotent: the other holder still holds
    await tick();
    expect(server.calls[0].init.signal!.aborted).toBe(false);
    other();
    await tick();
    expect(server.calls[0].init.signal!.aborted).toBe(true);
    expect(es.getStatus().state).toBe('idle');
  });

  it('a retained stream stays open while a subscriber comes and goes', async () => {
    const { server } = make();
    server.openStream();
    es.retain();
    const off = es.subscribe(RUN, () => {});
    await tick();
    off();
    await tick();
    expect(server.calls).toHaveLength(1);
    expect(server.calls[0].init.signal!.aborted).toBe(false);
  });
});
