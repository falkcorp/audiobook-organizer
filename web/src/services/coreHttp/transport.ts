// file: web/src/services/coreHttp/transport.ts
// version: 0.1.0
// guid: 3b0d6a52-7a1e-4d64-9c0b-5f2c8a9e41d7
// last-edited: 2026-11-20

// The raw transport shared by the core clients that cannot use createCoreClient
// (coreUpdater, coreBackups): one request over a raw fetch with the rules every one of
// them needs and none may drift on.
//
// UNWIRED: nothing in the running app imports this until the F8-04a/b flips.
//
// Rules this file enforces, each with a test in transport.test.ts:
//  - redirect: 'manual', as coreEvents does. An opaqueredirect response is an SSO bounce
//    and is raised as the API client's ApiAuthRedirectError; so is a 2xx text/html page.
//    response.redirected is never read.
//  - A non-2xx answer is decoded with the API client's decodeProblem and handed to the
//    caller's error factory as a Refusal. A legacy or proxy body is blanked from `detail`:
//    decodeProblem puts its first 200 characters in `title`, which could be anything a
//    proxy printed.
//  - A deadline of 0 or less means none, as createCoreClient's runtimeFetch has it.
//    "We gave up" is an ApiTimeoutError; "the caller gave up" stays the caller's AbortError.
//  - Retry-After is clamped to MAX_RETRY_AFTER_MS.
//  - Errors name the endpoint, never the query (an import's ?name= is the user's file name).
//  - Nothing is logged.

import {
  ApiAuthRedirectError,
  ApiTimeoutError,
  LEGACY_PROBLEM_CODE,
  SESSION_ENDING_CODES,
  decodeProblem,
  type Problem,
} from '@mediacore/api-client';

/** A request that gets no answer in this long is abandoned with ApiTimeoutError; <= 0 disables the deadline. */
export const DEFAULT_TIMEOUT_MS = 30000;

/**
 * Retry-After above this is clamped: a hostile or broken header must not park the UI.
 * Ten minutes is coreEvents' own ceiling (coreEvents/backoff.ts MAX_RETRY_AFTER_MS), so a
 * 503 from the updater or the backups route never asks the page to wait longer than the
 * event stream would.
 */
export const MAX_RETRY_AFTER_MS = 10 * 60 * 1000;

/** Retry-After as delta-seconds or an HTTP date, in ms; null when absent or unusable. */
export function parseRetryAfterMs(
  value: string | null | undefined,
  now: () => number = Date.now
): number | null {
  if (value == null) return null;
  const v = value.trim();
  if (v === '') return null;
  let ms: number;
  if (/^\d+$/.test(v)) {
    ms = Number(v) * 1000;
  } else {
    // An HTTP date starts with a day name; Date.parse would read "-5" as a year.
    if (!/^[A-Za-z]/.test(v)) return null;
    const at = Date.parse(v);
    if (Number.isNaN(at)) return null;
    ms = at - now();
  }
  return Math.min(Math.max(ms, 0), MAX_RETRY_AFTER_MS);
}

/** A non-2xx answer, decoded; the caller's error factory turns it into its own error type. */
export interface Refusal {
  status: number;
  code: string;
  /** Empty for a legacy or proxy body. */
  detail: string;
  requestId: string;
  retryAfterMs: number | null;
  requiredPermission: string | undefined;
  fields: NonNullable<Problem['errors']>;
}

export interface TransportOptions {
  baseUrl?: string;
  /** Default deadline of every request; a request may override it with deadlineMs. */
  timeoutMs?: number;
  fetch?: typeof fetch;
  /**
   * Called for a 401 whose code ends the session (SESSION_ENDING_CODES), before the
   * error is thrown, as createCoreClient's onUnauthorized is.
   */
  onUnauthorized?: (problem: Problem) => void;
}

export interface RawRequest {
  method: 'GET' | 'POST' | 'DELETE';
  /** Path with an optional query; the query is sent but never put in an error. */
  path: string;
  json?: unknown;
  raw?: Blob;
  /** Overrides the transport's deadline for this request; <= 0 means none. */
  deadlineMs?: number;
  signal?: AbortSignal;
}

export interface Transport {
  readonly baseUrl: string;
  /** One request; returns an ok, non-HTML response. Callers check status, content type and body. */
  send(op: string, r: RawRequest): Promise<Response>;
}

export function createTransport(
  opts: TransportOptions,
  refusalError: (op: string, refusal: Refusal) => Error
): Transport {
  // Raw fetch on purpose, not apiFetch or createCoreClient: both follow redirects, and the
  // bounce must be seen as an opaqueredirect (05-realtime 4.8 item 4). It is handled in
  // send() below (opaqueredirect and a 2xx text/html both raise ApiAuthRedirectError),
  // which is what the lint rule exists to guarantee.
  // eslint-disable-next-line no-restricted-properties
  const doFetch: typeof fetch = opts.fetch ?? ((...a) => globalThis.fetch(...a));
  const baseUrl = opts.baseUrl ?? '';
  const timeoutMs = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;

  async function send(op: string, r: RawRequest): Promise<Response> {
    const url = `${baseUrl}${r.path}`;
    const errUrl = `${baseUrl}${r.path.split('?')[0]}`;
    const headers: Record<string, string> = { Accept: 'application/json' };
    const init: RequestInit = {
      method: r.method,
      headers,
      credentials: 'include',
      cache: 'no-store',
      redirect: 'manual',
    };
    if (r.json !== undefined) {
      headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(r.json);
    } else if (r.raw !== undefined) {
      headers['Content-Type'] = 'application/octet-stream';
      init.body = r.raw;
    }

    const deadline = r.deadlineMs ?? timeoutMs;
    const ctl = new AbortController();
    let timedOut = false;
    const timer =
      deadline > 0
        ? setTimeout(() => {
            timedOut = true;
            ctl.abort();
          }, deadline)
        : undefined;
    const forward = () => ctl.abort();
    if (r.signal?.aborted) ctl.abort();
    else r.signal?.addEventListener('abort', forward);
    init.signal = ctl.signal;

    let res: Response;
    try {
      res = await doFetch(url, init);
    } catch (err) {
      if (timedOut) throw new ApiTimeoutError(errUrl, deadline);
      throw err;
    } finally {
      if (timer !== undefined) clearTimeout(timer);
      r.signal?.removeEventListener('abort', forward);
    }

    // SSO bounce: with redirect 'manual' the browser hands back an opaque response.
    if (res.type === 'opaqueredirect') throw new ApiAuthRedirectError(errUrl, errUrl);

    if (!res.ok) {
      const problem = await decodeProblem(res);
      if (res.status === 401 && SESSION_ENDING_CODES.has(problem.code))
        opts.onUnauthorized?.(problem);
      throw refusalError(op, {
        status: res.status,
        code: problem.code,
        detail: problem.code === LEGACY_PROBLEM_CODE ? '' : (problem.detail ?? problem.title ?? ''),
        requestId: problem.request_id ?? '',
        retryAfterMs: parseRetryAfterMs(res.headers?.get?.('Retry-After')),
        requiredPermission: problem.required_permission,
        fields: problem.errors ?? [],
      });
    }

    // A same-origin SSO page answers 200 text/html: the same bounce, not an answer.
    if ((res.headers?.get?.('Content-Type') ?? '').toLowerCase().includes('text/html')) {
      throw new ApiAuthRedirectError(errUrl, errUrl);
    }
    return res;
  }

  return { baseUrl, send };
}
