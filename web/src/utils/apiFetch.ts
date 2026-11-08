// file: web/src/utils/apiFetch.ts
// version: 1.5.0
// guid: c1d2e3f4-a5b6-7890-cdef-012345678901
// last-edited: 2026-11-08

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
 *  2. An HTML content-type on an `/api/` route with a non-error status (<400). Catches everything signal 1
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

// ---------------------------------------------------------------------------
// The stale-build rule (07 §7.5.2; 09 §9.4 R1 "build identity").
//
// The server stamps every response with X-App-Build, the UI build ID of the
// bundle it embeds, which `vite build` wrote into this page's
// <meta name="mediacore-build"> (web/build-id.ts). A tab left open across an
// upgrade, or across a rollback to a server that predates the header, holds a
// bundle whose lazy chunks may be gone, so it is offered a reload. This is
// the one place the rule runs for ABO's own calls; @mediacore/api-client's
// checkBuild takes it over (R1-30) with the same signature shape.
// ---------------------------------------------------------------------------

/**
 * Why a response says this tab's bundle is stale:
 *  - `header-mismatch`: X-App-Build names a different bundle than this page's.
 *  - `pre-r1-server`: the response carries `Reporting-Endpoints: csp=...`, so
 *    it is the app's, but no X-App-Build, so the server predates the header
 *    (an R1+ tab after a rollback to R0).
 */
export type StaleReason = 'header-mismatch' | 'pre-r1-server';

const BUILD_META_SELECTOR = 'meta[name="mediacore-build"]';

/**
 * The only reader of this page's build ID: the content of
 * `<meta name="mediacore-build">`, "" when the tag is absent. Read once, at
 * load (below); a document without a tag reads as "".
 */
export function readPageBuildId(): string {
  if (typeof document === 'undefined') return '';
  return document.querySelector(BUILD_META_SELECTOR)?.getAttribute('content')?.trim() ?? '';
}

/** The page's build ID, read once at load. "" and "dev" never trigger the rule. */
const pageBuildId = readPageBuildId();

/** An ID that carries no identity: an absent tag, or `vite serve`'s `dev`. */
function isUnknownBuildId(id: string): boolean {
  return id === '' || id.toLowerCase() === 'dev';
}

/**
 * checkBuild applies the rule to one response, headers only, and returns the
 * reason it is stale or null. It never reads the body. A response double with
 * no `headers` (several suites stub fetch that way) is never stale.
 *
 * `buildId` is a parameter, not a read of the meta, so the rule can be tested
 * with any page ID; apiFetch passes the page's. A `buildId` of "" or "dev"
 * never triggers it, and neither does a header of "" or "dev" (stage 5's
 * "unknown", never a mismatch).
 */
export function checkBuild(response: Response, opts: { buildId: string }): StaleReason | null {
  if (isUnknownBuildId(opts.buildId)) return null;
  if (typeof response.headers?.get !== 'function') return null;

  const header = (response.headers.get('X-App-Build') ?? '').trim();
  let reason: StaleReason | null = null;
  if (header !== '') {
    if (!isUnknownBuildId(header) && header !== opts.buildId) reason = 'header-mismatch';
  } else if (
    (response.status ?? 200) < 500 &&
    /(^|[\s,])csp\s*=/i.test(response.headers.get('Reporting-Endpoints') ?? '')
  ) {
    // The app's own answer (HEAD and every build since write the header) with
    // no X-App-Build. A proxy's 502 or an Access interstitial has neither
    // header and never counts.
    reason = 'pre-r1-server';
  }
  if (reason) noteStale(reason);
  return reason;
}

const detectedReasons: StaleReason[] = [];
const staleListeners = new Set<(reason: StaleReason) => void>();

function noteStale(reason: StaleReason): void {
  if (detectedReasons.includes(reason)) return;
  detectedReasons.push(reason);
  for (const listener of [...staleListeners]) listener(reason);
}

/** The reasons detected on this page load, each once. */
export function getStaleReasons(): readonly StaleReason[] {
  return detectedReasons;
}

/**
 * Subscribe to stale-build detections. The listener is called at once for each
 * reason already detected (a banner that mounts after the first API response,
 * the auth probe, still hears it) and then once per new reason. Returns the
 * unsubscribe function.
 */
export function onStaleBuild(listener: (reason: StaleReason) => void): () => void {
  staleListeners.add(listener);
  for (const reason of [...detectedReasons]) listener(reason);
  return () => {
    staleListeners.delete(listener);
  };
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
    checkBuild(response, { buildId: pageBuildId });
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
    checkBuild(response, { buildId: pageBuildId });
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
