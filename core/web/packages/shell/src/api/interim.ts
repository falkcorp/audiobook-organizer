// file: core/web/packages/shell/src/api/interim.ts
// version: 0.1.0
// guid: 322f5e22-a1aa-473c-805e-588ce0a4a7d3
// last-edited: 2026-11-20

// INTERIM, DELETE ME: hand-typed `paths` for the core auth and settings routes
// the shell calls. core's OpenAPI document (core/openapi/core.json) carries no
// paths yet, so `createCoreClient`'s generated `paths` cannot type them. Once
// F4-08 puts these routes into core.json, regenerate @mediacore/api-client,
// delete this file, and point src/auth, src/settings and the tests at
// `createCoreClient` / `CoreClient`.
//
// The shapes are the Go DTOs of core/internal/authhttp/dto.go and
// core/internal/confighttp/types.go. Path keys carry the full /api/core/v1
// prefix because the client's baseUrl is the origin, and each key is the exact
// string in `shellOperations`, because the runtime matches `public` on it.

import {
  createCoreClient,
  type CoreClientOptions,
  type ModuleClient,
  type OperationInfo,
  type Problem,
} from '@mediacore/api-client';

// ---- DTOs: authhttp ---------------------------------------------------------

export type AuthStatusBody = { has_users: boolean; auth_disabled: boolean; setup_required: boolean };

export type ShellUser = {
  id: string;
  username: string;
  email?: string;
  email_verified: boolean;
  display_name?: string;
  roles: string[];
  status: string;
  has_password: boolean;
  password_reset_required: boolean;
  sign_in_restricted: boolean;
  created_at: string;
  updated_at: string;
};

/** GET /me. Permissions are the concrete scopes the caller's checker holds. */
export type MeBody = { user: ShellUser; permissions: string[]; method: string; owner_verified: boolean };

export type LoginBody = { username: string; password: string; remember?: boolean };
export type SessionOpenedBody = { public_id: string; expires_at: string; user?: ShellUser };
export type SetupBody = { username: string; password: string; email?: string; bootstrap_token?: string };
export type BootstrapBody = { token: string };
export type BootstrapResultBody = {
  key: string;
  key_id: string;
  expires_at: string;
  user: ShellUser;
  generated_password?: string;
};
export type ProvidersBody = { providers: { id: string; kind: string }[] };
export type InviteLookupBody = { token: string };
export type InviteAcceptBody = { token: string; password: string };
export type InvitePreviewBody = { username: string; email?: string; role: string; expires_at: string };
export type TempLoginConsumeBody = { token: string; new_password?: string };

// ---- DTOs: confighttp -------------------------------------------------------

/** The JSON Schema subset the settings renderer reads (07 §7.4.5). */
export interface JsonSchema {
  type?: string | string[];
  title?: string;
  description?: string;
  enum?: unknown[];
  format?: string;
  minimum?: number;
  maximum?: number;
  default?: unknown;
  items?: JsonSchema;
  properties?: Record<string, JsonSchema>;
  writeOnly?: boolean;
  'x-mediacore-secret'?: boolean;
  'x-mediacore-ui'?: { group?: string; order?: number; widget?: string };
  [keyword: string]: unknown;
}

/** GET /settings/schema: one property per section the caller may read, keyed by section path. */
export type SettingsSchemaDoc = { $schema?: string; $id?: string; type: 'object'; properties: Record<string, JsonSchema> };

export type SecretView = { set: boolean; source?: string; updated_at?: string; updated_by?: string; unavailable?: boolean };

export type KeyStateView = {
  path: string;
  section: string;
  schema: JsonSchema;
  /** Absent for a secret key. */
  effective?: unknown;
  /** default, file, env, flag, boot_record or override. */
  source: string;
  locked_by?: string;
  /** live, restart or boot. */
  reload: string;
  protection: string;
  fail_closed?: boolean;
  pending?: boolean;
  quarantined?: boolean;
  secret?: SecretView;
};

export type SettingsViewBody = Record<string, KeyStateView>;
export type SettingsPatchBody = { set?: Record<string, unknown>; unset?: string[] };
export type SettingsPatchResult = {
  generation: number;
  applied: string[];
  restart_pending?: string[];
  post_commit_error?: { section: string; message: string }[];
};

/**
 * core.Problem plus the two members PATCH /settings adds (confighttp's
 * problemBody): `locked_by` on a 409, `need` on a 403.
 */
export type ShellProblem = Problem & { locked_by?: Record<string, string>; need?: Record<string, string> };

// ---- openapi-typescript-shaped paths ----------------------------------------

type HttpMethod = 'get' | 'put' | 'post' | 'delete' | 'options' | 'head' | 'patch' | 'trace';
type NoParams = { query?: never; header?: never; path?: never; cookie?: never };
type Headers = { [name: string]: unknown };
type Success<T> = { headers: Headers; content: { 'application/json': T } };
type NoContent = { headers: Headers; content?: never };
type Failure = { headers: Headers; content: { 'application/problem+json': ShellProblem } };

