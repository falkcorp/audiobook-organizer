// file: web/src/utils/apiFetch.ts
// version: 1.1.0
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

/** True for an abort the caller asked for (unmount, supersede), not a timeout. */
export function isAbortError(err: unknown): boolean {
  return (err as { name?: string } | null | undefined)?.name === 'AbortError';
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
    return fetch(url, {
      credentials: 'include',
      ...rest,
      headers,
      signal: callerSignal,
    });
  }

  const controller = new AbortController();
  let timedOut = false;
  const timer = setTimeout(() => {
    timedOut = true;
    controller.abort();
  }, timeoutMs);

  const forwardAbort = () => controller.abort();
  if (callerSignal) {
    if (callerSignal.aborted) {
      controller.abort();
    } else {
      callerSignal.addEventListener('abort', forwardAbort);
    }
  }

  try {
    return await fetch(url, {
      credentials: 'include',
      ...rest,
      headers,
      signal: controller.signal,
    });
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
