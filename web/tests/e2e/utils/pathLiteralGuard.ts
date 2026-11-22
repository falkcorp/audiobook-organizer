// file: web/tests/e2e/utils/pathLiteralGuard.ts
// version: 0.1.0
// guid: d8f4d414-6ada-45fe-8dd4-1a076ab85ca3
// last-edited: 2026-11-22

// Finds page-path literals that a spec spells out instead of taking from
// ./paths: a string starting with `/` (or `**/`) as an argument of `goto`,
// `toHaveURL`, `waitForURL` or `expectFinalUrl`, a regex literal with an
// escaped slash (`/\/login$/`) in `toHaveURL` or `waitForURL`, an
// `href="/..."` locator and an `expect(<url>).toContain('/...')`. Query-only
// patterns (`/band=CERTAIN/`) name no page and are allowed. API routes
// (`**/api/v1/**`) are not page URLs and are never passed to these calls.

export interface RawPagePath {
  /** 1-based line of the offending call. */
  line: number;
  /** The call's argument text, trimmed. */
  text: string;
}

const NAV_CALL = /\b(goto|toHaveURL|waitForURL|expectFinalUrl)\(/g;

/** The text between the call's `(` at `open` and its matching `)`. */
function argumentText(source: string, open: number): string {
  let depth = 0;
  let quote = '';
  for (let i = open; i < source.length; i++) {
    const c = source[i];
    if (quote) {
      if (c === '\\') i++;
      else if (c === quote) quote = '';
      continue;
    }
    if (c === "'" || c === '"' || c === '`') quote = c;
    else if (c === '(') depth++;
    else if (c === ')' && --depth === 0) return source.slice(open + 1, i);
  }
  return source.slice(open + 1);
}

/** True when `args` holds a quoted string that starts with `/` or `**` + `/`, or a regex literal naming a path. */
function namesAPage(call: string, args: string): boolean {
  if (/(['"`])(\/|\*\*\/)/.test(args)) return true;
  if (call === 'goto' || call === 'expectFinalUrl') return false;
  return /(?:^|[,(\s])\/(?![/*])(?:\\.|[^/\\\n])*\\\//.test(args);
}

/** Every raw page-path literal in `source`, in order. */
export function findRawPagePaths(source: string): RawPagePath[] {
  const found: RawPagePath[] = [];
  const lineOf = (index: number) => source.slice(0, index).split('\n').length;
  for (const m of source.matchAll(NAV_CALL)) {
    const open = m.index + m[0].length - 1;
    const args = argumentText(source, open);
    if (namesAPage(m[1], args)) found.push({ line: lineOf(m.index), text: args.trim() });
  }
  for (const m of source.matchAll(/href=\\?["']\//g)) {
    found.push({ line: lineOf(m.index), text: source.slice(m.index, m.index + 40) });
  }
  for (const m of source.matchAll(/expect\(\w*[uU]rl\w*\)\.toContain\(\s*['"`]\//g)) {
    found.push({ line: lineOf(m.index), text: source.slice(m.index, m.index + 60) });
  }
  return found.sort((a, b) => a.line - b.line);
}
