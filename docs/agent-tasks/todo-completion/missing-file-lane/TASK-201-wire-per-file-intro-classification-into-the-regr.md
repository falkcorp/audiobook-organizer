<!-- file: docs/agent-tasks/todo-completion/missing-file-lane/TASK-201-wire-per-file-intro-classification-into-the-regr.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9b8bf415-3d5e-479f-aaf8-164e98648df5 -->
<!-- last-edited: 2026-08-21 -->

# TASK-201 — Wire per-file intro classification into the regroup-shattered-books classifier, outranking runtime (TODO.md L8316)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · missing-file-lane subagent · **Why:** changing a signal's RANK in an existing classifier (making intro evidence outrank runtime) risks silently flipping verdicts on the 356 already-measured holds this item must validate against -- needs careful before/after diffing, not just a mechanical signal addition · **Depends on:** TASK-200 · **Wave:** 3 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 8316 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Per-file intro transcription as the primary book" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-18.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/missing-file-lane-201-wire-per-file-intro-classification-into-the-regr" -b agent/missing-file-lane-201-wire-per-file-intro-classification-into-the-regr origin/main
cd "$REPO/.worktrees/missing-file-lane-201-wire-per-file-intro-classification-into-the-regr"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

In internal/plugins/maintenance/regroup_shattered_ai.go's classification logic, add per-file intro classification (via ClassifyIntro, reading whatever per-file transcript store PR #2168 introduced) as a signal that OUTRANKS the existing runtime/duration proxy when both are available for a candidate book -- position is a weight, never a veto per ClassifyIntro's own documented design (a credits classification at file ordinal >0 IS the shattered-book signal, not something to discard). Validate the change by diffing its verdicts against the 356 holds already measured under the pure-runtime rule (find where that 356-hold measurement is recorded -- likely a fixture or a prior investigation doc -- and re-run the classifier with intro wired in to confirm no unexpected regression on that known set).

## Background (verify before editing)

