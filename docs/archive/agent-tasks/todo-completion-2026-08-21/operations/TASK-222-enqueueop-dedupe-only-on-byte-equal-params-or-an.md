<!-- file: docs/agent-tasks/todo-completion/operations/TASK-222-enqueueop-dedupe-only-on-byte-equal-params-or-an.md -->
<!-- version: 1.1.0 -->
<!-- guid: 5c487f8b-d81d-4c0e-8659-5c217f75fa83 -->
<!-- last-edited: 2026-09-02 -->

# TASK-222 — EnqueueOp: dedupe only on byte-equal params (or an explicit opt-in), never silently drop a different request (ENQ-DEDUP-1)

> **Status 2026-09-02:** ✅ DONE — PR #2688 merged 2026-08-22 (656c73574).

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Opus-class · operations subagent · **Why:** One conditional, but it changes enqueue semantics for ~146 operation definitions across every plugin; getting the default backwards either re-creates the cron pile-up or keeps silently dropping user work. · **Depends on:** none · **Wave:** 4 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 90033 as of commit 46628240 (later edits shift lines) — re-find it with `sed -n '90033p' TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-21.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/operations-222-enqueueop-dedupe-only-on-byte-equal-params-or-an" -b agent/operations-222-enqueueop-dedupe-only-on-byte-equal-params-or-an origin/main
cd "$REPO/.worktrees/operations-222-enqueueop-dedupe-only-on-byte-equal-params-or-an"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Stop EnqueueOp from silently discarding a second request whose params differ from the active op's. In internal/operations/registry/registry.go, move the params marshal above the ConcurrencyKey dedup block and change the dedup condition so an active op is only reused when `def.DedupeQueuedRuns || def.Schedule != nil || bytes.Equal(rawParams, []byte(op.Params))`. Otherwise fall through and enqueue a NEW queued row and return the NEW op id — dispatcher Gate 3 (internal/operations/registry/dispatcher.go:107) already serializes the runs, so the second job runs after the first instead of vanishing. Add `DedupeQueuedRuns bool` to registry.OperationDef next to ConcurrencyKey (types.go:68); because sdk.OperationDef is a type alias (pkg/plugin/sdk/operation.go:11) this reaches every plugin op with no other change. Set it on ZERO defs today — the Schedule!=nil clause already covers the cron case the dedup was written for. Keep the C-3 zombie-row skip intact.

## Background (verify before editing)

- Prod incident 2026-08-21 23:49: metadata.batch-apply-cached (ConcurrencyKey "metadata.batch-apply-cached", internal/server/batch_apply_op.go:71, NOT batchable) is enqueued with explicit book_ids (batchApplyOpParams.BookIDs at batch_apply_op.go:37). Approving more books while a batch apply ran neither queued a second job nor grew the running one: EnqueueOp returned the running op's id at registry.go:638 and the new params were discarded. The caller saw success; the approved books were never applied.
- The dedup was written for cron pile-up — its own comment (registry.go:615-618) cites 'Active Operations panel shows Purge Soft-Deleted twice from one cron schedule + one maintenance.window pass'. Cron ticks enqueue identical params, so byte-equality preserves exactly that behaviour.
- Gate 3 in the dispatcher (dispatcher.go:107-114) already refuses to START a second op holding the same ConcurrencyKey, so a second QUEUED row is safe: it waits, then runs. ConcurrencyKey serializes RUNS; it was never supposed to drop QUEUE entries carrying different work.
- params are marshalled at registry.go:642-652, BELOW the dedup block at 619-641, so the comparison bytes do not exist yet at the point of the decision. The marshal must move up. The batchable branch above (registry.go:585-609) already marshals its own copy for subject extraction — after moving the marshal, reuse the single rawParams there too rather than marshalling twice.
- PER-DEF DECISION, stated as a rule over the full enumeration (146 defs with a non-empty ConcurrencyKey; see the enumeration anchor): CATEGORY A — the 8 defs whose params carry an explicit selection (metadata.batch-save, metadata.batch-apply-cached, library.bulk-write-back, library.organize, acoustid.fingerprint via fingerprint_rescan, maintenance.probe-directory-books via chapters_backfill, maintenance.regroup-shattered-ai, maintenance.bulk-write-back) → default false, and byte-inequality means a different selection QUEUES. CATEGORY B — the ~45 defs with a non-nil cron Schedule → covered by the Schedule!=nil clause, dedupe on defID alone exactly as today, because a cron tick must never pile up and a resumable cron op's stored params drift via checkpoint merge (UpdateOperationV2Params, iface_ops_v2.go:130). CATEGORY C — every remaining def (manual, parameterless or with fixed params) → default false; identical params still dedupe (a double-click), different params queue. DedupeQueuedRuns exists so a future CATEGORY C def whose params legitimately vary per tick can opt back into defID-only dedupe; it is set on NO def in this change.
- OperationV2Row.Params is a string (internal/database/iface_ops_v2.go:54) holding the marshalled JSON, so the comparison is bytes.Equal(rawParams, []byte(op.Params)).

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n "ConcurrencyKey != \"\"\|ListActiveOperationsV2()\|hasLiveHandle(op.ID)\|return op.ID, nil\|func (r \*Registry) EnqueueOp" internal/operations/registry/registry.go   # 5 hits: EnqueueOp at L573, the guard at L619, ListActiveOperationsV2 at L620, the C-3 zombie skip at L631, and `return op.ID, nil` at L638 — EnqueueOp's dedup block matches on defID only and returns the existing op id
  grep -n "// Marshal params." internal/operations/registry/registry.go   # 1 hit at L642 (the rawParams block runs L643-652, below the dedup at L619-641) — params are marshalled AFTER the dedup block, so the fix requires moving the marshal above it
  grep -n "Params            string" internal/database/iface_ops_v2.go   # 1 hit at L54 (OperationV2Row.Params) — the active-op row carries the stored params to compare against
  grep -n "Gate 3: ConcurrencyKey already running?" internal/operations/registry/dispatcher.go   # 1 hit at L107 — Gate 3 in the dispatcher already serializes RUNS by ConcurrencyKey, so a second QUEUED row cannot run concurrently
  grep -n "ConcurrencyKey:  \"metadata.batch-apply-cached\"" internal/server/batch_apply_op.go   # 1 hit at L71 (its params struct with BookIDs []string is at L37) — metadata.batch-apply-cached carries an explicit book_ids selection and is not batchable — the op from the 2026-08-21 incident
  grep -n "type OperationDef = registry.OperationDef" pkg/plugin/sdk/operation.go   # 1 hit at L11 — sdk.OperationDef is a type ALIAS of registry.OperationDef, so a field added to the registry type automatically reaches every plugin op
  grep -n "ConcurrencyKey: ops with same non-empty key serialize" internal/operations/registry/types.go   # 1 hit at L68 — the ConcurrencyKey field and its doc comment live here, which is where the new field goes
  grep -n "Schedule \*string // cron expression" internal/operations/registry/types.go   # 1 hit at L83 — OperationDef already has a Schedule field marking cron-driven defs — the population the dedup was written for
  grep -rn "ConcurrencyKey:" internal --include='*.go' | grep -v _test.go | wc -l   # 152 lines, of which 3 are non-defs (types.go:68 doc comment, server/maintenance_job_op.go:97 `policy.ConcurrencyKey` dynamic) and 3 are the empty string (server/library_core_ops.go:332, server/folder_autoscan_op.go:50, maintenance/job.go:131) → ~146 real defs with a non-empty key — FULL ENUMERATION of defs with a ConcurrencyKey
  grep -rn "BookIDs\|book_ids" internal/server/*_op*.go internal/plugins/*/*.go | grep "\[\]string \`json:"   # 8 hits: server/batch_save_op.go:31, server/batch_apply_op.go:37, server/library_writeback_op.go:22, server/library_core_ops.go:53 (libraryOrganizeParams -> library.organize), plugins/acoustid/fingerprint_rescan.go:29, plugins/maintenance/chapters_backfill.go:96, plugins/maintenance/regroup_shattered_ai.go:68, plugins/maintenance/write_back.go:22 — the CATEGORY A exception list — every def whose params carry an explicit selection and which must therefore QUEUE rather than dedupe
  grep -n "UpdateOperationV2Params(id string, params \[\]byte) error" internal/database/iface_ops_v2.go   # 1 hit at L130 — checkpoint state is merged back into a running op's stored params, so a running op's Params can drift from what was enqueued
  ```

