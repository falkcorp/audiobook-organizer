<!-- file: docs/agent-tasks/todo-completion/maintenance/TASK-220-journal-every-duplicate-row-deletion-to-the-undo.md -->
<!-- version: 1.1.0 -->
<!-- guid: 9ae9e63c-aec0-4d1c-a0dd-846f8c07614c -->
<!-- last-edited: 2026-09-02 -->

# TASK-220 — Journal every duplicate-row deletion to the undo ledger and refuse to apply while a library.scan is active (DUPROW-3)

> **Status 2026-09-02:** 🟡 OPEN — still worth doing — CreateOperationChange still decl-only (deps.go:148, 1 hit in internal/plugins); ListActiveOperationsV2 absent from maintenance/deps.go though it exists iface_ops_v2.go:156; library.scan library_core_ops.go:48. Recommendation: keep — this is the safety gate before any apply=true run in prod; do before TASK-219.

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · maintenance subagent · **Why:** Row deletion on a prod data path with an owner decision still open; the journal must be complete enough to replay a deletion back, the guard must fail closed, and it crosses an interface boundary with an interfacebloat cap that makes the obvious edit illegal. · **Depends on:** none · **Wave:** 3 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 90032 as of commit 46628240 (later edits shift lines) — re-find it with `sed -n '90032p' TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-21.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/maintenance-220-journal-every-duplicate-row-deletion-to-the-undo" -b agent/maintenance-220-journal-every-duplicate-row-deletion-to-the-undo origin/main
cd "$REPO/.worktrees/maintenance-220-journal-every-duplicate-row-deletion-to-the-undo"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Make the EXISTING `maintenance.dedupe-book-file-rows` op safe to run in production by adding the two things it lacks: (1) every deleted row is journaled to the undo ledger via `store.CreateOperationChange` BEFORE the delete commits, carrying the full JSON of the row in OldValue so the deletion can be replayed back; and (2) an apply=true run REFUSES to start while a `library.scan` op is queued or running, checked through a new narrow `OperationQueueStore()` accessor on the maintenance StoreProvider (ListActiveOperationsV2). Keep apply=false as the default. Do NOT create a new op — the op exists at internal/plugins/maintenance/dedupe_book_file_rows.go:35 and is registered at plugin.go:44. Also record the still-open owner decision (deleting duplicate rows vs the standing never-delete-rows rule) in docs/operations/pending-prod-actions.md.

## Background (verify before editing)

- The op already ranks keepers (rankKeeper, L91), merges salvageable fields into the keeper and commits that BEFORE deleting donors (L339-355), and batch-deletes via DeleteBookFilesByIDs which is fail-closed on unresolvable IDs (L363-381). What it does NOT do is record what it deleted.
- No operation anywhere in internal/plugins/ writes an undo-ledger row: `grep -rn "CreateOperationChange" internal/plugins/` returns only the interface declaration at deps.go:114. The call-site pattern to copy lives in internal/organizer/service.go (e.g. L1559, L1568).
- CreateOperationChange is ALREADY in OpsStore via opsHousekeeping (deps.go:113-122), so the journal half needs no interface change at all.
- ListActiveOperationsV2 is NOT reachable from the plugin. OpsStore has exactly 8 embedded interfaces and opsHousekeeping has exactly 8 declared methods, and .golangci.yml:64 sets interfacebloat max: 8 — so adding the method to either one breaks the lint gate. The compliant move is a NEW narrow accessor on StoreProvider (which has 5 methods today), exactly as MetadataCacheStore and FileProvenanceStore did.
- StoreProvider is embedded in ServerDeps (deps.go:369), which has exactly two direct implementors: *Server (internal/server/server_maintenance_deps.go:32) and fakeDeps (internal/plugins/maintenance/title_backfill_test.go:129). t09OpsFakeDeps (backfill_ops_test.go:24) and provDeps (file_provenance_capture_test.go:22) both EMBED fakeDeps and inherit automatically.
- *Server's store field is `database.Store` (internal/server/server.go:165), which embeds operationsStore -> OpsV2Store, so `return s.store` satisfies the new accessor with no other change.
- database.MockStore already implements ListActiveOperationsV2 as a no-hook stub returning (nil, nil) at internal/database/mock_store.go:2791, so mock-backed tests see 'no active ops' by default.
- registry.OperationDef already has a `DependsOn []string` field enforced by dispatcher Gate 4 (dispatcher.go:135), but it only DEFERS DISPATCH while the named def is RUNNING — it does not see QUEUED rows and it cannot be conditional on params.Apply. It is worth adding as defence in depth but is NOT a substitute for the in-run refusal.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "func (p \*Plugin) runDedupeBookFileRows" internal/plugins/maintenance/dedupe_book_file_rows.go   # 1 hit at L157 — the repair op exists with apply=false as its default and does the keeper/salvage/delete work already
  grep -rn "CreateOperationChange" internal/plugins/ --include='*.go'   # 1 hit: internal/plugins/maintenance/deps.go:114 (declaration only) — NO op in internal/plugins/ calls CreateOperationChange — the only hit is the interface declaration
  grep -n "CreateOperationChange(change \*database.OperationChange) error" internal/plugins/maintenance/deps.go   # 1 hit at L114, inside opsHousekeeping which OpsStore embeds (OpsStore at L127) — CreateOperationChange is ALREADY reachable from the maintenance ops store, so no widening is needed for the journal half
  grep -n "ListActiveOperationsV2() (\[\]OperationV2Row, error)" internal/database/iface_ops_v2.go   # 1 hit at L140 — ListActiveOperationsV2 returns queued+running ops and is the mechanism for the scan guard
  grep -n "ListActiveOperationsV2" internal/plugins/maintenance/deps.go   # 0 hits — ListActiveOperationsV2 is NOT reachable from the maintenance plugin today
  grep -n "ID:              \"library.scan\"," internal/server/library_core_ops.go   # 1 hit at L68 — the def ID to guard against is exactly 'library.scan'
  grep -n "func ReporterOpID" internal/operations/registry/reporter.go   # 1 hit at L48 — an op can learn its own operation id through registry.ReporterOpID, which degrades to "" for a fake reporter
  grep -n "type OperationChange struct" internal/database/store.go   # 1 hit at L581 — OperationChange is the undo-ledger row type with ID/OperationID/BookID/ChangeType/FieldName/OldValue/NewValue
  grep -rn "var _ ServerDeps" internal/ --include='*.go'   # 3 hits: internal/plugins/maintenance/backfill_ops_test.go:52, internal/plugins/maintenance/title_backfill_test.go:129, internal/server/server_maintenance_deps.go:32 — there are exactly two ServerDeps implementors to update if StoreProvider grows a method: *Server and the fakeDeps test fake (t09OpsFakeDeps and provDeps embed fakeDeps)
  grep -n "max: 8" .golangci.yml   # 1 hit at L64 (settings.interfacebloat.max) — the interfacebloat linter caps declared entries at 8 and OpsStore is already at exactly 8 embeds, so ListActiveOperationsV2 must NOT be added to OpsStore
  grep -n "FileProvenanceStore() database.FileProvenanceStore" internal/plugins/maintenance/deps.go internal/server/server_maintenance_deps.go   # 2 hits: deps.go:174 (declaration) and server_maintenance_deps.go:67 (implementation) — the established precedent for a narrow extra store accessor is MetadataCacheStore/FileProvenanceStore on StoreProvider
  grep -n "InsertOperationV2(row OperationV2Row) error" internal/database/iface_ops_v2.go   # 1 hit at L119 — a test can seed a fake running library.scan row directly
  grep -n "Gate 4: DependsOn" internal/operations/registry/dispatcher.go   # 1 hit at L135 — the dispatcher already has a Gate 4 that blocks dispatch while a named def is running — DependsOn
  ```