- regroup_shattered_ai.go currently builds its classification input purely from durationSec := database.NormalizeDurationSec(files[i].FileSize, files[i].Duration) (L175) and assigns it into a DurationSec field (L183) -- there is no branch reading any transcript/intro state today.
- ClassifyIntro's own documented design (classify.go, per the TODO's parser summary) treats position as a weight, never a veto: 'credits at ordinal >0 IS the shattered-book signal, so vetoing it would hide the very finding this was built to surface' -- the regroup wiring must preserve this, not silently downgrade a mid-book credits hit.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'ClassifyIntro' internal/plugins/maintenance/regroup_shattered_ai.go   # 0 hits — the regroup classifier does not yet consult per-file intro classification at all
  grep -n 'DurationSec' internal/plugins/maintenance/regroup_shattered_ai.go   # at least 2 hits, including the durationSec computation around L175 and the DurationSec field assignment around L183 — duration is the classifier's current (proxy) signal, confirmed still live
  ```

### Reuse — don't invent

- Use `ClassifyIntro (credits/chapter/prose/unknown, per-file classification to consult per-file for each candidate book)` in `internal/transcribe/classify.go` (verify: `grep -n 'func ClassifyIntro' internal/transcribe/classify.go`) — do NOT write a parallel helper.

## Step-by-step

1. Locate the 356-hold measurement referenced by the TODO ('Validate by diffing against the 356 holds already measured under the runtime rule') -- search docs/handoffs, docs/plans, or the regroup test fixtures for a dataset or reference file recording this baseline.
2. In regroup_shattered_ai.go, after the existing DurationSec computation, add a lookup of per-file intro classification for the same candidate book (via whatever accessor PR #2168's per-file transcript storage exposes -- grep for it in internal/transcribe or internal/database).
3. When intro classification is available (not 'unknown'/absent) for a file, let it outrank the duration-based verdict for that book's classification decision -- implement as a priority override, not an averaging/blending, per the TODO's explicit 'outranking runtime where both exist' instruction.
4. When intro classification is absent (e.g. Tier 3 of the backfill in part 1 hasn't reached this book/file yet), fall back to the existing runtime-only behavior UNCHANGED -- per the TODO's own absent-value warning, an absent transcript must never be read as 'no shattering', only as 'no evidence yet'.
5. Re-run the classifier against the 356-hold baseline set found above and diff the verdicts; any changed verdict must be individually justifiable by the new intro evidence, not an unexplained side effect.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_missing-file-lane_201.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- a book with intro evidence pointing one way and duration pointing strongly the other way -- confirm the 'outrank' rule is unambiguous (intro wins outright when present, not a tie-break) per the TODO's explicit wording

## Tests

- i
- n
- t
- e
- r
- n
- a
- l
- /
- i
- t
- u
- n
- e
- s
- /
- s
- e
- r
- v
- i
- c
- e
- /
- f
- s
- _
- r
- e
- g
- r
- o
- u
- p
- _
- s
- h
- a
- p
- e
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
- C
- l
- a
- s
- s
- i
- f
- y
- S
- h
- a
- t
- t
- e
- r
- e
- d
- F
- o
- l
- d
- e
- r
- s
- _
- I
- n
- t
- r
- o
- O
- u
- t
- r
- a
- n
- k
- s
- R
- u
- n
- t
- i
- m
- e
- W
- h
- e
- n
- B
- o
- t
- h
- P
- r
- e
- s
- e
- n
- t
-  
- -
-  
- a
-  
- S
- h
- a
- t
- t
- e
- r
- B
- o
- o
- k
-  
- w
- h
- e
- r
- e
-  
- D
- u
- r
- a
- t
- i
- o
- n
- S
- e
- c
-  
- s
- a
- y
- s
-  
- '
- n
- o
- t
-  
- s
- h
- a
- t
- t
- e
- r
- e
- d
- '
-  
- b
- u
- t
-  
- a
-  
- m
- i
- d
- -
- b
- o
- o
- k
-  
- f
- i
- l
- e
- '
- s
-  
- i
- n
- t
- r
- o
-  
- c
- l
- a
- s
- s
- i
- f
- i
- e
- s
-  
- a
- s
-  
- c
- r
- e
- d
- i
- t
- s
- ;
-  
- a
- s
- s
- e
- r
- t
-  
- t
- h
- e
-  
- g
- r
- o
- u
- p
-  
- i
- s
-  
- f
- l
- a
- g
- g
- e
- d
- .
-  
- T
- e
- s
- t
- C
- l
- a
- s
- s
- i
- f
- y
- S
- h
- a
- t
- t
- e
- r
- e
- d
- F
- o
- l
- d
- e
- r
- s
- _
- F
- a
- l
- l
- s
- B
- a
- c
- k
- T
- o
- R
- u
- n
- t
- i
- m
- e
- W
- h
- e
- n
- I
- n
- t
- r
- o
- A
- b
- s
- e
- n
- t
-  
- -
-  
- w
- i
- t
- h
-  
- n
- o
-  
- p
- e
- r
- -
- f
- i
- l
- e
-  
- t
- r
- a
- n
- s
- c
- r
- i
- p
- t
- ,
-  
- a
- s
- s
- e
- r
- t
-  
- t
- h
- e
-  
- v
- e
- r
- d
- i
- c
- t
-  
- i
- s
-  
- i
- d
- e
- n
- t
- i
- c
- a
- l
-  
- t
- o
-  
- t
- h
- e
-  
- p
- r
- e
- -
- c
- h
- a
- n
- g
- e
-  
- c
- l
- a
- s
- s
- i
- f
- i
- e
- r
-  
- f
- o
- r
-  
- t
- h
- e
-  
- e
- x
- i
- s
- t
- i
- n
- g
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
- c
- a
- s
- e
- s
- .
-  
- D
- o
-  
- N
- O
- T
-  
- w
- r
- i
- t
- e
-  
- a
-  
- 3
- 5
- 6
- -
- h
- o
- l
- d
-  
- b
- a
- s
- e
- l
- i
- n
- e
-  
- t
- e
- s
- t
- :
-  
- n
- o
-  
- p
- e
- r
- -
- h
- o
- l
- d
-  
- v
- e
- r
- d
- i
- c
- t
-  
- d
- a
- t
- a
- s
- e
- t
-  
- e
- x
- i
- s
- t
- s
-  
- i
- n
-  
- t
- h
- e
-  
- r
- e
- p
- o
-  
- (
- t
- h
- e
-  
- n
- u
- m
- b
- e
- r
-  
- a
- p
- p
- e
- a
- r
- s
-  
- o
- n
- l
- y
-  
- a
- s
-  
- p
- r
- o
- s
- e
-  
- i
- n
-  
- T
- O
- D
- O
- .
- m
- d
- :
- 8
- 3
- 8
- 3
-  
- a
- n
- d
-  
- d
- o
- c
- s
- /
- c
- o
- n
- t
- i
- n
- u
- a
- t
- i
- o
- n
- /
- 2
- 0
- 2
- 6
- -
- 0
- 8
- -
- 0
- 6
- -
- p
- e
- r
- -
- f
- i
- l
- e
- -
- i
- n
- t
- r
- o
- -
- c
- o
- n
- t
- i
- n
- u
- a
- t
- i
- o
- n
- .
- m
- d
- :
- 1
- 2
- 4
- )
- .
-  
- I
- n
- s
- t
- e
- a
- d
- ,
-  
- e
- x
- t
- e
- n
- d
-  
- t
- h
- e
-  
- e
- x
- i
- s
- t
- i
- n
- g
-  
- t
- a
- b
- l
- e
-  
- i
- n
-  
- f
- s
- _
- r
- e
- g
- r
- o
- u
- p
- _
- s
- h
- a
- p
- e
- _
- t
- e
- s
- t
- .
- g
- o
-  
- a
- n
- d
-  
- a
- s
- s
- e
- r
- t
-  
- z
- e
- r
- o
-  
- v
- e
- r
- d
- i
- c
- t
-  
- c
- h
- a
- n
- g
- e
- s
-  
- o
- n
-  
- e
- v
- e
- r
- y
-  
- c
- a
- s
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
- n
- o
-  
- i
- n
- t
- r
- o
-  
- d
- a
- t
- a
- .

Anti-over-suppression test: `TestRegroupClassifier_FallsBackToRuntimeWhenIntroAbsent` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/itunes/service/... ./internal/plugins/maintenance/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/plugins/maintenance/... -run TestRegroupClassifier -count=1 -v passes
- [ ] the 356-hold baseline diff is reviewed and each change is individually explainable, not a blanket verdict shift
- [ ] go build ./... && go vet ./... exit 0
- [ ] Anti-over-suppression test: `TestRegroupClassifier_FallsBackToRuntimeWhenIntroAbsent` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/itunes/service/... ./internal/plugins/maintenance/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_missing-file-lane_201.md`.

## Commit message

```
refactor(missing-file-lane): Wire per-file intro classification into the regroup-shattere (TODO L8316)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

review_critical=true: this changes verdicts in a shattered-book detection/regroup path, which can drive book-splitting operations -- a wrong verdict shift has real data consequences. Depends on todo_line 8316 part 1 (tiered backfill) for meaningful coverage, though it can be implemented and tested against fixture data before the full backfill completes.
