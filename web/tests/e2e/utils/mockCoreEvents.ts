// file: web/tests/e2e/utils/mockCoreEvents.ts
// version: 0.1.0
// guid: 7ef3da72-c79c-447f-b470-81f6be13eb98
// last-edited: 2026-11-20

// e2e mock for the core event stream, GET /api/core/v1/events (F6-03 E3).
//
// The SPA's new client (web/src/services/coreEvents) reads the stream with fetch and a
// ReadableStream, so the window.EventSource mock in test-helpers.ts does not catch it.
// Playwright's route.fulfill only takes a complete string or Buffer body, so it cannot
// hold a stream open. This mock therefore has two parts:
//  - an init script (installCoreEventsShim) that wraps window.fetch for the events path
//    only and answers with a ReadableStream the test drives through page.evaluate: event
//    frames, control frames (opened, reset, closed{shutdown}) and refusals (429 with
//    Retry-After). Like the server it writes core.stream.opened.v1 first, heartbeats
//    every 15 s, and filters each frame by the stream's topic, exclude and key parameters;
//  - a page.route guard on the same path that records any request that got past the shim
//    and refuses it with a 500 (no Retry-After, so the client's tab-wide 429/503 hold is
//    not tripped and other streams keep running). Once one has escaped, every call on the
//    handle throws naming it, so the test fails on that message rather than a timeout.
//
// Call mockCoreEvents(page) before the first page.goto. It is safe to call more than once
// per Page, which the flip needs: the helpers (mockEventSource and friends) install it from
// several places and several specs call them twice per page. The first call installs; every
// later call on the same Page, even one that races the first, returns the SAME handle. A
// later call that names different options throws, because the shim is already configured;
// one with no options, or the same ones, reuses it. If the first call's setup fails the page
// is half mocked, so every later call rejects with that same error rather than adding a
// second init script whose config the shim would ignore. The shim's state is per DOCUMENT, not per Page: the init script
// runs again in every document the page loads, so after a navigation or reload the
// configured refusals answer that document's first requests again, requests() lists only
// the current document's requests, and any frame sent before is gone. A popup or another
// Page is neither shimmed nor guarded: call mockCoreEvents on it too.
//
// No spec imports this until the F6 flip switches e2e off mockEventSource.

import type { Page } from '@playwright/test';

export const CORE_EVENTS_PATH = '/api/core/v1/events';

export interface CoreEventsRefusal {
  status: number;
  code?: string;
  detail?: string;
  retryAfterSeconds?: number;
}

export interface CoreEventsShimConfig {
  /** Write core.stream.opened.v1 as each stream opens, as the server does. */
  autoOpened: boolean;
  /** Answers for the first requests, in order; later requests get a stream. */
  refusals: CoreEventsRefusal[];
  /**
   * Write a `: hb` comment to each open stream this often (ms), as the server does every
   * 15 s; the client drops a stream silent for 45 s. Default 15000; 0 turns it off.
   */
  heartbeatMs?: number;
}

export interface CoreEventsRequestRecord {
  url: string;
  topics: string[];
  exclude: string[];
  key: string;
  /** The Last-Event-ID header ('' when absent). */
  lastEventId: string;
  /** 'stream' or the refusal status. */
  answer: 'stream' | number;
  /** True while the stream is open (false once aborted, cancelled or ended). */
  open: boolean;
}

export interface CoreEventSendOptions {
  key?: string;
  /** The frame id; defaults to mock-<n>. */
  id?: string;
}

/** The in-page controller at window.__coreEventsMock. Each send returns how many streams got the frame. */
export interface CoreEventsShimController {
  requests: CoreEventsRequestRecord[];
  refuseNext(refusal: CoreEventsRefusal): void;
  event(topic: string, data: unknown, opts?: CoreEventSendOptions): number;
  control(topic: string, data: unknown): number;
  reset(reason: string): number;
  /** core.stream.closed.v1 {reason: 'shutdown'} to every stream, then ends them. */
  shutdown(): number;
  /** Ends every open stream (a dropped connection). */
  endAll(): number;
  openCount(): number;
}

/**
 * Installs the fetch shim in the page. Runs as a Playwright init script, so it must be
 * self-contained: no import or module-level name is reachable from inside it.
 */
