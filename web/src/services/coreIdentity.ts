// file: web/src/services/coreIdentity.ts
// version: 0.1.0
// guid: 8fd9bad9-c93f-4b8f-89a8-6784ad83471a
// last-edited: 2026-11-22

// ABO's identity calls on core's native routes under /api/core/v1 (F4-08 B8):
// sign-in, sign-up, the caller's own profile, password and API keys, user and
// invite administration, and the three public one-time-link routes. Every call
// goes through @mediacore/api-client's generated client, so the transport
// rules (credentials, the login-page bounce, the stale-build rule, the Problem
// envelope) are the client's, and core answers bare bodies (no `data` envelope).
//
// Nothing here logs, and no one-time token is ever placed in a URL: an invite or
// temp-login token travels in a request body only (07 section 7.4.3).
//
// A failed call throws an ApiError carrying the Problem's detail (or title) as
// its message, the HTTP status, and the Problem as `data`, so the SPA's existing
// error handling (err.status, err.message) reads it unchanged.
//
// api.ts re-exports the account and key functions, so their importers did not
// change; the types they return are api.ts's (AuthUser, APIKey, ...).

import {
  createCoreClient,
  readPageBuildId,
  type CoreClient,
  type Problem,
  type components,
} from '@mediacore/api-client';
import type {
  AdminUserSummary,
  APIKey,
  AuthStatus,
  AuthUser,
  CreateAPIKeyRequest,
  CreateAPIKeyResponse,
  TempLoginTokenResponse,
} from './api';
import { ApiError } from './apiError';

type Schemas = components['schemas'];
type CoreUserBody = Schemas['core.User'];
type CoreKeyBody = Schemas['core.APIKey'];

/** core's list routes cap a page at 1000; every list here asks for the whole of it. */
const PAGE = { limit: 1000 } as const;

let client: CoreClient | undefined;

/** The identity client, built on first use so a test that stubs fetch before its first call is honoured. */
function core(): CoreClient {
  client ??= createCoreClient({ buildId: readPageBuildId(), shell: false });
  return client;
}

/** Drops the cached client; tests only. */
export function resetCoreIdentityClientForTest(): void {
  client = undefined;
}

function problemMessage(problem: Problem | undefined, fallback: string): string {
  const text = problem?.detail?.trim() || problem?.title?.trim();
  return text || fallback;
}

function retryAfterMs(response: Response): number | undefined {
  const seconds = Number(response.headers?.get?.('Retry-After'));
  return Number.isFinite(seconds) && seconds > 0 ? Math.round(seconds * 1000) : undefined;
}

interface Settled<T> {
  data?: T;
  error?: Problem;
  response: Response;
}

/** The body of a successful call, or an ApiError for any other outcome. */
async function body<T>(settled: Promise<Settled<T>>, fallback: string): Promise<T> {
  const { data, error, response } = await settled;
  if (!response.ok || data === undefined) {
    throw new ApiError(problemMessage(error, fallback), response.status, error, retryAfterMs(response));
  }
  return data;
}

/** A call whose success carries no body (204). */
async function done(settled: Promise<Settled<unknown>>, fallback: string): Promise<void> {
  const { error, response } = await settled;
  if (!response.ok) {
    throw new ApiError(problemMessage(error, fallback), response.status, error, retryAfterMs(response));
  }
}

/** An absolute URL for a link core answered relative to the app's origin (`/invite#token=...`). */
export function absoluteLink(link: string): string {
  return new URL(link, window.location.origin).toString();
}

// --- The caller ------------------------------------------------------------

/**
 * Core reports `auth_disabled`, `has_users` and `setup_required`; the SPA's
 * AuthStatus keeps its own words. Core answers 401 to an anonymous caller
 * whenever authentication is on, even before the first account exists, so
 * sign-in is required exactly when auth is not disabled; before the first
 * account the sign-in page then offers setup (`bootstrap_ready`).
 */
