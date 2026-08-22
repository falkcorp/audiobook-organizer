<!-- file: docs/agent-tasks/todo-completion/organize/TASK-203-add-a-detection-only-counter-structured-log-for-.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8ce8f9b5-2eac-4eaf-ac72-eea45b4fe2b7 -->
<!-- last-edited: 2026-08-21 -->

# TASK-203 — Add a detection-only counter + structured log for generateTargetPath path collisions within one organize run (DEC-11)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet-class · organize subagent · **Why:** Small, well-scoped, but touches the concurrent whole-library organize worker pool (8 workers), so the shared collision map needs correct locking — not a haiku-safe mechanical change, but far short of opus-level risk since it is detection-only with zero behavior change. · **Depends on:** none · **Wave:** 5

Source: `TODO.md` line 90011 as of commit 46628240 (later edits shift lines) — re-find it with `sed -n '90011p' TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-19.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/organize-203-add-a-detection-only-counter-structured-log-for-" -b agent/organize-203-add-a-detection-only-counter-structured-log-for- origin/main
cd "$REPO/.worktrees/organize-203-add-a-detection-only-counter-structured-log-for-"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a run-scoped collision counter to internal/organizer/service.go's organizeBooks (the 8-worker bounded pool driving every whole-library organize/rename run). After each worker computes newPath via OrganizeOneBook→ReOrganizeInPlace→GenerateTargetPath (service.go ~L1005), check a mutex-protected map[string]string (targetPath → first book ID that produced it, scoped to the lifetime of one organizeBooks call) for the SAME path already claimed by a DIFFERENT book.ID. On a hit, increment a new Prometheus counter (internal/metrics/metrics.go, mirroring the existing itunesLocationUnmappable detection-only pattern) and log a structured warning naming both book IDs and the shared path. No existing branch, return value, or file operation changes — the collision is purely observed and reported, exactly matching decision 11's DETECTION-ONLY / fix-deferred scope, and reusing the counter-naming convention already established by itunes_location_unmappable_total.

## Background (verify before editing)

- generateTargetPath/GenerateTargetPath are *Organizer methods (organizer.go:301,321), not free functions — the collision can only be detected at the caller that invokes them repeatedly across many books in one run, since the functions themselves are per-book and stateless.
- organizeBooks (service.go:943) is that caller: an 8-worker bounded pool (numWorkers=8, L950) iterating booksToOrganize, each worker computing newPath via OrganizeOneBook→ReOrganizeInPlace→GenerateTargetPath/GenerateTargetDirPath.
- The package already has a proven pattern for exactly this shape of change — itunesLocationUnmappable (metrics.go ~L109) is a Namespace:'audiobook_organizer' CounterVec with a small-enum label, registered once in Register() (metrics.go:168), incremented via a one-line helper (RecordITunesLocationUnmappable, metrics.go:182), used purely for detection with no behavior change — the same shape decision 11 and decision 9 (PH-2b) both call for.
- organizeBooks already has a cross-worker shared-state pattern to copy for the new map: statsMu sync.Mutex guarding the shared *Stats struct (service.go:947).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "func (o \*Organizer) generateTargetPath" internal/organizer/organizer.go   # 1 hit at L321 — generateTargetPath is a method, not a free function — the decision's grep pattern needs a receiver
  grep -n "func (o \*Organizer) GenerateTargetPath" internal/organizer/organizer.go   # 1 hit at L301 — GenerateTargetPath (exported passthrough) is what all batch/whole-run callers use
  grep -n "func (orgSvc \*Service) organizeBooks\|numWorkers = 8" internal/organizer/service.go   # 2 hits: organizeBooks at L943, numWorkers=8 at L950 — organizeBooks is the whole-run batch driver, an 8-worker bounded pool iterating booksToOrganize
  grep -rn "prometheus.NewCounter" internal/organizer   # 0 hits — no prometheus counter exists in internal/organizer today
  grep -n "itunesLocationUnmappable = prometheus.NewCounterVec\|func Register()" internal/metrics/metrics.go   # 2 hits: itunesLocationUnmappable ~L109, Register() ~L168 — itunesLocationUnmappable is the sibling 'detection-only counter, no behavior change' pattern to copy, including its Register() wiring
  ```

### Reuse — don't invent

- Use `itunesLocationUnmappable + RecordITunesLocationUnmappable (CounterVec + helper pattern to copy)` in `internal/metrics/metrics.go` (verify: `grep -n "func RecordITunesLocationUnmappable" internal/metrics/metrics.go`) — do NOT write a parallel helper.
- Use `organizeBooks' existing statsMu sync.Mutex pattern for cross-worker shared state` in `internal/organizer/service.go` (verify: `grep -n "var statsMu sync.Mutex" internal/organizer/service.go`) — do NOT write a parallel helper.

## Step-by-step

