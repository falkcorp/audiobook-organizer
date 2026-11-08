// file: core/web/packages/api-client/src/errors.ts
// version: 1.0.0
// guid: 6bc2a9a6-6858-47c2-a18b-4646b8299df0
// last-edited: 2026-11-08

// The transport errors of the fetch runtime, carried over from ABO's
// web/src/utils/apiFetch.ts (07 §7.8): the request deadline and the SSO login-page
// bounce. The text is ABO's, moved, so the one implementation serves the legacy
// `apiFetch` (which re-exports these) and the generated client.

/**
 * Thrown when a request is cut off by the runtime's own `timeoutMs` deadline
 * (apiFetch's or the generated client's).
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
 * than an API response. Both transports call it: ABO's `apiFetch` for legacy
 * calls and this package's client for native ones.
 *
 * Two signals, because each alone has a gap:
 *
 *  1. `response.redirected` + a cross-origin final URL. Catches the Cloudflare
 *     Access bounce exactly. Misses same-origin login redirects, and misses
 *     opaque/`redirect: 'manual'` responses where `redirected` is not set.
 *  2. An HTML content-type on an `/api/` route with a non-error status (<400). Catches everything signal 1
 *     misses, including a same-origin SSO page. `text/html` on an API route is
 *     never legitimate — every endpoint under /api/ answers JSON (or an empty
 *     body), so there is no correct response this can misfire on.
 */
export function assertNotAuthRedirect(requestUrl: string, response: Response): void {
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

  // Optional chaining is load-bearing, not defensive noise: a real Response
  // always has `headers`, but test doubles routinely do not (several suites
  // stub fetch with a bare `{ ok, status, json }` object). Without the `?.`
  // this throws a TypeError and turns a passing suite red for reasons that
  // have nothing to do with auth.
  const contentType = response.headers?.get('Content-Type') ?? '';
  // Only a successful-looking response can be a login bounce: the bounce
  // arrives as a 200 after fetch follows the redirect. An HTML 4xx/5xx on an
  // API route (a proxy's 502 page, say) is a real server error and must reach
  // the caller as one, not be mislabelled "session expired".
  if (
    (response.status ?? 200) < 400 &&
    contentType.toLowerCase().includes('text/html') &&
    requestUrl.includes('/api/')
  ) {
    throw new ApiAuthRedirectError(requestUrl, response.url || requestUrl);
  }
}