### Reuse — don't invent

- Use `the existing C-3 zombie-row skip inside the dedup loop — KEEP IT, do not restructure around it` in `internal/operations/registry/registry.go` (verify: `grep -n "C-3: skip zombie rows" internal/operations/registry/registry.go`) — do NOT write a parallel helper.
- Use `subjectFromParams / the batchable early-return above the dedup block — the fix must sit BELOW it, untouched` in `internal/operations/registry/registry.go` (verify: `grep -n "if def.Batchable {" internal/operations/registry/registry.go`) — do NOT write a parallel helper.
- Use `existing registry test file and its fixtures` in `internal/operations/registry/registry_test.go` (verify: `grep -n "func Test" internal/operations/registry/registry_test.go`) — do NOT write a parallel helper.

## Step-by-step

1. internal/operations/registry/types.go — add the field immediately after ConcurrencyKey (L69) with a full doc comment:

	// DedupeQueuedRuns makes an enqueue reuse an already queued/running op for
	// this def REGARDLESS of params. Default false: an enqueue only dedupes when
	// its marshalled params are byte-identical to the active op's, so a request
	// carrying a different selection queues a second run instead of being
	// silently dropped (prod, 2026-08-21: approving more books while
	// metadata.batch-apply-cached ran discarded the new book_ids and reported
	// success). Cron-scheduled defs (Schedule != nil) already dedupe on def id
	// alone without setting this — see EnqueueOp. Set this only for a def whose
	// params legitimately vary between runs that must NOT both happen.
	DedupeQueuedRuns bool
