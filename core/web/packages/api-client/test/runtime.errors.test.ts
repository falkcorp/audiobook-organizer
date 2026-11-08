// file: core/web/packages/api-client/test/runtime.errors.test.ts
// version: 1.0.0
// guid: f6a2af79-c752-4ffb-bdf4-8bc8b1bbb61a
// last-edited: 2026-11-08

// The runtime's error handling (07 §7.18): envelope decoding, the request
// deadline, login-page bounce detection, and `skipAuthRedirect` taken from
// `x-mediacore-permission: public`.

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import {
  ApiAuthRedirectError,
  ApiTimeoutError,
  createCoreClient,
  createModuleClient,
  decodeProblem,
  isAbortError,
  isAuthRedirectError,
  LEGACY_PROBLEM_CODE,
  type OperationInfo,
  type Problem,
} from '../src/index';
import { operationsOf } from '../generator/generate';
import fixture from './fixtures/batcave.json';

const BASE = 'http://batcave.test';
const PROBLEM = 'application/problem+json';

// The module's own paths, as a generated `paths` would declare them for the fixture.
interface BatcavePaths {
  '/api/core/v1/batcave/login': { post: { responses: { 204: { content: never } } } };
  '/api/core/v1/batcave/gadgets/{gadgetId}': {
    get: { parameters: { path: { gadgetId: string } }; responses: { 200: { content: never } } };
    delete: { parameters: { path: { gadgetId: string } }; responses: { 204: { content: never } } };
  };
  '/api/core/v1/batcave/signal': { get: { responses: { 200: { content: never } } } };
}

const operations: OperationInfo[] = operationsOf(fixture as Parameters<typeof operationsOf>[0]);

function problemBody(over: Partial<Problem> = {}): string {
  return JSON.stringify({
    type: 'https://mediacore.dev/problems/not_found',
    title: 'Not Found',
    status: 404,
    code: 'not_found',
    request_id: 'req-alfred-1',
    ...over,
  });
}

function reply(body: string | null, status: number, headers: Record<string, string> = {}): Response {
  return new Response(status === 204 ? null : body, { status, headers });
}

afterEach(() => {
  vi.useRealTimers();
});

describe('decodeProblem', () => {
  it('reads a problem+json answer, with every member', async () => {
    const problem = await decodeProblem(
      reply(
        problemBody({
          status: 403,
          code: 'permission_denied',
          detail: 'no gadgets for you',
          instance: '/api/core/v1/batcave/gadgets/7',
          required_permission: 'batcave.gadgets.write',
          errors: [{ field: '/path/gadgetId', code: 'invalid', message: 'not a gadget' }],
        }),
        403,
        { 'Content-Type': PROBLEM },
      ),
    );
    expect(problem).toEqual({
      type: 'https://mediacore.dev/problems/not_found',
      title: 'Not Found',
      status: 403,
      code: 'permission_denied',
      request_id: 'req-alfred-1',
      detail: 'no gadgets for you',
      instance: '/api/core/v1/batcave/gadgets/7',
      required_permission: 'batcave.gadgets.write',
      errors: [{ field: '/path/gadgetId', code: 'invalid', message: 'not a gadget' }],
    });
  });

  it('takes request_id from X-Request-ID when the body has none', async () => {
    const res = reply(JSON.stringify({ title: 'Gone', code: 'gone', status: 410 }), 410, {
      'Content-Type': PROBLEM,
      'X-Request-ID': 'req-robin-2',
    });
    expect((await decodeProblem(res)).request_id).toBe('req-robin-2');
  });

  it('decodes the legacy envelope: error is the title, message the detail', async () => {
    const res = reply(JSON.stringify({ error: 'gone', message: 'the Batmobile moved' }), 410, {
      'Content-Type': 'application/json',
      'X-Request-ID': 'req-robin-3',
    });
    expect(await decodeProblem(res)).toEqual({
      type: 'about:blank',
      title: 'gone',
      status: 410,
      code: LEGACY_PROBLEM_CODE,
      request_id: 'req-robin-3',
      detail: 'the Batmobile moved',
    });
  });

  it('decodes a legacy envelope with only an error', async () => {
    const res = reply(JSON.stringify({ error: 'store not ready' }), 503, { 'Content-Type': 'application/json' });
    const problem = await decodeProblem(res);
    expect(problem.title).toBe('store not ready');
    expect(problem.detail).toBeUndefined();
    expect(problem.status).toBe(503);
  });

  it("decodes a proxy's HTML page and an empty body without throwing", async () => {
    const html = await decodeProblem(reply('<html>Bad Gateway</html>', 502, { 'Content-Type': 'text/html' }));
    expect(html).toMatchObject({ title: '<html>Bad Gateway</html>', status: 502, code: LEGACY_PROBLEM_CODE });
    const empty = await decodeProblem(new Response('', { status: 500, statusText: 'Internal Server Error' }));
    expect(empty).toMatchObject({ title: 'Internal Server Error', status: 500, code: LEGACY_PROBLEM_CODE });
  });
});

