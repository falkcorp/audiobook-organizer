## `SearchBooks` compares a NORMALIZED author name against a raw lower-cased query

`PebbleStore.SearchBooks` (`internal/database/pebble_store.go`) builds its author map with
`util.NormalizeAuthor(a.Name)` but matches against `strings.ToLower(query)`. Any transform
`NormalizeAuthor` applies beyond lower-casing — punctuation stripping, `Last, First`
reordering — is applied to one side of the comparison only, so author matches silently
fail for exactly the names that need normalizing most.

Noticed while adding dedup search (PR for `feat/dedup-server-side-search`), which
deliberately did NOT touch it: `SearchBooks` is a shared `BookSearchReader` method with
other callers, and changing its matching changes their results too.

- [ ] Measure how many authors normalize to something other than their lower-cased name
- [ ] Decide whether the query should be normalized, or the stored side lower-cased only
- [ ] Check the other `SearchBooks` callers before changing the predicate

Related: `SearchBooks` also does not match `file_path`, which is why dedup search needed its
own resolver rather than reusing it.
