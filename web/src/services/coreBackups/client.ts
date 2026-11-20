// file: web/src/services/coreBackups/client.ts
// version: 0.1.0
// guid: 7c9e97d7-7013-4623-8d8c-53616ee18dda
// last-edited: 2026-11-20

// The core backups client: list, create, download URL, delete, import and the staged
// restore (restore, revert, pending, cancel) over /api/core/v1/backups* and
// /api/core/v1/restore/* (05-backup 5B.3.8). Create only enqueues a task and answers 202
// {task_id}; follow the run on the event stream (coreEvents), never by polling here.
// Restore only STAGES: the swap happens at the next start, so the answer carries
// restart_required.
//
// UNWIRED: nothing in the running app imports this module until the F8-04a flip.
//
// Rules this file enforces, each with a test in client.test.ts:
//  - redirect: 'manual', as coreEvents does. An opaqueredirect response is an SSO bounce
//    and is raised as the API client's ApiAuthRedirectError; so is a 2xx text/html page.
//    response.redirected is never read.
//  - A non-2xx answer is decoded with the API client's decodeProblem into a
//    BackupRequestError (status, code, detail, request id, Retry-After, required scope).
//    A 403 is never retried or papered over: the caller decides what to show.
//  - Request bodies carry only the members the server accepts (it rejects unknown ones),
//    and a call with nothing to say sends no body at all.
//  - Download is a URL, not a fetch: the archive is credential-equivalent and streams, so
//    the page navigates to it (an anchor or window.location) and the browser's own
//    download manager owns it. The server audits it before the stream starts.
//  - A refusal is never retried: restore stages a store swap.
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
  BackupArgumentError,
  BackupProtocolError,
  BackupRequestError,
  type BackupArchive,
  type BackupCreateRequest,
  type BackupList,
  type BackupRestoreRequest,
  type BackupRestoreStaged,
  type BackupRevertRequest,
  type BackupTaskStarted,
  type PendingRestore,
} from './types';

export const BACKUPS_PATH = '/api/core/v1/backups';
export const BACKUPS_IMPORT_PATH = `${BACKUPS_PATH}/import`;
export const RESTORE_REVERT_PATH = '/api/core/v1/restore/revert';
export const RESTORE_PENDING_PATH = '/api/core/v1/restore/pending';

/** A request that gets no answer in this long is abandoned with ApiTimeoutError; <= 0 disables the deadline. */
export const DEFAULT_TIMEOUT_MS = 30000;
/** Retry-After above this is clamped: a hostile or broken header must not park the UI. */
export const MAX_RETRY_AFTER_MS = 10 * 60 * 1000;
/** The import's name bound (core/internal/backuphttp maxNameBytes). */
const MAX_NAME_BYTES = 200;

export interface BackupsClientOptions {
  baseUrl?: string;
  /** Deadline of every call except import. */
  timeoutMs?: number;
  /**
   * Deadline of import, which uploads a whole archive and can take far longer than any
   * other call. Default 0: no deadline, only the caller's signal ends it.
   */
  importTimeoutMs?: number;
  fetch?: typeof fetch;
  /**
   * Called for a 401 whose code ends the session (SESSION_ENDING_CODES), before the
   * error is thrown, as createCoreClient's onUnauthorized is.
   */
  onUnauthorized?: (problem: Problem) => void;
}

export interface CallOptions {
  signal?: AbortSignal;
}

export interface BackupsClient {
  /** GET /backups (core:system.view). */
  list(opts?: CallOptions): Promise<BackupList>;
  /** POST /backups (core:system.manage): enqueues a backup task; the archive appears in list() when it ends. */
  create(req?: BackupCreateRequest, opts?: CallOptions): Promise<BackupTaskStarted>;
  /** DELETE /backups/{id} (core:system.manage, interactive session). 409 protected_by_floor when a retention floor holds it. */
  remove(id: string, opts?: CallOptions): Promise<void>;
  /**
   * POST /backups/import?name= (core:system.manage, interactive session). The archive is
   * the raw request body. `name` is only a suggestion; the entry returned has the name the
   * server chose.
   */
  importArchive(name: string, body: Blob, opts?: CallOptions): Promise<BackupArchive>;
  /**
   * POST /backups/{id}/restore (core:backup.restore, interactive session, owner): stages a
   * restore that runs when the service restarts. 202 {pending, restart_required}.
   */
  restore(id: string, req?: BackupRestoreRequest, opts?: CallOptions): Promise<BackupRestoreStaged>;
  /** POST /restore/revert: stages the undo of the last applied restore. 404 no_undo_point when none is kept. */
  revert(req?: BackupRevertRequest, opts?: CallOptions): Promise<BackupRestoreStaged>;
  /** GET /restore/pending (core:backup.restore): the staged restore, or null on a 204. */
  pending(opts?: CallOptions): Promise<PendingRestore | null>;
  /** DELETE /restore/pending (core:backup.restore, interactive session, owner): unstages it. */
  cancelRestore(opts?: CallOptions): Promise<void>;
  /** The URL a download navigates to; see backupDownloadUrl. */
  downloadUrl(id: string): string;
}

