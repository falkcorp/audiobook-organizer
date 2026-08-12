// file: web/src/utils/apiFetch.ts
// version: 1.3.0
// guid: c1d2e3f4-a5b6-7890-cdef-012345678901
// last-edited: 2026-08-11

/**
 * Thrown when a request is cut off by apiFetch's own `timeoutMs` deadline.
 *
 * Deliberately NOT a plain `AbortError`: callers routinely abort their own
 * requests on unmount or when a newer request supersedes an older one, and
 * those aborts are normal control flow that must be swallowed silently. A
 * timeout is the opposite — it means the server never answered, and the user
 * has to be told. Keeping the two distinguishable is what lets a caller write
 * `if (isAbortError(err)) return;` without also swallowing real timeouts.
 */
export class ApiTimeoutError extends Error {
  readonly url: string;
  readonly timeoutMs: number;

  constructor(url: string, timeoutMs: number) {
    super(`Request timed out after ${timeoutMs}ms: ${url}`);
    this.name = 'ApiTimeoutError';
    this.url = url;
    this.timeoutMs = timeoutMs;
  }
}

/**
 * Thrown when a request was bounced to an identity provider's login page
 * instead of reaching the API.
 *
 * Deliberately NOT swallowed, and deliberately its own type, for the same
 * reason ApiTimeoutError is: without it, an expired session is INDISTINGUISHABLE
 * FROM SUCCESS. When a Cloudflare Access session expires the origin never sees
 * the request at all — CF answers with a 302 to
 * `https://<team>.cloudflareaccess.com/cdn-cgi/access/login/...`, `fetch`
 * follows redirects by default, and the caller gets a perfectly ordinary
 * `200 OK` carrying an HTML login page. Nothing about that response looks
 * wrong. Callers that only check `response.ok` — or that do not check at all —
 * treat the login page as a successful write and tell the user their change
 * was applied. Observed live: a user clicked APPLY repeatedly on a dead
 * session, saw a success toast each time, and nothing was ever written.
 *
 * Detection cannot rely on the status code (it is 200 by the time we see it)
 * or on `response.url` alone, so two independent signals are used; see
 * assertNotAuthRedirect.
 *
 * Do NOT try to refresh the session or drive the login flow from here. Detect,
 * surface, stop — the user has to re-authenticate in a real browser context.
 */
export class ApiAuthRedirectError extends Error {
  readonly url: string;
  /** Where the request actually ended up (the login page, typically). */
  readonly finalUrl: string;

  constructor(url: string, finalUrl: string) {
    super(`Request was redirected to a login page (session expired): ${url}`);
    this.name = 'ApiAuthRedirectError';
    this.url = url;
    this.finalUrl = finalUrl;
  }
}

/** True for an abort the caller asked for (unmount, supersede), not a timeout. */
export function isAbortError(err: unknown): boolean {
  return (err as { name?: string } | null | undefined)?.name === 'AbortError';
}

/** True when the error is an auth bounce to a login page. */
export function isAuthRedirectError(err: unknown): boolean {
  return (err as { name?: string } | null | undefined)?.name === 'ApiAuthRedirectError';
}

/**
 * Throws {@link ApiAuthRedirectError} if the response is a login page rather
 * than an API response.
 *
 * Two signals, because each alone has a gap:
 *
 *  1. `response.redirected` + a cross-origin final URL. Catches the Cloudflare
 *     Access bounce exactly. Misses same-origin login redirects, and misses
 *     opaque/`redirect: 'manual'` responses where `redirected` is not set.
 *  2. An HTML content-type on an `/api/` route. Catches everything signal 1
 *     misses, including a same-origin SSO page. `text/html` on an API route is
 *     never legitimate — every endpoint under /api/ answers JSON (or an empty
 *     body), so there is no correct response this can misfire on.
 */
