// file: core/web/packages/shell/test/realtime.resume.test.ts
// version: 0.1.0
// guid: 902c0852-6588-4324-9b38-5458f2251ea6
// last-edited: 2026-11-20

// 07 §7.18 realtime.resume: the fetch-based SSE reader, its resume and backoff,
// the control frames, and the hooks over a RealtimeSource.

import { ApiAuthRedirectError } from '@mediacore/api-client';
import { act, render } from '@testing-library/react';
import { createElement, useState } from 'react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import type { RealtimeEvent, RealtimeListener, RealtimeSource, ResetReason } from '../src/contracts';
import { createRealtimeClient, RealtimeProvider, useResync, useTopic, type RealtimeClientOptions } from '../src/realtime';
import { backoffDelay, retryAfterMs } from '../src/realtime/backoff';
import { closedFrame, FakeFetch, frame, openedFrame, recordingListener, resetFrame } from '../src/realtime/testing-fakes';

beforeEach(() => {
  vi.useFakeTimers({ toFake: ['setTimeout', 'clearTimeout'] });
});
afterEach(() => {
  vi.useRealTimers();
});

const settle = () => vi.advanceTimersByTimeAsync(0);
const advance = (ms: number) => vi.advanceTimersByTimeAsync(ms);

const GADGETS = 'batcave.gadgets.v1';
const COWL = 'batcave.cowl.v1';

/** A client with no locks and no channel: one stream, no election, so every call here is this tab's. */
function setup(over: Partial<RealtimeClientOptions> = {}) {
  const server = new FakeFetch();
  const rec = recordingListener();
  const client = createRealtimeClient({
    appId: 'batcave',
    fetch: server.fetch,
    locks: null,
    channel: () => null,
    win: null,
    doc: null,
    random: () => 1,
    ...over,
  });
  client.subscribe(rec.listener);
  return { server, rec, client };
}

describe('the request', () => {
  it('opens the events URL with cookies, manual redirects and the shared subscription set', async () => {
    const { server, client } = setup({ baseUrl: '/gotham' });
    client.start();
    await settle();
    const call = server.calls[0];
    expect(call.url).toBe('/gotham/api/core/v1/events?topic=*&exclude=core.task.log-appended.v1');
    expect(call.init.credentials).toBe('same-origin');
    expect(call.init.redirect).toBe('manual');
    expect(call.init.method).toBe('GET');
    expect(call.lastEventId).toBeNull();
  });

  it('start() twice is one connection', async () => {
    const { server, client } = setup();
    client.start();
    client.start();
    await settle();
    expect(server.calls).toHaveLength(1);
  });
});

describe('resume', () => {
  it('a reconnect sends the Last-Event-ID of the last event seen', async () => {
    const { server, rec, client } = setup();
    client.start();
    await settle();
    const stream = server.calls[0].stream!;
    stream.push(openedFrame());
    stream.push(frame(GADGETS, { n: 1 }, { id: 'wayne-1' }) + frame(GADGETS, { n: 2 }, { id: 'wayne-2' }));
    // A control frame has no id: it must not move the resume point.
    stream.push(closedFrame('shutdown'));
    stream.end();
    await settle();

    expect(rec.events.map((e) => e.data)).toEqual([{ n: 1 }, { n: 2 }]);
    expect(rec.closed).toEqual(['shutdown']);
    await advance(2999);
    expect(server.calls).toHaveLength(1);
    await advance(1);
    expect(server.calls).toHaveLength(2);
    expect(server.calls[1].lastEventId).toBe('wayne-2');
    expect(rec.resets).toEqual([]);
  });

  it.each<ResetReason>(['restart', 'gap', 'overflow'])(
    'a reconnect the server cannot resume (reset %s) calls onReset and the stream carries on',
    async (reason) => {
      const { server, rec, client } = setup();
      client.start();
      await settle();
      server.calls[0].stream!.push(frame(GADGETS, { n: 1 }, { id: 'wayne-1' }));
      server.calls[0].stream!.end();
      await advance(3000);
      expect(server.calls[1].lastEventId).toBe('wayne-1');

      server.calls[1].stream!.push(resetFrame(reason) + frame(GADGETS, { n: 2 }, { id: 'wayne-9' }));
      await settle();
      expect(rec.resets).toEqual([reason]);
      expect(rec.events.map((e) => e.data)).toEqual([{ n: 1 }, { n: 2 }]);
    },
  );

  it('a reconnect with nothing to resume from refetches, and the first connect does not', async () => {
    const { server, rec, client } = setup();
    client.start();
    await settle();
    server.calls[0].stream!.push(openedFrame());
    await settle();
    expect(rec.resets).toEqual([]);

    server.calls[0].stream!.end(); // no event ever carried an id
    await advance(3000);
    expect(server.calls[1].lastEventId).toBeNull();
    expect(rec.resets).toEqual(['gap']);
  });

  it('an unknown reset reason still refetches', async () => {
    const { server, rec, client } = setup();
    client.start();
    await settle();
    server.calls[0].stream!.push(resetFrame('meteor'));
    await settle();
    expect(rec.resets).toEqual(['gap']);
  });
});