1. In internal/metrics/metrics.go, add a new CounterVec near itunesLocationUnmappable (after L115): organizeTargetPathCollision = prometheus.NewCounterVec(prometheus.CounterOpts{Namespace: "audiobook_organizer", Name: "organize_target_path_collision_total", Help: "Total times generateTargetPath produced a path already claimed by a DIFFERENT book within the same organize run (detection-only; the fix is deferred — see docs/plans/DECISIONS-PENDING.md row 11)"}, []string{}) — no labels needed (a single counter is enough; do not label by path, unbounded cardinality).
2. Add it to the prometheus.MustRegister(...) call inside Register() (metrics.go:168-176).
3. Add a helper: func RecordOrganizeTargetPathCollision() { organizeTargetPathCollision.Inc() } next to RecordITunesLocationUnmappable (metrics.go ~L182).
4. In internal/organizer/service.go's organizeBooks (L943), declare a new shared, mutex-protected map right beside the existing `var statsMu sync.Mutex` (L947): `var pathMu sync.Mutex; claimedPaths := make(map[string]string, len(booksToOrganize))` (path → book ID).
5. Immediately after `newPath, err = orgSvc.OrganizeOneBook(workerOrg, &book, log)` (L1005) and only when `err == nil && newPath != ""`, add: lock pathMu, look up claimedPaths[newPath]; if present and != book.ID, call metrics.RecordOrganizeTargetPathCollision() and log.Warn("organize: target path collision — two different books would organize to the same path", "path", newPath, "book_id", book.ID, "other_book_id", claimedPaths[newPath]); else set claimedPaths[newPath] = book.ID; unlock. This must run BEFORE the existing oldPath==newPath / alreadyInRoot branching below (L1025+) so detection happens regardless of which branch the book takes, but must NOT alter any of those branches' behavior.
6. Import "github.com/falkcorp/audiobook-organizer/internal/metrics" in service.go if not already imported (grep -n '"github.com/falkcorp/audiobook-organizer/internal/metrics"' internal/organizer/service.go first — add only if the grep returns 0 hits).
7. Bump version headers on internal/metrics/metrics.go and internal/organizer/service.go.
8. Add a changelog fragment (changelog.d/, no header) describing the new detection-only counter.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_organize_203.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A book whose newPath equals its OWN oldPath (already correctly organized, the oldPath == newPath branch) must still be recorded as a claim in claimedPaths — otherwise a LATER book that collides with it would be missed.
- err != nil or newPath == "" (failed organize) must NOT be recorded as a claim — an empty/failed path cannot collide with anything.
- Concurrent workers computing the SAME newPath at the same instant must not both see 'not yet claimed' and both skip incrementing the counter — the check-then-set must happen under the same lock acquisition (no separate lock/unlock for check vs set).

## Tests

- internal/organizer/target_path_collision_test.go (new): TestOrganizeBooks_LogsCollisionWhenTwoBooksShareATargetPath — construct two books whose FolderNamingPattern/FileNamingPattern inputs are engineered to generate the identical target path (e.g. same Title/Author after sanitization) but different Book IDs; run organizeBooks; assert (a) BOTH books still organize successfully (no behavior change — one does not get skipped or blocked), and (b) the collision counter's value increases by exactly 1 (read via prometheus testutil.ToFloat64, or by asserting the log line was emitted if metrics aren't easily read in-test — check internal/metrics/metrics_test.go for the existing pattern used to assert counter values first).

Anti-over-suppression test: `N/A — this is a detection-only counter with no filter/guard/skip behavior to over-suppress; the collision counter must never influence which books organize or how (verified by the happy-path assertion in the one test above: both colliding books still organize successfully).` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/metrics/... ./internal/organizer/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] go test ./internal/organizer/... ./internal/metrics/... -run TestOrganizeBooks_LogsCollisionWhenTwoBooksShareATargetPath -count=1 exits 0.
- [ ] grep -n "organize_target_path_collision_total" internal/metrics/metrics.go returns >=2 hits (declaration + Register wiring).
- [ ] go build ./... && go vet ./... exits 0.
- [ ] Anti-over-suppression test: `N/A — this is a detection-only counter with no filter/guard/skip behavior to over-suppress; the collision counter must never influence which books organize or how (verified by the happy-path assertion in the one test above: both colliding books still organize successfully).` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/metrics/... ./internal/organizer/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_organize_203.md`.

## Commit message

```
feat(organize): Add a detection-only counter + structured log for generateTa (DEC-11)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

If this presence check already passes at HEAD — `grep -n "organize_target_path_collision_total" internal/metrics/metrics.go returns >=2 hits (declaration + Register wiring).` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

Decision 11's own suggested grep ('func generateTargetPath') doesn't match because the real target is a method with a receiver — same class of stale-grep issue seen in DEC-6's 'func newTestServer'. The fix itself (deduping the collision) is explicitly deferred per the decision; this brief is detection-only.