### Reuse — don't invent

- Use `CreateOperationChange journal-row pattern (ID via ulid.Make().String(), OperationID, BookID, ChangeType, FieldName, OldValue, NewValue)` in `internal/organizer/service.go` (verify: `grep -n "orgSvc.db.CreateOperationChange(&database.OperationChange{" internal/organizer/service.go`) — do NOT write a parallel helper.
- Use `StoreProvider narrow-accessor pattern (MetadataCacheStore / FileProvenanceStore)` in `internal/plugins/maintenance/deps.go` (verify: `grep -n "MetadataCacheStore() database.MetadataCacheStore" internal/plugins/maintenance/deps.go`) — do NOT write a parallel helper.
- Use `seedDupBooks + concurrentReporter + `p := &Plugin{deps: fakeDeps{store: s}}` PebbleStore fixture for this exact op` in `internal/plugins/maintenance/dedupe_book_file_rows_parallel_test.go` (verify: `grep -n "p := &Plugin{deps: fakeDeps{store: s}}" internal/plugins/maintenance/dedupe_book_file_rows_parallel_test.go`) — do NOT write a parallel helper.
- Use `GetOperationChanges / GetBookChanges — the readers a test uses to prove the journal landed` in `internal/database/iface_ops.go` (verify: `grep -n "GetOperationChanges(operationID string)" internal/database/iface_ops.go`) — do NOT write a parallel helper.

