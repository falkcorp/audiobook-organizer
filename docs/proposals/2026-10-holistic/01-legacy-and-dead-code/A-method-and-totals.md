<!-- file: docs/proposals/2026-10-holistic/01-legacy-and-dead-code/A-method-and-totals.md -->
<!-- version: 1.2.0 -->
<!-- guid: f0f01f26-8eb9-4e63-9a68-4673d7f3a25b -->
<!-- last-edited: 2026-10-08 -->

# Appendix A: Method, commands and measured totals

All tools ran read-only against the analysis worktree at `f7211eb39`, or against a `git archive HEAD` copy in the session scratchpad. Nothing was installed into the repo.

## Commands

| What | Command | Result |
|---|---|---|
| Unreachable Go funcs | `deadcode ./...` (x/tools v0.51.0, installed to scratch `GOBIN`) | 1,050 funcs / 12,526 lines (all files) |
| Same, tests as roots | `deadcode -test ./...` | 99 funcs / 1,184 lines |
| Tag/OS robustness | `deadcode -tags embed_frontend ./...`, no tags, and `GOOS=linux GOARCH=amd64 deadcode -tags embed_frontend,bench ./...` | identical output, all three |
| Line spans | scratch tool `span`: go/ast `FuncDecl` from doc comment to closing brace | see appendix B |
| staticcheck | `staticcheck -checks 'U1000,SA4*,SA9003,S1008,SA6002' ./...` | 3 empty branches, 1 unused method |
| Book.FilePath census | scratch tool `fieldref` (go/packages, typed selectors on `database.Book`) | 483 lines: 416 R / 67 W |
| Config census | scratch tool `fieldusage` on `config.Config` | appendix E |
| Route table | `git archive HEAD` to scratch + a throwaway test calling `router.Routes()` on `setupCredGuardServer` | 467 routes |
| Trial deletion | in the scratch copy: removed the NutsDB stack (kept `actTiers`, `actCompactableTiers`, `matchesFilter` in a new file), `internal/download`, `server/validators.go`, `server/{file_move,pipeline_checkpoint,deluge_importer_adapter}.go`; fixed 2 type switches | `go build ./...` exit 0; `diff -r` prod −3,345 / +53 = **−3,292 net**; tests −797; `go vet` fails only in the test files listed in P2 |
| Frontend | knip 5 (appendix D); TS compiler span script | 1,751 + 458 lines |

## Total, by definition

**Definition:** production lines (not `_test.go`, not test-support, not `pkg/`, not `internal/writeback/`) that the proposal would remove.

| Tier | Go | TS | Total | How measured |
|---|---|---|---|---|
| 1 Safe now | 2,449 | 2,225 | 4,674 | trial-deletion diff (file-level) + func spans for D3 and U1; knip files `wc -l` + TS spans; vite block 16 |
| 2 Per-item check | 3,162 | 0 | 3,162 | func spans (category D minus 12 test seams and minus `BackfillPebbleActivityToSQL`, which tier 3 already counts) |
| 3 Owner decision | 7,342 | 0 | 7,342 | func spans of category B (2,161) + `wc -l` of SQLite backend files (5,181) |
| See 03 | 705 | | 705 | func spans |
| **Total** | **13,658** | **2,225** | **15,883** | |

The tiers do not overlap: tier 1 uses file-level lines for the files it deletes whole (category A), tier 2 counts only category-D functions, and the one category-D function inside the SQLite backend files (16 lines) is moved to tier 3, where the SQLite files are counted by `wc -l`. Category B counts function spans, so whole-file deletion of `internal/download` would remove 953 lines rather than its 591 func-lines.

Test lines that go with the deletions: SQLite 5,373; `internal/download` 797; TS test-only files 194; total 6,364. Tests needing a port (not deletion) are listed in P2.

## Rule-4 checks applied

- String-dispatched ops: registry and scheduler register function values, so RTA follows them; no op-def handler appears in the unreachable list.
- Routes: a full route-table dump was cross-checked against `web/src`, `cmd/`, `scripts/` and `.claude/` (appendix C). AudioBooth is external, so routes are never called dead.
- Build tags: three tag/OS combinations gave identical results.
- Reflection: no `MethodByName`, `go:linkname` or `plugin.Open` in non-test code. RTA treats exported methods of types that escape to an interface as reachable, so method counts are a floor (D3 found that way).
- Generated mocks: excluded.