describe('framing', () => {
  it('reads CRLF split across chunks, comments, multi-line data and ignores event: lines', async () => {
    const { server, rec, client } = setup();
    client.start();
    await settle();
    const s = server.calls[0].stream!;
    s.push(': hb\n\n');
    s.push(`id: wayne-1\r`);
    s.push(`\nevent: ignored\r\ndata: {"topic":"${GADGETS}","data":{"n":1}}\r\n\r\n`);
    s.push(`data: {"topic":"${GADGETS}",\ndata: "data":{"n":2}}\n\n`);
    await settle();
    expect(rec.events).toEqual([
      { topic: GADGETS, data: { n: 1 } },
      { topic: GADGETS, data: { n: 2 } },
    ]);
    s.end();
    await advance(3000);
    expect(server.calls[1].lastEventId).toBe('wayne-1');
  });

  it('passes the key through and reports a malformed frame without dropping the stream', async () => {
    const { server, rec, client } = setup();
    client.start();
    await settle();
    const s = server.calls[0].stream!;
    s.push('data: {not json\n\n');
    s.push(frame(GADGETS, { n: 1 }, { id: 'wayne-1', key: 'robin-1' }));
    await settle();
    expect(rec.errors).toEqual([undefined]);
    expect(rec.events).toEqual([{ topic: GADGETS, key: 'robin-1', data: { n: 1 } }]);
    expect(server.calls).toHaveLength(1);
  });

  it('setTopics filters what this tab hears, not what the stream carries', async () => {
    const { server, rec, client } = setup();
    client.start();
    await settle();
    const s = server.calls[0].stream!;
    s.push(frame(COWL, 0, { id: 'wayne-1' }));
    await settle();
    expect(rec.events).toHaveLength(1); // nothing declared yet: everything is heard

    client.setTopics([GADGETS]);
    s.push(frame(COWL, 1, { id: 'wayne-2' }) + frame(GADGETS, 2, { id: 'wayne-3' }));
    await settle();
    expect(rec.events.map((e) => e.data)).toEqual([0, 2]);
    // The filtered frame still moved the resume point.
    s.end();
    await advance(3000);
    expect(server.calls[1].lastEventId).toBe('wayne-3');
    expect(server.calls[1].url).toBe(server.calls[0].url);
  });
});

