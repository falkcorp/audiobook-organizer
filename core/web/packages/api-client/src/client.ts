// file: core/web/packages/api-client/src/client.ts
// version: 1.0.0
// guid: 311d5d70-cbaa-4cbf-b059-200a34e6047b
// last-edited: 2026-11-08

// createCoreClient and createModuleClient (07 §7.8; 05-http-api §7): an
// openapi-fetch client over the generated `paths`, whose fetch and middleware carry
// today's apiFetch semantics: credentials 'include', an own deadline
// (ApiTimeoutError), SSO login-page bounce detection (ApiAuthRedirectError), the
// `Problem` error envelope, the 401 rule of 07 §7.9 and the one stale-build rule
// (checkBuild). openapi-fetch is pre-1.0 and pinned exact; nothing outside this
// file imports it, so a swap is one package.

import createClient, { type Client, type Middleware } from 'openapi-fetch';
import { checkBuild, type BuildCheckOptions } from './buildId';
import { ApiTimeoutError, assertNotAuthRedirect } from './errors';
import type { paths } from './generated/core';
import { coreOperations } from './generated/operations';
import { operationKey, publicOperationSet, type OperationInfo } from './operationInfo';
import { decodeProblem, isProblemResponse, type Problem } from './problem';

/**
 * The problem codes of a 401 that end the session (07 §7.9, FE-7). A 401 with any
 * other code, or on a `skipAuthRedirect` call, never reaches `onUnauthorized`.
 */
export const SESSION_ENDING_CODES: ReadonlySet<string> = new Set([
  'authentication_required',
  'expired',
  'revoked',
  'inactive_user',
  'invalid_credential',
]);

export interface CoreClientOptions extends BuildCheckOptions {
  /**
   * Prefix of every path. The page's origin by default (a relative URL is a valid
   * `Request` in a browser but not under jsdom or Node), `""` with no `location`.
   */
  baseUrl?: string;
  /** Abort a request after this many ms with ApiTimeoutError. Absent or <= 0: no deadline. */
  timeoutMs?: number;
  /** The fetch to use; `globalThis.fetch`, looked up per call, by default. */
  fetch?: typeof globalThis.fetch;
  /** The operation table the client reads `public` from; core's generated one by default. */
  operations?: readonly OperationInfo[];
  /**
   * Runs for a 401 whose problem `code` ends the session, on a call that is not
   * `skipAuthRedirect` (07 §7.9). The shell resets auth state and navigates here.
   */
  onUnauthorized?: (problem: Problem) => void;
}

/** What a client was created with; a module client inherits it from its core client. */
export interface ClientRuntime {
  readonly buildId: string;
  readonly shell: boolean;
  readonly baseUrl: string;
  readonly timeoutMs: number | undefined;
  readonly fetch: typeof globalThis.fetch;
  readonly onUnauthorized: ((problem: Problem) => void) | undefined;
  /** True when the operation's `x-mediacore-permission` is `public` (07 §7.9). */
  skipAuthRedirect(method: string, path: string): boolean;
}

export type CoreClient = Client<paths> & { readonly runtime: ClientRuntime };

export type ModuleClient<Paths extends object> = Client<Paths> & { readonly runtime: ClientRuntime };

/** Fetch with the runtime's deadline and bounce detection (ported from apiFetch). */
function runtimeFetch(rt: Pick<ClientRuntime, 'fetch' | 'timeoutMs'>): typeof globalThis.fetch {
  return async (input, init) => {
    const request = input instanceof Request && init === undefined ? input : new Request(input, init);
    const { timeoutMs } = rt;
    if (!timeoutMs || timeoutMs <= 0) {
      const response = await rt.fetch(request);
      assertNotAuthRedirect(request.url, response);
      return response;
    }

    const controller = new AbortController();
    let timedOut = false;
    const timer = setTimeout(() => {
      timedOut = true;
      controller.abort();
    }, timeoutMs);
    const forwardAbort = () => controller.abort();
    const callerSignal = request.signal;
    if (callerSignal.aborted) {
      controller.abort();
    } else {
      callerSignal.addEventListener('abort', forwardAbort);
    }
    try {
      const response = await rt.fetch(new Request(request, { signal: controller.signal }));
      assertNotAuthRedirect(request.url, response);
      return response;
    } catch (err) {
      // "We gave up" is a failure to report; "the caller gave up" is control flow.
      if (timedOut) throw new ApiTimeoutError(request.url, timeoutMs);
      throw err;
    } finally {
      clearTimeout(timer);
      callerSignal.removeEventListener('abort', forwardAbort);
    }
  };
}

