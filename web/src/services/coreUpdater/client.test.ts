// file: web/src/services/coreUpdater/client.test.ts
// version: 0.1.0
// guid: a16b8c6d-7b08-4a59-a02f-6dfa3fbdb997
// last-edited: 2026-11-20

import { ApiAuthRedirectError, ApiTimeoutError, isAuthRedirectError } from '@mediacore/api-client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  UPDATER_APPLY_PATH,
  UPDATER_CHECK_PATH,
  UPDATER_PATH,
  UPDATER_ROLLBACK_PATH,
  createUpdaterClient,
  parseRetryAfterMs,
  MAX_RETRY_AFTER_MS,
} from './client';
import { UpdaterProtocolError, UpdaterRequestError, type UpdaterStatus } from './types';

interface Call {
  url: string;
  init: RequestInit;
  headers: Record<string, string>;
}

function fake(...answers: Array<Response | ((c: Call) => Response | Promise<Response>)>) {
  const calls: Call[] = [];
  const f: typeof fetch = (input, init = {}) => {
    const headers: Record<string, string> = {};
    new Headers(init.headers).forEach((v, k) => {
      headers[k] = v;
    });
    const call = { url: String(input), init, headers };
    calls.push(call);
    const a = answers.shift();
    if (!a) return Promise.resolve(new Response('no answer', { status: 500 }));
    return Promise.resolve(typeof a === 'function' ? a(call) : a);
  };
  return { fetch: f, calls };
}