type Op = 'list' | 'create' | 'remove' | 'import' | 'restore' | 'revert' | 'pending' | 'cancel';

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

const CREATE_KEYS = ['reason', 'include_derived', 'include_recreatable'] as const;
const RESTORE_KEYS = [
  'verify',
  'allow_weak_verify',
  'foreign',
  'replace_config',
  'accept_unreadable_secrets',
  'restart',
] as const;
const REVERT_KEYS = RESTORE_KEYS.filter((k) => k !== 'foreign');

/**
 * The members of req the route accepts that are set, or undefined for no body at all. The
 * server rejects an unknown member (400 unknown_field), so nothing else is forwarded.
 */
function bodyOf(
  req: object | undefined,
  keys: readonly string[]
): Record<string, unknown> | undefined {
  if (req === undefined) return undefined;
  const out: Record<string, unknown> = {};
  for (const k of keys) {
    const v = (req as Record<string, unknown>)[k];
    if (v !== undefined) out[k] = v;
  }
  return Object.keys(out).length > 0 ? out : undefined;
}

function checkId(id: string): string {
  if (
    typeof id !== 'string' ||
    id === '' ||
    id.length > MAX_NAME_BYTES ||
    /[/\\]/.test(id) ||
    id.includes('..')
  ) {
    throw new BackupArgumentError('archive id is empty or not a plain id');
  }
  return id;
}

/** The server's cleanName rule (invalid_name), checked before an upload starts. */
function checkName(name: string): string {
  const bytes = typeof name === 'string' ? new TextEncoder().encode(name).length : 0;
  if (
    typeof name !== 'string' ||
    name === '' ||
    bytes > MAX_NAME_BYTES ||
    /[/\\]/.test(name) ||
    name.includes('..') ||
    // eslint-disable-next-line no-control-regex
    /[\u0000-\u001f\u007f-\u009f]/.test(name)
  ) {
    throw new BackupArgumentError('import name must be a plain file name');
  }
  return name;
}

/**
 * The URL of GET /backups/{id}/download (core:admin, interactive session, owner). It is a
 * URL on purpose: navigate to it and let the browser download it. Fetching it into memory
 * would hold the whole archive in the page.
 */
export function backupDownloadUrl(id: string, baseUrl = ''): string {
  return `${baseUrl}${BACKUPS_PATH}/${encodeURIComponent(checkId(id))}/download`;
}

