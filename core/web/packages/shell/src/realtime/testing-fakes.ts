// file: core/web/packages/shell/src/realtime/testing-fakes.ts
// version: 0.1.0
// guid: 86f81e20-a2d1-42d9-812e-94f999f10036
// last-edited: 2026-11-20

// Deterministic stand-ins for the browser APIs the realtime client injects: a
// fetch that serves SSE bodies as ReadableStreams, a Web Locks manager and a
// BroadcastChannel network. They are test doubles only (no timers of their own;
// delivery is by microtask, or held back by FakeBroadcastNetwork.hold).

import type { CloseReason, RealtimeEvent, RealtimeListener, ResetReason } from '../contracts';
import type { DocumentTarget, PageTarget } from './client';

/** A RealtimeListener that writes down everything it is told. */
export function recordingListener() {
  const events: RealtimeEvent[] = [];
  const resets: ResetReason[] = [];
  const closed: CloseReason[] = [];
  const errors: (number | undefined)[] = [];
  const listener: RealtimeListener = {
    onEvent: (e) => void events.push(e),
    onReset: (r) => void resets.push(r),
    onClosed: (r) => void closed.push(r),
    onError: (s) => void errors.push(s),
  };
  return { listener, events, resets, closed, errors };
}

// fetch --------------------------------------------------------------------

/** What the next fetch does. 'accept' (the default once the plan runs out) is a 200 text/event-stream. */
export type FakeStep = 'accept' | 'fail' | 'redirect' | { status: number; headers?: Record<string, string> };

export class FakeStream {
  private readonly encoder = new TextEncoder();
  private done = false;
  constructor(private readonly controller: ReadableStreamDefaultController<Uint8Array>) {}
  push(text: string): void {
    if (!this.done) this.controller.enqueue(this.encoder.encode(text));
  }
  /** A clean end of stream, as when the server closes the connection. */
  end(): void {
    if (this.done) return;
    this.done = true;
    this.controller.close();
  }
  fail(): void {
    if (this.done) return;
    this.done = true;
    this.controller.error(new TypeError('network error'));
  }
}

export class FakeCall {
  stream: FakeStream | null = null;
  constructor(
    readonly url: string,
    readonly init: RequestInit,
  ) {}
  get signal(): AbortSignal {
    return this.init.signal as AbortSignal;
  }
  get aborted(): boolean {
    return this.signal.aborted;
  }
  get lastEventId(): string | null {
    return new Headers(this.init.headers).get('Last-Event-ID');
  }
}

function abortError(): Error {
  return new DOMException('The operation was aborted.', 'AbortError');
}

export class FakeFetch {
  readonly calls: FakeCall[] = [];
  /** Steps consumed one per call. */
  plan: FakeStep[] = [];
  readonly fetch: typeof fetch = (input, init) => this.handle(String(input), init ?? {});

  /** Calls whose request is still open (not aborted). */
  get open(): FakeCall[] {
    return this.calls.filter((c) => !c.aborted);
  }

  private handle(url: string, init: RequestInit): Promise<Response> {
    const call = new FakeCall(url, init);
    this.calls.push(call);
    const step = this.plan.shift() ?? 'accept';
    const signal = init.signal as AbortSignal;
    return new Promise<Response>((resolve, reject) => {
      if (signal.aborted) return reject(abortError());
      if (step === 'fail') return reject(new TypeError('network error'));
      if (step === 'redirect') {
        return resolve({
          type: 'opaqueredirect',
          status: 0,
          ok: false,
          url: 'https://sso.wayne.example/login',
          headers: new Headers(),
          body: null,
          redirected: false,
        } as unknown as Response);
      }
      if (step !== 'accept') {
        const body = JSON.stringify({ type: 'about:blank', title: 'refused', status: step.status });
        return resolve(
          new Response(body, { status: step.status, headers: { 'Content-Type': 'application/problem+json', ...step.headers } }),
        );
      }
      const body = new ReadableStream<Uint8Array>({
        start: (controller) => {
          call.stream = new FakeStream(controller);
          // Like a real fetch body: aborting the request errors the stream.
          signal.addEventListener('abort', () => {
            try {
              controller.error(abortError());
            } catch {
              // already closed
            }
          });
        },
      });
      resolve(new Response(body, { status: 200, headers: { 'Content-Type': 'text/event-stream' } }));
    });
  }
}

/** One SSE message as the native stream frames it (05-realtime §4.2): no `event:` line. */
export function frame(topic: string, data: unknown, opts: { id?: string; key?: string } = {}): string {
  const envelope = { topic, ts: '2026-11-20T00:00:00Z', ...(opts.key === undefined ? {} : { key: opts.key }), data };
  return `${opts.id === undefined ? '' : `id: ${opts.id}\n`}data: ${JSON.stringify(envelope)}\n\n`;
}
export const openedFrame = (): string => `retry: 3000\n\n${frame('core.stream.opened.v1', { topics: ['*'] })}`;
export const resetFrame = (reason: string): string => frame('core.stream.reset.v1', { reason });
export const closedFrame = (reason: string): string => frame('core.stream.closed.v1', { reason });