export async function getAuthStatus(): Promise<AuthStatus> {
  const s = await body(core().GET('/api/core/v1/auth/status'), 'Failed to fetch auth status');
  return {
    has_users: s.has_users,
    auth_enabled: !s.auth_disabled,
    requires_auth: !s.auth_disabled,
    bootstrap_ready: s.setup_required,
  };
}

function authUserOf(user: CoreUserBody, permissions: readonly string[]): AuthUser {
  return {
    id: user.id,
    username: user.username,
    email: user.email ?? '',
    roles: user.roles ?? [],
    status: user.status,
    created_at: user.created_at,
    permissions: [...permissions],
  };
}

/**
 * The signed-in user with the permissions the server computed for this
 * session (GET /me lists the concrete scopes). A missing list is `[]`, which
 * fails closed.
 */
export async function getMe(): Promise<AuthUser> {
  const me = await body(core().GET('/api/core/v1/me'), 'Failed to fetch current user');
  return authUserOf(me.user, me.permissions ?? []);
}

export async function setupAdmin(payload: {
  username: string;
  password: string;
  email?: string;
  /** Required by core when the request is not from the host itself (the token file's contents). */
  bootstrap_token?: string;
}): Promise<{ user: AuthUser }> {
  const email = payload.email?.trim();
  const token = payload.bootstrap_token?.trim();
  const user = await body(
    core().POST('/api/core/v1/auth/setup', {
      body: { username: payload.username, password: payload.password, ...(email ? { email } : {}),
        ...(token ? { bootstrap_token: token } : {}),
      },
    }),
    'Failed to create admin account'
  );
  return { user: authUserOf(user, []) };
}

/** Signs in, then reads /me for the permissions the new session holds. */
export async function login(payload: {
  username: string;
  password: string;
  remember_me?: boolean;
}): Promise<{ user: AuthUser }> {
  await body(
    core().POST('/api/core/v1/auth/login', {
      body: {
        username: payload.username,
        password: payload.password,
        ...(payload.remember_me ? { remember: true } : {}),
      },
    }),
    'Failed to login'
  );
  return { user: await getMe() };
}

export async function logout(): Promise<void> {
  await done(core().POST('/api/core/v1/auth/logout'), 'Failed to logout');
}

export async function updateMe(payload: { email: string }): Promise<AuthUser> {
  await body(core().PATCH('/api/core/v1/me', { body: { email: payload.email } }), 'Failed to update profile');
  return getMe();
}

export async function changePassword(payload: {
  current_password: string;
  new_password: string;
}): Promise<void> {
  await done(
    core().PUT('/api/core/v1/me/password', {
      body: { current: payload.current_password, new: payload.new_password },
    }),
    'Failed to change password'
  );
}

/** The ids of the enabled single-sign-on providers (empty when none is configured). */
export async function listAuthProviders(): Promise<string[]> {
  const data = await body(core().GET('/api/core/v1/auth/providers'), 'Failed to list sign-in providers');
  return (data.providers ?? []).map((p) => p.id);
}

/** Where the browser goes to start single sign-on with a provider (a navigation, not a fetch). */
export function oidcStartPath(providerId: string): string {
  return `/api/core/v1/auth/oidc/${encodeURIComponent(providerId)}/start`;
}

// --- Users -----------------------------------------------------------------

function adminUserOf(u: CoreUserBody): AdminUserSummary {
  return {
    id: u.id,
    username: u.username,
    email: u.email ?? '',
    status: u.status,
    roles: u.roles ?? [],
    ...(u.merged_into_user_id ? { merged_into_user_id: u.merged_into_user_id } : {}),
  };
}

export async function listAdminUsers(): Promise<AdminUserSummary[]> {
  const page = await body(
    core().GET('/api/core/v1/users', { params: { query: PAGE } }),
    'Failed to list users'
  );
  return (page.items ?? []).map(adminUserOf);
}

