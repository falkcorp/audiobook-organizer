// file: web/src/services/coreHttp/transport.test.ts
// version: 0.1.0
// guid: 9a4f7c21-6d0e-4b85-a3f1-2c8e5d7b0416
// last-edited: 2026-11-20

import { ApiAuthRedirectError, ApiTimeoutError, isAuthRedirectError } from '@mediacore/api-client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  DEFAULT_TIMEOUT_MS,
  MAX_RETRY_AFTER_MS,
  createTransport,
  discardBody,
  parseRetryAfterMs,
  readJsonBody,
  type Refusal,
} from './transport';

class Refused extends Error {
  constructor(
    readonly op: string,
    readonly refusal: Refusal
  ) {
    super(`${op} ${refusal.status}`);
  }
}

interface Call {
  url: string;
  init: RequestInit;
  headers: Record<string, string>;
}

function fake(...answers: Response[]) {
  const calls: Call[] = [];
  const f: typeof fetch = (input, init = {}) => {
    const headers: Record<string, string> = {};
    new Headers(init.headers).forEach((v, k) => {
      headers[k] = v;
    });
    calls.push({ url: String(input), init, headers });
    return Promise.resolve(answers.shift() ?? new Response('no answer', { status: 500 }));
  };
  return { fetch: f, calls };
}

/** Pending until resolved; rejects with an AbortError when its signal aborts, as a real fetch does. */
function pendingFetch() {
  const signals: AbortSignal[] = [];
  let answer!: (r: Response) => void;
  const f: typeof fetch = (_u, init) =>
    new Promise<Response>((res, rej) => {
      answer = res;
      const sig = init?.signal as AbortSignal;
      signals.push(sig);
      const abort = () => rej(new DOMException('aborted', 'AbortError'));
      if (sig.aborted) abort();
      else sig.addEventListener('abort', abort);
    });
  return { fetch: f, signals, resolve: (r: Response) => answer(r) };
}

const jsonRes = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const problem = (
  status: number,
  code: string,
  detail: string,
  extra: Record<string, unknown> = {},
  headers: Record<string, string> = {}
) =>
  new Response(
    JSON.stringify({
      type: 'about:blank',
      title: detail,
      status,
      code,
      detail,
      request_id: 'req-alfred',
      ...extra,
    }),
    { status, headers: { 'Content-Type': 'application/problem+json', ...headers } }
  );

function opaqueRedirect(): Response {
  return new Proxy(
    { type: 'opaqueredirect', status: 0, ok: false },
    {
      get(t, p) {
        if (p === 'redirected') throw new Error('response.redirected must not be read');
        return (t as Record<string | symbol, unknown>)[p as string];
      },
    }
  ) as unknown as Response;
}

/** Headers arrive at once; the body then produces `first` and stalls for ever (fetch ignores the signal). */
function stalledBody(
  status = 200,
  headers: Record<string, string> = { 'Content-Type': 'application/json' },
  first?: string
) {
  let cancelled = false;
  const body = new ReadableStream<Uint8Array>({
    start(c) {
      if (first !== undefined) c.enqueue(new TextEncoder().encode(first));
    },
    cancel() {
      cancelled = true;
    },
  });
  return { res: new Response(body, { status, headers }), wasCancelled: () => cancelled };
}

