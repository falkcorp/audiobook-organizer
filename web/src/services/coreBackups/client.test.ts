// file: web/src/services/coreBackups/client.test.ts
// version: 0.1.0
// guid: add9cafc-404b-472b-bec0-08030a51d474
// last-edited: 2026-11-20

import { ApiAuthRedirectError, ApiTimeoutError, isAuthRedirectError } from '@mediacore/api-client';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

import {
  BACKUPS_IMPORT_PATH,
  BACKUPS_PATH,
  MAX_RETRY_AFTER_MS,
  RESTORE_PENDING_PATH,
  RESTORE_REVERT_PATH,
  backupDownloadUrl,
  createBackupsClient,
  parseRetryAfterMs,
} from './client';
import {
  BackupArgumentError,
  BackupProtocolError,
  BackupRequestError,
  type BackupArchive,
  type PendingRestore,
} from './types';

interface Call {
  url: string;
  init: RequestInit;
  headers: Record<string, string>;
}

function fake(...answers: Array<Response | ((c: Call) => Response | Promise<Response>)>) {
  const calls: Call[] = [];
  const f: typeof fetch = (input, init = {}) => {
    const headers: Record<string, string> = {};
    new Headers(init.headers).forEach((v, k) => {
      headers[k] = v;
    });
    const call = { url: String(input), init, headers };
    calls.push(call);
    const a = answers.shift();
    if (!a) return Promise.resolve(new Response('no answer', { status: 500 }));
    return Promise.resolve(typeof a === 'function' ? a(call) : a);
  };
  return { fetch: f, calls };
}

const jsonRes = (body: unknown, status = 200, headers: Record<string, string> = {}) =>
  new Response(JSON.stringify(body), {
    status,
    headers: { 'Content-Type': 'application/json', ...headers },
  });

const noContent = () => new Response(null, { status: 204 });

const problem = (
  status: number,
  code: string,
  detail: string,
  extra: Record<string, unknown> = {},
  headers: Record<string, string> = {}
) =>
  new Response(
    JSON.stringify({
      type: 'about:blank',
      title: detail,
      status,
      code,
      detail,
      request_id: 'req-bruce',
      ...extra,
    }),
    {
      status,
      headers: { 'Content-Type': 'application/problem+json', ...headers },
    }
  );

function opaqueRedirect(): Response {
  // The shape a browser hands back under redirect: 'manual' (status 0, ok false); as in coreEvents.
  return new Proxy(
    { type: 'opaqueredirect', status: 0, ok: false },
    {
      get(t, p) {
        if (p === 'redirected') throw new Error('response.redirected must not be read');
        return (t as Record<string | symbol, unknown>)[p as string];
      },
    }
  ) as unknown as Response;
}

const ID = '01JBATMANCAVE0000000000000';

const ARCHIVE: BackupArchive = {
  id: ID,
  name: 'batman-2026-11-20.archive',
  size: 1234,
  created_at: '2026-11-20T10:00:00Z',
  format: 'mediacore-backup/1',
  provenance: 'own',
  reason: 'manual',
  app_version: 'v1.0.0',
  core_version: 'v1.0.0',
  verified: { archive_id: ID, at: '2026-11-20T10:01:00Z', level: 'entries', ok: true },
};

const PENDING: PendingRestore = {
  archive_id: ID,
  kind: 'archive',
  staged_at: '2026-11-20T10:05:00Z',
  expires_at: '2026-11-20T11:05:00Z',
  staged_by_build: 'v1.0.0',
  requested_by: 'user-robin',
  warnings: [],
};

let consoleSpies: Array<ReturnType<typeof vi.spyOn>> = [];
beforeEach(() => {
  consoleSpies = (['log', 'info', 'warn', 'error', 'debug'] as const).map((m) =>
    vi.spyOn(console, m).mockImplementation(() => {})
  );
});
afterEach(() => {
  // The client never logs: not an error, not a header, not a URL.
  for (const s of consoleSpies) expect(s).not.toHaveBeenCalled();
  vi.restoreAllMocks();
  vi.useRealTimers();
});

