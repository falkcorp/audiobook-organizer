// file: core/web/packages/shell/src/auth/safeReturn.ts
// version: 0.1.0
// guid: d3a74e47-d146-4933-95a8-0971ee4efb79
// last-edited: 2026-11-20

/**
 * The post-login destination from `?return=`: a same-origin absolute path
 * (with its query and fragment), else "/". Rejects scheme-relative URLs
 * ("//host"), absolute URLs, backslash tricks ("/\host") and control characters,
 * then confirms by resolving against the page origin that the result stays on it.
 */
export function safeReturn(raw: string | null): string {
  if (!raw || !raw.startsWith('/') || raw.startsWith('//')) return '/';
  // eslint-disable-next-line no-control-regex
  if (/[\\\u0000-\u001f\u007f]/.test(raw)) return '/';
  const origin = typeof window !== 'undefined' ? window.location.origin : 'http://localhost';
  try {
    const url = new URL(raw, origin);
    if (url.origin !== origin) return '/';
    return url.pathname + url.search + url.hash;
  } catch {
    return '/';
  }
}

/** `/login?return=<path>` for a location the user was refused at; no `return` for the home page. */
export function loginPath(where: { pathname: string; search: string; hash: string }): string {
  const target = safeReturn(where.pathname + where.search + where.hash);
  return target === '/' ? '/login' : `/login?return=${encodeURIComponent(target)}`;
}