let consoleSpies: Array<ReturnType<typeof vi.spyOn>> = [];
beforeEach(() => {
  consoleSpies = (['log', 'info', 'warn', 'error', 'debug'] as const).map((m) =>
    vi.spyOn(console, m).mockImplementation(() => {})
  );
});
afterEach(() => {
  for (const s of consoleSpies) expect(s).not.toHaveBeenCalled();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

const make = (o: Parameters<typeof createTransport>[0]) =>
  createTransport(o, (op, refusal) => new Refused(op, refusal));

async function caught(p: Promise<unknown>): Promise<unknown> {
  try {
    await p;
  } catch (e) {
    return e;
  }
  throw new Error('expected the call to reject');
}

describe('request shape', () => {
  it('sends cookies, no cache, manual redirect and no body or Content-Type for a bare GET', async () => {
    const f = fake(jsonRes({}));
    const res = await make({ fetch: f.fetch }).send('op', { method: 'GET', path: '/x' });
    expect(res.status).toBe(200);
    const c = f.calls[0];
    expect(c.url).toBe('/x');
    expect(c.init).toMatchObject({
      method: 'GET',
      credentials: 'include',
      cache: 'no-store',
      redirect: 'manual',
    });
    expect(c.init.body).toBeUndefined();
    expect(c.headers.accept).toBe('application/json');
    expect(c.headers['content-type']).toBeUndefined();
  });

  it('prefixes baseUrl, sends json as application/json and a Blob as octet-stream', async () => {
    const f = fake(jsonRes({}), jsonRes({}));
    const t = make({ fetch: f.fetch, baseUrl: 'https://abo.example.test' });
    await t.send('a', { method: 'POST', path: '/x', json: { k: 1 } });
    const blob = new Blob(['x']);
    await t.send('b', { method: 'POST', path: '/y?name=z', raw: blob });
    expect(f.calls[0].url).toBe('https://abo.example.test/x');
    expect(f.calls[0].headers['content-type']).toBe('application/json');
    expect(f.calls[0].init.body).toBe('{"k":1}');
    expect(f.calls[1].url).toBe('https://abo.example.test/y?name=z');
    expect(f.calls[1].headers['content-type']).toBe('application/octet-stream');
    expect(f.calls[1].init.body).toBe(blob);
    expect(t.baseUrl).toBe('https://abo.example.test');
  });
});

describe('refusals', () => {
  it('hands a decoded problem to the factory with every member, and never retries', async () => {
    const f = fake(
      problem(
        403,
        'permission_denied',
        'missing scope',
        {
          required_permission: 'core:system.manage',
          errors: [{ field: '/body/x', code: 'invalid', message: 'bad' }],
        },
        { 'Retry-After': '7' }
      ),
      jsonRes({})
    );
    const e = (await caught(
      make({ fetch: f.fetch }).send('op', { method: 'GET', path: '/x' })
    )) as Refused;
    expect(e).toBeInstanceOf(Refused);
    expect(e.op).toBe('op');
    expect(e.refusal).toEqual({
      status: 403,
      code: 'permission_denied',
      detail: 'missing scope',
      requestId: 'req-alfred',
      retryAfterMs: 7000,
      requiredPermission: 'core:system.manage',
      fields: [{ field: '/body/x', code: 'invalid', message: 'bad' }],
    });
    expect(f.calls).toHaveLength(1);
  });

  it('blanks the detail of a legacy envelope and of a proxy page', async () => {
    const legacy = fake(
      new Response(JSON.stringify({ error: 'secret-host refused' }), {
        status: 502,
        headers: { 'Content-Type': 'application/json' },
      })
    );
    const a = (await caught(
      make({ fetch: legacy.fetch }).send('op', { method: 'GET', path: '/x' })
    )) as Refused;
    expect([a.refusal.status, a.refusal.code, a.refusal.detail]).toEqual([502, 'legacy_error', '']);

    const html = fake(
      new Response('<html>secret-host</html>', {
        status: 502,
        headers: { 'Content-Type': 'text/html' },
      })
    );
    const b = (await caught(
      make({ fetch: html.fetch }).send('op', { method: 'GET', path: '/x' })
    )) as Refused;
    expect([b.refusal.code, b.refusal.detail]).toEqual(['legacy_error', '']);
    expect(JSON.stringify([a.refusal, b.refusal])).not.toContain('secret-host');
  });

  it('calls onUnauthorized, before the error is thrown, for a session-ending 401 only', async () => {
    const order: string[] = [];
    const onUnauthorized = (p: { code: string }) => order.push(`unauthorized:${p.code}`);
    const run = async (r: Response) => {
      const t = createTransport({ fetch: fake(r).fetch, onUnauthorized }, (op, refusal) => {
        order.push('error');
        return new Refused(op, refusal);
      });
      await caught(t.send('op', { method: 'GET', path: '/x' }));
    };
    await run(problem(401, 'authentication_required', 'sign in'));
    await run(problem(401, 'something_else', 'x'));
    await run(problem(403, 'authentication_required', 'x'));
    expect(order).toEqual(['unauthorized:authentication_required', 'error', 'error', 'error']);
  });
});

describe('SSO bounce', () => {
  it('an opaqueredirect is ApiAuthRedirectError and response.redirected is never read', async () => {
    const e = await caught(
      make({ fetch: fake(opaqueRedirect()).fetch }).send('op', { method: 'GET', path: '/x' })
    );
    expect(e).toBeInstanceOf(ApiAuthRedirectError);
    expect(isAuthRedirectError(e)).toBe(true);
  });

  it('a 200 text/html page is the same bounce, but an HTML error status is a refusal', async () => {
    const page = new Response('<html>log in</html>', {
      status: 200,
      headers: { 'Content-Type': 'text/html; charset=utf-8' },
    });
    expect(
      await caught(make({ fetch: fake(page).fetch }).send('op', { method: 'GET', path: '/x' }))
    ).toBeInstanceOf(ApiAuthRedirectError);
    const err = new Response('<html>nope</html>', {
      status: 500,
      headers: { 'Content-Type': 'text/html' },
    });
    expect(
      await caught(make({ fetch: fake(err).fetch }).send('op', { method: 'GET', path: '/x' }))
    ).toBeInstanceOf(Refused);
  });

  it('response.redirected is never read on the 200 text/html path either', async () => {
    const page = new Response('<html>log in</html>', {
      status: 200,
      headers: { 'Content-Type': 'text/html' },
    });
    // On the prototype, so a read of the response send() builds is caught too, not only of the one fetch gave.
    const redirected = vi.spyOn(Response.prototype, 'redirected', 'get');
    expect(
      await caught(make({ fetch: fake(page).fetch }).send('op', { method: 'GET', path: '/x' }))
    ).toBeInstanceOf(ApiAuthRedirectError);
    expect(redirected).not.toHaveBeenCalled();
  });

  it('a 204 with no Content-Type is an answer, not a bounce', async () => {
    const res = await make({ fetch: fake(new Response(null, { status: 204 })).fetch }).send('op', {
      method: 'DELETE',
      path: '/x',
    });
    expect(res.status).toBe(204);
  });
});

describe('deadline', () => {
  it('defaults to DEFAULT_TIMEOUT_MS and raises ApiTimeoutError, not the AbortError', async () => {
    vi.useFakeTimers();
    const pf = pendingFetch();
    const p = caught(make({ fetch: pf.fetch }).send('op', { method: 'GET', path: '/x' }));
    await vi.advanceTimersByTimeAsync(DEFAULT_TIMEOUT_MS - 1);
    expect(pf.signals[0].aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(2);
    const e = (await p) as ApiTimeoutError;
    expect(e).toBeInstanceOf(ApiTimeoutError);
    expect(e.message).toContain('30000');
  });

  it('timeoutMs sets the default and deadlineMs overrides it per request', async () => {
    vi.useFakeTimers();
    const a = pendingFetch();
    const p = caught(
      make({ fetch: a.fetch, timeoutMs: 1000 }).send('op', { method: 'GET', path: '/x' })
    );
    await vi.advanceTimersByTimeAsync(1001);
    expect(await p).toBeInstanceOf(ApiTimeoutError);

    const b = pendingFetch();
    const q = caught(
      make({ fetch: b.fetch, timeoutMs: 1000 }).send('op', {
        method: 'GET',
        path: '/x',
        deadlineMs: 5000,
      })
    );
    await vi.advanceTimersByTimeAsync(1001);
    expect(b.signals[0].aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(4000);
    expect(await q).toBeInstanceOf(ApiTimeoutError);
  });

  it.each([0, -1])('a deadline of %i means none: no timer, never aborted', async (ms) => {
    vi.useFakeTimers();
    for (const [opts, req] of [
      [{ timeoutMs: ms }, {}],
      [{}, { deadlineMs: ms }],
    ] as const) {
      const pf = pendingFetch();
      const p = make({ fetch: pf.fetch, ...opts }).send('op', {
        method: 'GET',
        path: '/x',
        ...req,
      });
      expect(vi.getTimerCount()).toBe(0);
      await vi.advanceTimersByTimeAsync(24 * 3600 * 1000);
      expect(pf.signals[0].aborted).toBe(false);
      pf.resolve(jsonRes({}));
      await expect(p).resolves.toBeInstanceOf(Response);
    }
  });

  it('a caller abort stays an AbortError (before and during the request), not a timeout', async () => {
    const early = new AbortController();
    early.abort();
    const pf1 = pendingFetch();
    const e1 = (await caught(
      make({ fetch: pf1.fetch }).send('op', { method: 'GET', path: '/x', signal: early.signal })
    )) as Error;
    expect(e1.name).toBe('AbortError');

    const ctl = new AbortController();
    const pf2 = pendingFetch();
    const p = caught(
      make({ fetch: pf2.fetch }).send('op', { method: 'GET', path: '/x', signal: ctl.signal })
    );
    ctl.abort();
    const e2 = (await p) as Error;
    expect(e2).not.toBeInstanceOf(ApiTimeoutError);
    expect(e2.name).toBe('AbortError');
  });

  it('clears the timer and removes the abort listener after an answer and after a failure', async () => {
    vi.useFakeTimers();
    const ctl = new AbortController();
    const add = vi.spyOn(ctl.signal, 'addEventListener');
    const remove = vi.spyOn(ctl.signal, 'removeEventListener');
    const t = make({ fetch: fake(jsonRes({})).fetch });
    const res = await t.send('op', { method: 'GET', path: '/x', signal: ctl.signal });
    // The deadline and the signal stay armed until the body has been read (#4819 r3 m2).
    expect(vi.getTimerCount()).toBe(1);
    await res.json();
    expect(vi.getTimerCount()).toBe(0);
    expect(remove.mock.calls[0][1]).toBe(add.mock.calls[0][1]);
    const boom: typeof fetch = () => Promise.reject(new TypeError('network down'));
    await caught(
      make({ fetch: boom }).send('op', { method: 'GET', path: '/x', signal: ctl.signal })
    );
    expect(vi.getTimerCount()).toBe(0);
    expect([add.mock.calls.length, remove.mock.calls.length]).toEqual([2, 2]);
  });
});

describe('the body read is covered by the deadline and the caller signal', () => {
  it('a body that stalls past the deadline is an ApiTimeoutError naming the endpoint only', async () => {
    vi.useFakeTimers();
    const stalled = stalledBody(200, { 'Content-Type': 'application/json' }, '{"half":');
    const res = await make({
      fetch: fake(stalled.res).fetch,
      baseUrl: 'https://abo.example.test',
    }).send('op', { method: 'GET', path: '/x?name=robin-secret', deadlineMs: 100 });
    const p = caught(res.text());
    await vi.advanceTimersByTimeAsync(99);
    expect(vi.getTimerCount()).toBe(1);
    await vi.advanceTimersByTimeAsync(2);
    const e = (await p) as ApiTimeoutError;
    expect(e).toBeInstanceOf(ApiTimeoutError);
    expect(e.message).not.toContain('robin-secret');
    expect(e.url).toBe('https://abo.example.test/x');
    expect(stalled.wasCancelled()).toBe(true);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('a body that stalls after the caller aborts is the caller AbortError, not a timeout', async () => {
    const ctl = new AbortController();
    const stalled = stalledBody();
    const res = await make({ fetch: fake(stalled.res).fetch }).send('op', {
      method: 'GET',
      path: '/x',
      signal: ctl.signal,
    });
    const p = caught(res.text());
    ctl.abort();
    const e = (await p) as Error;
    expect(e).not.toBeInstanceOf(ApiTimeoutError);
    expect(e.name).toBe('AbortError');
    expect(stalled.wasCancelled()).toBe(true);
  });

  it('an error answer whose body stalls ends in the timeout, not a refusal with the status alone', async () => {
    vi.useFakeTimers();
    const stalled = stalledBody(503, { 'Content-Type': 'application/problem+json' });
    const p = caught(
      make({ fetch: fake(stalled.res).fetch }).send('op', {
        method: 'GET',
        path: '/x',
        deadlineMs: 100,
      })
    );
    await vi.advanceTimersByTimeAsync(101);
    expect(await p).toBeInstanceOf(ApiTimeoutError);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('a body read in time is unaffected and releases the timer and the listener', async () => {
    vi.useFakeTimers();
    const ctl = new AbortController();
    const remove = vi.spyOn(ctl.signal, 'removeEventListener');
    const res = await make({ fetch: fake(jsonRes({ ok: 'robin' })).fetch }).send('op', {
      method: 'GET',
      path: '/x',
      signal: ctl.signal,
    });
    await vi.advanceTimersByTimeAsync(DEFAULT_TIMEOUT_MS - 1);
    await expect(res.json()).resolves.toEqual({ ok: 'robin' });
    expect(vi.getTimerCount()).toBe(0);
    expect(remove).toHaveBeenCalledTimes(1);
    expect(res.status).toBe(200);
    expect(res.headers.get('Content-Type')).toBe('application/json');
  });

  it('discardBody releases the timer and the listener without reading', async () => {
    vi.useFakeTimers();
    const stalled = stalledBody();
    const res = await make({ fetch: fake(stalled.res).fetch }).send('op', {
      method: 'GET',
      path: '/x',
    });
    expect(vi.getTimerCount()).toBe(1);
    discardBody(res);
    await vi.advanceTimersByTimeAsync(0);
    expect(vi.getTimerCount()).toBe(0);
    expect(stalled.wasCancelled()).toBe(true);
  });

  it('a 204 has no body to bound: nothing stays armed', async () => {
    vi.useFakeTimers();
    await make({ fetch: fake(new Response(null, { status: 204 })).fetch }).send('op', {
      method: 'DELETE',
      path: '/x',
    });
    expect(vi.getTimerCount()).toBe(0);
  });

  it('with no deadline a stalled body ends only by the caller signal', async () => {
    vi.useFakeTimers();
    const ctl = new AbortController();
    const res = await make({ fetch: fake(stalledBody().res).fetch }).send('op', {
      method: 'GET',
      path: '/x',
      deadlineMs: 0,
      signal: ctl.signal,
    });
    const p = caught(res.text());
    await vi.advanceTimersByTimeAsync(24 * 3600 * 1000);
    ctl.abort();
    expect(((await p) as Error).name).toBe('AbortError');
  });
});

describe('readJsonBody', () => {
  it('reads JSON, and reports a body that is not JSON as ok:false', async () => {
    expect(await readJsonBody(jsonRes({ a: 1 }))).toEqual({ ok: true, value: { a: 1 } });
    expect(await readJsonBody(new Response('<html/>'))).toEqual({ ok: false });
    expect(await readJsonBody(new Response(''))).toEqual({ ok: false });
  });

  it('rethrows the deadline and the caller abort instead of calling them "not JSON"', async () => {
    vi.useFakeTimers();
    const res = await make({ fetch: fake(stalledBody().res).fetch }).send('op', {
      method: 'GET',
      path: '/x',
      deadlineMs: 50,
    });
    const p = caught(readJsonBody(res));
    await vi.advanceTimersByTimeAsync(51);
    expect(await p).toBeInstanceOf(ApiTimeoutError);
  });
});

describe('errors never carry the query', () => {
  it('a timeout and a bounce name the endpoint only', async () => {
    vi.useFakeTimers();
    const pf = pendingFetch();
    const p = caught(
      make({ fetch: pf.fetch, baseUrl: 'https://abo.example.test' }).send('op', {
        method: 'POST',
        path: '/import?name=robin-secret.archive',
        deadlineMs: 100,
      })
    );
    await vi.advanceTimersByTimeAsync(101);
    const t = (await p) as ApiTimeoutError;
    expect(t).toBeInstanceOf(ApiTimeoutError);
    expect(t.message).toContain('https://abo.example.test/import');
    expect(t.message).not.toContain('robin-secret');
    expect(t.url).toBe('https://abo.example.test/import');

    const b = (await caught(
      make({ fetch: fake(opaqueRedirect()).fetch }).send('op', {
        method: 'POST',
        path: '/import?name=robin-secret.archive',
      })
    )) as ApiAuthRedirectError;
    expect(b).toBeInstanceOf(ApiAuthRedirectError);
    expect(`${b.message}${JSON.stringify(b)}`).not.toContain('robin-secret');

    const page = new Response('<html/>', { status: 200, headers: { 'Content-Type': 'text/html' } });
    const h = await caught(
      make({ fetch: fake(page).fetch }).send('op', { method: 'GET', path: '/x?name=robin-secret' })
    );
    expect(`${(h as Error).message}${JSON.stringify(h)}`).not.toContain('robin-secret');
  });
});

describe('parseRetryAfterMs', () => {
  const now = Date.parse('2026-11-20T10:00:00Z');

  it('reads delta-seconds and HTTP dates', () => {
    expect(parseRetryAfterMs('5')).toBe(5000);
    expect(parseRetryAfterMs(' 3 ')).toBe(3000);
    expect(parseRetryAfterMs('0')).toBe(0);
    expect(parseRetryAfterMs('Fri, 20 Nov 2026 10:00:10 GMT', () => now)).toBe(10000);
    expect(parseRetryAfterMs('Fri, 20 Nov 2026 09:00:00 GMT', () => now)).toBe(0);
  });

  it('refuses absent, empty, negative and junk values', () => {
    for (const v of [null, undefined, '', '   ', '-5', '1.5', 'soon', '2026-11-20']) {
      expect(parseRetryAfterMs(v, () => now)).toBeNull();
    }
  });

  it('clamps to ten minutes, the same ceiling as coreEvents', () => {
    expect(MAX_RETRY_AFTER_MS).toBe(600000);
    expect(parseRetryAfterMs('600')).toBe(600000);
    expect(parseRetryAfterMs('601')).toBe(MAX_RETRY_AFTER_MS);
    expect(parseRetryAfterMs('999999999')).toBe(MAX_RETRY_AFTER_MS);
    expect(parseRetryAfterMs('Sat, 21 Nov 2026 10:00:00 GMT', () => now)).toBe(MAX_RETRY_AFTER_MS);
  });
});
