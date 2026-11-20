// file: core/web/packages/shell/src/version/compare.ts
// version: 0.1.0
// guid: 5608cf17-a94d-4f89-bcf1-31424c734b08
// last-edited: 2026-11-20

// Version comparison for the 07 §7.5.2 tables: the same rule for the core
// version row and the per-module version row.

export interface ParsedVersion {
  major: number;
  minor: number;
  patch: number;
}

const VERSION_RE = /^v?(\d+)\.(\d+)\.(\d+)(?:[-+].*)?$/;

/** Parses `MAJOR.MINOR.PATCH` (an optional `v` and any pre-release or build suffix are ignored); null when it is not one. */
export function parseVersion(version: string): ParsedVersion | null {
  const m = VERSION_RE.exec(version.trim());
  if (!m) return null;
  return { major: Number(m[1]), minor: Number(m[2]), patch: Number(m[3]) };
}

/**
 * True when a client at `client` may talk to a server at `server`: the majors
 * are equal, and in major 0 the minors are equal (any v0 minor may break), while
 * from major 1 the client minor must not exceed the server's. A version that
 * does not parse is never compatible (hiding beats calling a missing endpoint).
 */
export function isVersionCompatible(client: string, server: string): boolean {
  const c = parseVersion(client);
  const s = parseVersion(server);
  if (!c || !s) return false;
  if (c.major !== s.major) return false;
  if (c.major === 0) return c.minor === s.minor;
  return c.minor <= s.minor;
}