function client(f: ReturnType<typeof fake>, o: Parameters<typeof createBackupsClient>[0] = {}) {
  return createBackupsClient({ fetch: f.fetch, ...o });
}

/**
 * A fetch that stays pending until resolve() is called and, like a real fetch, rejects with an
 * AbortError the moment its signal aborts. A fake that ignores init.signal cannot tell a client
 * that set a deadline from one that did not.
 */
function pendingFetch() {
  const signals: AbortSignal[] = [];
  let answer!: (r: Response) => void;
  const f: typeof fetch = (_u, init) =>
    new Promise<Response>((res, rej) => {
      answer = res;
      const sig = init?.signal as AbortSignal;
      signals.push(sig);
      sig.addEventListener('abort', () => rej(new DOMException('aborted', 'AbortError')));
    });
  return { fetch: f, signals, resolve: (r: Response) => answer(r) };
}

async function caught(p: Promise<unknown>): Promise<unknown> {
  try {
    await p;
  } catch (e) {
    return e;
  }
  throw new Error('expected the call to reject');
}

describe('list', () => {
  it('sends GET /backups with cookies, no cache, manual redirect and no body', async () => {
    const f = fake(jsonRes({ backups: [ARCHIVE] }));
    const out = await client(f).list();
    expect(out.backups).toEqual([ARCHIVE]);
    expect(f.calls).toHaveLength(1);
    const c = f.calls[0];
    expect(c.url).toBe(BACKUPS_PATH);
    expect(c.init.method).toBe('GET');
    expect(c.init.credentials).toBe('include');
    expect(c.init.cache).toBe('no-store');
    expect(c.init.redirect).toBe('manual');
    expect(c.init.body).toBeUndefined();
    expect(c.headers.accept).toBe('application/json');
    expect(c.headers['content-type']).toBeUndefined();
  });

  it('prefixes baseUrl', async () => {
    const f = fake(jsonRes({ backups: [] }));
    await client(f, { baseUrl: 'https://abo.example.test' }).list();
    expect(f.calls[0].url).toBe('https://abo.example.test/api/core/v1/backups');
  });

  it('an empty catalogue is an empty array, not an error', async () => {
    const f = fake(jsonRes({ backups: [] }));
    await expect(client(f).list()).resolves.toEqual({ backups: [] });
  });

  it('a 403 is a BackupRequestError naming the scope, and is not retried', async () => {
    const f = fake(
      problem(403, 'permission_denied', 'missing scope', {
        required_permission: 'core:system.view',
      }),
      jsonRes({ backups: [] })
    );
    const e = (await caught(client(f).list())) as BackupRequestError;
    expect(e).toBeInstanceOf(BackupRequestError);
    expect(e.status).toBe(403);
    expect(e.isForbidden).toBe(true);
    expect(e.needsInteractiveSession).toBe(false);
    expect(e.code).toBe('permission_denied');
    expect(e.requiredPermission).toBe('core:system.view');
    expect(e.requestId).toBe('req-bruce');
    expect(f.calls).toHaveLength(1);
  });

  it('a 401 with a session-ending code calls onUnauthorized before throwing', async () => {
    const seen: string[] = [];
    const f = fake(problem(401, 'authentication_required', 'authentication is required'));
    const e = (await caught(
      client(f, { onUnauthorized: (p) => seen.push(p.code) }).list()
    )) as BackupRequestError;
    expect(e.status).toBe(401);
    expect(seen).toEqual(['authentication_required']);
  });

  it('a 503 carries Retry-After in ms', async () => {
    const f = fake(
      problem(503, 'unavailable', 'backup is not available', {}, { 'Retry-After': '5' })
    );
    const e = (await caught(client(f).list())) as BackupRequestError;
    expect(e.isUnavailable).toBe(true);
    expect(e.retryAfterMs).toBe(5000);
  });

  it('a proxy page that is not problem+json yields a legacy code and does not echo the body', async () => {
    const f = fake(
      new Response('<html>502 from nginx secret-host</html>', {
        status: 502,
        headers: { 'Content-Type': 'text/html' },
      })
    );
    const e = (await caught(client(f).list())) as BackupRequestError;
    expect(e).toBeInstanceOf(BackupRequestError);
    expect(e.status).toBe(502);
    expect(e.detail).toBe('');
    expect(e.message).not.toContain('secret-host');
  });

  it('an opaque redirect (SSO bounce) is ApiAuthRedirectError and never reads response.redirected', async () => {
    const f = fake(opaqueRedirect());
    const e = await caught(client(f).list());
    expect(e).toBeInstanceOf(ApiAuthRedirectError);
    expect(isAuthRedirectError(e)).toBe(true);
  });

  it('a 200 text/html page is the same bounce', async () => {
    const f = fake(
      new Response('<html>log in</html>', { status: 200, headers: { 'Content-Type': 'text/html' } })
    );
    expect(await caught(client(f).list())).toBeInstanceOf(ApiAuthRedirectError);
  });

  it('an archive without a string format or provenance is a BackupProtocolError', async () => {
    for (const mut of [{ format: undefined }, { provenance: undefined }, { format: 3 }]) {
      const f = fake(jsonRes({ backups: [{ ...ARCHIVE, ...mut }] }));
      expect(await caught(client(f).list())).toBeInstanceOf(BackupProtocolError);
    }
  });

  it('only ids the server issues reach fetch (ULID or legacy-)', async () => {
    const legacy200 = `legacy-${'a'.repeat(193)}`;
    const f = fake(noContent(), noContent(), noContent());
    await client(f).remove(legacy200);
    await client(f).remove('legacy-bruce_wayne-1');
    await client(f).remove(ID);
    for (const bad of [
      'abc',
      ID.toLowerCase(),
      ID + 'A',
      'legacy-',
      'legacy-a/b',
      'legacy-a.b',
      `legacy-${'a'.repeat(19)}`, // 26 bytes: Go's ULID-length branch refuses it
      `${legacy200}a`, // 201 bytes
    ]) {
      expect(await caught(client(f).remove(bad))).toBeInstanceOf(BackupArgumentError);
    }
    expect(f.calls).toHaveLength(3);
  });

  it('a 200 with the wrong shape is a BackupProtocolError, not a crash', async () => {
    for (const bad of [
      jsonRes({}),
      jsonRes([]),
      jsonRes({ backups: [{ id: 1 }] }),
      new Response('{', { status: 200, headers: { 'Content-Type': 'application/json' } }),
      new Response('x', { status: 200, headers: { 'Content-Type': 'text/plain' } }),
    ]) {
      const f = fake(bad);
      expect(await caught(client(f).list())).toBeInstanceOf(BackupProtocolError);
    }
  });

  it('a hung request is abandoned with ApiTimeoutError', async () => {
    vi.useFakeTimers();
    const f: typeof fetch = (_u, init) =>
      new Promise((_res, rej) =>
        init?.signal?.addEventListener('abort', () =>
          rej(new DOMException('aborted', 'AbortError'))
        )
      );
    const p = caught(createBackupsClient({ fetch: f, timeoutMs: 1000 }).list());
    await vi.advanceTimersByTimeAsync(1001);
    expect(await p).toBeInstanceOf(ApiTimeoutError);
  });

  it('the caller aborting is the AbortError, not a timeout', async () => {
    const ctl = new AbortController();
    const f: typeof fetch = (_u, init) =>
      new Promise((_res, rej) =>
        init?.signal?.addEventListener('abort', () =>
          rej(new DOMException('aborted', 'AbortError'))
        )
      );
    const p = caught(createBackupsClient({ fetch: f }).list({ signal: ctl.signal }));
    ctl.abort();
    const e = (await p) as Error;
    expect(e).not.toBeInstanceOf(ApiTimeoutError);
    expect(e.name).toBe('AbortError');
  });
});

