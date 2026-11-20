// file: web/src/services/coreEvents/connection.test.ts
// version: 0.1.0
// guid: abfaab0a-1d57-4d7c-a9d7-87bb7322b276
// last-edited: 2026-11-19

import { ApiAuthRedirectError, isAuthRedirectError } from '@mediacore/api-client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import { createFakeServer, frameText, problemResponse } from '../../test/eventStreamFake';
import { buildStreamUrl, createStreamConnection, type ConnectionOptions } from './connection';
import {
  StreamProtocolError,
  StreamRefusedError,
  TOPIC_CLOSED,
  TOPIC_OPENED,
  TOPIC_RESET,
  type StreamStatus,
} from './types';

const RUN = 'core.task.run-changed.v1';

function setup(over: Partial<ConnectionOptions> = {}) {
  const server = createFakeServer();
  const events: Array<{ topic: string; id: string }> = [];
  const resets: string[] = [];
  const closed: string[] = [];
  const statuses: StreamStatus[] = [];
  const conn = createStreamConnection({
    key: '',
    topics: () => [RUN, 'books.book.changed.v1'],
    onEvent: (env, id) => events.push({ topic: env.topic, id }),
    onReset: (r) => resets.push(r),
    onClosed: (r) => closed.push(r),
    onStatus: (s) => statuses.push(s),
    deps: { fetch: server.fetch, random: () => 0 },
    ...over,
  });
  return { server, conn, events, resets, closed, statuses };
}

const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms);

beforeEach(() => vi.useFakeTimers());
afterEach(() => vi.useRealTimers());

describe('buildStreamUrl', () => {
  it('repeats topic and adds key only for a keyed stream', () => {
    expect(buildStreamUrl('', ['a.b.v1', 'books.*'], '')).toBe('/api/core/v1/events?topic=a.b.v1&topic=books.*');
    expect(buildStreamUrl('', ['core.task.log-appended.v1'], 'run-batman-1')).toBe(
      '/api/core/v1/events?topic=core.task.log-appended.v1&key=run-batman-1',
    );
  });
});

describe('request shape', () => {
  it('GETs the events route with manual redirects, no cache and no Last-Event-ID the first time', async () => {
    const { server, conn } = setup();
    server.openStream();
    conn.open();
    await tick();
    expect(server.calls).toHaveLength(1);
    const c = server.calls[0];
    expect(c.url).toBe('/api/core/v1/events?topic=core.task.run-changed.v1&topic=books.book.changed.v1');
    expect(c.init.redirect).toBe('manual');
    expect(c.init.cache).toBe('no-store');
    expect(c.init.credentials).toBe('same-origin');
    expect(c.headers.accept).toBe('text/event-stream');
    expect(c.headers['last-event-id']).toBeUndefined();
    conn.close('idle');
  });
});

describe('resume and control frames', () => {
  it('sends the last event id on every reconnect, and a control frame never moves it', async () => {
    const { server, conn, events, statuses } = setup();
    const s1 = server.openStream();
    const s2 = server.openStream();
    server.openStream();
    conn.open();
    await tick();
    s1.control(TOPIC_OPENED, { topics: [RUN] });
    s1.event('n-1', RUN, { id: 'run-1' }, 'run-1');
    s1.event('n-2', 'books.book.changed.v1', { kind: 'updated', id: 'b1' }, 'b1');
    s1.control(TOPIC_RESET, { reason: 'gap' });
    await tick();
    expect(events.map((e) => e.id)).toEqual(['n-1', 'n-2']);
    expect(statuses.at(-1)?.state).toBe('open');

    s1.end();
    await tick(3000);
    expect(server.calls[1].headers['last-event-id']).toBe('n-2');

    s2.event('n-3', RUN, { id: 'run-1' }, 'run-1');
    await tick();
    s2.end();
    await tick(6000);
    expect(server.calls[2].headers['last-event-id']).toBe('n-3');
    conn.close('idle');
  });

  it('reset reaches onReset with the server reason', async () => {
    const { server, conn, resets } = setup();
    const s = server.openStream();
    conn.open();
    await tick();
    s.control(TOPIC_RESET, { reason: 'overflow' });
    await tick();
    expect(resets).toEqual(['overflow']);
    conn.close('idle');
  });

  it('closed reconnects with backoff and records the reason, without calling the end a fault', async () => {
    const { server, conn, closed, statuses } = setup();
    const s = server.openStream();
    server.openStream();
    conn.open();
    await tick();
    s.control(TOPIC_OPENED, {});
    s.control(TOPIC_CLOSED, { reason: 'shutdown' });
    s.end();
    await tick();
    expect(closed).toEqual(['shutdown']);
    const waiting = statuses.at(-1)!;
    expect(waiting.state).toBe('reconnecting');
    expect(waiting.closedReason).toBe('shutdown');
    expect(waiting.error).toBeUndefined();
    expect(waiting.delayMs).toBe(3000);
    await tick(2999);
    expect(server.calls).toHaveLength(1);
    await tick(1);
    expect(server.calls).toHaveLength(2);
    conn.close('idle');
  });

  it('skips a frame that is not JSON or not an envelope without ending the stream', async () => {
    const { server, conn, events } = setup();
    const s = server.openStream();
    conn.open();
    await tick();
    s.write('id: n-1\ndata: not json\n\n');
    s.write('id: n-2\ndata: {"x":1}\n\n');
    s.event('n-3', RUN, {});
    await tick();
    expect(events.map((e) => e.id)).toEqual(['n-3']);
    conn.close('idle');
  });
});

