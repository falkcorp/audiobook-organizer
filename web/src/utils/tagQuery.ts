// file: web/src/utils/tagQuery.ts
// version: 2.0.2
// guid: 1c8e4a6f-9b3d-4f27-a5e0-7d2b9c6f1e58
// last-edited: 2026-10-26

/**
 * Editing `tag:` terms in the Library search text, for the Browse-by-Tag
 * chips: a chip click appends `tag:"value"` (narrowing the current query) and
 * clicking an active chip removes its term again. Kept apart from
 * searchParser.ts so the grammar and these text edits change independently.
 *
 * WHAT A `tag:` TERM MEANS. The server applies a `tag:` term as an EXACT tag
 * filter (the whole tag, case folded, like the tags[] parameter), and only where it
 * is a conjunct of the whole query: on the top level, or in a parenthesised group
 * that is only ANDed. Under OR, under NOT or `-`, and in an alternation it is
 * refused. This file reads the text the same way, so a chip is "on" exactly when
 * its term narrows the request (an "active" term), and an edit never leaves text
 * the server cannot read: removing a term takes its AND, OR or NOT with it.
 *
 * The Library search parser (searchParser.ts) is a flat word splitter, so it reads
 * "(tag:noir" as free text and "tag:noir)" with the bracket in the value; a query
 * the server reads whole is read with the scanner below instead.
 */

type TokKind = 'open' | 'close' | 'op' | 'not' | 'term' | 'word';

interface Tok {
  kind: TokKind;
  /** The text of the token as written, including a glued `-`. */
  raw: string;
  /** A `-` glued to the front of a term or group. */
  neg: boolean;
  /** For an `op`: true for OR / `||`. */
  isOr?: boolean;
  field?: string;
  /** For a term: the text after `field:`. */
  rest?: string;
}

const FIELD_HEAD = /^[A-Za-z_][A-Za-z0-9_]*:[<>=]*$/;

function tokenize(q: string): Tok[] {
  const toks: Tok[] = [];
  let i = 0;
  while (i < q.length) {
    if (/\s/.test(q[i])) {
      i++;
      continue;
    }
    const start = i;
    let neg = false;
    if (q[i] === '-' && i + 1 < q.length && !/[\s)]/.test(q[i + 1])) {
      neg = true;
      i++;
    }
    if (q[i] === '(') {
      toks.push({ kind: 'open', raw: q.slice(start, i + 1), neg });
      i++;
      continue;
    }
    if (q[i] === ')') {
      toks.push({ kind: 'close', raw: ')', neg: false });
      i++;
      continue;
    }
    if (q.startsWith('&&', i) || q.startsWith('||', i)) {
      toks.push({ kind: 'op', raw: q.slice(i, i + 2), neg: false, isOr: q[i] === '|' });
      i += 2;
      continue;
    }
    let j = i;
    while (j < q.length) {
      const c = q[j];
      if (/\s/.test(c) || c === ')' || q.startsWith('&&', j) || q.startsWith('||', j)) break;
      if (c === '"') {
        const close = q.indexOf('"', j + 1);
        j = close === -1 ? q.length : close + 1;
        continue;
      }
      if (c === '(' && FIELD_HEAD.test(q.slice(i, j))) {
        // field:(a|b) alternation: the bracket belongs to the value.
        const close = q.indexOf(')', j);
        j = close === -1 ? q.length : close + 1;
        continue;
      }
      j++;
    }
    const raw = q.slice(start, j);
    const word = q.slice(i, j);
    i = j;
    if (!neg && (word === 'AND' || word === 'OR')) {
      toks.push({ kind: 'op', raw, neg: false, isOr: word === 'OR' });
      continue;
    }
    if (!neg && word === 'NOT') {
      toks.push({ kind: 'not', raw, neg: false });
      continue;
    }
    const m = /^([A-Za-z_][A-Za-z0-9_]*):([\s\S]*)$/.exec(word);
    if (m) {
      toks.push({ kind: 'term', raw, neg, field: m[1], rest: m[2] });
    } else {
      toks.push({ kind: 'word', raw, neg });
    }
  }
  return toks;
}

