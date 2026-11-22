// file: core/web/packages/shell/src/auth/safeReturn.ts
// version: 0.1.0
// guid: d3a74e47-d146-4933-95a8-0971ee4efb79
// last-edited: 2026-11-20

/** A return path longer than this is refused, not truncated (core's oidc.SanitizeReturn uses the same bound). */
const MAX_RETURN_LEN = 512;

// eslint-disable-next-line no-control-regex
const UNSAFE = /[\\\u0000-\u001f\u007f]/;

/**
 * The one client-side return-path check. A same-origin absolute path (with its
 * query and fragment) comes back normalized; anything else comes back as "",
 * so the caller falls back to its own default. Same rules as core's Go
 * `oidc.SanitizeReturn` (core/auth/oidc/returnpath.go), checked against the same
 * payload table (core/auth/oidc/testdata/return_payloads.json):
 *
 * - a single leading "/" (no scheme, no host, no "//host"), at most 512 chars;
 * - no backslash ("/\host" is "//host" to a browser) and no control character,
 *   anywhere in the raw string;
 * - resolved against the page origin, the path stays same-origin and does not
 *   start with "//": dot segments ("/.//host", "/..//host", "/%2e//host")
 *   collapse to "//host", which a browser navigating to it reads as
 *   scheme-relative, so checking the raw input alone is a parser differential;
 * - the path percent-decoded once has no backslash or control character, and
 *   resolved again it still stays same-origin and does not start with "//"
 *   ("/./%2F/host", "/%2F/host", "/%252e%252e//host" and "/%5chost" are
 *   refused; a malformed escape is refused too).
 */
export function sanitizeReturn(raw: string | null | undefined): string {
  if (!raw || raw.length > MAX_RETURN_LEN || !raw.startsWith('/') || raw.startsWith('//')) return '';
  if (UNSAFE.test(raw)) return '';
  const origin = typeof window !== 'undefined' ? window.location.origin : 'http://localhost';
  try {
    const url = new URL(raw, origin);
    if (url.origin !== origin || url.pathname.startsWith('//')) return '';
    const decoded = decodeURIComponent(url.pathname);
    if (UNSAFE.test(decoded)) return '';
    const again = new URL(decoded, origin);
    if (again.origin !== origin || again.pathname.startsWith('//')) return '';
    return url.pathname + url.search + url.hash;
  } catch {
    return '';
  }
}

/** The post-login destination from `?return=`: `sanitizeReturn`'s answer, else "/". */
export function safeReturn(raw: string | null): string {
  return sanitizeReturn(raw) || '/';
}

/** `/login?return=<path>` for a location the user was refused at; no `return` for the home page. */
export function loginPath(where: { pathname: string; search: string; hash: string }): string {
  const target = safeReturn(where.pathname + where.search + where.hash);
  return target === '/' ? '/login' : `/login?return=${encodeURIComponent(target)}`;
}