describe('the client hands every error back as a Problem', () => {
  function clientFor(res: () => Response) {
    return createModuleClient<BatcavePaths>(
      createCoreClient({ buildId: 'b-1', shell: false, baseUrl: BASE, fetch: async () => res() }),
      { operations },
    );
  }

  it('passes a native problem through', async () => {
    const client = clientFor(() => reply(problemBody(), 404, { 'Content-Type': PROBLEM }));
    const { data, error, response } = await client.GET('/api/core/v1/batcave/signal');
    expect(data).toBeUndefined();
    expect(response.status).toBe(404);
    expect(error).toMatchObject({ code: 'not_found', request_id: 'req-alfred-1' });
  });

  it('rewrites a legacy envelope into one', async () => {
    const client = clientFor(() =>
      reply(JSON.stringify({ error: 'no such gadget' }), 404, { 'Content-Type': 'application/json', 'X-Request-ID': 'req-4' }),
    );
    const { error, response } = await client.GET('/api/core/v1/batcave/signal');
    expect(response.headers.get('Content-Type')).toBe(PROBLEM);
    expect(error).toMatchObject({ title: 'no such gadget', code: LEGACY_PROBLEM_CODE, status: 404, request_id: 'req-4' });
  });

  it('sends credentials: include', async () => {
    const seen: Request[] = [];
    const client = createModuleClient<BatcavePaths>(
      createCoreClient({
        buildId: 'b-1',
        shell: false,
        baseUrl: BASE,
        fetch: async (input) => {
          seen.push(input as Request);
          return reply(null, 204);
        },
      }),
    );
    await client.POST('/api/core/v1/batcave/login');
    expect(seen[0].credentials).toBe('include');
  });
});

describe('the request deadline', () => {
  // A fetch that never answers; it rejects with AbortError when its request is aborted.
  const hang = (): typeof fetch => (input) =>
    new Promise((_resolve, reject) => {
      const signal = (input as Request).signal;
      signal.addEventListener('abort', () => reject(new DOMException('aborted', 'AbortError')));
    });

  it('rejects with ApiTimeoutError once timeoutMs passes', async () => {
    vi.useFakeTimers();
    const client = createModuleClient<BatcavePaths>(
      createCoreClient({ buildId: 'b-1', shell: false, baseUrl: BASE, timeoutMs: 500, fetch: hang() }),
    );
    const call = client.GET('/api/core/v1/batcave/signal');
    const settled = call.catch((e: unknown) => e);
    await vi.advanceTimersByTimeAsync(501);
    const err = await settled;
    expect(err).toBeInstanceOf(ApiTimeoutError);
    expect((err as ApiTimeoutError).timeoutMs).toBe(500);
    expect(isAbortError(err)).toBe(false);
  });

  it("is not a timeout when the caller aborts: that stays an AbortError", async () => {
    const client = createModuleClient<BatcavePaths>(
      createCoreClient({ buildId: 'b-1', shell: false, baseUrl: BASE, timeoutMs: 60_000, fetch: hang() }),
    );
    const controller = new AbortController();
    const call = client.GET('/api/core/v1/batcave/signal', { signal: controller.signal });
    const settled = call.catch((e: unknown) => e);
    controller.abort();
    const err = await settled;
    expect(isAbortError(err)).toBe(true);
    expect(err).not.toBeInstanceOf(ApiTimeoutError);
  });

  it('has no deadline by default', async () => {
    const client = createModuleClient<BatcavePaths>(
      createCoreClient({ buildId: 'b-1', shell: false, baseUrl: BASE, fetch: async () => reply(null, 204) }),
    );
    const { response } = await client.POST('/api/core/v1/batcave/login');
    expect(response.status).toBe(204);
  });
});