describe('backoff', () => {
  it('grows from 3 s doubling to 60 s and stays there', async () => {
    const { server, rec, client } = setup();
    server.plan = Array.from({ length: 8 }, () => 'fail' as const);
    client.start();
    await settle();
    let calls = 1;
    for (const wait of [3000, 6000, 12000, 24000, 48000, 60000, 60000]) {
      await advance(wait - 1);
      expect(server.calls, `before ${wait} ms`).toHaveLength(calls);
      await advance(1);
      calls++;
      expect(server.calls, `at ${wait} ms`).toHaveLength(calls);
    }
    expect(rec.errors).toEqual(Array.from({ length: 8 }, () => undefined)); // every failure surfaced
    await advance(59999);
    expect(server.calls).toHaveLength(8);
    await advance(1);
    expect(server.calls).toHaveLength(9); // the ninth attempt succeeded: there is no give-up
  });

  it('jitters downwards only: a quarter of the ceiling at most', async () => {
    const { server, client } = setup({ random: () => 0 });
    server.plan = ['fail', 'fail', 'fail'];
    client.start();
    await settle();
    await advance(2249);
    expect(server.calls).toHaveLength(1);
    await advance(1);
    expect(server.calls).toHaveLength(2);
    await advance(4499);
    expect(server.calls).toHaveLength(2);
    await advance(1);
    expect(server.calls).toHaveLength(3);
    expect(backoffDelay(0, () => 0.999)).toBeLessThanOrEqual(3000);
    expect(backoffDelay(30, () => 0.999)).toBeLessThanOrEqual(60000);
  });

  it('starts over from 3 s once a stream has been live', async () => {
    const { server, client } = setup();
    server.plan = ['fail', 'fail', 'accept'];
    client.start();
    await advance(3000);
    await advance(6000);
    expect(server.calls).toHaveLength(3);
    server.calls[2].stream!.push(openedFrame());
    server.calls[2].stream!.end();
    await settle();
    await advance(2999);
    expect(server.calls).toHaveLength(3);
    await advance(1); // 3 s, not the 12 s a third consecutive failure would wait
    expect(server.calls).toHaveLength(4);
  });

  it.each([429, 503])('waits the Retry-After of a %i, never sooner', async (status) => {
    const { server, rec, client } = setup();
    server.plan = [{ status, headers: { 'Retry-After': '30' } }];
    client.start();
    await settle();
    expect(rec.errors).toEqual([status]);
    await advance(29999);
    expect(server.calls).toHaveLength(1);
    await advance(1);
    expect(server.calls).toHaveLength(2);
  });

  it('keeps its own backoff when that is longer than the Retry-After', async () => {
    const { server, client } = setup();
    server.plan = [{ status: 503, headers: { 'Retry-After': '1' } }];
    client.start();
    await settle();
    await advance(2999);
    expect(server.calls).toHaveLength(1);
    await advance(1);
    expect(server.calls).toHaveLength(2);
  });

  it('reads a Retry-After given as an HTTP date against the injected clock', async () => {
    const now = 1_700_000_000_000;
    expect(retryAfterMs(new Date(now + 45_000).toUTCString(), now)).toBe(45_000);
    expect(retryAfterMs(new Date(now - 45_000).toUTCString(), now)).toBe(0);
    expect(retryAfterMs('soon', now)).toBe(0);
    expect(retryAfterMs(null, now)).toBe(0);

    const { server, client } = setup({ now: () => now });
    server.plan = [{ status: 503, headers: { 'Retry-After': new Date(now + 45_000).toUTCString() } }];
    client.start();
    await settle();
    await advance(44999);
    expect(server.calls).toHaveLength(1);
    await advance(1);
    expect(server.calls).toHaveLength(2);
  });

  it('a 401 is reported with its status and retried', async () => {
    const { server, rec, client } = setup();
    server.plan = [{ status: 401 }];
    client.start();
    await settle();
    expect(rec.errors).toEqual([401]);
    await advance(3000);
    expect(server.calls).toHaveLength(2);
  });

  it('an opaque redirect is an ApiAuthRedirectError, never read from response.redirected', async () => {
    const seen: ApiAuthRedirectError[] = [];
    const { server, rec, client } = setup({ onAuthRedirect: (e) => void seen.push(e) });
    server.plan = ['redirect'];
    client.start();
    await settle();
    expect(seen).toHaveLength(1);
    expect(seen[0]).toBeInstanceOf(ApiAuthRedirectError);
    expect(seen[0].url).toBe(server.calls[0].url);
    expect(rec.errors).toEqual([0]);
    await advance(3000);
    expect(server.calls).toHaveLength(2);
  });
});

