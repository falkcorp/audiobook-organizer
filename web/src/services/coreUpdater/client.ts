// file: web/src/services/coreUpdater/client.ts
// version: 0.1.0
// guid: 0daff21b-1ac9-4021-9463-08a563b7613a
// last-edited: 2026-11-20

// The core updater client: status, check, apply and rollback over /api/core/v1/updater*
// (05-updater 5U.10). Check, apply and rollback only enqueue a task and answer 202
// {task_id}; follow the run on the event stream (coreEvents), never by polling here.
//
// UNWIRED: nothing in the running app imports this module until the F8-04b flip.
//
// Rules this file enforces, each with a test in client.test.ts:
//  - redirect: 'manual', as coreEvents does. An opaqueredirect response is an SSO bounce
//    and is raised as the API client's ApiAuthRedirectError; so is a 2xx text/html page.
//    response.redirected is never read.
//  - A non-2xx answer is decoded with the API client's decodeProblem into an
//    UpdaterRequestError (status, code, detail, request id, Retry-After).
//  - Apply sends only {version}, and no body at all for "the release the last check
//    offered". It never names a release asset: the server owns the asset name.
//  - A refusal is never retried: apply installs a binary and restarts the service.
//  - Nothing is logged. Errors carry the operation, status, code and detail the server
//    chose to send; never a request body, header, cookie or URL query.

import {
  ApiAuthRedirectError,
  ApiTimeoutError,
  LEGACY_PROBLEM_CODE,
  SESSION_ENDING_CODES,
  decodeProblem,
  type Problem,
} from '@mediacore/api-client';

import {
  UpdaterProtocolError,
  UpdaterRequestError,
  type UpdaterApplyRequest,
  type UpdaterStatus,
  type UpdaterTaskStarted,
} from './types';

export const UPDATER_PATH = '/api/core/v1/updater';
export const UPDATER_CHECK_PATH = `${UPDATER_PATH}/check`;
export const UPDATER_APPLY_PATH = `${UPDATER_PATH}/apply`;
export const UPDATER_ROLLBACK_PATH = `${UPDATER_PATH}/rollback`;

/** A request that gets no answer in this long is abandoned with ApiTimeoutError; <= 0 disables the deadline. */
export const DEFAULT_TIMEOUT_MS = 30000;
/** Retry-After above this is clamped: a hostile or broken header must not park the UI. */
export const MAX_RETRY_AFTER_MS = 10 * 60 * 1000;

export interface UpdaterClientOptions {
  baseUrl?: string;
  timeoutMs?: number;
  fetch?: typeof fetch;
  /**
   * Called for a 401 whose code ends the session (SESSION_ENDING_CODES), before the
   * error is thrown, as createCoreClient's onUnauthorized is.
   */
  onUnauthorized?: (problem: Problem) => void;
}

export interface UpdaterClient {
  /** GET /updater (core:system.manage). */
  status(opts?: { signal?: AbortSignal }): Promise<UpdaterStatus>;
  /** POST /updater/check (core:system.manage): enqueues a check; it installs nothing. */
  check(opts?: { signal?: AbortSignal }): Promise<UpdaterTaskStarted>;
  /**
   * POST /updater/apply (core:admin, interactive session). `version` is a release tag
   * such as v1.2.3; omit it for the release the last check offered. The service
   * restarts when the task finishes.
   */
  apply(version?: string, opts?: { signal?: AbortSignal }): Promise<UpdaterTaskStarted>;
  /** POST /updater/rollback (core:admin, interactive session): back to the kept binary. */
  rollback(opts?: { signal?: AbortSignal }): Promise<UpdaterTaskStarted>;
}

type Op = 'status' | 'check' | 'apply' | 'rollback';

