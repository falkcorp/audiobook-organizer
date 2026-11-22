// file: web/src/services/apiError.ts
// version: 0.1.0
// guid: 4be03955-0d0a-4adc-af8a-e5faf1faca12
// last-edited: 2026-11-22

// ApiError lives in its own module so services/coreIdentity.ts can throw it
// without importing api.ts (which re-exports coreIdentity's functions): the
// import graph stays acyclic. api.ts re-exports it, so no importer changes.

export class ApiError extends Error {
  readonly status: number;
  readonly data?: unknown;
  /** The response's Retry-After (a count of seconds), in ms; unset when absent or not a count. */
  readonly retryAfterMs?: number;

  constructor(message: string, status: number, data?: unknown, retryAfterMs?: number) {
    super(message);
    this.name = 'ApiError';
    this.status = status;
    this.data = data;
    this.retryAfterMs = retryAfterMs;
  }
}