describe('create', () => {
  it('with nothing to say sends POST /backups with no body and no Content-Type', async () => {
    const f = fake(jsonRes({ task_id: 'task-1' }, 202));
    await expect(client(f).create()).resolves.toEqual({ task_id: 'task-1' });
    const c = f.calls[0];
    expect(c.url).toBe(BACKUPS_PATH);
    expect(c.init.method).toBe('POST');
    expect(c.init.body).toBeUndefined();
    expect(c.headers['content-type']).toBeUndefined();
  });

  it('sends only the members the server accepts', async () => {
    const f = fake(jsonRes({ task_id: 'task-2' }, 202));
    const req = {
      reason: 'before upgrade',
      include_derived: true,
      include_recreatable: false,
      bogus: 1,
    } as never;
    await client(f).create(req);
    const c = f.calls[0];
    expect(c.headers['content-type']).toBe('application/json');
    expect(JSON.parse(c.init.body as string)).toEqual({
      reason: 'before upgrade',
      include_derived: true,
      include_recreatable: false,
    });
  });

  it('an empty request object sends no body', async () => {
    const f = fake(jsonRes({ task_id: 'task-3' }, 202));
    await client(f).create({});
    expect(f.calls[0].init.body).toBeUndefined();
  });

  it('a 200 instead of 202, or no task_id, is a protocol error', async () => {
    expect(await caught(client(fake(jsonRes({ task_id: 'x' }, 200))).create())).toBeInstanceOf(
      BackupProtocolError
    );
    expect(await caught(client(fake(jsonRes({}, 202))).create())).toBeInstanceOf(
      BackupProtocolError
    );
    expect(await caught(client(fake(jsonRes({ task_id: '' }, 202))).create())).toBeInstanceOf(
      BackupProtocolError
    );
  });

  it('a 422 invalid reason keeps its field errors', async () => {
    const f = fake(
      problem(422, 'validation_failed', 'invalid body', {
        errors: [{ field: '/body/reason', code: 'invalid', message: 'bad' }],
      })
    );
    const e = (await caught(client(f).create({ reason: '\u0001' }))) as BackupRequestError;
    expect(e.status).toBe(422);
    expect(e.fields).toEqual([{ field: '/body/reason', code: 'invalid', message: 'bad' }]);
  });

  it('a 403 (core:system.manage missing) is surfaced and not retried', async () => {
    const f = fake(
      problem(403, 'permission_denied', 'missing scope', {
        required_permission: 'core:system.manage',
      })
    );
    const e = (await caught(client(f).create())) as BackupRequestError;
    expect(e.isForbidden).toBe(true);
    expect(e.requiredPermission).toBe('core:system.manage');
    expect(f.calls).toHaveLength(1);
  });
});

