<!-- file: PLAN.md -->
<!-- version: 1.1.0 -->
<!-- guid: 7d21c8f4-3e05-4a97-b6d2-1c8f5a09e743 -->
<!-- last-edited: 2026-09-01 -->

# Plan — unify the metadata-provenance duplication

**Status: EXECUTED 2026-09-01.** Three places where reality differed from this
plan are recorded at the bottom under "What the plan got wrong" — the plan is
kept as written above so the difference is legible.

Branch `refactor/unify-metadata-provenance`, worktree `.worktrees/provenance`.
Next target after `internal/metastate` (#3025) and `internal/personname` (#3029),
both of which found real bugs hiding in the duplication.

## Goal

Remove the last measured duplication in the metadata-state cluster:
`buildMetadataProvenance` (79 lines, two copies) and `metadataFieldState`
(one copy that shadows `metafetch.MetadataFieldState`).

## What is actually there — verified by grep at `cb6136033`, not assumed

| symbol | locations | status |
|---|---|---|
| `buildMetadataProvenance` | `audiobooks/helpers.go:398`, `server/server_metadata.go:355` | identical but for one type name |
| `metadataFieldState` (audiobooks) | `helpers.go:45` | field-for-field, tag-for-tag identical to `metafetch.MetadataFieldState` |
| `database.MetadataFieldState` | `store.go:1077` | **NOT a copy — do not touch** |

Two findings that shape the plan:

1. **The `server` copy has ZERO production callers.** Its only caller is
   `server/tag_roundtrip_test.go:153`. It is dead production code kept alive by
   its own test — the same shape as `isInitialToken`, removed in #3029. The
   `audiobooks` copy carries both real callers (`service_single.go:64,181`).
2. **`database.MetadataFieldState` is a false positive.** It is the *stored row*
   type — `*string` JSON-encoded values plus `BookID`/`Field` — while the other
   two are the *decoded view* type (`any` values, no keys). Same name, different
   meaning. Anyone who greps the name and folds all three corrupts data. This is
   why the plan names files rather than symbols.

## Why `internal/metafetch` is the home, and not a new package

`audiobooks` and `server` **both already import `metafetch`**
(`audiobooks/organize.go:17`, `audiobooks/rename.go:15`,
`server/server_metadata.go:18`), and `metafetch` imports neither — so no new
dependency and no cycle in either direction. `metafetch` already owns the
canonical `MetadataFieldState`. A new leaf package would be justified only if a
cycle forced it; it does not, so this is a move, not an extraction.

## Files to change

1. `internal/metafetch/helpers.go` — add exported `BuildMetadataProvenance`
   (the `audiobooks` body verbatim, retyped to `MetadataFieldState`).
   Delete the deprecated `metadataFieldState` alias at `:167` if it has no users left.
2. `internal/audiobooks/helpers.go` — delete local `metadataFieldState` and
   `buildMetadataProvenance`; keep the narrowed mirror comment from #3025 accurate.
3. `internal/audiobooks/{service_single,service_mutation}.go` — retype ~8 uses.
4. `internal/server/server_metadata.go` — delete the dead copy.
5. `internal/server/tag_roundtrip_test.go` — call `metafetch.BuildMetadataProvenance`.
6. `changelog.d/` fragment — headerless.

## Ordered steps

1. Confirm the two bodies are identical modulo the type name
   (`git show`-extract both, `diff`) — **do not** assume from the earlier survey.
2. Move the function into `metafetch`, exported. Build.
3. Repoint `audiobooks`, delete its local type and function. Build.
4. Delete the `server` copy, repoint its test. Build.
5. `go build ./... && go vet ./... && gofmt -l ./internal/`.
6. Full suites for `audiobooks`, `server`, `metafetch`, `metadata`.

## Test strategy

- **The move must be behaviour-preserving, so prove it rather than assert it.**
  Before changing anything, capture `buildMetadataProvenance` output for a fixed
  set of books/metadata from **both** copies via a throwaway probe; after the
  move, re-run against the shared one and diff. This is the differential that
  #3029 shipped without, at the cost of two rounds of review.
- `internal/metadata/write_roundtrip_test.go:432` already asserts every
  `Metadata` field is represented. Confirm it still runs and still fails when a
  field is dropped — mutation-check it rather than trusting the green.
- Mutation-test the moved function once: drop one provenance field, confirm a
  test fails. If none does, the coverage is nominal and I will say so.

## Rollback

Single branch, no data migration, no config, no flags. `git revert` the merge, or
close the PR — nothing outside these six files changes.

## Explicitly NOT in scope

- `stringPtr` (×8) and `boolPtr` (×6). Trivial one-liners that cannot diverge
  harmfully; folding them is churn with no defect to point at.
- `database.MetadataFieldState` — see above.
- The two still-duplicated parsers (`parseFilenameForAuthor`,
  `extractAuthorFromDirectory`), flagged in `internal/authorname/authorname.go`.
  Separate change, and #3029 must land first since it touches both copies.

---

## What the plan got wrong

Recorded rather than edited away, because the gap between a plan and its
execution is the useful part.

**1. `buildMetadataProvenance` had an undeclared dependency: `nonEmpty`.** The
plan listed the files to change and did not notice that the function body calls a
5-line helper which ALSO existed twice (`internal/audiobooks/helpers.go`,
`internal/server/server_helpers.go`, byte-identical). Moving the function without
it would have created a THIRD copy — the exact outcome the change exists to
prevent. `metafetch.NonEmpty` is now canonical and both packages hold a one-line
`var nonEmpty = metafetch.NonEmpty`, so there is one implementation and none of
the 52 call sites had to change.

The plan explicitly scoped out `stringPtr`/`boolPtr` as "trivial one-liners that
cannot diverge harmfully". That reasoning was fine for them and wrong for
`nonEmpty`, on a distinction the plan did not draw: a helper the moved code
DEPENDS ON is not optional scope.

**2. `metafetch`'s deprecated `metadataFieldState` alias is not dead.** Step 1
said to "delete the deprecated alias at :167 if it has no users left". It has ~15,
across `metadata_state_service.go` and `helpers.go` itself. Checked rather than
assumed; left in place with its comment corrected to say so.

**3. The differential probe was worth more than the plan implied.** It is listed
as a test-strategy bullet, and it is the only reason "behaviour-preserving" is a
measurement here rather than a claim: six cases covering nil state,
override-beats-stored-beats-fetched, name threading and comparison values,
captured before the move and diffed after. **Byte-identical, 14,666 bytes.** The
probe is deleted; the result is this paragraph.

## Result, measured

| symbol | before | after |
|---|---|---|
| `buildMetadataProvenance` | 2 copies, 79 lines each | 1, in `metafetch`, exported |
| `metadataFieldState` (audiobooks) | a second struct definition | an alias to `metafetch.MetadataFieldState` |
| `nonEmpty` | 2 copies | 1, plus 2 aliases |
| `database.MetadataFieldState` | untouched — a different type, see above | untouched |