// Web Locks ----------------------------------------------------------------

interface Waiter {
  granted: boolean;
  run(): Promise<void>;
}

/** The part of LockManager the client uses: exclusive locks, FIFO queue, abortable while queued. */
export class FakeLockManager {
  private readonly queues = new Map<string, Waiter[]>();
  private readonly held = new Set<string>();

  asLockManager(): LockManager {
    return this as unknown as LockManager;
  }
  isHeld(name: string): boolean {
    return this.held.has(name);
  }
  pending(name: string): number {
    return this.queues.get(name)?.length ?? 0;
  }

  request(name: string, a: LockOptions | ((lock: Lock | null) => unknown), b?: (lock: Lock | null) => unknown): Promise<unknown> {
    const options: LockOptions = typeof a === 'function' ? {} : a;
    const callback = typeof a === 'function' ? a : b!;
    return new Promise((resolve, reject) => {
      if (options.signal?.aborted) return reject(abortError());
      const waiter: Waiter = {
        granted: false,
        run: async () => {
          waiter.granted = true;
          try {
            resolve(await callback({ name, mode: 'exclusive' } as Lock));
          } catch (err) {
            reject(err);
          } finally {
            this.held.delete(name);
            this.next(name);
          }
        },
      };
      options.signal?.addEventListener('abort', () => {
        if (waiter.granted) return;
        const queue = this.queues.get(name) ?? [];
        const at = queue.indexOf(waiter);
        if (at !== -1) queue.splice(at, 1);
        reject(abortError());
      });
      const queue = this.queues.get(name) ?? [];
      queue.push(waiter);
      this.queues.set(name, queue);
      if (!this.held.has(name)) this.next(name);
    });
  }

  private next(name: string): void {
    const waiter = this.queues.get(name)?.shift();
    if (waiter === undefined) return;
    this.held.add(name);
    queueMicrotask(() => void waiter.run());
  }
}

// BroadcastChannel ---------------------------------------------------------

export class FakeChannel {
  onmessage: ((ev: { data: unknown }) => void) | null = null;
  onmessageerror: (() => void) | null = null;
  closed = false;
  /** When set, this endpoint's messages go nowhere (a tab that has hung or crashed). */
  muted = false;
  readonly posted: unknown[] = [];

  constructor(
    readonly name: string,
    private readonly network: FakeBroadcastNetwork,
  ) {}

  postMessage(data: unknown): void {
    if (this.closed) throw new DOMException('Channel is closed', 'InvalidStateError');
    this.posted.push(data);
    if (this.muted) return;
    this.network.route(this, structuredClone(data));
  }
  close(): void {
    this.closed = true;
    this.network.remove(this);
  }
  asBroadcastChannel(): BroadcastChannel {
    return this as unknown as BroadcastChannel;
  }
}

/** Every channel of one name is one bus; a sender does not hear itself, as with the real thing. */
export class FakeBroadcastNetwork {
  private readonly channels = new Set<FakeChannel>();
  private queue: (() => void)[] = [];
  /** While true, deliveries queue up until release(): lets a test make two messages cross. */
  hold = false;

  /** `channel` option for createRealtimeClient; endpoints are kept in `endpoints` per tab. */
  readonly endpoints: FakeChannel[] = [];
  open = (name: string): BroadcastChannel => {
    const channel = new FakeChannel(name, this);
    this.channels.add(channel);
    this.endpoints.push(channel);
    return channel.asBroadcastChannel();
  };

  get size(): number {
    return this.channels.size;
  }

  route(from: FakeChannel, data: unknown): void {
    for (const to of this.channels) {
      if (to === from || to.name !== from.name) continue;
      this.queue.push(() => {
        if (!to.closed) to.onmessage?.({ data });
      });
    }
    if (!this.hold) this.schedule();
  }
  remove(channel: FakeChannel): void {
    this.channels.delete(channel);
  }
  release(): void {
    this.hold = false;
    this.schedule();
  }
  private schedule(): void {
    queueMicrotask(() => {
      const batch = this.queue;
      this.queue = [];
      for (const deliver of batch) deliver();
      if (this.queue.length > 0 && !this.hold) this.schedule();
    });
  }
}

// Page ---------------------------------------------------------------------

/** A window or document stand-in the test fires lifecycle events on. */
export class FakePage implements PageTarget, DocumentTarget {
  visibilityState: DocumentVisibilityState = 'visible';
  private readonly listeners = new Map<string, Set<() => void>>();
  addEventListener(type: string, listener: () => void): void {
    const set = this.listeners.get(type) ?? new Set();
    set.add(listener);
    this.listeners.set(type, set);
  }
  removeEventListener(type: string, listener: () => void): void {
    this.listeners.get(type)?.delete(listener);
  }
  fire(type: string): void {
    for (const l of [...(this.listeners.get(type) ?? [])]) l();
  }
  listenerCount(): number {
    return [...this.listeners.values()].reduce((n, s) => n + s.size, 0);
  }
  setVisibility(state: DocumentVisibilityState): void {
    this.visibilityState = state;
    this.fire('visibilitychange');
  }
}
