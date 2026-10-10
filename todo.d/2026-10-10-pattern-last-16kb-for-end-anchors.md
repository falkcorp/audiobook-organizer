- [ ] **PATTERN-LAST-16KB** Also run end-anchored search patterns over the last
      16 KB of a long field. A regex or `*` pattern only sees the first 16 KB
      (`querygrammar.MaxPatternInputBytes`), so a `$`, `\z` or leading-`*`
      pattern (`*the end`) can never match a field over 16 KB, and a negated
      filter such as `-description:*the end` keeps every such row
      (`internal/querygrammar/querygrammar.go`, `TextMatcher.MatchExact`).
      Fix: for `$`/`\z`-anchored regexes and globs with a leading `*`, run a
      second bounded match over the field's last 16 KB under the same
      `Budget`. Cost: one extra match per long row, only for those patterns.