## Step-by-step

1. internal/plugins/maintenance/deps.go — add a new one-method interface next to the other narrow ones and a new accessor on StoreProvider:

// OpQueueReader is the single method the dedupe repair needs to see whether a
// library scan is in flight. It is its OWN accessor rather than a method on
// OpsStore because OpsStore is already at the interfacebloat cap of 8 embeds
// (.golangci.yml settings.interfacebloat.max) — the same reason
// MetadataCacheStore and FileProvenanceStore have their own accessors.
type OpQueueReader interface {
	ListActiveOperationsV2() ([]database.OperationV2Row, error)
}

and inside `type StoreProvider interface` (L154-175) add:
	// OperationQueueStore serves the dedupe-book-file-rows scan guard: an apply
	// run must refuse while library.scan is queued or running, because a scan
	// concurrently rewrites the same book_file rows this op deletes.
	OperationQueueStore() OpQueueReader
2. internal/server/server_maintenance_deps.go — add the implementation next to the existing accessors (around L54-67):

// OperationQueueStore implements maintenance.StoreProvider.
func (s *Server) OperationQueueStore() maintenanceplugin.OpQueueReader { return s.store }

Use whatever the file's existing import alias for the plugin package is (grep -n "maintenanceplugin" internal/server/server_maintenance_deps.go).
3. internal/plugins/maintenance/title_backfill_test.go — add the matching fake method next to the other four store accessors (near L44-58):

func (d fakeDeps) OperationQueueStore() OpQueueReader { return d.store }

`d.store` is typed database.Store, which already has the method, so nothing else changes. Verify `var _ ServerDeps = fakeDeps{}` at L129 still compiles.
4. internal/plugins/maintenance/dedupe_book_file_rows.go — add the scan guard at the top of runDedupeBookFileRows, AFTER params are unmarshalled and the store nil-check (currently L157-169), and BEFORE PASS 1:

	// 🔴 FAIL CLOSED. A library.scan rewrites the same book_file rows this op
	// deletes, so a concurrent scan can resurrect a row we just collapsed or
	// mutate the keeper mid-flight. Applies refuse; a dry run is read-only and
	// is allowed through so an operator can still take the census during a scan.
	if params.Apply {
		qs := p.deps.OperationQueueStore()
		if qs == nil {
			return fmt.Errorf("dedupe-book-file-rows: cannot verify no library.scan is active; refusing to apply")
		}
		active, aerr := qs.ListActiveOperationsV2()
		if aerr != nil {
			// Fail CLOSED: an unreadable queue is not proof of an idle queue.
			return fmt.Errorf("dedupe-book-file-rows: cannot list active operations; refusing to apply: %w", aerr)
		}
		for _, op := range active {
			if op.DefID == "library.scan" && (op.Status == "running" || op.Status == "queued") {
				return fmt.Errorf("dedupe-book-file-rows: library.scan is %s (op %s); refusing to apply — a running scan rewrites the rows this op deletes", op.Status, op.ID)
			}
		}
	}