export function installCoreEventsShim(config: CoreEventsShimConfig): void {
  const PATH = '/api/core/v1/events';
  const w = window as unknown as Window & { __coreEventsMock?: unknown };
  if (w.__coreEventsMock) return;
  const realFetch: typeof fetch =
    typeof w.fetch === 'function' ? w.fetch.bind(w) : () => Promise.reject(new TypeError('fetch is unavailable'));
  const enc = new TextEncoder();
  const refusals = [...config.refusals];
  const heartbeatMs = config.heartbeatMs ?? 15000;
  const requests: CoreEventsRequestRecord[] = [];
  let seq = 0;

  interface MockStream {
    topics: string[];
    exclude: string[];
    key: string;
    write(text: string): boolean;
    end(): boolean;
  }
  const streams: MockStream[] = [];

  const matches = (pattern: string, topic: string) =>
    pattern === '*' || pattern === topic || (pattern.endsWith('.*') && topic.startsWith(pattern.slice(0, -1)));
  const frame = (topic: string, data: unknown, key: string, id?: string) =>
    `${id ? `id: ${id}\n` : ''}data: ${JSON.stringify({ topic, ts: new Date().toISOString(), key, data })}\n\n`;

  const refuse = (r: CoreEventsRefusal) => {
    const headers: Record<string, string> = { 'Content-Type': 'application/problem+json' };
    if (r.retryAfterSeconds != null) headers['Retry-After'] = String(r.retryAfterSeconds);
    const detail = r.detail ?? `refused by mockCoreEvents (${r.status})`;
    return new Response(
      JSON.stringify({ type: 'about:blank', title: detail, status: r.status, code: r.code ?? '', detail }),
      { status: r.status, headers },
    );
  };

  const openStream = (record: CoreEventsRequestRecord, signal: AbortSignal | null | undefined) => {
    let ctl!: ReadableStreamDefaultController<Uint8Array>;
    let live = true;
    let heartbeat: ReturnType<typeof setInterval> | null = null;
    const stop = () => {
      if (!live) return false;
      live = false;
      if (heartbeat) clearInterval(heartbeat);
      heartbeat = null;
      record.open = false;
      const i = streams.indexOf(s);
      if (i !== -1) streams.splice(i, 1);
      signal?.removeEventListener('abort', onAbort);
      return true;
    };
    const onAbort = () => {
      if (!stop()) return;
      try {
        ctl.error(new DOMException('aborted', 'AbortError'));
      } catch {
        // already closed
      }
    };
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        ctl = c;
      },
      cancel() {
        stop();
      },
    });
    const s: MockStream = {
      topics: record.topics,
      exclude: record.exclude,
      key: record.key,
      write(text) {
        if (!live) return false;
        try {
          ctl.enqueue(enc.encode(text));
          return true;
        } catch {
          return false;
        }
      },
      end() {
        if (!stop()) return false;
        try {
          ctl.close();
        } catch {
          // already errored
        }
        return true;
      },
    };
    streams.push(s);
    signal?.addEventListener('abort', onAbort, { once: true });
    if (heartbeatMs > 0) heartbeat = setInterval(() => s.write(': hb\n\n'), heartbeatMs);
    s.write('retry: 3000\n\n');
    if (config.autoOpened) s.write(frame('core.stream.opened.v1', { topics: record.topics }, ''));
    return new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream', 'Cache-Control': 'no-store' } });
  };

  w.fetch = (input: RequestInfo | URL, init?: RequestInit) => {
    const req = typeof Request !== 'undefined' && input instanceof Request ? input : null;
    const raw = req ? req.url : input instanceof URL ? input.href : String(input);
    let url: URL;
    try {
      url = new URL(raw, w.location.href);
    } catch {
      return realFetch(input, init);
    }
    if (url.pathname !== PATH) return realFetch(input, init);
    const signal = init?.signal ?? req?.signal;
    if (signal?.aborted) return Promise.reject(new DOMException('aborted', 'AbortError'));
    const headers = new Headers(init?.headers ?? req?.headers);
    const refusal = refusals.shift();
    const record: CoreEventsRequestRecord = {
      url: url.pathname + url.search,
      topics: url.searchParams.getAll('topic'),
      exclude: url.searchParams.getAll('exclude'),
      key: url.searchParams.get('key') ?? '',
      lastEventId: headers.get('Last-Event-ID') ?? '',
      answer: refusal ? refusal.status : 'stream',
      open: !refusal,
    };
    requests.push(record);
    return Promise.resolve(refusal ? refuse(refusal) : openStream(record, signal));
  };

  const controller: CoreEventsShimController = {
    requests,
    refuseNext(r) {
      refusals.push(r);
    },
    event(topic, data, opts = {}) {
      const key = opts.key ?? '';
      const id = opts.id ?? `mock-${++seq}`;
      let n = 0;
      for (const s of [...streams]) {
        if (!s.topics.some((p) => matches(p, topic))) continue;
        if (s.exclude.some((p) => matches(p, topic))) continue;
        if (s.key !== '' && s.key !== key) continue;
        if (s.write(frame(topic, data, key, id))) n++;
      }
      return n;
    },
    control(topic, data) {
      let n = 0;
      for (const s of [...streams]) if (s.write(frame(topic, data, ''))) n++;
      return n;
    },
    reset(reason) {
      return controller.control('core.stream.reset.v1', { reason });
    },
    shutdown() {
      const n = controller.control('core.stream.closed.v1', { reason: 'shutdown' });
      controller.endAll();
      return n;
    },
    endAll() {
      let n = 0;
      for (const s of [...streams]) if (s.end()) n++;
      return n;
    },
    openCount: () => streams.length,
  };
  w.__coreEventsMock = controller;
}

