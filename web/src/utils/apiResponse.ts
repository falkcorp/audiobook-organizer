// file: web/src/utils/apiResponse.ts
// version: 1.0.0
// guid: c4166674-f392-49d0-bc83-9f64fd161658
// last-edited: 2026-10-10

import { isAuthRedirectError } from './apiFetch';

/**
 * The server wraps successful responses as `{ data: ... }` (httputil
 * SuccessResponse). Returns the payload, tolerating a bare body for the few
 * endpoints that answer without the envelope.
 */
export function unwrapData<T>(body: unknown): T {
  const record = body as { data?: unknown } | null | undefined;
  return (record?.data ?? body) as T;
}

/**
 * Reads the reason from a failed response: the JSON body's `error` field when
 * there is one, else `HTTP <status>`. Raw response text is never returned, so an
 * HTML error page cannot leak into the UI.
 */
export async function responseErrorMessage(resp: Response): Promise<string> {
  try {
    const body = (await resp.json()) as { error?: unknown };
    if (typeof body?.error === 'string' && body.error) return body.error;
  } catch {
    // not JSON; fall through to the status
  }
  return `HTTP ${resp.status}`;
}

/** Shown wherever an ApiAuthRedirectError is caught. */
export const SESSION_EXPIRED_MESSAGE = 'Your session has expired. Sign in again to continue.';

/**
 * User-facing text for a caught request error: the session-expired message for
 * an auth bounce, else the given fallback. The underlying error text (which can
 * carry a URL or a network stack message) is deliberately not shown.
 */
export function describeRequestError(err: unknown, fallback: string): string {
  return isAuthRedirectError(err) ? SESSION_EXPIRED_MESSAGE : fallback;
}
