// file: web/src/utils/safeReturn.ts
// version: 1.0.0
// guid: 70ff8235-119e-469c-a002-49c22d5e341f
// last-edited: 2026-08-21

/**
 * sanitizeReturn only allows a same-site absolute path (prevents open-redirect).
 *
 * This is a direct TypeScript port of `sanitizeReturn` in
 * `internal/server/handlers/oauth_login.go` — the two client-side navigation
 * sinks this guards (Login.tsx's `location.state.from`, BookDetail.tsx's
 * `sessionStorage['library_return_url']`) previously trusted values with no
 * validation at all, unlike every server-side redirect target which already
 * runs through the Go sanitizeReturn. Keep the two implementations in sync;
 * do not add checks here that the Go side does not also enforce (or vice
 * versa) without updating both.
 *
 * The return value must be a single leading slash: reject "//host" and
 * "/\host" (browsers normalize backslashes to slashes, so "/\evil.com" is
 * protocol-relative to evil.com), and any path containing a backslash.
 * Anything rejected — including a null/undefined/empty input, which
 * `sessionStorage.getItem` and an absent `location.state.from` both produce —
 * returns "" so the caller can fall back to a known-safe default.
 */
export function sanitizeReturn(ret: string | null | undefined): string {
  if (!ret || !ret.startsWith('/')) {
    return '';
  }
  if (ret.includes('\\')) {
    return '';
  }
  if (ret.length > 1 && (ret[1] === '/' || ret[1] === '\\')) {
    return '';
  }
  return ret;
}
