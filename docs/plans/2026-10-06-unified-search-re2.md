<!-- file: docs/plans/2026-10-06-unified-search-re2.md -->
<!-- version: 1.2.0 -->
<!-- guid: 3f6c2a9e-7d41-4b8a-9e05-c1d8f2b7a64e -->
<!-- last-edited: 2026-10-06 -->

# Unified search grammar (RE2) — Library search bar + Review Title filter

Owner decision 2026-10-06: the Library search bar and the Review → Metadata
Title filter use ONE syntax, built on standard RE2 regular expressions. Regex
runs server-side over the full result set. An invalid query shows a visible
error. It must never quietly match nothing or match everything.

## 1. The bug this unblocks

`-metadata:applied -review:no_match -duration:<10m title:a*` returns 0 books.
`title:a*` is parsed as a field filter and sent in the JSON `filters` param.
`fieldMatchesValueRT` then checks `strings.Contains(lower(title), "a*")` for a
literal asterisk, which no title contains.

The SearchBar help offered `title:vamp*`, `author:a || author:b`,
`author:smith~`, `format:(m4b|mp3)`, `year:>2020` and `year:[2015 TO 2020]`.
None of these ever worked. The first four exist only in the Bleve DSL, and a
known field token never reaches Bleve. The year forms were plain substring
matches. `progress_pct:>75` was also broken: `Atoi(">75")` fails, so it
matched nothing.

## 2. Grammar (one spec, both surfaces)

A query is whitespace-separated tokens. A token is either free text or
`[-|NOT ]field:value`. Every field token is ANDed with the others.

| Value form | Meaning | Example |
|---|---|---|
| `field:word` | Substring, case-insensitive | `author:sanderson` |
| `field:"two words"` | Literal substring. Quotes switch off every operator below, so `*`, `/` and `>` inside quotes are just characters. | `title:"a*b"` |
| `field:/RE2/` | RE2 regular expression, **case-insensitive by default**, unanchored. Spaces are allowed between the slashes. Write `\/` for a literal slash. | `title:/^\s*\p{L}/` |
| `field:a*` | Glob. Only `*` is a wildcard. Matches the WHOLE field value, after trimming surrounding whitespace. | `title:a*` (starts with a), `title:*saga` (ends with), `title:*vamp*` (contains) |
| `field:*` | The field has a non-empty value | `narrator:*` |
| `-field:…` / `NOT field:…` | Negates ANY of the forms above | `-title:/^\s*\d/` |
| numeric `field:>N` `>=` `<` `<=` `=` `==` `!=` | Numeric comparison | `year:>2020` |
| numeric `field:[a TO b]` | Inclusive range. `*` leaves one side open. | `year:[2015 TO 2020]`, `bitrate:[* TO 64]` |
| numeric `field:N` | Numeric equality | `year:2019` |
| `duration:` | Unchanged: units (`20m`, `1h30m`), comparisons and ranges | `duration:<10m` |

**Numeric fields:** `year` (matches if the print year OR the audiobook release
year satisfies the test; for `!=`, both must differ), `series_number`,
`bitrate`/`bitrate_kbps`, `file_size`/`file_size_bytes`,
`sample_rate`/`sample_rate_hz`, `channels`, `bit_depth`, `user_rating_*`, and
`progress_pct` (per-user). An unknown or unset value matches NO comparison,
range or equality, which is the same rule `duration` already follows. A
numeric field given a regex or glob is matched against its rendered decimal
text.

**Removed from help (they never worked on field tokens):** `||` OR, `~`
fuzzy, and `(a|b)` groups. Alternation is now regex: `author:/sanderson|jemisin/`,
`format:/^(m4b|mp3)$/`. Cross-field OR is not supported.

**Free text** (any word that is not a known `field:`) still goes to the
full-text index unchanged. Regex applies to field values only.

**Errors** become HTTP 400, and the message names the field and the token:
- an unterminated `/…`
- an empty `//`
- characters after the closing `/`
- an RE2 compile error, e.g. lookahead `(?=`, which gets the hint "RE2 has no lookahead; exclude with -field:/…/"
- a malformed comparison or range on a numeric field

The search bar shows the message as red helper text under the box. The
Library body shows an error panel instead of the previous page's stale books
or a "reconnecting" spinner. The client also runs a pre-check for the cases
it can detect for certain (unterminated or empty regex, lookaround,
backreference) and shows them while the user types. The server stays the
authority: the client never blocks a pattern the browser's own RegExp would
reject, because JS and RE2 differ (see §5).

