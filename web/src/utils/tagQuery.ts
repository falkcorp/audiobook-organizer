// file: web/src/utils/tagQuery.ts
// version: 1.1.0
// guid: 1c8e4a6f-9b3d-4f27-a5e0-7d2b9c6f1e58
// last-edited: 2026-10-26

/**
 * Editing `tag:` terms in the Library search text, for the Browse-by-Tag
 * chips: a chip click appends `tag:"value"` (narrowing the current query) and
 * clicking an active chip removes its term again. Kept apart from
 * searchParser.ts so the grammar and these two text edits change
 * independently.
 */

/** The `tag:` term for value: quoted, since tags routinely contain colons. */
export function tagTerm(tag: string): string {
  // A tag containing a double quote cannot be quoted by this grammar; it is
  // sent bare (the parser reads a bare value up to the next space).
  return tag.includes('"') ? `tag:${tag}` : `tag:"${tag}"`;
}

/** Appends a `tag:` term for tag to query (no-op when an identical term is present). */
export function appendTagTerm(query: string, tag: string): string {
  if (findTagTerm(query, tag)) return query;
  const base = query.trim();
  return base ? `${base} ${tagTerm(tag)}` : tagTerm(tag);
}

/**
 * Removes every non-negated `tag:` term for tag from query. A term inside a group
 * ("(read_status:finished tag:noir)") is found and taken out of it, and a group left
 * empty by that goes with it, so the text stays a query the parser reads.
 */
export function removeTagTerm(query: string, tag: string): string {
  let out = query;
  for (let m = findTagTerm(out, tag); m; m = findTagTerm(out, tag)) {
    let before = out.slice(0, m.start);
    let after = out.slice(m.end);
    // Inside a group the gap should not leave "( x" or "x )".
    if (before.endsWith('(')) after = after.replace(/^ +/, '');
    if (after.startsWith(')')) before = before.replace(/ +$/, '');
    if (before.endsWith('(') && after.startsWith(')')) {
      before = before.slice(0, -1);
      after = after.slice(1);
    }
    // No separator across a bracket: "(x" and "x)" stay tight.
    const glue = before.endsWith('(') || after.startsWith(')') ? '' : ' ';
    out = (before + glue + after).replace(/ {2,}/g, ' ');
  }
  return out.trim();
}

/**
 * The non-negated `tag:` terms in query text, read the way the server's grammar
 * reads them: a term starts at the beginning, after a space or after "(", a quoted
 * value runs to its closing quote, a bare value ends at a space or a parenthesis,
 * and text inside other double quotes (title:"a tag:b") is not a term. The Library
 * search parser is a flat word splitter, so it reads "(tag:noir" as free text and
 * "tag:noir)" with the bracket in the value; a query the server reads whole is read
 * with this instead.
 */
export function tagTermValues(query: string): string[] {
  const out: string[] = [];
  const term = /(-|NOT\s+)?tag:(?:"([^"]*)"|([^\s()"]+))/y;
  let i = 0;
  while (i < query.length) {
    const atBoundary = i === 0 || /[\s(]/.test(query[i - 1]);
    if (atBoundary) {
      term.lastIndex = i;
      const m = term.exec(query);
      if (m) {
        if (!m[1]) out.push(m[2] ?? m[3]);
        i += m[0].length;
        continue;
      }
    }
    if (query[i] === '"') {
      const close = query.indexOf('"', i + 1);
      i = close === -1 ? query.length : close + 1;
      continue;
    }
    i++;
  }
  return out;
}

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/** Locates a non-negated `tag:value` / `tag:"value"` term (case-sensitive field). */
function findTagTerm(query: string, tag: string): { start: number; end: number } | null {
  const v = escapeRegExp(tag);
  // Preceded by start, a space or "(" (so `-tag:` and `NOT tag:` are not matched
  // as active: the negated form is excluded below), followed by end, a space or
  // ")": a tag chip's term sits inside a group once the person groups the query.
  const re = new RegExp(`(^|[\\s(])tag:(?:"${v}"|${v})(?=[\\s)]|$)`, 'g');
  for (let m = re.exec(query); m; m = re.exec(query)) {
    const start = m.index + m[1].length;
    const before = query.slice(0, start);
    if (/(^|\s)NOT\s+$/.test(before)) continue;
    return { start, end: start + m[0].length - m[1].length };
  }
  return null;
}