describe('download URL', () => {
  it('is the download route for a legacy id too, with no fetch', () => {
    const f = fake();
    expect(client(f).downloadUrl(ID)).toBe(`/api/core/v1/backups/${ID}/download`);
    expect(backupDownloadUrl('legacy-a_b-1', 'https://abo.example.test')).toBe(
      'https://abo.example.test/api/core/v1/backups/legacy-a_b-1/download'
    );
    expect(f.calls).toHaveLength(0);
  });

  it('honours the client baseUrl', () => {
    expect(client(fake(), { baseUrl: 'https://abo.example.test' }).downloadUrl(ID)).toBe(
      `https://abo.example.test/api/core/v1/backups/${ID}/download`
    );
  });

  it('refuses an empty id or one that could leave the path', () => {
    for (const bad of ['', 'a/b', 'a\\b', '..', 'x..y', 'a'.repeat(201)]) {
      expect(() => backupDownloadUrl(bad)).toThrow(BackupArgumentError);
    }
  });
});

describe('remove', () => {
  it('sends DELETE /backups/{id} and resolves on 204', async () => {
    const f = fake(noContent());
    await expect(client(f).remove(ID)).resolves.toBeUndefined();
    expect(f.calls[0].url).toBe(`${BACKUPS_PATH}/${ID}`);
    expect(f.calls[0].init.method).toBe('DELETE');
    expect(f.calls[0].init.body).toBeUndefined();
  });

  it('a 409 protected_by_floor and a 404 are BackupRequestErrors', async () => {
    const a = (await caught(
      client(fake(problem(409, 'protected_by_floor', 'held'))).remove(ID)
    )) as BackupRequestError;
    expect([a.status, a.code]).toEqual([409, 'protected_by_floor']);
    const b = (await caught(
      client(fake(problem(404, 'archive_not_found', 'no archive has that ID'))).remove(ID)
    )) as BackupRequestError;
    expect([b.status, b.code]).toEqual([404, 'archive_not_found']);
  });

  it('a 403 interactive_required tells the caller to sign in interactively', async () => {
    const e = (await caught(
      client(
        fake(problem(403, 'interactive_required', 'this route needs an interactive session'))
      ).remove(ID)
    )) as BackupRequestError;
    expect(e.needsInteractiveSession).toBe(true);
  });

  it('a 200 instead of 204 is a protocol error; a bad id never reaches fetch', async () => {
    expect(await caught(client(fake(jsonRes({}))).remove(ID))).toBeInstanceOf(BackupProtocolError);
    const f = fake();
    expect(await caught(client(f).remove('../x'))).toBeInstanceOf(BackupArgumentError);
    expect(f.calls).toHaveLength(0);
  });
});