describe('refusals', () => {
  it('surface status, code and Retry-After and wait at least that long', async () => {
    const { server, conn, statuses } = setup();
    server.answer(() => problemResponse(429, 'rate_limited', 'too many streams', { 'Retry-After': '30' }));
    server.openStream();
    conn.open();
    await tick();
    const st = statuses.at(-1)!;
    expect(st.state).toBe('reconnecting');
    expect(st.error).toBeInstanceOf(StreamRefusedError);
    const err = st.error as StreamRefusedError;
    expect(err.status).toBe(429);
    expect(err.code).toBe('rate_limited');
    expect(err.retryAfterMs).toBe(30000);
    expect(st.delayMs).toBe(30000);
    await tick(29999);
    expect(server.calls).toHaveLength(1);
    await tick(1);
    expect(server.calls).toHaveLength(2);
    conn.close('idle');
  });

  it('a 403 and a 400 are refusals too, and are retried', async () => {
    const { server, conn, statuses } = setup();
    server.answer(() => problemResponse(403, 'topic_not_permitted', 'no'));
    server.answer(() => problemResponse(400, 'unknown_topic', 'no such topic'));
    server.openStream();
    conn.open();
    await tick();
    expect((statuses.at(-1)!.error as StreamRefusedError).code).toBe('topic_not_permitted');
    await tick(3000);
    expect((statuses.at(-1)!.error as StreamRefusedError).code).toBe('unknown_topic');
    await tick(6000);
    expect(server.calls).toHaveLength(3);
    conn.close('idle');
  });
});

describe('backoff', () => {
  it('doubles 3 s to 60 s and never gives up, far past the old five attempts', async () => {
    const { server, conn, statuses } = setup();
    conn.open();
    await tick();
    const delays: number[] = [];
    for (let i = 0; i < 10; i++) {
      const d = statuses.at(-1)!.delayMs!;
      delays.push(d);
      await tick(d);
    }
    expect(delays).toEqual([3000, 6000, 12000, 24000, 48000, 60000, 60000, 60000, 60000, 60000]);
    expect(server.calls).toHaveLength(11);
    expect(statuses.at(-1)!.state).toBe('reconnecting');
    conn.close('idle');
  });

  it('a good open (core.stream.opened.v1) resets the attempt counter', async () => {
    const { server, conn, statuses } = setup();
    server.answer(() => problemResponse(503, 'unavailable', 'x'));
    server.answer(() => problemResponse(503, 'unavailable', 'x'));
    const s = server.openStream();
    conn.open();
    await tick();
    await tick(3000);
    await tick(6000);
    s.control(TOPIC_OPENED, {});
    await tick();
    expect(statuses.at(-1)).toMatchObject({ state: 'open', attempt: 0 });
    s.end();
    await tick();
    expect(statuses.at(-1)!.delayMs).toBe(3000);
    conn.close('idle');
  });
});

