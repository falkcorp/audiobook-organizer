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

#### Review → Metadata Title filter uses the same grammar

The Title box now takes exactly what `title:` takes in the Library. Plain text matches anywhere in the title, `a*` is a wildcard, `/re/` is an RE2 regex, and you can combine `title:` tokens with `-` to exclude. Plain text used to be read as a JavaScript regex; a regex now needs slashes. Lookahead and backreferences are reported as errors instead of running, because RE2 does not support them.

#### Numeric filters understand units: `file_size:>20mb`, `bitrate:<64k`, `sample_rate:>=44.1khz`

`file_size:>20mb` returned nothing, because the value was matched as text against the stored byte count. File size, bitrate, sample rate, channels, bit depth, series number, year and progress are now compared as numbers, using one comparison grammar. That grammar supports the operators `>`, `>=`, `<`, `<=`, `=` and `!=`, and ranges written `[a TO b]`.

File size accepts the k, mb, gb and tb suffixes (1024-based). Bitrate accepts `k`/`kbps`, and sample rate accepts `khz`. A book whose value is unknown never matches a comparison. A malformed value is reported under the search box.
