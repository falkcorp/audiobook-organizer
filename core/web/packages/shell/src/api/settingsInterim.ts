// file: core/web/packages/shell/src/api/settingsInterim.ts
// version: 0.1.0
// guid: 6d0e41a2-8c37-4b95-a1f4-27b3e9c5d810
// last-edited: 2026-11-22

// INTERIM, DELETE ME: the hand-typed shapes of the two settings routes, which
// core.json types too loosely for the settings renderer. Its SettingsView leaves
// `schema` and `effective` as `true` (any), GET /settings/schema answers a bare
// `Value`, and core.Problem has no `locked_by` (a 409) or `need` (a 403) member
// although PATCH /settings sends them. Once core.json types those, regenerate
// @mediacore/api-client, delete this file, and read the generated shapes.
// The auth routes this file used to hold are gone: they come from the generated
// `paths` (api/client.ts).
//
// The shapes are the Go DTOs of core/internal/confighttp/types.go. Path keys
// carry the full /api/core/v1 prefix because the client's baseUrl is the origin.

import type { Problem } from '@mediacore/api-client';

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
type Failure = { headers: Headers; content: { 'application/problem+json': ShellProblem } };

type ReadOp<Res> = { parameters: NoParams; requestBody?: never; responses: { 200: Success<Res>; default: Failure } };
type WriteOp<Req, Res> = {
  parameters: NoParams;
  requestBody: { content: { 'application/json': Req } };
  responses: { 200: Success<Res>; default: Failure };
};

type Route<M extends HttpMethod, Op> = { parameters: NoParams } & { [K in M]: Op } & { [K in Exclude<HttpMethod, M>]?: never };

export interface SettingsPaths {
  '/api/core/v1/settings/schema': Route<'get', ReadOp<SettingsSchemaDoc>>;
  '/api/core/v1/settings': { parameters: NoParams } & { get: ReadOp<SettingsViewBody>; patch: WriteOp<SettingsPatchBody, SettingsPatchResult> } & {
    [K in Exclude<HttpMethod, 'get' | 'patch'>]?: never;
  };
}