/** The tag name a plain `tag:` term writes, or null for a term that is not one
 * whole tag (an alternation, a prefix or fuzzy term, a comparison, an empty value). */
function plainTagValue(rest: string): string | null {
  if (rest.startsWith('"')) {
    const close = rest.indexOf('"', 1);
    if (close === -1 || close !== rest.length - 1) return null;
    return rest.slice(1, close) || null;
  }
  // An unquoted value holds none of * ~ ^ (the server refuses them anywhere in it:
  // a prefix, a wildcard, fuzziness or a boost is not "that tag"; quote the name if
  // it contains one), nor starts a comparison or an alternation.
  if (rest === '' || /^[<>=([]/.test(rest) || /[*~^]/.test(rest)) return null;
  return rest;
}

/** The text the server's parser leaves behind for a boost or fuzzy marker written
 * after a quoted tag: tag:"noir"^2 is the tag term and the free text ^2, and a tag
 * filter takes neither, so the server refuses the pair. */
const TAG_MODIFIER_WORD = /^[\^~]\d*(\.\d+)?$/;

/** A token that joins nothing on its own: NOT, a bare `-`, AND, OR. */
function isDanglingKind(t: Tok): boolean {
  return t.kind === 'not' || t.kind === 'op' || (t.kind === 'word' && !t.neg && t.raw === '-');
}

/** The form a tag is compared in: what the server's GetBooksByTag folds it to. */
export function foldTag(tag: string): string {
  return tag.trim().toLowerCase();
}

interface Scan {
  toks: Tok[];
  /** Token indexes of the active tag terms, with the tag each names (folded). */
  active: Array<{ index: number; tag: string }>;
  /** True when the top level of the query has an OR. */
  rootHasOr: boolean;
}

interface Frame {
  negated: boolean;
  hasOr: boolean;
  parent: Frame | null;
}

function scan(query: string): Scan {
  const toks = tokenize(query);
  const root: Frame = { negated: false, hasOr: false, parent: null };
  let current = root;
  let pendingNot = false;
  const found: Array<{ index: number; tag: string; frame: Frame }> = [];
  toks.forEach((t, index) => {
    switch (t.kind) {
      case 'not':
        pendingNot = true;
        break;
      case 'op':
        if (t.isOr) current.hasOr = true;
        pendingNot = false;
        break;
      case 'open':
        current = { negated: t.neg || pendingNot, hasOr: false, parent: current };
        pendingNot = false;
        break;
      case 'close':
        if (current.parent) current = current.parent;
        pendingNot = false;
        break;
      case 'term': {
        const negated = t.neg || pendingNot;
        pendingNot = false;
        if (t.field === 'tag' && !negated) {
          const value = plainTagValue(t.rest ?? '');
          if (value !== null) found.push({ index, tag: foldTag(value), frame: current });
        }
        break;
      }
      case 'word': {
        // ^2 or ~ straight after a tag term: the server refuses the pair, so the
        // term narrows nothing.
        if (
          TAG_MODIFIER_WORD.test(t.raw) &&
          found.length &&
          found[found.length - 1].index === index - 1
        ) {
          found.pop();
        }
        pendingNot = false;
        break;
      }
      default:
        pendingNot = false;
    }
  });
  const active = found.filter(({ frame }) => {
    for (let f: Frame | null = frame; f; f = f.parent) {
      if (f.negated || f.hasOr) return false;
    }
    return true;
  });
  return {
    toks,
    active: active.map(({ index, tag }) => ({ index, tag })),
    rootHasOr: root.hasOr,
  };
}

/** The `tag:` term for value: quoted, since tags routinely contain colons. */
export function tagTerm(tag: string): string {
  // A tag containing a double quote cannot be quoted by this grammar; it is
  // sent bare (the parser reads a bare value up to the next space).
  return tag.includes('"') ? `tag:${tag}` : `tag:"${tag}"`;
}

/**
 * The tags the query text narrows by: its active `tag:` terms, in the order
 * written, folded to lower case and trimmed the way the server compares tags (a
 * chip for "noir" is on for `tag:Noir`). A term that is negated, inside a negated
 * group, under an OR, in an alternation, or not one whole tag narrows nothing and is
 * not listed.
 */
export function tagTermValues(query: string): string[] {
  return scan(query).active.map((a) => a.tag);
}

/**
 * Appends a `tag:` term for tag to query. No-op when the query already narrows by
 * that tag (compared case-insensitively). A query with an OR on its top level is
 * wrapped first, because `a OR b tag:"x"` would put the tag under the OR, which
 * the server refuses.
 */
export function appendTagTerm(query: string, tag: string): string {
  let s = scan(query);
  const want = foldTag(tag);
  if (s.active.some((a) => a.tag === want)) return query;
  let base = query.trim();
  // A trailing NOT, `-`, AND or OR has nothing to apply to; the new term would
  // become its operand (`a NOT` + tag = `a NOT tag:"x"`, which excludes the tag, or
  // `a -` + tag, which leaves a stray `-`). Drop them before appending.
  let toks = s.toks;
  while (toks.length && isDanglingKind(toks[toks.length - 1])) toks = toks.slice(0, -1);
  if (toks.length !== s.toks.length) {
    base = render(toks);
    s = scan(base);
    if (s.active.some((a) => a.tag === want)) return base;
  }
  if (!base) return tagTerm(tag);
  return `${s.rootHasOr ? `(${base})` : base} ${tagTerm(tag)}`;
}

/**
 * Removes every active `tag:` term for tag from query (compared case-insensitively),
 * and with it the AND that joined it, so the text stays a query the server reads:
 * `a AND tag:"x"` becomes `a`, `tag:"x" AND b` becomes `b`. A term that is not
 * active is left alone (`-(tag:"x")`, `(tag:"x" OR b)`), and a group left empty goes.
 * Text with no such term is returned unchanged.
 */
export function removeTagTerm(query: string, tag: string): string {
  const s = scan(query);
  const want = foldTag(tag);
  const drop = new Set(s.active.filter((a) => a.tag === want).map((a) => a.index));
  if (drop.size === 0) return query;
  let toks = s.toks.filter((_, i) => !drop.has(i));
  for (let changed = true; changed;) {
    changed = false;
    const next: Tok[] = [];
    for (let i = 0; i < toks.length; i++) {
      const t = toks[i];
      const prev = next[next.length - 1];
      const after = toks[i + 1];
      if (t.kind === 'open' && after?.kind === 'close') {
        // An emptied group goes.
        i++;
        changed = true;
        continue;
      }
      // An AND or OR with nothing to join on one side (at the start, after "(", before
      // ")", at the end, or next to another) is what is left of the term's connective.
      if (
        t.kind === 'op' &&
        (!prev ||
          prev.kind === 'open' ||
          prev.kind === 'op' ||
          !after ||
          after.kind === 'close' ||
          after.kind === 'op')
      ) {
        changed = true;
        continue;
      }
      // A NOT or a bare `-` with nothing after it (at the end, before ")" or before
      // an AND or OR) applied to the term that went: `- tag:"x"` leaves `-`.
      if (
        (t.kind === 'not' || (t.kind === 'word' && !t.neg && t.raw === '-')) &&
        (!after || after.kind === 'close' || after.kind === 'op')
      ) {
        changed = true;
        continue;
      }
      next.push(t);
    }
    toks = next;
  }
  return render(toks);
}

/** The text of a token list: tokens a space apart, none inside a bracket. */
function render(toks: Tok[]): string {
  let out = '';
  toks.forEach((t, i) => {
    const prev = toks[i - 1];
    if (i > 0 && prev.kind !== 'open' && t.kind !== 'close') out += ' ';
    out += t.raw;
  });
  return out;
}
