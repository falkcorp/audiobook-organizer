// file: web/src/utils/apiFetch.test.ts
// version: 1.0.0
// guid: 2f4a6c18-9b73-4d51-8e0c-7a5b1d3e9f02
// last-edited: 2026-08-11

// Guards the auth-redirect detection in apiFetch.
//
// The failure this prevents: when a Cloudflare Access session expires the API
// call is answered with a 302 to the CF login page, fetch follows it, and the
// caller receives a 200 carrying HTML. Every caller that checks only
// `response.ok` then treats a login page as a successful write. These tests
// assert on the thrown error type, because "returned something" was exactly
// what made the bug invisible.

import { describe, expect, it, vi, afterEach } from 'vitest';
import { apiFetch, ApiAuthRedirectError, isAuthRedirectError } from './apiFetch';

const originalFetch = global.fetch;

afterEach(() => {
  global.fetch = originalFetch;
  vi.restoreAllMocks();
});

/** Builds a Response with a controllable `url` and `redirected`. */
function makeResponse(opts: {
  url: string;
  redirected: boolean;
  contentType: string;
}): Response {
  const res = new Response('{}', {
    status: 200,
    headers: { 'Content-Type': opts.contentType },
  });
  Object.defineProperty(res, 'url', { value: opts.url });
  Object.defineProperty(res, 'redirected', { value: opts.redirected });
  return res;
}

describe('apiFetch auth-redirect detection', () => {
  it('throws when the response was redirected to a different origin', async () => {
    global.fetch = vi.fn().mockResolvedValue(
      makeResponse({
        url: 'https://jdfalk.cloudflareaccess.com/cdn-cgi/access/login/books.jdfalk.com',
        redirected: true,
        contentType: 'text/html; charset=UTF-8',
      })
    );

    await expect(
      apiFetch('https://books.jdfalk.com/api/v1/audiobooks/metadata/batch-apply-cached', {
        method: 'POST',
        body: '{}',
      })
    ).rejects.toBeInstanceOf(ApiAuthRedirectError);
  });

  it('throws on an HTML content-type for an /api/ route even without a redirect flag', async () => {
    // Covers the gap in the redirect signal: a same-origin SSO page, or a
    // response whose `redirected` flag never got set.
    global.fetch = vi.fn().mockResolvedValue(
      makeResponse({
        url: 'https://books.jdfalk.com/api/v1/audiobooks',
        redirected: false,
        contentType: 'text/html',
      })
    );

    const err = await apiFetch('https://books.jdfalk.com/api/v1/audiobooks').catch((e) => e);
    expect(isAuthRedirectError(err)).toBe(true);
  });

  it('passes a normal JSON API response through untouched', async () => {
    const ok = makeResponse({
      url: 'https://books.jdfalk.com/api/v1/audiobooks',
      redirected: false,
      contentType: 'application/json',
    });
    global.fetch = vi.fn().mockResolvedValue(ok);

    const response = await apiFetch('https://books.jdfalk.com/api/v1/audiobooks');
    expect(response).toBe(ok);
    expect(response.status).toBe(200);
  });

  it('does not misfire on a same-origin redirect to a JSON response', async () => {
    global.fetch = vi.fn().mockResolvedValue(
      makeResponse({
        url: 'https://books.jdfalk.com/api/v1/audiobooks/123',
        redirected: true,
        contentType: 'application/json',
      })
    );

    await expect(
      apiFetch('https://books.jdfalk.com/api/v1/audiobooks/123')
    ).resolves.toBeDefined();
  });
});