describe('SSO bounce', () => {
  it('raises ApiAuthRedirectError for an opaqueredirect, never reads response.redirected, and keeps retrying', async () => {
    const { server, conn, statuses } = setup();
    const opaque = new Proxy(
      { type: 'opaqueredirect', status: 0, ok: false },
      {
        get(t, p) {
          if (p === 'redirected') throw new Error('response.redirected must not be read');
          return (t as Record<string | symbol, unknown>)[p as string];
        },
      },
    ) as unknown as Response;
    server.answer(() => opaque);
    server.openStream();
    conn.open();
    await tick();
    const err = statuses.at(-1)!.error;
    expect(err).toBeInstanceOf(ApiAuthRedirectError);
    expect(isAuthRedirectError(err)).toBe(true);
    await tick(3000);
    expect(server.calls).toHaveLength(2);
    conn.close('idle');
  });

  it('a same-origin 200 text/html login page is the same bounce, not a stream', async () => {
    const { server, conn, statuses } = setup();
    server.answer(() => new Response('<html>login</html>', { status: 200, headers: { 'Content-Type': 'text/html' } }));
    conn.open();
    await tick();
    expect(statuses.at(-1)!.error).toBeInstanceOf(ApiAuthRedirectError);
    conn.close('idle');
  });

  it('a 200 with another content type is a protocol error', async () => {
    const { server, conn, statuses } = setup();
    server.answer(() => new Response('{}', { status: 200, headers: { 'Content-Type': 'application/json' } }));
    conn.open();
    await tick();
    expect(statuses.at(-1)!.error).toBeInstanceOf(StreamProtocolError);
    conn.close('idle');
  });
});

describe('deadlines', () => {
  it('abandons a connect that gets no headers and retries', async () => {
    const { server, conn, statuses } = setup({ connectTimeoutMs: 1000 });
    server.answer((call) => new Promise<Response>((_res, rej) => call.init.signal!.addEventListener('abort', () => rej(new DOMException('aborted', 'AbortError')))));
    server.openStream();
    conn.open();
    await tick(1000);
    expect(statuses.at(-1)!.state).toBe('reconnecting');
    expect(statuses.at(-1)!.error).toBeInstanceOf(StreamProtocolError);
    await tick(3000);
    expect(server.calls).toHaveLength(2);
    conn.close('idle');
  });

  it('drops a stream that goes silent past the idle deadline, and a heartbeat keeps it alive', async () => {
    const { server, conn, statuses } = setup({ idleTimeoutMs: 1000 });
    const s = server.openStream();
    conn.open();
    await tick();
    s.write(': hb\n\n');
    await tick(900);
    s.write(': hb\n\n');
    await tick(900);
    expect(statuses.at(-1)!.state).toBe('connecting');
    await tick(200);
    expect(statuses.at(-1)!.state).toBe('reconnecting');
    expect(statuses.at(-1)!.error).toBeInstanceOf(StreamProtocolError);
    conn.close('idle');
  });
});

describe('open, close, restart', () => {
  it('close aborts the request and schedules nothing; open again connects at once', async () => {
    const { server, conn, statuses } = setup();
    server.openStream();
    conn.open();
    await tick();
    conn.close('hidden');
    expect(server.calls[0].init.signal!.aborted).toBe(true);
    expect(statuses.at(-1)!.state).toBe('hidden');
    await tick(120000);
    expect(server.calls).toHaveLength(1);
    conn.open();
    await tick();
    expect(server.calls).toHaveLength(2);
    conn.close('idle');
  });

  it('restart reconnects at once with no backoff and resumes from the last id', async () => {
    const { server, conn } = setup();
    const s = server.openStream();
    server.openStream();
    conn.open();
    await tick();
    s.write(frameText(RUN, {}, '', 'n-9'));
    await tick();
    conn.restart();
    await tick();
    expect(server.calls).toHaveLength(2);
    expect(server.calls[1].headers['last-event-id']).toBe('n-9');
    conn.close('idle');
  });

  it('open is idempotent: two opens make one request', async () => {
    const { server, conn } = setup();
    server.openStream();
    conn.open();
    conn.open();
    await tick();
    expect(server.calls).toHaveLength(1);
    conn.close('idle');
  });
});