describe('importArchive', () => {
  it('posts the raw archive with the name in the query and no deadline by default', async () => {
    const f = fake(jsonRes(ARCHIVE, 201));
    const body = new Blob(['archive-bytes'], { type: 'application/zip' });
    const out = await client(f).importArchive('my backup.archive', body);
    expect(out).toEqual(ARCHIVE);
    const c = f.calls[0];
    expect(c.url).toBe(`${BACKUPS_IMPORT_PATH}?name=my%20backup.archive`);
    expect(c.init.method).toBe('POST');
    expect(c.init.body).toBe(body);
    expect(c.headers['content-type']).toBe('application/octet-stream');
  });

  it('is not abandoned by the 30 s default deadline', async () => {
    vi.useFakeTimers();
    const pf = pendingFetch();
    const p = createBackupsClient({ fetch: pf.fetch }).importArchive('a.archive', new Blob(['x']));
    await vi.advanceTimersByTimeAsync(10 * 60 * 1000);
    expect(pf.signals[0].aborted).toBe(false);
    pf.resolve(jsonRes(ARCHIVE, 201));
    await expect(p).resolves.toEqual(ARCHIVE);
  });

  it('honours importTimeoutMs when set', async () => {
    vi.useFakeTimers();
    const f: typeof fetch = (_u, init) =>
      new Promise((_res, rej) =>
        init?.signal?.addEventListener('abort', () =>
          rej(new DOMException('aborted', 'AbortError'))
        )
      );
    const p = caught(
      createBackupsClient({ fetch: f, importTimeoutMs: 500 }).importArchive(
        'a.archive',
        new Blob(['x'])
      )
    );
    await vi.advanceTimersByTimeAsync(501);
    expect(await p).toBeInstanceOf(ApiTimeoutError);
  });

  it('a timeout or bounce names the endpoint, never the ?name= query', async () => {
    vi.useFakeTimers();
    const hung: typeof fetch = (_u, init) =>
      new Promise((_res, rej) =>
        init?.signal?.addEventListener('abort', () =>
          rej(new DOMException('aborted', 'AbortError'))
        )
      );
    const p = caught(
      createBackupsClient({ fetch: hung, importTimeoutMs: 500 }).importArchive(
        'robin-secret.archive',
        new Blob(['x'])
      )
    );
    await vi.advanceTimersByTimeAsync(501);
    const t = (await p) as Error;
    expect(t).toBeInstanceOf(ApiTimeoutError);
    expect(t.message).not.toContain('robin-secret');
    expect(t.message).not.toContain('?name=');

    const f = fake(opaqueRedirect());
    const b = (await caught(
      client(f).importArchive('robin-secret.archive', new Blob(['x']))
    )) as Error;
    expect(b).toBeInstanceOf(ApiAuthRedirectError);
    expect(JSON.stringify(b)).not.toContain('robin-secret');
    expect(b.message).not.toContain('robin-secret');
  });

  it('a lone surrogate in the name is a BackupArgumentError, not a URIError', async () => {
    const f = fake();
    expect(
      await caught(client(f).importArchive('a\ud800.archive', new Blob(['x'])))
    ).toBeInstanceOf(BackupArgumentError);
    expect(f.calls).toHaveLength(0);
  });

  it('refuses a name the server would refuse before uploading anything', async () => {
    const f = fake();
    for (const bad of [
      '',
      'a/b.archive',
      'a\\b',
      '..',
      'x..y',
      'ctl\u0001.archive',
      'n'.repeat(201),
      'é'.repeat(101),
    ]) {
      expect(await caught(client(f).importArchive(bad, new Blob(['x'])))).toBeInstanceOf(
        BackupArgumentError
      );
    }
    expect(f.calls).toHaveLength(0);
  });

  it('413 body_too_large and 400 malformed_upload surface with their codes', async () => {
    const a = (await caught(
      client(fake(problem(413, 'body_too_large', 'the body is too large'))).importArchive(
        'a.archive',
        new Blob(['x'])
      )
    )) as BackupRequestError;
    expect([a.status, a.code]).toEqual([413, 'body_too_large']);
    const b = (await caught(
      client(fake(problem(400, 'malformed_upload', 'bad'))).importArchive(
        'a.archive',
        new Blob(['x'])
      )
    )) as BackupRequestError;
    expect(b.code).toBe('malformed_upload');
  });

  it('a 200 instead of 201 is a protocol error', async () => {
    expect(
      await caught(client(fake(jsonRes(ARCHIVE, 200))).importArchive('a.archive', new Blob(['x'])))
    ).toBeInstanceOf(BackupProtocolError);
  });
});