export interface MockCoreEventsOptions {
  autoOpened?: boolean;
  /** See CoreEventsShimConfig.heartbeatMs. */
  heartbeatMs?: number;
  /** Refuse the first requests, in order (e.g. a 429 with Retry-After). */
  refusals?: CoreEventsRefusal[];
}

/** The test-side handle; every call reaches the page's controller through page.evaluate. */
export interface CoreEventsMock {
  event(topic: string, data: unknown, opts?: CoreEventSendOptions): Promise<number>;
  control(topic: string, data: unknown): Promise<number>;
  reset(reason: string): Promise<number>;
  shutdown(): Promise<number>;
  endAll(): Promise<number>;
  refuseNext(refusal: CoreEventsRefusal): Promise<void>;
  requests(): Promise<CoreEventsRequestRecord[]>;
  openCount(): Promise<number>;
  /** Events-path requests that reached the network instead of the shim; should stay empty. */
  unmockedRequests(): string[];
  /** Throws naming every escaped request; a no-op while none has escaped. */
  assertNoUnmockedRequests(): void;
}

/** One entry per Page: the setup (shared by every call) and the options it was made with. */
const mockedPages = new WeakMap<Page, { key: string; handle: Promise<CoreEventsMock> }>();

function configOf(options: MockCoreEventsOptions): CoreEventsShimConfig {
  return {
    autoOpened: options.autoOpened ?? true,
    refusals: options.refusals ?? [],
    heartbeatMs: options.heartbeatMs ?? 15000,
  };
}

export function mockCoreEvents(page: Page, options: MockCoreEventsOptions = {}): Promise<CoreEventsMock> {
  const config = configOf(options);
  const key = JSON.stringify(config);
  const prior = mockedPages.get(page);
  if (prior) {
    // The shim is installed once per document and ignores a second config: say so instead of
    // letting the second caller think its refusals or heartbeat took effect.
    if (Object.keys(options).length > 0 && key !== prior.key) {
      return Promise.reject(
        new Error('mockCoreEvents: this page is already mocked with different options; configure it in the first call'),
      );
    }
    return prior.handle;
  }
  // Recorded before anything awaits, so two calls that race share one setup. A failed setup
  // stays recorded: the page may hold an init script, and a retry would add a second.
  const handle = setUp(page, config);
  mockedPages.set(page, { key, handle });
  return handle;
}

async function setUp(page: Page, config: CoreEventsShimConfig): Promise<CoreEventsMock> {
  await page.addInitScript(installCoreEventsShim, config);

  const unmocked: string[] = [];
  await page.route(
    (url) => url.pathname === CORE_EVENTS_PATH,
    async (route) => {
      unmocked.push(route.request().url());
      await route.fulfill({
        status: 500,
        contentType: 'application/problem+json',
        body: JSON.stringify({
          type: 'about:blank',
          title: 'unmocked event stream',
          status: 500,
          code: 'unmocked',
          detail: 'a request to /api/core/v1/events got past the mockCoreEvents shim',
        }),
      });
    },
  );

  const assertNoUnmockedRequests = () => {
    if (unmocked.length === 0) return;
    throw new Error(
      `mockCoreEvents: ${unmocked.length} request(s) to ${CORE_EVENTS_PATH} got past the shim ` +
        `(mockCoreEvents called after page.goto, or a fetch from a worker?): ${unmocked.join(', ')}`,
    );
  };

  const invoke = async <R>(method: keyof CoreEventsShimController, ...args: unknown[]): Promise<R> => {
    assertNoUnmockedRequests();
    return page.evaluate(
      ([m, a]) => {
        const ctl = (window as unknown as { __coreEventsMock?: Record<string, unknown> }).__coreEventsMock;
        if (!ctl) throw new Error('mockCoreEvents: the shim is not installed in this page (call it before page.goto)');
        const member = ctl[m];
        return (typeof member === 'function' ? member(...a) : member) as R;
      },
      [method, args] as [string, unknown[]],
    ) as Promise<R>;
  };

  return {
    event: (topic, data, opts) => invoke('event', topic, data, opts),
    control: (topic, data) => invoke('control', topic, data),
    reset: (reason) => invoke('reset', reason),
    shutdown: () => invoke('shutdown'),
    endAll: () => invoke('endAll'),
    refuseNext: (refusal) => invoke('refuseNext', refusal),
    requests: () => invoke('requests'),
    openCount: () => invoke('openCount'),
    unmockedRequests: () => [...unmocked],
    assertNoUnmockedRequests,
  };
}
