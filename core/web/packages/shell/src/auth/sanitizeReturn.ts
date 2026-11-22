// file: core/web/packages/shell/src/auth/sanitizeReturn.ts
// version: 0.1.0
// guid: 29c61762-fed7-4c8b-8e76-290268487771
// last-edited: 2026-11-22

/**
 * sanitizeReturn only allows a same-site absolute path (prevents open-redirect).
 *
 * This is a direct TypeScript port of `sanitizeReturn` in
 * `internal/server/handlers/oauth_login.go` — the client-side navigation
 * sinks this guards (a login page's `location.state.from`, a detail page's
 * saved return URL in `sessionStorage`) previously trusted values with no
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
 *
 * This is NOT `safeReturn` (./safeReturn.ts): that one answers "/" on rejection,
 * also rejects control characters and returns the URL-normalized path. The two
 * are kept apart because their reject values and accepted sets differ; see
 * docs/agent-tasks/platform-core/seams.d/F9-05-leaves.md.
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