describe('restore staging', () => {
  const STAGED = { pending: PENDING, restart_required: true };

  it('posts no body when there are no options, and returns pending plus restart_required', async () => {
    const f = fake(jsonRes(STAGED, 202));
    const out = await client(f).restore(ID);
    expect(out).toEqual(STAGED);
    const c = f.calls[0];
    expect(c.url).toBe(`${BACKUPS_PATH}/${ID}/restore`);
    expect(c.init.method).toBe('POST');
    expect(c.init.body).toBeUndefined();
  });

  it('sends exactly the restore options set, including restart:true', async () => {
    const f = fake(jsonRes(STAGED, 202));
    await client(f).restore(ID, {
      verify: 'entries',
      allow_weak_verify: false,
      foreign: true,
      replace_config: true,
      accept_unreadable_secrets: true,
      restart: true,
      nope: 1,
    } as never);
    const c = f.calls[0];
    expect(c.headers['content-type']).toBe('application/json');
    expect(JSON.parse(c.init.body as string)).toEqual({
      verify: 'entries',
      allow_weak_verify: false,
      foreign: true,
      replace_config: true,
      accept_unreadable_secrets: true,
      restart: true,
    });
  });

  it('a pending restore without warnings still has a warnings array', async () => {
    const { warnings: _w, ...rest } = PENDING;
    const f = fake(jsonRes({ pending: rest, restart_required: true }, 202));
    expect((await client(f).restore(ID)).pending.warnings).toEqual([]);
  });

  it('403 owner_required, foreign_archive_requires_flag, restore_option_not_authorized and a plain scope refusal are distinguished', async () => {
    const run = async (code: string, extra: Record<string, unknown> = {}) =>
      (await caught(
        client(fake(problem(403, code, 'refused', extra))).restore(ID)
      )) as BackupRequestError;
    const flags = (e: BackupRequestError) => [
      e.needsInteractiveSession,
      e.needsOwner,
      e.needsForeignFlag,
    ];
    expect(flags(await run('owner_required'))).toEqual([false, true, false]);
    expect(flags(await run('foreign_archive_requires_flag'))).toEqual([false, false, true]);
    const o = await run('restore_option_not_authorized');
    expect([o.isForbidden, ...flags(o)]).toEqual([true, false, false, false]);
    // core:backup.restore is Sensitive: a non-interactive caller gets permission_denied naming
    // the scope, never interactive_required.
    const d = await run('permission_denied', { required_permission: 'core:backup.restore' });
    expect([d.isForbidden, ...flags(d)]).toEqual([true, false, false, false]);
    expect(d.code).toBe('permission_denied');
    expect(d.requiredPermission).toBe('core:backup.restore');
  });

  it('is not abandoned by the 30 s default deadline, and honours stageTimeoutMs when set', async () => {
    vi.useFakeTimers();
    const pf = pendingFetch();
    const p = createBackupsClient({ fetch: pf.fetch }).restore(ID);
    await vi.advanceTimersByTimeAsync(10 * 60 * 1000);
    expect(pf.signals[0].aborted).toBe(false);
    pf.resolve(jsonRes(STAGED, 202));
    await expect(p).resolves.toEqual(STAGED);

    const hung: typeof fetch = (_u, init) =>
      new Promise((_res, rej) =>
        init?.signal?.addEventListener('abort', () =>
          rej(new DOMException('aborted', 'AbortError'))
        )
      );
    const q = caught(createBackupsClient({ fetch: hung, stageTimeoutMs: 500 }).restore(ID));
    await vi.advanceTimersByTimeAsync(501);
    expect(await q).toBeInstanceOf(ApiTimeoutError);
  });

  it('a refusal is never retried', async () => {
    const f = fake(problem(409, 'swap_pending', 'a swap is already pending'), jsonRes(STAGED, 202));
    const e = (await caught(client(f).restore(ID))) as BackupRequestError;
    expect([e.status, e.code]).toEqual([409, 'swap_pending']);
    expect(f.calls).toHaveLength(1);
  });

  it.each([
    [409, 'archive_verification_failed'],
    [409, 'archive_incompatible'],
    [409, 'unreadable_secrets'],
    [409, 'rename_refused'],
    [409, 'backup_in_progress'],
    [404, 'archive_not_found'],
    [507, 'insufficient_space'],
  ])('%i %s surfaces with its status and code', async (status, code) => {
    const e = (await caught(
      client(fake(problem(status, code, 'refused'))).restore(ID)
    )) as BackupRequestError;
    expect([e.status, e.code]).toEqual([status, code]);
  });

  it('a 200 instead of 202, or an answer without restart_required, is a protocol error', async () => {
    expect(await caught(client(fake(jsonRes(STAGED, 200))).restore(ID))).toBeInstanceOf(
      BackupProtocolError
    );
    expect(
      await caught(client(fake(jsonRes({ pending: PENDING }, 202))).restore(ID))
    ).toBeInstanceOf(BackupProtocolError);
    expect(
      await caught(client(fake(jsonRes({ restart_required: true }, 202))).restore(ID))
    ).toBeInstanceOf(BackupProtocolError);
  });

  it('a bad id never reaches fetch', async () => {
    const f = fake();
    expect(await caught(client(f).restore('a/b'))).toBeInstanceOf(BackupArgumentError);
    expect(f.calls).toHaveLength(0);
  });
});

