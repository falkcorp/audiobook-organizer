// file: web/src/test/mockCoreEvents.test.ts
// version: 0.1.0
// guid: e1987d47-6ee4-4ac0-a3da-f6738607ceba
// last-edited: 2026-11-20

// Unit test for the e2e event-stream mock (web/tests/e2e/utils/mockCoreEvents.ts). It
// lives under src/ because vitest excludes tests/e2e and tsc only includes src. The shim
// is installed the way Playwright installs an init script (from its source text), and
// driven with the real coreEvents client, so the mock speaks the protocol the app parses.

import type { Page, Route } from '@playwright/test';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  CORE_EVENTS_PATH,
  installCoreEventsShim,
  mockCoreEvents,
  type CoreEventsShimConfig,
  type CoreEventsShimController,
} from '../../tests/e2e/utils/mockCoreEvents';
import { createEventStream, type EventStream } from '../services/coreEvents/manager';
import { StreamRefusedError, TOPIC_RESET } from '../services/coreEvents/types';

const RUN = 'core.task.run-changed.v1';
const BOOK = 'books.book.changed.v1';
const LOG = 'core.task.log-appended.v1';

type MockWindow = Window & { __coreEventsMock?: CoreEventsShimController };
const w = window as unknown as MockWindow;
const tick = (ms = 0) => vi.advanceTimersByTimeAsync(ms);

/** As Playwright does: the function's source text, evaluated with no closure. */
function installFromSource(config: CoreEventsShimConfig) {
  const fn = new Function(`return (${installCoreEventsShim.toString()})`)() as typeof installCoreEventsShim;
  fn(config);
  return w.__coreEventsMock!;
}

let originalFetch: typeof fetch;
let passthrough: ReturnType<typeof vi.fn>;
let es: EventStream | undefined;

beforeEach(() => {
  vi.useFakeTimers();
  originalFetch = w.fetch;
  passthrough = vi.fn(() => Promise.resolve(new Response('{}', { status: 200 })));
  w.fetch = passthrough as unknown as typeof fetch;
});
afterEach(() => {
  es?.dispose();
  es = undefined;
  w.fetch = originalFetch;
  delete w.__coreEventsMock;
  vi.useRealTimers();
});

function client() {
  es = createEventStream({
    visibility: { visible: true, onChange: () => () => {} },
    deps: { fetch: (...a) => w.fetch(...a), random: () => 0 },
  });
  return es;
}