/** Retry-After as delta-seconds or an HTTP date, in ms; null when absent or unusable. */
export function parseRetryAfterMs(value: string | null | undefined, now: () => number = Date.now): number | null {
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

export function createUpdaterClient(opts: UpdaterClientOptions = {}): UpdaterClient {
  // Raw fetch on purpose, not apiFetch or createCoreClient: both follow redirects, and the
  // bounce must be seen as an opaqueredirect (05-realtime 4.8 item 4). It is handled in
  // send() below (opaqueredirect and a 2xx text/html both raise ApiAuthRedirectError),
  // which is what the lint rule exists to guarantee.
  // eslint-disable-next-line no-restricted-properties
  const doFetch: typeof fetch = opts.fetch ?? ((...a) => globalThis.fetch(...a));
  const baseUrl = opts.baseUrl ?? '';
  const timeoutMs = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;

  async function send(
    op: Op,
    method: 'GET' | 'POST',
    path: string,
    body: UpdaterApplyRequest | undefined,
    signal: AbortSignal | undefined,
  ): Promise<Response> {
    const url = `${baseUrl}${path}`;
    const headers: Record<string, string> = { Accept: 'application/json' };
    const init: RequestInit = {
      method,
      headers,
      credentials: 'include',
      cache: 'no-store',
      redirect: 'manual',
    };
    if (body !== undefined) {
      headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(body);
    }

    const ctl = new AbortController();
    let timedOut = false;
    // timeoutMs <= 0 means no deadline, as createCoreClient's runtimeFetch has it.
    const timer =
      timeoutMs > 0
        ? setTimeout(() => {
            timedOut = true;
            ctl.abort();
          }, timeoutMs)
        : undefined;
    const forward = () => ctl.abort();
    if (signal?.aborted) ctl.abort();
    else signal?.addEventListener('abort', forward);
    init.signal = ctl.signal;

    let res: Response;
    try {
      res = await doFetch(url, init);
    } catch (err) {
      // "We gave up" is a failure to report; "the caller gave up" is control flow.
      if (timedOut) throw new ApiTimeoutError(url, timeoutMs);
      throw err;
    } finally {
      if (timer !== undefined) clearTimeout(timer);
      signal?.removeEventListener('abort', forward);
    }

    // SSO bounce: with redirect 'manual' the browser hands back an opaque response.
    if (res.type === 'opaqueredirect') throw new ApiAuthRedirectError(url, url);

    if (!res.ok) {
      const problem = await decodeProblem(res);
      if (res.status === 401 && SESSION_ENDING_CODES.has(problem.code)) opts.onUnauthorized?.(problem);
      throw new UpdaterRequestError({
        operation: op,
        status: res.status,
        code: problem.code,
        // A legacy or proxy body is not ours to echo: decodeProblem puts its first 200
        // characters in title, which could be anything the proxy printed.
        detail: problem.code === LEGACY_PROBLEM_CODE ? '' : (problem.detail ?? problem.title ?? ''),
        requestId: problem.request_id ?? '',
        retryAfterMs: parseRetryAfterMs(res.headers?.get?.('Retry-After')),
        requiredPermission: problem.required_permission,
        fields: problem.errors ?? [],
      });
    }

    const contentType = (res.headers?.get?.('Content-Type') ?? '').toLowerCase();
    // A same-origin SSO page answers 200 text/html: the same bounce, not an answer.
    if (contentType.includes('text/html')) throw new ApiAuthRedirectError(url, url);
    if (!contentType.includes('application/json')) {
      throw new UpdaterProtocolError(op, `unexpected content type "${contentType}"`);
    }
    return res;
  }

  async function json(op: Op, res: Response): Promise<Record<string, unknown>> {
    let v: unknown;
    try {
      v = await res.json();
    } catch {
      throw new UpdaterProtocolError(op, 'the answer is not JSON');
    }
    if (typeof v !== 'object' || v === null || Array.isArray(v)) {
      throw new UpdaterProtocolError(op, 'the answer is not a JSON object');
    }
    return v as Record<string, unknown>;
  }

  async function started(op: Op, path: string, body: UpdaterApplyRequest | undefined, signal?: AbortSignal) {
    const res = await send(op, 'POST', path, body, signal);
    if (res.status !== 202) throw new UpdaterProtocolError(op, `expected 202, got ${res.status}`);
    const o = await json(op, res);
    if (typeof o.task_id !== 'string' || o.task_id === '') {
      throw new UpdaterProtocolError(op, 'the answer has no task_id');
    }
    return { task_id: o.task_id } satisfies UpdaterTaskStarted;
  }

  return {
    async status(o) {
      const res = await send('status', 'GET', UPDATER_PATH, undefined, o?.signal);
      const v = await json('status', res);
      if (
        typeof v.mode !== 'string' ||
        typeof v.channel !== 'string' ||
        typeof v.current !== 'string' ||
        typeof v.available !== 'boolean'
      ) {
        throw new UpdaterProtocolError('status', 'the answer lacks mode, channel, current or available');
      }
      return v as unknown as UpdaterStatus;
    },
    check: (o) => started('check', UPDATER_CHECK_PATH, undefined, o?.signal),
    apply(version, o) {
      const tag = version?.trim() ?? '';
      return started('apply', UPDATER_APPLY_PATH, tag === '' ? undefined : { version: tag }, o?.signal);
    },
    rollback: (o) => started('rollback', UPDATER_ROLLBACK_PATH, undefined, o?.signal),
  };
}
