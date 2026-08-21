// file: web/src/utils/pathAliases.ts
// version: 1.1.0
// guid: 7b3e5c02-91af-4d68-a15c-3f8092d6b4e1
// last-edited: 2026-08-21

// Renders one server-side POSIX path as the several forms a remote client can
// act on. Presentation only -- see docs/design/2026-08-20-dual-path-display.md.
//
// Deliberately NOT part of formatPath.ts: that file's header declares it
// mirrors internal/pathutil/abbreviate.go, and that claim must stay true.

import type { PathAlias } from '../services/api';
import { formatPath, type PathVar } from './formatPath';

export interface PathRendering {
  key: 'posix' | 'windows' | 'unc';
  label: string;
  /** What the reader sees. May be abbreviated to $(books)/... */
  display: string;
  /** What lands on the clipboard. Always the full literal path -- an
   *  abbreviated path pasted into Explorer is useless. */
  copyText: string;
  /** Anchor when non-null, plain text when null. Null unless the client OS is
   *  known to register a handler for the scheme. */
  href: string | null;
}

/**
 * hasSchemeHandler reports whether the client OS registers an smb: URI handler.
 *
 * macOS binds smb: to Finder (apple-default); GNOME/KDE bind it via gvfs/kio.
 * Windows registers nothing -- Explorer consumes UNC (\\host\share), not the
 * scheme -- so an anchor there is a dead link that looks live, which reads as
 * "the app is broken" rather than "the scheme is unsupported".
 *
 * Fail closed: an unrecognised or absent platform gets no anchor.
 */
export function hasSchemeHandler(platform?: string): boolean {
  if (!platform) return false;
  const p = platform.toLowerCase();
  if (p.startsWith('win')) return false;
  return p.includes('mac') || p.includes('linux');
}

/** The browser's best guess at the client OS, preferring the non-deprecated API. */
function detectPlatform(): string | undefined {
  const uaData = (navigator as { userAgentData?: { platform?: string } }).userAgentData;
  return uaData?.platform ?? navigator.platform ?? undefined;
}

/** Strips one trailing slash so a root configured either way behaves the same. */
const trimRoot = (root: string) => root.replace(/\/+$/, '');

/**
 * matchAlias returns the first alias whose root contains `path`, plus the
 * remainder. Callers must order aliases most-specific-first, the same contract
 * formatPath uses. An empty root is skipped so it cannot match everything.
 */
function matchAlias(path: string, aliases: PathAlias[]): { alias: PathAlias; rest: string } | null {
  for (const alias of aliases) {
    const root = trimRoot(alias.root ?? '');
    if (!root) continue;
    if (path === root) return { alias, rest: '' };
    if (path.startsWith(root + '/')) return { alias, rest: path.slice(root.length + 1) };
  }
  return null;
}

/** Joins with backslashes. See the separator contract in the spec. */
const toWindows = (prefix: string, rest: string) => {
  // Trim a trailing separator so an explicitly-configured `W:\` cannot render
  // `W:\\Author`. Seeded aliases are already normalized in Go
  // (normalizeWindowsPrefix); this only guards a hand-written one.
  const base = prefix.replace(/[\\/]+$/, '');
  return rest ? `${base}\\${rest.replace(/\//g, '\\')}` : base;
};

/** Percent-encodes each segment, leaving the separators alone. */
const toSmbURL = (base: string, rest: string) =>
  rest ? `${base}/${rest.split('/').map(encodeURIComponent).join('/')}` : base;

export function renderPath(
  path: string,
  aliases: PathAlias[] | undefined,
  vars: PathVar[],
  platform: string | undefined = detectPlatform(),
): PathRendering[] {
  const match = matchAlias(path, aliases ?? []);
  const anchorable = hasSchemeHandler(platform);

  const posix: PathRendering = {
    key: 'posix',
    label: 'Linux',
    display: formatPath(path, vars),
    copyText: path,
    href:
      match?.alias.smb_url && anchorable ? toSmbURL(match.alias.smb_url, match.rest) : null,
  };

  const out: PathRendering[] = [posix];
  if (!match) return out;

  // Each rendering is independent: a partially configured alias emits fewer
  // lines rather than a wrong or empty one.
  if (match.alias.windows) {
    const w = toWindows(match.alias.windows, match.rest);
    out.push({ key: 'windows', label: 'Windows', display: w, copyText: w, href: null });
  }
  if (match.alias.unc) {
    const u = toWindows(match.alias.unc, match.rest);
    out.push({ key: 'unc', label: 'UNC', display: u, copyText: u, href: null });
  }
  return out;
}