const jsonRes = (body: unknown, status = 200, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json', ...headers } });

const problem = (status: number, code: string, detail: string, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify({ type: 'about:blank', title: detail, status, code, detail, request_id: 'req-bruce' }), {
    status,
    headers: { 'Content-Type': 'application/problem+json', ...headers },
  });

function opaqueRedirect(): Response {
  const r = new Response(null, { status: 200 });
  Object.defineProperty(r, 'type', { value: 'opaqueredirect' });
  return r;
}

const STATUS: UpdaterStatus = {
  mode: 'check',
  channel: 'stable',
  current: 'v1.0.0',
  latest: 'v1.1.0',
  available: true,
  last_checked: '2026-11-20T10:00:00Z',
  previous_kept: 'v0.9.0',
};

let consoleSpies: Array<ReturnType<typeof vi.spyOn>> = [];
beforeEach(() => {
  consoleSpies = (['log', 'info', 'warn', 'error', 'debug'] as const).map((m) =>
    vi.spyOn(console, m).mockImplementation(() => {}),
  );
});
afterEach(() => {
  vi.useRealTimers();
  vi.restoreAllMocks();
});

describe('status', () => {
  it('GETs /api/core/v1/updater with credentials, manual redirects and no body', async () => {
    const s = fake(jsonRes(STATUS));
    const got = await createUpdaterClient({ fetch: s.fetch }).status();
    expect(got).toEqual(STATUS);
    const c = s.calls[0];
    expect(c.url).toBe(UPDATER_PATH);
    expect(c.url).toBe('/api/core/v1/updater');
    expect(c.init.method).toBe('GET');
    expect(c.init.redirect).toBe('manual');
    expect(c.init.credentials).toBe('include');
    expect(c.init.cache).toBe('no-store');
    expect(c.init.body).toBeUndefined();
    expect(c.headers.accept).toBe('application/json');
  });

  it('prefixes baseUrl', async () => {
    const s = fake(jsonRes(STATUS));
    await createUpdaterClient({ fetch: s.fetch, baseUrl: 'https://abo.example.test' }).status();
    expect(s.calls[0].url).toBe('https://abo.example.test/api/core/v1/updater');
  });

  it('rejects an answer without the required members', async () => {
    const s = fake(jsonRes({ mode: 'check' }));
    await expect(createUpdaterClient({ fetch: s.fetch }).status()).rejects.toBeInstanceOf(UpdaterProtocolError);
  });

  it('rejects a non-object answer', async () => {
    const s = fake(jsonRes([1, 2]));
    await expect(createUpdaterClient({ fetch: s.fetch }).status()).rejects.toBeInstanceOf(UpdaterProtocolError);
  });

  it('rejects a 2xx that is not JSON', async () => {
    const s = fake(new Response('ok', { status: 200, headers: { 'Content-Type': 'text/plain' } }));
    await expect(createUpdaterClient({ fetch: s.fetch }).status()).rejects.toBeInstanceOf(UpdaterProtocolError);
  });
});

describe('check and rollback', () => {
  it('POST check with no body and returns the task id', async () => {
    const s = fake(jsonRes({ task_id: 'run-gotham-1' }, 202));
    const got = await createUpdaterClient({ fetch: s.fetch }).check();
    expect(got).toEqual({ task_id: 'run-gotham-1' });
    const c = s.calls[0];
    expect(c.url).toBe(UPDATER_CHECK_PATH);
    expect(c.init.method).toBe('POST');
    expect(c.init.body).toBeUndefined();
    expect(c.headers['content-type']).toBeUndefined();
    expect(c.init.redirect).toBe('manual');
  });

  it('POST rollback with no body', async () => {
    const s = fake(jsonRes({ task_id: 'run-gotham-2' }, 202));
    const got = await createUpdaterClient({ fetch: s.fetch }).rollback();
    expect(got.task_id).toBe('run-gotham-2');
    expect(s.calls[0].url).toBe(UPDATER_ROLLBACK_PATH);
    expect(s.calls[0].init.method).toBe('POST');
    expect(s.calls[0].init.body).toBeUndefined();
  });

  it('refuses a 200 where 202 is documented, and a 202 without a task id', async () => {
    const a = fake(jsonRes({ task_id: 'x' }, 200));
    await expect(createUpdaterClient({ fetch: a.fetch }).check()).rejects.toBeInstanceOf(UpdaterProtocolError);
    const b = fake(jsonRes({}, 202));
    await expect(createUpdaterClient({ fetch: b.fetch }).check()).rejects.toBeInstanceOf(UpdaterProtocolError);
    const c = fake(jsonRes({ task_id: '' }, 202));
    await expect(createUpdaterClient({ fetch: c.fetch }).rollback()).rejects.toBeInstanceOf(UpdaterProtocolError);
  });
});

describe('apply', () => {
  it('sends only {version}, trimmed, as JSON', async () => {
    const s = fake(jsonRes({ task_id: 'run-gotham-3' }, 202));
    const got = await createUpdaterClient({ fetch: s.fetch }).apply('  v1.1.0 ');
    expect(got.task_id).toBe('run-gotham-3');
    const c = s.calls[0];
    expect(c.url).toBe(UPDATER_APPLY_PATH);
    expect(c.init.method).toBe('POST');
    expect(c.headers['content-type']).toBe('application/json');
    expect(JSON.parse(String(c.init.body))).toEqual({ version: 'v1.1.0' });
    expect(c.init.redirect).toBe('manual');
  });

  it('never names a release asset', async () => {
    const s = fake(jsonRes({ task_id: 'r' }, 202));
    await createUpdaterClient({ fetch: s.fetch }).apply('v1.1.0');
    const body = String(s.calls[0].init.body);
    expect(Object.keys(JSON.parse(body))).toEqual(['version']);
    expect(body).not.toMatch(/asset|audiobook-organizer/i);
    expect(s.calls[0].url).not.toMatch(/asset/i);
  });

  it.each([undefined, '', '   '])('sends no body for the offered release (%j)', async (v) => {
    const s = fake(jsonRes({ task_id: 'r' }, 202));
    await createUpdaterClient({ fetch: s.fetch }).apply(v);
    expect(s.calls[0].init.body).toBeUndefined();
    expect(s.calls[0].headers['content-type']).toBeUndefined();
  });

  it.each([
    [409, 'updater_off'],
    [409, 'unversioned_build'],
    [409, 'local_build_needs_version'],
    [409, 'no_update'],
    [409, 'not_newer'],
    [422, 'invalid_version'],
  ])('decodes the %i %s refusal and does not retry', async (status, code) => {
    const s = fake(problem(status, code, 'refused'), jsonRes({ task_id: 'must-not-be-reached' }, 202));
    const err = await createUpdaterClient({ fetch: s.fetch }).apply('v1.1.0').catch((e: unknown) => e);
    expect(err).toBeInstanceOf(UpdaterRequestError);
    const e = err as UpdaterRequestError;
    expect(e.status).toBe(status);
    expect(e.code).toBe(code);
    expect(e.detail).toBe('refused');
    expect(e.requestId).toBe('req-bruce');
    expect(e.isApplyRefusal).toBe(true);
    expect(s.calls).toHaveLength(1);
  });

  it('carries field errors of a 422', async () => {
    const body = {
      type: 'about:blank',
      title: 'invalid',
      status: 422,
      code: 'validation_failed',
      errors: [{ pointer: '/body/version', code: 'invalid_version', detail: 'must be a release tag such as v1.2.3' }],
    };
    const s = fake(
      new Response(JSON.stringify(body), { status: 422, headers: { 'Content-Type': 'application/problem+json' } }),
    );
    const e = (await createUpdaterClient({ fetch: s.fetch }).apply('nope').catch((x: unknown) => x)) as UpdaterRequestError;
    expect(e.fields).toEqual(body.errors);
  });
});

describe('error decoding', () => {
  it('flags interactive_required and carries the required permission', async () => {
    const body = {
      type: 'about:blank',
      title: 'forbidden',
      status: 403,
      code: 'interactive_required',
      detail: 'this route needs an interactive session',
      required_permission: 'core:admin',
    };
    const s = fake(
      new Response(JSON.stringify(body), { status: 403, headers: { 'Content-Type': 'application/problem+json' } }),
    );
    const e = (await createUpdaterClient({ fetch: s.fetch }).rollback().catch((x: unknown) => x)) as UpdaterRequestError;
    expect(e.needsInteractiveSession).toBe(true);
    expect(e.isApplyRefusal).toBe(false);
    expect(e.requiredPermission).toBe('core:admin');
  });

  it('a plain 403 forbidden is not an interactive-session problem', async () => {
    const s = fake(problem(403, 'forbidden', 'no'));
    const e = (await createUpdaterClient({ fetch: s.fetch }).status().catch((x: unknown) => x)) as UpdaterRequestError;
    expect(e.needsInteractiveSession).toBe(false);
  });

  it('interactive_required only counts on a 403', async () => {
    const s = fake(problem(409, 'interactive_required', 'x'));
    const e = (await createUpdaterClient({ fetch: s.fetch }).apply('v1.1.0').catch((x: unknown) => x)) as UpdaterRequestError;
    expect(e.needsInteractiveSession).toBe(false);
  });

  it('reads Retry-After on a 503 and clamps it', async () => {
    const s = fake(problem(503, 'unavailable', 'try later', { 'Retry-After': '5' }));
    const e = (await createUpdaterClient({ fetch: s.fetch }).check().catch((x: unknown) => x)) as UpdaterRequestError;
    expect(e.status).toBe(503);
    expect(e.retryAfterMs).toBe(5000);

    const big = fake(problem(503, 'unavailable', 'x', { 'Retry-After': '999999' }));
    const e2 = (await createUpdaterClient({ fetch: big.fetch }).check().catch((x: unknown) => x)) as UpdaterRequestError;
    expect(e2.retryAfterMs).toBe(MAX_RETRY_AFTER_MS);

    const none = fake(problem(500, 'internal', 'x'));
    const e3 = (await createUpdaterClient({ fetch: none.fetch }).check().catch((x: unknown) => x)) as UpdaterRequestError;
    expect(e3.retryAfterMs).toBeNull();
  });

  it('decodes a legacy envelope and a proxy page as legacy_error', async () => {
    const legacy = fake(
      new Response(JSON.stringify({ error: 'bad gateway' }), { status: 502, headers: { 'Content-Type': 'application/json' } }),
    );
    const e = (await createUpdaterClient({ fetch: legacy.fetch }).status().catch((x: unknown) => x)) as UpdaterRequestError;
    expect(e).toBeInstanceOf(UpdaterRequestError);
    expect(e.code).toBe('legacy_error');
    expect(e.status).toBe(502);

    const html = fake(new Response('<html>502</html>', { status: 502, headers: { 'Content-Type': 'text/html' } }));
    const e2 = (await createUpdaterClient({ fetch: html.fetch }).status().catch((x: unknown) => x)) as UpdaterRequestError;
    expect(e2).toBeInstanceOf(UpdaterRequestError);
    expect(e2.status).toBe(502);
  });

  it('calls onUnauthorized for a session-ending 401 only', async () => {
    const seen: string[] = [];
    const onUnauthorized = (p: { code: string }) => seen.push(p.code);
    const a = fake(problem(401, 'authentication_required', 'sign in'));
    await expect(createUpdaterClient({ fetch: a.fetch, onUnauthorized }).status()).rejects.toBeInstanceOf(UpdaterRequestError);
    const b = fake(problem(401, 'something_else', 'x'));
    await expect(createUpdaterClient({ fetch: b.fetch, onUnauthorized }).status()).rejects.toBeInstanceOf(UpdaterRequestError);
    const c = fake(problem(403, 'authentication_required', 'x'));
    await expect(createUpdaterClient({ fetch: c.fetch, onUnauthorized }).status()).rejects.toBeInstanceOf(UpdaterRequestError);
    expect(seen).toEqual(['authentication_required']);
  });
});

describe('auth redirects', () => {
  it('raises ApiAuthRedirectError for an opaqueredirect, on every operation', async () => {
    const ops: Array<(c: ReturnType<typeof createUpdaterClient>) => Promise<unknown>> = [
      (c) => c.status(),
      (c) => c.check(),
      (c) => c.apply('v1.1.0'),
      (c) => c.rollback(),
    ];
    for (const op of ops) {
      const s = fake(opaqueRedirect());
      const err = await op(createUpdaterClient({ fetch: s.fetch })).catch((e: unknown) => e);
      expect(err).toBeInstanceOf(ApiAuthRedirectError);
      expect(isAuthRedirectError(err)).toBe(true);
      expect(s.calls).toHaveLength(1);
    }
  });

  it('raises it for a 200 text/html login page too, but not for an HTML error status', async () => {
    const page = fake(new Response('<html>login</html>', { status: 200, headers: { 'Content-Type': 'text/html; charset=utf-8' } }));
    await expect(createUpdaterClient({ fetch: page.fetch }).status()).rejects.toBeInstanceOf(ApiAuthRedirectError);
    const accepted = fake(new Response('<html>login</html>', { status: 202, headers: { 'Content-Type': 'text/html' } }));
    await expect(createUpdaterClient({ fetch: accepted.fetch }).apply('v1.1.0')).rejects.toBeInstanceOf(ApiAuthRedirectError);
  });

  it('never reads response.redirected', async () => {
    const res = jsonRes(STATUS);
    Object.defineProperty(res, 'redirected', {
      get() {
        throw new Error('redirected must not be read');
      },
    });
    const s = fake(res);
    await expect(createUpdaterClient({ fetch: s.fetch }).status()).resolves.toEqual(STATUS);
  });
});

describe('deadline and abort', () => {
  it('raises ApiTimeoutError when the server never answers', async () => {
    vi.useFakeTimers();
    const f: typeof fetch = (_u, init) =>
      new Promise((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
      });
    const p = createUpdaterClient({ fetch: f, timeoutMs: 1000 }).status().catch((e: unknown) => e);
    await vi.advanceTimersByTimeAsync(1000);
    expect(await p).toBeInstanceOf(ApiTimeoutError);
  });

  it('a caller abort stays an AbortError, not a timeout', async () => {
    const f: typeof fetch = (_u, init) =>
      new Promise((_resolve, reject) => {
        init?.signal?.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
      });
    const ctl = new AbortController();
    const p = createUpdaterClient({ fetch: f }).status({ signal: ctl.signal }).catch((e: unknown) => e);
    ctl.abort();
    const err = (await p) as Error;
    expect(err).not.toBeInstanceOf(ApiTimeoutError);
    expect(err.name).toBe('AbortError');
  });
});

describe('no secrets in logs or errors', () => {
  it('logs nothing, and an error carries neither the request body nor response headers', async () => {
    const canary = 'canary-batcave-sentinel';
    const s = fake(
      new Response(JSON.stringify({ type: 'about:blank', title: 'forbidden', status: 403, code: 'forbidden', detail: 'no' }), {
        status: 403,
        headers: {
          'Content-Type': 'application/problem+json',
          'Set-Cookie': `sid=${canary}`,
          Authorization: `Bearer ${canary}`,
        },
      }),
    );
    const err = (await createUpdaterClient({ fetch: s.fetch })
      .apply('v1.1.0')
      .catch((e: unknown) => e)) as UpdaterRequestError;
    expect(err).toBeInstanceOf(UpdaterRequestError);
    const dump = `${err.message} ${JSON.stringify(err)} ${err.stack ?? ''}`;
    expect(dump).not.toContain(canary);
    expect(dump).not.toContain('v1.1.0'); // the request body is not echoed
    for (const spy of consoleSpies) expect(spy).not.toHaveBeenCalled();
    expect(s.calls[0].headers.authorization).toBeUndefined(); // the client adds no credential header itself
  });

  it('logs nothing on success or on a redirect', async () => {
    await createUpdaterClient({ fetch: fake(jsonRes(STATUS)).fetch }).status();
    await createUpdaterClient({ fetch: fake(opaqueRedirect()).fetch }).check().catch(() => {});
    for (const spy of consoleSpies) expect(spy).not.toHaveBeenCalled();
  });
});

describe('parseRetryAfterMs', () => {
  it('reads seconds and HTTP dates, ignores junk and negatives', () => {
    const now = () => Date.parse('2026-11-20T10:00:00Z');
    expect(parseRetryAfterMs('3', now)).toBe(3000);
    expect(parseRetryAfterMs('Fri, 20 Nov 2026 10:00:07 GMT', now)).toBe(7000);
    expect(parseRetryAfterMs('Fri, 20 Nov 2026 09:00:00 GMT', now)).toBe(0);
    expect(parseRetryAfterMs('soon', now)).toBeNull();
    expect(parseRetryAfterMs('-5', now)).toBeNull();
    expect(parseRetryAfterMs('', now)).toBeNull();
    expect(parseRetryAfterMs(null, now)).toBeNull();
  });
});