## 3. Where it lives

- **Go, shared:** new package `internal/querygrammar`. `CompileText(raw,
  quoted)` covers substring, glob and regex. `ParseNumericExpr(raw)` covers
  comparisons, ranges and bare numbers. It has no dependencies beyond the
  stdlib, so the review handler can import it later.
- **Go, Library:** `internal/audiobooks`.
  - `compileFieldFilter` turns a `FieldFilter` into a `compiledFilter`.
  - Patterns are compiled ONCE per request, when the predicate is built (memdb
    pushdown and post-filter paths), never once per row.
  - `ValidateFilterValue`, which feeds the handler's 400, calls the same
    compile, so the validator and the matcher cannot disagree.
  - `GetAudiobooksPage` and `CountAudiobooksFiltered` validate too, so
    background ops (`resolveFilterToBookIDs`) fail loudly instead of resolving
    to 0 books.
- **Pushdown guard:** `review` / `library_state` filters are plucked into the
  store's exact-match `ReviewStatus` / `LibraryState` only when the value is a
  plain literal. Without this, `review:/^no/` would be EqualFold-compared as
  literal text and return 0, which is the same failure shape as the original
  bug.
- **`FieldFilter.Quoted`** (`json:"quoted,omitempty"`) is added to
  `audiobooks.FieldFilter` and `operations.FieldFilter`. The web client sends
  it. It is needed so `title:"a*"` stays literal.
- **Web:**
  - `web/src/utils/searchParser.ts` keeps a `/…/` value intact, including any
    spaces inside.
  - New `web/src/utils/queryGrammar.ts` holds the client pre-check plus an
    RE2→JS translator and evaluator for the review follow-up.
  - `SearchBar` gets an `errorText` prop and its help is rewritten.
  - `FilterPanel` and `Library.tsx` thread the error through and send
    `quoted`.
  - `libraryContentState` gains a `query_error` state for 4xx.

## 4. Review Title filter — follow-up (done in this branch after #3813 merged)

Implemented in a separate commit. The `titleFilterError` and chip-row behaviours
from #3813 are kept. An invalid value still filters nothing, and the error
under the field now names the token and the reason. The original spec
follows.

`QueueRail.tsx` and `lanes/useMetadataLane.ts` are being edited on another
branch, so this branch leaves them alone. The exact follow-up is:

1. In `useMetadataLane.ts`, replace the `titleRegex` `useMemo` (~L1183-1191,
   `new RegExp(filters.titleFilter, 'i')` inside a try/catch that swallows
   errors) with:
   ```ts
   const titleFilter = useMemo(() => compileTitleFilter(filters.titleFilter), [filters.titleFilter]);
   ```
   `compileTitleFilter` comes from `web/src/utils/queryGrammar.ts`. In the
   `beforeRuntime` chain, change the title step to:
   ```ts
   .filter((r) => titleFilter.test(r.book.title || ''))
   ```
   Export `titleFilterError: titleFilter.error` from the hook.
2. In `QueueRail.tsx`:
   - Change the Title TextField to `error={!!titleFilterError}` and
     `helperText={titleFilterError}`.
   - Change the placeholder from `"regex"` to `"text, a*, /regex/"`.
3. What `compileTitleFilter` accepts. It is the same value grammar as
   `title:` in the Library:
   - a bare value: `foo` is a substring, `a*` a glob, `/re/` a regex
   - OR one or more `title:` tokens, any of them negated: `title:a* -title:/^\s*\d/`
   - Any other field token is an error ("only title: filters apply here").
   - **Behaviour change:** bare `foo(` was a JS regex and is now a literal
     substring. Regex now needs slashes, exactly like the Library.
4. `results` is the lane's complete candidate set; paging is client-side. So
   evaluating it client-side is still "over the full result set". Using
   server-side evaluation instead, by posting the value to the review endpoint
   and filtering with `querygrammar.CompileText`, is the alternative listed in
   Decisions.

## 5. JS RegExp vs RE2 (what `queryGrammar.ts` handles)

- **Rejected with an error. RE2 rejects these, and JS would accept them:**
  - lookarounds `(?=`, `(?!`, `(?<=`, `(?<!`
  - backreferences `\1`…`\9`, `\k<name>`
  - possessive and atomic forms `(?>`, `*+`, `++`, `?+`
