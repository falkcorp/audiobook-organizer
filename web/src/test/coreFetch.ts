// file: web/src/test/coreFetch.ts
// version: 0.1.0
// guid: 34ffe0fe-c9f6-4ed3-a71c-0401771acfb7
// last-edited: 2026-11-22

// Test helpers for pages that talk to core's native routes (/api/core/v1) through
// @mediacore/api-client: the generated client calls fetch(Request), so a stub reads the
// method, URL and body off that Request. Answers are core's bare bodies and Problem envelopes.

import { vi } from 'vitest';

export interface CoreCall {
  method: string;
  /** The path with the /api/core/v1 prefix removed, and the query string kept. */
  path: string;
  body: unknown;
}

export const coreJson = (body: unknown, status = 200) =>
  new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

export const coreProblem = (status: number, code: string, detail: string) =>
  new Response(JSON.stringify({ type: 'about:blank', title: detail, status, code, detail }), {
    status,
    headers: { 'Content-Type': 'application/problem+json' },
  });

export type CoreHandler = (call: CoreCall) => Response | undefined | Promise<Response | undefined>;

/**
 * Stubs global fetch with a router over core's routes. A handler answers `METHOD /path` (the path
 * without the prefix); a route no handler knows answers 404 so a stray call fails loudly. Returns
 * the recorded calls.
 */
export function stubCoreFetch(routes: Record<string, CoreHandler | Response | (() => Response)>) {
  const calls: CoreCall[] = [];
  const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
    const req = input instanceof Request ? input : new Request(new URL(String(input), 'http://localhost'), init);
    const url = new URL(req.url);
    const raw = req.method === 'GET' || req.method === 'HEAD' ? '' : await req.clone().text();
    let body: unknown = null;
    if (raw) {
      try {
        body = JSON.parse(raw);
      } catch {
        body = raw;
      }
    }
    const call: CoreCall = {
      method: req.method,
      path: url.pathname.replace(/^\/api\/core\/v1/, '') + url.search,
      body,
    };
    calls.push(call);
    const key = `${call.method} ${url.pathname.replace(/^\/api\/core\/v1/, '')}`;
    const h = routes[key];
    const out = typeof h === 'function' ? await h(call) : h?.clone();
    return out ?? coreProblem(404, 'not_found', `the stub has no ${key}`);
  });
  vi.stubGlobal('fetch', fetchMock);
  return { calls, fetchMock };
}
