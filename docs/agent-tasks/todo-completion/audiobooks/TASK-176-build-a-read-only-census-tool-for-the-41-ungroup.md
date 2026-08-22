<!-- file: docs/agent-tasks/todo-completion/audiobooks/TASK-176-build-a-read-only-census-tool-for-the-41-ungroup.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5733a3cd-ac9e-4d66-8710-312ce53ab5fe -->
<!-- last-edited: 2026-08-21 -->

# TASK-176 — Build a read-only census tool for the 41 ungrouped-but-explicitly-non-primary books (TODO.md L3354)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · audiobooks subagent · **Why:** Small, targeted diagnostic query -- no design decision needed, just running a query and reading the result, but requires careful query construction to isolate exactly the anomalous population. · **Depends on:** none · **Wave:** 1

Source: `TODO.md` line 3354 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**41 ungrouped books are hidden anyway**" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-17-rescope.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/audiobooks-176-build-a-read-only-census-tool-for-the-41-ungroup" -b agent/audiobooks-176-build-a-read-only-census-tool-for-the-41-ungroup origin/main
cd "$REPO/.worktrees/audiobooks-176-build-a-read-only-census-tool-for-the-41-ungroup"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Write tools/cmd/orphan-nonprimary-census/main.go (read-only, modeled on reconcile-paths's CLI structure) that queries the store for books where VersionGroupID is nil/empty AND IsPrimaryVersion is explicitly false (not nil) -- this is the anomalous population, distinct from the already-explained 724 no-primary-group iTunes-importer books (a different code path, tracked at todo_line 3344/3340). For each match, record CreatedAt/UpdatedAt for correlation against known import/dedup/merge job runs.

## Background (verify before editing)

- This 41-book population is explicitly called out as 'unexplained' and distinct from the 724-book iTunes-importer bug, which always assigns SOME version_group_id (just fails to elect a primary within it) -- these 41 have NO group at all, a different code path entirely.
- internal/database/file_provenance.go (NOT 'internal/ledger', which does not exist -- correcting the prior scout's citation) is the real provenance-tracking package landed in commit 8f6d0d99, but it tracks FILE moves specifically, not general book-field writes -- it may not directly explain who set IsPrimaryVersion=false on these 41 rows; CreatedAt/UpdatedAt correlation against known job runs is the more directly applicable approach.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  sed -n '862,866p' internal/audiobooks/service_filtering.go   # shows `eff := s.IsPrimaryVersion == nil || *s.IsPrimaryVersion` -- nil is treated as primary/visible — nil IsPrimaryVersion is treated as primary (visible) at the post-filter call site -- so a hidden, ungrouped book's field must be explicitly false, not nil
  bfs 2>/dev/null; ls internal/ledger 2>&1   # No such file or directory — internal/ledger does NOT exist as a package -- the prior scout's provenance-tracking reference was wrong
  git show --stat=200 --format= 8f6d0d99 | grep '\.go '   # >=1 hit — lists the .go files of the ledger commit — the real provenance-chain work (commit 8f6d0d99) landed in internal/database/file_provenance.go instead
  head -12 tools/cmd/reconcile-paths/main.go   # shows the READ-ONLY dry-run CLI pattern (CSV report output, no DB writes) — a read-only CLI census precedent already exists under tools/cmd/ to model the new tool on
  ```

### Reuse — don't invent

- Use `reconcile-paths's read-only CLI structure (flag-driven, CSV output, optional SSH-to-prod mode) as the template for the new census tool` in `tools/cmd/reconcile-paths/main.go` (verify: `grep -n 'func main' tools/cmd/reconcile-paths/main.go`) — do NOT write a parallel helper.

## Step-by-step

1. Write tools/cmd/orphan-nonprimary-census/main.go following reconcile-paths's read-only CLI structure: accept --db/--api flags, filter for VersionGroupID == nil/empty AND IsPrimaryVersion != nil && *IsPrimaryVersion == false.
2. Run it against a snapshot/copy of the store data (never write) to get the actual list of book IDs (expect ~41, confirming or correcting the sampled figure).
3. For each match, output CreatedAt/UpdatedAt timestamps for correlation against known import/dedup/merge job runs (no ledger/provenance package directly tracks book-field writes, per the correction above -- timestamp correlation is the available approach).
4. Write findings to a new docs/audits/ file or todo.d fragment describing the root cause, once found, as a candidate for a targeted fix distinct from ElectMissingPrimaries (which only helps books that DO have a version group).

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_audiobooks_176.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- If the diagnostic finds MORE than 41 (or a different set) than the original sample suggested, treat the original 41 as a sampled/superseded figure rather than assuming the new count is wrong.

## Tests

- t
- o
- o
- l
- s
- /
- c
- m
- d
- /
- o
- r
- p
- h
- a
- n
- -
- n
- o
- n
- p
- r
- i
- m
- a
- r
- y
- -
- c
- e
- n
- s
- u
- s
- /
- m
- a
- i
- n
- _
- t
- e
- s
- t
- .
- g
- o
- :
-  
- T
- e
- s
- t
- S
- e
- l
- e
- c
- t
- s
- O
- n
- l
- y
- E
- x
- p
- l
- i
- c
- i
- t
- l
- y
- N
- o
- n
- P
- r
- i
- m
- a
- r
- y
- U
- n
- g
- r
- o
- u
- p
- e
- d
-  
- -
-  
- t
- a
- b
- l
- e
- -
- d
- r
- i
- v
- e
- n
-  
- o
- v
- e
- r
-  
- i
- n
- -
- m
- e
- m
- o
- r
- y
-  
- b
- o
- o
- k
-  
- r
- o
- w
- s
- ,
-  
- a
- s
- s
- e
- r
- t
- i
- n
- g
-  
- t
- h
- e
-  
- p
- r
- e
- d
- i
- c
- a
- t
- e
-  
- m
- a
- t
- c
- h
- e
- s
-  
- O
- N
- L
- Y
-  
- (
- V
- e
- r
- s
- i
- o
- n
- G
- r
- o
- u
- p
- I
- D
-  
- n
- i
- l
- /
- e
- m
- p
- t
- y
-  
- A
- N
- D
-  
- I
- s
- P
- r
- i
- m
- a
- r
- y
- V
- e
- r
- s
- i
- o
- n
-  
- !
- =
-  
- n
- i
- l
-  
- A
- N
- D
-  
- *
- I
- s
- P
- r
- i
- m
- a
- r
- y
- V
- e
- r
- s
- i
- o
- n
-  
- =
- =
-  
- f
- a
- l
- s
- e
- )
-  
- a
- n
- d
-  
- i
- n
-  
- p
- a
- r
- t
- i
- c
- u
- l
- a
- r
-  
- d
- o
- e
- s
-  
- N
- O
- T
-  
- m
- a
- t
- c
- h
-  
- a
-  
- b
- o
- o
- k
-  
- w
- i
- t
- h
-  
- I
- s
- P
- r
- i
- m
- a
- r
- y
- V
- e
- r
- s
- i
- o
- n
-  
- =
- =
-  
- n
- i
- l
-  
- (
- w
- h
- i
- c
- h
-  
- i
- n
- t
- e
- r
- n
- a
- l
- /
- a
- u
- d
- i
- o
- b
- o
- o
- k
- s
- /
- s
- e
- r
- v
- i
- c
- e
- _
- f
- i
- l
- t
- e
- r
- i
- n
- g
- .
- g
- o
- :
- 8
- 6
- 4
-  
- t
- r
- e
- a
- t
- s
-  
- a
- s
-  
- p
- r
- i
- m
- a
- r
- y
- )
-  
- n
- o
- r
-  
- o
- n
- e
-  
- t
- h
- a
- t
-  
- h
- a
- s
-  
- a
-  
- v
- e
- r
- s
- i
- o
- n
-  
- g
- r
- o
- u
- p
- .
-  
- E
- x
- t
- r
- a
- c
- t
-  
- t
- h
- e
-  
- p
- r
- e
- d
- i
- c
- a
- t
- e
-  
- i
- n
- t
- o
-  
- a
-  
- t
- e
- s
- t
- a
- b
- l
- e
-  
- f
- u
- n
- c
- t
- i
- o
- n
-  
- s
- o
-  
- t
- h
- e
-  
- c
- e
- n
- s
- u
- s
-  
- l
- o
- g
- i
- c
-  
- i
- s
-  
- v
- e
- r
- i
- f
- i
- a
- b
- l
- e
-  
- w
- i
- t
- h
- o
- u
- t
-  
- a
-  
- l
- i
- v
- e
-  
- s
- t
- o
- r
- e
- .

Anti-over-suppression: N/A

## How to test

```bash
go build ./... && go vet ./... && go test ./tools/cmd/orphan-nonprimary-census/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] The diagnostic report names all books matching the anomaly's exact criteria and, ideally, a common writer/job correlated with their CreatedAt/UpdatedAt.
- [ ] Anti-over-suppression: N/A
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./tools/cmd/orphan-nonprimary-census/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_audiobooks_176.md`.

## Commit message

```
feat(audiobooks): Build a read-only census tool for the 41 ungrouped-but-expli (TODO L3354)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `the artifact this task adds is present: re-run sed -n '862,866p' internal/audiobooks/service_filtering.go` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Corrects the prior scout's reuse target: 'internal/ledger' does not exist in this repo; the provenance work from commit 8f6d0d99 landed as internal/database/file_provenance.go and tracks file moves, not general book-field writes, so it likely does not directly explain this anomaly -- CreatedAt/UpdatedAt correlation is the practical approach instead.