2. internal/operations/registry/registry.go — move the params marshal ABOVE the dedup block. Cut the block currently at L642-652 (`// Marshal params.` through the `rawParams = json.RawMessage("{}")` else branch) and paste it immediately BELOW the batchable early-return (which ends around L609) and ABOVE the `// Dedupe:` comment at L611. Then delete the now-duplicated `rawParamsForSubject` marshal inside the batchable branch and use `rawParams` there instead — verify with `grep -n rawParamsForSubject internal/operations/registry/registry.go` that no reference survives.
3. internal/operations/registry/registry.go — change the dedup decision inside the loop. The loop currently reads: skip non-matching DefID, skip C-3 zombie rows, then log and `return op.ID, nil`. Insert the params check between the zombie skip and the return:

				// ConcurrencyKey serializes RUNS (dispatcher Gate 3); it was never
				// meant to drop QUEUE entries carrying different work. Only reuse
				// the active op when this request asks for the SAME thing.
				sameWork := def.DedupeQueuedRuns ||
					def.Schedule != nil || // cron tick: the pile-up case this dedup was written for
					bytes.Equal(rawParams, []byte(op.Params))
				if !sameWork {
					r.logger.Info("registry: active op exists but params differ — queueing a second run",
						"op_id", op.ID, "def_id", defID, "status", op.Status)
					continue
				}

Keep the existing C-3 zombie skip and the existing `r.logger.Info("registry: enqueue deduped — active op exists", ...)` line exactly as they are. Add "bytes" to the import block.
4. internal/operations/registry/registry.go — `continue` (not `break`) is deliberate: another active row for the same def might match byte-for-byte. Add a one-line comment saying so.
5. Confirm nothing downstream assumes EnqueueOp is idempotent per def. Run `grep -rn "EnqueueOp(" internal --include='*.go' | grep -v _test.go | wc -l` and skim the call sites for any that compare the returned id to a stored one; if one exists, note it in the PR body rather than changing it.
6. Bump `// version:` and `// last-edited:` on internal/operations/registry/types.go and internal/operations/registry/registry.go.
7. Add a changelog fragment under changelog.d/ (no file header in the fragment) describing the behaviour change: a second enqueue with different params now creates a second QUEUED op instead of returning the running one's id.

