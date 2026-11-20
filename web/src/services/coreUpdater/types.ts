// file: web/src/services/coreUpdater/types.ts
// version: 0.1.0
// guid: fcbf7dd7-bffd-4943-9e14-be1d472aaf51
// last-edited: 2026-11-20

// Types of the core updater client: the wire shapes of GET/POST /api/core/v1/updater*
// (05-updater 5U.10) and the errors the client raises.
//
// UNWIRED: nothing in the running app imports this module until the F8-04b flip.
//
// HAND-WRITTEN, TO BE REPLACED: core/openapi/core.json has no updater path yet
// (updaterhttp is not mounted), so there is no generated type to use. Every wire shape
// below is copied from core/updater/tasks.go (StatusView) and
// core/internal/updaterhttp/routes.go (startResponse, applyRequest, mapPrepare). When
// F8-04b regenerates core.json, replace these with `components['schemas'][...]` from
// @mediacore/api-client and delete the hand-written copies.
//
// The release asset is never named here. The server picks it (ABO: goreleaser's raw
// binary name, internal/updater.FetchOptions()); apply takes a release tag only.

import type { Problem } from '@mediacore/api-client';

/** core.updater.mode as StatusView serves it (core/updater/types.go Mode.String). */
export type UpdaterMode = 'off' | 'check' | (string & {});

/** Release channel (05-updater 5U.3). */
export type UpdaterChannel = 'stable' | 'prerelease' | (string & {});

/** Why the updater cannot act (05-updater 5U.3 Status.DisabledReason). */
export type UpdaterDisabledReason = 'off' | 'dev_build' | 'binary_not_writable' | (string & {});

/** GET /api/core/v1/updater: core/updater.StatusView. Omitted members are absent, not null. */
export interface UpdaterStatus {
  mode: UpdaterMode;
  channel: UpdaterChannel;
  current: string;
  latest?: string;
  available: boolean;
  /** RFC 3339 time of the last completed check. */
  last_checked?: string;
  /** A fixed token, never free text. */
  last_error?: string;
  /** Version of the kept previous binary: the manual rollback target. */
  previous_kept?: string;
  disabled_reason?: UpdaterDisabledReason;
  local_build?: boolean;
  local_build_note?: string;
  auto_apply_paused?: boolean;
}

/** The 202 of check, apply and rollback: the task that now runs. */
export interface UpdaterTaskStarted {
  task_id: string;
}

/** The apply body: a release tag such as v1.2.3. Absent means the release the last check offered. */
export interface UpdaterApplyRequest {
  version?: string;
}

/**
 * The 409 codes apply answers before a task exists (updaterhttp mapPrepare). A malformed
 * tag is a 422 whose top-level code is `validation_failed`; `invalid_version` appears only
 * as a field error's code (see INVALID_VERSION_FIELD_CODE).
 */
export const APPLY_REFUSAL_CODES = [
  'updater_off',
  'unversioned_build',
  'local_build_needs_version',
  'no_update',
  'not_newer',
] as const;
/** The field-error code of a malformed apply tag, inside a 422 validation_failed's errors[]. */
export const INVALID_VERSION_FIELD_CODE = 'invalid_version';
export type ApplyRefusalCode = (typeof APPLY_REFUSAL_CODES)[number];

/** One entry of a problem's `errors` array: {field, code, message} (core.Problem). */
export type UpdaterFieldError = NonNullable<Problem['errors']>[number];

/**
 * A non-2xx answer, decoded. `code` is the machine code (`authentication_required`,
 * `interactive_required`, `updater_off`, ...); `legacy_error` when the answer was not
 * problem+json. Carries no request body, header or cookie.
 */
export class UpdaterRequestError extends Error {
  readonly status: number;
  readonly code: string;
  readonly detail: string;
  readonly requestId: string;
  /** Retry-After in ms, when the server sent one (a 503 does), else null. */
  readonly retryAfterMs: number | null;
  readonly requiredPermission: string | undefined;
  readonly fields: readonly UpdaterFieldError[];

  constructor(init: {
    operation: string;
    status: number;
    code: string;
    detail: string;
    requestId: string;
    retryAfterMs: number | null;
    requiredPermission?: string;
    fields?: readonly UpdaterFieldError[];
  }) {
    super(`updater ${init.operation} refused: ${init.status} ${init.code}${init.detail ? ` (${init.detail})` : ''}`);
    this.name = 'UpdaterRequestError';
    this.status = init.status;
    this.code = init.code;
    this.detail = init.detail;
    this.requestId = init.requestId;
    this.retryAfterMs = init.retryAfterMs;
    this.requiredPermission = init.requiredPermission;
    this.fields = init.fields ?? [];
  }

  /** The session is not interactive enough for apply and rollback (core:admin needs one). */
  get needsInteractiveSession(): boolean {
    return this.status === 403 && this.code === 'interactive_required';
  }

  /** An apply the server refused before starting a task (the 409s and the 422). */
  get isApplyRefusal(): boolean {
    if (this.status === 422) return this.fields.some((f) => f.code === INVALID_VERSION_FIELD_CODE);
    return this.status === 409 && (APPLY_REFUSAL_CODES as readonly string[]).includes(this.code);
  }
}

/** A 2xx answer that is not the documented shape (wrong content type, missing member). */
export class UpdaterProtocolError extends Error {
  readonly operation: string;
  constructor(operation: string, message: string) {
    super(`updater ${operation}: ${message}`);
    this.name = 'UpdaterProtocolError';
    this.operation = operation;
  }
}