describe('installCoreEventsShim', () => {
  it('is self-contained: it installs from its source text alone', () => {
    const ctl = installFromSource({ autoOpened: true, refusals: [] });
    expect(typeof ctl.event).toBe('function');
  });

  it('serves the shared stream to the real client: opened first, then filtered event frames', async () => {
    const ctl = installFromSource({ autoOpened: true, refusals: [] });
    const stream = client();
    const books: unknown[] = [];
    const resyncs: string[] = [];
    stream.subscribe(BOOK, (e) => books.push({ id: e.id, key: e.key, data: e.data }));
    stream.onResync((i) => resyncs.push(i.reason), { coalesceMs: 0, minIntervalMs: 0 });
    await tick();
    expect(ctl.requests).toEqual([
      {
        url: `${CORE_EVENTS_PATH}?topic=*&exclude=${encodeURIComponent(LOG)}`,
        topics: ['*'],
        exclude: [LOG],
        key: '',
        lastEventId: '',
        answer: 'stream',
        open: true,
      },
    ]);
    expect(stream.getStatus().state).toBe('open');
    expect(resyncs).toEqual(['reconnect']);
    expect(ctl.event(BOOK, { kind: 'updated', id: 'bk-hush' }, { key: 'bk-hush' })).toBe(1);
    expect(ctl.event(LOG, { line: 'excluded' }, { key: 'run-1' })).toBe(0);
    await tick();
    expect(books).toEqual([{ id: 'mock-1', key: 'bk-hush', data: { kind: 'updated', id: 'bk-hush' } }]);
    expect(passthrough).not.toHaveBeenCalled();
  });

  it('narrows a keyed stream to its topics and key, as the server does', async () => {
    const ctl = installFromSource({ autoOpened: true, refusals: [] });
    const stream = client();
    const lines: string[] = [];
    stream.subscribe(LOG, (e) => lines.push(e.key), { stream: 'keyed', key: 'run-joker-1' });
    await tick();
    expect(ctl.requests[0]).toMatchObject({ topics: [LOG], exclude: [], key: 'run-joker-1' });
    expect(ctl.event(LOG, {}, { key: 'run-joker-1' })).toBe(1);
    expect(ctl.event(LOG, {}, { key: 'run-joker-2' })).toBe(0);
    expect(ctl.event(RUN, {}, { key: 'run-joker-1' })).toBe(0);
    await tick();
    expect(lines).toEqual(['run-joker-1']);
  });

  it('heartbeats, so the real client keeps a quiet stream open past its idle deadline', async () => {
    const ctl = installFromSource({ autoOpened: true, refusals: [] });
    const stream = client();
    stream.subscribe(RUN, () => {});
    await tick();
    await tick(120000);
    expect(ctl.requests).toHaveLength(1);
    expect(stream.getStatus().state).toBe('open');
  });

  it('with the heartbeat off a quiet stream is dropped by the client, and an ended stream leaves no timer', async () => {
    const ctl = installFromSource({ autoOpened: true, refusals: [], heartbeatMs: 0 });
    const stream = client();
    stream.subscribe(RUN, () => {});
    await tick();
    await tick(50000);
    expect(ctl.requests.length).toBeGreaterThan(1);
    stream.dispose();
    await tick();
    expect(vi.getTimerCount()).toBe(0);
  });

  it('an ended stream stops its heartbeat', async () => {
    const ctl = installFromSource({ autoOpened: true, refusals: [] });
    await w.fetch(CORE_EVENTS_PATH);
    expect(vi.getTimerCount()).toBe(1);
    ctl.endAll();
    expect(vi.getTimerCount()).toBe(0);
  });

  it('autoOpened false leaves the open to the test', async () => {
    const ctl = installFromSource({ autoOpened: false, refusals: [] });
    const stream = client();
    stream.subscribe(RUN, () => {});
    await tick();
    expect(stream.getStatus().state).toBe('connecting');
    ctl.control('core.stream.opened.v1', { topics: ['*'] });
    await tick();
    expect(stream.getStatus().state).toBe('open');
  });

  it('a module wildcard topic matches and an exact exclude wins', async () => {
    const ctl = installFromSource({ autoOpened: false, refusals: [] });
    await w.fetch(`${CORE_EVENTS_PATH}?topic=core.*&exclude=${RUN}`);
    expect(ctl.event('core.log.recorded.v1', {})).toBe(1);
    expect(ctl.event(RUN, {})).toBe(0);
    expect(ctl.event(BOOK, {})).toBe(0);
  });

  it('closed{shutdown} reaches the client and ends the streams; the reconnect resumes from the last id', async () => {
    const ctl = installFromSource({ autoOpened: true, refusals: [] });
    const stream = client();
    const closed: string[] = [];
    stream.subscribe(RUN, () => {});
    stream.onClosed((i) => closed.push(i.reason));
    await tick();
    ctl.event(RUN, { id: 'run-1' }, { key: 'run-1', id: 'n-7' });
    await tick();
    expect(ctl.shutdown()).toBe(1);
    expect(ctl.openCount()).toBe(0);
    await tick();
    expect(closed).toEqual(['shutdown']);
    await tick(3000);
    expect(ctl.requests).toHaveLength(2);
    expect(ctl.requests[1].lastEventId).toBe('n-7');
  });

  it('a reset control frame reaches the resync handlers, the first one right after the open included', async () => {
    const ctl = installFromSource({ autoOpened: true, refusals: [] });
    const stream = client();
    const reasons: string[] = [];
    stream.subscribe(RUN, () => {});
    stream.onResync((i) => reasons.push(i.reason), { coalesceMs: 0, minIntervalMs: 0 });
    await tick();
    expect(ctl.reset('gap')).toBe(1);
    expect(ctl.control(TOPIC_RESET, { reason: 'overflow' })).toBe(1);
    await tick();
    expect(reasons).toEqual(['reconnect', 'gap', 'overflow']);
  });

  it('a 429 with Retry-After is a refusal the client honours', async () => {
    const ctl = installFromSource({ autoOpened: true, refusals: [] });
    ctl.refuseNext({ status: 429, code: 'rate_limited', retryAfterSeconds: 30 });
    const stream = client();
    stream.subscribe(RUN, () => {});
    await tick();
    const err = stream.getStatus().error as StreamRefusedError;
    expect(err).toBeInstanceOf(StreamRefusedError);
    expect(err).toMatchObject({ status: 429, code: 'rate_limited', retryAfterMs: 30000 });
    expect(ctl.requests[0]).toMatchObject({ answer: 429, open: false });
    await tick(29999);
    expect(ctl.requests).toHaveLength(1);
    await tick(1);
    expect(ctl.requests).toHaveLength(2);
    expect(stream.getStatus().state).toBe('open');
  });

  it('configured refusals answer the first requests in order', async () => {
    const ctl = installFromSource({ autoOpened: true, refusals: [{ status: 503, retryAfterSeconds: 5 }, { status: 403 }] });
    const a = await w.fetch(CORE_EVENTS_PATH);
    const b = await w.fetch(CORE_EVENTS_PATH);
    const c = await w.fetch(CORE_EVENTS_PATH);
    expect([a.status, a.headers.get('Retry-After'), b.status, b.headers.get('Retry-After'), c.status]).toEqual([
      503,
      '5',
      403,
      null,
      200,
    ]);
    expect(ctl.requests.map((r) => r.answer)).toEqual([503, 403, 'stream']);
  });

  it('an abort closes the stream and marks the request closed', async () => {
    const ctl = installFromSource({ autoOpened: true, refusals: [] });
    const stream = client();
    stream.subscribe(RUN, () => {});
    await tick();
    expect(ctl.openCount()).toBe(1);
    stream.dispose();
    await tick();
    expect(ctl.openCount()).toBe(0);
    expect(ctl.requests[0].open).toBe(false);
    expect(ctl.event(RUN, {})).toBe(0);
  });

  it('a request whose signal is already aborted is rejected and not recorded', async () => {
    const ctl = installFromSource({ autoOpened: true, refusals: [] });
    const ac = new AbortController();
    ac.abort();
    await expect(w.fetch(CORE_EVENTS_PATH, { signal: ac.signal })).rejects.toMatchObject({ name: 'AbortError' });
    expect(ctl.requests).toEqual([]);
  });

  it('passes every other request to the original fetch, Request objects included', async () => {
    installFromSource({ autoOpened: true, refusals: [] });
    await w.fetch('/api/v1/health');
    await w.fetch(new Request('http://localhost/api/core/v1/eventsx'));
    expect(passthrough).toHaveBeenCalledTimes(2);
  });

  it('installs once per page', () => {
    const first = installFromSource({ autoOpened: true, refusals: [] });
    const second = installFromSource({ autoOpened: false, refusals: [] });
    expect(second).toBe(first);
  });
});