function middleware(rt: ClientRuntime, isPublic: ReadonlySet<string>): Middleware {
  return {
    async onResponse({ request, response, schemaPath }) {
      checkBuild(response, { buildId: rt.buildId, shell: rt.shell });
      if (response.ok) return undefined;

      const problem = await decodeProblem(response.clone());
      if (
        response.status === 401 &&
        SESSION_ENDING_CODES.has(problem.code) &&
        !isPublic.has(operationKey(request.method, schemaPath))
      ) {
        rt.onUnauthorized?.(problem);
      }
      // A native answer already is a Problem; a legacy envelope or a proxy's page is
      // handed on as one, so the error of every call has the one shape.
      if (isProblemResponse(response)) return undefined;
      const headers = new Headers(response.headers);
      headers.set('Content-Type', 'application/problem+json');
      headers.delete('Content-Length');
      headers.delete('Content-Encoding');
      return new Response(JSON.stringify(problem), {
        status: response.status,
        statusText: response.statusText,
        headers,
      });
    },
  };
}

function build<P extends object>(rt: ClientRuntime, operations: readonly OperationInfo[]): ModuleClient<P> {
  const client = createClient<P>({
    baseUrl: rt.baseUrl,
    credentials: 'include',
    fetch: runtimeFetch(rt),
  });
  client.use(middleware(rt, publicOperationSet(operations)));
  return Object.assign(client, { runtime: rt });
}

function runtimeOf(opts: CoreClientOptions, operations: readonly OperationInfo[]): ClientRuntime {
  const isPublic = publicOperationSet(operations);
  return {
    buildId: opts.buildId,
    shell: opts.shell,
    baseUrl: opts.baseUrl ?? (typeof location !== 'undefined' ? location.origin : ''),
    timeoutMs: opts.timeoutMs,
    fetch: opts.fetch ?? ((...args) => globalThis.fetch(...args)),
    onUnauthorized: opts.onUnauthorized,
    skipAuthRedirect: (method, path) => isPublic.has(operationKey(method, path)),
  };
}

/**
 * The client for core's `/api/core/v1` routes. `buildId` and `shell` are both
 * required: the page's `readPageBuildId()` and whether the caller is the shell
 * (07 §7.5.2). ABO's pre-shell bundle creates one with `shell: false`.
 */
export function createCoreClient(opts: CoreClientOptions): CoreClient {
  const operations = opts.operations ?? coreOperations;
  return build<paths>(runtimeOf(opts, operations), operations) as CoreClient;
}

/**
 * A module's client over its own generated `Paths` (07 §7.8, FE-28), on the same
 * runtime: it inherits the core client's `buildId`, `shell`, deadline, fetch and
 * `onUnauthorized`. `operations` is the module's generated table, for its
 * `public` operations.
 */
export function createModuleClient<Paths extends object>(
  core: CoreClient,
  opts: { operations?: readonly OperationInfo[] } = {},
): ModuleClient<Paths> {
  const operations = opts.operations ?? [];
  const isPublic = publicOperationSet(operations);
  const rt: ClientRuntime = {
    ...core.runtime,
    skipAuthRedirect: (method, path) => isPublic.has(operationKey(method, path)),
  };
  return build<Paths>(rt, operations);
}
