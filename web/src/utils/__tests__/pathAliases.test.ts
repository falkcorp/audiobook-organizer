// file: web/src/utils/__tests__/pathAliases.test.ts
// version: 1.2.0
// guid: 2f1c9a55-6d84-4b0e-9a37-8e5b0c14d7a2
// last-edited: 2026-08-21

import { describe, it, expect } from 'vitest';
import { renderPath, hasSchemeHandler } from '../pathAliases';
import type { PathAlias } from '../../services/api';

const ALIASES: PathAlias[] = [
  { root: '/library/books', windows: 'W:', unc: '\\\\host\\books', smb_url: 'smb://host/books' },
];
const VARS = [{ name: 'books', value: '/library/books' }];
const P = '/library/books/Some Author/Some Title/part1.m4b';

const by = (rs: ReturnType<typeof renderPath>, k: string) => rs.find((r) => r.key === k)!;

describe('renderPath', () => {
  it('renders posix, windows and unc for a matching alias', () => {
    const rs = renderPath(P, ALIASES, VARS, 'macOS');
    expect(rs.map((r) => r.key)).toEqual(['posix', 'windows', 'unc']);
  });

  it('abbreviates display but copies the literal path', () => {
    const posix = by(renderPath(P, ALIASES, VARS, 'macOS'), 'posix');
    expect(posix.display).toBe('$(books)/Some Author/Some Title/part1.m4b');
    expect(posix.copyText).toBe(P);
  });

  it('flips separators to backslashes for the windows and unc forms', () => {
    const rs = renderPath(P, ALIASES, VARS, 'macOS');
    expect(by(rs, 'windows').copyText).toBe('W:\\Some Author\\Some Title\\part1.m4b');
    expect(by(rs, 'unc').copyText).toBe('\\\\host\\books\\Some Author\\Some Title\\part1.m4b');
  });

  it('joins a multi-segment windows prefix without touching its separators', () => {
    // The Go seam normalizes a seeded prefix to this shape (a file:// URL,
    // percent-decoded and backslashed). Rendering must not re-flip it.
    const aliases: PathAlias[] = [
      { root: '/library/books', windows: 'W:\\itunes\\iTunes Media' },
    ];
    const rs = renderPath(P, aliases, VARS, 'macOS');
    expect(by(rs, 'windows').copyText).toBe(
      'W:\\itunes\\iTunes Media\\Some Author\\Some Title\\part1.m4b',
    );
  });

  it('does not double a separator when the prefix already ends in one', () => {
    const aliases: PathAlias[] = [
      { root: '/library/books', windows: 'W:\\', unc: '\\\\host\\books\\' },
    ];
    const rs = renderPath(P, aliases, VARS, 'macOS');
    expect(by(rs, 'windows').copyText).toBe('W:\\Some Author\\Some Title\\part1.m4b');
    expect(by(rs, 'unc').copyText).toBe(
      '\\\\host\\books\\Some Author\\Some Title\\part1.m4b',
    );
  });

  it('never puts an href on a windows or unc rendering', () => {
    const rs = renderPath(P, ALIASES, VARS, 'macOS');
    expect(by(rs, 'windows').href).toBeNull();
    expect(by(rs, 'unc').href).toBeNull();
  });

  it('percent-encodes each smb segment but not the separators', () => {
    const p = '/library/books/A & B/Title [Unabridged]/it#1.m4b';
    const posix = by(renderPath(p, ALIASES, VARS, 'macOS'), 'posix');
    expect(posix.href).toBe(
      'smb://host/books/A%20%26%20B/Title%20%5BUnabridged%5D/it%231.m4b',
    );
  });

  it('leaves parentheses and apostrophes unescaped', () => {
    const p = "/library/books/Author (Reader)/it's here!/x.m4b";
    const posix = by(renderPath(p, ALIASES, VARS, 'macOS'), 'posix');
    expect(posix.href).toContain("Author%20(Reader)/it's%20here!");
  });

  it('returns only the posix rendering when no alias matches', () => {
    const rs = renderPath('/elsewhere/x.m4b', ALIASES, VARS, 'macOS');
    expect(rs.map((r) => r.key)).toEqual(['posix']);
    expect(by(rs, 'posix').href).toBeNull();
  });

  it('returns only the posix rendering when no aliases are configured', () => {
    const rs = renderPath(P, [], VARS, 'macOS');
    expect(rs.map((r) => r.key)).toEqual(['posix']);
  });

  it('skips an alias with an empty root so it cannot match everything', () => {
    const rs = renderPath(P, [{ root: '', windows: 'W:' }], VARS, 'macOS');
    expect(rs.map((r) => r.key)).toEqual(['posix']);
  });

  it('matches the most specific root first', () => {
    const nested: PathAlias[] = [
      { root: '/library/books/audiobooks', windows: 'A:' },
      { root: '/library/books', windows: 'W:' },
    ];
    const rs = renderPath('/library/books/audiobooks/x.m4b', nested, VARS, 'macOS');
    expect(by(rs, 'windows').copyText).toBe('A:\\x.m4b');
  });

  it('tolerates a trailing slash on the configured root', () => {
    const rs = renderPath(P, [{ root: '/library/books/', windows: 'W:' }], VARS, 'macOS');
    expect(by(rs, 'windows').copyText).toBe('W:\\Some Author\\Some Title\\part1.m4b');
  });

  it('omits each rendering whose alias field is empty, independently', () => {
    const rs = renderPath(P, [{ root: '/library/books', unc: '\\\\host\\books' }], VARS, 'macOS');
    expect(rs.map((r) => r.key)).toEqual(['posix', 'unc']);
    expect(by(rs, 'posix').href).toBeNull();
  });

  it('omits unc and href when only windows is configured', () => {
    const rs = renderPath(P, [{ root: '/library/books', windows: 'W:' }], VARS, 'macOS');
    expect(rs.map((r) => r.key)).toEqual(['posix', 'windows']);
    expect(by(rs, 'posix').href).toBeNull();
  });

  it('omits windows and unc when only smb_url is configured', () => {
    const rs = renderPath(P, [{ root: '/library/books', smb_url: 'smb://host/books' }], VARS, 'macOS');
    expect(rs.map((r) => r.key)).toEqual(['posix']);
    expect(by(rs, 'posix').href).toBe(
      'smb://host/books/Some%20Author/Some%20Title/part1.m4b',
    );
  });

  it('renders the bare prefix with no trailing separator on an exact root match', () => {
    const rs = renderPath('/library/books', ALIASES, VARS, 'macOS');
    expect(by(rs, 'windows').copyText).toBe('W:');
    expect(by(rs, 'unc').copyText).toBe('\\\\host\\books');
  });
});

