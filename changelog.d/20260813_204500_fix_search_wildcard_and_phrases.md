### Fixed

- **Search: a trailing `*` no longer returns nothing.** Prefix and wildcard queries were
  built from the raw term, and those Bleve query types bypass the field analyser — so
  they were compared against already-lowercased index terms without being lowercased
  themselves. Any capitalised term matched zero documents: on production `Hyperion*`
  returned 0 while `hyperion*` returned 21, and `Dragon*` returned 0 while `dragon*`
  returned 1757. The web UI appends `*` to what was typed, so this fired on ordinary
  capitalised typing.
- **Search: a bare quoted phrase is now an actual phrase.** The free-text scanner split
  on whitespace before looking for quotes, so `"All Jobs"` became two tokens, `"All` and
  `Jobs"`, with the quote characters still attached; the analyser discarded them as
  punctuation and `Quoted` was never set, leaving the `MatchPhraseQuery` branch in the
  translator unreachable for bare free text. `"Side Jobs"` no longer matches
  *Jobs on the Side*. The field-scoped form (`title:"a b"`) was never affected.

  Known limitation, filed separately: a phrase whose distinguishing word is an English
  stopword still over-matches, because the analyser drops the stopword before matching —
  `"All Jobs"` reduces to `jobs`. Fixing that needs an analyser change and a full
  re-index.