export function createBackupsClient(opts: BackupsClientOptions = {}): BackupsClient {
  // Raw fetch on purpose, not apiFetch or createCoreClient: both follow redirects, and the
  // bounce must be seen as an opaqueredirect (05-realtime 4.8 item 4). It is handled in
  // send() below (opaqueredirect and a 2xx text/html both raise ApiAuthRedirectError),
  // which is what the lint rule exists to guarantee.
  // eslint-disable-next-line no-restricted-properties
  const doFetch: typeof fetch = opts.fetch ?? ((...a) => globalThis.fetch(...a));
  const baseUrl = opts.baseUrl ?? '';
  const timeoutMs = opts.timeoutMs ?? DEFAULT_TIMEOUT_MS;
  const importTimeoutMs = opts.importTimeoutMs ?? 0;

  interface Req {
    method: 'GET' | 'POST' | 'DELETE';
    path: string;
    json?: unknown;
    raw?: Blob;
    deadlineMs?: number;
    signal?: AbortSignal;
  }

  /** One request; returns an ok, non-HTML response. Callers check status and body. */
  async function send(op: Op, r: Req): Promise<Response> {
    const url = `${baseUrl}${r.path}`;
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
    // A deadline <= 0 means none, as createCoreClient's runtimeFetch has it.
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
      // "We gave up" is a failure to report; "the caller gave up" is control flow.
      if (timedOut) throw new ApiTimeoutError(url, deadline);
      throw err;
    } finally {
      if (timer !== undefined) clearTimeout(timer);
      r.signal?.removeEventListener('abort', forward);
    }

    // SSO bounce: with redirect 'manual' the browser hands back an opaque response.
    if (res.type === 'opaqueredirect') throw new ApiAuthRedirectError(url, url);

    if (!res.ok) {
      const problem = await decodeProblem(res);
      if (res.status === 401 && SESSION_ENDING_CODES.has(problem.code))
        opts.onUnauthorized?.(problem);
      throw new BackupRequestError({
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

    // A same-origin SSO page answers 200 text/html: the same bounce, not an answer.
    if ((res.headers?.get?.('Content-Type') ?? '').toLowerCase().includes('text/html')) {
      throw new ApiAuthRedirectError(url, url);
    }
    return res;
  }

  async function obj(op: Op, res: Response, status: number): Promise<Record<string, unknown>> {
    if (res.status !== status)
      throw new BackupProtocolError(op, `expected ${status}, got ${res.status}`);
    const contentType = (res.headers?.get?.('Content-Type') ?? '').toLowerCase();
    if (!contentType.includes('application/json')) {
      throw new BackupProtocolError(op, `unexpected content type "${contentType}"`);
    }
    let v: unknown;
    try {
      v = await res.json();
    } catch {
      throw new BackupProtocolError(op, 'the answer is not JSON');
    }
    if (typeof v !== 'object' || v === null || Array.isArray(v)) {
      throw new BackupProtocolError(op, 'the answer is not a JSON object');
    }
    return v as Record<string, unknown>;
  }

  function noContent(op: Op, res: Response): void {
    if (res.status !== 204) throw new BackupProtocolError(op, `expected 204, got ${res.status}`);
  }

  function archive(op: Op, v: unknown): BackupArchive {
    const a = v as Partial<BackupArchive> | null;
    if (
      typeof a !== 'object' ||
      a === null ||
      typeof a.id !== 'string' ||
      typeof a.name !== 'string' ||
      typeof a.size !== 'number' ||
      typeof a.created_at !== 'string'
    ) {
      throw new BackupProtocolError(op, 'an archive lacks id, name, size or created_at');
    }
    return a as BackupArchive;
  }

  function pendingOf(op: Op, v: unknown): PendingRestore {
    const p = v as Partial<PendingRestore> | null;
    if (
      typeof p !== 'object' ||
      p === null ||
      typeof p.archive_id !== 'string' ||
      typeof p.kind !== 'string'
    ) {
      throw new BackupProtocolError(op, 'the pending restore lacks archive_id or kind');
    }
    // The server always sends an array, but one that arrives without it must not crash a renderer.
    return { ...(p as PendingRestore), warnings: Array.isArray(p.warnings) ? p.warnings : [] };
  }

  async function staged(op: Op, path: string, body: object | undefined, signal?: AbortSignal) {
    const o = await obj(op, await send(op, { method: 'POST', path, json: body, signal }), 202);
    if (typeof o.restart_required !== 'boolean') {
      throw new BackupProtocolError(op, 'the answer has no restart_required');
    }
    return {
      pending: pendingOf(op, o.pending),
      restart_required: o.restart_required,
    } satisfies BackupRestoreStaged;
  }

  return {
    async list(o) {
      const v = await obj(
        'list',
        await send('list', { method: 'GET', path: BACKUPS_PATH, signal: o?.signal }),
        200
      );
      if (!Array.isArray(v.backups))
        throw new BackupProtocolError('list', 'the answer has no backups array');
      return { backups: v.backups.map((a) => archive('list', a)) };
    },
    async create(req, o) {
      const res = await send('create', {
        method: 'POST',
        path: BACKUPS_PATH,
        json: bodyOf(req, CREATE_KEYS),
        signal: o?.signal,
      });
      const v = await obj('create', res, 202);
      if (typeof v.task_id !== 'string' || v.task_id === '') {
        throw new BackupProtocolError('create', 'the answer has no task_id');
      }
      return { task_id: v.task_id } satisfies BackupTaskStarted;
    },
    async remove(id, o) {
      const path = `${BACKUPS_PATH}/${encodeURIComponent(checkId(id))}`;
      noContent('remove', await send('remove', { method: 'DELETE', path, signal: o?.signal }));
    },
    async importArchive(name, body, o) {
      const path = `${BACKUPS_IMPORT_PATH}?name=${encodeURIComponent(checkName(name))}`;
      const res = await send('import', {
        method: 'POST',
        path,
        raw: body,
        deadlineMs: importTimeoutMs,
        signal: o?.signal,
      });
      return archive('import', await obj('import', res, 201));
    },
    async restore(id, req, o) {
      return staged(
        'restore',
        `${BACKUPS_PATH}/${encodeURIComponent(checkId(id))}/restore`,
        bodyOf(req, RESTORE_KEYS),
        o?.signal
      );
    },
    revert: (req, o) => staged('revert', RESTORE_REVERT_PATH, bodyOf(req, REVERT_KEYS), o?.signal),
    async pending(o) {
      const res = await send('pending', {
        method: 'GET',
        path: RESTORE_PENDING_PATH,
        signal: o?.signal,
      });
      if (res.status === 204) return null;
      return pendingOf('pending', await obj('pending', res, 200));
    },
    async cancelRestore(o) {
      noContent(
        'cancel',
        await send('cancel', { method: 'DELETE', path: RESTORE_PENDING_PATH, signal: o?.signal })
      );
    },
    downloadUrl: (id) => backupDownloadUrl(id, baseUrl),
  };
}