- **Translated. RE2 accepts these, and JS rejects or reads them differently:**
  - a leading `(?i)`, `(?s)` or `(?-i)` becomes a JS flag
  - `\pL` / `\PL` become `\p{L}` / `\P{L}`
  - `[[:alpha:]]` and the other POSIX classes become their ASCII equivalents
  - `\z` becomes `$`
  - `\A` becomes `^`
  - `\Q…\E` becomes an escaped literal
- Compiled with flags `iu`. The `u` flag is required for `\p{L}`, and `s` is
  added when `(?s)` is used. If the JS engine still rejects the translated
  pattern, the error is shown. That is the only way a client-side evaluator
  can differ from the server, and it fails visibly.

## 6. Tests

- **Go** (`internal/querygrammar` table tests): substring, quoted literal,
  glob prefix/suffix/contains/`*`, regex case-insensitive and `(?-i)`,
  `\p{L}`, invalid regex error, lookahead error, unterminated or empty regex
  and trailing characters, comparisons, ranges with `*`, malformed ranges.
- **Go** (`internal/audiobooks`):
  - `title:a*` returns titles starting with "a" (leading whitespace trimmed)
  - negated regex `-title:/^\s*\d/`
  - `year:>2020`, `year:[2015 TO 2020]`, either-year semantics, unset year
    matching nothing
  - `bitrate:<64` with an unset bitrate matching nothing
  - `progress_pct:>75`
  - an invalid regex makes `ValidateFilterValue` fail and
    `GetAudiobooks` error
  - a regex `review:` value is not plucked
  - existing conformance and empty-filter tests still pass
- **Vitest:** the parser keeps a regex with spaces and a negated regex
  (`-` and `NOT`); an unterminated regex is kept as a value;
  the `queryGrammar` pre-check and translator; `SearchBar` renders
  `errorText`; `libraryContentState` returns `query_error`.

## 7. Rollback

Revert the branch commit(s). There is no data or schema change: `Quoted` is
an `omitempty` request field, and the server ignores its absence. The old
behaviour for plain words (substring) is unchanged.

## 8. Decisions for owner to validate

1. **Regex is case-insensitive by default.** Every other text filter here is
   case-insensitive, and so is the review filter today (`'i'`). A regex that
   was case-sensitive while `title:foo` is not would surprise people. Opt out
   with `(?-i)`.
2. **`title:a*` matches the whole title after trimming whitespace, not any
   word in it.** The intent was "starts with a". Whole-value matching is how
   shell and file globs work, and `*word*` still gives "contains". For
   "any word starts with", use `title:/\ba/`.
3. **`?` is NOT a glob wildcard.** Titles contain literal question marks.
   Regex covers single-character matching.
4. **A bare number on a numeric field means equality, not substring.**
   `year:2019` no longer matches "12019", and `bitrate:128` no longer matches
   1280. An unset numeric matches nothing, including `channels:0`, which used
   to match books with no channel count. Text forms on a numeric field (`*`,
   glob, regex) are matched against each KNOWN value's decimal text. So
   `bitrate:*` means "has a bitrate", `-channels:*` finds unset ones, and
   `year:/^20/` checks both years.
5. **`field:*` means "has a value".** The alternative was to reject it.
6. **OR (`||`), fuzzy (`~`) and `(a|b)` are dropped from help** in favour of
   regex alternation within one field. Cross-field OR is not offered.
7. **Glob trims the field value; regex does not.** The canonical regex
   "starts with a letter" therefore keeps its `\s*`: `title:/^\s*\p{L}/`.
8. **Review evaluation stays client-side, using the shared translator.** The
   lane already holds the full set. For byte-for-byte RE2 parity, the
   alternative is to send the value to the server and filter there. That
   costs an API change on the review endpoint.
9. **Bare text in the Review box is now a substring, not a regex.** Regex
   needs `/…/` there too, so it is one syntax.
10. **Sidebar picks (author, series, genre, language, version group) are sent
    quoted, i.e. as literal text.** A name containing `*` or starting with `/`
    must never become a wildcard or a regex.
11. **A 400 is not toasted any more.** With regex in the grammar, every
    debounced pause while typing `/^\s*` returns a 400. The message stays
    under the box and in the error panel until the query is fixed.
12. **`tag:` is an exact tag lookup, and `-tag:` was never implemented** (it
    did nothing). `-tag:x`, `tag:a*` and `tag:/x/` are now visible
    client-side errors, and `-tag:read` is gone from the help. `read_status:`
    rejects patterns on both client and server. Implementing tag exclusion is
    a follow-up if wanted.
