// file: web/src/services/coreBackups/types.ts
// version: 0.1.0
// guid: 89abf6cb-5106-4785-9523-8eaaed3f772c
// last-edited: 2026-11-20

// Types of the core backups client: the wire shapes of /api/core/v1/backups* and
// /api/core/v1/restore/* (05-backup 5B.3.8) and the errors the client raises.
//
// UNWIRED: nothing in the running app imports this module until the F8-04a flip.
//
// HAND-WRITTEN, TO BE REPLACED: core/openapi/core.json has no backup path yet
// (backuphttp is not mounted), so there is no generated type to use. Every wire shape
// below is copied from core/internal/backuphttp/errors.go (archiveDTO, verifyDTO,
// pendingDTO, listResponse, createRequest, createResponse, restoreRequest,
// restoreResponse). When F8-04a regenerates core.json, replace these with
// `components['schemas'][...]` from @mediacore/api-client and delete the copies.

import type { Problem } from '@mediacore/api-client';

/** Verification depth, as parseLevel reads it (core/internal/backuphttp/errors.go). */
export type BackupVerifyLevel = 'checksum' | 'entries' | 'open';

/** Where the archive came from (backup.Provenance.String); open set, the server may add values. */
export type BackupProvenance = 'own' | 'foreign' | (string & {});

/** One verification result (verifyDTO). `reason` is a closed code, never error text. */
export interface BackupVerification {
  archive_id?: string;
  /** RFC 3339. */
  at: string;
  level: BackupVerifyLevel | (string & {});
  ok: boolean;
  reason?: string;
}

/** One catalogue entry (archiveDTO). `name` is a plain file name, never a path. */
export interface BackupArchive {
  id: string;
  name: string;
  size: number;
  /** RFC 3339. */
  created_at: string;
  format: string;
  provenance: BackupProvenance;
  reason?: string;
  app_version?: string;
  core_version?: string;
  verified?: BackupVerification;
}

/** GET /backups (listResponse). */
export interface BackupList {
  backups: BackupArchive[];
}

/** POST /backups body (createRequest). Every member is optional; the reason defaults to "manual". */
export interface BackupCreateRequest {
  reason?: string;
  include_derived?: boolean;
  include_recreatable?: boolean;
}

/** The 202 of POST /backups (createResponse): the task that now runs. Follow it on coreEvents. */
export interface BackupTaskStarted {
  task_id: string;
}

/** What kind of restore is staged (kindName). */
export type PendingRestoreKind =
  'archive' | 'revert' | 'checkpoint' | 'external' | 'invalid' | (string & {});

/** The staged restore (pendingDTO). */
export interface PendingRestore {
  archive_id: string;
  kind: PendingRestoreKind;
  staged_at: string;
  expires_at: string;
  staged_by_build?: string;
  requested_by?: string;
  warnings: string[];
}

/**
 * The restore body (restoreRequest). `verify` defaults to "open" on the server.
 * `foreign` needs core:admin as well as the restore scope (403 otherwise).
 * `restart` asks the service to restart once the restore is staged (the drain that
 * broadcasts the shutdown frame to every connected page); without it the caller owes
 * the restart, which `restart_required` reports.
 */
export interface BackupRestoreRequest {
  verify?: BackupVerifyLevel;
  allow_weak_verify?: boolean;
  foreign?: boolean;
  replace_config?: boolean;
  accept_unreadable_secrets?: boolean;
  restart?: boolean;
}

/** The revert body is the restore body without `foreign`, which the server ignores. */
export type BackupRevertRequest = Omit<BackupRestoreRequest, 'foreign'>;

/** The 202 of a restore or revert (restoreResponse). The swap happens at the next start. */
export interface BackupRestoreStaged {
  pending: PendingRestore;
  restart_required: boolean;
}

/**
 * Codes the backup routes answer with, from mapError and the route guards
 * (core/internal/backuphttp). Open on purpose: an unknown code is still an error with a
 * status and a request id.
 */
export const BACKUP_REFUSAL_CODES = [
  'archive_not_found',
  'archive_verification_failed',
  'archive_incompatible',
  'archive_has_no_checksum',
  'unreadable_secrets',
  'swap_pending',
  'rename_refused',
  'backup_in_progress',
  'protected_by_floor',
  'foreign_archive_requires_flag',
  'no_undo_point',
  'insufficient_space',
  'invalid_name',
  'body_too_large',
  'malformed_upload',
  'file_required',
] as const;
export type BackupRefusalCode = (typeof BACKUP_REFUSAL_CODES)[number];

/** One entry of a problem's `errors` array: {field, code, message} (core.Problem). */
export type BackupFieldError = NonNullable<Problem['errors']>[number];

/**
 * A non-2xx answer, decoded. `code` is the machine code (`authentication_required`,
 * `interactive_required`, `owner_required`, `forbidden`, `archive_not_found`, ...);
 * `legacy_error` when the answer was not problem+json. Carries no request body, header
 * or cookie.
 */
export class BackupRequestError extends Error {
  readonly status: number;
  readonly code: string;
  readonly detail: string;
  readonly requestId: string;
  /** Retry-After in ms, when the server sent one (a 503 does), else null. */
  readonly retryAfterMs: number | null;
  /** The scope a 403 names, when the server names one. */
  readonly requiredPermission: string | undefined;
  readonly fields: readonly BackupFieldError[];

  constructor(init: {
    operation: string;
    status: number;
    code: string;
    detail: string;
    requestId: string;
    retryAfterMs: number | null;
    requiredPermission?: string;
    fields?: readonly BackupFieldError[];
  }) {
    super(
      `backups ${init.operation} refused: ${init.status} ${init.code}${init.detail ? ` (${init.detail})` : ''}`
    );
    this.name = 'BackupRequestError';
    this.status = init.status;
    this.code = init.code;
    this.detail = init.detail;
    this.requestId = init.requestId;
    this.retryAfterMs = init.retryAfterMs;
    this.requiredPermission = init.requiredPermission;
    this.fields = init.fields ?? [];
  }

  /** Any 403: the caller lacks a scope, an interactive session or the owner assurance. */
  get isForbidden(): boolean {
    return this.status === 403;
  }

  /** The session is not interactive enough (delete, import, download, restore need one). */
  get needsInteractiveSession(): boolean {
    return this.status === 403 && this.code === 'interactive_required';
  }

  /** Download and restore need the deployment owner while core.auth.owner.required is set. */
  get needsOwner(): boolean {
    return this.status === 403 && this.code === 'owner_required';
  }

  /** A foreign archive restored without `foreign: true`. */
  get needsForeignFlag(): boolean {
    return this.status === 403 && this.code === 'foreign_archive_requires_flag';
  }

  /** The 503 of a collaborator that is not wired yet; retry after `retryAfterMs`. */
  get isUnavailable(): boolean {
    return this.status === 503;
  }
}

/** A 2xx answer that is not the documented shape (wrong content type or status, missing member). */
export class BackupProtocolError extends Error {
  readonly operation: string;
  constructor(operation: string, message: string) {
    super(`backups ${operation}: ${message}`);
    this.name = 'BackupProtocolError';
    this.operation = operation;
  }
}

/** A request the client refused to send: an id or name the server would refuse. */
export class BackupArgumentError extends Error {
  constructor(message: string) {
    super(`backups: ${message}`);
    this.name = 'BackupArgumentError';
  }
}