Then, always:
- Keep the change purely transform — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_operations_222.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- nil params vs an empty struct: both must marshal to the same bytes before comparison (the existing code already substitutes json.RawMessage("{}") for nil — keep that, and do the substitution BEFORE the comparison).
- Semantically-equal but textually-different JSON (key order, whitespace, an omitempty field present as its zero value) compares UNEQUAL and therefore QUEUES a second op. That is the safe direction — queue, never drop — and must be stated in the code comment. Do NOT add canonicalization; it would reintroduce a silent-drop path.
- A RUNNING op's stored Params can drift from what was enqueued because checkpoint state is merged back in (UpdateOperationV2Params, internal/database/iface_ops_v2.go:130). For a resumable cron def this would break dedupe — which is exactly why the Schedule != nil clause exists. For a manual def it means a redundant queued row, not data loss.
- Batchable defs return early ABOVE the dedup block (registry.go:585-609) and are unaffected. Do not move the dedup above the batch branch.
- The C-3 zombie skip must run BEFORE the params comparison: a zombie row's params are irrelevant, it must be skipped either way.
- `continue` rather than `break` on a params mismatch: another active row for the same def may still match byte-for-byte.
- ListActiveOperationsV2 returning an error keeps the EXISTING behaviour (the `if listErr == nil` guard at L620 means the whole dedup is skipped and the op enqueues). Do not change that — failing open here means an extra queued row, and Gate 3 still serializes it.

## Tests

