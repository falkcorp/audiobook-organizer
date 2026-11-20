// file: core/web/packages/shell/src/contracts.ts
// version: 0.1.0
// guid: 8e2a5c71-3f09-4d64-a8b7-0c6e1d9f4a25
// last-edited: 2026-11-20

// The state shapes shared between the shell's providers, its hooks and
// @mediacore/shell/testing (07 §7.4.2, §7.4.4, §7.9). Kept in one file so the
// real providers and the test doubles implement the same contract.

import type { Permission } from './types';

/** The signed-in user, as GET /api/core/v1/me returns it (core/internal/authhttp/me.go getMe). */
export interface AuthUser {
  id: string;
  username: string;
  display_name?: string;
  email?: string;
  [extra: string]: unknown;
}

export type AuthStatus = 'loading' | 'anonymous' | 'authenticated' | 'error';

/** useAuth()'s value. `error` holds the message of a failed probe; it is never swallowed. */
export interface AuthState {
  status: AuthStatus;
  user: AuthUser | null;
  permissions: readonly Permission[];
  method: string | null;
  ownerVerified: boolean;
  /** GET /auth/status: no user exists yet, so /setup is the only way in. */
  setupRequired: boolean;
  authDisabled: boolean;
  error: string | null;
  /** Re-probes /auth/status and /me. */
  refresh(): Promise<void>;
  /** POST /auth/logout, then anonymous; the caller navigates to /login. */
  logout(): Promise<void>;
}

// Realtime (07 §7.9.2; the control vocabulary is 05-realtime §4.2's, FE8-9).

export interface RealtimeEvent {
  topic: string;
  key?: string;
  data: unknown;
}
export type ResetReason = 'restart' | 'gap' | 'overflow';
export type CloseReason = 'shutdown' | 'credential_revoked' | 'permission_revoked';

export interface RealtimeListener {
  onEvent(e: RealtimeEvent): void;
  /** A core.stream.reset.v1 frame, or a reconnect that could not resume: refetch everything. */
  onReset(reason: ResetReason): void;
  /** A core.stream.closed.v1 frame. */
  onClosed(reason: CloseReason): void;
  /** A transport failure (not a control frame); the client is retrying. */
  onError?(status?: number): void;
}

/**
 * What RealtimeProvider consumes: the real client (one SSE stream per
 * browser, shared across tabs) or @mediacore/shell/testing's FakeRealtime.
 */
export interface RealtimeSource {
  subscribe(listener: RealtimeListener): () => void;
  /** The topics this tab currently has subscribers for. */
  setTopics(topics: readonly string[]): void;
  start(): void;
  stop(): void;
}

// Toasts.

export type ToastSeverity = 'success' | 'info' | 'warning' | 'error';
export interface ToastApi {
  show(message: string, severity?: ToastSeverity): void;
  success(message: string): void;
  error(message: string): void;
}

// Tasks (05-tasks TaskRunView, hand-typed until core.json carries it).

export type TaskRunState = 'queued' | 'running' | 'succeeded' | 'failed' | 'cancelled' | 'interrupted';
export interface TaskRunView {
  id: string;
  kind: string;
  state: TaskRunState;
  progress?: { done: number; total: number };
  message?: string;
  error?: string;
  started_at?: string;
  finished_at?: string;
  [extra: string]: unknown;
}

/** The terminal states: a run in one of them never changes again. */
export const TERMINAL_TASK_STATES: ReadonlySet<TaskRunState> = new Set(['succeeded', 'failed', 'cancelled', 'interrupted']);