5. internal/plugins/maintenance/dedupe_book_file_rows.go — add `DependsOn: []string{"library.scan"}` to dedupeBookFileRowsDef() (L36-66), with a comment saying it is defence in depth: dispatcher Gate 4 (internal/operations/registry/dispatcher.go:135) defers DISPATCH while a scan RUNS, but does not see QUEUED scans and cannot be conditional on params.Apply, so the in-run refusal in step 4 is the real guard.
6. internal/plugins/maintenance/dedupe_book_file_rows.go — journal every deletion. Inside the RunItems callback, the group loop already accumulates `pendingDeletes` (L358-360). Change it to also accumulate the FULL rows so they can be serialized: add `var pendingRows []database.BookFile` next to `var pendingDeletes []string` (L294) and append `redundant[ri]` alongside each ID.

Then, in the batched-delete block (L368-381), journal BEFORE calling DeleteBookFilesByIDs:

	if len(pendingDeletes) > 0 {
		// 🔴 JOURNAL FIRST. The ledger row must exist before the delete commits:
		// a journal written after a delete is lost exactly when the process dies
		// between the two, which is the only case it exists for. A journal write
		// that FAILS aborts this book's delete — an unreplayable deletion is worse
		// than a duplicate row that survives to the next run.
		journalOK := true
		for ri := range pendingRows {
			blob, merr := json.Marshal(pendingRows[ri])
			if merr != nil { journalOK = false; break }
			if jerr := store.CreateOperationChange(&database.OperationChange{
				ID:          ulid.Make().String(),
				OperationID: opID,          // from registry.ReporterOpID(reporter), captured once before RunItems
				BookID:      bookID,
				ChangeType:  "book_file_delete",
				FieldName:   pendingRows[ri].ID,
				OldValue:    string(blob),   // the ENTIRE row, so the deletion can be replayed back
				NewValue:    "",
			}); jerr != nil {
				log.Warn("dedupe-book-file-rows: journal write failed; leaving this book's rows intact",
					"book_id", bookID, "row", pendingRows[ri].ID, "err", jerr)
				journalOK = false
				break
			}
		}
		if !journalOK {
			mu.Lock(); failed++; mu.Unlock()
		} else if derr := store.DeleteBookFilesByIDs(pendingDeletes); derr != nil {
			... existing failure branch unchanged ...
		} else {
			... existing success branch unchanged ...
		}
	}