/** Disables an account. The server refuses to disable the last admin. */
export async function disableUser(id: string): Promise<void> {
  await body(core().POST('/api/core/v1/users/{id}/disable', { params: { path: { id } } }), 'Failed to deactivate user');
}

/** Enables an account. The server answers 409 `merged_away` for an account that was merged into another. */
export async function enableUser(id: string): Promise<void> {
  await body(core().POST('/api/core/v1/users/{id}/enable', { params: { path: { id } } }), 'Failed to reactivate user');
}

/** A one-time link that lets the user choose a new password. Shown once; the URL is absolute. */
export async function createPasswordResetLink(id: string): Promise<string> {
  const made = await body(
    core().POST('/api/core/v1/users/{id}/reset-password', { params: { path: { id } } }),
    'Failed to reset password'
  );
  return absoluteLink(made.link);
}

/** A one-time link that signs the user in. Shown once. */
export async function createTempLoginToken(userID: string): Promise<TempLoginTokenResponse> {
  const made = await body(
    core().POST('/api/core/v1/users/{id}/temp-login', { params: { path: { id: userID } } }),
    'Failed to mint temp-login link'
  );
  return { login_url: absoluteLink(made.link) };
}

export async function listRoleNames(): Promise<string[]> {
  const page = await body(core().GET('/api/core/v1/roles', { params: { query: PAGE } }), 'Failed to list roles');
  return (page.items ?? []).map((r) => r.name);
}

// --- Invites ---------------------------------------------------------------

export interface PendingInvite {
  id: string;
  username: string;
  email?: string;
  role: string;
  created_by: string;
  created_at: string;
  expires_at: string;
}

export async function listInvites(): Promise<PendingInvite[]> {
  const page = await body(
    core().GET('/api/core/v1/users/invites', { params: { query: PAGE } }),
    'Failed to list invites'
  );
  return page.items ?? [];
}

export interface CreatedInvite {
  /** The absolute one-time link (`/invite#token=...`); shown once, never listed again. */
  link: string;
  invite: PendingInvite;
}

export async function createInvite(input: { username: string; role: string; email?: string }): Promise<CreatedInvite> {
  const email = input.email?.trim();
  const made = await body(
    core().POST('/api/core/v1/users/invites', {
      body: { username: input.username, role: input.role, ...(email ? { email } : {}) },
    }),
    'Failed to create invite'
  );
  return { link: absoluteLink(made.link), invite: made.invite };
}

export async function deleteInvite(id: string): Promise<void> {
  await done(core().DELETE('/api/core/v1/users/invites/{id}', { params: { path: { id } } }), 'Failed to revoke invite');
}

/** What an invitation link is for. The token goes in the body, never a URL. */
export interface InvitePreview {
  username: string;
  email?: string;
  role: string;
  expires_at: string;
  /** Books-side hints core's lookup does not carry; absent today (B10s feeds them). */
  email_login?: boolean;
  phone_app_token?: boolean;
}

/** The one-time phone-app key the claim answers with (a books feature; shown once, kept in state only). */
export interface PhoneAppKey {
  status: 'issued' | 'unavailable';
  client_id?: string;
  client_secret?: string;
  id_header?: string;
  secret_header?: string;
  message?: string;
}

export async function lookupInvite(token: string): Promise<InvitePreview> {
  return body(core().POST('/api/core/v1/auth/invites/lookup', { body: { token } }), 'Could not load the invitation');
}

/**
 * Accepts an invitation and opens its session (the cookie); the page then reloads into the app.
 * Core's answer carries no phone-app key: the guest's key is claimed by a separate call
 * (services/phoneKeyClaim.ts) once this one has succeeded.
 */
export async function acceptInvite(token: string, password: string): Promise<void> {
  await body(
    core().POST('/api/core/v1/auth/invites/accept', { body: { token, password } }),
    'Could not accept the invitation'
  );
}

