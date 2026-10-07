// file: web/src/utils/tagQuery.ts
// version: 1.0.0
// guid: 1c8e4a6f-9b3d-4f27-a5e0-7d2b9c6f1e58
// last-edited: 2026-10-06

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

/** Removes every non-negated `tag:` term for tag from query. */
export function removeTagTerm(query: string, tag: string): string {
  let out = query;
  for (let m = findTagTerm(out, tag); m; m = findTagTerm(out, tag)) {
    out = (out.slice(0, m.start) + ' ' + out.slice(m.end)).replace(/ {2,}/g, ' ');
  }
  return out.trim();
}

function escapeRegExp(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

/** Locates a non-negated `tag:value` / `tag:"value"` term (case-sensitive field). */
function findTagTerm(query: string, tag: string): { start: number; end: number } | null {
  const v = escapeRegExp(tag);
  // Preceded by start or a space (so `-tag:` and `NOT tag:` are not matched as
  // active: the negated form is excluded below), followed by end or a space.
  const re = new RegExp(`(^|\\s)tag:(?:"${v}"|${v})(?=\\s|$)`, 'g');
  for (let m = re.exec(query); m; m = re.exec(query)) {
    const start = m.index + m[1].length;
    const before = query.slice(0, start);
    if (/(^|\s)NOT\s+$/.test(before)) continue;
    return { start, end: start + m[0].length - m[1].length };
  }
  return null;
}
