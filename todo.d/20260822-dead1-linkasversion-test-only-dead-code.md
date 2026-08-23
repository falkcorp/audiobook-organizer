### 🧹 DEAD-1 residue — `linkAsVersion` is dead production code

- [ ] **Remove `Importer.linkAsVersion`** (`internal/itunes/service/importer.go:1780`) and the
      two tests that are its only callers. Spun out of the 2026-05-01 re-audit close-out
      (item 42), where it is the one DEAD-1 symbol that was never actually removed.

  **Why it was missed.** DEAD-1 (= R-5 in `docs/archive/codebase-evaluation.md:107`) named
  four unused symbols. The close-out grep covered three of them —
  `legacySaveConfigToDatabase_REMOVED`, `bookTagKeyspace`,
  `bookSummarySelectColumnsQualified` — got 0 hits, and treated that as the whole answer.
  The fourth, `linkAsVersion`, was never in the grep and is still there. Re-verified at HEAD
  `95d6db6ee`.

  **Exact extent (`gopls references`, not a name grep).** `linkAsVersion` has **2**
  references, total, and both are tests:

  - `internal/itunes/service/importer.go:1780` — the declaration
  - `internal/itunes/service/importer_error_paths_test.go:531` — direct call
  - `internal/itunes/service/importer_error_paths_test.go:562` — direct call

  Zero production callers on any path. It lost its last real caller somewhere after
  `4207faf3b` moved the `Importer` into `itunesservice`, and `89cc3db1d` (TODO 4.13d error
  and edge-case coverage) then wrote tests against the orphan.

  **Why `staticcheck` will not find this for you.** U1000 counts in-package test usage as
  usage, so an unexported function exercised only by its own package's tests is invisible to
  it. `staticcheck -checks SA4006,U1000 ./internal/itunes/...` is clean at HEAD and that
  proves nothing here. Only symbol resolution (`gopls references`) answers the question — do
  not re-scope this task from a clean linter run.

  **Removal is not free of judgement.** Deleting the function also deletes two passing tests
  (`TestLinkAsVersion_CreatesVersionBook` and
  `TestLinkAsVersion_ExistingHasNoVGID_CreatesVGID`) and will drop
  `internal/itunes/service` coverage. That is correct — coverage of unreachable code is not
  coverage — but confirm first that the *behaviour* it implements (version-linking an
  imported book onto an existing primary's `VersionGroupID`) is genuinely reached by another
  path, and not a feature that was silently dropped when the caller went away. If it turns
  out to be a lost feature rather than dead code, this becomes a bug report, not a deletion.
  Note `docs/AI-REFERENCE.md:457` still documents it as live behaviour.

  **Before removing, re-run the reference check** — if a production caller has appeared since
  2026-08-22, keep the function. Gate: `go build ./...` + `make test`.