describe('closed frames', () => {
  it.each(['credential_revoked', 'permission_revoked'])('a closed frame with %s stops reconnecting', async (reason) => {
    const { server, rec, client } = setup();
    client.start();
    await settle();
    server.calls[0].stream!.push(closedFrame(reason));
    await settle();
    expect(rec.closed).toEqual([reason]);
    expect(server.calls[0].aborted).toBe(true);
    await advance(10 * 60_000);
    expect(server.calls).toHaveLength(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('an unknown close reason is treated as a shutdown: reconnect', async () => {
    const { server, rec, client } = setup();
    client.start();
    await settle();
    server.calls[0].stream!.push(closedFrame('meteor'));
    server.calls[0].stream!.end();
    await advance(3000);
    expect(rec.closed).toEqual(['shutdown']);
    expect(server.calls).toHaveLength(2);
  });
});

describe('stop', () => {
  it('stop() aborts the in-flight fetch and nothing reconnects', async () => {
    const { server, client } = setup();
    client.start();
    await settle();
    expect(server.calls[0].aborted).toBe(false);
    client.stop();
    expect(server.calls[0].aborted).toBe(true);
    await advance(10 * 60_000);
    expect(server.calls).toHaveLength(1);
  });

  it('stop() before the response arrives aborts quietly', async () => {
    const { server, rec, client } = setup();
    client.start();
    client.stop();
    await settle();
    expect(server.calls[0].aborted).toBe(true);
    expect(rec.errors).toEqual([]);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('stop() clears a pending reconnect timer, and start() begins afresh without the old resume point', async () => {
    const { server, client } = setup();
    client.start();
    await settle();
    server.calls[0].stream!.push(frame(GADGETS, 1, { id: 'wayne-1' }));
    server.calls[0].stream!.fail();
    await settle();
    expect(vi.getTimerCount()).toBe(1);
    client.stop();
    expect(vi.getTimerCount()).toBe(0);
    await advance(60_000);
    expect(server.calls).toHaveLength(1);

    client.start();
    await settle();
    expect(server.calls[1].lastEventId).toBeNull();
  });
});

// The hooks ---------------------------------------------------------------

class StubSource implements RealtimeSource {
  readonly listeners = new Set<RealtimeListener>();
  readonly topicCalls: string[][] = [];
  starts = 0;
  stops = 0;
  subscribe(l: RealtimeListener) {
    this.listeners.add(l);
    return () => void this.listeners.delete(l);
  }
  setTopics(topics: readonly string[]) {
    this.topicCalls.push([...topics]);
  }
  start() {
    this.starts++;
  }
  stop() {
    this.stops++;
  }
  emit(e: RealtimeEvent) {
    act(() => this.listeners.forEach((l) => l.onEvent(e)));
  }
  reset(reason: ResetReason) {
    act(() => this.listeners.forEach((l) => l.onReset(reason)));
  }
}

function Probe(props: { topic: string; keyed?: string; into: unknown[]; tag?: string }) {
  useTopic<unknown>(props.topic, (d) => props.into.push(props.tag === undefined ? d : [props.tag, d]), props.keyed === undefined ? undefined : { key: props.keyed });
  return null;
}
function ResyncProbe(props: { into: string[]; tag: string }) {
  useResync(() => props.into.push(props.tag));
  return null;
}
const provider = (source: RealtimeSource, ...children: ReturnType<typeof createElement>[]) =>
  createElement(RealtimeProvider, { source }, ...children);

describe('hooks', () => {
  it('useTopic delivers only its topic, and only its key when it has one', () => {
    const source = new StubSource();
    const all: unknown[] = [];
    const robin: unknown[] = [];
    render(
      provider(
        source,
        createElement(Probe, { topic: GADGETS, into: all, key: 'all' }),
        createElement(Probe, { topic: GADGETS, keyed: 'robin-1', into: robin, key: 'robin' }),
      ),
    );
    source.emit({ topic: COWL, data: 'cowl' });
    source.emit({ topic: GADGETS, key: 'robin-1', data: 'a' });
    source.emit({ topic: GADGETS, key: 'robin-2', data: 'b' });
    source.emit({ topic: GADGETS, data: 'c' });
    expect(all).toEqual(['a', 'b', 'c']);
    expect(robin).toEqual(['a']);
  });

  it('tells the source the union of the topics in use, and starts and stops it with the provider', () => {
    const source = new StubSource();
    const sink: unknown[] = [];
    const view = render(provider(source, createElement(Probe, { topic: GADGETS, into: sink, key: 'a' })));
    expect(source.starts).toBe(1);
    expect(source.topicCalls.at(-1)).toEqual([GADGETS]);

    view.rerender(
      provider(
        source,
        createElement(Probe, { topic: GADGETS, into: sink, key: 'a' }),
        createElement(Probe, { topic: GADGETS, into: sink, key: 'b' }),
        createElement(Probe, { topic: COWL, into: sink, key: 'c' }),
      ),
    );
    expect(source.topicCalls.at(-1)).toEqual([COWL, GADGETS]); // one entry per distinct topic

    view.rerender(provider(source, createElement(Probe, { topic: COWL, into: sink, key: 'c' })));
    expect(source.topicCalls.at(-1)).toEqual([COWL]);

    view.unmount();
    expect(source.stops).toBe(1);
    expect(source.listeners.size).toBe(0);
  });

  it('keeps the latest callback without resubscribing on a re-render', () => {
    const source = new StubSource();
    const seen: string[] = [];
    function Counter() {
      const [n, setN] = useState(0);
      useTopic<string>(GADGETS, (d) => seen.push(`${d}:${n}`));
      return createElement('button', { onClick: () => setN(n + 1) }, 'more');
    }
    const view = render(provider(source, createElement(Counter)));
    const calls = source.topicCalls.length;
    source.emit({ topic: GADGETS, data: 'a' });
    act(() => view.getByRole('button').click());
    source.emit({ topic: GADGETS, data: 'b' });
    expect(seen).toEqual(['a:0', 'b:1']);
    expect(source.topicCalls).toHaveLength(calls);
    expect(source.listeners.size).toBe(1);
    expect(source.starts).toBe(1);
  });

  it('useResync runs every mounted handler on a reset, latest callback, and not after unmount', () => {
    const source = new StubSource();
    const ran: string[] = [];
    const view = render(
      provider(source, createElement(ResyncProbe, { into: ran, tag: 'a', key: 'a' }), createElement(ResyncProbe, { into: ran, tag: 'b', key: 'b' })),
    );
    source.reset('overflow');
    expect(ran).toEqual(['a', 'b']);

    view.rerender(provider(source, createElement(ResyncProbe, { into: ran, tag: 'a2', key: 'a' })));
    source.reset('gap');
    expect(ran).toEqual(['a', 'b', 'a2']);
  });

  it('runs useResync handlers when the real client could not resume', async () => {
    const { server, client } = setup();
    const ran: string[] = [];
    render(createElement(RealtimeProvider, { source: client }, createElement(ResyncProbe, { into: ran, tag: 'a' })));
    await settle();
    server.calls[0].stream!.push(frame(GADGETS, 1, { id: 'wayne-1' }));
    server.calls[0].stream!.end();
    await advance(3000);
    server.calls[1].stream!.push(resetFrame('restart'));
    await settle();
    expect(ran).toEqual(['a']);
  });

  it('hands closed frames and transport errors to the provider props', () => {
    const source = new StubSource();
    const closed: string[] = [];
    const errors: (number | undefined)[] = [];
    render(createElement(RealtimeProvider, { source, onClosed: (r) => void closed.push(r), onError: (s) => void errors.push(s) }));
    act(() => source.listeners.forEach((l) => (l.onClosed('credential_revoked'), l.onError?.(503))));
    expect(closed).toEqual(['credential_revoked']);
    expect(errors).toEqual([503]);
  });

  it('useTopic outside a provider fails loudly', () => {
    const quiet = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    expect(() => render(createElement(Probe, { topic: GADGETS, into: [] }))).toThrow(/RealtimeProvider/);
    quiet.mockRestore();
  });
});
