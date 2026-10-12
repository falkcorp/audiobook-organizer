### Fixed

- `author:` in Library and Review filters now matches a book when the pattern matches any of its credited authors, not only the primary one, so co-authored books no longer disappear from author filters. `-author:` excludes a book only when none of its credits match, and a regex is tried against each name separately. `narrator:` is unchanged.