/** Spends a temp-login link and opens its session (the cookie). */
export async function consumeTempLogin(token: string, newPassword?: string): Promise<void> {
  await body(
    core().POST('/api/core/v1/auth/temp-login/consume', {
      body: { token, ...(newPassword ? { new_password: newPassword } : {}) },
    }),
    'Could not sign in with this link'
  );
}

// --- API keys --------------------------------------------------------------

/** HEAD called a disabled key `inactive`; core calls it `disabled`. The UI keeps its word for it. */
function keyStatusOf(status: string): APIKey['status'] {
  if (status === 'disabled') return 'inactive';
  return status === 'revoked' ? 'revoked' : 'active';
}

function daysSince(iso: string | null | undefined): number | null {
  if (!iso) return null;
  return Math.floor((Date.now() - new Date(iso).getTime()) / 86_400_000);
}

function apiKeyOf(k: CoreKeyBody): APIKey {
  return {
    id: k.id,
    user_id: k.owner,
    name: k.name,
    scopes: k.scopes ?? [],
    status: keyStatusOf(k.status),
    created_at: k.created_at,
    use_count: k.use_count,
    never_used: !k.last_used_at,
    days_since_last_use: daysSince(k.last_used_at),
    // A key has no stored prefix to show; its id is the handle the list and revoke use.
    identifier: k.id,
    ...(k.owner_username ? { username: k.owner_username } : {}),
    ...(k.last_used_at ? { last_used_at: k.last_used_at } : {}),
    ...(k.last_used_ip ? { last_used_ip: k.last_used_ip } : {}),
    ...(k.expires_at ? { expires_at: k.expires_at } : {}),
    ...(k.revoked_at ? { revoked_at: k.revoked_at } : {}),
    ...(k.description ? { description: k.description } : {}),
  };
}

/** The caller's own keys, or (all) every key the caller may see. */
export async function listAPIKeys(all?: boolean): Promise<APIKey[]> {
  const page = await body(
    all
      ? core().GET('/api/core/v1/api-keys', { params: { query: PAGE } })
      : core().GET('/api/core/v1/me/api-keys', { params: { query: PAGE } }),
    'Failed to list API keys'
  );
  return (page.items ?? []).map(apiKeyOf);
}

export async function getAPIKey(id: string): Promise<APIKey> {
  return apiKeyOf(await body(core().GET('/api/core/v1/api-keys/{id}', { params: { path: { id } } }), 'Failed to get API key'));
}

export async function createAPIKey(request: CreateAPIKeyRequest): Promise<CreateAPIKeyResponse> {
  const payload: Schemas['core.KeyCreate'] = {
    name: request.name,
    scopes: request.scopes ?? [],
    ...(request.description ? { description: request.description } : {}),
    ...(request.expires_in_days ? { expires_in: `${request.expires_in_days * 24}h` } : {}),
    ...(request.expires_at ? { expires_at: request.expires_at } : {}),
  };
  const made = await body(
    request.user_id
      ? core().POST('/api/core/v1/users/{id}/api-keys', { params: { path: { id: request.user_id } }, body: payload })
      : core().POST('/api/core/v1/me/api-keys', { body: payload }),
    'Failed to create API key'
  );
  return {
    id: made.api_key.id,
    name: made.api_key.name,
    token: made.key,
    scopes: made.api_key.scopes ?? [],
    created_at: made.api_key.created_at,
    ...(made.api_key.expires_at ? { expires_at: made.api_key.expires_at } : {}),
  };
}

export async function updateAPIKeyStatus(id: string, status: 'active' | 'inactive'): Promise<APIKey> {
  return apiKeyOf(
    await body(
      core().PATCH('/api/core/v1/api-keys/{id}', {
        params: { path: { id } },
        body: { status: status === 'inactive' ? 'disabled' : 'active' },
      }),
      'Failed to update API key status'
    )
  );
}

export async function revokeAPIKey(id: string): Promise<void> {
  await done(core().DELETE('/api/core/v1/api-keys/{id}', { params: { path: { id } } }), 'Failed to revoke API key');
}