Add imports: "github.com/oklog/ulid/v2" (match the exact import path already used in internal/organizer/service.go — check it with `grep -n "ulid" internal/organizer/service.go`). encoding/json is already imported.
7. internal/plugins/maintenance/dedupe_book_file_rows.go — capture the op id ONCE before the RunItems call: `opID := registry.ReporterOpID(reporter)` (registry is already imported for RunItems). Log a WARN if it is empty (`"dedupe-book-file-rows: no operation id; journal rows will be uncorrelated"`) but do NOT refuse — ReporterOpID degrades to "" for fakes by design (internal/operations/registry/reporter.go:43-47).
8. Bump `// version:` and `// last-edited:` headers on internal/plugins/maintenance/dedupe_book_file_rows.go (1.4.0 -> 1.5.0), internal/plugins/maintenance/deps.go (1.10.0 -> 1.11.0), and internal/server/server_maintenance_deps.go.
9. docs/operations/pending-prod-actions.md — append an entry (do not replace existing content): op id `maintenance.dedupe-book-file-rows`, params `{"apply": true}`, the OPEN OWNER DECISION (deleting duplicate book_file rows conflicts with the standing 'never delete rows in any repair; REPOINT' rule), the mitigation now in place (every deletion journaled as change_type book_file_delete with the full row JSON in old_value, replayable via GetOperationChanges(<op id>)), and the pre-run checklist (no library.scan queued or running; run apply=false first and review the TSV). Bump the file's version header. ALSO record the question flagged in notes below: the file's own comments describe a completed production run, which may mean rows were already deleted before this decision was made — that must be verified, not assumed.
10. Add a changelog fragment under changelog.d/ and, if a new follow-up task falls out (e.g. building the replay-from-journal tool), a todo.d/ fragment. NEITHER fragment gets a file/version/guid header.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_maintenance_220.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- ListActiveOperationsV2 returns an error → FAIL CLOSED and refuse the apply. An unreadable queue is not proof of an idle queue.
- OperationQueueStore() returns nil (a fake that did not wire it) → refuse the apply with a clear message; never silently proceed.
- registry.ReporterOpID(reporter) returns "" (fake reporter, or a reporter that does not implement OpID) → journal rows are still written with OperationID "". They are then uncorrelated but still carry the full row in OldValue, so replay by BookID via GetBookChanges still works. Log a WARN; do NOT refuse.
- json.Marshal of a BookFile failing → treat exactly like a journal write failure: skip this book's delete, count it as failed. Do not delete a row you could not serialize.
- A zombie 'running' library.scan row with no live handle (the C-3 case the registry itself guards against at registry.go:631) will make this op refuse. That is the SAFE direction — say so in the error message so an operator knows to clear the stale row rather than assume the op is broken.
- The scan guard is a point-in-time check: a library.scan enqueued one second later is not caught. DependsOn (step 5) narrows that window at dispatch. State this limitation in the code comment; do not claim the op is scan-proof.
- params.Apply == false must skip the guard entirely — a dry run is read-only and blocking it would remove the operator's only way to take a census during a long scan.

## Tests

- internal/plugins/maintenance/dedupe_book_file_rows_parallel_test.go (EXISTING file — append): `TestDedupeBookFileRows_ApplyJournalsEveryDeletedRow` — PebbleStore fixture (`database.NewPebbleStore(t.TempDir())` + `s.WaitForWarmup()`), `seedDupBooks(t, s, 3, 4)`, run with Apply:true, then for each book call `s.GetBookChanges(bookID)` and assert exactly 3 rows with ChangeType=="book_file_delete", each OldValue unmarshalling into a database.BookFile whose ID is one of the deleted ones and whose FilePath equals the book's seeded path. 9 journal rows total for 3 books x 3 deletions.
- internal/plugins/maintenance/dedupe_book_file_rows_parallel_test.go: `TestDedupeBookFileRows_JournalFailureLeavesRowsIntact` — wrap the store in a small test type embedding *database.PebbleStore that overrides CreateOperationChange to return an error; run with Apply:true; assert `s.GetBookFiles(id)` still returns all `copies` rows for every book and the op returned nil (it degrades, it does not abort the sweep). This is THE data-loss assertion: an unjournaled deletion must never happen.
- internal/plugins/maintenance/dedupe_book_file_rows_parallel_test.go: `TestDedupeBookFileRows_ApplyRefusesWhileLibraryScanRunning` — seed dup books, then `s.InsertOperationV2(database.OperationV2Row{ID:"scan-1", DefID:"library.scan", Status:"running"})`; run with Apply:true; assert the returned error is non-nil and its message contains "library.scan" and "refusing to apply", AND that `s.GetBookFiles(id)` still returns all `copies` rows for every book.
- internal/plugins/maintenance/dedupe_book_file_rows_parallel_test.go: `TestDedupeBookFileRows_ApplyRefusesWhileLibraryScanQueued` — identical but Status:"queued". Proves the guard covers the case dispatcher Gate 4 cannot see.
- internal/plugins/maintenance/dedupe_book_file_rows_parallel_test.go: ANTI-OVER-SUPPRESSION — `TestDedupeBookFileRows_ApplyProceedsWhenOnlyAnUnrelatedOpIsActive`: insert an active row with DefID "maintenance.title-repair" Status "running", run with Apply:true, assert the op returns nil AND every book collapsed to exactly 1 row. Without this a guard that refused on ANY active op — or on all applies — would pass every other test.
- internal/plugins/maintenance/dedupe_book_file_rows_parallel_test.go: `TestDedupeBookFileRows_DryRunIsAllowedDuringALibraryScan` — library.scan row inserted with Status "running", run with Apply:false; assert nil error and that no rows were deleted. The guard must be apply-only.
- internal/plugins/maintenance/dedupe_book_file_rows_test.go: `TestDedupeBookFileRowsDef_DependsOnLibraryScan` — pure assertion that `(&Plugin{}).dedupeBookFileRowsDef().DependsOn` contains "library.scan".

