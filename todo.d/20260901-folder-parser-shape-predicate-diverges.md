- [ ] **Unify `folder_parser.looksLikeAuthorSegment` with `personname.LooksLikePersonName`**
      — `internal/metadata/folder_parser.go` is the third path→author parser. Its
      placeholder gap is now closed (PR #3035), but its SHAPE predicate still
      diverges from the shared one on named input classes:
      2–5 words rather than 2–4; an ASCII-only `w[0] < 'A' || w[0] > 'Z'`
      first-letter test that drops every caseless script (CJK, Hebrew, Arabic,
      Thai) `personname` deliberately keeps; and early `return true` on `","` and
      `" & "` that skip the shape check entirely.
      So a Cyrillic or CJK author directory passes the shared predicate and fails
      this one, while `"Discworld, Mort"` and `"anything & anything"` pass this one
      and fail the shared one.
      **Cost, stated up front:** this changes answers on those three classes, so it
      needs its own differential corpus over real library paths — not a
      compile-and-green. Consumers to re-verify: `scanner.go:1245`, `scanner.go:1329`,
      `internal/importer/service.go:208`, plus `folder_parser_test.go`.