- internal/operations/registry/enqueue_dedupe_test.go (NEW file, needs the 4-line version header): `TestEnqueueOp_SameParamsReturnsSameOpID` — register a def with ConcurrencyKey "t.dedupe" and Schedule nil; enqueue params {"book_ids":["a"]}, then enqueue the identical params again; assert both calls return the SAME op id and only one row is active.
- internal/operations/registry/enqueue_dedupe_test.go: `TestEnqueueOp_DifferentParamsQueuesASecondOp` — same def; enqueue {"book_ids":["a"]} then {"book_ids":["b"]}; assert the second call returns a DIFFERENT, non-empty op id and that ListActiveOperationsV2 now reports two rows for the def, the second with status "queued". This is the prod-incident regression.
- internal/operations/registry/enqueue_dedupe_test.go: `TestEnqueueOp_SecondQueuedOpStartsOnlyAfterTheFirstCompletes` — drive the dispatcher (or assert directly against Gate 3 via the registry's concurrencyKeys map) to show the second op does not start while the first holds the key, and does start after it completes. If driving the dispatcher is impractical in a unit test, assert instead that the def's ConcurrencyKey is non-empty and cite dispatcher.go:107 in a comment — but say so explicitly rather than silently weakening the test.
- internal/operations/registry/enqueue_dedupe_test.go: `TestEnqueueOp_CronScheduledDefStillDedupesOnDefIDAlone` — register a def with ConcurrencyKey "t.cron" AND Schedule pointing at a cron string; enqueue with params {"limit":1} then {"limit":2}; assert BOTH calls return the same op id. This pins the pile-up behaviour the original dedup existed for.
- internal/operations/registry/enqueue_dedupe_test.go: `TestEnqueueOp_DedupeQueuedRunsOptInIgnoresParams` — def with DedupeQueuedRuns:true, Schedule nil; two different param payloads; assert the same op id both times.
- internal/operations/registry/enqueue_dedupe_test.go: `TestEnqueueOp_NilParamsAndEmptyObjectAreEqual` — enqueue with params nil (marshals to "{}") then with an explicit empty struct; assert the same op id, so a nil/empty caller does not spuriously queue a duplicate.
- internal/operations/registry/enqueue_dedupe_test.go: `TestEnqueueOp_ZombieRunningRowIsStillSkipped` — the C-3 regression: seed a 'running' row with no live handle and identical params; assert a NEW op id is returned (the zombie is skipped, not deduped against).
- internal/operations/registry/enqueue_dedupe_test.go: ANTI-OVER-SUPPRESSION — `TestEnqueueOp_DedupeStillFiresForTheCommonDoubleClick`: same def, Schedule nil, DedupeQueuedRuns false, identical params enqueued three times in a row; assert exactly ONE active row exists. Without this, a change that simply deleted the dedup block would pass every 'queues a second op' test.
- internal/operations/registry/enqueue_dedupe_test.go: `TestOperationDefs_DedupeDecisionIsExplicit` — the per-def table test. Iterate every def the registry has registered via `Registry.ActiveDefs() []OperationDef` (internal/operations/registry/registry.go:977 — verify with `grep -n "func (r \*Registry) ActiveDefs" internal/operations/registry/registry.go`, 1 hit). For each def with a non-empty ConcurrencyKey assert exactly one of these holds and record which: (a) DedupeQueuedRuns is true AND the def id appears in an in-test `optedIn` map (empty today — a new opt-in fails the test until it is listed with a reason), or (b) DedupeQueuedRuns is false. Additionally assert that each of the 8 selection-carrying def ids — "metadata.batch-save", "metadata.batch-apply-cached", "library.bulk-write-back", "library.organize", "maintenance.bulk-write-back", "maintenance.regroup-shattered-ai", plus the acoustid fingerprint-rescan and maintenance.probe-directory-books ids (read the exact ID strings from their def files) — has DedupeQueuedRuns == false. If the test cannot enumerate registered defs without a full server, put it in internal/server/ instead and say so in the PR body.

Anti-over-suppression test: `TestEnqueueOp_DedupeStillFiresForTheCommonDoubleClick` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/operations/registry/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `go build ./... && go vet ./... && go test ./internal/operations/registry/... ./internal/server/... -count=1` exits 0.
- [ ] `grep -n "DedupeQueuedRuns" internal/operations/registry/types.go internal/operations/registry/registry.go` returns 1 hit in types.go (the declaration) and 1 in registry.go (the condition).
- [ ] `grep -rn "DedupeQueuedRuns: *true" internal --include='*.go' | grep -v _test.go | wc -l` reports 0 — no def opts in as part of this change.
- [ ] `grep -n "C-3: skip zombie rows" internal/operations/registry/registry.go` still returns 1 hit — the zombie skip survived.
- [ ] `grep -n "bytes.Equal(rawParams" internal/operations/registry/registry.go` returns 1 hit, and its line number is LESS than the line number of `grep -n "// Marshal params."`... i.e. verify with `grep -n "rawParams" internal/operations/registry/registry.go` that the first assignment to rawParams precedes the dedup loop.
- [ ] `go test ./internal/operations/registry/... -run 'EnqueueOp_|DedupeDecisionIsExplicit' -count=1 -v` shows every listed test PASS, including TestEnqueueOp_DedupeStillFiresForTheCommonDoubleClick.
- [ ] Anti-over-suppression test: `TestEnqueueOp_DedupeStillFiresForTheCommonDoubleClick` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/operations/registry/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_operations_222.md`.

## Commit message

```
refactor(operations): EnqueueOp: dedupe only on byte-equal params (or an explicit  (ENQ-DEDUP-1)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the symbol already lives at its NEW location and is absent from the old one (re-run the re-verify greps above), the move is already done — run acceptance instead. Rollback = `git revert` the commit; behaviour at the old site is restored.

## Coordinator notes

internal/operations/registry/enqueue_dedupe_test.go is a NEW file (needs the 4-line version header). sdk.OperationDef is a type ALIAS of registry.OperationDef (pkg/plugin/sdk/operation.go:11) — verified — so the new field reaches all ~146 plugin defs automatically; there is no second struct to edit and no converter. FULL ENUMERATION as required: `grep -rn "ConcurrencyKey:" internal --include='*.go' | grep -v _test.go` returns 152 lines = 146 real defs with a non-empty key + 3 empty-string defs (library.transcode at library_core_ops.go:332, library.folder-auto-scan at folder_autoscan_op.go:50, maintenance/job.go:131) + 1 dynamic (`policy.ConcurrencyKey`, server/maintenance_job_op.go:97) + 1 doc comment (types.go:68) + 1 duplicate counted in the plugins/scheduler overlap; the per-def decision is stated as three categories in `background` with the grep that derives each. The dynamic `policy.ConcurrencyKey` def (maintenance_job_op.go:97) inherits the same rule and needs no per-policy decision because its params come from the policy row. BEHAVIOUR CHANGE the owner should know about: the UI will now show a second job where it previously showed none — that is the intended fix, not a regression.