function assertNotAuthRedirect(requestUrl: string, response: Response): void {
  let requestOrigin = '';
  let finalOrigin = '';
  try {
    const base = typeof window !== 'undefined' ? window.location.href : undefined;
    requestOrigin = new URL(requestUrl, base).origin;
    finalOrigin = response.url ? new URL(response.url, base).origin : requestOrigin;
  } catch {
    // Unparseable URL — fall through to the content-type signal.
  }

  if (response.redirected && finalOrigin && requestOrigin && finalOrigin !== requestOrigin) {
    throw new ApiAuthRedirectError(requestUrl, response.url);
  }

  const contentType = response.headers.get('Content-Type') ?? '';
  if (contentType.toLowerCase().includes('text/html') && requestUrl.includes('/api/')) {
    throw new ApiAuthRedirectError(requestUrl, response.url || requestUrl);
  }
}

export interface ApiFetchOptions extends RequestInit {
  /**
   * Abort the request after this many milliseconds and reject with
   * {@link ApiTimeoutError}.
   *
   * Omitted (or <= 0) means NO timeout, which is the historical behaviour and
   * remains the default on purpose: several endpoints — `/activity/compact`,
   * scans, transcodes — legitimately run for minutes, and a blanket default
   * here would break them. Callers that know their endpoint should be fast opt
   * in explicitly.
   */
  timeoutMs?: number;
}

/**
 * apiFetch is a thin wrapper around the browser Fetch API that standardises
 * the options every service call needs:
 *
 *   - credentials: 'include'  — sends session cookies on every request
 *   - Content-Type: application/json — added automatically for non-GET
 *     requests that have a body (unless the caller sets it explicitly)
 *   - timeoutMs — optional deadline, enforced with an internal AbortController
 *
 * A caller-supplied `options.signal` is still honoured: it is chained into the
 * internal controller, so either the caller or the deadline can cut the
 * request off.
 *
 * Usage:
 *   const response = await apiFetch('/api/v1/audiobooks');
 *   const response = await apiFetch('/api/v1/audiobooks/123', {
 *     method: 'PUT',
 *     body: JSON.stringify(payload),   // Content-Type set automatically
 *     signal: controller.signal,
 *     timeoutMs: 15000,
 *   });
 */
export async function apiFetch(url: string, options: ApiFetchOptions = {}): Promise<Response> {
  const { timeoutMs, signal: callerSignal, ...rest } = options;
  const method = (rest.method ?? 'GET').toUpperCase();
  const headers = new Headers(rest.headers);

  if (
    rest.body !== undefined &&
    method !== 'GET' &&
    method !== 'HEAD' &&
    !headers.has('Content-Type')
  ) {
    headers.set('Content-Type', 'application/json');
  }

  if (!timeoutMs || timeoutMs <= 0) {
    const response = await fetch(url, {
      credentials: 'include',
      ...rest,
      headers,
      signal: callerSignal,
    });
    assertNotAuthRedirect(url, response);
    return response;
  }

  const controller = new AbortController();
  let timedOut = false;
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, timeoutMs);

  // Flattened deliberately. The listener IS removed, in the finally below —
  // but scripts/check-memory-leaks.py looks ahead from the addEventListener
  // line and gives up once scope_depth drops below -1, so with this nested one
  // level deeper the two closing braces ended the search before it could reach
  // the cleanup, and it reported a leak that does not exist. Optional chaining
  // makes the nested null-check redundant anyway: a nil callerSignal is falsy
  // here and the addEventListener below is then a no-op, which is exactly what
  // the nested form did.
  const forwardAbort = () => controller.abort();
  if (callerSignal?.aborted) {
    controller.abort();
  } else {
    callerSignal?.addEventListener('abort', forwardAbort);
  }

  try {
    const response = await fetch(url, {
      credentials: 'include',
      ...rest,
      headers,
      signal: controller.signal,
    });
    assertNotAuthRedirect(url, response);
    return response;
  } catch (err) {
    // Distinguish "we gave up" from "the caller gave up". Both surface as an
    // AbortError out of fetch(); only the former is a failure to report.
    if (timedOut) {
      throw new ApiTimeoutError(url, timeoutMs);
    }
    throw err;
  } finally {
    clearTimeout(timer);
    callerSignal?.removeEventListener('abort', forwardAbort);
  }
}
