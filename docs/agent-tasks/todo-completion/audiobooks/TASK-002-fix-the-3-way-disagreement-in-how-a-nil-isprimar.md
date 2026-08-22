<!-- file: docs/agent-tasks/todo-completion/audiobooks/TASK-002-fix-the-3-way-disagreement-in-how-a-nil-isprimar.md -->
<!-- version: 1.0.0 -->
<!-- guid: a7515fad-2961-42ee-8175-12c9a421a3d1 -->
<!-- last-edited: 2026-08-21 -->

# TASK-002 — Fix the 3-way disagreement in how a nil IsPrimaryVersion is treated (matcher vs. pushdown vs. serialized field) (TODO.md L3348)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · audiobooks subagent · **Why:** Root cause is fully located (3 exact call sites); the fix is a mechanical unification of nil semantics, but on a prod-data-adjacent listing/filtering path that needs careful regression testing across both the memdb-pushdown and post-filter query strategies. · **Depends on:** none · **Wave:** 4 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 3348 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**`is_primary_version` in the payload disagrees wi" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-04.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/audiobooks-002-fix-the-3-way-disagreement-in-how-a-nil-isprimar" -b agent/audiobooks-002-fix-the-3-way-disagreement-in-how-a-nil-isprimar origin/main
cd "$REPO/.worktrees/audiobooks-002-fix-the-3-way-disagreement-in-how-a-nil-isprimar"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Pick ONE canonical rule for what a nil IsPrimaryVersion means (recommend: nil counts as primary/true, matching the existing memdb_summaries.go GetBookSummaries sort-path behavior and the majority of the codebase's existing 'historical Pebble fallback semantics' comment), then make all three sites agree: service_query.go:346 and :532 change from `!= nil && *...` to `== nil || *...`; and the DTO serialization at service_filtering.go:737 stops passing the raw nullable field and instead serializes the SAME effective boolean the filter used, so a client reading is_primary_version from the JSON body agrees with what the is_primary_version=true filter actually returned.

## Background (verify before editing)

- Production measured two independently-computed 'primary book' counts disagreeing (40,839 vs 35,108) — this 3-way nil-semantics split is the stated reason why.
- 5,731 books have no version_group_id and are returned by ?is_primary_version=true (via the nil-is-true path) while their own serialized field reads false — nothing is hidden by this specific defect, but any client trusting the field over the filter will disagree with the server for those 5,731 books.
- Referenced in project memory as project_is_primary_version_nil_divergence.md: 'STILL LIVE; #2449 masks it' — this scout's own code reading independently reproduces and locates the exact 3 divergent call sites.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  sed -n '862,866p' internal/audiobooks/service_filtering.go   # 'eff := s.IsPrimaryVersion == nil || *s.IsPrimaryVersion' — the post-filter path treats nil as primary (true)
  grep -n 'bPrimary := b.IsPrimaryVersion != nil && \*b.IsPrimaryVersion' internal/audiobooks/service_query.go   # 2 hits, at L346 and L532 — the pushdown/memdb path treats nil as NOT primary (false), twice
  sed -n '735,739p' internal/audiobooks/service_filtering.go   # 'IsPrimaryVersion: summary.IsPrimaryVersion,' — direct assignment, no nil-coalescing — the serialized DTO field is a raw passthrough of the nullable field, not the effective value
  ```

### Reuse — don't invent

- No existing helper identified; do not invent new constants for concepts that already have a name — grep first.

## Step-by-step

1. In internal/audiobooks/service_query.go, change line 346's `bPrimary := b.IsPrimaryVersion != nil && *b.IsPrimaryVersion` to `bPrimary := b.IsPrimaryVersion == nil || *b.IsPrimaryVersion`.
2. Apply the identical change at service_query.go:532.
3. In internal/audiobooks/service_filtering.go, find where the DTO's IsPrimaryVersion field is populated (line 737, `IsPrimaryVersion: summary.IsPrimaryVersion`) and change it to compute and serialize the effective boolean, e.g. `effPrimary := summary.IsPrimaryVersion == nil || *summary.IsPrimaryVersion; ... IsPrimaryVersion: &effPrimary,` — keep the field type as *bool (nullable) but never let it BE nil in the output once this normalization is applied, since the whole point is 'no more nil in the wire format that disagrees with the filter'.
4. Search for any OTHER `IsPrimaryVersion != nil` or `IsPrimaryVersion == nil` comparison in internal/audiobooks and internal/database (grep -rn 'IsPrimaryVersion' internal/audiobooks internal/database | grep -v _test) to confirm no fourth divergent site was missed.
5. Update memdb_summaries.go's existing nil-is-primary comment (the one describing 'historical Pebble fallback semantics') if it references the specific line numbers changed in steps 1-2, since this fix makes that comment's semantics the codebase-wide rule rather than a special case.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_audiobooks_002.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- Changing service_query.go's pushdown semantics (nil now = primary) changes which books an is_primary_version=true request returns — this is a real behavior change for however many currently-nil-and-therefore-excluded books exist via the pushdown path specifically; measure the delta before shipping, since it may need its own changelog note distinct from the 'field now agrees with filter' framing.
- Once the DTO always emits a concrete true/false, any frontend code checking `book.is_primary_version === null` or `=== undefined` as a signal will stop seeing that state — grep web/src for such checks before shipping.

## Tests

- internal/audiobooks/service_query_test.go: a book with IsPrimaryVersion=nil must now be INCLUDED by an is_primary_version=true filter through the pushdown path (previously excluded) — assert this directly, since it's a behavior change, not just a refactor.
- internal/server/handlers/audiobooks/*_test.go: assert a book response's serialized is_primary_version field is never null and always agrees with whether that book was included under an is_primary_version=true request.
- Anti-over-suppression: a book with an explicit IsPrimaryVersion=false must still be correctly EXCLUDED by is_primary_version=true in both the pushdown and post-filter paths — this fix must not accidentally make every book 'primary'.

Anti-over-suppression test: `service_query_test.go case: IsPrimaryVersion=false explicitly is still excluded by is_primary_version=true.` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/audiobooks/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] [ ] `go test ./internal/audiobooks/...` passes including the new cache/nil-semantics tests.
- [ ] [ ] `go test ./internal/audiobooks/... -run TestIsPrimaryVersion_NilAgreesAcrossFilterAndSerialization -v` exits 0 (new test: book with IsPrimaryVersion=nil and no version_group_id is included by is_primary_version=true AND its serialized field reads true).
- [ ] [ ] Anti-over-suppression test: `service_query_test.go case: IsPrimaryVersion=false explicitly is still excluded by is_primary_version=true.` — a known-good input still passes with the new guard active.
- [ ] [ ] Edge cases above hold.
- [ ] [ ] Gate green: `make ci (Go)` exits 0; `go vet`/lint clean.
- [ ] [ ] File headers bumped on every changed file.
- [ ] [ ] Changelog fragment present: `test -f changelog.d/20260821_audiobooks_002.md`.
- [ ] Anti-over-suppression test: `service_query_test.go case: IsPrimaryVersion=false explicitly is still excluded by is_primary_version=true.` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/audiobooks/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_audiobooks_002.md`.

## Commit message

```
fix(audiobooks): Fix the 3-way disagreement in how a nil IsPrimaryVersion is  (TODO L3348)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

Flagged in project memory as STILL LIVE and masked by #2449 — this scout's grep-verified 3-site root cause should let a fix proceed without further investigation.
