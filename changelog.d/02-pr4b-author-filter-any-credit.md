### Added

#### `author:` matches every credited author, not only the primary one

`author:` in Library and Review filters now matches a book when the pattern
matches any of its author-role credits (author, co-author, primary, or an
unlabelled credit), so co-authored books no longer disappear from author
filters. Narrators, translators, editors and other contributors credited on a
book are not its authors and do not match. `-author:` excludes a book only when
none of its author credits match, and a regex is tried against each name on
its own, never against the names joined. `narrator:` is unchanged.

Credits are loaded once per query, not once per book. A stored credit list that
cannot be decoded costs only its own book its co-credits (logged once per bad
row) instead of turning every author filter back to primary-only. Changing a
book's credits now also refreshes cached library pages, so an `author:` page
picks up a newly added co-author at once instead of after the cache expires.
