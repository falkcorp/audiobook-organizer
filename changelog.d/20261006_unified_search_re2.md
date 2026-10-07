### Fixed

#### Library search: `title:a*` and the other advertised filter syntax now actually work

The query `-metadata:applied -review:no_match -duration:<10m title:a*` returned 0 books. The server compared `title:a*` as a substring containing a literal asterisk. The search help also advertised forms that never worked on a field filter: `title:vamp*`, `author:a || author:b`, `author:smith~`, `format:(m4b|mp3)`, `year:>2020`, `year:[2015 TO 2020]` and `progress_pct:>75`. Each of them quietly returned 0 books.

### Added

#### One search grammar based on RE2 regular expressions (Library now, Review Title filter next)

These value forms work in every `field:value` filter and are evaluated server-side over the whole library:

- `word` matches anywhere in the field.
- `"quoted"` is literal text.
- `/RE2/` is a regular expression. It ignores case unless you add `(?-i)`, and it may contain spaces.
- `a*`, `*a` and `*a*` are wildcards over the whole value.
- `*` means the field has any value.
- Prefix `-` or `NOT` to negate any form.

Numeric fields such as year, bitrate and series_number also accept comparisons (`>2020`, `<64`) and ranges (`[2015 TO 2020]`). An unknown value never matches a comparison.

An invalid regex or a malformed comparison now returns a 400 that names the token. The error appears under the search box and in place of the book list. Background operations that resolve a filter fail instead of selecting 0 books.

The grammar is implemented once in Go (`internal/querygrammar`) and once in TypeScript (`web/src/utils/queryGrammar.ts`). The TypeScript copy pre-checks queries in the browser and translates RE2 syntax to JavaScript regex syntax.
