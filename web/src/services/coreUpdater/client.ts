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
//  - The request itself (redirect: 'manual', the SSO-bounce and text/html handling, the
//    deadline, problem+json decoding, Retry-After) is coreHttp's; a non-2xx answer becomes
//    an UpdaterRequestError (status, code, detail, request id, Retry-After).
//  - Apply sends only {version}, and no body at all for "the release the last check
//    offered". It never names a release asset: the server owns the asset name.
//  - A refusal is never retried: apply installs a binary and restarts the service.
//  - Nothing is logged. Errors carry the operation, status, code and detail the server
//    chose to send; never a request body, header, cookie or URL query.

import type { Problem } from '@mediacore/api-client';

import {
  DEFAULT_TIMEOUT_MS,
  MAX_RETRY_AFTER_MS,
  createTransport,
  parseRetryAfterMs,
} from '../coreHttp';

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

// The deadline, the Retry-After parser and its cap live in coreHttp, shared with coreBackups.
export { DEFAULT_TIMEOUT_MS, MAX_RETRY_AFTER_MS, parseRetryAfterMs };

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

export function createUpdaterClient(opts: UpdaterClientOptions = {}): UpdaterClient {
  const transport = createTransport(
    opts,
    (operation, r) =>
      new UpdaterRequestError({
        operation,
        status: r.status,
        code: r.code,
        detail: r.detail,
        requestId: r.requestId,
        retryAfterMs: r.retryAfterMs,
        requiredPermission: r.requiredPermission,
        fields: r.fields,
      })
  );

  async function send(
    op: Op,
    method: 'GET' | 'POST',
    path: string,
    body: UpdaterApplyRequest | undefined,
    signal: AbortSignal | undefined,
  ): Promise<Response> {
    const res = await transport.send(op, { method, path, json: body, signal });
    // Every updater answer is JSON; the transport has already refused an SSO page.
    const contentType = (res.headers?.get?.('Content-Type') ?? '').toLowerCase();
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
