// file: web/src/utils/apiFetch.ts
// version: 1.5.1
// guid: c1d2e3f4-a5b6-7890-cdef-012345678901
// last-edited: 2026-11-08

// The transport errors, the login-page bounce check and the stale-build rule live
// in @mediacore/api-client's runtime (07 §7.5.2, §7.8), moved there from this file
// so the legacy `apiFetch` and the generated client run ONE implementation while
// the two transports coexist. They are re-exported here under their old names, so
// no importer changes.
import {
  ApiAuthRedirectError,
  ApiTimeoutError,
  assertNotAuthRedirect,
  checkBuild,
  getStaleReasons,
  isAbortError,
  isAuthRedirectError,
  onStaleBuild,
  readPageBuildId,
  type StaleReason,
} from '@mediacore/api-client';

export {
  ApiAuthRedirectError,
  ApiTimeoutError,
  checkBuild,
  getStaleReasons,
  isAbortError,
  isAuthRedirectError,
  onStaleBuild,
  readPageBuildId,
  type StaleReason,
};

/**
 * The page's build ID, read once at load by the runtime's one meta reader. "" and
 * "dev" never trigger the rule. ABO's pre-shell bundle is never the shell, so
 * every check passes `shell: false` (07 §7.5.2).
 */
const pageBuildId = readPageBuildId();

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
    checkBuild(response, { buildId: pageBuildId, shell: false });
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
    checkBuild(response, { buildId: pageBuildId, shell: false });
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