describe('mockCoreEvents(page)', () => {
  function fakePage() {
    const routes: Array<{ match: (url: URL) => boolean; handler: (route: Route) => Promise<void> }> = [];
    const initScripts: Array<{ fn: unknown; arg: unknown }> = [];
    const page = {
      addInitScript: async (fn: unknown, arg: unknown) => {
        initScripts.push({ fn, arg });
        installFromSource(arg as CoreEventsShimConfig);
      },
      route: async (match: (url: URL) => boolean, handler: (route: Route) => Promise<void>) => {
        routes.push({ match, handler });
      },
      evaluate: async (fn: (arg: unknown) => unknown, arg: unknown) => fn(arg),
    };
    return { page: page as unknown as Page, routes, initScripts };
  }

  it('installs the shim as an init script with the options, and drives it through page.evaluate', async () => {
    const { page, initScripts } = fakePage();
    const mock = await mockCoreEvents(page, { autoOpened: false, refusals: [{ status: 429, retryAfterSeconds: 9 }] });
    expect(initScripts).toEqual([
      {
        fn: installCoreEventsShim,
        arg: { autoOpened: false, refusals: [{ status: 429, retryAfterSeconds: 9 }], heartbeatMs: 15000 },
      },
    ]);
    const stream = client();
    stream.subscribe(BOOK, () => {});
    await tick();
    expect(await mock.requests()).toMatchObject([{ answer: 429 }]);
    await mock.refuseNext({ status: 503 });
    await tick(9000);
    expect((await mock.requests()).map((r) => r.answer)).toEqual([429, 503]);
    await tick(6000);
    expect(await mock.openCount()).toBe(1);
    expect(await mock.event(BOOK, { kind: 'created', id: 'bk-1' }, { key: 'bk-1' })).toBe(1);
    expect(await mock.control('core.stream.opened.v1', {})).toBe(1);
    expect(await mock.reset('gap')).toBe(1);
    expect(await mock.endAll()).toBe(1);
    await tick(3000);
    expect(await mock.shutdown()).toBe(1);
    expect(await mock.openCount()).toBe(0);
  });

  it('guards the events path at the network: records an escaped request, refuses it with a 500 and no Retry-After, and fails every later call loudly', async () => {
    const { page, routes } = fakePage();
    const mock = await mockCoreEvents(page);
    expect(routes).toHaveLength(1);
    const { match, handler } = routes[0];
    expect(match(new URL('http://127.0.0.1:5173/api/core/v1/events?topic=*&exclude=x'))).toBe(true);
    expect(match(new URL('http://127.0.0.1:5173/api/core/v1/events'))).toBe(true);
    expect(match(new URL('http://127.0.0.1:5173/api/core/v1/eventsx'))).toBe(false);
    expect(match(new URL('http://127.0.0.1:5173/api/events'))).toBe(false);
    expect(mock.unmockedRequests()).toEqual([]);
    expect(() => mock.assertNoUnmockedRequests()).not.toThrow();
    const fulfilled: unknown[] = [];
    const route = {
      request: () => ({ url: () => 'http://127.0.0.1:5173/api/core/v1/events?topic=*' }),
      fulfill: async (o: unknown) => void fulfilled.push(o),
    } as unknown as Route;
    await handler(route);
    expect(mock.unmockedRequests()).toEqual(['http://127.0.0.1:5173/api/core/v1/events?topic=*']);
    expect(fulfilled).toHaveLength(1);
    const f = fulfilled[0] as { status: number; headers?: Record<string, string>; body: string };
    expect(f.status).toBe(500); // not 429/503: the client's tab-wide hold stays untouched
    expect(f.headers?.['Retry-After']).toBeUndefined();
    expect(JSON.parse(f.body)).toMatchObject({ status: 500, code: 'unmocked' });
    expect(() => mock.assertNoUnmockedRequests()).toThrow(/1 request\(s\) to \/api\/core\/v1\/events got past the shim.*topic=\*/);
    await expect(mock.openCount()).rejects.toThrow(/got past the shim/);
    await expect(mock.event(RUN, {})).rejects.toThrow(/got past the shim/);
  });

  // #4813 r2 m-c: scan-import-organize.spec.ts and setup-modes.spec.ts call mockEventSource twice
  // per page, so the helper that installs this mock runs twice on one Page.
  it('is safe to call twice per page: the second call returns the first handle and installs nothing', async () => {
    const { page, routes, initScripts } = fakePage();
    const first = await mockCoreEvents(page, { refusals: [{ status: 429, retryAfterSeconds: 9 }] });
    const second = await mockCoreEvents(page);
    const third = await mockCoreEvents(page, { refusals: [{ status: 429, retryAfterSeconds: 9 }] });
    expect(second).toBe(first);
    expect(third).toBe(first);
    expect(routes).toHaveLength(1);
    expect(initScripts).toHaveLength(1);
    const stream = client();
    stream.subscribe(BOOK, () => {});
    await tick();
    expect(await second.requests()).toMatchObject([{ answer: 429 }]); // the first call's refusals
  });

  it('two calls that race share one setup', async () => {
    const { page, routes, initScripts } = fakePage();
    const [a, b] = await Promise.all([mockCoreEvents(page), mockCoreEvents(page)]);
    expect(a).toBe(b);
    expect(routes).toHaveLength(1);
    expect(initScripts).toHaveLength(1);
  });

  it('a second call that names different options throws, and the first setup stands', async () => {
    const { page, routes, initScripts } = fakePage();
    const first = await mockCoreEvents(page, { heartbeatMs: 0 });
    await expect(mockCoreEvents(page, { heartbeatMs: 5000 })).rejects.toThrow(/already mocked with different options/);
    await expect(mockCoreEvents(page, { autoOpened: false })).rejects.toThrow(/different options/);
    await expect(mockCoreEvents(page)).resolves.toBe(first);
    expect(routes).toHaveLength(1);
    expect(initScripts).toHaveLength(1);
    // Another Page is neither shimmed nor guarded: it gets its own handle.
    const other = fakePage();
    delete w.__coreEventsMock;
    const otherMock = await mockCoreEvents(other.page);
    expect(otherMock).not.toBe(first);
  });

  it('a failed setup stays failed for every later call instead of adding a second init script', async () => {
    const calls: string[] = [];
    const page = {
      addInitScript: async () => {
        calls.push('init');
      },
      route: async () => {
        calls.push('route');
        throw new Error('route refused');
      },
      evaluate: async () => undefined,
    } as unknown as Page;
    await expect(mockCoreEvents(page)).rejects.toThrow(/route refused/);
    await expect(mockCoreEvents(page)).rejects.toThrow(/route refused/);
    expect(calls).toEqual(['init', 'route']);
  });

  it('a call before the shim exists in the page says so', async () => {
    const page = {
      addInitScript: async () => {},
      route: async () => {},
      evaluate: async (fn: (arg: unknown) => unknown, arg: unknown) => fn(arg),
    } as unknown as Page;
    const mock = await mockCoreEvents(page);
    await expect(mock.openCount()).rejects.toThrow(/not installed/);
  });
});