describe('login-page bounce detection', () => {
  function bouncing(res: Response) {
    return createModuleClient<BatcavePaths>(
      createCoreClient({ buildId: 'b-1', shell: false, baseUrl: BASE, fetch: async () => res }),
    );
  }

  it('a cross-origin redirect is an ApiAuthRedirectError', async () => {
    const res = reply('{}', 200, { 'Content-Type': 'application/json' });
    Object.defineProperty(res, 'redirected', { value: true });
    Object.defineProperty(res, 'url', { value: 'https://wayne.cloudflareaccess.com/cdn-cgi/access/login/batcave' });
    const err = await bouncing(res).GET('/api/core/v1/batcave/signal').catch((e: unknown) => e);
    expect(isAuthRedirectError(err)).toBe(true);
    expect(err).toBeInstanceOf(ApiAuthRedirectError);
    expect((err as ApiAuthRedirectError).finalUrl).toContain('cloudflareaccess.com');
  });

  it('a 200 text/html answer on an /api/ route is one too (a same-origin SSO page)', async () => {
    const res = reply('<html>sign in</html>', 200, { 'Content-Type': 'text/html; charset=utf-8' });
    const err = await bouncing(res).GET('/api/core/v1/batcave/signal').catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiAuthRedirectError);
  });

  it("an HTML 502 is a server error, not 'session expired'", async () => {
    const res = reply('<html>Bad Gateway</html>', 502, { 'Content-Type': 'text/html' });
    const { error, response } = await bouncing(res).GET('/api/core/v1/batcave/signal');
    expect(response.status).toBe(502);
    expect(error).toMatchObject({ status: 502, code: LEGACY_PROBLEM_CODE });
  });
});

describe('skipAuthRedirect comes from x-mediacore-permission: public', () => {
  it('marks exactly the public operations of the table', () => {
    const core = createCoreClient({ buildId: 'b-1', shell: false, baseUrl: BASE, operations });
    expect(core.runtime.skipAuthRedirect('POST', '/api/core/v1/batcave/login')).toBe(true);
    expect(core.runtime.skipAuthRedirect('get', '/api/core/v1/batcave/gadgets/{gadgetId}')).toBe(false);
    expect(core.runtime.skipAuthRedirect('get', '/api/core/v1/batcave/signal')).toBe(false);
    expect(core.runtime.skipAuthRedirect('post', '/api/core/v1/batcave/unknown')).toBe(false);
    // `authenticated` is a permission scope, not `public`.
    expect(operations.find((o) => o.path.endsWith('/signal'))?.public).toBe(false);
  });

  function session(status: number, code: string, onUnauthorized: (p: Problem) => void) {
    const core = createCoreClient({
      buildId: 'b-1',
      shell: false,
      baseUrl: BASE,
      operations,
      onUnauthorized,
      fetch: async () => reply(problemBody({ status, code }), status, { 'Content-Type': PROBLEM }),
    });
    return createModuleClient<BatcavePaths>(core, { operations });
  }

  it('a session-ending 401 on a non-public call reaches onUnauthorized', async () => {
    const onUnauthorized = vi.fn();
    const client = session(401, 'expired', onUnauthorized);
    await client.GET('/api/core/v1/batcave/gadgets/{gadgetId}', { params: { path: { gadgetId: '7' } } });
    expect(onUnauthorized).toHaveBeenCalledTimes(1);
    expect(onUnauthorized.mock.calls[0][0]).toMatchObject({ code: 'expired', status: 401 });
  });

  it('the same 401 on a public call shows its error in place (a wrong password)', async () => {
    const onUnauthorized = vi.fn();
    const client = session(401, 'invalid_credential', onUnauthorized);
    const { error } = await client.POST('/api/core/v1/batcave/login');
    expect(onUnauthorized).not.toHaveBeenCalled();
    expect(error).toMatchObject({ code: 'invalid_credential' });
  });

  it.each([
    [401, 'something_else'],
    [403, 'permission_denied'],
    [503, 'auth_unavailable'],
  ])('a %i %s never ends the session', async (status, code) => {
    const onUnauthorized = vi.fn();
    const client = session(status, code, onUnauthorized);
    await client.GET('/api/core/v1/batcave/signal');
    expect(onUnauthorized).not.toHaveBeenCalled();
  });
});

describe('createModuleClient inherits the core client', () => {
  beforeEach(() => {
    vi.resetModules();
  });

  it('shares buildId, shell and the stale-build signal', async () => {
    const { createCoreClient: coreOf, createModuleClient: moduleOf, onStaleBuild } = await import('../src/index');
    const core = coreOf({
      buildId: 'old-1',
      shell: true,
      baseUrl: BASE,
      fetch: async () => reply('{}', 200, { 'X-App-Build': 'new-2' }),
    });
    const mod = moduleOf<BatcavePaths>(core, { operations });
    expect(mod.runtime).toMatchObject({ buildId: 'old-1', shell: true, baseUrl: BASE });
    const heard = vi.fn();
    onStaleBuild(heard);
    await mod.GET('/api/core/v1/batcave/signal');
    expect(heard).toHaveBeenCalledWith('header-mismatch');
  });
});
