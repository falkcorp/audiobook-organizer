// file: web/src/utils/setupErrorMessage.ts
// version: 0.1.0
// guid: d1f2387e-cfa3-4aa2-9adb-424c80a08f63
// last-edited: 2026-11-22

// What the first-run setup forms (pages/Login.tsx setup mode, pages/Setup.tsx) say when core's
// POST /api/core/v1/auth/setup refuses (core/internal/authhttp/errors.go). Two refusals are told
// apart by their machine code, never by status: a 401 here is a wrong token, not "log in first".

import { ApiError } from '../services/apiError';

/** 401 bootstrap_invalid: a token was sent (or required) and it is wrong, expired or already used. */
export const BOOTSTRAP_INVALID_MESSAGE =
  'The bootstrap token is wrong, expired or already used. Get the current token from the server and try again.';

/** 403 setup_closed: core answers the same whether an account exists or this caller may not run setup. */
export const SETUP_CLOSED_MESSAGE =
  'Setup is not available. An administrator account may already exist, or setup cannot run from this machine without the bootstrap token. Try signing in.';

/** The machine code of a core Problem carried by an ApiError, or ''. */
function problemCode(err: unknown): string {
  if (!(err instanceof ApiError)) return '';
  const code = (err.data as { code?: unknown } | null | undefined)?.code;
  return typeof code === 'string' ? code : '';
}

/** The text to show for a failed setup call: the two setup codes by their own messages, else the server's text. */
export function setupErrorMessage(err: unknown, fallback: string): string {
  switch (problemCode(err)) {
    case 'bootstrap_invalid':
      return BOOTSTRAP_INVALID_MESSAGE;
    case 'setup_closed':
      return SETUP_CLOSED_MESSAGE;
    default:
      return err instanceof Error && err.message ? err.message : fallback;
  }
}