describe('hasSchemeHandler — Decision 5, fail closed', () => {
  it.each(['macOS', 'MacIntel', 'Linux', 'Linux x86_64'])('anchors on %s', (p) => {
    expect(hasSchemeHandler(p)).toBe(true);
  });

  it.each(['Windows', 'Win32', 'WinCE', '', undefined, 'Fuchsia'])(
    'does not anchor on %s',
    (p) => {
      expect(hasSchemeHandler(p as string | undefined)).toBe(false);
    },
  );

  it('gives Windows precedence over a Linux substring in the same string', () => {
    // Pins the fail-closed guard's ordering: startsWith('win') is checked
    // BEFORE the mac/linux substring check, so a platform string containing
    // both tokens (e.g. WSL's navigator.platform) still resolves to false.
    expect(hasSchemeHandler('Windows Subsystem for Linux')).toBe(false);
  });

  it('gates the posix href and nothing else', () => {
    const mac = renderPath(P, ALIASES, VARS, 'macOS');
    const win = renderPath(P, ALIASES, VARS, 'Win32');
    expect(by(mac, 'posix').href).not.toBeNull();
    expect(by(win, 'posix').href).toBeNull();
    // The gate must change the href and nothing else about the row.
    expect(win.map((r) => [r.key, r.display, r.copyText])).toEqual(
      mac.map((r) => [r.key, r.display, r.copyText]),
    );
  });
});

// The test that matters: one row emits several strings for one file, each of
// which looks correct alone. A link that opens a different file than the
// clipboard pastes would pass every test above.
describe('every rendering of a row resolves to the same file', () => {
  const cases = [
    '/library/books/Plain/x.m4b',
    '/library/books/A & B/Title [Unabridged]/it#1.m4b',
    "/library/books/Author (Reader)/it's here!/x.m4b",
    '/library/books/Ünïcödé Ω/x.m4b',
    '/library/books/trailing space /x.m4b',
  ];

  it.each(cases)('round-trips %s', (p) => {
    const rs = renderPath(p, ALIASES, VARS, 'macOS');
    const posix = by(rs, 'posix');

    // display -> un-abbreviate
    expect(posix.display.replace('$(books)', '/library/books')).toBe(p);
    // copyText is already literal
    expect(posix.copyText).toBe(p);
    // href -> strip scheme+share, decode
    const tail = decodeURIComponent(posix.href!.replace('smb://host/books', ''));
    expect('/library/books' + tail).toBe(p);
    // windows/unc -> flip separators back
    const windows = by(rs, 'windows');
    const unc = by(rs, 'unc');
    expect(windows.copyText.replace(/^W:/, '/library/books').replace(/\\/g, '/')).toBe(p);
    expect(unc.copyText.replace(/^\\\\host\\books/, '/library/books').replace(/\\/g, '/')).toBe(p);
    // display and copyText must stay in lockstep for windows/unc -- there is
    // no abbreviation step for these forms, so a divergence would mean the
    // link and the on-screen text point at different files.
    expect(windows.display).toBe(windows.copyText);
    expect(unc.display).toBe(unc.copyText);
  });
});