type ReadOp<Res> = { parameters: NoParams; requestBody?: never; responses: { 200: Success<Res>; default: Failure } };
type WriteOp<Req, Res> = {
  parameters: NoParams;
  requestBody: { content: { 'application/json': Req } };
  responses: { 200: Success<Res>; default: Failure };
};
type NoBodyNoContentOp = { parameters: NoParams; requestBody?: never; responses: { 204: NoContent; default: Failure } };

type Route<M extends HttpMethod, Op> = { parameters: NoParams } & { [K in M]: Op } & { [K in Exclude<HttpMethod, M>]?: never };

export interface ShellPaths {
  '/api/core/v1/auth/status': Route<'get', ReadOp<AuthStatusBody>>;
  '/api/core/v1/me': Route<'get', ReadOp<MeBody>>;
  '/api/core/v1/auth/login': Route<'post', WriteOp<LoginBody, SessionOpenedBody>>;
  '/api/core/v1/auth/logout': Route<'post', NoBodyNoContentOp>;
  '/api/core/v1/auth/setup': Route<'post', WriteOp<SetupBody, ShellUser>>;
  '/api/core/v1/auth/bootstrap': Route<'post', WriteOp<BootstrapBody, BootstrapResultBody>>;
  '/api/core/v1/auth/providers': Route<'get', ReadOp<ProvidersBody>>;
  '/api/core/v1/auth/invites/lookup': Route<'post', WriteOp<InviteLookupBody, InvitePreviewBody>>;
  '/api/core/v1/auth/invites/accept': Route<'post', WriteOp<InviteAcceptBody, SessionOpenedBody>>;
  '/api/core/v1/auth/temp-login/consume': Route<'post', WriteOp<TempLoginConsumeBody, SessionOpenedBody>>;
  '/api/core/v1/settings/schema': Route<'get', ReadOp<SettingsSchemaDoc>>;
  '/api/core/v1/settings': { parameters: NoParams } & { get: ReadOp<SettingsViewBody>; patch: WriteOp<SettingsPatchBody, SettingsPatchResult> } & {
    [K in Exclude<HttpMethod, 'get' | 'patch'>]?: never;
  };
}

const API = '/api/core/v1';

function op(method: 'get' | 'post' | 'patch', path: string, operationId: string, isPublic: boolean): OperationInfo {
  return { method, path: `${API}${path}`, operationId, public: isPublic };
}

/**
 * The operation table for the routes above. `public` marks the routes that
 * need no session, so a 401 on them is shown in place (a wrong password) and
 * never ends the session (07 §7.9.1: login, setup, bootstrap, providers, the
 * invite and temp-login routes, and the status probe).
 */
export const shellOperations: OperationInfo[] = [
  op('get', '/auth/status', 'getAuthStatus', true),
  op('get', '/me', 'getMe', false),
  op('post', '/auth/login', 'login', true),
  op('post', '/auth/logout', 'logout', false),
  op('post', '/auth/setup', 'setupFirstAdmin', true),
  op('post', '/auth/bootstrap', 'redeemBootstrapToken', true),
  op('get', '/auth/providers', 'listAuthProviders', true),
  op('post', '/auth/invites/lookup', 'lookupInvite', true),
  op('post', '/auth/invites/accept', 'acceptInvite', true),
  op('post', '/auth/temp-login/consume', 'consumeTempLogin', true),
  op('get', '/settings/schema', 'getSettingsSchema', false),
  op('get', '/settings', 'getSettings', false),
  op('patch', '/settings', 'updateSettings', false),
];

/** An openapi-fetch client over ShellPaths, with the runtime of api-client. */
export type ShellClient = ModuleClient<ShellPaths>;

type UnauthorizedListener = (problem: Problem) => void;

const unauthorizedListeners = new WeakMap<object, Set<UnauthorizedListener>>();

/**
 * Subscribes to the session-ending 401s of `client` (07 §7.9.1). The runtime
 * takes one `onUnauthorized` at creation, before any provider exists, so
 * createShellClient fans it out to the listeners registered here. A client not
 * made by createShellClient has none: the subscription is then a no-op.
 */
export function onShellUnauthorized(client: ShellClient, listener: UnauthorizedListener): () => void {
  const set = unauthorizedListeners.get(client);
  if (!set) return () => {};
  set.add(listener);
  return () => {
    set.delete(listener);
  };
}

/** createCoreClient with the shell's interim operation table and typed paths. */
export function createShellClient(opts: CoreClientOptions): ShellClient {
  const listeners = new Set<UnauthorizedListener>();
  const core = createCoreClient({
    ...opts,
    operations: shellOperations,
    onUnauthorized: (problem) => {
      opts.onUnauthorized?.(problem);
      for (const l of [...listeners]) l(problem);
    },
  });
  // The runtime's types come from core's (empty) generated paths; the same
  // object is a client over ShellPaths.
  const client = core as unknown as ShellClient;
  unauthorizedListeners.set(client, listeners);
  return client;
}