Anti-over-suppression test: `TestDedupeBookFileRows_ApplyProceedsWhenOnlyAnUnrelatedOpIsActive` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... ./internal/server/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... ./internal/server/... -count=1` exits 0.
- [ ] `go test -race ./internal/plugins/maintenance/... -run DedupeBookFileRows -count=1` exits 0 (the journal writes happen inside parallel workers).
- [ ] `grep -rn "CreateOperationChange" internal/plugins/ --include='*.go' | grep -v deps.go | wc -l` reports >= 1 — i.e. the plugin now actually calls it, where before the only hit was the declaration.
- [ ] `grep -n "refusing to apply" internal/plugins/maintenance/dedupe_book_file_rows.go` returns >= 2 hits (list failure and scan-active).
- [ ] `grep -n "OperationQueueStore" internal/plugins/maintenance/deps.go internal/server/server_maintenance_deps.go internal/plugins/maintenance/title_backfill_test.go` returns exactly 1 hit in each of the three files.
- [ ] `grep -n "dedupe-book-file-rows" docs/operations/pending-prod-actions.md` returns >= 1 hit.
- [ ] golangci-lint on the changed packages reports no `interfacebloat` finding (OpsStore stayed at 8 embeds; the new method went on StoreProvider, which goes 5 -> 6).
- [ ] Anti-over-suppression test: `TestDedupeBookFileRows_ApplyProceedsWhenOnlyAnUnrelatedOpIsActive` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/plugins/maintenance/... ./internal/server/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_maintenance_220.md`.

## Commit message

```
feat(maintenance): Journal every duplicate-row deletion to the undo ledger and  (DUPROW-3)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If this presence check already passes at HEAD — ``grep -rn "CreateOperationChange" internal/plugins/ --include='*.go' | grep -v deps.go | wc -l` reports >= 1 — i.e. the plugin now actually calls it, where before the only hit was the declaration.` — this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

NOT A NEW OP. maintenance.dedupe-book-file-rows already exists at internal/plugins/maintenance/dedupe_book_file_rows.go:35 (459 lines, v1.4.0, registered plugin.go:44) with apply=false default, a reviewed keeper order (rankKeeper L91), field salvage-before-delete ordering (L339-355), and a bounded RunItems worker pool. This brief adds ONLY the journal and the scan guard. ⚠️ VERIFY, DO NOT ASSUME: the file's own comments describe a production run that already collapsed rows ('its 130 rows were collapsed', 'the first full production run was killed at book 19/194', 'a later full-library dry run confirmed it'). If accurate, rows were deleted BEFORE the owner decision this item asks for — that changes what the pending-prod-actions.md entry should say, and it means unjournaled deletions may already exist with no replay path. Check the prod operation history for this def id before writing that entry, and report what you find rather than asserting either way. FILE COLLISION: this edits the same file as todo_line 90031 — land this one FIRST (it is the review_critical one) and rebase 90031 on top; never run them in parallel worktrees. Known flaky: internal/plugins/maintenance is on the repo's flaky list, so re-run before concluding a red test is a regression.