describe('revert, pending and cancel', () => {
  it('revert posts to /restore/revert and never sends foreign', async () => {
    const f = fake(
      jsonRes({ pending: { ...PENDING, kind: 'revert' }, restart_required: true }, 202)
    );
    const out = await client(f).revert({ restart: true, foreign: true } as never);
    expect(out.pending.kind).toBe('revert');
    expect(f.calls[0].url).toBe(RESTORE_REVERT_PATH);
    expect(JSON.parse(f.calls[0].init.body as string)).toEqual({ restart: true });
  });

  it('revert is not abandoned by the 30 s default deadline, and honours stageTimeoutMs', async () => {
    vi.useFakeTimers();
    const pf = pendingFetch();
    const p = createBackupsClient({ fetch: pf.fetch }).revert();
    await vi.advanceTimersByTimeAsync(10 * 60 * 1000);
    expect(pf.signals[0].aborted).toBe(false);
    const staged = { pending: { ...PENDING, kind: 'revert' }, restart_required: true };
    pf.resolve(jsonRes(staged, 202));
    await expect(p).resolves.toEqual(staged);

    const hung: typeof fetch = (_u, init) =>
      new Promise((_res, rej) =>
        init?.signal?.addEventListener('abort', () =>
          rej(new DOMException('aborted', 'AbortError'))
        )
      );
    const q = caught(createBackupsClient({ fetch: hung, stageTimeoutMs: 500 }).revert());
    await vi.advanceTimersByTimeAsync(501);
    expect(await q).toBeInstanceOf(ApiTimeoutError);
  });

  it('revert 404 no_undo_point is a BackupRequestError', async () => {
    const e = (await caught(
      client(fake(problem(404, 'no_undo_point', 'nothing to undo'))).revert()
    )) as BackupRequestError;
    expect([e.status, e.code]).toEqual([404, 'no_undo_point']);
  });

  it('pending returns the staged restore on 200', async () => {
    const f = fake(jsonRes(PENDING));
    await expect(client(f).pending()).resolves.toEqual(PENDING);
    expect(f.calls[0].url).toBe(RESTORE_PENDING_PATH);
    expect(f.calls[0].init.method).toBe('GET');
  });

  it('pending returns null on 204: nothing staged is not an error', async () => {
    await expect(client(fake(noContent())).pending()).resolves.toBeNull();
  });

  it('pending 403 and a 200 text/html bounce are told apart from "nothing staged"', async () => {
    expect(
      await caught(client(fake(problem(403, 'permission_denied', 'missing scope'))).pending())
    ).toBeInstanceOf(BackupRequestError);
    expect(
      await caught(
        client(
          fake(new Response('<html/>', { status: 200, headers: { 'Content-Type': 'text/html' } }))
        ).pending()
      )
    ).toBeInstanceOf(ApiAuthRedirectError);
  });

  it('cancelRestore sends DELETE /restore/pending and resolves on 204', async () => {
    const f = fake(noContent());
    await expect(client(f).cancelRestore()).resolves.toBeUndefined();
    expect(f.calls[0].url).toBe(RESTORE_PENDING_PATH);
    expect(f.calls[0].init.method).toBe('DELETE');
  });

  it('cancelRestore 403 owner_required surfaces', async () => {
    const e = (await caught(
      client(fake(problem(403, 'owner_required', 'needs the owner'))).cancelRestore()
    )) as BackupRequestError;
    expect(e.needsOwner).toBe(true);
  });
});

describe('parseRetryAfterMs', () => {
  it('reads seconds and HTTP dates, clamps, and refuses junk', () => {
    expect(parseRetryAfterMs('5')).toBe(5000);
    expect(parseRetryAfterMs(null)).toBeNull();
    expect(parseRetryAfterMs('')).toBeNull();
    expect(parseRetryAfterMs('-5')).toBeNull();
    expect(parseRetryAfterMs('999999999')).toBe(MAX_RETRY_AFTER_MS);
    const now = Date.parse('2026-11-20T10:00:00Z');
    expect(parseRetryAfterMs('Fri, 20 Nov 2026 10:00:10 GMT', () => now)).toBe(10000);
    expect(parseRetryAfterMs('Fri, 20 Nov 2026 09:00:00 GMT', () => now)).toBe(0);
  });
});
