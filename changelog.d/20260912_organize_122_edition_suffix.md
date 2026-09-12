### Added

#### `{edition_suffix}` folder/file naming-pattern token

Naming patterns gain an `{edition_suffix}` placeholder that renders `" (Unabridged)"`
(a leading space plus the book's edition in parentheses) when the book has an
edition, and nothing at all when it does not. The raw `{edition}` token could not
be wrapped safely: `{title} ({edition})` was fine for books with an edition but
relied on the empty-parens cleanup for the rest. With the new token a folder
pattern such as `{author}/{series}/{title}{edition_suffix} ({print_year})` places
two editions of the same title and print year in different directories instead of
colliding on one target (the second was skipped with `ErrTargetOccupied` and
never organized). The token is built after the trim pass in
`internal/organizer/pathbuild.go`, like `{series_prefix}`, so its separator
survives; a whitespace-only edition, or one that scrubs to whitespace, collapses to
nothing. Patterns that do not use the token expand byte-identically; the shipped
defaults are unchanged, so nothing moves until an operator opts in. The Settings
page lists the token and its preview renders it.
