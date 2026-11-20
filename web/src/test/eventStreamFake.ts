// file: web/src/test/eventStreamFake.ts
// version: 0.1.0
// guid: d9d4c32c-5e73-4270-b1b8-2fd0e9f6102a
// last-edited: 2026-11-19

// Test double for the core event stream's server: a fetch whose answers the test
// scripts, and an SSE body the test feeds frame by frame. Fixtures name Batman books
// and characters only.

export interface FakeCall {
  url: string;
  init: RequestInit;
  headers: Record<string, string>;
}

export interface FakeStream {
  response: Response;
  /** Write raw wire text. */
  write(text: string): void;
  /** Write one event frame. */
  event(id: string, topic: string, data: unknown, key?: string): void;
  /** Write one id-less control frame. */
  control(topic: string, data: unknown): void;
  /** The server ends the body cleanly. */
  end(): void;
}

const enc = new TextEncoder();

export function frameText(topic: string, data: unknown, key = '', id?: string): string {
  const env = JSON.stringify({ topic, ts: '2026-11-19T00:00:00Z', key, data });
  return `${id === undefined ? '' : `id: ${id}\n`}data: ${env}\n\n`;
}

/** An open stream answer that errors its body when the request is aborted. */
export function sseStream(signal?: AbortSignal | null): FakeStream {
  let ctl!: ReadableStreamDefaultController<Uint8Array>;
  // The abort listener is detached once the stream is over (aborted, cancelled by
  // its reader, or ended by the server), so a long-lived signal never keeps a
  // finished fake's controller reachable.
  let detach = () => {};
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      ctl = c;
      if (!signal) return;
      const onAbort = () => {
        detach();
        try {
          c.error(new DOMException('aborted', 'AbortError'));
        } catch {
          // already closed
        }
      };
      detach = () => signal.removeEventListener('abort', onAbort);
      signal.addEventListener('abort', onAbort, { once: true });
    },
    cancel() {
      detach();
    },
  });
  const response = new Response(body, {
    status: 200,
    headers: { 'Content-Type': 'text/event-stream' },
  });
  const write = (text: string) => {
    try {
      ctl.enqueue(enc.encode(text));
    } catch {
      // closed or errored: the reader already saw why
    }
  };
  return {
    response,
    write,
    event: (id, topic, data, key = '') => write(frameText(topic, data, key, id)),
    control: (topic, data) => write(frameText(topic, data)),
    end() {
      detach();
      try {
        ctl.close();
      } catch {
        // already closed
      }
    },
  };
}

export function problemResponse(
  status: number,
  code: string,
  detail: string,
  headers: Record<string, string> = {},
): Response {
  return new Response(JSON.stringify({ type: 'about:blank', title: detail, status, code, detail }), {
    status,
    headers: { 'Content-Type': 'application/problem+json', ...headers },
  });
}

export type Responder = (call: FakeCall) => Response | Promise<Response>;

/**
 * A scripted fetch. `answer` queues one responder per call, in order; once the queue
 * is empty `fallback` answers (default: 503). `streams` collects every stream it served.
 */
export function createFakeServer() {
  const calls: FakeCall[] = [];
  const queue: Responder[] = [];
  const streams: FakeStream[] = [];
  let fallback: Responder = () => problemResponse(503, 'unavailable', 'try later');

  const fetchImpl: typeof fetch = (input, init = {}) => {
    const headers: Record<string, string> = {};
    new Headers(init.headers).forEach((v, k) => {
      headers[k] = v;
    });
    const call: FakeCall = { url: String(input), init, headers };
    calls.push(call);
    const responder = queue.shift() ?? fallback;
    return Promise.resolve(responder(call));
  };

  return {
    fetch: fetchImpl,
    calls,
    streams,
    answer(r: Responder) {
      queue.push(r);
    },
    /** Queue an open stream answer and return it. */
    openStream(): FakeStream {
      let s: FakeStream | undefined;
      queue.push((call) => {
        s = sseStream(call.init.signal);
        streams.push(s);
        return s.response;
      });
      // The stream exists once the call is made; hand back a lazy proxy.
      return new Proxy({} as FakeStream, {
        get: (_t, prop: keyof FakeStream) => {
          if (!s) throw new Error('fake stream not requested yet');
          return s[prop];
        },
      });
    },
    setFallback(r: Responder) {
      fallback = r;
    },
  };
}
