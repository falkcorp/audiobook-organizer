// file: web/src/utils/signInRequired.ts
// version: 0.1.0
// guid: e48ec326-6f38-443c-ab40-2863fa2994a0
// last-edited: 2026-11-22

// SEC5-8 (docs/specs/platform-core/05-rbac.md, 05-authn.md section 4.11): with authentication off
// core refuses the auth-disabled principal on interactive and credential routes. The SPA names the
// refusal instead of showing a raw 403. This module is the one rule and the one message; the hook
// (hooks/useSignInRequiredForAction.tsx) and every call site go through it. UI-only: it changes no
// request, route or permission.

import { ApiError } from '../services/apiError';

export const SIGN_IN_REQUIRED_MESSAGE = 'Turn on sign-in to do this';

/**
 * Where an operator turns sign-in on. Settings cannot do it under auth-off (05-authn section 4.11:
 * "set ABO_CORE_AUTH_MODE=required and restart"), so the target is the operator configuration doc
 * (its ENABLE_AUTH row is the nearest existing page; none names core.auth.mode yet).
 */
export const SIGN_IN_HELP_URL =
  'https://github.com/falkcorp/audiobook-organizer/blob/main/docs/configuration.md';

/** The refusal codes of an auth-disabled principal: stage 13's problem+json, and the books credential guard. */
const REFUSAL_CODES = new Set(['interactive_required', 'FORBIDDEN']);

/** Auth is off only when the server said so; unknown (null) and not-yet-loaded count as on. */
export function isAuthOff(authEnabled: boolean | null | undefined, initialized = true): boolean {
  return initialized && authEnabled === false;
}

/** The refusal's code, or '' when the error is not a 403 carrying one. */
function refusalCode(err: unknown): string {
  if (!(err instanceof ApiError) || err.status !== 403) return '';
  const code = (err.data as { code?: unknown } | null | undefined)?.code;
  return typeof code === 'string' ? code : '';
}

/**
 * Whether `err` is the auth-off refusal. `interactive_required` is specific to it; `FORBIDDEN` is a
 * generic books code, so it counts only while auth is off. With auth on a 403 stays an ordinary error.
 */
export function isSignInRequiredRefusal(err: unknown, authOff: boolean): boolean {
  const code = refusalCode(err);
  if (!REFUSAL_CODES.has(code)) return false;
  return code === 'interactive_required' || authOff;
}

/** An ApiError carrying the status and parsed body of a failed books response, for call sites that threw a bare Error. */
export async function apiErrorFromResponse(resp: Response, fallback: string): Promise<ApiError> {
  const data: unknown = await resp.json().catch(() => ({}));
  const message = (data as { error?: unknown } | null)?.error;
  return new ApiError(typeof message === 'string' && message ? message : fallback, resp.status, data);
}
