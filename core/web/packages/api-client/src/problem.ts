// file: core/web/packages/api-client/src/problem.ts
// version: 1.0.0
// guid: d1c837e6-e2bd-4705-9d5f-7d0192a25b91
// last-edited: 2026-11-08

// Error-envelope decoding (07 §7.8; 05-http-api §3.3). A native route answers an
// error as an RFC 9457 `application/problem+json` `Problem`; a route still on the
// legacy mount answers ABO's own envelope, `{"error": "<text>", "message"?: "<text>"}`
// (internal/server/gone.go, web/src/services/api.ts's buildApiError), or no JSON at
// all (a proxy's HTML 502). The client types `Problem` as the error of every call,
// so a legacy answer is decoded into one here and the caller sees one shape.

import type { components } from './generated/core';

/** The native error body (05-http-api §3.3), as core.json declares it. */
export type Problem = components['schemas']['core.Problem'];

/** The `code` a decoded legacy envelope carries: it names no stable machine code. */
export const LEGACY_PROBLEM_CODE = 'legacy_error';

const PROBLEM_CONTENT_TYPE = 'application/problem+json';

/** True when the response declares itself `application/problem+json`. */
export function isProblemResponse(response: Response): boolean {
  return (response.headers?.get?.('Content-Type') ?? '').toLowerCase().includes(PROBLEM_CONTENT_TYPE);
}

function text(v: unknown): string | undefined {
  return typeof v === 'string' && v.trim() !== '' ? v : undefined;
}

/**
 * Decodes an error response into a `Problem`, whichever envelope it carries. It
 * reads the body of the response it is given (callers pass a clone), never throws,
 * and always returns a complete `Problem`: `request_id` falls back to the
 * `X-Request-ID` header, `status` to the response's.
 *
 * Legacy reading: `title` is the envelope's `error`, as ABO's buildApiError reads
 * it as the message, else its `message`, else the status text; `detail` is the
 * `message` when both are present and differ; `code` is `legacy_error`. A body
 * that is not JSON gives its first 200 characters as the title.
 */
export async function decodeProblem(response: Response): Promise<Problem> {
  const status = response.status;
  const requestId = response.headers?.get?.('X-Request-ID') ?? '';
  let raw = '';
  try {
    raw = await response.text();
  } catch {
    // An unreadable body decodes like an empty one.
  }
  let json: unknown;
  try {
    json = raw === '' ? undefined : JSON.parse(raw);
  } catch {
    json = undefined;
  }
  const obj = json && typeof json === 'object' && !Array.isArray(json) ? (json as Record<string, unknown>) : null;

  if (obj && isProblemResponse(response)) {
    const problem: Problem = {
      type: text(obj.type) ?? 'about:blank',
      title: text(obj.title) ?? (response.statusText || `HTTP ${status}`),
      status: typeof obj.status === 'number' ? obj.status : status,
      code: text(obj.code) ?? '',
      request_id: text(obj.request_id) ?? requestId,
    };
    const detail = text(obj.detail);
    if (detail) problem.detail = detail;
    const instance = text(obj.instance);
    if (instance) problem.instance = instance;
    const required = text(obj.required_permission);
    if (required) problem.required_permission = required;
    if (Array.isArray(obj.errors)) problem.errors = obj.errors as NonNullable<Problem['errors']>;
    return problem;
  }

  const error = obj ? text(obj.error) : undefined;
  const message = obj ? text(obj.message) : undefined;
  const fallback = obj ? undefined : text(raw.trim().slice(0, 200));
  const title = error ?? message ?? fallback ?? (response.statusText || `HTTP ${status}`);
  const problem: Problem = {
    type: 'about:blank',
    title,
    status,
    code: LEGACY_PROBLEM_CODE,
    request_id: requestId,
  };
  const detail = message && error && message !== error ? message : undefined;
  if (detail) problem.detail = detail;
  return problem;
}
