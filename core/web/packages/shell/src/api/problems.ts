// file: core/web/packages/shell/src/api/problems.ts
// version: 0.1.0
// guid: bd3bcbac-8047-4c54-a919-0c94e9345979
// last-edited: 2026-11-20

// Turning an openapi-fetch result into a value or a thrown ApiCallError, and an
// error of any kind into the message a view renders. No failure is swallowed:
// every caller shows the message (07 §7.9.1, the four-states rule).

import { isAbortError } from '@mediacore/api-client';
import type { ShellProblem } from './interim';

/** A non-2xx answer, carrying the decoded Problem (api-client decodes every error body into one). */
export class ApiCallError extends Error {
  readonly problem: ShellProblem;
  readonly status: number;

  constructor(problem: ShellProblem, status: number) {
    super(problemMessage(problem));
    this.name = 'ApiCallError';
    this.problem = problem;
    this.status = status;
  }
}

const FALLBACK = 'The request failed.';

export function problemMessage(p: Partial<ShellProblem> | undefined): string {
  return p?.detail || p?.title || FALLBACK;
}

/** The text to show for any thrown value (an ApiCallError, a network TypeError, a timeout). */
export function errorMessage(err: unknown): string {
  if (err instanceof Error && err.message) return err.message;
  return FALLBACK;
}

type Result<D> = { data?: D; error?: unknown; response: Response };

/** The data of a successful call; throws ApiCallError for any other answer. */
export async function unwrap<D>(call: Promise<Result<D>>): Promise<D> {
  const res = await call;
  if (!res.response.ok || res.error !== undefined) {
    throw new ApiCallError((res.error ?? {}) as ShellProblem, res.response.status);
  }
  return res.data as D;
}

export { isAbortError };
