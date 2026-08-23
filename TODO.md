<!-- file: TODO.md -->
<!-- version: 10.39.0 -->
<!-- guid: 8e7d5d79-394f-4c91-9c7c-fc4a3a4e84d2 -->
<!-- last-edited: 2026-08-23 -->

# Project TODO — live items only

## 📥 Inbox

Tasks assembled from `todo.d/` fragments. Add a new task by dropping a fragment
file in `todo.d/` rather than editing this section by hand — see
[`todo.d/README.md`](todo.d/README.md). Checking a task off, or promoting it
into one of the curated sections below, is a normal direct edit.

<!-- todo-insert-here -->

- [ ] **TODO-052-UNDOC** `docs/api/openapi.json` has no entry at all for two
      live, permission-gated routes discovered while TASK-052 triaged the 15
      stale `POST /maintenance/{job-name}` paths (PR for TODO L296):
      `GET /maintenance/jobs` (the maintenance job catalogue —
      `internal/server/maintenance_dispatcher.go`'s `listMaintenanceJobs`,
      wired in `internal/server/server_lifecycle.go`) and
      `POST /maintenance/wipe` (admin-only, `s.handleWipe`, same file). Neither
      was ever documented, so unlike the 15 deleted paths there is no stale
      entry blocking this — it is pure addition. `POST /maintenance/jobs/{job_id}`
      (added by TASK-052) references `GET /maintenance/jobs` in its
      description as the live source of truth for the job_id enum; that
      cross-reference is currently undocumented itself.

- [ ] **TODO-REVERTDEDUPE** `auto-revert.yml`'s own "File the bug" step
      (`.github/workflows/auto-revert.yml` ~L305, `gh issue create`) has no
      pre-check against an already-open issue for the same failing SHA —
      unlike the new `auto-revert-backstop.yml`, which gained a `gh issue
      list --state open --search` dedupe check specifically because this gap
      exists. A flapping CI failure that `auto-revert.yml` handles repeatedly
      (e.g. `workflow_run` firing more than once for the same commit, or a
      revert that does not fix the build) could already be filing duplicate
      issues today, independent of the backstop. Add the same dedupe check to
      `auto-revert.yml`'s issue-filing step.

- [ ] **TODO-051-UNDOC** `docs/api/openapi.json` is missing correctly-prefixed
      entries for 11 live routes that TASK-051 found undocumented while
      deleting group-relative duplicate paths (PR for TODO L296): `/users/invite`,
      `/users/invites`, `/users/invites/{token}`, `/auth/accept-invite`,
      `/deluge/status`, `/deluge/test-connection`, `/itunes/rebuild`,
      `/itunes/write-back-all`, `/users/{id}/deactivate`,
      `/users/{id}/reactivate`, `/users/{id}/reset-password`. Each has a bogus
      group-relative stub at the wrong (bare) path today — do not delete those
      stubs until a correctly-prefixed replacement is written, per
      `.claude/skills/api-doc/SKILL.md`.

- [ ] **SCAN-PHASE** Restructure the library scan into discrete, resumable phases —
      owner report 2026-08-22: the scan "seems way too slow", and the proposal is
      that it "should just go in phases so it can easily resume at a phase".

  **Why phases, specifically.** The complaint is duration, but the fix asked for is
  *resumability*, and those are different problems that happen to share a cause. A
  scan that is one long uninterruptible pass has to be re-run from zero after any
  interruption — a deploy, a restart, a crash, a timeout — so its effective cost is
  not its runtime but its runtime times the number of times it gets interrupted.
  Phase checkpoints attack that multiplier without needing any single phase to get
  faster. Note this also removes the "never deploy mid-scan" constraint that
  currently gates every production restart (`docs/operations/`, and the handoff
  runbook), which is a second, separate win.

  **Measure before designing.** Do not assume the slow part. `scheduled.library_scan`
  runs every 360 min, so there is real production timing to pull from
  `journalctl -u audiobook-organizer` rather than guess. The phase boundaries are
  only useful if they fall where the time actually goes, and a phase split chosen
  from intuition will checkpoint in the wrong places.

  **Design notes / open questions:**
  - What are the phases? Candidate split: discover files → parse/probe metadata →
    resolve contributors → write/index → post-scan maintenance. Confirm against
    measured timings, not this list.
  - Where does phase state live, and what makes a phase idempotent enough to resume
    into rather than restart? A phase that is resumable only from its start is still
    a large win over a scan that is resumable only from its start.
  - Interaction with the existing checkpoint machinery — `internal/plugins/maintenance/`
    already has a `pipeline_checkpoint.go` with `checkpointPrefix`/`checkpointTTLDays`
    consts currently flagged as **unused**. Check whether that is a half-built version
    of this idea before writing a second one.
  - Resume must never re-apply metadata or re-write tags for work a prior phase
    already committed; the apply pipeline has a history of double-writing
    (`dedupe-book-file-rows`, the 42-rows/21-paths incident).

  **Not scoped here:** making any individual phase faster. That is a separate
  optimization task and should be filed from the measurements above.

- [ ] **`internal/ai/retry.go`'s `isPermanentAIError` HTTP-429 branch checks
      the wrong JSON field for OpenAI's real quota-exhaustion error.** Found
      while wiring `internal/scanner/ai_failure.go`'s `isPermanentAIFailure`
      to reuse this classifier (TODO L4852/L4961). The branch is
      `case 429: return apiErr.Code == "insufficient_quota"`, and
      `openai-go`'s `apierror.Error.Code` decodes the response JSON's `"code"`
      field (`internal/apierror/apierror.go`:
      `Code string \`json:"code" api:"required"\``). The production error
      captured in `internal/scanner/ai_failure_test.go`'s `prodQuotaError` —
      copied from the scanner's own incident journal, not composed — is a 429
      with `"type": "insufficient_quota"` but `"code": "credit_balance_exhausted"`.
      Against the real payload, `apiErr.Code` is `"credit_balance_exhausted"`,
      not `"insufficient_quota"`, so this branch returns `false` for the exact
      error the scanner's test suite exists to catch: `DoWithRetry` retries it
      as transient, burns `maxRetries` attempts with backoff, and only
      `internal/scanner/ai_failure.go`'s substring-marker fallback (which
      still checks for `"credit_balance_exhausted"` and `"insufficient_quota"`
      as raw text) catches it after the fact. Fix is presumably to also accept
      `apiErr.Type == "insufficient_quota"`, or to match on either field
      depending on which one OpenAI's docs treat as stable API for this error
      family — needs the same kind of primary-source check TASK-124 did
      before changing retry.go's classification, since retry.go's own
      `DoWithRetry` is used by other callers too.

- [ ] **TODO-MOCKORDER** Decide whether to add a permanent guard against shadowed
      branches in the `setupMockApi` dispatcher
      (`web/tests/e2e/utils/test-helpers.ts`). TASK-093 audited the 10
      `pathname.startsWith(...)` catch-alls by hand and found 0 shadowed
      branches, but an audit decays the moment someone adds a new branch below a
      catch-all — which is exactly how the `/api/v1/audiobooks/batch` POST bug
      got in. **Caveat that makes this a decision, not a task:** the dispatcher
      mixes three branch forms — 67 `pathname === '...'`, 10
      `pathname.startsWith(...)`, and 24 `pathname.match(/.../)` — and a
      literal-parsing guard reads the first two accurately but can only
      approximate a regex by its leading literal prefix (one of the 24, at
      ~L1584, is even split across lines and unreadable by a line-based parser).
      A guard blind to a third of the branches would advertise more coverage than
      it has. Either accept that limit explicitly, or restructure the dispatcher
      into a route table that can be checked exactly.

- [ ] **TODO-MOCKWORKS** `web/tests/e2e/utils/test-helpers.ts` ~L1750:
      `pathname.startsWith('/api/v1/works')` has no trailing slash, so it also
      matches any future sibling path with that prefix (`/api/v1/workspaces`,
      `/api/v1/works-queue`, ...). Nothing is shadowed today; add the trailing
      slash (plus a separate exact branch for bare `/api/v1/works`) before any
      such endpoint is mocked. Same file ~L732: `/api/v1/backup/list` has no
      HTTP-method guard, so it answers a `DELETE /api/v1/backup/list` ahead of
      the `/api/v1/backup/` DELETE catch-all below it.

- [ ] **COLLECTION-NAME-CONFLICT-SENTINEL** `PebbleStore.UpdateCollection`'s
      duplicate-name rejection still signals with a bare
      `fmt.Errorf("collection name %q already in use", ...)`, matched at call
      sites via `strings.Contains(err.Error(), "already in use")`
      (`internal/server/handlers/collections.go`,
      `internal/server/handlers/abs/collections.go`). Give it a sentinel —
      `var ErrCollectionNameInUse = errors.New(...)`, wrapped with `%w` — and
      switch those call sites to `errors.Is`, the way
      `ErrCollectionVersionConflict` now works in the same file.

  **Why this is worth doing rather than leaving as-is.** The version-conflict
  CAS was very nearly shipped with the same string match, on the argument that
  it matched the existing convention. It does not: `internal/database` already
  declares sentinels elsewhere (`ErrSettingNotFound` in `settings.go`,
  `ErrNoHNSWSnapshot` in `hnsw_embedding_store.go`), so `already in use` is the
  outlier, not the pattern. Converting the CAS turned up the concrete failure
  mode too — a test fake in `abs/collections_test.go` was hand-building a
  lookalike message, and would have gone on passing against a handler that had
  stopped recognising the error at all. The name conflict has exactly the same
  exposure today.

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

### 📋 `DECISIONS-PENDING.md` contradicts itself — settled decisions still listed as open

- [ ] **Reconcile `docs/plans/DECISIONS-PENDING.md`'s open table with its own recorded-decisions
      table.** Surfaced by TASK-058 (PR #2715) while verifying the execution-manifest gates.

  The file carries both a "Decisions recorded 2026-08-21 (owner, via AskUserQuestion)" table
  **and** an open/pending table that still lists the same rows 1–5 as awaiting a decision. It also
  still says PR #1935 "stays open"; `gh pr view 1935` reports **MERGED**. So the document asserts
  two contradictory states about the same five items, and a reader landing on the open table gets
  the wrong one.

  **Why this is worth fixing rather than ignoring:** the manifest at
  `docs/plans/2026-07-10-execution-manifest.md` was just corrected to match the *recorded* table.
  Leaving the stale open table in place recreates exactly the drift that correction removed, and
  the next reader has no way to tell which table is authoritative.

  **Two nuances to preserve when reconciling** — both were nearly lost once already:

  - INIT-7 is **HOLD CONFIRMED**, not "parked". The owner answered "KEEP ON HOLD".
    `SCOUT-INSTRUCTIONS.md:14`'s `ON HOLD → "parked"` is the scout package's classification
    convention for excluding an item from briefing, **not** a decision the owner made.
  - INIT-6's #1935 merged, but it was the plan doc *"for owner sign-off"*. The STOP-FOR-HUMAN
    spec review was never held. Recording a bare "merged" reads as approved and would contradict
    the item's own hold status.

  Related: `TODO.md` item 33 still calls REPO-SIZE-1 STOP-FOR-HUMAN, though
  `docs/plans/2026-07-10-repo-size-history-rewrite-plan.md:223` records "Adopt Option (d)…Do not
  rewrite history." Same reconciliation pass should cover it.

### Bulk book-merge shows "Merged all" even when individual merges failed

`web/src/components/dedup/DedupBookTab.tsx` — `handleMergeSelected` (~:107) and
`handleMergeAll` (~:129) both loop over groups, catch per-group failures into
`setError(...)`, then unconditionally call `setMergeSuccess('Merged ...')` after
the loop. A run where 4 of 10 groups failed shows a success banner and a stale
error banner side by side, with no indication which groups actually merged.
`fetchDuplicates()` then re-lists, so the failed groups silently reappear
underneath the success message.

- [ ] Track per-group outcomes in the loop and report "Merged N of M" (naming the
      failures) instead of an unconditional success string.

**Why this is worth doing now rather than later:** until #2736, `api.mergeBooks`
returned the response envelope instead of `body.data`, so `initial.id` was
`undefined` on *every* invocation and the catch fired every time. The
success-after-error path was therefore permanently active and obvious to anyone
using the tab. #2736 fixed the id, which turns an always-on bug into a rare
latent one — less visible, not less wrong. Found by a silent-failure review of
#2736; pre-existing, deliberately left out of that PR's scope.

### 🧹 DEP-1e — drop the deprecated `Book.ITunesPath` field

- [ ] **Remove `ITunesPath *string` from `database.Book`** and the `BookCore` round-trip that
      carries it. Spun out of the 2026-05-01 re-audit close-out (item 42), where it was the one
      sub-item that is genuinely still open.

  **Correcting the record.** The prior close-out note called DEP-1e "moot (post-SQLite removal)".
  It is not moot — the field is still declared and still copied at HEAD `629d5fa79`. Nothing
  re-checked the claim after the SQLite store was deleted, so a stale justification outlived its
  reason.

  **Exact extent (measured with `gopls findReferences` on the field, not a name grep).**
  `Book.ITunesPath` has **6 references, total**:

  - `internal/database/store.go:220` — the declaration itself
  - `internal/database/bookcore.go:207` — read in `func (b *Book) Core() BookCore`
  - `internal/database/bookcore.go:321` — written in `func (c *BookCore) ToBook() Book`
  - `internal/itunes/service/importer_mock_test.go:127, 152, 177` — test-only writes

  So it is a **pure carrier**: written by tests, round-tripped through `BookCore`, and read by no
  production logic on any path. Removal is mechanical — delete the field, delete
  `BookCore.ITunesPath` (`bookcore.go:62`) and both copy lines, then fix the three test literals.

  **Why a name grep is the wrong instrument here.** `grep 'book\.ITunesPath'` returns **0 hits**
  and looks like proof the field is already dead. It is not: the two real call sites use receivers
  named `b` and `c`. Meanwhile `grep '\bITunesPath\b'` returns **75** non-test hits, nearly all of
  which are the *authoritative* `BookFile.ITunesPath` (a plain `string`) and are unrelated to this
  task. Neither count answers the question; only symbol resolution does. Do not re-scope this task
  from either number.

  **Do not confuse the two fields.** `BookFile.ITunesPath` (`store.go:810`, a `string`) is live and
  load-bearing — iTunes import, write-back, path repair and reconcile all use it. Only the
  `Book`-struct `*string` is being removed.

  **Before removing, re-run the reference check** — if a new production reader has appeared since
  2026-08-22, keep the field and re-scope. Gate: `go build ./...` + `make test`.

### 🐛 `LegacyOpID` defeats `EnqueueOp`'s dedupe for maintenance jobs (serialization fixed 2026-08-22)

- [x] ~~**Decide whether the 37 maintenance jobs should serialize against themselves**, and if so
      give each def a per-job `ConcurrencyKey` (the op ID is the natural key) **plus**
      `DedupeQueuedRuns: true`.~~ — **decided and shipped 2026-08-22 (PR #2709).**
      `registerMaintenanceJobOp` now derives `ConcurrencyKey` from the job's own op ID when the
      job's policy leaves it empty (a job that declares its own key keeps it, so the field stays
      meaningful). `DedupeQueuedRuns` was deliberately **NOT** set: `maintenanceJobOpParams`
      carries `DryRun`, so "run for real" clicked during a dry run would be silently dropped —
      the exact bug #2688 fixed. Mutation-verified: with the key reverted, two enqueues overlap
      (`maxOverlap == 2`); with it, they run sequentially.

- [x] ~~**Still open from this fragment:** `LegacyOpID` continues to defeat `EnqueueOp`'s
      byte-equality dedupe, because every request mints a fresh ULID.~~ — **fixed 2026-08-22
      (measured in PR #2717, fixed in the PR that closed this).**

      **The claim that it "disappears with `maintenance_dispatcher.go` in the v1 kill" was
      wrong, and re-measuring is what caught it.** `maintenanceJobOpParams` is constructed at
      three sites, and deleting the dispatcher removes only two — `server_lifecycle.go:287`
      (`resumeLegacyOp`) stamps a fresh `LegacyOpID` on the **restart** path with no dispatcher
      involvement, and `resumeInterruptedOperations` has no per-job dedupe, so
      restart-after-double-click reproduced the bug regardless. Repo-wide the field has ~30
      construction sites across nine subsystems; it is the v1↔v2 bridge seam, not a dispatcher
      artifact. The dedupe fix was therefore **independent of the v1 kill**, not gated on it.

      **The stamp was excluded from the comparison, not dropped.** Dropping it would have
      regressed two things: `propagateLegacyOpStatus` reads it to move the v1 row off
      `pending` (TODO.md records that bridge as measured working on 2026-08-16), and
      `maintenance_job_op.go:132,142-147` keys the activity log off it — the latter guarded on
      `p.LegacyOpID != ""`, so it would have failed **silently**. `Run` decodes from
      `rawParams`, not the `SaveParams` snapshot, so "keep it at :180" would not have helped.

  **Where:** `internal/server/maintenance_job_op.go` — `registerMaintenanceJobOp` is the single
  factory for all 37 defs, so both fields are set in one place. `internal/maintenance/job.go:131`
  (`DefaultPolicy`) is where `ConcurrencyKey: ""` is hardcoded, and `job.go:123` explicitly defers
  per-job keys to "PR-2".

  **The state of things as originally found (fixed by #2709).** Two gates both test
  `def.ConcurrencyKey != ""`: `EnqueueOp`'s dedupe block (`registry.go`) and dispatcher Gate 3
  (`dispatcher.go:107`). Every maintenance job used `DefaultPolicy()`, whose `ConcurrencyKey` is
  `""`. So **neither gate had ever applied to a maintenance job**: a double-click started two runs,
  and they ran *concurrently*, not serialized. Both gates now apply.

  **Correction to an earlier note (2026-08-22).** A previous version of this fragment claimed
  PR #2688 (params-aware `EnqueueOp` dedupe) turned a silently-swallowed double-click into two
  serialized runs, and that setting `DedupeQueuedRuns: true` would restore single-run behaviour.
  **Both claims are wrong**, because they assume execution reaches a branch these defs never
  enter. #2688 changed nothing for the maintenance family. `DedupeQueuedRuns` alone would be
  inert. The error came from accepting a subagent's report without checking the gate condition it
  depended on.

  **What IS true about `LegacyOpID`.** `maintenance_dispatcher.go:153` generates a fresh
  `opID := ulid.Make().String()` per request and puts it in `maintenanceJobOpParams.LegacyOpID`
  (lines 181, 190), so two identical requests never marshal byte-equal. That defeats #2688's
  byte-equality dedupe *for any def that reaches it* — so it must be dealt with as part of the
  work above, not before it. Same shape at `reconcile.go:52`, `reconcile.go:131`,
  `duplicates/handler.go:588`.

  ~~Do not simply drop `LegacyOpID` from the struct: the v2 op needs it to find the legacy
  `operations` row to update, and `resumeLegacyOp` (`server_lifecycle.go`) reads it on restart.~~
  — **superseded 2026-08-23; the field is now gone from `maintenanceJobOpParams`.** Both reasons
  were removed rather than worked around: there is no legacy `operations` row to find (the v1
  minter in `runMaintenanceJob` is deleted, so a maintenance run mints a v2 row only), and
  `resumeLegacyOp`'s `maintenance:` branch is deleted too — it was the SECOND resume path for a
  single logical run, the v2 twin having already been resumed by `resumeAfterStartup`.

  The activity log, the per-item results and the operation summary log are now keyed off
  `opsregistry.ReporterOpID(reporter)`. That was safe because all three are keyed by an operation
  id **string** with no foreign key to any `operations` row — the same shape that made the
  `GetOperationSummaryLog` read a non-issue.

  `JobID` stays, but its documented reason ("retained-for-old-rows", i.e. resume reading params
  written by an older build) is now **false** — that path is deleted. It is read by nothing;
  `EnqueueOp`'s dedupe is scoped per-def, so params cannot conflate two jobs. Removing it is a
  candidate for a separate change.

  `sameParamsIgnoringLegacyID` and the `legacy_op_status.go` bridge **stay**:
  `server_lifecycle.go` still stamps a `LegacyOpID` on the `isbn-enrichment` and
  `metadata-refresh` legacy-resume branches.

  **Why it matters:** `cleanup-empty-folders` removes directories from disk, and seven jobs are
  both `CanResume()` and advertise `dry_run: true`. Two concurrent runs of a mutating job is the
  failure mode worth closing.

### 🧩 Queued-op consolidator — collapse N queued runs of one def into a single merged op

Owner decision 2026-08-22: **do B now, then build this.**

- [ ] **B (do first):** give each maintenance def a per-job `ConcurrencyKey` so a job can never run
      concurrently with itself. One field in `registerMaintenanceJobOp`
      (`internal/server/maintenance_job_op.go`): `ConcurrencyKey: maintenanceOpID(jobID)`. Do NOT
      also set `DedupeQueuedRuns` — dropping a second request silently is the bug #2688 fixed, and
      `maintenanceJobOpParams` carries `DryRun`, so a "run for real" clicked during a dry-run would
      vanish and report success. Needs a test that two enqueues produce two SEQUENTIAL runs, and a
      mutation check that removing the key lets them overlap.

- [ ] **Then: the consolidator.** Once ~3–4 ops for the same def are QUEUED, open one new op whose
      params are the merge of theirs, and close the originals. **Queued only — never touch a
      RUNNING op**, which has already done work.

#### Why this is not just `OperationDef.Batchable`

The registry already has batching (`types.go:124-155`): `Batchable` buckets a call's *subject*
before any row exists, returns `("", nil)`, and flushes on a debounce (`BatchWindow`) capped by
`BatchMaxWait`. Close, but the wrong shape for this ask. Batching coalesces *before* the op is
real; the consolidator coalesces rows the user has already seen in Active Operations. Whether the
right build is "extend Batchable to a post-enqueue mode" or "a separate consolidator pass" is open
— but the difference in visibility is the reason it cannot just be `Batchable: true`.

#### The constraint that decides the design: op-ID identity

`EnqueueOp` returns an ID and **callers retain it**: `internal/plugins/maintenance/optimize.go:148`
captures `childID` and waits on it; `internal/scheduler/tasks.go` captures `v2ID` at :134, :173,
:194, :253, :316, :337, :364. If a consolidator closes those rows, every holder is watching a dead
op — a wait that never returns, a UI row that vanishes.

`Batchable` dodges this honestly by returning `""` up front: "no ID yet." A consolidator cannot —
it has already handed out IDs. So it needs one of:

1. **A `superseded_by` pointer on the closed rows**, with the ops API and the activity feed
   following it. Preferred: the waiter follows the redirect, and the UI can say "merged into op X"
   instead of dropping a row. This is the honest version of the feature.
2. Restricting consolidation to defs whose callers provably never retain the ID. Narrower, and the
   proof rots the first time someone adds a caller.

#### Merge semantics must be per-def. There is no safe generic default

`book_ids: [...]` unions obviously. `dry_run: bool` does not — merging `true` and `false` is a
policy choice, and choosing wrong turns a preview into a mutation. That is the same hazard
`maintenance_dispatcher.go` already documents for resume (seven jobs are both `CanResume()` and
advertise `dry_run: true`; `cleanup-empty-folders` removes directories from disk).

So: the def supplies a merge function, and **a def with no merge function is not consolidatable**.
Refuse rather than guess. A generic "last write wins" or "OR the booleans" default would be a
data-loss bug wearing a convenience feature's clothes.

#### Other things the build must settle

- Trigger: a count threshold (3–4), a time window, or both? `BatchWindow`/`BatchMaxWait` already
  model the time half — reuse the vocabulary rather than inventing a second one.
- Every close must be journaled with the replacement ID. An op that disappears without a record is
  the silently-discarded-request failure again, just later in the pipeline.
- Interaction with `ConcurrencyKey` from B: consolidation operates on the queue that Gate 3 builds
  up, so B is a prerequisite, not merely "first" — without a key there is no queue to consolidate,
  because everything dispatches immediately.

#### Update, same day: scope this down — most of it already exists

Owner refinement: consolidate only ops whose **parameters are identical**, and otherwise just
block so they run sequentially. That is a different, much smaller feature than the one sketched
above, and **`EnqueueOp` already implements it** as of #2688: it reuses an active op when
`bytes.Equal(rawParams, op.Params)`, and queues a second row otherwise, which Gate 3 then
serializes. Identical params also dissolves the merge-function problem — if the params are the
same, the merged op's params *are* the params; nothing needs merging.

The only reason this does not work today is `LegacyOpID`: a fresh ULID per request that makes
"same parameters" never true. That field exists solely to bridge back to v1, and
`docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md` **Phase 1 step 3 deletes
`maintenance_dispatcher.go`**, the only thing that writes it. That plan is IN PROGRESS (step 1
landed as #2551).

**Revised plan:**

1. `ConcurrencyKey` per maintenance def (item B above) — still needed; without it nothing queues.
2. Finish v1 retirement, Phase 1 steps 2–3. `LegacyOpID` disappears with the dispatcher.
3. Re-measure. Same-params dedupe and different-params serialization should both work with no new
   code.

Only build a consolidator if step 3 shows a real gap. The `superseded_by` redirect and the per-def
merge function above are **not** needed for the same-params-only version — do not build them
speculatively. Keep the notes: they apply if a general merge is ever wanted, and they record why
"just OR the booleans" is unsafe.

- [ ] **Decide the fate of `api.startBulkMetadataFetch`, now caller-less.** Deleting
      the unreachable Bulk Fetch Metadata dialog (TASK-092) removed
      `Library.tsx:handleBulkFetchMetadata`, which was the helper's only production
      caller. `web/src/services/api.ts:1928` now has zero callers in `web/src`
      outside its own unit test in `api.test.ts`.

  **Why this is being written down rather than fixed in TASK-092.** The helper is
  `export`ed, so `noUnusedLocals` does not flag it and neither does the linter —
  exactly the shape that let `linkAsVersion` survive a dead-code sweep with
  test-only callers (see `WAVE-1-STATE.md`, "DEAD-1 is not resolved"). Left alone
  it is invisible: not dead by any automated measure, not reachable by any user.

  **What has to be decided, because the answer is not obvious.** The client helper
  is gone from the UI but the backend v2 bulk-metadata-fetch operation it enqueues
  is untouched and still works. So either:

  - the feature was retired on purpose — the `REMOVED 2026-08-09` note in
    `web/tests/e2e/batch-operations.spec.ts` says the e2e coverage was deliberately
    deleted then, which points this way — and the helper plus its test should go
    too, and possibly the backend op with them; or
  - the dialog was collateral damage and a bulk metadata fetch is still wanted, in
    which case the helper is the surviving half of a feature that needs re-wiring
    to a reachable control, not deletion.

  Do not resolve this by deleting the helper on the strength of "no callers" alone.
  The live "Fetch Selected" flow (`handleFetchReview` -> `api.batchFetchCandidates`)
  is a *different* operation, not a replacement for this one, so its existence does
  not prove this feature was superseded.

- [ ] Add a Settings panel for `path_aliases` (root / Windows prefix / UNC /
      smb URL). v1 is config-and-seed only, so changing an alias means editing
      config. See `docs/design/2026-08-20-dual-path-display.md` open question 1.
- [ ] Make `PathAliases` the single source for the Windows prefix and have
      `reconcile.TranslateITunesPath` read from it, retiring the duplication
      that `ValidatePathAliases` currently only guards against.
- [ ] Reset the module-scope `cachedAliasesPromise` in `PathLinks.tsx`
      (and `cachedVarsPromise` in `formatPath.ts`) between tests. Both
      caches persist across a test file, so today every test shares one
      seeded alias set; a future test needing different alias data per
      case will get a stale answer with no obvious cause.
- [ ] Decide how `path_aliases` re-derives after a normalization change.
      `SeedPathAliases` short-circuits on `len(aliases) > 0`, so once a value is
      persisted it is never re-seeded, and `ValidatePathAliases` cannot tell a
      stale persisted value from a correct one. Harmless today (the feature has
      never been deployed, so no config_blob holds a pre-normalization value),
      but any future change to `normalizeWindowsPrefix` inherits the same
      problem — a stored alias will not pick it up.

- [ ] Replace the fixed `resume_policy` enum with a **condition-based** resume
      decision, with elapsed time as one available condition rather than the
      only one. Today `resume_policy` is a single static value per op-def
      (`restart` / `resume` / `drop`), decided without reference to the state
      the op was in when it stopped.
      Motivating case (2026-08-21): a deploy restart hit `library.scan` at
      13,922/40,089 books, ~80 minutes in. Its policy is `restart`, so all of
      that was discarded and the scan began again from zero — even though the
      partial work was minutes old and still valid.
      Wanted: resume when a condition set says the prior progress is still
      trustworthy, otherwise restart. Time is the obvious first condition
      ("resumed within ~3h"), but it should not be special-cased — express it
      as one predicate among others, e.g.:
        - elapsed since `started_at` / since the last checkpoint
        - whether the scanned root's mtime or file count changed meanwhile
        - whether a conflicting op ran in between
        - config or op-def version drift since the checkpoint
      Design note: this is a real expansion, not a tweak. `resume_policy` is
      consumed wherever ops restart, so the change is a policy *evaluator*
      taking op state + environment, not a new enum member. Keep the current
      static values working as degenerate always-true/always-false conditions
      so the migration is incremental.
      See `internal/config` op-def plumbing and the measured resume behaviour
      note (applies do NOT resume; `batch-apply-cached` = ResumeDrop).

- [ ] **MERGE-UNDO** Make a review-initiated merge reversible. The machinery is
      half-built and entirely unwired: `Engine.UnmergeAuto`
      (`internal/dedup/auto_resolve.go:450`) reverts both books to their
      pre-merge `book_ver` snapshots and has **no production caller at all** —
      it is reachable only from tests. Three gaps stand between that and a
      working undo, and none of them is the hard part of the other two:
      - Only the auto-resolve path journals. `PutAutoMergeJournalEntry` is
        called from `auto_resolve.go` alone, so a merge dispatched from the
        review lane records no pre-merge snapshot timestamps and there is
        nothing for `UnmergeAuto` to revert *to*.
      - `UnmergeAuto` declares its own scope limit: it restores the BOOK RECORD
        only. It does not reverse the external-ID reassignment (loser→winner)
        that `MergeBooks` performed, nor the enqueued iTunes write-back
        removals. Its comment names the missing follow-on explicitly.
      - No endpoint or op exposes it, so there is no way to invoke it.
      Deferred deliberately on 2026-08-20 when the dupes lane was made faster to
      triage: the user chose to ship throughput first and treat undo as its own
      task, since it is backend work with a correctness surface (external-ID
      restoration) that does not belong inside a keyboard-shortcut change. The
      speedup did not make merges less reversible — they were never reversible
      from that screen — but it does raise the rate at which they happen, which
      is the reason this is written down rather than left implicit.

## CI / automation

- [ ] Decide whether the 22 `gha-*` repos (plus `magnet-handler`) should keep their
      classic branch protection. They all require PR reviews and share a
      `set-auto-merge` check, so they look like a deliberate template rather than
      drift — unlike audiobook-organizer, whose protection was removed 2026-08-20.
- [ ] Add a scheduled detect-only backstop for `auto-revert.yml`: if `main`'s tip has
      a failed gate run older than 30 minutes and no open auto-revert issue exists,
      file the issue. Covers the case where the `workflow_run` listener never fires
      (runner outage, cancelled run).
- [x] ~~`scripts/test_check_memory_leaks.py` is executed by no workflow. Either wire it
      into `repo-guards` next to the auto-revert selector tests, or delete it.~~ —
      closed 2026-08-22 (PR #2700, TASK-007): wired into a CI job.

- [ ] 🔌 **ABS coverage gaps N-1 … N-10** (audit:
      [`docs/audits/2026-08-11-abs-coverage-gap-audit.md`](docs/audits/2026-08-11-abs-coverage-gap-audit.md)).
      We serve 48 of upstream's 223 routes, but the endpoint coverage for our two target
      clients is fine — the defects are in what those 48 routes *say*. In priority order:

      1. **N-1 — `GET /socket.io/…` returns `200 text/html`, not 404.** `nonSPAPrefixes`
         (`internal/server/spa_fallback.go:41-44`) lists only `/api` and `/auth/`, so the
         handshake falls through NoRoute to `c.Data(200, "text/html", indexData)`
         (`static_embed.go:95`); the non-embedded build 302s to `/` instead. Absorb's
         polling handshake gets HTML with a success status. **One-line fix + regression
         test.** This is the same bug the comment above that list was written to prevent
         for `/auth/openid` — it is one prefix short.
      2. **N-2 — the conformance harness cannot see a wrong value.** `assertConformant`
         hardcodes `Options{IgnoreExtra: true}` and never sets `CompareValues`, so
         `diff.go:78` and `:102-108` never execute. **All 25 always-hardcoded fields and
         all 9 stubs pass.** Turn both gates on for value-real endpoints (expect red — that
         is the point), add the 4 orphan fixtures (N-7), and assert `/socket.io/` → 404.
         Nothing else on this list stays fixed without it.
      3. **N-3 — we advertise `Delete:true`/`Update:true`** (`handlers/abs/dto.go:283-297`)
         while `LibraryStore` has no writer and zero write routes are registered. Clients
         render edit/delete affordances that cannot work.
      4. **N-4 — unimplemented `/api/…` paths 301 into `/api/v1/…` instead of 404ing**
         (`wire_abs_routes.go:46-83`). Affects `/api/collections`, `/api/playlists`,
         `/api/authors/:id`, `/api/series/:id`, `/api/users`, `/api/podcasts`. Absorb
         treats 404 as "degrade gracefully"; a 301 into a foreign API is not that.
      5. ~~**N-5 — `/search` narrators emit `numBooks: 0`**~~ (`browse.go:949`), which renders
         "0 books" beside every narrator. The contract says omit the field; `/narrators`
         does, `/search` does not.
      6. ~~**N-6 — a stats read failure reports `total = 0`**~~ (`stats.go:73-79`),
         indistinguishable from "never listened". Keep the 200 (a 5xx flips the client's
         connection dot) but log at warn + add a metric. — ✅ DONE 2026-08-22
         (PR #2701, TASK-145): 200 kept, warn log + metric added.
      7. **N-7/N-8/N-9/N-10** — 4 golden fixtures never loaded by any test (all write
         endpoints); `absRouteList()` reports 46 of 48 registrations so its
         "covers EVERY registered route" guard test is false; play-session `mediaMetadata`
         over-emits 6 fields vs the oracle; advertised login rate limit (10/10min) does not
         match the real throttle (15 failures/15min).

- [ ] ⚙️ **Decide `ABS_API_ENABLED` for production (N-11).** It defaults to `false`
      (`internal/config/abs_config.go:28-35`); when off, `wireABSRoutes` registers **zero**
      of the 48 routes. Nothing in the repo sets it and `deploy/local.conf` is gitignored,
      so prod state cannot be determined from the tree. Not a claim that it is off — a claim
      that an operator cannot tell.

- [ ] 🌐 **Per-stream `language` is always `nil` (N-12).** `mapper.go:676` returns nil
      unconditionally and says so in-code: the scanner never persists per-stream language.
      The only one of the 25 always-constant DTO fields that is a real data gap rather than
      a deliberate constant. Needs a scanner change, not a mapper change.

- [ ] 📚 **Docs consolidation follow-ups (from the 2026-08-11 inventory).** Full evidence in
      [`docs/audits/2026-08-11-docs-inventory.md`](docs/audits/2026-08-11-docs-inventory.md).
      Six items that a docs pass could not decide:

      1. **Resolve the two prod-run contradictions.** `TODO.md:4988` says the dedup prod
         drain was never executed; `docs/operations/pending-prod-actions.md:26` says it ran
         2026-07-18 (9,074→1,311). Same split on T04: `TODO.md:5311` unchecked vs
         `docs/dedup/STATUS.md:78-86` "EXECUTED ON PRODUCTION". Purgeable drifts 7,878 vs
         7,891. **Each record makes the other unfalsifiable** — only the owner knows which
         run actually happened. This is the ONLY thing blocking `dedup-pipeline-hardening`
         from being archivable.
      2. **Union-merge `docs/openapi.yaml` into `docs/api/openapi.json`.** They are two
         independently hand-maintained specs, neither generated. JSON has 117 paths the YAML
         lacks; **YAML has 25 the JSON lacks** (`/auth/login|logout|me|sessions*`,
         `/ai/scans*`). Picking a winner loses real surface.
      3. **Decide the 11 UNCERTAIN docs** (list in the inventory §4).
      4. **Classify `docs/system/**` (9) and `docs/architecture/**` (9)** — needed to settle
         whether the top-level architecture docs duplicate them.
      5. **Make `run-sweep.sh` fail loudly on a package it cannot parse.** It discovers work
         via `find -name 'TASK-*.md'` and 4 of 10 live packages have none, so it emits
         nothing — indistinguishable from "nothing to do".
      6. ~~**Write headers for the CURRENT files still missing them**~~ (the 76 fleet files are
         archived; the remainder are live docs). — ✅ DONE 2026-08-22 (PR #2713,
         TASK-183): **37** files headered, not the briefed 35 (expected drift); acceptance
         grep returns 0. `docs/development/writing-a-plugin.md` was a false positive — it
         already had all four fields in one block comment, converted to the one-line form.
         Still open, deliberately out of scope: 36 docs under
         `docs/agent-tasks/todo-completion/state/scratchpad/` sit below the audit's
         `-maxdepth 4` and remain headerless — decide whether that tree should be
         header-exempt like `todo.d/`.

## ABS

- [ ] **Align the ABS conformance fixtures with the oracle capture so the value gate can be
      turned on permanently.** `assertConformant` still runs with `CompareValues` off, so no test
      compares a single value. Turning it on today reddens **12** tests — but reading the findings
      rather than counting them shows they are mostly *not* defects:

      - **Fixture drift (most of them).** The fake library seeds a synthetic book; the oracle is a
        real capture of *The Odyssey*. So `size` is 4096 vs 1.20828875e8, `duration` 9975 vs
        9975.480544, `publishedYear` `800` vs `800BC`, track titles `The Odyssey: Book 06` vs
        `odyssey_06_homer_butler_64kb.mp3`, `timeBase` `1/1000` vs `1/14112000`.
      - **Deliberate divergences** that must be whitelisted, never "fixed": `user.type` is `user`
        not `root` (`dto.go:275-277` — it makes Absorb hide the admin UI we do not implement), and
        `Source` is `audiobook-organizer` not `docker`.
      - **Two worth an actual decision:** whether `media.tracks[].title` should be the filename
        (as ABS sends) rather than a display title, and the author ordering in `/personalized`.

      The work is to seed the fake library FROM the oracle fixture so the values match by
      construction, then flip the gate on and keep it on. `library_fake_test.go` is 767 lines, so
      this is bounded but not small.

      ⚠️ **Do not chase green by normalizing `size`/`duration`/`progress`/`currentTime`/
      `startOffset`.** `normalize.go:19-20` records keeping them comparable as an explicit
      decision — they are real playback data. Normalizing them would make the suite pass while
      deleting the exact signal the gate exists to produce. Four environment-dependent keys
      (`fullpath`, `loadedat`, `ipaddress`, `useragent`) have already been normalized, which is
      what took the count from 13 to 12; that is the end of what normalization can honestly fix.

      Also still open from the same audit: 4 golden fixtures that no test ever loads.

## Dedup

- [ ] **Exact-candidate backlog is re-accumulating — fix the source, not the symptom.** The
      2026-07-18 prod triage drain worked exactly as designed (verified from the prod journal:
      `apply=true dismissed=7891 dismiss_errors=0`), taking exact-pending **9,074 → 1,311**.
      Measured again on **2026-08-12: exact-pending is 5,947** — a ~4.5× regrowth in 3.5 weeks.
      Dismissed also fell 9,242 → 8,258, so candidates are moving between states, not just
      being added.

      A second drain would buy another few weeks and teach us nothing. The question is what
      keeps *emitting* these candidates: the original population was 7,891 title-leak/stub junk
      caused by two iTunes-importer bugs (see `docs/dedup/STATUS.md` and the duration-ms /
      title-leak root-cause notes). Either those bugs still produce leaky titles, or the
      exact-layer keying still treats a stub as a real match.

      First step is measurement, not code: classify the current 5,947 with
      `maintenance.dedup-exact-triage` **in dry-run** and compare the population mix against
      the 2026-07-18 report (purgeable 7,891 / keep 278 / review 2,150). If the mix looks the
      same, the source bug is live; if it has shifted toward `review`, this is normal library
      growth and the alarm is false.

      Also note `stale-drain=3,059` and `stale-fp=384` now appear as exact-layer statuses that
      did not exist in the 2026-07-18 accounting — worth understanding before drawing
      conclusions from the pending count alone.

## 🧪 `internal/database` short tests intermittently HANG in CI, and raising the timeout has stopped working

**Observed 2026-08-12 on PR #2333.** `Coverage Floor (PR gate)` failed with:

```
panic: test timed out after 25m0s
FAIL	github.com/falkcorp/audiobook-organizer/internal/database	1500.048s
```

`1500.048s` is exactly the 25m ceiling, so the package **hung** — the elapsed figure is the
limit, not a measurement.

### Why this is a stall and not a slow package

Four samples, all on effectively the same code:

| Where | Result |
|---|---|
| `main` CI, 45 min earlier (#2332 merge, run 31613853389) | `ok internal/database` **200.894s** |
| PR #2333 branch, local isolated, `-short -count=1 -timeout 25m` | `ok internal/database` **280.696s** |
| PR #2333 branch, CI, first attempt (job 94175733438) | **HUNG**, panic at 25m0s |
| PR #2333 branch, CI, re-run of the same commit (job 94184589579) | **pass**, 13m19s |

Same commit, one hang and one pass ⇒ intermittent, not a code defect in that PR. The diff
that hit it touched only `internal/server` (three strings in a slice) plus tests and docs, and
has no path to `internal/database`.

### The part that should worry us

**#2270 raised this timeout from 10m to 25m** for the same class of failure. The ceiling has
now been hit at *both* heights. Raising it again is symptom treatment — a hung test will
exhaust any limit. See `docs/audits/` and the `project_ci_gotests_intermittent_stalls` notes
for the earlier 600.764s (= default 10m) instances.

### Lead worth following first

The local run is overwhelmingly **wait-bound, not CPU-bound**:

```
17.20s user  21.03s system  13% cpu  4:44.87 total
```

~90% of wall-clock is spent waiting (I/O, locks, or sleeps), which is both why the package is
slow and why it is the most likely one to stall when a CI runner is contended. A hang here is
probably a lock/channel/`WaitGroup` that a contended scheduler can expose, not slow
computation.

### Tasks

- [ ] Capture a goroutine dump from a real failure. **Do NOT `gh run rerun` before saving the
      log** — the re-run overwrites it, and the panic dump names the stuck test. That evidence
      was destroyed on this occurrence.
- [ ] Once a stuck test is named: find the unbounded wait. Look for `sync.WaitGroup.Wait`,
      channel receives, and `Lock()` calls with no context/deadline in `internal/database`
      tests and helpers.
- [ ] Consider a per-test deadline (`t.Context()` / `context.WithTimeout`) so a hang fails in
      seconds naming itself, instead of consuming the whole package budget and reporting only
      the package name.
- [ ] Reduce the wait-bound cost while there — 200–280s for a `-short` run of one package is
      most of the coverage gate's budget on its own.

**Not urgent for correctness** — no product bug is implied, and a re-run clears it. It is a
throughput and trust problem: a red gate that is sometimes meaningless trains us to re-run
instead of read, which is exactly how a real failure gets waved through.

---

## ✅ Second, unrelated Coverage Floor failure — FIXED in this PR

Reading the log instead of re-running found a **different** defect. `Coverage Floor` failed
again on the docs-only commit of this very PR, at 15m27s (not a timeout — it ran and failed):

```
--- FAIL: TestServerStartGracefulShutdown (13.99s)
    server_more_test.go:346: timeout waiting for server shutdown
```

The log showed `"Server exited"` had **already been logged**. The server shut down correctly;
the test simply stopped waiting too early.

**Root cause: the test's budget was smaller than the shutdown path's own designed waits.**

| Step | Budget the implementation allows |
|---|---|
| ops-registry shutdown (`server_lifecycle.go:580`) | **10s** |
| ↳ goroutine drain inside it (`operations/registry/registry.go:1042`) | 2s, observed firing **twice** |
| HTTP shutdown + `bgCtx` drain + four store closes | unbounded |
| **the test's total allowance** | **5s** |

4 of those 5 seconds were consumed by deliberate waiting before any real work, leaving ~1s of
margin — fine on an idle laptop (5/5 passes locally, ~14.2s each), tips over on a contended
runner.

Raised to 60s with the arithmetic recorded in-code. This is **not** raising a limit to make
red go green: a genuinely hung shutdown still fails, exactly as it would have at 5s. It only
removes the case where a *correct* shutdown loses a race with its own assertion. Verified
3/3 after the change.

**Two further hazards in that test, left alone deliberately (not in scope here):**

- [ ] `syscall.Kill(os.Getpid(), syscall.SIGTERM)` signals the **entire test binary**, not a
      child. Every test in the package shares that process, so this is a global side effect
      fired from one test. It works today; it is a trap for whoever adds parallelism.
- [ ] The unconditional `time.Sleep(6 * time.Second)` before the signal is pure wall-clock
      cost paid on every run, in a package that is already the gate's biggest consumer.

### Meta-observation worth acting on

**The `Coverage Floor` gate failed 2 of 3 runs today, each for a completely different and
unrelated reason** (`internal/database` hang; `TestServerStartGracefulShutdown` margin). One
was environmental, one was a real defect that had been sitting there — and the only reason
the real one was found is that the log was read instead of the job re-run. That ratio is the
argument for treating this gate's flakiness as a work item rather than a nuisance.

## Docs / API

- [ ] **The OpenAPI spec still documents 48 endpoints that no router serves.** After the
      2026-08-12 union merge, `docs/api/openapi.json` was diffed against the **real** route
      table (obtained by calling `s.router.Routes()` on the actual router, not by grepping),
      and 48 documented operations have no matching route. They fall into three groups:

      1. **Group-relative artifacts** — the spec's generator missed Gin group prefixes, so it
         recorded `/login` instead of `/auth/login`, `/books` instead of `/itunes/books`,
         `/{id}` instead of `/ai/scans/{id}`, and so on. The correctly-prefixed paths are now
         present (they came from the YAML), so these are duplicates of real endpoints.
      2. **Removed maintenance endpoints** — 16 `POST /maintenance/*` paths. Only
         `/maintenance/wipe` still exists as a POST; the rest became registry operations
         (`maintenance.dedup-books` etc.) dispatched through the ops API.
      3. **`/torrents`** — group-relative fragment of the Deluge integration group.

      Two more (`/compare`, `/path`) were already removed in the merge because duplicate
      `operationId: "unknown"` made the spec fail validation. `/path` is the sharpest
      illustration of the whole problem: it was scraped out of a **code comment** at
      `internal/server/server.go:988`.

      This matters for the same reason as the `/auth/openid` and `/socket.io` probes: a
      client that trusts the spec and gets a 404 is worse off than one with no spec at all.

      Not removed in the merge PR because each deserves individual confirmation, and because
      a test-server route table may omit conditionally-registered routes (integrations behind
      a flag). The group-relative ones are safe to delete on sight; the maintenance ones
      should be checked against whether an ops-API equivalent should be documented instead.

      Full list:

  - `DELETE /invites/{token}`
  - `DELETE /sessions/{id}`
  - `DELETE /{id}`
  - `GET /books`
  - `GET /import-status/{id}`
  - `GET /invites`
  - `GET /library-status`
  - `GET /me`
  - `GET /sessions`
  - `GET /status`
  - `GET /torrents`
  - `GET /{id}`
  - `GET /{id}/results`
  - `POST /accept-invite`
  - `POST /import`
  - `POST /import-status/bulk`
  - `POST /invite`
  - `POST /login`
  - `POST /logout`
  - `POST /maintenance/backfill-book-files`
  - `POST /maintenance/cleanup-backups`
  - `POST /maintenance/cleanup-empty-folders`
  - `POST /maintenance/cleanup-organize-mess`
  - `POST /maintenance/cleanup-series`
  - `POST /maintenance/dedup-books`
  - `POST /maintenance/enrich-book-files`
  - `POST /maintenance/fix-author-narrator-swap`
  - `POST /maintenance/fix-book-file-paths`
  - `POST /maintenance/fix-library-states`
  - `POST /maintenance/fix-read-by-narrator`
  - `POST /maintenance/fix-version-groups`
  - `POST /maintenance/generate-itl-tests`
  - `POST /maintenance/recompute-itunes-paths`
  - `POST /maintenance/refetch-missing-authors`
  - `POST /rebuild`
  - `POST /setup`
  - `POST /sync`
  - `POST /test-connection`
  - `POST /test-mapping`
  - `POST /validate`
  - `POST /write-back`
  - `POST /write-back-all`
  - `POST /write-back/preview`
  - `POST /{id}/apply`
  - `POST /{id}/cancel`
  - `POST /{id}/deactivate`
  - `POST /{id}/reactivate`
  - `POST /{id}/reset-password`

## Security

- [ ] **SEC-9: the OpenAI API key is sent from the browser.**
      `web/src/components/wizard/WelcomeWizard.tsx:147-160` calls
      `fetch('https://api.openai.com/v1/models', { Authorization: \`Bearer ${openaiKey}\` })`
      directly from the client during setup, to validate the key the user just typed.

      This puts the key in the browser's network log, in any extension with request access, and
      in whatever the user's corporate TLS-inspecting proxy keeps. It was flagged as SEC-9 in
      `docs/audits/2026-06-22-repo-optimization-security-sweep.md` and is still live seven weeks
      later — surfaced again 2026-08-12 while assessing that audit for archivability.

      The fix is a server-side validation endpoint: POST the key to the backend, let the backend
      call OpenAI, return valid/invalid. The key then never leaves the origin. The wizard flow
      does not change from the user's point of view.

      Sibling findings from the same audit that ARE fixed (so this is not a stale doc):
      SEC-1 (committed `abk_` key), SEC-3 (temp-login trusting the `Host` header,
      `auth_temp_login.go:128`), SEC-4 (security headers, `server_middleware.go:103-109`),
      TOOL-2 (`mockery ... || true` removed from CI), TOOL-8 (2026-08-10).

## Docs

- [ ] **Give the 2026-06-22 security sweep a status column so it can eventually be retired.**
      `docs/audits/2026-06-22-repo-optimization-security-sweep.md` carries **41 finding IDs**
      (ARCH-1..8, FE-1..8, PERF-1..8, SEC-1..9, TOOL-1..8). Exactly **one** of them (PERF-1)
      appears anywhere in `TODO.md`. There is no other tracker, so the document is the sole home
      of ~40 findings whose current state nobody knows.

      It is demonstrably still live — `changelog.d/20260810_213500_make_test_everything.md:22`
      draws down TOOL-8 — so it cannot be archived. But it also cannot be *trusted*: a
      2026-08-12 spot-check of 5 IDs found 4 already fixed (SEC-1, SEC-3, SEC-4, TOOL-2) and 1
      still live (SEC-9, filed separately). At that rate most of the document is describing
      problems that no longer exist, which makes the few real ones easy to miss.

      The cheap fix is a status column — verify each of the 41 against HEAD once, mark
      fixed/open/obsolete with a `file:line`. Then the open ones can move to `TODO.md` and the
      document becomes archivable. This is a bounded, mechanical pass, and it is the thing
      standing between this audit and retirement.

### ABS author and series detail routes are unimplemented and redirect into a 404

Found by enumerating the paths the app ACTUALLY requested in the server log, rather
than by reading our own route table — the table can say which routes exist, never
which absent routes are being asked for.

```
  6 /api/v1/authors/:num      <- the redirect target
  3 /api/authors/:num         <- what the app asked for
  1 /api/series
```

The 1:2 ratio is the redirect signature (each request logs the 301 and the target).
Probed against production:

| route | result |
|---|---|
| `GET /api/authors/:id` | 301 → `/api/v1/authors/:id` → **404 "endpoint not found"** |
| `GET /api/series/:id` | 301 → `/api/v1/series/:id` → **404** |
| `GET /api/series` | 301 → `/api/v1/series` → app-API shape (`{"data":{"items":[…]}}`) |

There is no `GET /authors/:id` in `wire_entities_routes.go` at all — only
`/authors/:id/books`, `/authors/:id/aliases`, `DELETE /authors/:id` and friends. So
the ABS author page asks for an author and gets a 404, which the ABS contract tells
clients to treat as "unsupported, degrade gracefully" — it renders empty, silently.
Same failure the playlist detail route had.

**This is NOT a quick prefix reservation.** The app API has live routes at
`GET /series/:id/books`, `PATCH /series/:id`, `PUT /series/:id/name`,
`POST /series/:id/split`, `DELETE /series/:id`, and the author namespace is denser
still. Reserving `/api/series/` or `/api/authors/` wholesale is exactly the defect
that took out 46 live app routes twice (#2332 → #2335) and again, more narrowly, in
the playlist reservation. Use `absCollisionDetailRoutes`, which matches on method
plus exactly one segment.

Work:

1. `GET /api/authors/:id` — ABS author DTO with its `libraryItems`. The id is ours
   (it comes from our own `/libraries/:id/authors` list), so no id-mapping problem.
2. `GET /api/series/:id` — ditto; the per-series book build already exists and is
   cached (`seriesBooksCached`).
3. Decide what bare `GET /api/series` should do. It currently redirects to an
   app-API shape an ABS client cannot parse — the "looks implemented, behaves
   broken" case. Either serve it or reserve it as an honest 404.
4. Each addition needs a `TestPlaylistReservationDoesNotSwallowAppSubRoutes`-style
   companion listing that namespace's app routes, or the next widening repeats this.

## ABS surface — what is still missing after the series/playlist fix

Reported from the app 2026-08-13: playlists opened empty, series showed unrelated books
while claiming zero, collections were empty. The first two are fixed; this records what
was deliberately left, so "playlists and series work now" is not read as "the ABS surface
is complete".

- [ ] **Collections do not exist — this is a FEATURE, not a wiring fix.** `/api/collections`
      404s and `/api/libraries/:id/collections` returns an empty page, and both are
      **honest**: there is no `Collection` model, store, or route anywhere in
      `internal/database`. Contrast with playlists, where an empty response was hiding a
      fully populated `UserPlaylist` model — that asymmetry is the whole point. "Returns
      an empty page" is not by itself evidence of a gap; check whether a backing model
      exists before costing the work. Building this is a new entity end to end: storage,
      CRUD, ownership, ordering, plus ~10 upstream routes. Cost it before starting.
- [ ] **Series DETAIL is still not served.** `/api/series/:id` 301s into the app API, the
      same class of bug that made playlists open empty. It was not fixed alongside
      playlists because populating the series LIST (which the client renders) addressed
      the reported symptom, and claiming `/api/series/` from the redirect is a second
      routing decision that deserves its own change. Upstream also has
      `GET /api/libraries/:id/series/:seriesId`, which sits under the already-reserved
      `/api/libraries/` prefix and therefore needs **no** routing decision at all —
      prefer that route first.
- [ ] **The series list ignores `limit` and `page`.** It returned all 14,625 series in one
      response before this change and still does; the books are now embedded, so the
      payload grew. Upstream supports both params
      (`abs-upstream-api-reference.md:115-117`). Not changed here because introducing a
      default page size would silently truncate a client that currently receives
      everything — that is a behaviour change needing its own decision, not a side effect
      of a bug fix.
- [ ] **`testdata/abs-fixtures/get_api_libraries_id_series.json` contains ZERO series.**
      It was captured against an empty library, so it cannot settle the `books` contract
      and a green assertion against it proves nothing about series membership. The shape
      used here came from the upstream reference instead. Re-capture against a populated
      library before treating that fixture as an oracle. Same trap as the sessions fixture
      holding 3 items against a page size of 10.
- [ ] **`docs/reference/abs-target-client-contract.md` §11 lists playlists as "safe to
      stub", and that guidance is now falsified.** A user opened a playlist in the app and
      got an empty screen, so a client demonstrably calls the surface. The §11 list rests
      on the same fixture corpus that contains zero playlist requests — absence there
      bounds what the fixtures prove, never what the client does. Re-check every other
      entry in that list against real app behaviour rather than against the corpus.

### ABS API — sweep every endpoint for accepted-but-ignored query parameters

Three separate bugs on 2026-08-13 were the same defect wearing different hats: an
endpoint accepted a query parameter and read it with nothing, then answered `200`
with a wrong-but-plausible body.

- `GET /api/libraries/:id/items` — `filter` ignored, so every filtered request
  returned the whole library. This is what made series show "random books".
- `GET /api/libraries/:id/series` — `page`, `limit`, `sort` ignored. `limit=100`
  and `limit=500` both returned all 14,625 rows.
- (fixed earlier) the same surface's `sort` on items, noted in `absItemFilter`.

All three were invisible to the 28-fixture conformance oracle because **no fixture
carries a query parameter at all** — the corpus bounds what it can prove, and a
parameter that never appears in a capture can never be asserted on.

Work to do:

1. Enumerate every ABS route from `absRouteList()` and, for each, diff the query
   parameters upstream ABS 2.36.0 documents against the ones the handler actually
   reads. `c.Query(` / `c.GetQuery(` grep is the starting point, not the answer —
   the failure mode is a parameter read by *nobody*, which greps as absence.
2. For each unread parameter decide explicitly: honour it, or return an empty /
   error response. Never silently ignore — an ignored filter is strictly worse
   than an unimplemented one, because the wrong answer looks like a right answer.
3. Consider a test that drives each route with a parameter set and asserts the
   response *changes*. A parameter that provably makes no difference is the bug.
4. Re-capture the oracle fixtures with query parameters present, so the
   conformance suite can see this class at all.

- [ ] 💾 **Run `maintenance.booksig-sidecar-migrate` on production** — the op is
      merged and dry-run gated, but the ~580 MB/startup saving from PR #2387 is
      **not realized until the data actually moves**. #2387 shipped the
      `book_sig:<id>` sidecar with fallback-first reads, so all 67,824 rows still
      carry their signature inline and warmup still reports
      `discarded_field_mb[book_sig_v1_and_mask] = 580` against
      `phase_mb[books] = 729`. This is the only irreversible step in the sidecar
      design, so it needs an owner decision, not a scheduled run.

      Ordered procedure:

      1. **Dry run first**, whole library. Read the reported counts:
         `migrated / stripped-only / not-candidate / skipped-raced / errors`.
      2. **Instrument check before applying.** Compare against a NUMBER, not a
         vibe. 580 MB of inline signature at ~22 KB per book implies roughly
         **27,000 candidates** — i.e. well under half the library, because most
         books never had a signature. The op prints this cross-check itself as
         "candidates imply ~N MB", computed from the CANDIDATE count, so a
         healthy dry run should land near **~580 MB**. Two failure shapes:

         - reports all 67,824 as candidates → implies ~1,459 MB, which
           disagrees with the 580 MB warmup measurement by 2.5×: the detector is
           matching books that have no signature.
         - reports a few hundred → implies single-digit MB: the detector is not
           recognizing the inline shape at all.

         Either way, stop — do not apply on a detector that disagrees with the
         byte accounting. Note the 22 KB figure is itself derived from the
         580 MB total, so this checks the detector's population, not the size.
      3. **Canary**: apply with `{"dryRun":false,"limit":100}`. Do NOT assume the
         limited run is a stable prefix the full run resumes past — `ListBookIDs`
         has two implementations (memdb index order, which also drops
         soft-deleted books, vs. the Pebble key range) and which one answers
         depends on warmup state. The op is idempotent, so a full run simply
         re-examines the canary's books and reports them `not_candidate`; that
         is the guarantee to rely on, not the ordering.
         Verify the pairing on a named book: `GetBookByID` must return a non-nil
         `BookSigV1`, and its `book:` row must no longer contain
         `book_sig_v1`.
      4. **Full apply**: `{"dryRun":false}`. Expect to need MORE THAN ONE pass.
         Besides raced rows, the memdb `ListBookIDs` skips soft-deleted books,
         so a single run is not guaranteed to have enumerated every row still
         carrying an inline signature. Step 6's "candidates ≈ 0 on re-run" is
         the completion signal — not "the apply finished without errors".
      5. **Verify with the positive pair, not an absence.**
         `discarded_field_mb[book_sig_v1_and_mask] → 0` is weak evidence — it
         reads zero if the migration worked *or* if the field accounting stopped
         recognizing the field. Require instead that **`phase_mb[books]` actually
         drops from 729** AND a `GetBookByID` on a named migrated book still
         returns its signature.
      6. **Re-run the dry run.** Candidates should be ~0. Any `skipped-raced`
         count from the apply is books another writer touched mid-migration;
         they were skipped rather than reverted, and a second pass picks them up.

      Rollback: reads stay fallback-first, so migrated and un-migrated rows both
      work throughout. The row rewrite is irreversible in place but not lossy —
      the signature lives in the sidecar, and every migrated book keeps its
      pre-migration `book_ver:` snapshots, which still carry the full inline copy
      (`UpdateBook` never strips those), so `booksig-recovery-audit` remains a
      second-line recovery path.

- [ ] 🎧 **Run `maintenance.chapters-backfill` against production.** The op ships
      dry-run-by-default and has never been run on the real library. Sequence:
      (1) dry run over the `job (chapter-backfill test cohort)` static playlist
      (id `01KZXMN8F8ZEXVQQPZ2SF74T0A`, 77 books, 58 single-file) via
      `{"bookIds": [...]}`; (2) apply over that cohort and verify against the
      ffprobe oracle — `Deadly Jobs` must report **231** chapters, `The Icarus Job`
      **28**, `The Colchis Job` **20**, and the two markerless files
      (`132 132 - Job.m4b`, `Delve 132 - Chapter 132 - Job.m4b`) must stay at the
      synthesized single chapter; (3) only then a whole-library apply. Expect
      roughly 14,600 single-file candidates of which about half carry markers.

- [ ] 🔁 **Wire a durable "probed, found none" marker before this op is ever
      scheduled.** `SaveChaptersForBook` deletes its key on an empty slice, so a
      book with no embedded markers is byte-identical to one that was never
      examined, and every run re-ffprobes that whole population (~half of
      single-file containers). That is acceptable for a manual op and NOT
      acceptable nightly. `internal/operations/freshness` already provides
      `Stamp`/`ClearStamps` but has **zero non-test callers** — reaching it from an
      op needs a new `ServerDeps` accessor plus server wiring. Adding a `Schedule`
      to the op without doing this first is the bug.
      `TestChaptersBackfill_NoMarkers_WritesNothingAndReprobes` pins the current
      behaviour and will fail loudly when the marker lands.

- [ ] 🔍 **Index track names so smart playlists can match them.** The Bleve
      `BookDocument` (`internal/search/document.go:19`) carries only book-level
      fields — title, author, narrator, series, publisher, description, file_path.
      Track names live in `BookFile.FilePath` / `BookFile.Title` and are never
      indexed, and smart playlists evaluate exclusively through Bleve, so **no
      dynamic playlist can match a track name**. Verified: three copies of the
      Scourby Bible readings have a track literally named `Job` and appear in zero
      search results for "job". Needs a `TrackNames []string` field on
      `BookDocument`, a text field mapping, and a full reindex. Until then,
      track-name cohorts must be built as static playlists.

- [ ] 🧩 **Investigate per-chapter split files standing as their own books.** While
      probing, item `97e56ed2` turned out to be a 463 s fragment
      (`01 Angel in the Whirlwind - 1 - The.m4b`) registered as a standalone book,
      and several sampled "single-file" books are per-chapter splits mis-grouped
      the same way. Unrelated to chapter extraction — those files genuinely have no
      markers — but it inflates the single-file population and produces 8-minute
      "audiobooks".

## Library data integrity — surfaced by the chapters-backfill cohort run

Measured 2026-08-13 against the 77-book `job` test cohort on production. These are
**pre-existing defects the backfill exposed**, not regressions from it.

- [ ] **`BookFile.FilePath` rows point at files that do not exist — 16,130 books
      library-wide, 33.7% of all single-file books.** ⚠️ The cohort figure this
      was first written from (14 of 58, 24%) understated it; a whole-library dry
      run put the real number at `probe-failed=16130`, and an independent `test -e`
      sweep over a 400-book random sample agreed at 88/295 = 29.8% (which is what
      rules out ffprobe concurrency exhaustion — `test -e` has no subprocess to
      exhaust). Of 88 sampled missing rows, **86 (97.7%) have a `Book.FilePath`
      that IS a regular file on disk**; only 2 are genuinely gone. So this is
      recoverable, not data loss.
      **MITIGATED, NOT FIXED (2026-08-13, PR #2372):** `maintenance.chapters-backfill`
      now falls back to `Book.FilePath` when the `BookFile` path does not resolve,
      recovering ~16k books. That is a workaround inside ONE op — the stale rows
      are still stale, and every other consumer that resolves a file by stored
      path still degrades silently on them. The row repair itself is still open.
      The op probed
      `.../Timothy Zahn/The Icarus Job/The Icarus Job/The Icarus Job - Timothy Zahn - read by narrator.m4b`;
      the file actually lives at `.../Unknown Author/The Icarus Job/`. Eight of
      the fourteen are filed under `Cliff Kurt`, **who is the narrator, not the
      author** — the real files are under `PZG/`. The signature is a path
      recomputed from edited metadata without the file ever being moved (or a
      re-organize that never wrote back the `BookFile` row). Any op that resolves
      a file by stored path silently degrades on these. Full list:
      `probe-failed=15` in op `01KZXSZM5K6DA7QP21DPRAR17C`.
- [ ] **`Book.FilePath` and `BookFile.FilePath` disagree for the same book.** For
      `The Icarus Job` the book row points into the iTunes tree while the book-file
      row points at a nonexistent path under the organized tree. Any consumer that
      picks the "wrong" one gets a different answer. Decide which is authoritative
      and make the other derive from it.
- [ ] **`Book.FilePath` is NOT unique — 1,264 values are shared by more than one
      book row (4,353 of 63,870 rows, 6.8%).** This bounds how far the #2372
      fallback can safely be reused: anything that resolves a book to a file via
      `Book.FilePath` can land on a row belonging to a different book. It happens
      not to bite the chapters backfill (0 of the 88 sampled recoverable rows are
      among the 4,353), but that is a property of today's data, not a guarantee —
      **re-run the collision count before extending the fallback to any op that
      WRITES a book row**, since chapters go to their own `chapters:<bookID>`
      keyspace and a book-row write would not be so contained. Likely the same
      root cause as the duplicate-book-rows item below.
- [ ] **Stored `duration` is short of the real container by 119–186s on 7 cohort
      books.** Confirmed by ffprobe: `Mushoku Tensei … Vol. 03` stores 33582s while
      both physical copies measure 33767.759s. The chapter timelines written by the
      backfill are correct; the duration field is stale. Related:
      `project_duration_filesize_aggregation` (snapshots, not sums).
- [ ] **Multi-file chapter synthesis produces a timeline that stops short.** One of
      the two `Genesis` rows (1,189 files) serves 1,189 chapters ending at 32,636s
      against a 258,256s duration; its twin ends correctly at 258,256s. The mapper's
      per-file synthesis is picking up wrong or missing per-file durations.
- [ ] **Duplicate book rows per title under different author folders** (`Deadly Jobs`
      ×3, `The Icarus Job` ×3, every `Mushoku Tensei` volume ×2 as `PZG` and
      `Unknown Author`). Worth checking as a *source* for exact-pending dedup
      regrowing to 5,947 by 2026-08-12 — that note says it needs a source fix
      rather than another drain. Pointer only; not chased here.

## Follow-up on the op itself

- [ ] `registry.RunItems` label re-render (fixed 2026-08-13) changed shared
      infrastructure used by every op. Progress labels for other ops now advance
      one item later than before — verify none of them assumed the old timing.
- [ ] One unreproduced failure of `internal/plugins/maintenance` was observed on
      2026-08-13 during mutation testing; 8 subsequent runs (3 under `-race`, plus
      `internal/operations/registry`) were green and the failure detail was not
      captured before re-running. If it recurs, capture the output first.

## BUG: 10,780 version groups elect MORE than one primary

Found 2026-08-13 while repairing the opposite defect (groups electing *no*
primary, fixed by `ElectMissingPrimaries`). A full per-group census of all
63,870 books found:

| shape | groups |
|---|---|
| exactly one primary | ~13,530 |
| **more than one primary** | **10,780** |
| zero primaries | 479 (repaired) |

So the multi-primary shape is not an edge case — it is nearly half of all
version groups.

**The member-count histogram is the lead.** Of the 10,780 groups: 9,824 have
exactly 3 members, 842 have 2, 110 have 4, 4 have 5-6. A single shape repeating
~9,800 times is a systematic writer with a rule, not data drift.

**Some groups contain genuinely different books**, which means this is not only
a flag-accounting bug — the grouping itself is wrong:

```
group 01KNDCGCM62AGA9GYGV3G0523J  members=3 primaries=2
   primary=True   The Boxcar Children Collection, Volume 2
   primary=True   Mike's Mystery              <- a different book
   primary=False  The Boxcar Children Collection, Volume 2

group vg-67868a6ffc2aa170  members=4 primaries=2
   primary=True   Singularity Online Book 2
   primary=True   - Sorcerer Ascendant (2020)  <- title is a bare subtitle
   primary=False  Singularity Online Book 2
   primary=False  - Sorcerer Ascendant (2020)
```

Note the second group's id is `vg-67868a6ffc2aa170` — 16 hex chars, **not** the
`vg-<ULID>` shape the current code mints. That is a third, older id format and
probably identifies the writer responsible.

### Steps

1. Identify the writer(s) minting 16-hex-char `vg-` ids. The id format is the
   cheapest available fingerprint — bucket the 10,780 groups by id shape first
   and see whether multi-primary correlates with one shape.
2. Decide whether the fix is demotion (too many primaries) or **regrouping**
   (books that should never have shared a group). The Boxcar Children sample
   says at least some are the latter, and demoting a primary there would paper
   over a wrong group rather than fix it.
3. **Do not write a blind demotion pass.** Unlike electing a missing primary —
   which strictly increases visibility and is safely reversible — demoting a
   primary can hide a book that is currently visible. Any apply needs a dry run
   reviewed against real samples first.
4. Extend the invariant to both directions once the cause is known: every
   version group elects **exactly** one primary. The zero-primary half is
   already asserted in `TestVersionGroupInvariant_ZeroPrimaryGroupsAreRepaired`.

### 6,157 books are invisible in the web UI — `vg-` singleton groups elect no primary

Confirmed against production 2026-08-13. Reported as "search is broken"; search is fine.

Books whose `version_group_id` carries a **`vg-` prefix** sit one-per-group with
`is_primary_version=false` and **no primary elected anywhere in the group**. The web
Library page filters to primary versions by default, so a singleton group with no
primary can never satisfy it and the book cannot be seen — in search, in browsing, or
in any count. The AudiobookShelf app applies no such filter, which is why the same book
is visible there and was reported as a search discrepancy.

| | |
|---|---|
| total books | 63,870 |
| `is_primary_version=true` | 40,839 |
| `is_primary_version=false` | 23,031 |
| **`vg-` singletons with no primary** | **6,157 (9.6% of the library)** |

Counted exhaustively — all 24 pages of the non-primary set, not sampled. Every affected
book was created between **2026-04-04 and 2026-08-11**; zero `vg-` rows appear in the
older 11,031 non-primary books, which bounds the writer to that window.

Healthy groups use an unprefixed id and elect exactly one primary (verified on a real
two-member group). The `vg-` prefix is the tell — find what mints it.

Work:

1. Find the writer that mints a `vg-`-prefixed `version_group_id` without electing a
   primary. That is the defect. Everything else is cleanup.
2. Backfill the existing 6,157. The sole member of a single-member group should be its
   primary — but **verify the group is genuinely a singleton before flipping the flag**,
   or a real duplicate pair ends up with two primaries and the dedup UI inherits a new
   class of bug.
3. Add a data invariant: **no version group may have zero primaries.** This is the shape
   of defect an invariant suite catches and no unit test will — see the existing
   data-loss invariant suite (#1930–#1942) for the pattern.
4. Check whether anything else keys off the `vg-` prefix before renaming or normalising
   it.

Related but independent, found in the same investigation and recorded in
`docs/handoffs/2026-08-13-web-search-returns-unrelated-books.md`: search silently drops
English stopwords (`all jobs` searches only `jobs`, returning 283 results), and quoted
phrases never become a `MatchPhraseQuery` because the parser leaves the quote characters
attached to the terms.

- [ ] 🤔 **Decide whether the newly-implemented filter fields belong in
      `filterFieldQueryParams`.** The bare-parameter guard
      (`internal/server/handlers/audiobooks/handler.go`) rejects a request that
      passes a *filter field* as a bare query parameter, because gin ignores the
      unrecognized parameter and the request silently lists the whole library.
      Fourteen field names became filterable on 2026-08-14 (`year`, `duration`,
      `file_size`, `bitrate`, `sample_rate`, `channels`, `bit_depth`,
      `series_number`, `isbn10`, `isbn13`, `work_id`, `created_at`,
      `updated_at`, `marked_for_deletion`) and none were added to that set, so
      `?year=2019` still silently returns all 63,869 books.

      **Deliberately not done in the same PR**, and the reason is written above
      the set itself: including a name wrongly is *not* harmless — it rejects a
      request that used to work. `library_state` is the standing example, a real
      bare parameter that an earlier revision added to this set and broke
      `TestListBooksWithTagFilter` with. `created_at` and `updated_at` are the
      obvious suspects here (sort keys, plausibly read bare somewhere), and
      `duration` and `file_size` are not obviously safe either.

      Before adding any name, check **every accessor spelling** it might be read
      under — `c.Query`, `c.QueryArray`, `httputil.ParseQueryString`,
      `ParseQueryInt` — not one grep of one form. The survey that produced the
      current set grepped only `c.Query("…")` and so could not see the
      `ParseQueryString` form, which is exactly how `library_state` got in.

      Now that `audiobooks.FieldIsKnown` exists there is a tempting derivation:
      make the guard consult it and subtract the genuine bare parameters. That
      is probably the right end state, but it inverts the safety property — the
      set stops being opt-in and becomes opt-out, so a new filter field
      automatically starts rejecting a bare parameter of the same name. Worth
      doing only with the accessor survey above done properly first.

## Organize/apply rename paths: three hand-verified silent failures

Found while unifying the target-path builders (`refactor/unify-path-builders`).
All three were read at the source and confirmed by hand — this is the
hand-verified count, not a machine-flagged count. None is fixed; the unification
PR deliberately carried only the two defects that were the stated requirement
(directory organize was not file aware; organized `book_file` rows were derived
by guessing rather than from the planner).

- [ ] **F7 — `ApplyMetadataFileIO` returns nothing, so rename failure is
      unreachable to every caller.** `internal/metafetch/service_files.go:80`:
      `func (mfs *Service) ApplyMetadataFileIO(id string)` has no return value.
      A failure from the apply pipeline is swallowed into
      `slog.Warn("apply pipeline failed for", ...)`. Six call sites cannot
      observe it, and `internal/server/batch_apply_one.go:124` reports
      `Applied: true` regardless of what happened on disk. This is the one that
      most directly breaks "updates all the rows correctly": the API says the
      apply succeeded when the files never moved. Fix is mechanical but wide —
      an `error` return, two interfaces (`internal/server/batch_apply_one.go:29`,
      `internal/server/handlers/metadata_cache.go:61`), six call sites and two
      regenerated mock files — which is why it was split out.

- [ ] **F6 — `ensureLibraryCopy` treats an empty organize as success.**
      `internal/metafetch/service_apply.go:~349`: `newBookPath = targetDir` is
      set unconditionally after `OrganizeBookDirectory`, and
      `OrganizeBookDirectory` `MkdirAll`s that directory before copying
      anything. If every source file is absent from disk, the copies all skip,
      `pathMap` comes back empty, and a new primary book record is created
      pointing at an empty directory. Partially mitigated by the unification
      PR — `OrganizeBookDirectory` now errors when every row is flagged
      `Missing` — but rows that are *not* flagged and are simply gone from disk
      still produce the empty-directory outcome. Check `len(pathMap)` at the
      call site.

- [ ] **F5 (remainder) — `OrganizeBookDirectory` still cannot tell a resumed
      copy from a stranger.** The unification PR narrowed this: a destination
      that already exists is now adopted only when it is `os.SameFile` with the
      source or byte-identical in size, and an unrelated occupant is warned
      about and left alone instead of being written into `pathMap`. The size
      comparison is still a heuristic — two different files of equal size are
      adopted as the same file. A content hash (the codebase already has
      `scanner.ComputeFileHash`) would settle it, at the cost of reading both
      files.

**Also worth doing:** `MoveBookFile` (`internal/organizer/move.go:32-98`) is the
one function in the repo with the correct pattern — verify source, verify
destination absent, move, DB-update, and **roll the file back if the DB write
fails**. It is on none of the three rename paths. Routing them through it would
retire most of the above rather than patching each.

- [ ] **Split `AudiobookService`, not just its store interface.** `audiobookStore`
      (`internal/audiobooks/service.go:36`) is one of the four remaining
      `interfacebloat` findings. A compiler probe measured its true requirement at
      ~50 methods (44 direct calls, plus `RecordMetadataChange` and 5 author/series
      alias-and-count methods pulled in by assignability constraints). At <=7
      methods per group that needs 8 groups, which lands exactly on the linter's
      limit of 8 -- so a flat regrouping buys width but no headroom, and a nested
      tier of mid-level composites would score 3 while still carrying all 50
      methods, which is the wide-embed style with better names.
      The honest unit of work is the service itself: the probe bucketed its calls
      as `service_single.go` 23, `service_mutation.go` 20, `service_query.go` 15,
      `service_tags.go` 10, `service_filtering.go` 8, `helpers.go` 5 -- six real
      consumers sharing one `store` field. Split those into services with their own
      narrow stores and `audiobookStore` dissolves rather than being regrouped.

- [ ] Audit existing "we use the wide type because X requires it" comments across the
      codebase. Two were checked on 2026-08-18 and both were stale —
      `handlers.OrganizeStore` (`= database.Store`, 398 methods) and
      `handlers/operations.OperationsStore` both cited call sites that had since been
      narrowed. Grep for justification comments near `database.Store` /
      `database.BookStore` and re-verify each claim against the current signatures.

### ghcommon reusable-workflow pins are a month apart — decide, don't drift

Measured 2026-08-18. Eight workflows pin `falkcorp/github-common` at
`d0c3326b` (**2026-07-19**); `ci.yml` pins `828afb50` (2026-08-18). The older pin
is **22 commits behind**.

| Pin | Date | Workflows |
|---|---|---|
| `d0c3326b` | 2026-07-19 | `frontend-ci`, `nightly`, `nightly-burndown`, `hard-burndown`, `prerelease`, `release-prod`, `security`, `triage-poll` |
| `828afb50` | 2026-08-18 | `ci` |

- [ ] Decide whether to bump the eight, and do it in **at least two PRs** —
      not one. `release-prod.yml` and `prerelease.yml` are the risk: a reusable
      release workflow that broke somewhere in those 22 commits is not
      discovered until someone cuts a release, by which point the bump is
      several PRs back and no longer the obvious suspect. Bump the
      low-consequence ones (`triage-poll`, the burndowns) first and let them run
      a nightly before touching release or security.
- [ ] Not done unattended on purpose: this was left for a human on 2026-08-18
      rather than folded into the CI-wiring PR, because verifying a release
      workflow requires actually cutting a release.

Note this is drift, not inconsistency for its own sake — the eight point at
several *different* reusable workflows (`reusable-ci`, `reusable-release`,
`reusable-security`, `reusable-burndown`, `reusable-triage-poll`), so a single
shared SHA is a convention, not a correctness requirement.

### Decide whether `.golangci.yml`'s `\.worktrees/` path exclusion should be deleted

`scripts/check-interface-width.sh` (v1.4.0) now scopes `GOLANGCI_LINT_CACHE` per
worktree, so cached positions cannot cross checkouts and the exclusion is inert
*for the width gate*. It is still live for every other golangci-lint invocation,
including the `go-lint` CI job, which shares the global cache.

The evidence says it protects nothing and can only do harm: each worktree carries
its own `go.mod`, so `go list ./...` from the repo root returns **0** packages
under `.worktrees/`. Package loading never goes there. The exclusion therefore
only ever matches cache-replayed positions — and on 2026-08-18 that made the
width gate report 0 findings when the true count was 4, because all four replayed
with `.worktrees/` paths and were silently dropped.

**The discriminator before deleting it:** run golangci-lint from the repo root
with a clean, isolated cache and the exclusion temporarily removed, with a
sibling worktree present. If no reported path is under `.worktrees/`, the line is
confirmed inert and can go. If one is, golangci-lint's loader does not agree with
`go list` and the line is load-bearing.

Do this in a PR that owns the `go-lint` job's counts — removing it changes what
that job reports, and it is not the width gate's to change.

### Interface width: the five the sweep did not reach

The flat-list split sweep took `interfacebloat` from 28 findings to 5 (#2542,
#2545, #2546, #2547, #2549, #2550, #2553, #2554, #2556). The five survivors are
recorded in
[`docs/audits/2026-08-18-interface-width-shapes.md`](docs/audits/2026-08-18-interface-width-shapes.md)
§6 and are **phase-2 work, not leftovers** — none of them yields to
split-then-compose:

- [ ] `database.Store` (40) — make unreachable rather than smaller (plan phase 2).
- [ ] `itunes/service.Store` (17 declared / 24 called) — 7 assignability
      constraints incl. `database.OperationStore`; needs the parameter-type fix
      #2552 applied to its helpers first.
- [ ] `maintenance.JobStore` (12) — deliberate choice from the #2534 arbitration;
      revisit only as per-job interfaces (plan phase 2, item 1).
- [ ] `audiobookStore` / `audiobookUpdateStore` (11 each) — the service calls **44
      distinct store methods**. The finding is that the *service* is too big; do
      not re-group the interface in place, it scores worse on the gate and reads
      no better.

Gate state: the width ratchet (#2548) pins the baseline at 5, so these cannot
grow silently, and a PR adding a sixth has to justify it or add a `//nolint:
interfacebloat` with a reason.

- [ ] **Narrow `positionSyncStore` and `pathRepairerStore` — both blocked on a wide
      parameter type in another package, not on their own declaration.** These are
      the two of six iTunes subsystem stores left wide after the first narrowing
      pass. Direct calls are small (`positionSyncStore` 8, `pathRepairerStore` 5);
      what holds them wide is what they get passed to:
      - `readstatus.RecomputeUserBookState` and `readstatus.SetManualStatus` take an
        **anonymous composite** `interface{database.BookFileStore;
        database.UserPositionStore}` inline in their signatures. An anonymous
        interface cannot be narrowed in place or nolint-ed, and `interfacebloat`
        does not report it because it is not a declaration. Give it a name and
        narrow it to what `readstatus` actually calls.
      - `pathRepairerStore` is additionally passed somewhere wanting the whole
        `database.OperationStore`, plus `operations.OperationStateDeleter`,
        `pidLookup` and `tierAStore`.
      This is the #2552 lever one package out: fix the parameter types and the two
      leaves narrow themselves.
- [ ] **Re-probe `itunesservice.Store` after those two land.** Its measured
      requirement was computed against 151-method leaves, so it is stale by
      construction. Only then decide whether `Store` composes from the six
      subsystem interfaces or should be replaced by per-consumer interfaces —
      its 8 remaining methods (`CreateAuthor`, `CreateSeries`, `GetSeriesByName`,
      `SetBookAuthors`, `IsHashBlocked`, `SaveLibraryFingerprint`,
      `GetPendingDeferredITunesUpdates`, `MarkDeferredITunesUpdateApplied`) are
      import-pipeline writes belonging to none of the six.

- [ ] Decide whether maintenance jobs should take per-job store interfaces instead of the
      shared `maintenance.JobStore`. Measured 2026-08-18 after narrowing JobStore to 52
      methods: **23 of the 37 directly-called methods are used by exactly one job**, and
      only five are used by more than four (`GetAllBooksCore` 18 files, `GetBookByID` 12,
      `UpdateBook` 10, `GetAllBookFilesCore` 10, `GetBookFiles` 8). So most of the shared
      contract is not shared.
      The blocker is structural, not conceptual: `Run` is a method on `MaintenanceJob`,
      so every job must accept the same parameter type, and jobs register themselves at
      `init()` via `Register(job)` with no store in scope. Per-job stores means
      constructing jobs with their store instead — `All(store)` rather than `All()` — which
      touches the registry and both call sites (`maintenance_job_op.go:64`,
      `maintenance_dispatcher.go:26`). The second is deleted by phase 1 of
      `docs/plans/2026-08-17-kill-v1-and-narrow-store-interfaces.md`, so this is cheaper
      to do after the v1 retirement than before it.

### Narrow the `metafetch` → `organizer` store chain

Three constructors in `internal/audiobooks` still declare `database.Store` —
`NewOrganizeService`, `NewOrganizePreviewService`, `NewRenameService`. They are thin
forwarding layers, so they cannot be narrowed until what they forward into is
narrowed first. Measured 2026-08-18 with empty-interface compiler probes:

| Blocker | Shape |
|---|---|
| `metafetch.Service.db` (`internal/metafetch/service.go`) | struct field, `database.Store`. Probe: **36 direct calls** + constraints `database.BookFileHashUpdater`, `database.RawKVStore`, `organizer.OrganizerStore`, and `database.Store` itself. |
| `organizer.PreviewService.db` (`internal/organizer/preview.go:43`) | struct field, `database.Store`, plus `NewPreviewService(db database.Store)`. |

`metafetch`'s residual `database.Store` constraint comes from only two places, both
in-package or in `internal/database`, so it is not another layer of depth:

- `database.EnsureSingletonBookTag(db database.Store, ...)` — called from
  `service_apply.go:811,819`.
- `hasCheckpoint` / `setCheckpoint` / `clearCheckpoints` in
  `internal/metafetch/service_writeback.go` — local funcs taking `database.Store`,
  seven call sites. Their bodies look like KV access, so `database.RawKVStore` is
  the likely target.

Suggested order, leaf-first — each step is a separate PR and each one is green on
its own:

1. Narrow the four checkpoint helpers and `database.EnsureSingletonBookTag`.
2. Narrow `metafetch.Service.db` to the measured 36 + 3 constraints, grouped so
   both the union and each group stay at or under 8 declared entries.
3. Narrow `organizer.PreviewService.db` and `NewPreviewService`.
4. Narrow the three `internal/audiobooks` forwarding constructors. Their own
   requirement is small — `organize_preview.go` and `rename.go` each need only
   `organizer.Store` (or `database.Store` until step 3), `importPathLister` and
   `authorSeriesStore`.

The seven `audiobooks_compat.go` wrappers are already function-value aliases, so
step 4 propagates to the server package with no edit there.

## Config

- [ ] **Audit every config option name — the set has grown by accretion and the
      naming is inconsistent.** Prod's `/api/v1/config` currently returns **113**
      keys. They were added over a long period by different code paths, and nothing
      has ever reviewed them as a set, so the vocabulary drifted.

      The trigger: `write_back_metadata` is named for a whole subsystem but gates a
      single call site on the auto-fetch path, while the flag that actually controls
      tag writing on apply is `auto_write_tags_on_apply`. Reading the two together
      leads a reasonable person to the wrong conclusion about whether the app writes
      to their files. That one has its own entry above; this task is the sweep.

      **What to look for:**
      - *Scope lies* — a name broader than what it gates (the
        `write_back_metadata` class). Name the flag after its call site, not its
        subsystem.
      - *Asymmetric pairs* — two flags controlling the same behaviour on two paths
        that do not share a prefix (`..._on_fetch` / `..._on_apply`).
      - *Names that read as booleans but are not* — e.g. `organization_strategy`
        has the value `auto`, which sits next to `auto_organize` (a real bool)
        and invites exactly the confusion it caused today.
      - *Dead options* — a key that nothing reads. Verify by grepping the
        `AppConfig.<Field>` READ sites, not the field declaration: several keys are
        declared, bound to viper and persisted, yet never consulted. A key that
        cannot change behaviour should be deleted, not documented.
      - *Unclear units* — `auto_scan_debounce_seconds` is good; anything with a
        bare number and no unit suffix is not.

      **Method:** enumerate from the live `/api/v1/config` response, not from the
      struct — the struct carries fields the API does not expose and vice versa. For
      each key, find its READ sites and write down the one sentence that describes
      what it actually changes. Any key where that sentence does not match the name
      is a rename candidate.

      **Every rename needs the deprecated-alias migration** described in the
      `write_back_metadata` entry: live config is a persisted snapshot, so a bare
      rename silently reverts the setting to its default on next load.

- [x] **Bare `store.(*PebbleStore)` assertions swept — all 10 production sites
      converted (2026-08-19).** Zero remain outside `_test.go`, where they are
      correct: those build a bare `*PebbleStore` locally and never see the
      decorator. Found while removing the interface-shaped twin of this bug
      (#2580, 11 sites) — the concrete shape had never been swept.
      `internal/database/store_capability.go` documents the failure mode and
      records two prod jobs silently degraded for weeks by it.

- [x] **Provenance traced for every other site (2026-08-19).** The question that
      decides severity is **does this value come from `Server.Store()`?**
      `serviceregistry.Container.Build` runs **eagerly inside `NewServer`**, and
      `Override("store", resolvedStore)` seeds `KeyStore` with the **bare** store
      and is never replaced — so "built by a service-registry factory" means bare,
      lazily-built or not. `Start` installs the `indexedStore` wrapper afterwards
      onto `s.store` only, which is what `Server.Store()` returns.

      - `handlers/diagnostics.go` (`GetDBHealth`) — bare: the handler captures
        `s.Store()` in `wireHandlers` → `setupRoutes` → `NewServer`. Its methods
        running at request time does not change what it captured. Converted.
      - `wire_abs_routes.go:494` — **racy**, and the one real defect. Wiring runs
        inside `NewServer` like every other handler, but this site reads
        `s.Store()` *inside a goroutine*, so the read happens whenever that
        goroutine is scheduled — possibly after `Start` has written the wrapper.
        Construction time vs. read time is the whole distinction.
      - `scanner/process_file.go` — bare (`scanner.SetStore(resolvedStore)` in
        `NewServer`, never re-set). Converted defensively.
      - `dedup/lifecycle.go` — bare (`Get[dedup.Store](c, KeyStore)`). Defensive.
      - `dedup/engine.go` Tier-0 LSH lookup — bare, same reason. Defensive (#2598).
      - `database/migrations.go` — bare by construction. Defensive.
      - `plugins/dedup/*`, `plugins/acoustid/lsh_backfill.go` — all assert narrow
        interfaces that `database.Store` does not carry, but all hold the bare
        store via the container. Left alone.
      - `server/search_coverage.go`, `server/middleware/absauth.go`,
        `operations/registry/legacy_op_status.go`,
        `handlers/audiobooks/handler.go`, `handlers/audiobooks/handler_files.go` —
        compile-probed: every asserted method **is** in `database.Store`, so these
        resolve through the decorator regardless. Safe as written.

      Test-file assertions are out of scope: those build a bare `*PebbleStore`
      locally and never see the decorator.

### Dedup

- [ ] `Engine.SetLSHStore` and `Engine.SetAcoustIDBookFileStore`
      (`internal/dedup/engine.go`) have **no call sites anywhere** — not in
      production wiring, not in tests — so `de.lshAcoustIDStore` and
      `de.acoustidBookFileStore` are always nil and `CollectLSHAcoustID` /
      `CollectExactAcoustID` (`internal/dedup/collectors_acoustid.go`) never run.
      Verified structurally, not by name grep: both fields are assigned only
      inside their own setter bodies (`engine.go:202`, `engine.go:208`), and all
      four collector call sites — `engine.go:530`/`:536` and
      `rescore.go:233`/`:239` — sit behind an `if de.<field> != nil` guard, which
      is also why a nil store does not panic on `CollectLSHAcoustID`'s
      unconditional `store.IsLSHIndexBuilt()`. The collectors' own unit tests
      pass stubs directly and so cannot detect the missing wiring. Found
      2026-08-19 while fixing the neighbouring Tier-0 candidate-lookup decorator
      bug. Decide whether to wire them in `registry_wire.go`'s dedup engine
      factory (resolving the concrete store with `database.AsPebbleStore`, not a
      bare assertion) or delete the collectors and setters together.

## Dedup

- [ ] **Clean up the 2,504 already-orphaned dedup candidates — use the existing
      `dedup.purge-stale`, do NOT build a new op.** A 2026-08-19
      `dedup.breakdown-backfill` dry run reported `skipped_no_book: 2504`: pending
      candidates whose book row has been hard-deleted. Such a row is permanently
      stuck — resolving it 500s on "book not found", and it is never re-scored
      because every producer iterates live books only — so it sits in the pending
      queue forever. Together with the 2,713 zero-signal rows this is roughly half
      the pending backlog.

      The *recurrence* is fixed: `PebbleStore.DeleteBook` now cascades the teardown
      of a book's pending candidates, so no new orphans are created by any of its
      16 call sites. That commit does not clean the existing rows, because their
      books are already gone and there is no delete left to hook.

      The cleanup already exists and needs no new code: `PurgeStaleCandidates`
      (`internal/dedup/engine.go`) lists every pending book candidate across all
      layers and hard-deletes those with a missing book on either side — exactly
      this population. It is exposed as the `dedup.purge-stale` operation.

      **Why they accumulated:** `dedup.purge-stale` has no `Schedule:` on its
      OperationDef. It runs only when invoked manually, or as a step inside
      `dedup.full-scan` and the embedding backfill. With the cascade in place the
      source is closed, so scheduling it is likely unnecessary — but that should be
      confirmed after the cleanup run, by re-running `dedup.breakdown-backfill`
      as a dry run and checking `skipped_no_book` stays at 0.

      **Blocked on a user decision:** running it with `apply:true` mutates prod
      data, the same gate that `dedup.breakdown-backfill`'s apply is waiting behind.
      Note it deletes only `pending` rows — `merged` / `dismissed` rows are the
      historical records behind the UI's Merged / Dismissed tabs and are preserved
      by both `PurgeStaleCandidates` and the new cascade.

- [ ] 🧊 **`*PebbleStore` struct split — LOWEST PRIORITY. Literally do anything else
      before working on this.** Decision doc:
      [`docs/plans/2026-08-19-pebblestore-struct-split-decision.md`](docs/plans/2026-08-19-pebblestore-struct-split-decision.md).

      **Deliberately parked, not abandoned.** Keeping it visible so it is not
      re-derived from scratch a fourth time — it has now been costed twice and
      corrected twice, and each pass cost real effort to reach the same answer.

      **Why it is parked.** Re-derived by AST at `21808fdc`: only **14 of 558**
      `*PebbleStore` methods (2.5%) touch any domain-local field, while `db` alone is
      touched by **408 of 558** (73.1%) and 117 touch no struct field at all. The
      struct is overwhelmingly one shared handle plus behaviour, so splitting it by
      domain buys separation the field-sharing numbers do not support.

      **Two traps for whoever picks this up.**

      1. **Step 1 is not a deliverable on its own.** Extracting `core` and having
         `PebbleStore` embed it moves zero methods; it leaves all 558 in place *plus*
         a new indirection layer with no consumer. Strictly worse than either endpoint
         unless steps 2-6 also land. Do not ship it as a "first increment".
      2. **`libGen` and `counterMu` are CORE, not domain-local.** Two separate costing
         passes classified them as domain-local and both produced 20/3.6% instead of
         14/2.5%. `libGen` is bumped by `Create`/`Update`/`DeleteBook` and read by
         `LibraryGeneration`; `counterMu` guards the shared `nextID` allocator. Re-read
         the decision doc's own Corrections section before re-running any census — the
         error survived an independent instrument because the instrument faithfully
         reproduced a wrong *definition*.

      **Revisit if** the 14/558 ratio moves materially, or if domain separation becomes
      wanted for a reason other than field sharing (build times, ownership boundaries,
      testability) — in which case the field-touching measurement is not the right
      criterion and the case should be argued on that basis instead.

- [ ] **Finish killing `database.Store` — 18 references left outside `internal/database`.**
      Down from 398-method-wide everywhere; see
      `docs/plans/2026-08-18-decouple-database-layer.md`. The remainder splits into:
      - **7 left by design** — `internal/server/server.go` (the `store` field, `Store()`,
        `NewServer`, and the nil-store error text) and `internal/server/indexed_store.go`
        (the embedded `database.Store`, the `StoreUnwrapper` assert, and `Unwrap()`).
        These are the composition root and the decorator contract; they go away in plan
        phases 3–4 by splitting `PebbleStore` so `database.Store` becomes unreachable,
        not by narrowing them in place.
      - **3 test helpers** — `internal/testutil/integration.go` (rationale verified
        genuine: integration tests poke at any domain a scenario needs) and
        `internal/database/dbtest/invariants.go` ×2.
      - **8 the `Server.Store()` chain** — `internal/plugins/maintenance/deps.go` ×3 and
        `internal/server/server_maintenance_deps.go` ×2 plus their callers. Blocked on
        `Server.Store()` itself. ⚠️ `deps.go` forwards into `missing_file_repair.go` /
        `missing_file_audit.go`, which run against prod and are a separate hands-off
        lane — do not touch those without asking.

## Config

- [ ] **Rename `write_back_metadata` → `auto_write_tags_on_fetch`.** The current name
      reads like a global "do we ever write tags to files" switch. It is not. It
      gates exactly one call site — `mfs.writeBackMetadata(updatedBook, meta)` at
      `internal/metafetch/service_fetch.go:309`, on the **auto-fetch** path only.

      Tag writing on **apply** is a completely separate flag,
      `auto_write_tags_on_apply` (`internal/metafetch/service_writeback.go:604`),
      which is **on** in prod. So the two live side by side in the config with one
      named for what it does and the other named for the whole subsystem, and
      reading `write_back_metadata: false` naturally leads to "we're not writing
      tags to files at all" — which is wrong. That misreading already happened.

      Renaming makes the pair symmetric and self-documenting:
      `auto_write_tags_on_fetch` / `auto_write_tags_on_apply`.

      **Touch points:** `internal/config/config.go:531` (struct field + json tag),
      `:1445` (viper binding), `internal/config/persistence.go:1075` (snapshot
      load), `internal/metafetch/service_fetch.go:309` (the only read site), and
      `internal/config/config_unit_test.go:654`.

      **Migration matters — do not do a bare rename.** Live config is a persisted
      snapshot, not defaults (the stored value overrides `config.go`'s default).
      Prod's snapshot has the OLD key, so the loader must keep honouring
      `write_back_metadata` as a deprecated alias, or the setting silently reverts
      to its default on the next load and the fetch path changes behaviour without
      anyone asking for it. Read the old key when the new one is absent, and log
      once at WARN when the alias is used.

- [ ] **Related asymmetry found while tracing the above: auto-fetch embeds cover
      art into audio files even when tag write-back is off.**
      `mfs.embedCoverInBookFiles(updatedBook, coverPath)` sits *outside* the
      `if config.AppConfig.WriteBackMetadata` block in `service_fetch.go` (~:301 vs
      :309). So with `write_back_metadata: false`, auto-fetch still modifies files
      on disk — artwork only, no text tags. That may well be intended, but it is
      not what either flag's name suggests, and it means "off" does not mean "does
      not touch my files." Decide whether cover embedding belongs under the same
      gate, and say so in a comment either way.

- [x] **BENCH-BUILD** `go build -tags bench ./cmd/...` fails on `main`
      (confirmed pre-existing, unrelated to the env-var consolidation PR that
      surfaced it): `cmd/dedup_bench_batch.go`, `cmd/dedup_bench_runner.go`,
      `cmd/dedup_bench_types.go` reference `server.AuthorDedupGroup`, and
      `cmd/dedup_bench.go` references `server.FindDuplicateAuthors` — neither
      symbol exists in `internal/server` anymore. The dedup-bench CLI tooling
      is `//go:build bench`-gated so `make ci`/plain `go build ./...` never
      catches this; someone removed/renamed the symbols in `internal/server`
      without updating the bench tools.

      **Fixed in #2643.** `b6fe7c5a` (2026-04-18) moved both symbols to
      `internal/dedup`; the four call sites are repointed. Broken for four
      months. Recurrence closed by a new `make bench-check` target, wired into
      `make ci` and into the `SDK Deps & Bench Build` CI job.

- [x] **SDKGUARD-LEAK** `make sdkguard` fails on `main` (confirmed
      pre-existing): `internal/cache`, `internal/audioutil`, and
      `internal/syncapi/progress` have leaked into `pkg/plugin/sdk`'s
      dependency tree, which `tools/cmd/sdkguard` treats as forbidden. Either
      remove the imports pulling these in, or add them to `allowedInternals`
      in `tools/cmd/sdkguard/main.go` with a comment explaining why each is
      safe to expose to SDK consumers.

      **Fixed in #2643**, but neither suggested remedy was the right one: all
      three are *transitive* deps of packages the guard already approved, and
      five of its nine entries were already undocumented accretions of the same
      kind, so appending three more would have re-broken on the next one. The
      guard is now two tiers — declared roots for direct imports, plus a
      bidirectional snapshot ratchet (`internal-deps.txt`) for the transitive
      closure. Red for 33 days on a green `main`, because `sdkguard` ran in no
      workflow at all.

- [ ] **CFG-AUDIT** Triage the findings in
      `docs/audits/2026-08-20-config-option-audit.md` (full config-option
      inventory + grep-verified usage/naming/default audit, 565 options). At
      minimum decide on: (1) `EnableRateLimit=false` not actually disabling
      rate limiting — only `APIRateLimitPerMinute > 0` gates it; (2)
      `AuthRateLimitPerMinute` is fully wired but never enforced anywhere; (3)
      `APIRateLimitPerMinute` default drift between the fresh-install viper
      default (0/unlimited) and `ResetToDefaults()`/`.env.example` (100); (4)
      `ai_backend.local_base_url` defaulting to a hardcoded developer LAN IP,
      which silently routes fresh installs into local-LLM mode; (5)
      `Config.ChapterConsolidationThresholdMin` being omitted from
      `ResetToDefaults()`, so a factory reset silently disables chapter
      consolidation instead of restoring the intended default of 10 — ✅ DONE 2026-08-22 (PR #2729, TASK-019); (6)
      whether to delete the fully inert `--enable-sqlite3-i-know-the-risks`
      flag now that the SQLite backend is gone; (7) whether to wire up or
      remove the two entirely-unenforced Settings-UI subsystems (Storage
      Quotas, Memory Limits) and the ~10 other dead Settings-page toggles
      (`create_backups`, `verify_after_write`, `AutoFetchMetadata`,
      `EmbedCoverArt`, etc.) listed in the report so users stop being able to
      flip a switch that does nothing.

- [x] **SCORE-REC** *(DONE 2026-08-23 — TASK-079, PR #2806. Both "Done means"
      clauses below were verified, not assumed: (1) `ScoreStep{` composite
      literals outside `score_breakdown.go` = **0** at `b9ccfc4a8` (the two
      remaining hits are test fixtures constructing expected values, which is
      not what this criterion targets); (2) the factors ARE pinned by mutation,
      not just green — halving `service_scoring.go`'s `score *= 1.5` to `0.75`
      failed `TestPickBestMatchFromScored/author_match_boosts`, then restored
      to a clean tree. Note the fix shipped differs from what this entry
      implies: routing through the recorder while returning a hardcoded `0`
      alongside `*rec.breakdown()` lets the two return values diverge silently
      — proven by mutation — so both now derive from one `bd := rec.breakdown()`.)*
      Route `ScoreOneResultWithBreakdown` through `scoreRecorder`
      like `ApplyNonBaseAdjustmentsWithBreakdown` now is. It still hand-builds
      its own `ScoreOpBase` step at `internal/metafetch/service_scoring.go:724`,
      duplicating what `newScoreRecorder` does. This is the last hand-rolled
      `ScoreStep` site left after #2639, which converted the sibling function
      because `scoreRecorder.add` had been flagged unused — the linter can only
      see the unused helper, never the copied logic that should be calling it,
      so nothing will flag this one. Done means: no `ScoreStep` composite
      literals outside `score_breakdown.go`, and the golden fixtures in
      `service_scoring_test.go` still pin the same totals (verify by mutation,
      not by a green run — halving a factor must fail them).

- [ ] 🏷️ **"Browse by Tag" surfaces internal bookkeeping tags and formats them
      badly.** Owner report 2026-08-10 with a screenshot of the Library page on
      mobile (`books.jdfalk.com`, "Browse by Tag (149)"). The widget is *almost*
      right; every problem below is presentation, not tagging.

      Observed, top five chips in order:

      | Chip as rendered | Count | Verdict |
      |---|---:|---|
      | `dedup:duration-match` | 24,883 | **Strip** — internal dedup bookkeeping |
      | `metadata:language:en` | 15,036 | Keep, but **reformat** (see below) |
      | `metadata:source:audible` | 14,895 | **Strip** — provenance, not a browse axis |
      | `dedup:duration-abridged` | 3,573 | **Strip**, and the count is **suspect** |
      | `science fiction & fantasy` | 1,109 | ✅ this is what the widget is for |

      **What the owner asked for:**

      1. **Strip `dedup:duration-match` entirely.** Nobody browses their library
         by "the deduper thought these two durations matched."
      2. **Strip `metadata:source:audible`** and its siblings (`google-books`,
         etc.). *"For those weird ones like audible metadata source or google
         books or whatever don't put those at the top ever if we can. If we just
         have to hide them that's fine too."* — so a hide/allow-list is an
         acceptable implementation; they do not have to be deleted from the data.
      3. **Strip the `metadata:` prefix and put a space between key and value.**
         `metadata:language:en` should read `language: en`, not
         `metadata:language:en`. Owner: *"for gosh sakes."*
      4. **`dedup:duration-abridged` (3,573) — verify the number before trusting
         it.** Owner: *"not sure on the abridged. That's a weird one. I don't
         think it's as high as you think it is."* Treat this as a **separate
         data question** from the display cleanup: 3,573 abridged editions out of
         the library is a claim the tagger is making, and the owner's intuition
         is that it is over-firing. Do not "fix" it by hiding the tag — measure
         whether the abridged detection is correct first, then decide. Hiding a
         wrong number makes it unfalsifiable.
      5. **Confirm tags are per-BOOK, not per-file.** Owner: *"Also tags are per
         book right?"* This needs a definitive answer from the schema, not an
         assumption — if any tag is stored per `book_file`, a multi-file
         audiobook would inflate every count in this widget by its file count,
         which would independently explain why several of these numbers look too
         high. **Check this before touching the display**, because it changes
         whether the counts above are even meaningful.

      **Suggested shape** (not locked): an ordering/visibility policy for tag
      namespaces rather than per-tag special cases — genuine subject tags
      (`science fiction & fantasy`) rank first, `metadata:*` renders
      prefix-stripped and `key: value` formatted below them, and `dedup:*` plus
      other machine-internal namespaces are hidden from Browse by Tag entirely
      while remaining searchable/filterable for anyone who wants them.

      Screenshot in the 2026-08-10 session; widget renders on the Library page
      under the search box, above `Select All`.

- [ ] **E2EGATE-NOTREQUIRED** The E2E suite runs on every qualifying PR and its
      result is enforced by nothing. A red run merges exactly as easily as a
      green one. **Owner decision required — do not enable unattended.**

      This is the April-2026 spec-rot incident one layer up. That incident was
      "the suite existed and nothing ran it." The state now is "the suite runs
      and nothing acts on the answer," which is quieter and looks healthier.

      **Measured 2026-08-10 ~09:5x EDT**, on `main` at `dc724b80`:

          gh api repos/falkcorp/audiobook-organizer/rules/branches/main

      That endpoint is the authoritative one — it returns every rule applying to
      the branch **including org-inherited rulesets**, which a repo-scoped
      `/rulesets` query misses. It returned exactly four rules, all from the
      `falkcorp` **Organization** ruleset:

          required_linear_history
          deletion
          repository_delete
          repository_transfer

      There is **no `required_status_checks` rule and no `pull_request` rule**,
      and classic protection (`/branches/main/protection`) returns 404 "Branch
      not protected". Consequences, all of which follow directly:

      - A PR whose E2E run fails can be merged with the normal green button.
      - `gh pr merge --admin` bypasses nothing on this repo, because nothing is
        required. Tonight's four merges (#2277–#2280) were gated only by the
        agent session's own bash poll loop, not by GitHub.
      - Nothing requires a pull request at all; a direct push to `main` is
        blocked only by `required_linear_history`.

      **The fix is not simply "turn on required status checks."** `e2e.yml`
      carries `paths: ['web/**', '**.go', 'go.mod', 'go.sum',
      '.github/workflows/e2e.yml']`. A required check that is filtered out of a
      given PR can leave that PR pinned at *"Expected — Waiting for status"*
      forever, which would strand every docs-only PR — the exact shape of
      #2279 and #2280. GitHub's behaviour here depends on how the check is
      registered, and **this has not been measured on this repo.** Resolve that
      before enabling, e.g. with an always-runs `E2E Summary` job that reports
      success when the real job is skipped by path.

      **What was checked and found sound**, so it is not the problem: the
      `paths` filter does cover the whole suite. Every spec, plus
      `playwright.config.ts`, `global-setup.ts` and `utils/test-helpers.ts`,
      lives under `web/tests/e2e/`, which `web/**` matches. A fixture or config
      change like the one that caused the original four-month rot *would*
      trigger the job today.

      **NOT claimed:** that enabling enforcement is safe as-is; that the
      `paths`/required-check interaction behaves any particular way here; or
      that any PR has actually merged red. No merged-red PR was searched for.

      Fixed in the same PR as this fragment: `e2e.yml`'s header comment
      asserted the job "BLOCKS on every trigger", which was never true. A
      comment claiming a gate exists is worse than no comment, because it stops
      anyone from checking.

- [ ] 🔴 **The binary ITL parser extracts ZERO smart playlists from real iTunes
      libraries, while the XML export of the same library has 292.** Measured
      2026-08-10 against the owner's live library. This is the blocker standing
      between `maintenance.itunes-playlist-import` and the owner's request
      *"I want all my dynamic playlists from iTunes imported."*

      | Source | Playlists | Smart |
      |---|---:|---:|
      | `/mnt/bigdata/books/itunes/iTunes Library.itl` (live, Jul 19, 32 MB) | 357 | **0** |
      | `.../bkup/itunesgood/iTunes Library.itl` (Apr 2, 28 MB) | 335 | **0** |
      | `/mnt/bigdata/books/itunes/iTunes Library.xml` (same library) | 351 | **292** |

      **Not writeback data loss.** Both the live ITL and an April backup return
      zero, so this is not our ITL writer stripping records — it is extraction
      that never fires on real files. Both parse cleanly otherwise (97,999 and
      90,900 tracks, correct titles, `ver=12.13.10.3`).

      **Not an unimplemented stub either** — that was the first hypothesis and it
      was wrong. `itl_be.go:341-354` and `itl_le.go:429-441` do populate
      `IsSmart`, `SmartCriteria` (hohm `0x65`) and `SmartInfo` (hohm `0x66`).
      The code exists and presumably works on the synthetic fixtures the unit
      tests build by hand — `playlist_sync_test.go` constructs `ITLPlaylist`
      values with `IsSmart: true` directly, so **no test has ever exercised the
      parser that fills those fields.** A third instance of the session's theme:
      the tests pass because they bypass the thing that is broken.

      The XML proves the data exists and is recoverable: 292 `Smart Criteria`
      blobs, with names that are clearly the owner's real playlists (series and
      author names — "A Mage's Cultivation", "Aether's Revival", "Aaron Oster",
      "All the Skills").

      **Two candidate directions — needs a decision:**
      1. **Fix ITL extraction.** Find why the `0x65`/`0x66` branch does not fire
         on 12.13.10.3 files (offset/section assumption, playlist record layout,
         BE-vs-LE path). Highest value: the ITL is the write/authority surface,
         so push-back will need this too. Start by dumping the hohm types
         actually encountered while parsing one real playlist record — the
         parser reaches these playlists (titles are correct), so the records are
         either absent from the stream or skipped.
      2. **Import from the XML export instead.** The criteria are present and
         base64-encoded; `ParseSmartCriteria`/`TranslateSmartCriteria` already
         consume that blob shape. Much cheaper, unblocks the owner immediately,
         and read-only against a file we already parse elsewhere. Downside: the
         XML is an export, not the authority, so it can lag the ITL.

      Recommend **2 to unblock, then 1** — but confirm the XML's base64 blob is
      byte-identical in shape to what `ParseSmartCriteria` expects before
      assuming it drops in.

      `maintenance.itunes-playlist-import` already guards this case explicitly:
      when it parses playlists but extracts zero smart ones it logs a warning
      naming the XML discrepancy and the `grep -c 'Smart Criteria'` check,
      rather than reporting a clean "0 imported" that reads like an empty
      library.

- [ ] **ITUNES-SMARTCRIT-PARSE** `ParseSmartCriteria` does not understand the
      real Smart Criteria format. It reports success on every blob and returns
      **garbage**. Measured 2026-08-10 against all 292 smart playlists in the
      owner's live `iTunes Library.xml`.

      🚨 **This is a "reporting success while meaning nothing" defect**, and it
      is worse than a parse failure would be: `ParseSmartCriteria` is documented
      as "tolerant of unknown fields/operators — those are recorded as raw hex
      rather than causing parse failure," so a totally wrong layout yields
      `err == nil` and a full-looking rule list. **292/292 blobs parsed with
      zero errors and 1,751 rules — and essentially every rule is empty:**

          PLAYLIST "Audiobooks" conj=OR rules=2
             field=unknown_0x2000000 op=op_0x00 operands=[]
             field=unknown_0x00      op=op_0x00 operands=[]

      One operand decoded as `72057594037927936` — that is `0x0100000000000000`,
      a byte-order artifact, confirming the endianness is also wrong.

      **Is the criteria plain text in the XML?** No. `Smart Criteria` is a
      base64-wrapped **binary** blob — byte-for-byte the same `SLst` structure as
      in the `.itl`. The XML's advantage is only that the blob is *present*
      (292/292) where ITL extraction yields 0. **But the string operands inside
      the blob are plain UTF-16BE and are recoverable without a full parser.**

      **What the format actually is** (derived from the 292 real blobs, and the
      two claims marked ✅ are the only ones validated against ground truth):

      - ✅ Magic `SLst` (`0x534c7374`) at offset 0 — the parser never checks it.
      - ✅ **Big-endian**, not little-endian.
      - ⚠️ It is **NOT** a flat `header + rules[]` array. The blob is a **nested
        tree of `SLst` containers** — an 850-byte single-rule blob contains
        *three* `SLst` magics (offsets 0x000, 0x0C0, 0x1FC). An earlier revision
        of this entry claimed a flat 136-byte header with variable-length rules;
        parsing all 292 blobs that way **overruns on 281/292** and that claim was
        wrong. The container nesting is still unmapped.

      **A parser is not required to extract the rules.** Operands can be located
      structurally and the surrounding rule header read at fixed negative
      offsets. Over all 292 blobs this yields **358 operands whose declared
      length matches**, which is what proves the alignment:

          find a UTF-16BE run at offset `off`
          require  u32be(off-4)  == len(run)*2     # length prefix agrees
          field  =  u32be(off-56)
          operator= u32be(off-52)

      **Field codes** (over alignment-proven operands): `3`=Album ×204,
      `4`=Artist ×126, `8`=Genre ×23, plus `71` ×2, `2` ×2, `14` ×1.

      **Operator words — validated against actual playlist membership**, by
      testing whether the materialized members really satisfy the rule:

      | word | meaning | evidence |
      |---|---|---|
      | `0x1000002` | contains | satisfied **4017/4017 = 100%** |
      | `0x3000002` | does **NOT** contain | satisfied **0/23700 = 0.0%** |
      | `0x1000001` | **UNRESOLVED** (22 uses) | 18.2% — fits neither |

      🚨 `0x3000002` is a **perfect inversion**. Treating every `…0002` word as
      "contains" would ship 79 playlists matching exactly the books they were
      written to exclude — silently, and looking like a successful import. Do not
      map an operator word without running this membership check on it.

      **Conjunction:** with negation applied, **38/46** multi-rule playlists have
      every rule holding for >95% of members, so rules are predominantly ANDed;
      the other 8 (e.g. `Recent Litrpg`, 10 rules) are ORs. The AND/OR flag has
      **not** been located — candidate is the u32 at offset 8 of each `SLst`
      container (`0x02` outer vs `0x01` inner in the dump), untested.

      **Why no test caught the original defect:** `playlist_sync_test.go` builds
      `ITLPlaylist` values by hand with `IsSmart: true` and never exercises a
      real blob, and the tolerant-by-design error handling means a round-trip
      over real data still returns `nil`. Any fix must assert on **rule
      content** — a non-empty `Rules` slice is not evidence, since the broken
      parser produces 1,751 of them. The membership check above is the ground
      truth to assert against, and it needs no prod access.

      **Two independent recovery sources, covering 290/292 (99.3%):**

      1. **224 of 292 carry materialized `Playlist Items`** (116,822 track refs)
         — actual current membership, needing **no criteria parsing at all**.
         Imports as a static snapshot.
      2. **66 more have criteria strings but zero materialized items.** For these
         the operands are the only source.
      3. **2 have neither** and are unrecoverable from the XML.

      **The split is convenient:** the 68 zero-membership playlists are almost
      exactly the *series* ones (`Ascend Online`, `Aurora Cycle`,
      `Anime Trope System`, `A Snake's Life`) — the ones the owner expects to
      become obsolete once series support lands. So the criteria parser is the
      **low-value half**: the 224 that can be snapshotted need none of it.

      **Track resolution is by persistent ID, not path.** `Playlist Items` give
      Track IDs, which resolve 100% within the XML to a `Tracks` entry carrying a
      `Persistent ID`; `BookFile.ITunesPersistentID` is indexed with
      `GetBookByITunesPersistentID`. This sidesteps the `itunes_path`
      normalization bug entirely — the XML `Location` values are Windows drive
      paths (`file://localhost/W:/itunes/iTunes Media/...`) and should not be
      used for matching.

      ⚠️ **NOT MEASURED, and it gates option 1:** what fraction of the XML's
      track persistent IDs actually exist in our DB. Measure that before
      promising 224 importable playlists — if PID coverage is poor, both recovery
      paths are moot.

      Do not wire criteria-based import until the operator mapping is asserted
      against real membership — right now it would silently import 292 playlists
      whose rules are all empty.

- [ ] **MERGE-CACHE-EVICT** A merge must evict (or dirty-flag) every merged-away
      book/file ID from **every** read cache, so the losers stop appearing after
      the merge succeeds. **Owner-reported 2026-08-10:** "you think you merged
      something then you see 2 copies still." Applies to both merge shapes —
      several files into one book, and two books into a version group.

      This is a trust bug, not a cosmetic one: a merge that visibly does nothing
      teaches the owner not to believe the merge button. Mechanism does not
      matter (evict, dirty-flag, write-through) — the invariant does: **after
      `MergeBooks` returns success, no read path may still serve a loser ID.**

      **Established by grep at `76269d57` (measured, not inferred):**

      - Merge entry points: `internal/merge/service.go:125`
        (`(*Service).MergeBooks`) and `internal/dedup/book_dedup.go:395`
        (`MergeBooks`). HTTP entry: `internal/server/handlers/duplicates/handler.go:292`.
      - Losers are **soft-deleted**, not removed: `merge.SoftDeleteBook`
        (`internal/merge/service.go:544`) sets `MarkedForDeletion` /
        `MarkedForDeletionAt` and calls `store.UpdateBook`, falling back to
        `DeleteBook` only if that write fails.
      - `UpdateBook` does write through to the in-memory copy — the
        `UpsertBookToMemDB` / `DeleteBookFromMemDB` API exists at
        `internal/database/memdb_sync.go:123` and `:182` — and calls
        `InvalidateLibraryStats`. So **memdb is the layer least likely to be at
        fault**; do not start there.
      - 🚨 **`internal/merge/` and `internal/dedup/` contain ZERO references to
        the search index** — no `IndexBook`, no delete-from-index, no dirty-set
        enqueue. The Bleve index lives in `internal/search/` (`bleve_index.go`,
        `index_builder.go`). A merge therefore never tells the index its losers
        are gone.

      **NOT established — verify before fixing, do not assume:**

      1. Whether `UpdateBook` itself enqueues into the search dirty set added by
         **#2268**. If it does, the index may self-heal on the next reconcile
         pass and the visible-duplicate window is a *latency* problem, not a
         *correctness* one — a different fix (force a reconcile on merge) than
         an explicit evict.
      2. Whether a soft-deleted book is **deleted from** the Bleve index or
         merely re-indexed carrying `MarkedForDeletion`, and whether the query
         path filters on that flag. If it is filtered only *after* pagination,
         this is the same post-filter-after-pagination defect already recorded
         for search — losers would consume page slots even once filtered.
      3. The **file-level** merge path (several files into one book) was not
         located in this pass. Find it and check it independently; do not assume
         it shares `merge.SoftDeleteBook`.
      4. Whether the version-group read path de-duplicates. Related:
         `GetBooksByVersionGroup`'s pointer index (fixed in **#2288**) — a merge
         writes `VersionGroupID`, so a stale index row is another way a loser
         could keep surfacing.

      **Acceptance criteria (the regression test to write):** merge N books into
      a version group, then **immediately** — no sleep, no refresh — re-query
      (a) the library list, (b) search, (c) the version-group endpoint, and
      assert every loser ID appears in **none** of them. A test that passes only
      after a sleep is measuring the reconciler, not the fix.

      Related: the cached-aggregates dirty-flag pattern already used elsewhere in
      this codebase, and `InvalidateLibraryStats` as the existing precedent for
      "a write invalidates a derived read."

- [x] 🔴 **The Drawer's close transition can stall forever, leaving an invisible
      full-viewport backdrop that swallows every click until reload.** FIXED
      2026-08-11 in `web/src/theme.ts` — `MuiDrawer.defaultProps.slotProps
      .transition = { exit: false }`. No longer blocks TODO-MUI-1 (5.14 → 6.5).

      🚨 **This entry's original verdict — "real v6 regression" — was WRONG, and
      so was the 2026-08-10 fix it was chasing.** The defect is present on
      `main` (MUI 5.18.0) too. v6 did not introduce it; v6 moved its timing
      window onto the common path, which is why v6 failed 2/2 and v5 passed 2/2
      in the original n=2 comparison. Two runs per side cannot tell a regression
      apart from a shifted race.

      Measured 2026-08-11, chromium, `workers=1`, `--repeat-each=10`, n=10 per
      cell. P1 = press Escape with nothing crossing the CDP boundary first;
      P2 = identical but with one `page.evaluate()` immediately before the
      Escape. (The pre-existing probe called `page.evaluate` before *every*
      Escape, so its "closes 6/6" result had measured the instrumented world.)

      | build | P1 pass | P2 pass |
      |---|---|---|
      | v6.5.0, `exit: 0` only (as filed) | **0/10** | 10/10 |
      | v5.18.0, `exit: 0` only (i.e. `main` today) | 9/10 | **1/10** |
      | v6.5.0 + `exit: false` | 10/10 | 10/10 |

      MECHANISM (in-page instrumentation + a patched react-transition-group):
      Escape is delivered correctly and `onClose(_, 'escapeKeyDown')` fires —
      focus, `isTopModal()` and the Select menu are all fine, so the four
      theories in the original report are disproven. The Slide and the
      Backdrop's Fade both enter `exiting`, RTG schedules completion with
      `setTimeout`, **the timer fires**, and RTG calls `setState({status:
      'exited'})` on an instance whose `updater.isMounted` is `true`. React
      never applies that update: the same instance re-renders 4 ms later still
      `exiting`, `componentDidUpdate` agrees at +9 ms, and a 300 ms probe still
      reads `exiting`. So `onExited` never runs, `useModal`'s `exited` stays
      false, `Modal` never returns null, and its `position: fixed; inset: 0`
      backdrop stays hit-testable — `document.elementFromPoint(20, 300)`
      returns it. `exit: false` makes RTG take the synchronous branch in
      `performExit` instead, so the lost update is never scheduled.

- [ ] 🟡 **Why does React silently drop a `setState` issued from a `setTimeout`
      on a mounted class component inside a portal?** This is the residual
      unknown under the Drawer fix above and under the 2026-08-10 "invisible
      sheet" incident, and it is now the *only* part still unexplained. Ruled
      out: duplicate React copies (single `react@18.3.1`, deduped), StrictMode
      (dev-only in `web/src/main.tsx`), `flushSync`/`startTransition`/
      `unstable_batchedUpdates` (absent from `web/src`), uncaught exceptions
      (none observed), and unmounting (`componentWillUnmount` never runs,
      `isMounted` stays true). Leading untested hypothesis: this is a
      production-build React 18 concurrent-root update issued while the root is
      mid-render, where the dev-only warning that would have named it does not
      exist. `exit: false` sidesteps the question rather than answering it, so
      any other component that keeps a timer-driven exit transition is still
      exposed — `MuiMenu` currently is (its `exit: 0` measured 20/20 on
      2026-08-10, but that is the same kind of evidence that `exit: 0` on the
      Drawer produced before it failed 10/10 on v6).

- [ ] 🟡 **`batch-operations.spec.ts:100` [webkit] is an intermittent flake —
      find its mechanism.** Observed 2026-08-10 failing once on `main`
      (`76269d57`) and once on the MUI v6 branch, and **passing** on a re-run of
      each. `main` is green: 556 passed / 0 failed / 8 skipped, exit 0.

          Error: expect(locator).toBeChecked() failed
          Locator: getByLabel('Select Test Book 1', { exact: true })
          Timeout: 5000ms — element(s) not found

      "Batch Operations › selection persists across page navigation". When it
      does fail, the checkbox is not merely unchecked — its **label is absent**,
      so the row is not rendering as the test expects at all. Webkit only. That
      shape (row not rendered yet, rather than state lost) points at the
      navigation completing before the list re-renders, which is a timing
      mechanism worth finding rather than re-running past.

      🚨 **This entry previously claimed `main` was red.** That claim came from a
      single run and was wrong; it was published in a PR body and a memory file
      before being re-run. A failure seen once on a suite with known webkit
      flake is not a measurement — re-run before recording it. Per
      `feedback_fix_flaky_tests`, this still gets its mechanism found rather
      than being ignored as noise.

- [ ] 🎧 **iTunes dynamic-playlist import and playlist push-back are fully
      implemented and NEVER CALLED.** Owner request 2026-08-10: *"I want all my
      dynamic playlists from iTunes imported"* and *"I'd like it if we could sync
      our dynamic playlists."* Measured the same day — this is a **wiring gap,
      not a build gap.**

      `internal/itunes/service/playlist_sync.go` (v2.1.0) implements both halves
      of spec 3.4:

      - `MigrateSmartPlaylists(lib *itunes.ITLLibrary) (imported, skipped int)`
        — reads smart playlists from the ITL, parses the Smart Criteria blob,
        translates it to our DSL, creates `UserPlaylist` rows with `type=smart`,
        and stores the raw blob in `ITunesRawCriteriaB64` for audit. Idempotent,
        skips playlists already imported by iTunes PID.
      - `PushDirty() int` — creates an ITL playlist for dirty playlists with no
        PID, updates the track list for those that have one.

      **Both have ZERO non-test callers.** Verified by enumerating every method
      on `*PlaylistSync` and grepping for call sites across `internal/` and
      `cmd/` excluding `_test.go`:

          MigrateSmartPlaylists -> 0 non-test callers
          PushDirty             -> 0 non-test callers

      The service *constructs* `PlaylistSync` (`itunes/service/service.go:124`,
      "M1 step 4") and the store side is complete — `ListDirtyUserPlaylists()`
      exists, `idx:upl:dirty:` is maintained, `idx:upl:itunes:<pid>` maps PIDs
      back to playlists. Everything is in place except an invocation. So the
      owner's iTunes smart playlists have never been imported, and no playlist
      has ever been pushed back, while the code to do both sits tested and idle.

      This is the same failure shape as the rest of the 2026-08-10 backlog: a
      mechanism that reports nothing because it never runs. It will not show up
      as an error, a warning, or a failed op — there is simply no op.

      **Work:**
      1. Decide the trigger for `MigrateSmartPlaylists` — a one-shot maintenance
         op (consistent with the rest of the codebase), a step in the existing
         iTunes import flow, or an explicit endpoint. It needs an `*ITLLibrary`,
         so it has to hang off whatever already parses the ITL.
      2. Decide the trigger for `PushDirty` — this one WRITES to the iTunes
         library, so it must respect the standing rule that the active iTunes
         tree is hands-off, and it must be dry-run-gated on first run like every
         other apply path here. Import (read-only) and push (write) should ship
         as **separate** units; the owner asked for import first.
      3. Report exact counts on the first import run (imported / skipped), and
         verify by re-reading the DB rather than trusting the return values.

- [x] ✅ **Confirm playlists are book-level, not file-level — and delete the dead
      file-level path.** Owner requirement 2026-08-10: *"we need to be sure
      playlists operate at the book level not the file level."* — closed 2026-08-21:
      `generatePlaylistFile`/`PlaylistItem` deleted (`git log --oneline -- internal/playlist/playlist.go` shows `a1923369 chore(playlist): delete dead file-level M3U playlist path`; no non-test references remain).

      **Checked 2026-08-10 — the live model is already book-level.**
      `database.UserPlaylist` (`internal/database/store.go:391`) stores
      `BookIDs []string` (book ULIDs) for static playlists and
      `MaterializedBookIDs []string` for evaluated smart ones. `Type` is
      `"static"` or `"smart"`. `MaterializeSmartPlaylist` evaluates to book IDs.
      No `book_file` reference anywhere in the type. ✅

      **But a legacy file-level path still exists** in `internal/playlist/playlist.go`:

          type PlaylistItem struct {
              BookID   int      // ← book IDs are ULID strings, not ints
              FilePath string   // ← ONE path per book; audiobooks are multi-file
              ...
          }

      `generatePlaylistFile` writes an M3U with a single `FilePath` line per
      item, which is wrong for any multi-file audiobook, and `BookID int` is a
      leftover from the removed SQLite schema. Its only sibling,
      `GeneratePlaylistsForSeries`, was already gutted in fable5 TASK-022 and now
      just returns an error telling callers to use the Store-backed API.

      `generatePlaylistFile` has **no non-test callers** — its only references
      are in `internal/playlist/playlist_test.go`. So the file-level model is
      dead code that four tests keep alive, and it is exactly the sort of thing
      that gets copied back into service later because it looks like the
      playlist implementation. **Delete it and its tests**, or, if M3U export is
      wanted as a real feature, rewrite it against `UserPlaylist` with all of a
      book's files expanded in order.

      No production behaviour should change either way — but do not close this
      by inspection alone; grep for `PlaylistItem` at the time of the fix in case
      something has picked it up since.

- [x] ~~**A `todo.d` fragment assembled between the PR that files it and the PR
      that finishes it leaves an open task in `TODO.md` for completed work.**
      Hit for real on 2026-08-10; found only because `TODO.md` happened to be
      re-read after the merge. Nothing reported it.~~ — closed 2026-08-22
      (PR #2714, TASK-055): documented as a process rule in `todo.d/README.md`
      ("Finishing work that had a fragment") and `CLAUDE.md`'s Post-Task Hygiene,
      deliberately **not** as a mechanical guard — the race is a human-timing
      problem a linter cannot see. Hit again in this very wave: the ConcurrencyKey
      fragment filed a task that shipped as #2709 the same day.

      **Exact timeline** (`git log` on `main`):

          04:25 EDT  a75b9ad2  PR #2272 adds
                               todo.d/20260810-library-exhaustive-deps-…md
          04:51 EDT  6658d1a8  assemble_todo.py folds it into TODO.md
                               and `git rm`s the fragment  [skip ci]
          05:12 EDT  a655753e  PR #2273 does the work and deletes the
                               same fragment

      Result: the fragment is gone, the work is done, and `TODO.md` carries an
      unchecked `- [ ]` entry describing it — including instructions that PR
      #2273 had just proven wrong. Cleaned up by hand in #2274.

      **Why it is easy to miss.** `scripts/assemble_todo.py` *consumes*
      fragments: `git_rm(fragments)` at `main()`'s end deletes each one as it
      folds it in. So by the time the finishing PR merges, the fragment is
      already gone from `main`, and that PR's own deletion of it is a silent
      no-op. The absence of the fragment therefore proves nothing either way —
      it looks identical whether the task was assembled or never existed.

      The window is not narrow: 26 minutes here.

      **A mechanical check is harder than it first looks.** The obvious one —
      "if a PR deletes a `todo.d` fragment, require the matching `TODO.md`
      entry to be checked off" — will not fire reliably, because after a rebase
      the deletion may not be in the PR's diff at all (assemble already removed
      the file upstream). And "flag any `- [ ]` entry whose
      `<!-- file: todo.d/… -->` marker points at a missing fragment" matches
      *every* assembled entry, since assemble always deletes. Both directions
      are dead ends without knowing whether the work happened, which is not
      derivable from the files.

      **So the practical fix is a rule, not a script:** when a PR completes work
      that had a `todo.d` fragment, `grep TODO.md` for it before merging and
      check the entry off there if assemble got to it first. Worth adding to
      `todo.d/README.md` and to the post-task hygiene list in `CLAUDE.md`,
      beside the existing CHANGELOG/TODO/executive-summary triple.

      **If a mechanical guard is wanted anyway,** the least-bad version is
      probably a check on the *PR body*: PRs that say "closes the todo.d
      fragment …" or delete a fragment must also touch `TODO.md`. That is a
      heuristic, and it should be written as one rather than as a guarantee.

- [x] ~~**VGBACKFILL-SCAN-BOUNDS** The version-group backfill scans only ~13% of
      the library.~~ **RETRACTED 2026-08-11 — there was no under-scan.** Kept
      rather than deleted so nobody re-derives the same wrong conclusion from
      the same logs.

      **What was claimed:** `scanned=48874` against `books=366922` from the same
      boot = 13.3%, therefore the backfill's Pebble iterator bounds
      (`book:0` .. `book:;`, admitting only digit-leading IDs) were excluding
      ~318k rows.

      **What is actually true:** `books=366922` was never a book count. The
      memdb warmup's `warmIter` returned the number of Pebble KEYS it visited
      under the `book:` prefix, and that prefix is shared with roughly seven
      secondary-index families — `book:path:`, `book:hash:`,
      `book:originalhash:`, `book:organizedhash:`, `book:versiongroup:`,
      `book:work:`, `book:asin:`/`book:isbn13:`. The row callback skips those
      via `strings.Count(key, ":") != 1`, but the skipped keys were still
      counted and then published under the label `books`. About 7.5 keys per
      book row on production.

      The real library is **~46k–55k books**, corroborated three ways in the
      same logs: `total_books=46221` and `total_books=54734` from system
      status, and the organizer's own `Fetched 48896 total books from
      database`. So `scanned=48874` was a **complete** scan, and the digit-only
      iterator bounds — while genuinely fragile — were not excluding anything,
      because production book IDs are ULIDs and a ULID minted this century
      starts with `0`.

      **How the error was made, because the shape recurs:** the original entry
      explicitly listed this explanation ("the two numbers could also disagree
      because `books=366922` counts something the `book:<id>` keyspace does
      not") and marked the whole thing NOT YET CONFIRMED. It was then upgraded
      to CONFIRMED on 2026-08-11 when a *second* subsystem — the organizer's
      `GetAllBooksCore` paging loop — reported 48,896 against the same
      `books=366922`. Two independent readings agreeing looked like
      corroboration. They were not independent: both were compared against the
      **same unverified denominator**. Agreement between two numerators says
      nothing about the denominator they share.

      **Fixed in the same PR as this retraction:** warmup now reports rows
      inserted into memdb, and reports keys scanned separately under its own
      name so the two can never be confused again. Pinned by
      `TestWarmupCounts_CountRowsNotPebbleKeys`, which fails with
      `expected: 4, actual: 20` against the old counting.

- [ ] **VGBACKFILL-BOUNDS-FRAGILE** Separately, and still worth doing: the
      version-group backfill's iterator bounds `book:0` .. `book:;` admit only
      IDs whose first byte is `0x30`–`0x3A`. That is correct today only because
      every production book ID happens to be a ULID starting with `0`. It is
      not enforced anywhere — `CreateBook` mints a ULID only `if book.ID == ""`,
      so a caller supplying a letter-leading ID would become permanently
      invisible to this scan with no error. Replace the bounds with a prefix
      scan over `book:` → `book;` and let the existing one-colon structural
      filter reject the secondary indexes, which is what it was written for.
      This is a latent-correctness fix, **not** the cause of any observed
      under-scan.

- [ ] **`wipeActivity` dry-run count saturates at 2.** `wipeActivity` in
  `internal/server/maintenance_fixups.go` reports its dry-run row count from
  `svc.Query(ctx, ActivityFilter{Limit: 1})`'s `total`. Since the bounded-scan
  change in `0adf6e97`, `total` is a LOWER BOUND: the walk stops once it has
  collected `Offset+Limit+1 == 2` matches, so the dry-run preview now reports
  "2" no matter how many activity rows actually exist. The wipe itself is
  unaffected (it calls `WipeAllActivity`), so this is a misleading preview
  rather than data loss — but the preview is exactly what an operator uses to
  decide whether to run the wipe. Fix needs either a dedicated count path or a
  `CountByPrefix`-style call rather than reusing the paged query. Noted inline
  at the call site during the activity-cancellation work (branch
  `fix/activityquery`).

- [ ] **`WipeAllActivity` still does an uncancellable full scan on a request
  path.** It calls `scanTierKVs(context.Background(), ...)` per tier, and is
  reachable from `handleWipe`. The activity-cancellation work deliberately left
  the maintenance methods (`Prune`, `WipeAllActivity`, `Summarize`,
  `CompactByDay`) context-free per scope, so `Query`/`GetDistinctSources` are
  cancellable but this path is not: an abandoned wipe request still scans every
  tier to completion. Lower severity than the query path (a wipe is rare and
  operator-initiated, not fired on every page load) but it is the same shape of
  defect and the same fix.

- [ ] **SEARCH-CACHE** Search results are not cached anywhere on the server.
      Every keystroke-debounced query re-runs the full Bleve search plus the
      book hydration behind it.

      **Measured by reading the code 2026-08-11:** `AudiobookService` already
      owns a `listCache`, but it is consulted only on the **non-search** branch
      of `GetAudiobooks` (`internal/audiobooks/service_query.go`, cache key
      `all:<limit>:<offset>:p=…:sb=…:asc=…:noq=…`). The `if search != ""` branch
      goes straight to `searchWithBleve` / `store.SearchBooks` and never touches
      a cache on the way in or out.

      The frontend has `web/src/stores/useLibraryCache.ts` (50 entries, LRU-ish
      eviction), so a repeated query from the *same* browser tab may be served
      client-side — but nothing is shared between users, tabs, or the mobile
      app, and a cold tab always pays full cost.

      **Why it is worth doing:** the per-user post-filter path fetches a
      `searchPostFilterWindow`-sized candidate set from Bleve and narrows it in
      Go, so a search is markedly more expensive than a plain list page. That is
      also the reason the cache key cannot be the query string alone.

      **Cache key must include, or it will serve wrong results:**

      - the query string,
      - `limit`/`offset`,
      - `UserID` whenever per-user filters are active (`PerUserFilters` +
        `UserID`) — per-user narrowing happens AFTER Bleve returns, so two users
        running the same query legitimately get different sets,
      - sort field and direction,
      - every `ListFilters` value that participates in post-filtering
        (`LibraryState`, `Tag`/`Tags`, `FieldFilters`, `IsPrimaryVersion`,
        fingerprint status/coverage bounds).

      ⚠️ **Invalidation is the hard part, and it is the same gap as
      [MERGE-CACHE-EVICT].** A cached search that outlives an edit, merge or
      delete shows books that no longer exist, which is exactly the "I merged
      these and still see two copies" confusion. Prefer wiring it to the
      existing search-index dirty-set/reconciler
      (`internal/server/search_reconciler.go`) rather than a bare TTL — or use a
      short TTL (30–60s) as an explicit, documented first cut and say so in the
      log.

      Do NOT cache before deciding invalidation. A stale search result is worse
      than a slow one.

- [ ] **REVIEW-COMBINE-FIRST** Let the owner combine two books into one, or
      merge them as duplicates, **before** applying metadata — from the same
      surface where they are choosing the metadata. Requested 2026-08-11:
      *"a way to combine two books into one before I apply metadata, or merge
      them as duplicates before I apply my metadata."*

      **The ordering is the whole point.** Today the sequence is forced:
      metadata is applied to whatever book row happens to exist, and only later
      can rows be combined. When one logical book is split across several rows
      (extremely common in this library — see the 199 books exploded into 6,060
      single-file folders), the owner ends up applying the same metadata several
      times to fragments of one book, and the combine afterwards has to
      reconcile competing metadata that need never have diverged.

      Both actions already exist as separate UI flows:

      - **Combine into One Book** — `web/src/components/BatchToolbar.tsx:101`
        → `web/src/pages/Library.tsx:1256` → `POST /api/v1/audiobooks/combine`
        (`internal/server/wire_dedup_routes.go:75`). Hard-deletes the absorbed
        shells.
      - **Merge as Versions** — `web/src/pages/Library.tsx:1232`. Soft-deletes
        the losers and demotes them to non-primary.

      So the backend capability is there; what is missing is reaching it from
      the metadata chooser without losing the chooser's state.

      ⚠️ **Blocked-ish — read first.** Two live defects sit directly under this:

      1. **Version groups with two elected primaries.** Sampled on prod
         2026-08-11: 10 of 15 groups had two members both marked
         `is_primary_version=true`, so the "merged" book lists twice
         permanently. Suspected writers: `internal/merge/service.go:196-206`
         (reuses an existing group ID but only writes the flag on the books
         passed in, never demoting pre-existing members) and
         `internal/reconcile/reconcile.go:770-795`. Building combine-before-apply
         on top of a merge that can leave two primaries will produce more of
         them, faster. **Not verified** as the causal path for those rows.
      2. Applying metadata from the review screen did not reach the files at
         all until the fix on branch `fix/review-apply-writes-tags` — see the
         separate entry.

      Documentation check done 2026-08-11: the universal review queue
      (`docs/plans/2026-07-13-review-queue-and-regroup.md`, shipped July,
      `review_apply_enabled` defaults OFF) is **regroup-only**. It does not
      cover user-initiated combine, dedup review, or metadata review. The bulk
      metadata review plan
      (`docs/archive/superpowers/plans/2026-04-06-bulk-metadata-review.md`) and
      the dedup label review panel
      (`docs/archive/agent-tasks/dedup-ui/TASK-04-label-review-panel.md`) were
      both archived without shipping. The owner's instinct that these want one
      home is right; that home does not exist yet.

- [ ] 🚨 **E2E runs in one worktree can silently be served by a DIFFERENT
      worktree's server, and `global-setup.ts` does not catch it.** Hit for real
      on 2026-08-11 while gating `fix/library-load-freeze`. This is a false-green
      generator and it affects every agent running e2e concurrently.

      **What happened.** `playwright.config.ts` hardcodes `127.0.0.1:8484` and
      sets `reuseExistingServer: !process.env.CI`. There were 11 worktrees
      checked out. The sequence:

      1. Killed `:8484`, confirmed free.
      2. Ran `npm run build && go build` in my worktree (~3 minutes).
      3. During that window, a sibling worktree's agent started ITS server on
         `:8484`.
      4. My server launched, failed with
         `listen tcp 127.0.0.1:8484: bind: address already in use`, and did not
         exit — it just never served.
      5. `curl :8484` returned `200`, so everything looked healthy.
      6. My gate ran green-path assertions against the **sibling's build**, which
         contained none of my changes, and reported 4 failed / 1 passed.

      I only noticed because the numbers were *identical* to the pre-fix run —
      42,017 DOM nodes, requested limits `["1000","20"]`. Had my change been a
      no-op in the other direction, this would have been a **false green** and I
      would have shipped it.

      **Why the existing guard misses it.** `global-setup.ts` asserts the served
      bundle is not older than local build artifacts. A sibling that just rebuilt
      passes that check comfortably. The guard answers *"is it stale?"* — the
      question that matters here is *"is it mine?"*

      **Fix shape**, either or both:

      1. **Derive the port from the worktree** so collisions are impossible —
         e.g. hash the worktree path into a port, or read `E2E_PORT` with a
         per-worktree default. Then two agents cannot contend at all. (Workaround
         used on the day: a throwaway config with `--port 8585` and
         `reuseExistingServer: false`. It worked — full chromium suite 283
         passed / 0 failed / 21 skipped — but it was scratch, not committed.)
      2. **Assert identity, not freshness.** Have `global-setup.ts` fetch the
         served `index.html`, resolve the hashed asset filenames, and require
         they match the filenames present in the local `web/dist`. A sibling's
         bundle has different content hashes, so this fails immediately and
         loudly.

      Also worth fixing: a server that fails to bind should **exit non-zero**
      rather than staying alive doing nothing. That hung process is what made
      `lsof`/`ps` look reassuring while the port belonged to someone else.

- [ ] **ORGANIZE-4TH-COPY** `internal/server/handlers/filesystem.go:286` is a
      FOURTH copy of the single-file/multi-file organize routing bug, and it is
      the worst-behaved of the four.

      **Not fixed here on purpose.** That file belongs to **Wave 12** of the
      silent-failure plan, and the plan's rule is that every wave's file set is
      disjoint from every other's. Wave 3 leaving it alone is the rule working,
      not an oversight — but the audit characterises that line as bucket (d)
      "per-path DB resolution during a filesystem browse", which undersells it.
      Re-rank it 🔴 and fix it in Wave 12, or pull it forward on its own.

      **The defect.** The auto-organize block after a filesystem browse calls
      `org.OrganizeBook(dbBook)` — the SINGLE-FILE path — for every book. Any
      book whose `file_path` is a directory fails with
      `file_path %s is a directory but single-file organize was requested`.
      Multi-file books are most of the library.

      **Why it is worse than the other three:** the error is discarded by a
      bare `continue` with **no log at all**, so unlike `server.go` (which at
      least logged a warning) this copy fails completely silently. It also
      collapses `if err != nil || dbBook == nil` into one branch, hiding a DB
      lookup error and a missing row as the same non-event.

      **Fix:** `organizeService.OrganizeOneBook(org, dbBook, log)` plus
      organized/failed/notInDB/lookupErrors counters, exactly as
      `server.go`'s `AutoOrganizeFn` (#2303) and
      `folder_autoscan_op.go` (this wave) now do.

      ---

      **The other five call sites of `Organizer.OrganizeBook` are CORRECT.**
      Recorded so nobody re-checks them:

      | site | why it is fine |
      |---|---|
      | `organizer/service.go:1000` | this IS `OrganizeOneBook`'s single-file branch |
      | `itunes/service/importer.go:1549` | guarded by `if len(files) > 1 → organizeMultiFileBook` |
      | `server/batch_save_op.go:125` | guarded by an `isDir` stat → `OrganizeDirectoryBook` |
      | `server/handlers/organize.go:253` | full three-way branch (alreadyInRoot / isDir / single) |
      | `metafetch/service_apply.go:345` | the `else` of an explicit multi-file branch |

      **The process lesson, which is the reason this entry is this long.**
      PR #2303 fixed this defect in `server.go`, hoisted the three-way decision
      into `Service.OrganizeOneBook`, and asserted in its own PR body that "a
      third caller cannot reintroduce the same omission by copying the wrong
      half." That claim was **wrong when it was written**: a third copy
      (`folder_autoscan_op.go`) and a fourth (`filesystem.go`) already existed.

      The fix was right; the claim of completeness was not, because the search
      that produced it grepped for the *symptom string* from the production log
      rather than for every caller of the dangerous function. A one-line
      `grep -rn '\.OrganizeBook('` would have found all six immediately.

      When a fix is justified by "now it cannot happen elsewhere", the grep that
      proves it must be over the **callee**, not the symptom.

- [ ] **PLAYBACK-IMPORT** Listened / in-progress status is not coming across from
      iTunes (or from the files), so a book the owner has already finished still
      shows as unplayed here, and a book they are part-way through shows no
      progress. Reported 2026-08-11: *"I thought we were tracking listened status
      and copying that over from iTunes... it feels like none of the stuff to
      actually make the other features that need those were done."*

      Investigate and report before changing anything — this is suspected to be
      an **unwired pipeline**, the same shape as two other defects found the same
      night (the iTunes playlist importer was never called; nothing ever
      scheduled a folder scan). Confirm which of these is true rather than
      assuming:

      - Does the iTunes importer **read** the ITL/XML play-count, `Played`
        flag, and bookmark/position fields at all? If it parses them, where do
        they land?
      - Is there a **write path** from those parsed values onto the book /
        book_file rows (read status, progress position)? `internal/readstatus/`
        and `internal/itunes/service/position_sync.go` both exist — are either
        actually invoked on import, or only on the 2-way-sync path?
      - Does the **file itself** carry progress (embedded chapter/position
        metadata, `.m4b` bookmarks)? If so, is it read on scan?
      - Does the **API expose** listened/progress to the UI, and does the UI
        render it? A value that is stored but never surfaced looks identical to
        one that was never stored.

      ⚠️ Related known defect — do not repair progress data until it is fixed:
      silent-failure **Wave 5** covers `internal/itunes/service/position_sync.go`
      (lines 85, 118), where a *failed read* is indistinguishable from "no prior
      state", so the iTunes bookmark **overwrites the user's real playback
      position**. Backfilling progress through that path could destroy the very
      data this task is meant to restore. Wave 5 lands first.

      Also relevant: `internal/readstatus/readstatus.go:144` discards the
      existing state and rebuilds a fresh one on a read error, and
      `internal/database/pebble_store_playback.go:107` leaks a stale status index
      entry so a book can appear under two statuses at once.

- [ ] **LEAKSCAN-SCOPE** `scripts/check-memory-leaks.py` reports a false
      `addEventListener without removeEventListener` when the add is nested more
      than one brace level deeper than the cleanup. Its look-ahead abandons the
      search once `scope_depth < -1`, so an add inside `if (x) { if (y) {...}
      else { add } }` with the matching remove in a `finally` at function level
      is never paired — the two closing braces end the scan first.

      Hit for real on 2026-08-11 in `web/src/utils/apiFetch.ts`: the listener
      *was* removed in `finally`, and CI failed anyway. Worked around there by
      flattening the nesting, which is not a fix — the next correctly-cleaned-up
      listener at that depth will fail the same way, and the obvious "fix" a
      future contributor reaches for is deleting the check.

      Proper fix: pair by handler identity within the enclosing function rather
      than by brace-depth proximity, or track the function body extent instead
      of a running depth counter. Whatever is chosen, add a regression fixture
      with the add nested two levels below the remove so the heuristic cannot
      silently regress.

- [ ] **REVIEW-PREVIEW** Play the first ~2 minutes of audio directly from the
      metadata chooser. Requested 2026-08-11: *"I need a way to play the first 2
      minutes of audio right from the metadata chooser."*

      **Why it matters more than it sounds.** The chooser asks the owner to
      confirm a candidate match, but everything on screen is second-hand — a
      title string, a cover, a narrator name. The only ground truth for "is this
      actually the right book and the right narration" is the audio itself, and
      today confirming means leaving the UI entirely. For a library with known
      title contamination and mis-grouped multi-part sets, a 2-minute listen is
      the cheapest possible verification.

      Notes before designing:

      - A range-request audio endpoint may already exist for the player; check
        before adding a second one. If one exists, the work is UI-only.
      - The chooser row shows a **single file** even for a 40-part book (see
        REVIEW-MULTIFILE-CLARITY below), so "the first 2 minutes" must mean the
        first 2 minutes of **part 1 of the book**, not of whichever file happens
        to be attached to the row. Getting this wrong makes the preview
        actively misleading.
      - Do not stream the whole file. Bound the response; an unbounded
        request-scoped read on this server is exactly the shape that OOM-killed
        production on 2026-08-11.

      Documentation check done 2026-08-11: nothing designed. One backlog entry
      in `docs/archive/backlog-2026-04-10.md` calls it "nice but" and it was
      never specced. Not covered by the review-queue plan.

- [ ] **ROWCOUNT-REVERIFY** Re-measure every production table row count once the
      row/key-separated warmup counter is deployed, and correct what it moves.

      **Why this is open rather than done.** The `books` figure that appears
      throughout this repo (392,962 → 366,922 → 366,916, depending on vintage)
      was never a book count. It was the number of Pebble KEYS under the
      `book:` prefix, which is shared with roughly seven secondary-index
      families — `book:path:`, `book:hash:`, `book:originalhash:`,
      `book:organizedhash:`, `book:versiongroup:`, `book:work:`,
      `book:asin:`/`book:isbn13:` — at about 7.5 keys per row. The warmup now
      reports rows and keys separately, pinned by
      `TestWarmupCounts_CountRowsNotPebbleKeys`, but production has not yet
      run the fixed counter.

      **Best current number: ~48,900 books.** From the organizer's own full
      paging enumeration on 2026-08-11 (`Fetched 48896 total books from
      database`), corroborated by system-status readings of 46,221 and 54,734.
      This is the strongest available evidence, not a verified count.

      **What has already been corrected** (2026-08-11): the inflated figure was
      removed or annotated in `memdb_sort_index_cost_test.go`, `config.go`,
      `memdb_sort_indexers.go`, `pebble_store_versiongroup_backfill.go`,
      `library_list_warmer.go`, `bleve_translator.go`, `dedup/lifecycle.go`,
      `web/src/pages/Library.tsx`, `docs/design/2026-08-09-search-backend-options.md`
      and `docs/perf-audit-2026-05-29-heap-breakdown.md`.

      **The one that changed an answer, and needs an owner decision:** the sort
      index memory cost. The per-book measurement (+3,750 B/book for all nine
      indexes, measured at 100,000 books) was always correct; only the
      population it was multiplied by was wrong. Re-extrapolated:

      | | old (366,916) | corrected (~48,900) |
      |---|---|---|
      | all nine sort keys | +1,312 MB | **~+175 MB** |
      | per sort key | ~146 MB | **~19 MB** |

      "+1.3 GB on a box already at 1.25 GB resident" reads as prohibitive.
      "+175 MB" does not. The sort indexes shipped default-off on the strength
      of the larger number; whether to enable some or all of them is worth
      re-deciding on the corrected one.

      **Still to do:**
      1. After the next deploy, capture the warmup line and record rows AND
         keys for every table, not just books.
      2. Re-verify `book_files`, `works`, `book_authors`, `series`, `authors`.
         These came from the same counter, but each prefix has its own index
         families (or none), so they may or may not be inflated. **Do not
         assume they are wrong and do not assume they are right** — that
         assume-by-analogy step is what produced the original error.
      3. Re-multiply the absolute totals in
         `docs/perf-audit-2026-05-29-heap-breakdown.md`. Its per-row struct
         analysis is derived from struct shape and is unaffected.

      **The recurrence to avoid.** This number survived three separate
      opportunities to catch it: the design doc noticed the 392K-vs-44,888
      discrepancy and resolved it in the wrong direction by inventing
      "non-primary versions" to explain the gap; a later pass "corrected" 392K
      to 366,916, propagating the error while appearing to fix it; and a
      backfill audit reached "13.3% under-scan" by dividing a real scan count
      by the inflated one. Two numerators agreeing tells you nothing about the
      denominator they share.

- [ ] 🐛 **`GET /audiobooks/soft-deleted` computes its `total` by fetching up to
      10,000 rows and taking `len()`, so the count is silently WRONG above
      10,000 and the server pays a 10,000-row read on every call.** Found
      2026-08-11 while fixing the library load freeze (branch
      `fix/library-load-freeze`); deliberately NOT fixed there, because that
      change is client-side and this one is not.

      `internal/server/handlers/audiobooks/handler.go`, in
      `ListSoftDeletedAudiobooks`:

      ```go
      books, err := h.audiobookService.GetSoftDeletedBooks(ctx, params.Limit, params.Offset, olderThanDays)
      ...
      // Get total count (unpaginated) for proper pagination support
      allBooks, _ := h.audiobookService.GetSoftDeletedBooks(ctx, 10000, 0, olderThanDays)
      total := len(allBooks)
      ```

      Two separate problems:

      1. **The count saturates.** A library with 12,000 soft-deleted books
         reports `total: 10000`. Nothing anywhere says the number is a floor, so
         the UI presents a wrong count as an exact one. Note the error from the
         second call is discarded into `_`, so a failed count is reported as
         `total: 0` — indistinguishable from "nothing is soft-deleted", which is
         the more alarming direction to be wrong in.

      2. **The read happens regardless of `limit`.** The client-side fix now
         asks for `limit=1` on mount specifically to avoid pulling 10,000 rows,
         and the handler pulls them anyway to compute `total`. The wire payload
         and the DOM cost are gone (that was the freeze); the server-side read
         is not.

      Fix shape: add a real count to the store layer — `CountSoftDeletedBooks`
      alongside `GetSoftDeletedBooks`, iterating keys without materializing
      book structs — and have the handler call it instead of the
      fetch-and-`len()` trick. Propagate the error rather than dropping it. If
      an exact count is genuinely too expensive, return an explicit
      `total_is_lower_bound: true` so the UI can render "10,000+" honestly,
      but prefer the real count.

      Worth checking for the same fetch-and-`len()` pattern elsewhere in the
      handlers package while in there.

- [ ] **VG-DOUBLE-PRIMARY** A version group can end up with **two** members both
      flagged `is_primary_version=true`, so a merged book shows twice in the
      library forever. Found 2026-08-11 while investigating the owner's report
      that combined books still list individually.

      **Measured on prod, cache-busted so this is not the stale-list defect:**
      sampled 15 non-primary books at `offset=500`; all 15 sat in genuine
      multi-member groups **with** an elected primary — so there is no
      orphan-group defect. But **10 of those 15 groups had two primaries out of
      three members.** Reproduced on the plain (non-search) list path the UI
      uses:

      ```
      ?limit=20&is_primary_version=true&filters=[{"field":"title","value":"Dungeon Tour Guide"}]&_cb=201
      → count=4
        01KNDBMH1SJ29JF4ZTS5NTETPF  grp=01KNDBMH1SJ29JF4ZTS6S5B1X0  primary=True  created 2026-04-04
        01KZQQVA66GVMFNVWPA9T0V2EE  grp=01KNDBMH1SJ29JF4ZTS6S5B1X0  primary=True  created 2026-08-11
        01KNDC7G0GCX0ZPTJDXQG8NQ3N  grp=01KNDC7G0GCX0ZPTJDXSRQFF2X  primary=True
        01KZR8862HZJE7AWMTGQFADEHG  grp=01KNDC7G0GCX0ZPTJDXSRQFF2X  primary=True
      ```

      Two groups, four rows. The list filter is an exact index lookup on `true`
      (`internal/database/memdb_summaries.go:133`,
      `internal/database/memdb_reads.go:623-628`), so both members list. This is
      **independent of the response-cache staleness bug** — it persists after a
      cache bust and after a restart.

      **Candidate writers — none verified as the causal path for these rows:**

      - `internal/merge/service.go:196-206` reuses an existing group's ID but
        writes `IsPrimaryVersion` only on the books passed **into** the call.
        Pre-existing members of that group are never demoted. This is the
        strongest candidate.
      - `internal/reconcile/reconcile.go:770-795` promotes `kept` and demotes
        only `originals`, not sibling library copies.
      - `internal/reconcile/reconcile.go:1358-1367` mints a new group + primary.

      The newer half of each observed pair is a `01KZ…` ULID minted 2026-08-11
      at an `organize`d path, which *looks like* the organize/library-copy path
      minting a second primary into an existing group. **Not verified** — do not
      start from that assumption without confirming it.

      **Needs both halves:**
      1. Forward fix — enforce one primary per group. When reusing an existing
         group ID, load every current member and demote them.
      2. Backfill — the existing double-primary rows do not self-heal. Needs a
         maintenance repair op. Scope unknown: 10 of 15 sampled is not a
         library-wide rate, it is a sample of one offset window. **Measure the
         real count before sizing the repair.**

      Add an invariant test that a group can never have more than one primary,
      and run it against the existing data as a diagnostic before writing the
      repair.

      Related but distinct: the 24h list-cache staleness
      (branch `fix/list-cache-generation`) masks merges too, but that one is a
      read-path bug that a restart clears. This one is real, persistent data.

- [ ] **UI-LOCKUP-2** The web interface still locks up despite the virtualization
      work and the earlier backend fixes. Reported 2026-08-11.

      **Do not assume this is still a frontend/DOM-volume problem.** Measured on
      prod the same night, the backend alone can account for a UI that appears
      frozen:

      - `GET /api/v1/audiobooks?library_state=imported&limit=1` took **36
        seconds** — for one row.
      - `GET /api/libraries/{id}/personalized` took **2m10s**.
      - The server was OOM-killed **four times** in ninety minutes, and memdb
        warmup takes **568 s (9.5 min)** during which the library is unusable —
        `library list warm-up: memdb not ready after 5 min, skipping` fires
        because warmup outlives its own waiter, so the list cache never warms.
      - The activity-log query ignores client disconnect, so abandoned requests
        keep scanning; 30 such goroutines were pinning 30 GB with zero clients
        connected.

      A frontend cannot render what it cannot fetch, and a browser tab whose
      requests never return looks exactly like a frozen UI. So the first job is
      to **separate the two**, not to add more virtualization:

      1. Reproduce with DevTools Network open. Are requests **pending** (backend)
        or returning fast while the page janks (frontend)?
      2. If backend: which endpoint, and is it one of the known-unbounded ones?
      3. If frontend: profile it. Is virtualization actually active on the list
        that janks, or only on the one that was fixed before?
      4. Check whether the lock-up correlates with server restarts / warmup
        windows — if it only happens in the ~10 min after a restart, it is
        startup sequencing, not the UI.

      State which of the two it is, with evidence, before writing any fix. The
      previous round of this task was closed against a DOM-volume hypothesis; if
      that hypothesis was wrong, or was right then and is not the binding
      constraint now, say so explicitly.

- [ ] **Repair books applied from the Metadata Review screen before the tag-write fix.**
  `BatchApplyFromCache` updated the database without ever writing tags or
  embedding cover art (fixed in `fix/review-apply-writes-tags`). Every book
  applied from that screen while the defect was live has correct metadata in
  the DB and stale tags on disk. A repair path already exists and does not need
  to be built: the `library.bulk-write-back` operation
  (`internal/server/metadata_ops.go:808` `runBulkWriteBack`, HTTP entry
  `handleBulkWriteBack` in `internal/server/handlers/metadata/handler.go:1175`)
  re-writes tags for a filtered set of books with a worker pool and resume.
  What is missing is the *selection*: there is no record of which books were
  applied from the review screen specifically, so scoping the repair means
  either running it library-wide or deriving the set from the activity log.
  Owner decision — no code needed if a library-wide run is acceptable.

- [ ] **Consider the same file-I/O audit for the remaining apply-shaped
  endpoints.** Two apply paths existed and only one wrote tags. Nothing
  structurally prevents a third from drifting the same way — a shared
  "apply + schedule file I/O" helper would, and neither path uses one today.

- [ ] **17 API calls will now surface an expired session that previously
  returned silent success.** `apiFetch` throws `ApiAuthRedirectError` on a
  login-page response; callers in `web/src/services/api.ts` that check only
  `response.ok` and never read the body (quarantineBook, unquarantineBook,
  restoreSoftDeletedBook, removeImportPath, changePassword, linkBookVersion,
  markNoMatch, includeFilesystemPath, deleteBackup, clearMetadataNoMatch,
  runMaintenanceWindow, updateTaskConfig, saveUserColumnConfig,
  saveSavedFilterPresets, mergeDedupCandidate, dismissDedupCandidate,
  revokeAPIKey) will now throw where they used to succeed. That is the fix
  working, but each caller's catch handler should be checked for a message
  that makes sense to a user.

## ⚠️ The activity channel overflows during organize and DROPS records

While accounting for organize failure logs on production, 1,000 of the 19,519 matching
lines since 2026-08-10 turned out not to be organize failures at all:

```
[WARN] activity channel full, dropped: operation: …
```

They cluster tightly — 44–70 per second during the Aug 11 22:35–22:36 organize run — i.e.
the activity pipeline saturates precisely when an operation is producing the most activity
worth recording. Every dropped line is an activity record that no longer exists anywhere.

**Why it matters.** The drop is announced only in the process log, at WARN. Nothing in the
API, the activity feed, or the operation summary tells a user their activity history has
holes in it, or where. Anyone reading the activity log for "what did this organize run
do?" gets a silently truncated answer that looks complete — the same shape as the other
silent-success defects on this list.

**What is NOT measured:** whether the drops are uniform or biased toward a particular
activity type; whether an operation's own change rows (`organize_failed` /
`organize_summary`) go through this channel and are therefore also lossy, or use a
different, durable path. Establish that first — if operation changes are lossy, then the
per-book error detail an organize run is supposed to leave behind is unreliable, which
undermines using it as evidence for anything else.

**Fix directions, cheapest first:**

1. **Count and surface.** Keep a dropped-record counter and report it on the operation and
   in the activity feed ("N activity records dropped during this run"). Turns an invisible
   loss into a visible one. Does not fix the loss.
2. **Back-pressure instead of drop** for operation-scoped activity, so a burst slows the
   producer rather than discarding history. Needs care: organize's worker pool must not
   deadlock behind a full channel.
3. **Resize / batch** the channel. Simplest, but only moves the threshold — a large enough
   library will always outrun a fixed buffer, so do this *with* (1), never instead of it.

Whatever is chosen, (1) is non-negotiable: a queue that drops data must say how much.

- [ ] **`BookFile.Duration` is `int` seconds, so every per-track duration is truncated and
  `startOffset` drift compounds through a book.** Found by turning on value comparison in
  the ABS conformance suite (#2337), measured against the real *Odyssey* capture in
  `testdata/abs-fixtures/get_api_items_id.json`:

  ```
  oracle sum(audioFiles.duration) : 9975.431111
  int-truncated sum (ours)        : 9973          → 2.431 s short
  oracle startOffsets : [0, 1386.06, 2788.70, 4309.21, 6928.98, 8602.20]
  ours (int seconds)  : [0, 1386,    2788,    4308,    6927,    8600   ]
                                                       ↑ 2.200 s drift by track 6
  ```

  `startOffset` is cumulative, so error accumulates at roughly 0.4 s per track boundary —
  this item has 6 files but its own tags say the work is 24 parts, which would drift on
  the order of 10 s by the final track. A client that seeks using `startOffset` lands
  progressively further off the deeper into the book the listener is.

  `internal/database/store.go:696` — `Duration int`. There is no millisecond field on
  `BookFile` (`AcoustIDFingerprintDurationSec` immediately below it *is* `float64`, so
  sub-second precision is already understood to matter elsewhere in the same struct).
  `mapper.go:217` widens it back out with `DurationSec: float64(f.Duration)`, which cannot
  recover what the store never held.

  Not fixed alongside the conformance work by owner decision on 2026-08-12: changing a core
  production field's type touches the store, the mappers, the importers and needs a
  backfill, and had no business riding along with test-fixture changes. The affected
  conformance assertions carry **bounded** allowances (they still fail if a duration is
  wrong by more than the known truncation), so this stays visible rather than becoming
  permanently accepted.

- [ ] **`deviceInfo.deviceType` is always `"unknown"`, and the capture cannot tell us what
  it should be.** `play.go:307` defaults it to `"unknown"` and then echoes whatever the
  client sent (`play.go:315`), so a client that supplies `deviceType` is already handled —
  the gap is only that we never *derive* it. Real ABS derives it from the User-Agent, and
  the oracle answered `"wearable"` for a request whose body carried only `clientName` and
  `deviceId`.

  **Blocked on evidence, not effort: 0 of 28 fixtures record request headers at all**, so
  the User-Agent that produced `"wearable"` is not preserved anywhere. Inferring a
  UA→deviceType rule from one output with no recorded input is exactly the single-sample
  mistake that produced the retracted `tagTrack` finding. Unblocking this means teaching
  the capture harness in `testdata/abs-oracle/` to record request headers and re-capturing;
  the derivation is then a small, testable mapping. Low priority regardless — nothing in
  the client contract reads `deviceType`; it is diagnostic, shown in the sessions list.

- [ ] **`publishedYear` loses the era: `Book.PrintYear` is an `int`, so the oracle's
  `"800BC"` comes back `"800"`.** ABS passes the raw date tag through. Same shape of loss
  as the duration truncation above — a typed column cannot hold what the tag said. The
  `publishedDecades` filter facet inherits it. Only visible on pre-CE material, so this is
  genuinely low priority, but it is the same class of bug and worth recording as such.

- [x] **`timeBase` is hardcoded `"1/1000"` at `internal/server/handlers/abs/mapper.go:645`**
  where the oracle carries ffprobe's real stream `1/14112000`. We do not capture stream
  `time_base` at import, so there is nothing to map from. Owner decision 2026-08-12: allow
  it with a documented permanent allowance rather than add an ingest field and backfill for
  a value no client is known to divide by. Revisit only if a client turns out to use it.

- [ ] **SEC-CODEQL-BACKLOG** 326 open CodeQL alerts on `main`, including **2
      critical** and **17 high**. Counted 2026-08-12 via the code-scanning API
      across all four result pages — this is the full set, not a page-1 sample.

      **Why this is filed as one entry and not 326:** 302 of the 326 are a
      single rule, `go/log-injection` (medium), spread across ~30 files. It is
      one pattern — user-supplied strings reaching a log call without newline
      stripping — not 302 independent defects. Fixing it is a mechanical sweep
      plus one helper, and it should be done as a sweep or explicitly accepted
      as a whole, never one PR at a time.

      ```
      302  [medium]    go/log-injection
        5  [high]      go/path-injection
        3  [medium]    actions/missing-workflow-permissions
        3  [high]      go/disabled-certificate-check
        3  [high]      js/remote-property-injection
        2  [high]      go/clear-text-logging
        2  [critical]  go/request-forgery
        2  [none]      js/trivial-conditional
        1  [high]      go/weak-sensitive-data-hashing
        1  [high]      go/uncontrolled-allocation-size
        1  [high]      js/insecure-temporary-file
        1  [high]      go/zipslip
      ```

      **The two critical ones are server-side request forgery** and should be
      read first, because both sit on paths that fetch a URL chosen by remote
      metadata rather than by the owner:

      - `internal/metadata/cover.go:135` (alert #662)
      - `internal/covers/covers.go:82` (alert #645)

      Not assessed. Do not assume they are false positives — a cover URL comes
      from a third-party provider response, which is exactly the untrusted
      input SSRF rules are about.

      **Highs worth reading before the log-injection sweep,** because they are
      on file-mutating or archive-extracting paths where a wrong answer costs
      data rather than log noise:

      - `go/zipslip` — `internal/backup/backup.go:275` (alert #13). Archive
        entry paths used during extraction. This is the restore path.
      - `go/path-injection` ×5 — `internal/fileops/safe_operations.go:122` and
        `:157` (#1477, #1478), `internal/metadata/assemble.go:272` (#1429),
        `internal/server/handlers/filesystem.go:271` (#1105),
        `internal/audiobooks/service_mutation.go:63` (#1104).
      - `go/disabled-certificate-check` ×3 — two are in `tools/cmd/` one-offs
        (`merge-split-books`, `reconcile-paths`), one is in
        `internal/mtls/provisioning.go:142` and matters more. **Resolved
        2026-08-23 (TASK-084, PR #2800):** all 3 (alerts #379, #974, #959)
        dismissed via the code-scanning API with `dismissed_reason: "won't
        fix"`, each justification re-verified clause-by-clause at HEAD before
        dismissal. Dismissed, not fixed — the `InsecureSkipVerify` calls
        remain in the code, intentionally, with their in-code justifications
        unchanged.
      - `go/weak-sensitive-data-hashing` — `internal/database/apikey_token.go:33`
        (alert #1466). API-token hashing.

      **Two alerts already assessed as false positives, with the reason
      recorded so nobody re-derives it:**

      1. `go/clear-text-logging` at `internal/server/server.go:360` (raised on
         PR #2321). The flagged expression is `fmt.Sprintf("%T", s.Store())`.
         `%T` renders only the dynamic type name and cannot render a field
         value, so the `password` field CodeQL traced into the store struct
         cannot reach the log record. CodeQL does not model `%T` as
         value-suppressing. The reason is also in a comment at the line.
      2. `go/uncontrolled-allocation-size` at
         `internal/database/memdb_summaries.go:163`. The `make` cap is clamped
         on **both** sides before use: `memdb_summaries.go:80` turns
         `limit <= 0` into 1,000,000 and `:160` clamps anything above 4096 down
         to 4096. No panic path and no unbounded allocation.

      **Context that changes how to read this list:** the CodeQL PR check fails
      on *new* alerts in changed code, so with a 302-alert pre-existing pattern
      almost any PR that adds a log line turns the check red. That has already
      happened twice (#2320 added 7 `go/log-injection` instances in
      `internal/server/handlers/metadata_cache.go`; #2321 surfaced 3 more in
      `internal/httputil/respond.go`). Until the sweep lands, a red CodeQL
      check on a PR carries almost no signal, which is itself the problem — a
      gate that is always red is a gate nobody reads.

      **Suggested order:** (1) read the 2 criticals, (2) the zipslip and the 5
      path-injections, (3) decide sweep-vs-accept on `go/log-injection` as a
      single decision, (4) re-check that the PR gate means something again.

## ✅ FIXED — `EmbeddingStore.Close()` deadlocked against in-flight writes

**Fixed by `fix/embeddingstore-close-lock`.** Every DB-touching method now holds
`closeMu.RLock` for its whole duration and `Close` takes the write lock, so `Close` cannot
begin until the last in-flight operation has returned and the Pebble UB is unreachable.
The three chaos tests' worker waits are now bounded, so a regression fails in 30 seconds
naming the broken invariant instead of hanging for the full job cap.

The heading of this entry originally read "~4–5% of CI runs". That figure is **retracted**
— see the rate section below. It was inferred from run history and then contradicted by
the same branch hanging 2 out of 2 attempts.

Everything below is the original diagnosis, kept because the mechanism is worth reading.

---

`TestChaos_MixedReadWriteDuringClose` hangs forever, burning the whole Go Tests job.
This is the cause of the long-standing "Minimal CI / Go Tests cancelled with no reason"
mystery — under the old 20-minute cap the job was killed before Go's own 30-minute
`-timeout` could fire, so the hang could never name itself. Raising the cap to 35m
(github-common #346 + pin bump #2322) made it self-identify on the first occurrence.

**NOT a production bug.** `owned: true` — the flag that makes `Close()` actually shut the
Pebble DB down — appears in exactly three places, all test files
(`embedding_store_chaos_test.go:30`, `embedding_store_test.go:23`,
`embedding_store_candidate_durability_test.go:32,53`). The production constructor
`NewEmbeddingStore` (`internal/database/embedding_store.go:211`) sets `owned: false`, so
`Close()` returns `nil` on its first line and never touches Pebble. Do not describe this
as a prod shutdown hazard — it is a latent one that becomes real only if anything ever
sets `owned: true` outside tests.

### Mechanism

From the goroutine dump of run 31603570061, exactly **4** goroutines are involved (the
other 8 in the dump are idle `nutsdb.doWrites` background workers leaked by earlier tests
and one `testing.tRunner` — red herrings):

```
goroutine 2261 [sync.WaitGroup.Wait, 29 minutes]   <- the test's wg.Wait()
3 x writer     [sync.Mutex.Lock,    29 minutes]
    pebble/v2.(*commitPipeline).prepare   commit.go:455
    pebble/v2.(*DB).applyInternal         db.go:882
    pebble/v2.(*DB).Set                   db.go:646
    database.(*EmbeddingStore).setJSON
```

The test calls `store.Close()` while 13 goroutines are mid-operation. Pebble documents
that calling `Close` concurrently with any other DB method is not safe. Three writers
that were already inside `db.Set` block on the commit-pipeline mutex and never wake.

Two things make this hard to see:

- **`recover()` cannot catch a deadlock.** All three chaos tests defend with
  `defer func() { recover() }()` and a comment saying PebbleDB panics during close are
  acceptable. That defence is sound against a panic and useless against a hang — which is
  why the tests looked adequately guarded for months.
- **`closed atomic.Bool` is structurally incapable of fixing it.** It is a check-then-act:
  a writer that passed the check and is *inside* `db.Set` when `Close()` lands is already
  past the gate. Serialising against an in-flight operation requires a lock held for the
  operation's **duration**, not a flag read at its start.

### Rate, measured

60 most recent `ci.yml` runs: 37 success, 15 cancelled, 5 failure, 3 in-flight. Of the 15
cancelled runs, the Go Tests **job** duration separates the two meanings of `cancelled` —
normal duration is ~8 min:

| Go Tests duration | count | reading |
|---|---|---|
| 3s – 8m | 12 | concurrency-supersede, benign |
| 20m14s | 1 | hit the old 20m cap — the hang |
| 33m (run 31603570061) | 1 | the hang, self-identified under the 35m cap |

**The rate is NOT established, and a first estimate of "4–5%" from that table was wrong.**
Re-running the failed job on PR #2323 hung a second time — 14:58:12→15:32:15, 34m03s,
hitting the 30m Go timeout again. That branch is **2 attempts, 2 hangs**. A per-run rate
inferred from the history table cannot be reconciled with 2/2 on one branch, so treat the
table as a floor on *how often it has been seen*, not as a probability:

- PR #2323 (`docs/todo-listened-status`): **2 of 2 attempts hung**.
- Historical: at least **1 further** hang (the 20m14s run) in the 60 examined.
- Local macOS: **0 of 50**.

Do not quote a percentage until someone runs the same SHA N times on Linux CI. What is
established is that it recurs, that it reproduces on demand on at least one branch, and
that each occurrence costs the full job cap.

NOT reproducible locally: 50 runs of `go test ./internal/database/ -run
TestChaos_MixedReadWriteDuringClose -count=1 -race` on macOS produced 0 hangs. macOS
scheduling does not generate the interleaving that Linux + `-race` + parallel packages
does. Treat the local result as "wrong instrument", not as evidence of absence.

Because it reproduces reliably on at least one branch, this now **blocks PRs**, which
raises it from "documented annoyance" to "fix before the next `internal/database` PR".

### Fix direction (not yet applied)

Give `EmbeddingStore` the guarantee Pebble does not: a `sync.RWMutex` where operations
hold `RLock` for their whole duration and `Close` takes `Lock`. Then no operation can be
in flight while `pebble.DB.Close()` runs, and the UB becomes unreachable.

Scoping notes for whoever does it — the 38 `s.db.*` call sites are **not** funnelled
through a few helpers, so this is not a 2-line change:

| primitive | sites | guard granularity |
|---|---|---|
| `s.db.Get` | 13 | primitive-level wrapper is sufficient — the whole op is inside the lock |
| `s.db.Set` | 5 | same |
| `s.db.Delete` | 1 | same |
| `s.db.NewIter` | 10 | **method-level** — the iterator outlives any wrapper, and Pebble's constraint explicitly covers outstanding iterators |
| `s.db.NewBatch` | 8 | **method-level** — the batch outlives the wrapper |

Hazard to avoid: four exported methods call other exported methods
(`embedding_store.go:349` → `ListByType`, `:515` → `UpsertCandidateNew`, `:1458` and
`:1462` → `CountByType`). A naive `RLock` at every method entry makes these recursive,
which deadlocks whenever a writer is waiting between the two `RLock`s. Those four need a
wrapper/inner split (`Foo` takes the lock and calls `fooLocked`). They are all in the
iterator group, i.e. exactly the methods that need method-level guarding anyway.

Do **not** fix this by deleting or skipping the chaos test. Its premise — that closing a
store we own should not hang — is reasonable, and our type is the right place to provide
the guarantee.

### Adjacent finding, do not expand scope to fix

`closed atomic.Bool` is documented as "set on Close; makes post-close ops return errors,
not panic", but it is checked in only **2 of 34** methods.
`TestChaos_OperationsAfterClose` passes anyway because *Pebble* returns `ErrClosed` on a
closed DB — not because of that guard. The field's doc comment claims a property the code
does not have. Worth correcting when the RWMutex work lands, since the same pass touches
every method.

## 🔴 An empty `FieldFilter` value silently returns the WHOLE library

Measured against production 2026-08-12 on `GET /api/v1/audiobooks?filters=…`:

| `filters` value | `count` returned |
|---|---|
| `[{"field":"title","value":"Hyperion"}]` | 4 |
| `[{"field":"title","value":"zzzzz-no-such-title-exists"}]` | 0 |
| `[{"field":"title","value":""}]` | **63,870 — the entire library** |

The filter works. An **empty value is dropped**, and the request degrades to an unfiltered
list. The response carries no indication that a filter was discarded: the caller asked
"which books have a blank title?" and got back every book in the library, including ones
whose titles are plainly non-blank (`The Awakened Spark`, `Hyperion`).

**Why this is worse than a plain bug.** The failure is indistinguishable from a legitimate
answer. Anyone measuring "how many books are missing field X" gets the library size and
may well believe it — the number is large, which is exactly what a "lots of books are
missing metadata" hypothesis predicts. It is a filter that answers "everything" when it
means "I ignored you", and it will silently corrupt any audit built on it. It blocked a
real measurement while investigating the organize target-path collisions.

The same silent drop applies to the flat `?title=` query parameter, which is not a
supported parameter at all — passing it returns the unfiltered first page rather than an
"unknown parameter" error. Two separate paths, both answering confidently to a question
they never applied.

**Fix direction.** Decide explicitly what an empty value means and make the code say so:

- If empty means "match rows where this field is empty" — implement it, and it becomes the
  natural way to audit missing metadata.
- If empty is not a supported query — reject the request with 400 and name the offending
  filter. Never silently widen the result set.

Either is fine. Silently returning everything is not. Add a test that pins the chosen
behaviour, because both alternatives look identical from the outside today.

Related: this is the same family as the search post-filter pagination defect and the
"fallback that triggers only on ZERO results" — a code path that cannot distinguish
"no constraint" from "constraint I could not apply".

## ✅ ANSWERED — the organize target-path collisions are DISTINCT BOOKS, not one book's files

This closes the "are the 3,194 collisions distinct books or one book's files?" question.
**They are distinct book rows, confirmed by ID.** But the headline number was wrong in a
way that matters, and the shape of the collision is not what the earlier note implied.

### How it was measured

Every `target path already occupied by a different file` line on the production host
since 2026-08-10: **19,519 lines, fully accounted for** —

| bucket | lines |
|---|---|
| `operation: Organize failed for <title>: …` | (parsed) |
| `operation: Failed to organize <title>: …`  | (parsed) |
| **both shapes together** | **18,519** |
| `[WARN] activity channel full, dropped: operation: …` | 1,000 |

Two different phrasings for the identical failure means **two emit sites**. Any grep that
matches only one of them under-reports by ~40%. That is how the first pass at this
counted 12,213 and thought it had everything.

### Per-run breakdown (runs split on a >300s gap between failures)

| run | window (Aug 11) | failures | distinct titles | distinct targets | contain "read by narrator" |
|---|---|---|---|---|---|
| 0 | 02:00:30 → 02:01:00 | 1,098 | 837 | 874 | 827 |
| 1 | 02:17:18 → 02:34:07 | 782 | 735 | 743 | 750 |
| 2 | 06:36:18 → 06:36:22 | 1,098 | 837 | 874 | 827 |
| 3 | 06:53:15 → 08:41:12 | 6,514 | 2,702 | 3,418 | 5,956 |
| 4 | 09:09:14 → 10:04:15 | 4,736 | 4,703 | 4,716 | 4,184 |
| 5 | 10:25:23 → 10:44:50 | 1,636 | 971 | 1,005 | 1,258 |
| 6 | 22:34:02 → 22:37:16 | 2,655 | 475 | 478 | **0** |

### Finding 1 — the collisions are distinct books, verified against the DB

The top colliding target in run 6 is hit **848 times by a single title** (the empty
string). A title alone cannot distinguish "848 distinct books that all have a blank
title" from "one book logged 848 times", so the log was not sufficient. Querying the
API by exact title settles it:

| title | books in DB with that exact title | times it collides in run 6 |
|---|---|---|
| `Clarke, Susanna` | **128** (distinct IDs) | 120 |
| `nobody103 (Jack Voraces)` | **176** (distinct IDs) | 84 |

Distinct book IDs, same title, same author → **identical expanded target path**. Every
book after the first finds the path occupied. This is a stampede of distinct books onto
one name, and it is the dominant failure mode.

Note what those two titles are: an **author name** (`Clarke, Susanna`) and a
**narrator credit** (`nobody103 (Jack Voraces)`) sitting in the *title* field. The
collision is downstream of the metadata-parser contamination already tracked elsewhere —
fixing the titles would dissolve most of these collisions without touching the organizer.

### Finding 2 — the "read by narrator" fix was ORTHOGONAL to the collisions

Run 3 (pre-fix, paths contain the literal) and run 6 (post-fix, zero occurrences) show
the same pileup on the same degenerate name:

```
run 3:  .../Unknown Author/Unknown Title/Unknown Title - Unknown Author - read by narrator.mp3   x852
run 6:  .../Unknown Author/Unknown Title/Unknown Title - Unknown Author.mp3                       x848
```

852 → 848 across the fix. **The narrator literal never caused these collisions.** It was
correlated with them only because both are symptoms of the same missing metadata. The
comment in `internal/organizer/organizer_test.go` that read "2,611 of 3,194 occupied-path
organize failures contained 'read by narrator'" invited exactly the wrong inference and
is corrected in the same PR as this fragment.

### Finding 3 — the mode is not constant across runs, and that is unexplained

Run 4 is 4,736 failures over 4,716 distinct targets — essentially **1:1, no stampede at
all** — while runs 3 and 6, on the same day and the same library, are heavily stamped.
Whatever distinguishes run 4 (a different book population, a filter, a different entry
path into organize) is **not measured**. Do not assume a single cause for all seven runs.

### ⚠️ The 3,194 figure is not reproduced by this data

No run produced 3,194 failures, and no run produced 2,611 narrator-literal lines. The
closest candidates are run 6 (2,655) and run 5 (1,636). The original 3,194/2,611 pair
came from some other source — most likely one operation's `stats.Failed`, which counts
*all* failures rather than only occupied-target ones, or a run before the 2026-08-10 log
horizon. **Treat 3,194 as unverified** until whoever recorded it says where it came from.
The per-run table above is the measured replacement.

### What is NOT claimed

- How many books have a genuinely blank title. The obvious query for it is broken — see
  the sibling fragment on empty `FieldFilter` values returning the whole library.
- Whether the occupying file at each target is a real organized book (case 3) or an
  orphan from a partial organize (case 4). `organizer.go` distinguishes these internally
  but logs both identically.
- Why run 4 shows no stampede.

### 🔴 OWNER DECISION — do not pick this unilaterally

`generateTargetPath` has **no uniqueness guarantee**. When the naming pattern expands to
the same string for N books, all N target one path and N−1 fail. The existing empty-stem
fallback in `generateTargetPath` makes this worse in a specific way: it was added because
an empty stem produced a bare `.m4b` that "EVERY such book collides on", and it falls
back to `defaultTitle` — *a constant*. That trades a collision on one name for a
collision on another name. It is working exactly as written.

Three ways out, and the choice changes what lands on disk in a 63,870-row production
library, so it is yours:

1. **Refuse** to organize a book whose expanded path is degenerate (title and author both
   defaulted), reporting "insufficient metadata to name a unique file" instead of a
   collision. Honest, but it is a real behaviour change: the *first* such book currently
   succeeds and would stop succeeding.
2. **Disambiguate** — append the book ID or the source filename stem when the expanded
   path is already claimed. Nothing stops being organized, but ~848 files get names with
   an ID in them.
3. **Leave it** and fix the upstream metadata instead. Given that 128 books are titled
   with an author's name and 176 with a narrator credit, this may dissolve the problem at
   the source — but it is the slowest of the three.

A detection-only counter (report "N books have insufficient metadata to name a unique
file" at the end of an organize run, changing nothing on disk) is safe to add ahead of
this decision and would give a real number for option 3.

## 🔴 `RecomputeBookAggregates` is O(N²) on one write path and never runs on the other

Measured on production 2026-08-11, 06:30–10:46 (the window of a 4h15m library scan).

### The numbers

| metric | value |
|---|---|
| `RecomputeBookAggregates updated` log lines | **126,928** |
| distinct books touched | **5,595** |
| most recomputes for a single book | **1,189** |
| next three | 616, 416, 400 |
| file-record reads implied (Σ N(N+1)/2) | **5,430,858** |
| file-record reads if coalesced (1 per book) | 126,928 |
| **read amplification** | **42.8×** |
| book-row writes | 126,928 vs 5,595 → **22.7×** |

Book-size distribution of the touched set: 711 books with 1 file, 1,824 with 2–9,
2,901 with 10–99, 157 with 100–499, 2 with 500+.

### Why it is quadratic

`RecomputeBookAggregates` reads **every** file of a book (`GetBookFiles`) and rewrites
the book row on each call. Five write methods each trigger one via
`notifyBookFileChange`:

- `CreateBookFile`, `UpdateBookFile`, `DeleteBookFile`, `DeleteBookFilesForBook`,
  `DeleteBookFilesByIDs`

So inserting N files for one book one at a time costs 1+2+…+N file reads and N book-row
writes. For the 1,189-file book that is ~706,000 reads to insert 1,189 rows.

### The other half: the batch path never recomputes at all

`BatchUpsertBookFiles` does **not** call `notifyBookFileChange` anywhere. It refreshes
memdb and invalidates library stats, but the book's `Duration` and `FileSize` aggregates
are simply left stale after a batch write.

| path | recomputes per book |
|---|---|
| `CreateBookFile` / `UpsertBookFile` | **N** (O(N²) reads) |
| `BatchUpsertBookFiles` | **0** (stale aggregates) |

Two paths, opposite failure modes, neither correct. The right answer is **exactly one
recompute per affected book** on both.

### ⚠️ Attribution is NOT established — do not repeat this mistake

These 126,928 calls were initially attributed to the scan writing book files. **That is
wrong.** The scanner writes via `BatchUpsertBookFiles`
(`internal/scanner/scanner.go:1544`), which never triggers a recompute. The claim was
made from co-occurrence in a time window, and corrected on PR #2355.

`RecomputeBookAggregates updated` lines carry **no `opID` — 0 of 126,928** — so the log
cannot say who caused them. Co-occurring in the same window: 10,369 re-organizes, and
27,018 `book_file PID uniqueness: transferred to new row` (which is emitted from the
`CreateBookFile` mint path, and therefore *is* a per-file `notifyBookFileChange`
trigger). Neither is traced.

**First task is attribution, not the fix**: add the operation ID to the store's log line,
or instrument `notifyBookFileChange` with a caller tag. Fixing before knowing the
workload means the fix cannot be measured.

#### ✅ Attribution shipped — awaiting a production sample

`internal/database/aggregate_caller.go` adds a `caller` field naming the nearest stack
frame outside `internal/database`, e.g. `internal/merge.(*Service).mergeBooks:438`. It is
on all three aggregate log lines, including the `RecomputeBookAggregates updated` Info
line the 126,928 baseline was counted from — so the next sample is directly comparable
with it. A runtime stack walk was chosen over a signature change because
`RecomputeBookAggregates` is mocked in eight generated files.

**Still open — the numbers below cannot be produced until prod runs this build:**

```
# which subsystem drives the recomputes
journalctl -u audiobook-organizer --since "..." \
  | grep -o 'caller=[^ ]*' | sort | uniq -c | sort -rn

# redundant recomputes per caller (requires log level debug)
... | grep 'no change needed' | grep -o 'caller=[^ ]*' | sort | uniq -c | sort -rn
```

Degenerate values are meaningful, not noise: `runtime.goexit:0` means the write came from
a bare `go store.…(…)` with no in-repo frame left on the stack. Expect `caller` to name
the merge service, the five maintenance jobs, or the metadata write-back path — **not**
the scanner, which writes via `BatchUpsertBookFiles` and never reaches this code.

### Fix direction, once attribution is known

A coalescing scope on the store:

```go
flush := store.BeginAggregateBatch()   // notifyBookFileChange records book IDs
defer flush()                          // one RecomputeBookAggregates per touched book
```

- Depth-counted and mutex-guarded — the scanner runs worker pools, so concurrent
  `CreateBookFile` calls for different books must be safe.
- **Scope it per book, not per scan.** A scan-wide scope leaves aggregates stale for the
  whole 4h15m run, which is a correctness regression traded for speed.
- Apply the same coalescing inside `BatchUpsertBookFiles` — that closes the staleness gap
  in the same change.

## 🔴 Stored ZERO values shadow every `scheduled.*` default — nothing has been scanning

Measured on production 2026-08-12 via `GET /api/v1/config`:

```
scheduled.library_scan   = {enabled: false, interval: 0, on_startup: false}
scheduled.dedup_refresh  = {enabled: true,  interval: 0, on_startup: false}
scheduled.author_split   = {enabled: true,  interval: 0, on_startup: false}
scheduled.db_optimize    = {enabled: false, interval: 0, on_startup: false}
scheduled.label_refinement = {enabled: false, interval: 0, on_startup: false}
```

Compare the shipped viper defaults (`internal/config/config.go` ~line 1100):

| key | default | on prod |
|---|---|---|
| `scheduled.library_scan.enabled` | **true** | false |
| `scheduled.library_scan.interval` | **360** | 0 |
| `scheduled.dedup_refresh.interval` | 360 | 0 |
| `scheduled.db_optimize.interval` | 1440 | 0 |
| `scheduled.label_refinement.interval` | 10080 | 0 |

**Every interval in the block is 0.** Not one default survived.

### Consequence

`library_scan` is the only unattended discovery path for newly added books. With
`interval: 0` it never gets a ticker, so **nothing has been scanning automatically**.
PR #2315 shipped the periodic scan enabled-by-default and is deployed — the code is
right, the stored config defeats it. A book copied into an import path is still never
noticed until somebody presses Scan by hand, which is the exact bug #2315 set out to fix.

Only four tasks got tickers at the last restart (`tombstone_cleanup`, `purge_deleted`,
`isbn_enrichment`, `cleanup_activity_log`) — they read their intervals from config
fields outside the `scheduled.*` block, which is why the scheduler looked healthy.

### Mechanism

Viper's precedence chain (flag > env > file > default) only arbitrates values read
*through* viper. This codebase reads viper **once** into a plain Go struct
(`config.AppConfig`), and then a second, independent loader
(`LoadConfigFromDatabase` → `applySetting`) mutates that same struct from DB rows.
Two writers, one struct, no precedence rule between them — and the DB writer runs last.

`persistence.go` has a comment asserting this is safe: *"an absent stored setting must
leave the viper default alone. That holds here because applySetting is a per-leaf-key
switch: a key that was never stored is simply never seen."* The reasoning is correct;
the premise is not. The keys **were** stored. The tell is `dedup_refresh.enabled: true`
and `author_split.enabled: true` when both default to `false` — something wrote explicit
values for the whole block. That is the signature of a full-struct save (`PUT /config`
serialises every field), where fields the caller never populated are written as their Go
zero value.

**In Go, `0` and `false` are indistinguishable from "unset".** Once a whole-struct save
lands, the default is dead permanently — no later default change can ever take effect.
That is why raising a default (as #2315 did) had no effect on an existing install, and
it will happen again to the next default anyone changes.

### Immediate repair (one API call, no code change)

```
PUT /api/v1/config   scheduled.library_scan = {enabled: true, interval: 360}
```

⚠️ Owner call, not applied: a full library scan is expensive on this library (see the
open "prod scans take hours" item), so turning on a 6-hourly scan has a real cost.
Decide the interval before enabling.

### Durable fix — see the settings-architecture design

The repair above fixes one key on one host. It does not stop the next default from being
shadowed. The structural options are in
`docs/design/2026-08-12-unified-settings-architecture.md`.

### Make metadata fields on the book page clickable (future improvement)

Requested by the owner 2026-08-13: from a book's detail page you cannot click the
author's name to jump to the library filtered by that author. Every metadata field that
identifies a *set* of books should be a link into the library with that filter applied.

- [ ] **Author name → library filtered by that author.** The most-wanted one. The API
      already supports it: `/api/v1/audiobooks?author_id=<id>`, and the book payload
      already carries `author_id` plus an `authors[]` array with `id`, `name`, `role`
      and `position` — so a book with several contributors should link each one
      separately rather than only the primary.
- [ ] **Series name → library filtered by that series.** `series_id` is on the payload
      and `?series_id=` is supported. Worth pairing with `series_index` so the link can
      land on the right position in the series.
- [ ] **Narrator, publisher, genre, and release year.** Same idea, but check each has a
      real filter behind it before making it a link — a link that silently returns the
      whole library is worse than plain text. `library_state` and tags already have
      filter support and are good candidates.
- [ ] **⚠️ Do not link `version_group_id` to a filtered view until the filter works.**
      `?filter=version_group_id:X` and `?version_group_id=X` are both **silently
      ignored** today — they return the entire library (count=63,870) rather than
      erroring. Fixing that filter is tracked in
      `20260813-search-index-repair-prod-findings.md`; a "other versions of this book"
      link depends on it.

Notes for whoever picks this up:

- Prefer real `<a href>` navigation over an onClick handler so the links are
  middle-clickable, openable in a new tab, and shareable — a filtered library view is
  exactly the kind of thing someone pastes to someone else.
- The library page's filter state lives in `useLibraryQuery.ts`; the link target needs
  to set the same query parameters the page already reads, so that landing on the URL
  and clicking the filter in the UI produce identical results.
- Remember the page also applies `is_primary_version=true` by default. An author link
  that inherits that default will hide non-primary copies — which is correct for
  browsing, but worth being deliberate about rather than accidental.

## ✅ Six scheduled tasks were enabled but could never run

Found by the three-state startup diagnostic added in #2346, on the first production boot
that carried it (2026-08-13 02:45 UTC). Of 18 registered tasks: 5 had a working ticker, 7
were reachable through the nightly maintenance window, and **6 were enabled and dead**.

Four of the six declared `RunInMaintenanceWindow` but were **absent from
`maintenanceOrder`** in `NewTaskScheduler`. `internal/server/scheduler_maintenance_window_op.go:97`
iterates `MaintenanceOrder()` and only then checks `IsEnabled() && RunInMaintenanceWindow()`,
so a task missing from the list is unreachable no matter what the toggle says. This is the
*same* dead-config shape already documented in the `maintenanceOrder` comment for
`library_scan` — it recurred four more times because nothing checked.

| Task | Was | Now |
|---|---|---|
| `temp_file_cleanup` | declared window, not listed | listed with the cleanup cluster |
| `trash_cleanup` | declared window, not listed | listed with the cleanup cluster |
| `archive_sweep` | declared window, not listed | listed with the cleanup cluster |
| `library_organize` | declared window, not listed | listed late (mutates files, expensive) |
| `transcode` | `IsEnabled: true`, trigger fails by design | `IsEnabled: false` |
| `series_normalize` | `IsEnabled: true`, no timer, opts out of window | `IsEnabled: false` |

Three of these are retention/cleanup jobs that had never run once. **Correcting an
overstatement in the merged PR body and changelog**, which called all three "unbounded
on-disk leaks" — only one touches disk, and it is currently empty:

| Task | Operates on | Measured on prod 2026-08-13 |
|---|---|---|
| `temp_file_cleanup` | disk — walks `config.AppConfig.RootDir` for `*.tmp.m4b`/`*.tmp.m4a` (`sweep.CleanupOrphanedTempFiles`) | **0 files, 0 bytes** |
| `trash_cleanup` | database rows — `versions.CleanupTrashedVersions` | not measured |
| `archive_sweep` | database rows — `sweep.SweepArchivedBooks` | not measured |

The temp-file count was taken with `find` over the real root the op uses, so "0" is a
measurement, not an assumption. It does mean the disk-leak framing was wrong: whatever
these three were holding, it was not gigabytes of stranded audio. The two DB-side counts
are still unknown and will be reported by the first window run, which logs
`Trash cleanup: purged N versions` and `Archive sweep: cleaned N books`.

`transcode` and `series_normalize` are marked disabled rather than given tickers because
neither can usefully run unattended — `transcode`'s scheduled `TriggerFn` fails on purpose
without a `book_id`, and `series_normalize` moves files. `runTask` does not consult
`IsEnabled`, so manual/API invocation is unchanged; only the automatic paths and the
displayed state are affected.

### Recurrence guard

`internal/scheduler/task_reachability_test.go` asserts every registered task is
timer-driven, wired into `maintenanceOrder`, or explicitly disabled, plus two narrower
checks (no `maintenanceOrder` entry naming a non-existent task; no task claiming the window
while absent from the list). The invariant checks **wiring**, not configuration — it uses
`inMaintenanceOrder` rather than `reachableViaMaintenanceWindow`, because the latter reads
`config.AppConfig.Maintenance.*`, which is zero under test, and a structural invariant that
fails on an operator's config choice gets muted.

### Not claimed / still open

- **How much disk the three leaks are holding is NOT measured.** Nothing counted the
  orphaned temp files, expired trash, or over-retention archives on prod before this
  landed. Worth measuring on the first window run after deploy.
- Whether the window has time to reach the newly-appended entries is untested: the op
  breaks out of the loop when the window closes (01:00–04:00 on prod), and `library_organize`
  plus `library_scan` sit at the end behind everything else.
- `metadata_upgrade`, `library_size_refresh` and `library_organize` are wired but gated on
  `config.Maintenance.*` toggles whose production values were not checked.

## ✅ An empty `FieldFilter` value matched the WHOLE LIBRARY (fixed)

`fieldMatchesValue` ends in:

```go
return strings.Contains(strings.ToLower(bookValue), strings.ToLower(value))
```

`strings.Contains(anything, "")` is **always true** in Go, so a filter whose value is empty
constrains nothing. Measured live on prod 2026-08-13 (post-deploy build), and the filter is
otherwise healthy — this is specific to the empty value:

| `filters=[{"field":"title","value":X}]` | total |
|---|---|
| `X = ""` | **63,870 — the entire library** |
| `X = "zzqqxx"` | 0 |
| `X = "Skills"` | 25 |

### Why this was more than a confusing read

`FieldFilters` also flow into `Server.resolveFilterToBookIDs`
(`internal/server/metadata_ops.go:458`), which resolves a `FilterSpec` into concrete book
IDs **for background operations** with `limit=100000` — used by
`metadata_batch_candidates.go:59` and the bulk metadata fetch op. An empty value there
silently retargets a scoped job at the whole library. That is the **base64 op-params defect
(#2309) one level down**: same shape, same whole-library default, different entry point.

### Fixed in all three layers that were silent

1. **HTTP boundary** (`handlers/audiobooks/handler.go`) — 400 naming the offending field.
2. **Background-op path** (`metadata_ops.go`) — `resolveFilterToBookIDs` returns an error;
   params arrive already deserialized from the queue, so there is no HTTP boundary here.
3. **Matcher** (`service_filtering.go`) — `matchesFieldFilters` fails **closed** on an empty
   value. Matching nothing is visibly wrong and harmless; matching everything is invisibly
   wrong and, on the op path, destructive. `Negated` is deliberately not consulted —
   neither `f == ""` nor `f != ""` is a constraint anyone can have meant.

No in-repo code constructs an empty-value filter (checked: the list warmer's ~20
constructions all carry real values), so layer 3 only fires on input the boundary should
already have rejected.

### Not addressed

- **The frontend was not changed.** Whatever sends an empty value will now get a 400
  instead of the whole library. That is the intended, safe direction, but the UI path that
  produces it has not been traced — worth doing so the user sees a sensible message rather
  than a raw error.
- Whether any *stored* smart playlist or saved filter contains an empty value was not
  checked; such a filter now returns nothing instead of everything.

## Search: "keyword" fields are not exact-match at all

`bookIndexMapping`'s `keyword()` helper (`internal/search/bleve_index.go`) sets
`f.Analyzer = standard.Name`. Bleve's *standard* analyser is not a keyword
analyser — it tokenizes on unicode boundaries and **carries a stopword filter**.
So every field intended as exact-match is being tokenized and stopword-stripped:

- `genre`, `language`, `library_state`, `format`
- `tags` (array)
- `isbn10`, `isbn13`, `asin`
- `_type` — which is the document-type discriminator

Consequences to measure before fixing:

- A genre like `"Science Fiction"` is indexed as two terms, so a filter for it
  can match `"Fiction"` alone.
- Identifier fields (`isbn*`, `asin`) are case-folded and tokenized rather than
  stored verbatim; whether that breaks lookups depends on the query path, which
  has not been checked.
- `_type` is used for document routing. Worth confirming it still resolves
  correctly before changing anything.

The fix is `keyword.Name` (`analysis/analyzer/keyword`), which emits the input as
a single unanalyzed term.

**This requires a full re-index**, same as the stopword change — bump
`bookMappingVersion` and the existing recreate path handles it.

Deliberately NOT bundled with the stopword fix (2026-08-13): moving a second
axis of the mapping in the same rebuild would have made the mutation test for
the phrase behaviour ambiguous, and these fields need their own before/after
measurement on production rather than being carried along silently.

### Search / version-group census corrections (2026-08-13)

Measured by a full 63,870-book census against production, correcting the figures in
`docs/handoffs/2026-08-13-web-search-returns-unrelated-books.md` v2.0.0.

- [ ] **765 books, not 6,157, are wrongly hidden by the primary-version filter** (1.20%,
      not 9.6%). Breakdown: 724 sit in a version group where no member is primary; 41
      have no `version_group_id` at all and are still hidden. The other 22,266 unreachable
      books are legitimately collapsed duplicates whose group *does* elect a primary.
- [ ] **Find the writer that creates a `vg-` group without electing a primary.** The lead
      is good: 472 of 7,154 `vg-` groups have no primary versus 7 of 17,635 unprefixed —
      a ~166x enrichment. Note `vg-` groups are NOT mostly singletons (12,877 books across
      7,154 groups; 1,905 singletons), so a repair that assumes singleton-ness is unsafe.
- [ ] **`is_primary_version` in the payload disagrees with the filter for 2,776 books.**
      Books with no `version_group_id` are returned by `is_primary_version=true` while
      their own serialized field is **ABSENT** — not `false`. Nothing is hidden by this,
      but any client reading the field instead of calling the filter will disagree with
      the server about those books. It is why two independent counts of "primary books"
      differed (40,839 vs 35,108).

      *(Corrected 2026-08-23 against production, full-library page-through. **Two fixes
      to this entry.** (1) The field is **absent**, not `false`: `Book`/`BookSummary` tag
      it `json:",omitempty"`, so a nil `*bool` omits the KEY ENTIRELY rather than
      emitting `false` — which is a different bug with a different client-side fix, since
      a client reading `body.is_primary_version` gets `undefined`, not a wrong boolean.
      (2) The population is **2,776**, not 5,731 — it was 5,702 on 2026-08-14 and has
      roughly halved. The core claim is unchanged and now has an arithmetic proof:
      explicit-true is 37,613 (grouped) + 1,352 (ungrouped) = 38,965, and
      `?is_primary_version=true&show_quarantined=true` returns **41,741** =
      38,965 + 2,776, so the nils are provably inside the filter's answer.
      ⚠️ **PR #2805 changes this contract to serialize the effective value, i.e. absent
      → `true`. When it merges, this entry closes for clients but the underlying store
      divergence does NOT — see the `is_primary_version` divergence note; #2805 makes the
      API stop reporting it, not stop happening.)*
- [ ] **116 ungrouped books are hidden anyway** and do not fit the rule above.
      Unexplained.

      *(Re-measured 2026-08-23: **116**, not 41 — and it is GROWING. The 41 was a
      correct measurement on 2026-08-14; the same cross-tab nine days later gives 116,
      while the library itself shrank by 7,112 books. This is no longer a "small
      concrete sample" — it nearly tripled and warrants finding the writer. The census
      tool that produces it landed in #2809: `tools/cmd/orphan-nonprimary-census`,
      which emits the per-book CSV with `created_at`/`updated_at` for correlating
      against job runs. Verified by three independent instruments (different paging
      param, different termination condition, different population), 0 duplicate ids
      across 56,727 rows.)*
- [ ] **`version_group_id` is silently ignored as a filter** on `/api/v1/audiobooks` —
      both `?filter=version_group_id:X` and `?version_group_id=X` return the entire
      library (count=63,870) rather than erroring. Same silent-filter family as the bare
      query-parameter rejection in ab04824e. This is what forced a full census instead of
      a targeted group lookup.

- [ ] **Decide whether to force a search-index rebuild on prod.** The boot-time
      coverage check (`internal/server/search_coverage.go`) repairs the gap on the
      next restart by marking ~40K books dirty and letting the reconciler drain
      them (~5,000/tick, 30s ticks). That is a large background operation on a
      live server. Owner call: let it happen on the next natural restart, or
      schedule it. Measured gap 2026-08-13: books created 2026-08 were 2%
      searchable (1 found / 50 missing in sample), 2026-04 were 97% (38/1).
- [ ] **`all` and `and` are stopwords and are silently dropped from queries.**
      `dropStopwordOnlyConjuncts` (`internal/search/bleve_translator.go:150`)
      strips conjuncts that analyse to zero tokens — it exists to fix "shards of
      oblivion" returning nothing. Measured in the query JSON emitted by
      `TestReproAllJobsAndClasses`: `All Jobs and Classes` searches only
      `Jobs AND Classes`, and `all jobs` searches only `jobs`. The user is given
      no indication half the query was discarded. Independent of the index-coverage
      bug fixed on 2026-08-13; needs its own change.
- [ ] **Quoted phrases do not produce a `MatchPhraseQuery`.** The server-side
      parser never strips the quote characters, so `"All Jobs and Classes"`
      becomes the terms `All` and `Classes"` — closing quote glued to the final
      token. Confirmed in the same emitted query JSON. The translator's
      `n.Quoted` branch (`bleve_translator.go:317`) works; it simply never fires.
      It *appears* to work only because the English analyzer discards the quote as
      punctuation. Phrase search is not doing what the UI help text implies.
- [ ] **`SearchIndexDroppedCount` is not actually exposed on `/metrics`.** The
      comment in `internal/server/search_reconciler.go` says it is "Exposed for
      the metrics endpoint and for tests", but a live scrape of prod `/metrics`
      on 2026-08-13 returned 100 metric families and none matching
      `search`/`dirty`. Same declared-but-not-registered shape as the
      `maintenanceOrder` defect (#2360). Add the drop counter and the dirty-set
      backlog so the next divergence is visible without grepping journald.
- [ ] **A one-book version group can have no primary member.**
      `01KXXVBGQGH6PEP9WE0ZWHBJ50` ("All Jobs and Classes! Book II") is the sole
      member of `vg-01KXXVBGMHPATT8X1X3DV5AW2Q` and has
      `is_primary_version=false`, so it is invisible in the default Library view
      (which filters to primary versions) no matter what the search index says.
      Worth a sweep for other headless groups plus a repair, since a group with
      no primary is unreachable by design rather than by accident.

### Search-index coverage repair — production findings (2026-08-13)

The repair from #2381 ran on prod at 19:20 EDT. Measured, not estimated:

```
search index is short of the library; marking books for reconciliation
    indexed=51086  books=67824  shortfall=16738
```

**16,738 books (24.7% of the library) were missing from the search index** and
unfindable by any web search. The drain ran clean — `removed=0 failed=0` on every
batch, ~5,000 per ~2.5 min.

Follow-ups this surfaced:

- [ ] **The store reports 67,824 live books; the API list endpoint reports 63,870.**
      A 3,954-book gap. `ListBookIDs` already excludes `MarkedForDeletion`, so these
      are live rows, and `/api/v1/audiobooks` applies no default filter when
      `library_state` is empty. Paging the endpoint returns exactly 63,870 distinct
      ids, so it is internally consistent — it simply never serves those 3,954.
      **Cause not established.** Worth confirming whether they are genuinely
      unreachable to clients or an artifact of how the total is derived; if the
      former, it is a third invisible-books population, larger than the 765 in
      `20260813-primary-version-census-corrections.md` and unrelated to it.
- [ ] **The coverage gate compares two slightly different populations.**
      `reconcileSearchIndexCoverage` tests `len(ListBookIDs()) <= DocCount()`.
      `ListBookIDs` excludes deletion-marked books; `DocCount` counts whatever Bleve
      holds, which can include docs for books since deleted. If stale docs ever
      accumulate, the comparison can read "not short" while real books are missing —
      the same "one comparison cannot distinguish two states" shape as the bug the
      gate was written to catch. Deletes do flow through `DeleteIndexedBook` from both
      the index worker and the reconciler, so this is currently latent, not active.
      Consider comparing sets rather than counts, or logging both numbers on every
      boot (the `indexed=`/`books=` pair already does this — keep it).
- [ ] **The search index has ZERO metrics.** A live `/metrics` scrape returns 50 metric
      families and **not one** mentions search, bleve, index, or dirty. This is the
      direct reason a quarter of the library was unfindable for an unknown period with
      nobody noticing — there was no signal to notice. `audiobook_organizer_books_total`
      already exists, so **half the comparison is exported already**; adding a
      `search_index_docs_total` gauge (and a dirty-backlog gauge) would have made this
      bug a visible divergence on a graph rather than a user report. Note this also
      re-confirms the earlier finding that `SearchIndexDroppedCount` is not on
      `/metrics` despite a comment saying it is, and extends it: nothing about the index
      is exported at all.
- [ ] **`audiobook_organizer_books_total` reports the PRIMARY count, not the total.**
      It is fed by `CountPrimaryBooks()` (`server_lifecycle.go:393`) while its help text
      reads *"Current total number of books in library"*. Live value **40,841** against
      **67,824** live books in the store — under-reporting the library by ~40%. Either
      rename/reword it or add a true total alongside; any dashboard built on it is
      currently wrong about the library size.
- [ ] **Re-measure the per-cohort coverage now that a true figure exists.** The
      earlier 2%-of-August / 97%-of-April figures were *sampled* (n=51 and n=39
      decided) and pointed the right direction but understated the total: 16,738 is
      more than a single month's intake, so the gap spans wider than the August
      cohort alone. Treat the sampled percentages as superseded.

### Search follow-ups from the wildcard/phrase fix (2026-08-13)

- [x] **Phrases containing an English stopword still over-match.** Fixed in #2391,
      deployed and verified on production 2026-08-13 22:03 EDT: `"All Jobs"` went from
      **300 results to 3**, all three the intended book. The cause was subtler than
      "the stopword is dropped": the stop filter removes tokens *without renumbering
      the positions of the survivors*, and `MatchPhraseQuery` rebuilds the phrase from
      those positions. So `"All Jobs"` became a **one-slot** phrase (a bare term query)
      while `"Lord of the Rings"` became a **four-slot phrase with two nil slots** —
      wildcards matching "Lord ANY ANY Rings". Text fields now use a
      stopword-preserving analyser, with an index mapping-version marker that triggers
      the rebuild. The re-index ran in ~36 min over 67,824 books, `failed=0` on every
      batch. `TestQuotedPhraseWithLeadingStopword` was replaced by
      `TestQuotedPhraseWithStopword`, which asserts both cases with word-order decoys.
- [ ] **Fuzzy queries (`~`) have the same case-sensitivity defect the wildcard fix just
      addressed.** `bleve_translator.go` builds `NewFuzzyQuery` from the raw term, and
      FuzzyQuery bypasses the analyser exactly as PrefixQuery and WildcardQuery do. Not
      fixed here because the report was specifically about `*` and expanding the change
      silently would make both harder to review. The fix is the same one-line
      `patternTerm()` call already in the file.

- [ ] **A04's designated probe cannot verify the op-ID audit trail.**
      `maintenance.temp-file-cleanup` routes through
      `sweep.CleanupOrphanedTempFiles`, which records each deletion via
      `activity.LogBatch` — the ACTIVITY feed — and never calls
      `CreateOperationChange`. So even a run that deletes files writes zero
      `operation_changes` rows, and the 2026-08-14 probe (which found 0
      orphans anyway) was doubly inconclusive. To verify #2414 on prod, pick
      a mutating op whose write path actually calls `CreateOperationChange`
      (C513's list of the 8 `ctxOpID` consumers is the menu — read the branch
      first), or decide temp-file deletions SHOULD be operation_changes and
      wire it, making the safest probe also a valid one.

- [ ] **Activity log: auto-compact after 7 days, user-configurable.** Owner
      request (2026-08-14): the activity log grows into a mess — compact
      (prune/roll up) entries older than a retention window automatically,
      defaulting to 7 days, with the retention period exposed as a setting on
      the activity log screen itself (not buried in general settings). Notes:
      `maintenance.cleanup-activity-log` already exists with a midnight-daily
      schedule — the work is wiring a `activity_log_retention_days` config
      key (default 7, 0 = never) into it rather than a new job, plus the
      settings control on the ActivityLog page and a line in the log header
      showing the active retention ("entries older than 7 days are compacted
      automatically"). Follow the config rules: absent key keeps the shipped
      default (#2350 class), and the stored-zeros-shadow-defaults design
      (D111 fragment) applies to the 0=never sentinel.

- [ ] **Activity-log summaries drop their data — "cover art saved to" (to
      WHERE?), "ISBN enrichment succeeded for" (for WHAT?).** Owner
      screenshot 2026-08-14 18:03. Root cause located: these are slog calls
      whose sentence is in the MESSAGE and whose data is in ATTRS —
      `internal/metafetch/service_apply.go:611`
      `slog.Info("cover art saved to", "path", coverPath)`,
      `service_fetch.go:37` ("ISBN enrichment succeeded for", "id"),
      `service_fetch.go:292` — and the slog→activity bridge keeps only the
      message. A neighboring row ("ISBN enrichment found" isbn=… title=…
      with a stray quote) shows the OPPOSITE bridge behavior: a raw slog
      TextHandler line, quotes and all, pasted into the summary — so two
      inconsistent bridges exist. Fix: one bridge that renders attrs into
      the summary (book title resolved from id where present), and sweep
      metafetch's sentence-shaped slog messages onto it.

- [ ] **Metadata-apply activity rows don't NAME the book.** Same screenshot:
      "Applied narrator: Alex Kozlowski → Grant Cartwright" with a bare
      "book →" link — the summary must lead with the book title ("The
      Whispering Night: applied narrator …"); a link target is not a
      summary. Also "Applied audiobook_release_year: → 2021" renders an
      empty FROM value as a dangling arrow — show "(none) → 2021".

### Author delete paths guard with the listing counter, same shape as the series bug

- [ ] **`BulkDeleteAuthors` and `DeleteEmptyAuthor` decide "is this author empty?"
  with `GetBooksByAuthorIDCore`**, which `internal/database/memdb_reads.go:529`
  documents as a *listing* view — it applies the primary-version filter and
  returns only live books. The repo already knows this is the wrong getter for
  this class of caller; the comment on `GetBooksByAuthorIDAllVersions` says so
  explicitly:

  > `GetBooksByAuthorIDWithRoleCore` is what merges and deletes consult to find
  > the links they must rewrite before removing an author. For that caller a
  > missed link is data loss — the author gets deleted and the junction row is
  > left pointing at a row that no longer exists.

  The delete handlers are exactly that caller and still use the listing getter,
  so an author whose only books are trashed or non-primary counts as zero and is
  deletable, stranding those books and their `book_authors` junction rows.

  This is the author-side twin of the series bug fixed in #2400 (weekly prune)
  and the UI delete paths. It was NOT fixed alongside them because the series fix
  could reuse `SeriesBookRefStore`/`GetAllSeriesBookRefCounts`, and no author
  equivalent exists — the fix needs a `GetAllAuthorBookRefCounts` that counts
  `Book.AuthorID` **and** `book_authors` junction rows in every book state,
  across both the memdb and Pebble implementations, with a conformance test
  (see `internal/database/author_getter_conformance_test.go`, and the
  memdb-vs-Pebble divergence it already caught: 86 links warm vs 84 cold).

  Until then the risk is live but small — it needs a user to bulk-delete authors
  from the UI. Not a background job, so nothing is accumulating on its own.

## Author table: copyright text and HTML entities leaked into artist tags

Found while fixing the stranded-`&` author rows (`dedup.NormalizeAuthorName`,
2026-08-14). These are a **different** defect and were deliberately left alone —
the leading-conjunction strip requires trailing whitespace precisely so it does
not mangle them.

- [ ] Author id 46583 is named `&#169` — an HTML entity for `©` with its
      trailing `;` already lost somewhere upstream. 1 book attached.
- [ ] Author id 51870 is named `&#169;2013 by HarperCollinsPublishers` — a whole
      copyright line stored as an author. 0 books attached, so it can likely just
      be deleted.
- [ ] Find where the entity loses its `;`. `SplitCompositeAuthorName`'s semicolon
      branch splits `&#169;2013 by HarperCollinsPublishers` into `["&#169",
      "2013 by HarperCollinsPublishers"]` but then discards the result because
      `&#169` has no space and only one part survives — so the branch returns
      nothing and is *not* the culprit. The truncation happens somewhere else.
- [ ] Decide whether author-name ingest should HTML-unescape at all. If it should,
      `html.UnescapeString` belongs at the same chokepoint, but note it would turn
      `&#169;2013 by HarperCollinsPublishers` into `©2013 by
      HarperCollinsPublishers` — still not an author, so entity decoding alone
      does not fix the real problem, which is copyright text in an artist tag.
- [ ] Consider a `isDirtyAuthorName` rule for names starting with `©`/`&#`/a
      4-digit year, so these are rejected at creation instead of repaired later.

## Author table: book titles are being comma-split into author rows

Also found on 2026-08-14, and deliberately excluded from the leading-conjunction
data repair. Three rows begin with `and ` but are **not** stranded conjunctions
from a credit list — they are fragments of book titles that reached the artist
tag and were then split on the comma:

- [ ] id 46595 `and Thanks for All the Fish` (2 books) — from *So Long, and
      Thanks for All the Fish*
- [ ] id 46989 `and the Farm Boy (DBY)` (5 books)
- [ ] id 47193 `and Make Better Decisions` (16 books)

Stripping the leading `and` from these produces `Thanks for All the Fish`, which
is still not an author — it just stops *looking* broken. The repair op therefore
matches `&` only, and these three are left visibly wrong on purpose.

- [ ] The real defect is that `SplitCompositeAuthorName`'s comma branch has no
      notion of person-vs-title: its only per-part test is "contains a space".
      A title clause passes as readily as a name.
- [ ] Consider requiring a part to look like a personal name (2-4 words, no
      leading lowercase function word, no trailing parenthetical like `(DBY)`)
      before accepting a comma split, or refusing to split when the source
      string also carries title-ish punctuation.
- [ ] Check how many other author rows are title fragments without the `and`
      giveaway — the 57 rows beginning with `-` are the next place to look.

## Author table: misspelling shared by both rows of a duplicate pair

- [ ] `Sylverster McCoy` (2 books) and `& Sylverster McCoy` (1 book) are *both*
      misspelled — the actor is Sylvester McCoy. Merging the `&` row into its twin
      leaves the misspelling intact in the survivor. Worth a targeted rename after
      the conjunction repair lands.

## B06 chapters end-to-end: VERIFIED on prod — E02 backfill only needs the residue

Measured against the LIVE ABS surface on production 2026-08-14 (ABS API is
enabled and serving at root: `/ping`, `/api/libraries`, `/api/items/:id`;
library `b5e3a5b2…`, 34,513 items):

- **Multi-file synthesis works.** 28-file book (Mutineer): 28 synthesized
  chapters, offsets contiguous, `last end == media.duration` exactly
  (103,747s), titles from embedded track titles.
- **Stored chapters ARE being served — and they are widespread.** 21 of 29
  sampled single-file items return >1 chapter (37, 85, 105 …), which for a
  single file can only come from the chapters store. Timeline sanity-checked
  on one (37 chapters, contiguous, last end 28,885.1 vs duration 28,885).
  The "backfill has NOT run library-wide so most books have no stored
  chapters" premise is stale — scan-time extraction has already covered
  ~72% of the sampled single-file population.
- **Graceful absence works.** Single-file item with no stored chapters
  serves exactly one whole-book chapter (0 → duration).

**E02 implication:** the chapters-backfill run is a RESIDUE job (~28% of
single-file books in the sample), not a whole-library one. Run it dry-run
first to size the real target set; the serving path needs no changes.

- [ ] **`bulk-write-back` cannot do the approved E08 library-wide run as-is.**
      Canary (100 books, op `01M00PGZKA0KBMPTZMAJTEKPD5`, 2026-08-14) measured
      ~35 s/book, strictly serial, and 23/23 processed→written — it rewrites
      tags unconditionally instead of skipping files whose tags already match
      the DB. Library-wide (~40K organizer-tree books) is weeks, not the
      approved nightly window. Before the full run: (1) add a tag-diff skip
      (probe ≈1 s vs rewrite ≈35 s; only mismatched files rewritten — also
      turns the op into a usable "how many books actually have stale tags"
      census); (2) bounded worker pool inside the op per the concurrency
      mandate (`RunBulkWriteBack`,
      `internal/server/server_maintenance_deps.go:44`) — the ConcurrencyKey
      serializes whole ops, so chunk-parallelism across ops is not available.
      Owner approval for the full run already given (2026-08-14); only these
      prerequisites block it.

## C111 census: the nil `is_primary_version` population is 5,702 (all ungrouped) — "22,552 nils" was a misread

Full-library page-through on production 2026-08-14 (63,839 rows, `omitempty`
distinguishes nil from false), cross-tabbed against `version_group_id`:

| flag  | has VG | count  |
|-------|--------|--------|
| true  | yes    | 35,586 |
| false | yes    | 22,510 |
| false | **no** | **41** |
| nil   | no     | 5,702  |
| nil   | yes    | 0      |
| true  | no     | 0      |

- **The structure is clean:** every version-grouped book carries an explicit
  flag; every nil book is a groupless singleton. The long-quoted "22,552 nil
  books" was actually the explicit-FALSE population (22,510+41 ≈ today's
  22,551 `is_primary_version=false` count).
- **The index path counts nil as TRUE** — proven by arithmetic, not code
  reading: `is_primary_version=true` answers 41,288 = 35,586 explicit-true
  + 5,702 nil.
- **Correction to `20260814-c716-api-store-gap-resolved.md`:** the
  `show_quarantined=true` bug drops the 22,551 **explicit-false** books
  (it silently applies primary=true when the filter is unset), NOT the nils.
- **The 41 false/no-VG books are C314's exact population** — ungrouped books
  stuck at explicit false, invisible in every primary-only view, electable
  to primary trivially (they have no group to conflict with).

**D-2 semantic the data recommends: nil = true** (an ungrouped book is its
own primary). Unify:
- [ ] Make every raw `*bool` post-filter treat nil as true (matching
      `effectiveBoolFieldIndex{Default: true}`), or
- [ ] better: backfill explicit `true` onto the 5,702 nil rows (dry-run
      gated) so nil ceases to exist, then make nil a validation error at
      write time.
- [ ] Fix the 41 ungrouped-false rows to true in the same op (C314).
- [ ] Re-run this census as the post-fix verification: expected end state is
      exactly two populations (true, false+VG).

## C716 resolved: the "3,954-book API-vs-store gap" decomposes to 3,953 instrument + 2 quarantined + 0 unexplained

Measured on production 2026-08-14 (~10:30 EDT), every instrument controlled:

- **3,953 of the gap was the measuring instrument.** The 67,824 "live" store
  count (2026-08-13, search reconciler log) came from the leaky Pebble
  `ListBookIDs` that still counted soft-deleted books — the drift fixed on
  main (264585b5, PR #2408). The 10:01 post-fix boot logs now say
  `books=63871` (search coverage) and `totalBooks=63871` (iTunes PID
  backfill — a second, independent caller). 63,871 + 3,953 (trash set,
  re-verified intact via `/audiobooks/soft-deleted` total) = 67,824 exactly.
- **2 books remain store-visible but list-invisible, and both are
  QUARANTINED** (`quarantine_reason: "taglib permanently unreadable after
  transcode attempt"`, quarantined 2026-04-24):
  `01KNDC17RY2ATJFRACA50N9AMJ`, `01KNDC4VB60GTBQ137A0YJ29KX`.
  The default list applies `ExcludeQuarantined` unless
  `?show_quarantined=true` (`audiobooks_helpers.go` /
  `buildAudiobookListResponse`) — a hidden-but-intentional default filter.
  Note the **inconsistent visibility**: both books are still served by
  direct GET `/audiobooks/:id` AND by `/authors/:id/books` (author path does
  not exclude quarantined). Decide whether that asymmetry is wanted.
- **The API list is internally consistent**: full page-through returned
  63,869 rows, 63,869 distinct ids, zero duplicates; `count` stayed 63,869
  at an off-the-end offset (control); metadata-export (`GetAllBooksCore`,
  independent store path) returned 63,871 with the two quarantined ids as
  the exact set difference.

Follow-up bugs found by the controls (route to C1/C3, do NOT fix here):

- [ ] **`show_quarantined=true` SHRINKS the list.** A flag that can only
      widen the set returned 41,319 books against the default 63,869.
      41,319 = 41,317 (`is_primary_version=true` count) + the 2 quarantined
      — i.e. with the quarantine exclusion off and no explicit
      `is_primary_version` param, the scan path serves only primary-flagged
      books and silently drops the 22,552-book nil-flag population. Same
      family as the filed nil/false `is_primary_version` divergence
      (`effectiveBoolFieldIndex{Default:true}` vs raw `*bool`); this is a
      second concrete symptom, on the main list path. With an explicit
      `is_primary_version=false` the flag behaves (22,552 with or without
      quarantine).
- [ ] `is_primary_version=false` answers 22,552 — exactly the known
      nil-flag population size. Establish whether explicit-false books are
      currently 0 in prod or whether the false-filter is returning nils
      (memory says the census counted ~765 explicit-false in one path).

- [ ] **CA12 wave 2: model `logging.Sanitize`/`SanitizeErr`/`logger.sanitizeLogLine`
      as CodeQL log-injection sanitizers via the model pack.** #2445 removed
      the fast-path bypass, but the conduit's own alerts
      (`internal/logging/structured.go:51/58/65`) are STILL open at 316 total:
      taint through the variadic `args ...any` slice survives because CodeQL
      treats per-element slice writes as weak updates — no code shape fixes
      that. The repo already has the mechanism
      (`.github/codeql/models/*.model.yml`, `pathInjectionSanitizer` rows) —
      BEFORE adding rows, verify the extensible predicate name for log
      injection actually exists in the pinned `codeql/go-all` (an unknown
      `extensible:` fails the pack). If none exists, the alternatives are a
      custom .ql suite or bulk dismissal-with-justification of
      conduit-routed alerts. Baseline to beat: 316 open go/log-injection.

## 56 duplicate-name author groups, and most of them are not authors

Found 2026-08-14 while checking whether the stranded-ampersand repair had left
duplicates behind. It had not — all 16 repaired names resolve to exactly one row
— but enumerating all 9,320 authors to prove that surfaced a separate problem.

**49 exact-duplicate name groups, 56 once case and whitespace are normalized.**

They fall into three kinds, and they want different fixes:

### 1. Book titles stored as authors

| name | rows |
|---|---|
| `Cthulhu Armageddon (Unabridged)` | **25** |
| `1 Ο Χάρι Πότερ και η Φιλοσοφική Λίθος` | 4 |
| `05_Rise of the Corinari` | 3 |
| `Sorcerer Ascendant` | 3 |
| `"Mind's Eye"` | 3 |

### 2. Disc labels stored as authors

`CD 13` ×10, `CD 06` ×10, `CD 05` ×7, `CD 15` ×5, `CD 18` ×4.

Both of these are the same disease as the `& Name` rows — a metadata parse
writing a non-author string into the author field — just a different input
shape. `-Dickens Short Stories` ×3 is the leading-delimiter variant, matching
the `- 3` / `- Legion` junk seen in the ABS series listing the same day.

### 3. Real authors, genuinely duplicated

| name | rows (id, book_count) |
|---|---|
| `Karen Joy Fowler` | 44479(1) 44480(0) 44481(1) 44482(1) 44483(1) 44484(3) |
| `Valery Starsky` | 46007(1) 46008(1) 46009(1) 46010(27) |
| `Raymond L. Weil` | 40775(39) 42117(27) **45616(0) spelled `Raymond  L.  Weil`** |
| `Time Pebbles` | 43574(0) 43575(0) 43576(29) |

`Raymond L. Weil` is the instructive one: two legitimate rows PLUS a third whose
only difference is doubled internal whitespace. Author matching is not
normalizing whitespace, so the dedupe that should have caught it never fires.

- [ ] Normalize whitespace (and probably case) in author lookup/creation, so a
      `Raymond  L.  Weil` can never be minted alongside `Raymond L. Weil`.
      ⚠️ Check `util.NormalizeAuthor` first — it is already used for the series
      name index (`pebble_store_series.go`), so the helper may exist and simply
      not be applied on the author path.
- [ ] Merge the type-3 real-author duplicates. The existing
      `maintenance.author-*` ops already know how to relink via the join slice —
      see `author_conjunction_repair.go`'s `mergeAuthorInto`, which handles the
      BookAuthor rewrite and the AuthorID hydration correctly.
- [ ] Decide what to DO with types 1 and 2 rather than merging them. Merging 25
      `Cthulhu Armageddon (Unabridged)` rows into one still leaves a book title
      masquerading as an author. These need the books re-parsed, or the rows
      retired and the books re-attributed — a different operation from dedupe.
- [ ] 🚨 Do NOT write a single op that treats all three kinds the same. Type 3
      wants a merge; types 1 and 2 want the author link removed entirely. An op
      that merges everything would consolidate the junk and make it look
      intentional — the laundering failure mode recorded in
      `feedback_stripping_without_corroboration_is_laundering`.

**Counts to re-measure before acting** — these are from the 2026-08-14 07:50
snapshot of a 9,320-row author table, taken via `/api/v1/authors` paged by
`limit`/`offset` (note: `page` is not a parameter this endpoint accepts).

## F110 measured: playlist-item PID coverage is 88.5% via ExternalIDMapping — F111 is GO

Measured on production 2026-08-14. Two instruments, very different answers —
the brief named the wrong one:

- **The RIGHT instrument — `ExternalIDMapping` (track-PID → book), i.e.
  `GetBookByExternalID("itunes", pid)`:** this morning's post-#2367 boot
  backfill completed `tracks_processed=97981 registered=86732` — **88.5% of
  all XML tracks** now resolve to a book at track level. This is the lookup
  F111's importer must use.
- **The instrument the F110 brief named — `GetBookByITunesPersistentID`
  (album-level `Book.ITunesPersistentID`):** only 13,128 books carry the
  field (API `/api/v1/itunes/books` page-through = boot log exactly), and
  only 14.0% of the 84,296 distinct user-playlist PIDs resolve through it.
  **Do not use it for playlist import** — it answers a different question.
- Current XML (`iTunes Library.xml`, 160MB, 2026-07-19): 269 user playlists
  carry materialized `Playlist Items`, 98,184 refs. Under even the weak
  album-PID instrument the distribution is bimodal: **124 playlists at 100%
  coverage**, 96 at 1–49%, 13 at 0% — the mapping instrument strictly
  improves on this.

**Verdict: coverage does NOT moot the recovery paths (the F110 gate
question). F111 (static-snapshot import by PID) is GO** once the owner
green-lights the run — resolve via `GetBookByExternalID("itunes", pid)`,
import as idempotent one-shot maintenance op, verify by re-reading the DB.
Exact per-playlist import counts come from that run's report.

## `is_primary_version` means different things on the library path and the author path

Found 2026-08-14 while trying to verify an author merge. Both paths accept the
same parameter and answer confidently; they disagree about what a NIL flag
means, so the same book is primary on one path and non-primary on the other.

### Measured on production 2026-08-14

Library-wide the filter partitions the library exactly, and the rows it returns
carry an explicit `false`:

    is_primary_version=false  -> total=22552   rows have is_primary_version: false
    is_primary_version=true   -> total=41317
    (no filter)               -> total=63869   = 22552 + 41317 ✓

On the author path it returns rows whose flag is **null**, not false:

    author_id=38542&is_primary_version=false -> 1 row, is_primary_version: null
    author_id=38543&is_primary_version=false -> 1 row, is_primary_version: null

And it cannot return a book whose flag is explicitly `false`. Book
`01KNDB8NWHXV2DKRQESBA9SDRA` records `author_id: 42623`, `is_primary_version:
false`. Yet:

    author_id=42623                          -> 1 row, and it is NOT that book
    author_id=42623&is_primary_version=false -> 0 rows
    author_id=42623&is_primary_version=true  -> 1 row (a different book)

So a book that exists, names that author, and is explicitly non-primary is
unreachable through its own author's listing under every value of the filter.

### Likely cause

`memdb_schema.go` builds the index with `effectiveBoolFieldIndex{Default:
true}` — a nil `IsPrimaryVersion` indexes as **true**. A post-filter comparing
the raw `*bool` instead sees nil as "not true" and treats it as **false**. Same
nil, opposite readings, depending on which layer answers.

That also explains the shape of the disagreement: the author path is
primary-only by design (`GetBooksByAuthorIDCore` is the LISTING view — see
#2410), so an explicitly-`false` book is dropped before the parameter is ever
consulted, while a nil-flag book survives the index (nil→true) and is then
handed to a post-filter that calls it false.

- [ ] Decide the single meaning of a nil `IsPrimaryVersion` and apply it in both
      places. `Default: true` is already the storage answer, so the post-filter
      is the side that should change — but confirm before flipping, because
      22,552 books currently answer to `false` library-wide and some of those
      may be nil-flagged.
- [ ] Add a conformance test in the shape used by #2406/#2410/#2411: one
      fixture containing a nil-flag book, an explicit-true book and an
      explicit-false book; assert the library path and the author path classify
      all three identically. A fixture without a nil-flag row cannot catch this.
- [ ] Decide whether the author listing SHOULD expose non-primary books at all.
      Today it cannot, which is defensible for a listing, but it means the UI
      has no way to show a book on the author page it is genuinely attached to.

⚠️ **This is why the 46627 merge could not be verified** — see the handoff. Every
available instrument for "which books does author X have" is either
primary-only or disagrees about nil, so `0 non-primary books for 43791` is not
evidence the merge failed. Do not read it as such.

## PERF-4 is still live: iTunes search calls `SearchBooks(search, 0, 0)`

Re-verified 2026-08-14 while adding the status column to the 2026-06-22
security sweep: `internal/server/handlers/itunes.go:709` still calls
`h.store.SearchBooks(search, 0, 0)` — the exact call the audit flagged as
returning no rows (its cited mechanism: the store treats limit 0 as "nothing",
`pebble_store.go` search path).

- [ ] First MEASURE, don't assume: confirm what `SearchBooks(q, 0, 0)` returns
      today (the store may have changed limit-0 semantics since June). A
      bogus-value + known-good-value probe against a seeded store settles it in
      one test.
- [ ] If it returns nothing: the iTunes search surface has been silently empty
      — fix with a bounded call (or route through Bleve IDs + iTunes filter,
      as the audit suggested), and add the value-asserting test that would
      have caught a filter answering nothing.
- [ ] If it returns everything: that is the opposite failure (unbounded
      materialization on a handler path) and wants a limit anyway.

- [ ] **Legacy operation rows never leave "pending" — the ops UI shows
      finished jobs as running for hours.** Twice on 2026-08-14 this misled
      the operator: the composer scan showed progress 0 while 3h into real
      work, and the E02 chapters dry-run showed as an active 1.5-hour task
      when it had finished at 17:57 with logged results. A
      `GET /api/v1/operations?limit=20` dump shows EVERY maintenance-job row
      of the day stuck at status "pending" — including `fix-file-modes` and
      `normalize-primary-flags`, which completed with journaled summaries.
      The v2 registry rows complete correctly; it is the LEGACY op row
      (`maintenance:<job>` type, created for jobs dispatched via
      `maintenance.job`) whose status/progress is write-only after creation.
      Fix: on v2 op completion, propagate terminal status (+ final
      progress/message) onto the paired LegacyOpID row; backfill-repair the
      day's stuck rows; and the ops UI should render v2 state when a legacy
      row has a live v2 twin. Note the C510 opstate sweep treats unknown
      statuses as KEEP — stuck-pending rows also pin their opstate blobs
      forever, so this defect quietly defeats that retention too.

- [ ] **Check GitHub CI on the merge commit.** Merged on an explicit instruction
      to skip the CI *wait*, so no GitHub result was read. Local verification was
      complete and green: `go build ./...` exit 0, `go vet ./internal/server/...`
      exit 0, `gofmt` clean, and `go test ./internal/server/...
      ./internal/maintenance/... ./internal/operations/... -short -race -count=1`
      → **exit 0, 19/19 packages ok, zero failures** (`internal/server` 898s).
      Plus four independent mutations each killing a distinct test. Only the
      GitHub-side result (lint, frontend, changelog-check) is unread.

- [ ] **`opstate:<id>:params` keys are never swept.** `runMaintenanceJob` now
      persists a small params blob (~90 bytes) per maintenance run so a restart
      can resume faithfully. `DeleteOperationState` clears both `opstate:<id>`
      and `opstate:<id>:params`, but only two of the 34 jobs
      (`recompute-book-aggregates`, `backfill-file-hashes`) call
      `operations.ClearState` on clean completion — the other 32 leave the key
      behind forever. There is no retention sweep for the `opstate:` prefix
      (grep confirms the only writers/deleters are in
      `internal/database/pebble_store_operations.go`). Growth is small but
      unbounded; either add an `opstate:` sweep to `retention-and-hygiene` or
      have `maintenance.job`'s Run clear params when the job finishes.

- [ ] **Verify the dry-run default on prod after deploy.** `GET
      /api/v1/maintenance/jobs` publishes `default_params`; POST a job that
      advertises `dry_run:true` with no body and confirm the run reports a
      preview rather than applying. Safest probe: `scan-composer-tags` (scan
      only). Do **not** probe with `cleanup-series` or `cleanup-empty-folders`.

- [ ] **`dedup.series-dedup` still has no dry-run parameter at all.**
      `internal/dedup/series_dedup.go:266` `DedupSeries` applies on every
      invocation, and its merge loop reassigns books via the *listing* getter
      `GetBooksBySeriesIDCore` (which filters trashed and non-primary rows)
      before calling `DeleteSeries` unconditionally — the mechanism that strands
      books on a deleted series ID. It has never run in production (0 of 10,161
      operations), so there is no existing damage; it is a latent hazard only.
      Give it a dry-run parameter and switch it to the all-versions getter
      before anything wires it to a trigger.

- [ ] **Consider making the resume path's fallback observable.** When no saved
      params exist, `resumeLegacyOp` now logs at info and resumes with the
      advertised default. Once the pre-change operations have aged out, that log
      line firing at all means something failed to save — worth a metric rather
      than a log grep.

- [ ] **Metadata matcher: shift-click range selection.** Owner request
      (2026-08-14): clicking one row then shift-clicking another should select
      every row between them, like a file manager. Track the anchor index of
      the last plain click; on shift-click select the inclusive range
      (respecting current sort/filter order). Frontend-only.

- [ ] **Metadata matcher: "skip all" / hide-multiples control.** Owner request
      (2026-08-14): multi-match groups need a way to be hidden in bulk —
      a "skip all" that stashes them for a later pass — so a triage session
      can clear the unambiguous rows without wading through the multiples
      every time. Persist the skip set (per user or per session) so hidden
      groups come back on demand, not on reload.

- [ ] **Metadata matcher: apply falsely reports "signed out — no changes were
      made" after a long write.** Owner observed (2026-08-14): with write-to-
      files enabled, a multi-file apply blocks past the auth/session timeout;
      the UI then reports a sign-out AND claims no changes were made — but
      the writes had clearly happened. Two defects: (1) the result message is
      dishonest — never claim "no changes" from a timeout, report "connection
      lost, operation may still be running" and re-query; (2) the root fix is
      the background-job dispatch already filed in
      `20260814-matcher-writeback-background-job.md` — an op id returned
      immediately makes the timeout impossible and the ops screen owns
      progress/results.

- [ ] **Metadata matcher: multi-file write-to-files must dispatch as a
      background operation.** Owner request (2026-08-14): with "write to
      files" enabled and more than 1 file affected, the apply currently
      blocks the UI until every file is rewritten — at the measured
      ~35 s/file for a full tag rewrite that is minutes-to-forever from the
      user's chair. Route the >1-file case through the operations system
      (`maintenance.bulk-write-back` already exists, takes explicit
      book_ids, and shows in the ops UI) and return immediately with the
      op id; single-file applies can stay synchronous. Note bulk-write-back
      is serial ~35 s/book — the E08 prerequisites fragment
      (diff-skip + in-op parallelism) applies here too.

## memdb and Pebble disagree about author→book links — ROOT-CAUSED, one step left

Found 2026-08-14 by running `maintenance.author-conjunction-repair` twice in
dry-run against prod and getting two different answers from the same op, same
binary, same data.

| run | started | path taken | `books_relinked` |
|---|---|---|---|
| 1 | 4s after service restart, memdb not yet warm | Pebble junction scan | **86** |
| 2 | memdb warm | memdb | **84** |

Row counts were identical in both (`authors_matched=46`,
`would_merge_into_existing=31`, `would_rename_in_place=15`). The entire
difference is **author 46627 (`& Nicholas Courtney`)**: the Pebble path finds 2
book links, memdb finds 0.

### ✅ Root cause (2026-08-14, fixed in `fix/author-getter-conformance`)

**memdb's query filtered out non-primary versions; neither Pebble path did.**
`memdb.GetBooksByAuthorID` skipped any book with `IsPrimaryVersion == false`.
Author 46627's 2 links are co-author credits on non-primary versions, so memdb
returned 0 and Pebble returned 2.

Nothing was wrong with the loader, which is why ruling out `safeInsert` was
correct and yet led nowhere: the junction rows loaded fine (`skipped_total=0`,
`book_authors=290643`). The rows were present the whole time and the *query*
discarded the books they pointed at.

A second, opposite divergence was found in the same read: **the Pebble path of
`GetBooksByAuthorIDCore` never opened the junction table at all**, so it saw
only `Book.AuthorID` and was blind to every co-author. One getter under-reported
non-primary versions, the other under-reported co-authors, and the two errors
pointing opposite ways is why aggregate counts stayed plausible for so long.

Contract now pinned by `internal/database/author_getter_conformance_test.go`:

- `...WithRoleCore` — the COMPLETE set (junction + legacy, non-primary
  **included**). Merges and deletes consult this one; a missed link is data loss.
- `...Core` — the LISTING view (junction + legacy, non-primary **excluded**).
- Both exclude soft-deleted books.

- [x] Identify why memdb and Pebble disagreed.
- [x] Write the conformance test rather than a per-path assertion — one fixture,
      both implementations, assert equal. This was the third memdb/Pebble
      divergence in a week (see the soft-deleted leak, #2392).
- [ ] **After this deploys**, drop `skip_author_ids: [46627]` from the repair
      invocation and repair that row. Everything else already applied
      2026-08-14 02:0x: 30 merged, 15 renamed, 0 failures, 145/145 book links
      verified *via the memdb-backed API — the Pebble path was not re-read
      post-apply*. Author 46627 is the ONLY remaining stranded-ampersand row;
      the other two survivors, `&#169` and
      `&#169;2013 by HarperCollinsPublishers`, are the separate HTML-entity
      defect.

**Why this blocked the repair:** the merge path relinks the books it can see and
then DELETES the author row. Run through memdb it would relink 0 books for 46627
and delete the author anyway, leaving the 2 Pebble junction rows pointing at an
author id that no longer exists — the orphaning hazard H8 documents on
`maintenance.author-split-scan`. The row was excluded by id for the 2026-08-14
apply rather than papered over with a heuristic. With the fix deployed, the warm
path sees those 2 links and the merge relinks them before deleting.

- [ ] **CORRECTION to `20260814-matcher-writeback-background-job.md`: the
      blocking half of the matcher is the metadata FETCH, not the file
      write.** Owner (2026-08-14): the write side is already backgrounded;
      the fetch side is effectively a singleton and must (1) be dispatched
      as a background operation visible in the ops system, and (2) fan out
      across books — WITH staggered start delays/jitter per request so the
      fan-out doesn't flood the metadata providers all at once.
      Evidence so far: `metafetch.chainMu` is NOT the singleton — it only
      guards cached-chain construction, and the chain is documented safe
      for concurrent worker pools (per-source rate limiter + circuit
      breaker carry their own mutexes). Look instead at (a) the bulk
      dialog's one-book-at-a-time interactive flow, and (b) whatever the
      matcher's search endpoint serializes server-side. Design notes:
      bounded worker pool per the concurrency mandate sized for
      NETWORK-bound work (small fixed concurrency, e.g. 3-4), plus
      per-worker start jitter (e.g. 250-500 ms spread) layered UNDER the
      existing per-source limiters so providers see a ramp, not a burst;
      progress per book surfaces through the op reporter so the UI polls
      the op instead of holding a request open (kills the false sign-out
      symptom at the root).

## Offset-pagination walkers: 5 remaining + the CI flake is NOT the cross-page swap

C815 (#PR) collapsed the 4 `internal/reconcile` whole-library offset loops to
single-snapshot reads. Two follow-ups:

- [ ] **5 more walkers share the exposure** (whole-library offset loops over
      `GetAllBooksCore(pageSize, offset)`, memdb-dispatched → cross-page
      snapshot-swap window): `internal/quarantine/service.go:232`,
      `internal/plugins/maintenance/repair_junk_titles.go:76`,
      `internal/plugins/maintenance/title_backfill.go:86`,
      `internal/plugins/maintenance/title_repair.go:199`,
      `internal/plugins/maintenance/duration_backfill.go:97`, plus the two
      internal pagers in `pebble_store.go` (:1414 folder-dup, :1551
      metadata-dup). Same collapse applies; verify each loop is pure
      accumulate (the reconcile four were) before editing.
- [ ] **The original CI flake (run 30702594886, 39/40 books) CANNOT be the
      cross-page swap**: 40 books with pageSize 5000 is a SINGLE page. The
      book was missing from the snapshot served by that one call — which
      points at a warmup/publish race (a book created while the memdb rebuild
      iterator was past its key, published without it, write-through buffer
      hole?). That is a STORE-layer bug and survives any enumeration pattern.
      Needs its own repro: create books concurrently with a forced warmup
      rebuild and diff the published snapshot against Pebble.

- [ ] **Verify op-ID audit trail on prod after deploying the run-context fix.** Trigger
  one low-risk maintenance op (`maintenance.temp-file-cleanup` is the safest — it
  records changes but only touches orphaned `*.tmp.m4b`/`*.tmp.m4a` files) and confirm
  `operation_changes` now has rows keyed to that op ID. Until this is observed on prod,
  the fix is verified only by unit test. The prod check is the one that matters: the
  wiring passes through `wireServerFromContainer`, which no test exercises.
- [ ] **Historical gap is permanent — do not chase it.** Every maintenance op run before
  this deploy recorded no `operation_changes` rows. Those runs cannot be reverted and the
  history cannot be reconstructed; the data to rebuild it was never written. Relevant when
  investigating anything that happened before 2026-08-14: an empty change list for a
  pre-fix run means "recording was off", not "nothing changed".
- [ ] **Audit the eight `ctxOpID` consumers now that the ID actually arrives.** Their
  `CreateOperationChange` calls have never executed in production, so their payloads have
  never been exercised against real data — a wrong field or a panic in one of those
  branches would have been invisible until now. Worth one read-through of each call site
  (`series.go`, `cleanup.go` x2, `write_back.go`, `reconcile.go`, `dedup_ops.go`,
  `optimize.go`, `metadata.go`) before or shortly after the deploy.

- [ ] **Organize renames with placeholder metadata — "Unknown Author" gets
      baked into directories and filenames.** Observed in a Review Organize
      preview (2026-08-14): a book under
      `audiobook-organizer/Unknown Author/All Jobs and Classes!…_ LitRPG/Epic Progression/`
      was offered a rename to
      `…/Unknown Author/All Jobs and Classes!…/… - Unknown Author - read by Ryan Dimon, Arielle Noelle.m4b`
      — the path cleanup itself worked (series-suffix folder collapsed), but
      the placeholder author was written into BOTH the folder and the new
      filename, twice. The book plainly has usable metadata (full title,
      two narrators) so an author lookup would very likely resolve.
      Wanted behavior for anything in the organizer tree:
      1. If the author is a placeholder ("Unknown Author"/empty), organize
         must NOT bake it into the target path. Resolve metadata FIRST
         (metadata fetch by title/narrators/tags), and only rename once a
         real author exists;
      2. otherwise route the book to review flagged "author unresolved"
         instead of proposing a rename that cements the placeholder;
      3. the rename template should always be built from resolved author +
         metadata, never from whatever the current row happens to hold.
      Audit how many books already have "Unknown Author" baked into their
      organizer-tree paths while carrying resolvable metadata.

- [ ] **E07 residue: 2 ambiguous duplicate-PID groups need a human pick.**
      The 2026-08-14 live census (`GET /api/v1/itunes/pid-integrity`) shows
      the duplicate-PID population is down to 2 groups — the same Alcatraz
      content present in both the organizer tree and the iTunes tree
      (sample PID `31A790B4DEF5981C`). The recorded "8,984 auto-resolvable"
      was stale pre-repair state. `files_to_clear=0`, so the repair op has
      nothing safe to do automatically; an operator must pick the canonical
      file per group. The iTunes-tree copies are HANDS-OFF — resolution must
      clear the organizer-side copy or be deferred, never touch the iTunes
      tree.

## Bleve index holds ~3,953 docs for soft-deleted books

The 2026-08-13 search-index repair backfilled 16,738 "missing" books using
the pre-#2408 leaky `ListBookIDs` enumeration — which included the trash.
Post-fix boot log (2026-08-14 10:01): `search index coverage OK
indexed=67824 books=63871`. DocCount − live = 3,953 = the soft-deleted set
exactly, so trashed books are now indexed and plausibly reachable through
web search until the index is reconciled.

- [ ] Add/verify a reconcile pass that REMOVES index docs whose book is
      soft-deleted or gone (the coverage gate only checks
      `len(ListBookIDs()) <= DocCount()`, which a polluted index passes
      forever — already noted in 20260813-search-index-repair-prod-findings
      as the "two slightly different populations" item; this is a live
      instance, not a hypothetical).
- [ ] Verify with a bogus-value control: search for a known trashed title
      before and after the cleanup.

## 2026-06-22 security-sweep: the items still open after the status pass

The audit now carries a per-finding status column (verified against HEAD
2026-08-14: 14 fixed, 8 partial, 13 open, 5 unverified, 1 obsolete). The open
items that are NOT already tracked elsewhere, so they don't live only in an
audit nobody reopens:

- [ ] **SEC-2** — bootstrap still writes plaintext credential files
      (`internal/server/bootstrap.go:108,:153`). Decide opt-in/local-only.
- [ ] **SEC-4 residue** — no CSP header yet (middleware comment defers until a
      nonce/hash strategy is settled).
- [ ] **SEC-8 residue** — Dockerfile build-dep tarballs (`utfcpp`, `taglib`)
      are `curl | tar` with no SHA256 verification; base images are pinned.
- [ ] **PERF-5** — `internal/itunes/backfill.go:60-68` offset pagination over
      a mutable snapshot (same class as the AssignOrphanVGs bug; use
      cursor/`GetAllBooksFullFrom`).
- [ ] **TOOL-1** — `testdata` is 2.2G tracked; decide fetched-dataset split.
- [ ] **FE-2/FE-3/FE-4** — the three stale-deps findings' line anchors have
      moved; re-anchor and verify (one sitting, all in web/src/pages).
- [ ] ARCH-3/4/5/7/8 remain structural programs. **ARCH-8 is DONE
      (2026-08-23, TASK-087, PR #2804)** — ARCH-3/4/5/7 are still open, which
      is why this entry stays unchecked. `config.GetConfig(c)` and
      `plugin.GetEventBus(c)` now own the two services that were read with one
      consistent type; 18 call sites across 12 files converted. Note what the
      accessors actually buy: a *misspelled* key was already a compile error
      (every site used a `Key*` constant), but a **valid key paired with the
      wrong type** — `Get[*plugin.EventBus](c, KeyConfig)` — used to
      type-check and panic at the assertion inside `Get`, and is now
      inexpressible. `serviceregistry.Get[T](c, KeyStore)` was deliberately
      NOT converted: it is consumed through ~15–20 narrowed interface types by
      design, and collapsing it would undo the store-decoupling work.
      `TestNoRawGetForAccessorOwnedTypes` guards against a raw
      `Get[*config.Config]` / `Get[*plugin.EventBus]` reappearing (escape
      hatch: a `serviceregistry-guard:allow-raw-get` comment on the line).

(SEC-9 is already filed; PERF-4 has its own fragment; PERF-2's remainder is
the aggregate-coalescing task; PERF-7 is the BookSig/memdb program.)

### Series table integrity — follow-ups from the 2026-08-14 prune repair

- [ ] **Identify what produced the 2026-08-11 burst of phantom series references.**
  Books reference 6,893 series IDs that have no row. The damage is EPISODIC, not a
  steady leak — only 10 distinct days ever, dated by ULID prefix on the book IDs:

  | day | books |
  |---|---|
  | 2026-06-18 | 78 |
  | 2026-06-19 | 16 |
  | 2026-07-19 | 507 |
  | **2026-08-11** | **5,367** |
  | 2026-08-12 | 133 |

  (the rest predate June; 7,220 landed in 2026-04.)

  The 08-11 books were all created within the same minute, 22:36 local, are loose
  files under `Unknown Author`, and carry titles like `Chapter 06` and
  `Singularity Online Book 3` — i.e. a scan of unsorted audio, not a maintenance
  op. Their series IDs are mid-range (153577, 165008, 165695), interleaved with
  live rows, NOT the contiguous all-dead 180000–184999 block.

  Checked and ruled out: no series-deleting op appears in the operations list for
  2026-08-11 or 08-12 (41 ops those days: purge-deleted ×18, temp-file-cleanup ×14,
  isbn-enrichment ×4, cleanup_activity_log ×2, maintenance-window ×2,
  metadata_candidate_fetch ×1). No scan op is recorded there either, which is
  itself worth explaining. `maintenance.series-prune` is ruled out for this burst.

  **RESOLVED — it is propagation, not minting.** Dating each phantom series ID by
  the earliest book that references it:

  | day | ids seen | first appearance | inherited |
  |---|---|---|---|
  | 2026-04-04 | 5,261 | 5,261 | 0 |
  | 2026-04-28 | 932 | 909 | 23 |
  | 2026-04-29 | 17 | 17 | 0 |
  | 2026-04-30 | 145 | 139 | 6 |
  | 2026-05-01 | 1 | 0 | 1 |
  | 2026-06-18 | 70 | 70 | 0 |
  | 2026-06-19 | 16 | 16 | 0 |
  | 2026-07-19 | 481 | 481 | 0 |
  | **2026-08-11** | **5,068** | **0** | **5,068** |
  | **2026-08-12** | **121** | **0** | **121** |

  No new phantom series ID has appeared since **2026-07-19**. Both August bursts
  are 100% inherited — every ID they reference was already dangling.

  The mechanism follows from `resolveSeriesID` (`internal/scanner/scanner.go:2487`):
  it resolves by **name**, creates the series when absent, and `CreateSeries`
  commits to Pebble with `pebble.Sync` before returning. **A scan therefore cannot
  produce a dangling reference** — it would just create a fresh series row. So any
  book holding a dangling `SeriesID` got it by **copying an existing book's
  record**, not by scanning.

  So the remaining work is not a hunt for a minting bug. It is: find the paths
  that copy `SeriesID` onto newly-created book rows and have them drop a reference
  whose series no longer exists. Prime suspects, given the 08-11 rows are
  per-chapter books (`Chapter 06`) created inside one minute:
  `internal/dedup/split_book_detector.go`, `maintenance.regroup-shattered-ai`,
  `maintenance.itunes-regroup`. Start from book `01KZSX7TW6BZXJX11F8K6Y0DSZ`.

- [ ] **`BulkDeleteSeries` still deletes on a filtered count.**
  `internal/server/handlers/entities/handler.go:1017` guards with
  `GetBooksBySeriesIDCore`, the same display counter that skips trashed and
  non-primary books and caused the phantom references. It should use
  `database.AsSeriesBookRefStore(...).GetAllSeriesBookRefCounts()` like
  `executeSeriesPrune` now does. Same for the single-delete path at line 1007.

- [ ] **Two more series deleters have no cache invalidation and no ref guard**, and
  sit in packages with no path to the server's caches:
  `internal/dedup/series_dedup.go` (`DedupSeries`, `MergeSeries`) and
  `internal/maintenance/jobs/cleanup_series.go` (`csUnlinkAndDeleteSeries`,
  `csMergeSeriesGroup`). Consider moving invalidation into the store layer
  (`PebbleStore.DeleteSeries` already notifies memdb) so no caller can forget.

- [ ] **`WithOpID` is never called in production code**, so `ctxOpID(ctx)` returns ""
  for all 8 maintenance ops that read it (`series.go`, `cleanup.go` ×2,
  `write_back.go`, `reconcile.go`, `dedup_ops.go`, `optimize.go`, `metadata.go`).
  Every `CreateOperationChange` in `executeSeriesPrune` is therefore skipped: the
  2026-08-14 prune deleted 326 series and recorded zero changes, so there is no
  audit trail and no revert. `maintenance.purge-deleted` has the same gap while
  permanently destroying books. Note this also invalidates "0 changes recorded"
  as evidence that an op did not run.

- [ ] **~2,270 series look like they were created from a book title rather than a real
  series** (990 where the series name equals its only book's title, 1,280 where one
  contains the other). Do NOT delete on book-count alone: 2,322 single-book series
  are real series you own one book from (*Arliss Cutter*, *The Spiderwick
  Chronicles*, *Star Runners*). Needs a dry-run that emits the list, a hand-audit of
  ~40 of the "near" bucket, and its own apply gate — the repair must be narrower
  than the classifier.

- [ ] **Check `scripts/setup-prometheus-auth.py` for the dead-indentation
      bug found in its server-side shell sibling.** The staged
      `abo-prometheus-auth.sh` (server home dir, patched in place to v1.0.1
      on 2026-08-14) computed a YAML body indent from a whitespace-only
      regex capture and then called `.index('-')` on it — a guaranteed
      `ValueError` for any list-style `- job_name:` entry, i.e. every real
      prometheus.yml. If the repo script shares the pattern, fix it there
      too; if not, note that the shell script diverged.

## Soft-deleting a book UPSERTS it into the search index

Found during C715: `DeleteAudiobook(SoftDelete: true)` sets the flags via
`store.UpdateBook`, and the `indexedStore` decorator enqueues a REINDEX on
every UpdateBook — so soft-deleting a book refreshes its search doc instead
of removing it. The trashed book stays searchable until the next boot's
coverage reconcile (set-based since C715) deletes the stale doc.

- [ ] Make the soft-delete transition enqueue a Bleve DELETE instead: either
      teach `indexedStore.UpdateBook` to check `MarkedForDeletion` on the
      updated row, or have the soft-delete path call the index delete
      explicitly. Mirror-image: RestoreAudiobook's UpdateBook reindex is
      CORRECT — don't break it.
- [ ] Regression test: soft-delete an indexed book, assert a title probe
      returns nothing WITHOUT a boot reconcile.

## `?version_group_id=` lists the whole library, and cannot be guarded

Found 2026-08-14 while trying to verify that an author merge had relinked two
books. Every form of the query returned the same answer:

    version_group_id='vg-08c1a396b'          -> count=63869  first=01KNDB8NWHXV2DKRQESBA9SDRA
    version_group_id='vg-TOTALLY-BOGUS-XYZ'  -> count=63869  first=01KNDB8NWHXV2DKRQESBA9SDRA
    version_group_id=''                      -> count=63869  first=01KNDB8NWHXV2DKRQESBA9SDRA

The negative control is the point: a bogus group ID and a real one are
indistinguishable, so the parameter is not read at all. The instrument was
unusable for the verification it was reached for, which is how it was noticed.

**Why the bare-param guard does not cover this.** That guard rejects names in
`audiobooks.KnownFilterFields()`. `version_group_id` is not in it — it is not a
filter field, so `bookFieldValue` has no case for it and the guard has nothing
to match. This is a genuine gap, not the same bug: `?year=` was a *known* field
passed the wrong way; `?version_group_id=` is a field the list never supported.

- [ ] Decide whether the list should filter on `version_group_id` at all. There
      is a real case: the memdb store already indexes it (`memIdxVersionGroupID`
      in `memdb_schema.go`) and `GetAllBooksFrom` accepts it as a filter key, so
      the storage layer supports the lookup the API does not expose.
- [ ] If yes: add a `case "version_group_id"` to `bookFieldValue` and the name
      to `allFilterFieldNames`. `TestFilterFieldNames_MatchTheMatcher` will hold
      the two together, and the bare-param guard then covers it automatically —
      no third list to update. Check the Pebble path too; a memdb-only index
      would be exactly the dual-implementation divergence fixed in #2406/#2410/#2411.
- [ ] If no: it still must not answer with the whole library. Extend the guard
      with a small set of *storage* filter keys that are not list filter fields,
      so the request is rejected rather than silently widened.

⚠️ Whichever way this goes, the rule from `FirstUnknownFilterField` applies: the
two failure modes here are inverted and both misleading — an unknown field
*inside* `filters` matches nothing and answers `count:0`, while a filter field
passed *bare* matches everything and answers with the library. Neither should be
reachable by a typo.

- [ ] **124 files remain 0600 after the fix-file-modes repair (1,547 repaired),
      and they expose a stale-path defect.** The repair enumerates
      `GetAllBookFilesCore()`, but the residue files are on-disk paths the
      canary write-back REALLY wrote (mtime in the canary window) that do not
      appear in that enumeration. Worse: the sampled book's `/files` API row
      points at a path that does NOT exist on disk (`.../The Seven Deadly
      Demons 3 - Dungeon of Pride/Dungeon of Pride.m4b` → ENOENT) while the
      real file lives at `.../The Seven Deadly Demons/Dungeon of Pride/...`.
      So (a) some books' file rows carry stale paths, (b) the write-back
      resolves the REAL file anyway (different row? path fallback?), and
      (c) the repair job can't see those paths. Investigate the row-vs-disk
      divergence (organize moved files without updating rows? duplicate
      rows?), then either extend fix-file-modes with a disk-walk mode or
      repair the residue by hand:
      `sudo find <organizer-root> -type f -user <service-user> -perm 600 -exec chmod 664 {} +`

### Search placeholder hint missing when navigating to All Books from Finished

Navigating from **Finished** directly to **All Books** leaves the search bar
without its `try author:"Name"` placeholder hint. Clicking away to the Dashboard
and then back to All Books makes it appear.

Reported 2026-08-15.

The refresh-fixes-it shape points at state that is computed on mount (or on a
particular route transition) rather than derived from the current route/filter on
every render — so the Finished → All Books transition reuses a mounted component
without recomputing the hint, while Dashboard → All Books remounts it.

Worth checking:
- whether the placeholder is derived from a `useState` initialized once vs a
  `useMemo`/derived value keyed on the active view
- whether the Finished and All Books routes share a component instance (same
  route element, differing only by a query param or filter prop), which would
  skip the remount
- whether the hint depends on a fetched field list that is only requested on
  mount

Low severity, cosmetic, but it makes the search syntax undiscoverable for anyone
arriving via that path — which is the one path where a user has just finished a
book and is most likely to go looking for another by the same author.

- [ ] **ABS series list emits a non-ABS `books[]` shape, and no series render in ABS clients.**
      Measured 2026-08-16 against production with the client's exact query
      (`?page=0&limit=50&sort=name`, what AudioBooth actually sends).

      **Root cause (evidenced):** ABS defines a series' `books` as full
      `LibraryItem` objects. Ours emit six ad-hoc fields only:
      `duration, id, libraryId, libraryItemId, sequence, title` — no `media`,
      no `media.metadata`, no `mediaType`, no `coverPath`, no `path`/`ino`.

      The control that makes this conclusive is the **playlists** endpoint,
      which the same app renders correctly: its items embed a complete
      `libraryItem` with all 20 ABS fields including `media.metadata`,
      `coverPath` and `mediaType`. Same client, same auth, same library — the
      one with the correct shape works, the one with the ad-hoc shape does not.
      A typed (Swift) client decoding `books: [LibraryItem]` fails on the first
      entry and discards the whole response, which is why **23 of 50
      well-formed series still render as zero**.

      Ruled out — do not re-investigate:
      - Not a timeout: series is 20 KB in 0.34s; playlists is 131 KB in 3.2s
        and renders fine.
      - Not auth, not pagination, not the query params: HTTP 200,
        `results=50`, `total=15528`.

      **Secondary bug, worth fixing in the same pass:** 27 of 50 entries have
      `books: []`, and 9 of those are self-contradictory — `numBooks >= 1` with
      `books: []` and `totalDuration: 0` (e.g. "Salem's Lot (read by Ron
      McLarty)" reports `numBooks=1`). The other 18 report `numBooks=0`.

      Fix: build the series `books` array from the same library-item serializer
      the playlists path already uses, rather than a bespoke projection.

- [ ] **The AI-scan cancel wiring is unverified, and it fails silently.**
      `CancelOperationV2` now cancels an AI scan through the pipeline manager
      (ported from the retired `DELETE /operations/:id`), but the collaborators
      arrive via `handlers.WithAIScanCancellation(...)` in `wire_handlers.go` and
      nothing asserts that call is still there. Drop it and cancelling an AI scan
      returns `204 No Content` while the scan keeps running — the exact defect the
      port exists to prevent.
      No test can cover it today because `Server.pipelineManager`
      (`*aiscan.PipelineManager`) and `Server.aiScanStore` (`*database.AIScanStore`)
      are concrete types, so a test cannot substitute them and drive the real
      construction path. Narrow them to the `ScanCanceler` / `AIScanLister`
      interfaces the handler already declares, then assert the wiring.
      Good candidate for the interface-splitting review.

      **Measured 2026-08-22 (#2720) — it was worse than "unverified".** The branch
      could not fire at all. `CancelOperationV2` matches an incoming **v2** op id
      against `scan.OperationID`, but that field held a **v1** ULID minted by
      `ulid.Make()` inside aiscan and registered with nothing. The two id spaces
      never intersected in practice: the v1 row reached no timeline, so no UI could
      offer its id, and the frontend's only AI-scan cancel is
      `DedupAIReviewTab.tsx:209` → `api.cancelAIScan(scan.ID)`, which posts the
      **scan** id to a different route and never touches `OperationID`.
      The `ai.author-scan` migration stores the **v2** op id in that field, which
      makes the branch reachable for the first time.
      ⚠️ Still open, and deliberately: reachable is not verified. Nobody has
      confirmed the ops-timeline cancel button issues `DELETE /operations/v2/:id`,
      and the `WithAIScanCancellation` wiring is still unasserted — which is the
      original point of this item.

- [ ] **August executive-summary roundup is stale.** `2026-08-31-august-monthly-roundup-executive-summary.md`
      says it consolidates "the seven dated summaries ... from 2026-08-04 to 2026-08-09"
      and was last edited 2026-08-14, but the directory now holds individual summaries
      through 2026-08-16. It describes itself as "month in progress — updated as work
      lands", so it needs a consolidation pass covering 08-10 through 08-16 before the
      month closes.

- [x] **Backfill legacy operation rows stuck at `pending`.** #2483 fixed the forward path
      (terminal status now mirrors from `publishOpTerminal`), but rows created before it
      stay frozen at whatever status they started with. `/api/v1/operations` shows several
      on page one alone (`archive-sweep`, `trash-cleanup`, `temp-file-cleanup`,
      `cleanup_activity_log`, `maintenance-window`, `purge-deleted`). Needs a one-off
      supervised pass — it rewrites historical records, so run it watching, not unattended.
      — closed 2026-08-21: not backfilled row-by-row, but the underlying symptom is gone —
      `git show --stat 1ce1de7d` shows `1ce1de7d refactor(ops): retire the v1 operation reads
      that served a table stuck on "pending"` deleted the v1 `GET /operations`,
      `/operations/:id/status`, `/operations/:id/logs` handlers entirely
      (`internal/server/handlers/operations/handler.go`, -87 lines); `wire_operations_routes.go`
      confirms the routes are gone (`RETIRED 2026-08-16` comment) and every caller now reads
      v2, which does not carry the stale `pending` rows.

- [ ] **Cancelling an operation the registry has never heard of reports success.**
      `DELETE /operations/v2/<unknown-id>` returns `204 No Content`. The handler
      calls `registry.Cancel(id)`, which returns `nil` for an id with no entry,
      so the route cannot distinguish "asked a running op to stop" from "did
      nothing at all". Measured 2026-08-16 in
      `TestOperationEndpointsErrors` — the assertion was written expecting 500
      and the test disagreed.
      This is the same shape as the legacy route it replaced, which answered 204
      after force-updating a legacy `operations` row that nothing was reading.
      Retiring that route did not fix the lie, it just stopped the write.
      Cancel should 404 for an unknown id and 204 only when something was
      actually signalled. Check whether the UI treats 204 as "cancelled" and
      shows a confirmation for an op that is still running.

- [ ] **"Dynamic" collections are currently *manually* refreshed.** A query-backed
      collection is evaluated at creation, when its query is edited, when it is read
      through the native API, and when `POST /api/v1/collections/:id/materialize` is
      called. Nothing refreshes it in the background. The ABS read path deliberately
      never evaluates (it serves `MaterializedBookIDs`), so a collection created via
      the native API and then only ever viewed in the app shows its **creation-time**
      membership indefinitely. Smart playlists solved this with a `Dirty` flag plus a
      push worker; collections have no equivalent yet. Either add one, or rename the
      concept so the word stops promising more than it does.

- [ ] **`AddBookToCollection` is read-modify-write with no version check.** Two
      concurrent adds to the same collection can lose one, and now that any holder of
      `collections.manage` can edit server-wide rows, concurrent edits are a realistic
      shape rather than a theoretical one. `Collection.Version` already exists and is
      incremented by `UpdateCollection` — a compare-and-swap on it is the cheap fix.

- [ ] **`POST /api/session/local-all` 404s.** Observed from the app alongside the
      collections 404s on 2026-08-16. Separate ABS gap, not covered by #2498 — the
      `/api/session/` prefix is reserved, so this reaches the ABS surface and finds no
      route. Needs the same treatment: implement it, or confirm a 404 is the honest
      answer and record why.

- [ ] **Finish (or delete) the iTunes plugin op migration.** `internal/plugins/itunes/`
      holds five stub `Run` bodies. Four are excluded from `registeredDefs()` because
      the real implementations live in `internal/server/itunes_ops.go` and
      `itunes_path_ops.go`; the package is now half a migration that does nothing.
      Either port the real bodies in (`itunes.sync` additionally needs
      `s.activityWriter` + `s.itunesActivityFn` threaded into `Plugin`, which is a
      design decision, not a move) or delete the stub files and their defs. Leaving
      them is what caused #2490's sibling bug: a stub that looks registrable.
- [ ] **Wire `itunes.position-sync` or drop it.** `internal/itunes/service/position_sync.go`
      implements a full bidirectional bookmark/play-count sync (`PositionSync.Sync()
      (pulled, pushed int)`) and **nothing in the codebase calls it** — the only
      reference is the TODO comment in the plugin stub. Wiring it turns on real writes
      to user positions across two systems on a 63k-book library, so it needs an
      explicit decision and a dry-run, not a one-line hookup.

- [x] **~~The `LegacyOpID` bridge still leaves rows at `pending`.~~ WRONG — the
      bridge worked.** This item was written from a production read of rows that
      all predated the fix. Measured 2026-08-16: the newest `pending` row was
      `01:05:41 -04:00`, the bridge (`5aeb02a8`) landed at `01:19`, and **zero**
      rows created after it are pending. The list is `created_at` DESC and the
      200-row page reaches back to 2026-08-09, so every post-bridge row was in
      the sample — the absence is real, not a sampling artifact. The two rows
      created after the 16:36 restart are both `completed`, at `1/1` with message
      `"completed"`, which is the bridge's own signature.
      **There was a real defect underneath it**, now fixed: `legacyStatusFor`
      enumerated three interrupted variants, and `interruptedStatus` mints
      `interrupted_quiesced` for every resume policy except `ResumeDrop` — three
      of the four legal values — while `worker.go` publishes
      `interrupted_restart`. Unmapped statuses returned early without writing or
      logging, indistinguishable from an op with no legacy row. #2500 had just
      moved `library.scan` to `ResumeRestart`, making the unmapped branch its
      normal outcome across a restart.
      The scheduler now writes no legacy rows at all, so the question is moot for
      it either way.

- [ ] **`ClearStaleOperations` is still wired, deliberately.** `POST
      /operations/clear-stale` force-marks pending/running/queued legacy rows as
      `failed`. It is the only broom for the ~183 historical rows stranded before
      the bridge landed, so deleting it now would remove the only tool for them.
      It is also dishonest for rows whose jobs actually completed — `failed` is
      not what happened. Retire it together with the supervised backfill in
      `todo.d/20260816-backfill-stuck-legacy-op-rows.md`, not before.

      **Backfill BUILT 2026-08-22 (#2721):** `operations.backfill-legacy-status`,
      dry-run by default. Awaiting a prod dry run before applying.
      ⚠️ **Two findings that change this item.** First, the count: nothing in the
      codebase could produce one. `GET /operations/stale` and the restart reaper
      both use `isStaleOperationStatus`, which matches running/queued/in_progress
      and **NOT** `pending` — the exact status these rows are stuck in — so the
      stale view answered `count: 0` against prod on 2026-08-22 while the rows
      existed. `ClearStaleOperations` uses a *different* inline predicate that
      **does** include `pending`, so the clear button acts on rows the stale view
      will not show you. The "~183" figure is therefore **unverified**; the
      backfill's own census (`ListOperations(0,0)`, unwindowed) is the first real
      instrument for it.
      Second, `ClearStaleOperations` is capped at `GetRecentOperations(500)`, so
      even where it *can* see pending rows it only reaches the newest 500 — "the
      only broom for the ~183 historical rows" holds only for whichever of them
      fall inside that window.
      ~~Note `internal/aiscan/pipeline.go` still writes the legacy table directly at
      4 call sites, so "nothing writes it anymore" is not yet true.~~
      **Resolved 2026-08-22 (#2720):** aiscan no longer writes the legacy table at
      all. The count was also wrong — there were **6** write sites, not 4; lines
      27-29 were the interface declaration, not writes. All six went with the
      `ai.author-scan` migration, and the three v1 methods were removed from
      `aiscan.Store` (7 methods → 4), so re-introducing a write is now a **compile
      error** rather than something review has to catch. The stranded-row backfill
      remains the only thing gating this retirement.

- [x] **`CancelOperation` (legacy) had AI-scan handling `CancelOperationV2`
      lacked.** Ported first, route deleted second. The wiring that supplies the
      pipeline manager is not itself asserted — see
      `todo.d/20260816-ai-scan-cancel-wiring-unverified.md`.

- [ ] **`/tasks/*` and `/maintenance-window/*` are NOT v1 operations.** Six routes
      on the legacy operations handler are scheduler *configuration*, not
      operation records. They should not be converted to op-defs or deleted with
      the rest; move them to their own handler so "retire v1 operations" does not
      read as "delete task scheduling". Still outstanding.

- [ ] **`library.import`, `library.organize` and `library.transcode` still carry the
      4h ceiling and `ResumeDrop`.** Only `library.scan` was changed, deliberately —
      it is the one measured to exceed 4h. Check whether the others can also exceed
      their ceiling on a 63k-book library before assuming they are fine; `organize`
      in particular touches every book.

- [ ] **Convert the remaining long-running `ResumeDrop` ops to real resume.** The
      mechanism now exists: `registry.RunItems` gained `ResumeFrom`,
      `CheckpointEvery` and `CheckpointStateFn` (concurrent-safe via a
      contiguous-completion watermark), and 51 call sites route through it. As of
      2026-08-17 the live registry reports 140 defs: 100 `drop`, 19 `restart`, 19
      `requeue`, 2 `ask`. Work through the `drop` list and convert the ones that are
      both long-running and idempotent per item — `metadata.batch-apply-cached`,
      `reconcile.apply` and the full-library sweeps first. Ops that are short-lived
      or unsafe to re-enter should STAY `drop` and get a comment saying why; an
      honest drop is better than a resume that does not work.

- [ ] **Forward `IsCanceled()` through `reporterLogger` and exercise the four guards it wakes up.**
      `LoggerFromReporter` now bridges `UpdateProgress` to the ops registry
      reporter, but `IsCanceled()` still delegates to the wrapped logger, which
      answers `false` unconditionally. That leaves four cancellation guards
      unreachable, as they have been since the 2026-05-11 BridgeQueue removal:
      `internal/scanner/service.go:190`, `internal/organizer/service.go:897` and
      `:1082`, `internal/reconcile/reconcile.go:597`.
      Cancellation itself is not broken — every one of these services also
      honours `ctx`, which is what the watchdog cancels — so this is a
      responsiveness and correctness-of-intent item, not an outage. It was held
      back from the progress fix deliberately: switching on four branches that
      have not run in three months, in the same change that unblocks production
      scanning, would make a bad first run impossible to bisect.
      Before flipping it: read each guard for what it does on the way out
      (partial state, half-written aggregates, skipped cleanup), and check
      whether `scanner/service.go:177`'s "both cancellation channels have to be
      checked here" comment still describes the intended behaviour once the
      logger channel is live.

- [ ] **Audit the other two silently-stubbed `StandardLogger` methods.**
      `RecordChange` and `ChangeCounters` (`internal/logger/standard.go:62-63`)
      are also empty/nil, so any operation running through
      `LoggerFromReporter` that records changes is discarding them the same way
      progress was being discarded. Determine whether the scanner/organizer
      change-tracking counters are consumed anywhere (activity feed, op summary)
      and, if so, whether they have been empty since 2026-05-11.

### Import-path scan no longer surfaces per-file scan errors

The "View Errors" button on a path row in Settings → Paths is unreachable for
errors found *during* a scan. It renders only when `errorCount > 0`
(`web/src/components/settings/PathsSettingsTab.tsx:169`, and the same shape in
`SettingsGeneral.tsx:695`), and `errors` is seeded as a permanently empty array
in `web/src/hooks/useImportFolderHandlers.ts:103`.

That was a deliberate, correct fix at the time: the code used to read
`response.errors` off the trigger response, which never existed — starting a
scan is asynchronous and answers an operation id only, so it was `undefined` at
runtime long before the type admitted it. Typing the trigger honestly as
`{ id }` is what exposed it.

What was never done is the other half: nothing now reads the errors back off
the operation. The count is non-zero only when the trigger call itself throws,
so a scan that finds ten corrupt files reports "Scan complete. Found N
audiobooks." and offers no way to see them.

**To restore — TWO layers, not one.** An earlier version of this note said the fix
was to poll the operation and feed per-file failures into `ScanStatus.errors`. That
assumed the data exists on the backend. Verified 2026-08-17 that it does not:

- **Nothing collects per-file failures.** `Errors []`, `FailedFiles`, `SkippedFiles`
  do not appear anywhere in `internal/scanner/` — the scan never accumulates which
  files failed, so there is no list to fetch.
- **The failures that do get logged are free text, mostly below the bar.**
  `scanner.go:1672` logs a tag-read failure at **Debug**; `process_file.go:100` logs
  one at **Warn**. Both interpolate the path into the message string rather than
  putting it in structured `attrs`, so a client would have to regex file paths out of
  log prose.

So the work is:

1. **Backend** — emit per-file failures into the operation log at warn/error with the
   file path (and reason) in structured `attrs`, not interpolated into the message.
2. **Frontend** — read them off `GET /operations/v2/:id` (which already returns
   `data.logs` beside `data.operation`, so no new endpoint) into `ScanStatus.errors`.

Scope this as a feature, not a wiring fix.

The E2E test that covered this,
`web/tests/e2e/scan-import-organize.spec.ts` › *scan operation: handles errors
gracefully*, now asserts the button's ABSENCE, with a comment pointing here. It
will fail as soon as the capability comes back — that is the signal to restore
the original assertion on the error text.

- [ ] **3 scheduled tasks are ENABLED but can never run.** Startup logs
      `Scheduled task is ENABLED but can NEVER run` for `library_organize`,
      `library_size_refresh` and `metadata_upgrade` — all `interval=0s`,
      `declaresMaintenanceWindow=false`, `inMaintenanceOrder=true`. Pre-existing (15
      occurrences before the 2026-08-16 boot). Each needs either a
      `scheduled.<task>.interval` or `declaresMaintenanceWindow=true`.
      ⚠️ `library_organize` is the trigger for the library-wide relocation from #2479 —
      enabling it starts moving files across the whole library, so decide deliberately.
      See `docs/handoffs/2026-08-16-overnight-silent-failure-fixes.md`.

### Store-interface audit — defects found incidentally (each independent of the refactor)

Surfaced while producing `docs/audits/2026-08-16-store-interface-decomposition.md` (§11).
Measured at `8011a755`. **None is caused by that proposal**; they are filed separately so the
proposal's scope stays reviewable. Items marked ⚠ are agent-reported and not hand-verified.

**Concurrency / correctness**

- [ ] `internal/database/store.go` — `globalStore` is guarded by `globalStoreMu sync.RWMutex`
      (`:1217`) but three of five accesses bypass it: `InitializeStore` writes bare (`:1261`),
      `CloseStore` reads bare (`:1275`) and writes bare (`:1276`). `:1280` is a
      `time.Sleep(100 * time.Millisecond)` commented "brief pause to let in-flight goroutines
      notice the nil" — a race workaround, not a fix. Blast radius is test-only today (zero
      production readers of the global), which is exactly why it becomes production-critical
      the moment a `GetGlobalStore()` call is reintroduced.
- [ ] `internal/server/wire_abs_routes.go:494` — bare `s.Store().(*database.PebbleStore)`
      assertion inside a goroutine, the literal form `internal/database/store_capability.go:44`
      forbids. `Server.Store()` (`server.go:331-333`) reads `s.store` with no lock while
      `server_lifecycle.go:362` writes `s.store = wrapped`; the goroutine is launched from
      `setupRoutes()` inside `NewServer`, i.e. before `Start()`. The data race is certain;
      **which side wins is not** — this is not a claim that warmup is skipped in prod.

**Capability pattern — the historically-realized defect class**

- [ ] `internal/database/iface_assert.go` — its comment claims compile-time proof that
      `PebbleStore` satisfies *every* sub-interface. It asserts **36 of 40**. Missing:
      `OAuthIdentityStore`, `MetadataCacheStore`, `RejectedMetadataStore`, `ReviewStore`.
      One line each.
- [ ] `internal/merge/service.go:34-42` — `AsExternalIDReassigner` uses a bare
      `s.(ExternalIDReassigner)` instead of `database.AsCapability`. Called on `ms.db` at
      `:236` and `:377`. Latent today (registry-built `merge.Service` holds the bare store),
      but one wiring change turns it into silent skipping of iTunes-PID/ASIN reassignment on
      merge. Same shape at `internal/plugins/acoustid/reset_all.go:69` and `lsh_backfill.go:86`.
- [ ] `internal/operations/registry/register.go:40-42` — `prodSchedulerStore` embeds
      `database.Store` and adds `BookFiles`, but does not implement `StoreUnwrapper`.
      Defect-*shaped*, not live: no capability lookup currently runs through it.

> Context for why this class is not hypothetical: `internal/server/server_lifecycle.go:1737-1766`
> documents the **third** capability lost to the same decorator, measured in production
> 2026-08-10 23:07:40 — the version-group index backfill "had NEVER ONCE RUN, silently" since
> the decorator was installed, and is the likely origin of the under-reporting in #2277.

**Comments that are false at HEAD**

- [ ] `internal/importer/service.go:27-31` — `type Store = database.Store` justified by
      "`versions.CreateIngestVersion` requires the full Store interface." It uses **4 methods**.
- [ ] `internal/server/handlers/organize.go:57-62` — `type OrganizeStore = database.Store`
      justified by `organizer.SetStore` and `deluge.NotifyDelugeAfterOrganize`. At HEAD those
      take a 4-method `OrganizerStore` and an anonymous `interface{ database.BookVersionStore }`.
- [x] `internal/dedup/collectors_metadata.go:51` — "`database.EnsureSingletonBookTag` (which
      requires the full Store interface)." It uses **3**.
- [x] `internal/database/store.go:17` cites
      `docs/superpowers/specs/2026-04-17-store-interface-segregation-design.md`, which is not on
      main. Recoverable via `git show 29e256ac:<path>`. Either restore the doc or repoint the
      reference to `docs/archive/superpowers/plans/`.

**Test quality**

- [x] ~~⚠ `internal/database/mock_store.go` — ~88 of `MockStore`'s 399 methods have no `Func`
      override field and are hardwired to a zero return no test can change.
      `GetAllAuthorBookCounts` (`:863`) returns `map[int]int{}, nil` unconditionally, so
      `TestListAuthors_Success` asserts against a response where every author has `BookCount: 0`.~~
      — closed 2026-08-22 (PR #2704, TASK-034): 89 methods gained `XFunc` override fields
      (override-guard count 313 → 402). Guarded by a structural AST test
      (`mock_store_override_test.go`) that fails naming every method still missing a guard,
      with a positive control so a broken parser cannot pass vacuously.
- [ ] ⚠ `internal/server/organize_service_test.go:34` — vacuous test. It sets `GetAllBooksFunc`;
      the code under test calls `GetAllBooksCore`, whose func field is unset → `nil, nil`.
      `TestOrganizeService_PerformOrganize_NoBooksToOrganize` asserts only `err == nil` and
      passes against a mock wired to nothing.

**Dead generated code (part of the audit's step 1, listed here for tracking)**

- [x] `internal/scanner/mocks` — 442 generated lines, **zero** importers, while
      `internal/scanner`'s own tests hand-roll `fullMockScanner`
      (`scanner_coverage_test.go:655`) because importing the mocks package would cycle.
      Delete the `Scanner:` entry from `.mockery.yaml`; keep the hand-written double.
- [ ] `internal/operations/mocks` — 206 generated lines, effectively unreferenced.
- [x] `Makefile` `check-mock-fresh` — **DELETED 2026-08-17.** Ran `go generate` where the
      repo has **zero** `//go:generate` directives, so its regeneration step was a no-op and
      the following `git diff` only detected a dirty worktree. Measured: mutate the `Store`
      interface and leave the mock alone and it printed "Mock is fresh", exit 0. Deleted
      rather than repaired — `iface_assert.go:12`, `mock_store.go:30`, `vet` and `mocks-check`
      all already go red on that mutation, so no coverage was lost.

### `PUT /tasks/:name` refuses 16 of the 27 registered scheduler tasks

`bindingForTask` in `internal/server/handlers/operations/handler.go` covers 12
task names. The scheduler registers 27. The other 16 fall to the "task %q config
is not configurable" 400:

`acoustid_online_lookup`, `ai_dedup_batch`, `archive_sweep`, `batch_poller`,
`cleanup_activity_log`, `cleanup_old_backups`, `dedup_llm_review`,
`isbn_enrichment`, `label_refinement`, `library_size_refresh`,
`metadata_upgrade`, `resolve_production_authors`, `series_normalize`,
`temp_file_cleanup`, `transcode`, `trash_cleanup`

**This is pre-existing, not a regression.** The switch that `bindingForTask`
replaced had the same 12 names and the same `default:` 400 — verified against
`a422b4d7^`. It is filed here because the binding table now makes the gap
countable, and because it fails LOUDLY (400), which is a different and much less
dangerous defect than the silent 200 that PR #2502 fixes.

At least one of the 16 has real config behind it: `ai_dedup_batch`'s IsEnabled is
`Scheduled.AIDedupBatch.Enabled && config.AppConfig.EnableAIParsing`
(`internal/scheduler/tasks.go:745`), so there is a per-task `enabled` field the
endpoint declines to write. Note the `&&` — the same getter-reads-more-than-the-
bound-field shape that `library_scan` had, so a binding added for it needs the
same treatment (see `foldLegacy`, and prefer rejecting with a hint naming
`enable_ai_parsing` over pretending the per-task flag is sufficient).

Before adding bindings, check what the Maintenance settings page actually renders
for these 16 — if it shows editable controls for them, users are getting a 400
from a control that looks live.

Do NOT add a binding without also extending
`TestUpdateTaskConfig_SchedulerReportsTheAppliedValue`, which reads the value
back through the real `TaskDefinition` getters via `scheduler.ListTasks()`. The
field-level assertion alone cannot see an OR/AND mask — that is exactly how the
`library_scan` masking survived the first round of tests.

### Stranded `.tmp-rename` recovery — bisect complete, recovery outstanding

~35 GB of audio is stranded on disk inside directories that a path-construction
bug created. The bisect is **done**; recording it so nobody re-derives it.

**Introduced:** `f29c3ce6`, 2026-03-03 13:43:40 —
`viper.SetDefault("segment_title_format", "{title} - {track}/{total_tracks}")`.
A shipped default with a literal `/` in it, so `{track_title}` expanded to
`"Pink Bean Series - 1/9"` with every variable value perfectly clean.

**Removed:** `c54721c7`, 2026-08-15 22:10:50 — deleted `segment_title_format`
outright and defaulted the file pattern to `{title} - {track:02d}`.

Live for 5.5 months. `243e2f38` ("scrub path separators from template
variables", 2026-05-28) is **not** the fix for this one — `scrubVar` sanitizes
variable values, and this separator was in the template. It fixed a genuinely
separate bug (title metadata containing `3/85`) whose on-disk wreckage is
identical, which is why the two were conflated.

**Measured on prod 2026-08-16 (read-only):**

| metric | value |
|---|---|
| stranded `.tmp-rename` files | 2,584 |
| bogus directories | 2,535 |
| affected books | 82 |
| books with no other copy | **77** |
| size | 35.2 GB |
| bogus-dir mtime range | 2026-04-07 → 2026-04-30, plus **2 on 2026-08-14** |

Directory mtime is the right instrument, not file mtime: `rename(2)` preserves
file mtime, so the files carry inherited dates, while the bogus directory only
exists as a product of the bug. `mtime == ctime` on all 2,535.

The two 2026-08-14 directories came from the `internal/metafetch` twin, which
had no `scrubVar` at all until #2479 unified the builders — so the live
metadata-apply path was still stranding files two days before that landed.

**⚠️ The `.tmp-rename` census is an undercount.** The bogus directories also
contain *successfully* renamed files (`Project Hail Mary - 16/31.mp3` sits
beside `Project Hail Mary - 24/31.mp3.tmp-rename`). Any recovery must sweep the
directories, not just the `.tmp-rename` glob.

**Outstanding:**

- [ ] Recovery tool. Dry-run by default, full report before anything moves.
      **77 books have no other copy — a wrong move is unrecoverable.** Derive
      and validate the naming rule against the 5 books that also contain
      surviving audio (Project Hail Mary, Singularity Online 1, Welcome to the
      Multiverse 5, Dreamcatcher, Neuromancer) before pointing it at the other
      77. Reconstruct by rejoining directory tail + filename
      (`"Pink Bean Series - 1" + " " + "9.m4b"`), not by relocating the bare
      file, which discards the chapter's identity.
- [ ] Compare with per-file SHA-256; where hashes differ because of embedded
      artwork, fall back to `ffmpeg -v error -i FILE -map 0:a -f md5 -`, which
      hashes decoded audio and ignores container metadata. Exact, unlike
      AcoustID — only exact should authorize a delete.
- [ ] Investigate book rows affected as a side effect. `scrubVar`'s own comment
      records the scanner reacting by creating **85 separate Book records** for
      one book, so look for spurious rows (path segment matching ` - [0-9]+$`,
      or a purely numeric title) *before* doing soft-delete/purge archaeology.
- [ ] Confirm no new bogus directories appear now that both the pattern and the
      builder guard are in place. The post-fix observation window is currently
      about zero.

- [ ] **Give the AI parser typed provider errors.** `internal/scanner/ai_failure.go`
      decides whether an AI failure is permanent by substring-matching the error text
      (`insufficient_quota`, `invalid_api_key`, …) because `aiParser.ParseBatch` flattens
      the HTTP status and the provider's error code into a `fmt.Errorf` string several
      layers down. Return a typed error carrying status + provider code so the check can
      be `errors.As` instead of `strings.Contains`. The current matcher is safe to miss —
      the phase still stops after 3 consecutive failures — but a miss costs ~60s of
      guaranteed-failing calls per scan.

- [ ] **Maintenance window: watchdog cancels it, then the plugin reports success.** Prod
      cancelled the maintenance window at 331s idle, after which the plugin logged
      "completed successfully (100%)". Pre-existing disagreement, but newly consequential
      after #2483: the legacy operations row is now mirrored as `canceled` while the op's
      own log claims success, so the two records actively contradict each other.

### 🐛 `book_file.missing` / `file_exists` are stale — nothing maintains them

Measured 2026-08-17 against prod: all **2,077** `book_file` rows belonging to the
60 fully-broken books reported `missing: false` and `file_exists: true` via
`GET /audiobooks/:id/files`, while `maintenance.missing-file-audit` proves every
one of those paths fails `os.Stat`.

They are stored columns, not live checks, and no writer keeps them current.

- **Do not filter on them.** Any query that treats `missing = false` as "the file
  is there" is silently wrong, and would report a fully-broken library as healthy.
- Audit who reads them before deciding the fix — either maintain them or remove
  them so nothing can be misled.
- **The cheap fix is already half-built:** `maintenance.missing-file-audit` stats
  every `book_file` path and therefore computes exactly this truth on every run
  (532,296 rows in ~168s). It just discards it. Persisting the per-row verdict
  would make the columns correct as a side effect of a job that already runs.

⚠️ **Unowned.** Neither of the two sessions working this repo on 2026-08-17 owns it:
the maintenance-v2 lane owns the `MaintenanceJob` → `OperationDef` migration, not
these columns; the prod-ops lane found it but does not own
`internal/plugins/maintenance`. Surfaced deliberately rather than absorbed by
either, so it needs an owner assigned.

Found while measuring signal coverage for the missing-file repoint work; see
`docs/audits/2026-08-17-missing-file-audit-full-population.md` §9.

- [ ] **Classify the 71,954 missing `book_file` rows by shape before any
      `missing-file-repair` apply.** Full-population audit
      (`docs/audits/2026-08-17-missing-file-audit-full-population.md`) proved two
      distinct populations: track-slash rows whose bytes are on disk under the
      `{track:02d}` name (repoint, never delete) and vanished-directory rows
      (delete is correct). `missing-file-repair` has no repoint mode and its
      per-book safety rule waves the recoverable rows through.
- [ ] **Decide the 16,265 books with no surviving file** (was believed to be 5,
      from a 120-book sample). Human decision, still open.
- [ ] **`missing-file-repair` dry run hit the 20,000 `max_deletes` cap.** The true
      repairable-row count is unmeasured; a capped apply looks complete but is not.
- [ ] **1,006 missing rows are under the iTunes tree**, contradicting the
      `missing_file_audit.go` header comment that says none are. Investigate
      separately — the iTunes tree is hands-off.
- [ ] **61 rows carry a mangled `/X:/books/itunes/Audiobooks` Windows path.**

- [ ] **Decide what to do with the books whose EVERY `book_file` row is dead.** The
      general repair is decided and built (`maintenance.missing-file-repair`, option
      "delete only where the book keeps a surviving file"), but it deliberately
      skips books with no surviving file — 5 of 120 in the sample. Deleting their
      rows would leave the book with nothing at all. Options: locate the audio by
      filename/size/hash and re-point the row, mark the book as missing rather than
      deleting, or leave it. The repair op names these books in its report, so run
      the audit + a dry run first and decide against the real list.

- [ ] **Answer why the organizer recorded destination rows it never populated.**
      Every dead path is under the organizer's own destination tree and none under
      the iTunes tree, which points at the library-wide move in #2479. The repair
      cleans up the symptom; this is the cause, and without it the rows come back.

- [ ] **Register `HEAD` for the audio/file routes.** The server registers no `HEAD` handler
      anywhere, so `HEAD /api/items/:id/file/:ino/download` 404s on a file that exists. Upstream
      Audiobookshelf runs on Express, which auto-answers `HEAD` for a `GET` route; gin does not.
      Not currently causing failures — the production journal shows real clients only send `GET` —
      but any client that preflights with `HEAD` would see "file not found".

### 🔴 `dedup.llm-review` holds a library write with no `ConcurrencyKey` — it can run concurrently with itself

`internal/plugins/dedup/llm_review.go:19` registers `ID: "dedup.llm-review"` declaring both
`sdk.CapLibraryRead` and `sdk.CapLibraryWrite` (`:28`, `:29`) but sets **no `ConcurrencyKey`**.

It is the **only** one of the 17 write-declaring `ResumeDrop` ops in `internal/plugins/dedup/` in
that state. The other 16 each serialize against themselves with a key matching their own op ID
(`dedup.auto-resolve`, `dedup.purge-stale`, `dedup.drain-stale`, …). Verified three ways — two
independent grep runs in separate sessions plus a Python re-derivation that touches no regex engine
— and `llm_review.go` is the sole result every time.

**Why this is a defect and not a style inconsistency:** an empty `ConcurrencyKey` means the scheduler
will happily run a second `dedup.llm-review` while the first is mid-flight, both holding
`CapLibraryWrite`. That is the same double-mutation hazard CLAUDE.md's concurrency section describes
for auto-merge/auto-resolve apply paths — "an auto-merge/auto-resolve apply path that must not
double-merge a book processed by two workers at once" — except it arrives through the **scheduler**
rather than through resume, so the resume-policy audit that found it would not have caught it.

**Fix is almost certainly one line** (`ConcurrencyKey: "dedup.llm-review"`, matching all 16 siblings),
but confirm first that concurrent self-execution was not deliberate — LLM review may have been left
unkeyed on purpose to allow parallel batches, in which case the writes need to be verified disjoint
and a comment should say so.

**Ownership:** found while scoping the `ResumeDrop` census and **claimed by no session** —
`internal/plugins/dedup/` is outside both the maintenance-to-v2 lane and the prod-ops lane. Filed so
it does not sit in a mutual-assumption gap.

Context: `docs/plans/2026-08-17-maintenance-jobs-to-v2-ops.md` (the dedup scoping section, which also
records why a file-scoped grep gives a false all-clear on `auto_resolve.go`).

- [ ] **Six E2E mocks point at operation URLs that no longer exist, and two
      separate things were confused because of it.**
      `getOperationStatus` now polls `GET /operations/v2/:id`; it used to poll
      `GET /operations/:id/status`, retired in #2502. These mocks still target
      the old shape, so the request stops matching, falls through to the real
      server and 404s — a stale mock here fails silently, it does not error:
      - `web/tests/e2e/dynamic-ui-interactions.spec.ts:269` — `**/api/v1/operations/*/status`
      - `web/tests/e2e/dedup-operations.spec.ts:141` — `**/api/v1/operations/*/status`
      - `web/tests/e2e/dedup.spec.ts:189` — `**/api/v1/operations/*/status`
      - `web/tests/e2e/diagnostics.spec.ts:80` — `**/api/v1/operations/op-2`
      - `web/tests/e2e/diagnostics.spec.ts:175` — `**/api/v1/operations/op-1`
      - `web/tests/e2e/transcode-and-counting.spec.ts:97` — `**/api/v1/operations/op-transcode-1`
      Retargeting also needs a **body change**, not just a URL change:
      `getOperationStatus` reads `def_id` / `progress_current` /
      `progress_total` / `progress_message` off the v2 record, while every mock
      above returns the flat legacy `type` / `progress` / `total` / `message`
      shape. A URL-only fix yields progress 0 and an undefined type.
      **Measured 2026-08-16, and this is the part that matters:** retargeting
      `dynamic-ui-interactions.spec.ts` to `**/api/v1/operations/v2/*` with a v2
      body changed nothing — 6 failed / 4 passed before and after. A control run
      of that spec against `origin/main` (detached checkout, same machine, same
      command) also gives **6 failed / 4 passed**, so those six failures are
      PRE-EXISTING and have nothing to do with the route retirement. The failing
      assertions are all "spinner/loading button is visible"
      (`Scan All`, `Organize Library`, per-path scan, dashboard variants,
      visual-regression). Root cause still unknown — do not assume it is the
      mock.
      Note the daily scheduled E2E runs on `main` are green (8/8, 2026-08-09..16)
      while the `pull_request` run is red. Those are different triggers, so a
      green schedule history is NOT a control for a PR failure — that mistake is
      what made these look like a regression in #2502.


## Update 2026-08-16: one of these was NOT pre-existing

The claim above that the E2E failures are all pre-existing was measured on ONE
spec (`dynamic-ui-interactions`, 6 failed / 4 passed identically on the branch
and on detached `origin/main`) and then generalised to all 24. That was one
sample, not a census.

Running the other five failing specs against detached `origin/main` on the same
machine gave 17 failures; the branch gave 18. The extra one,
`operation-monitoring.spec.ts:246 > cancels running operation`, was a real
regression from retiring `DELETE /operations/:id` -- the mock's one-segment
regex could not match the two-segment `/operations/v2/<id>` path. Fixed in
`test(e2e): point the cancel mock at the v2 route the client now calls`.

Re-measured after that fix, five specs, same machine:

| | failed | passed |
|---|---|---|
| `origin/main` | 17 | 28 |
| branch | 17 | 28 |

Identical failure sets. The remaining 23 (17 here + 6 in
`dynamic-ui-interactions`) are genuinely pre-existing.

**Lesson for whoever picks this up:** a failure count is not a failure set. Diff
the sets with `comm`, per spec, or a regression hides inside an unchanged-looking
total.

- [ ] **Add an `{edition_suffix}` folder-pattern token.** Two editions of the same
      title sharing a `{print_year}` compute the same target path under the
      current default (`{author}/{series}/{title} ({print_year})`). They do not
      clobber — `OrganizeBook` stats the target, finds a different file owned by a
      different book, fires `OnCollision` to raise a dedup candidate and returns
      `ErrTargetOccupied`, and both `rename.go` and `move.go` refuse to overwrite
      independently — but the second edition simply never gets organized, which
      looks like "organize didn't run" unless someone checks the collision queue.
      `{edition}` already exists in the token vocabulary (`pathbuild.go`), but it
      is a raw value: books with no edition would render a dangling space or empty
      parens. Model the new token on `{series_prefix}`, which is built AFTER the
      trim pass precisely so its separator counts as pattern structure rather than
      metadata and collapses to "" when the value is empty.
      Discussed 2026-08-17; deliberately deferred — collisions are visible and
      safe, so this is an ergonomics fix, not a correctness one.

- [ ] **Investigate the LLM host's GPU cooling before running another full `library.scan`.**
      Measured 2026-08-17 during the scan's AI-parsing phase: the card held **97 °C against
      its own 95 °C shutdown spec** (slowdown 92 °C, max-operating 88 °C, target 83 °C),
      with `HW Thermal Slowdown: Active` and a cumulative slowdown counter of
      8,737,239,236 us (~2h 25m). Clocks were pinned at 1860 MHz against a 2130 MHz
      maximum — ~87% of rated clock — at 92–93% sustained utilization.
      Cancelling the load dropped it 97 °C → 61 °C in 70 s and cleared the latched
      throttle, so the cooler does move heat; sustained 100%-duty inference simply
      exceeds it. **`nvidia-smi` reports `[Unknown Error]` for fan speed on this card,
      which is unexplained and is the one thing warranting a physical look.**
      Two knock-ons, both recorded in `docs/plans/2026-08-17-split-scan-ai-phase.md`:
      a client-side worker pool on the AI batch loop is off the table (the GPU is
      saturated, not idle), and the measured "AI parsing is 69.4% of scan wall-clock"
      figure is thermally confounded — any re-measurement must record GPU temperature
      and clock alongside it. Recovering 1860 → 2130 MHz is ~14.5% on the phase that is
      ~69% of the scan, for zero code risk.

- [ ] **`make ci` cannot pass on `main` — staticcheck has 10 findings and aborts the target**

  Measured 2026-08-17 by running `make staticcheck` at `origin/main` (detached) and on a
  feature branch and diffing the two lists: **10 findings, byte-identical on both**. The
  feature branch introduced none and removed none. Because `staticcheck` runs before
  `test-all-short` and `coverage-check-short` in the `ci:` target, `make ci` exits 1 on a
  clean checkout of `main`, and the two stages after it never run at all.

  Why this went unnoticed: GitHub CI merges PRs green, so whatever the required checks run,
  it is not this target. The documented local gate and the enforced remote gate disagree —
  which means "I ran `make ci`" currently proves less than it reads.

  The 10 findings (8 are dead code, 1 is a real nil-deref candidate):

  - `internal/metafetch/service_apply.go:637` — **SA5011 possible nil pointer dereference**,
    with the contradicting nil-check at `:662`. This is the one with a bug behind it.
  - `internal/plugins/maintenance/regroup_shattered_ai_test.go:180` — SA4006/SA4010, an
    `append` result that is never used. A test that discards what it builds.
  - U1000 unused: `dlIntPtr` + `dlInt64Ptr`
    (`internal/database/dataloss_preserve_invariant_test.go:26-27`), `(*Plugin).pathRepairDef`
    (`internal/plugins/itunes/path_repair.go:16`), `updatedBooks` field
    (`internal/plugins/maintenance/author_conjunction_repair_test.go:22`), `udRowByItem`
    (`internal/server/handlers/abs/userdata_test.go:332`), `errString`
    (`internal/server/handlers/metadata_cache.go:403`), `operationV2ToLegacy`
    (`internal/server/handlers/operations/handler.go:114`).

  Fix order that matters: triage the SA5011 first (it is the only one that can misbehave at
  runtime), then clear the U1000s, then decide whether staticcheck belongs in the required
  remote checks — a gate that only fails locally trains people to skip it.

### Applying metadata while a scan is running is silently reverted

There is no guard, warning, or queueing anywhere that stops a metadata apply from
racing an in-flight `library.scan`. When they overlap, the scan wins for a specific
set of fields and the user's apply is reverted with no error surfaced.

Hit live on 2026-08-17: a full `library.scan` was mid-run (≈19k of 30,562 books) when
metadata applies started. Books the scan had not yet reached were exposed.

**Mechanism** (`internal/scanner/scanner.go:2430-2452`). The rescan path is already
hardened against wholesale loss — an earlier data-loss bug wrote a partial `dbBook`
through full-replace `UpdateBook` and wiped fetched metadata, ratings and
transcriptions. The fix inverted it:

```go
merged := *existing                  // start from the COMPLETE existing row
applyScannerFields(&merged, dbBook)  // overlay ONLY scanner-authoritative fields
getStore().UpdateBook(existing.ID, &merged)
```

Anything outside `applyScannerFields` therefore survives by construction. The problem
is what is inside it. Each of these is overwritten whenever the scanner produced a
non-empty value (`if scanned.Title != "" { dst.Title = scanned.Title }`):

> `Title` `AuthorID` `SeriesID` `SeriesSequence` `Narrator` `Publisher` `Language`
> `ASIN` `WorkID` `OpenLibraryID` `HardcoverID` `GoogleBooksID`
> `Duration` `FileHash` `FilePath` `FileSize` `Format` `LibraryState` `Quantity`

`Title` is effectively always overwritten: when tags are empty the scanner falls back
to `extractInfoFromPath` (`scanner.go:1103`), so `scanned.Title` is virtually never
`""`. Provider IDs an apply just wrote (`ASIN`, `OpenLibraryID`, `HardcoverID`,
`GoogleBooksID`, `WorkID`) are in the overwrite set too.

Survives by construction: `Description`, `CoverURL`, `ISBN10`, `ISBN13`, `Edition`,
`PrintYear`, `AudiobookReleaseYear`, ratings, review status, quarantine, transcriptions.

**Two things that look like protection and are not:**

- `preserveExistingFields` — its own comment says it exists to "prevent rescan from
  wiping out data added by metadata fetch, AI parse, or manual edits", i.e. exactly
  this case. It has **one call site** (`scanner.go:2195`), inside the narrow branch
  where a book's file path moved. It is not on the general update path, and it omits
  `Title`/`AuthorID`/`SeriesID` regardless.
- The incremental-skip cache — a full run **deliberately disables** it
  (`scanner_reliability_test.go:99`: "an active full run must disable the shared
  incremental-skip cache"), so every book is processed and "unchanged file gets
  skipped" does not hold. `write_back_metadata` is `False` in prod anyway, so an
  apply never touches the file and could not mark it changed even if the cache were
  live.

**Proposed fix**, roughly in order of value:

1. Refuse or queue a metadata apply while a `library.scan` op is active, and say so in
   the UI. Cheapest, closes the hazard.
2. Narrow scanner authority: only claim a field when it was actually read from tags,
   not when it came from the `extractInfoFromPath` fallback. That fallback is a guess
   and should never outrank a fetched value.
3. Warn in the apply result when the applied book was re-scanned after the apply.

**Note for whoever picks this up:** the field list above was read at
`5dac7488`. Re-read `applyScannerFields` before relying on it — it is the kind of list
that grows silently.

### Compound narrator names are not split into individual narrators

A book narrated by two or three people is stored as one narrator whose *name* is
the whole credit string — "Michael Kramer & Kate Reading" is a single narrator
record, not two. Filtering, faceting, and "more by this narrator" therefore miss
every multi-narrator book, and the narrator list is polluted with entries that are
not people.

The schema is not the problem. `BookNarrator` (`internal/database/store.go:107`) is
a proper many-to-many join, `SetBookNarrators` exists, and `NarratorsJSON`
(`store.go:253`) carries a second tier into the summary projection. Nothing needs
migrating — the rows just never get created.

**Where it goes wrong: nothing splits at ingest.** `internal/metadata/metadata.go:266-269`
reads `PERFORMER` / `TXXX:NARRATOR` / `©nrt` into a single `metadata.Narrator`
string, runs `cleanTagValue` over it, and stores it whole. There is no split on that
path, so every scan and every metadata apply writes compound names straight through.

**The splitter that does exist is in the wrong place and too narrow.** The only
narrator-splitting code in the repo lives inside `OptimizeDatabase`
(`internal/server/handlers/operations/handler.go:276`, reporting a `narrators_split`
count). Three problems with relying on it:

1. **It is a manual maintenance op, not a rule.** It repairs history when someone
   runs it; the next scan re-introduces compound names immediately. Splitting
   belongs on the ingest path, with the op kept only as a backfill for old rows.
2. **It only splits on `" & "`.** `splitMultipleNames`
   (`internal/audiobooks/service_filtering.go:1086`) is `strings.Split(name, " & ")`
   and nothing else — so `"Kate Reading, Michael Kramer"`, `" and "`, `";"` and
   `" with "` are all left as one name. Comma-separated credits are the common case
   in real tags.
3. **It leaves two sources of truth.** `book.Narrator` keeps the compound string
   after the join rows are written, so callers reading the scalar and callers
   reading `book_narrators` disagree. Decide which is authoritative and say so.

Also note it walks `GetAllBooksCore(0, 0)` — the entire library in memory — in a
plain sequential loop, which is the shape CLAUDE.md's concurrency rule calls out. If
this gets promoted to a real backfill it needs a bounded worker pool.

**Suggested shape:**

- Split at ingest, in `internal/metadata`, so scans and applies both benefit.
- Widen the separator set beyond `" & "`: comma, `" and "`, `";"`, `" with "`, and
  narrator-specific noise like a trailing "(Narrator)". Be conservative — a name
  containing a comma ("Smith, John") must not be shredded, so prefer an explicit
  separator list over a generic tokenizer, and add fixtures for the ambiguous cases.
- Keep `OptimizeDatabase`'s pass as a one-time backfill for existing rows, using the
  same splitter rather than a second copy of the logic.
- Decide and document whether `book.Narrator` or `book_narrators` wins.

- [ ] **`OperationDef.Permissions` is enforced by nothing — and PR-3 is about to delete the code that *is* doing the enforcing**

  `internal/operations/registry/types.go:78` documents the field as "user perms required to
  trigger via API". Measured 2026-08-17: the **only** read of `def.Permissions` anywhere in the
  repo is `json.Marshal` at `internal/operations/registry/registry.go:509`, which writes it into
  an `op_definitions_v2` column. No handler, middleware, or registry path ever compares it against
  the caller. The v2 operations handler package contains zero references to it. It is a field that
  reads like a gate and behaves like a comment.

  The gate that actually exists is route-level and **uniform across every v2 op**:

  - `internal/server/wire_operations_routes.go:27` — `POST /operations/v2` requires
    `auth.PermScanTrigger`, whatever the op is.
  - `internal/server/maintenance_dispatcher.go:91-96` — the **v1** maintenance route requires
    `auth.PermSettingsManage`, or the job's own `PermissionAware.Permission()` when it implements
    one.

  Exactly one job implements `PermissionAware`: `bulkFetchMetadataJob`
  (`internal/maintenance/jobs/bulk_fetch_metadata.go:43` → `library.edit_metadata`).

  **The gap has a named role on each side.** From `internal/auth/seed.go:37-49`, the seeded
  `editor` role holds `scan.trigger` but **not** `settings.manage`. So an editor cannot run, say,
  `cleanup-backups` through the v1 maintenance route, but can run it through
  `POST /operations/v2` with op `maintenance.cleanup-backups`.

  **This is not a regression from PR #2533.** The `maintenance.job` bridge was registered on the
  same registry behind the same `scan.trigger` route and took the job as a `job_id` parameter, so
  the identical bypass existed with one generic door. What #2533 changed is that there are now 37
  named, enumerable, catalogue-listed doors instead of one door with a parameter — the gap is
  unchanged in kind but far more discoverable.

  **Why this is PR-3's problem specifically:** PR-3 retires the legacy v1 registry and dispatcher.
  The per-job enforcement at `maintenance_dispatcher.go:95-96` is *the only* per-job permission
  check in the system, and it lives on the code PR-3 deletes. Retiring v1 without first wiring
  `Permissions` into the v2 trigger path silently drops `bulk-fetch-metadata`'s
  `library.edit_metadata` requirement and leaves all 37 maintenance ops behind a blanket
  `scan.trigger`.

  Order that matters: enforce `def.Permissions` in `TriggerOperationV2` (falling back to the
  route-level permission when the slice is empty) **before** PR-3 deletes the v1 dispatcher — not
  after. Then the 37 `Permissions: settings.manage` declarations that
  `internal/server/maintenance_job_op.go` already writes become load-bearing instead of decorative,
  and `bulkFetchMetadataJob` needs its `PermissionAware` value threaded into its `OperationDef`
  rather than the hardcoded default.

  Instrument note: the first grep for readers of this field returned four hits that were all
  `role.Permissions` — a different type on the auth side. The finding is the count *after*
  separating the two types, not the raw grep.

### 🎯 Move intro transcription from per-book to per-file (LOW priority)

Stated goal 2026-08-17: **every file** should carry a Whisper transcription, to help
connect books and for other matching work. Today `maintenance.transcribe-book-intros`
(`internal/plugins/maintenance/intro_transcribe.go`) paginates **books** — 44,877 of
them, 42,884 already transcribed — while there are **532,296** `book_file` rows, so
per-file is roughly a 12× larger job.

Per-file storage already exists and is well shaped for it: `IntroTranscription` is on
`BookFile` (`internal/database/store.go:854`), and the parsed fields
(`TranscribedTitle` / `Author` / `Narrator` / `Translator` / `CoverArtist`) are
**retained** in the memdb core — only the raw transcript blob is stripped, because it
carries ~99% of the group's bytes (`internal/database/bookfilecore.go:84`).

Sizing notes:
- Transcription is a single remote faster-whisper endpoint (`WHISPER_REMOTE_URL`).
  `WHISPER_ENDPOINTS` supports a **pool** and is unset — that is the throughput lever
  if this is ever run at per-file scale.
- The GPU thermal block was lifted 2026-08-17 (cooling improved), but post-fix
  thermals are unmeasured; record temp and clock alongside any throughput figure.

Explicitly LOW priority — per-book is fine for now.

### Contributor data cleanup — follow-ups to `maintenance.purge-empty-authors`

- [ ] **Narrator equivalent of the empty-author purge.** There is no
  `DeleteNarrator` on the store at all — narrators live at `narrator:<id>` with no
  delete path, so the op cannot be written until that exists. Scope it alongside
  whatever decides the narrator identity question below.
- [ ] **Decide what the 822 zero-book-but-has-files authors actually are.** Measured
  2026-08-17: of 4,975 zero-book authors, 4,153 also have zero files (unambiguous
  junk, purgeable today) and 822 have files. A zero book count with files present
  looks more like a book that lost its junction entry than an empty author, so the
  purge op holds them back by default (`require_zero_files`). Someone has to look at
  a sample and decide before that flag is ever flipped.
- [ ] **Author↔narrator swap repair.** Measured lower bound: 1,052 names appear in
  BOTH the author and narrator tables; 67 of those are swap-shaped (narrates ≥5
  books, "authors" 1–2), accounting for ~96 book-author links. Ray Porter, Scott
  Brick, Nick Podehl and Andrea Parsneau all currently exist as authors. This is a
  LOWER BOUND — the rule only sees names present in both tables, so a swap whose
  "author" never appears as a narrator elsewhere is invisible to it. Route any
  repair through the review queue rather than blind-applying; this is far smaller
  than it looks from the UI, where the impression is driven mostly by the empty
  authors and (until #2512) the compound narrator entries.
- [ ] **`DeleteAuthor`'s junction cleanup is dead code.** It iterates the
  `book_author:` keyspace (singular). Nothing in the repo writes that keyspace — the
  live data is the per-book `book_authors:<bookID>` array — and the iterator bounds
  (`book_author:` → `book_author;`) exclude the plural form anyway. So deleting an
  author who HAS books leaves them referenced inside every `book_authors` array.
  Harmless for the empty-author purge (no references by definition), a real bug for
  any other caller.

### `repair-missing-files` tier 2 can repoint a row at another book's audio

`rmfr_repairOne` in `internal/maintenance/jobs/repair_missing_files.go` resolves a missing
`book_file` row through four tiers. **Tier 2 (`:292-339`) accepts a unique basename match with no
ownership check at all.**

```go
paths := idx[base]            // filename index across ALL search roots
switch len(paths) {
case 1:
    candidate, method = paths[0], "filename"   // ← :299-301, accepted unconditionally
```

The `default:` branch immediately below (`:304-337`) narrows multi-match candidates by parent
directory and then by author last name. The `case 1:` branch does neither. The asymmetry is the
bug: the code already encodes that a bare basename is insufficient evidence of identity, but
applies that knowledge only when the match is ambiguous. **One match is evidence of uniqueness,
not of correctness** — a singleton basename elsewhere in the search roots is no more likely to
belong to this book than one of several.

A hit rewrites the row via `UpdateBookFile` at `:566`, setting `FilePath`, `OriginalFilename`,
`Missing=false`, `FileSize` and `Format` — so the row ends up pointing at an unrelated book's
audio while *looking* fully repaired. There is no post-write verification of book identity.

**Reachability — measure the ROW side, not the DISK side.** Both numbers exist and only one bounds
the risk. All measurements by the prod-ops lane; recorded here as theirs.

*On-disk* (does the corpus contain singleton basenames at all): 4,082 files carry bare-digit
basenames across 517 distinct names, **170 of them singletons**. Controls: normal-named mp3s under
one author = 35; a planted nonexistent name = 0.

*Row-side* (do actual missing rows resolve to a singleton — the only way to reach `case 1:`):
building the same index tier 2 builds, 379,527 distinct basenames over both search roots, and
looking up every distinct basename from 260 sampled missing rows gives **1 singleton
("Dungeon of Pride.m4b") / 101 multi / 1 absent** (planted control ✓).

**So the on-disk figure overstates the risk and the row-side figure is the one to quote: ~1 in
102.** This corrects the first version of this fragment, which cited the on-disk numbers as
reachability — the same count-the-wrong-population error the audit header warns about.

The track-slash population specifically does **not** mis-repoint: `131.mp3` occurs **9** times, not
once (settled by direct `find` after two parses disagreed; known-good control `166.mp3` = 172,
planted bad control = 0). Nine occurrences reach the multi-match branch, which narrows by parent
directory — stored parent `Zero History - 2` matches none of the nine real parents — and falls
through to zero. **A miss, not a mis-repoint.**

⚠️ Consequence for prioritisation: this defect is real and worth fixing on its own merits, but it
is **not** what blocks the track-slash repair. Do not sequence the repair behind it.

Bare-digit basenames are common because of the `segment_title_format` slash bug (default
`{title} - {track}/{total_tracks}`, `f29c3ce6` → `c54721c7`, documented at
`internal/organizer/pathbuild.go:139-158`): a row reading `.../Zero History - 70/131.mp3` is one
filename, "track 70 of 131", whose `/` became a path separator. Its basename is `131.mp3`.

**These two findings compound, which is why neither should be fixed in isolation:**
`repair-missing-files` advertises `dry_run:true`, and per
`todo.d/20260817-resumerequeue-two-divergent-implementations.md` an interrupted dry run can come
back as a real run through the nil-params requeue path. A silent preview→apply transition on a job
whose tier 2 can repoint across books is a worse outcome than either defect alone. The prod-ops
lane declined to run this job as a prod dry run for exactly this reason and read the tiers
statically instead.

Fix direction: require tier 2's `case 1:` to pass the same parent-directory / author narrowing the
multi-match branch already applies, or reject the single match outright and let tiers 3-4 handle
it. Either way the accept path should carry a same-book assertion before `UpdateBookFile`.

Separately — and this does **not** fix the above — none of the four tiers resolves the track-slash
shape, so repointing that population needs new candidate logic rather than a wiring change:

- tier 1 (`:281`, iTunes PID → XML Location): organizer-tree rows have no `ITunesPersistentID`.
- tier 2 (`:292`): looks up `131.mp3`; the real file is `Zero History - 70.mp3`.
- tier 3 (`:341`, stem-prefix in same dir): `os.ReadDir` on the phantom parent — 25/25 distinct
  parents absent on the live tree, with three positive controls present in the same batch.
- tier 4 (`:366`, author + title-prefixed album dir): stats `<album>/131.mp3` (absent), and its
  single-audio-file fallback does not apply to books holding 130+ files.

`repair_missing_files.go` remains the right model for the *write* (`:566` field set) and for
dry-run-returns-a-plan (`res.Method` / `res.NewPath` per row); only the candidate search is unfit.

### `ResumeRequeue` has two live implementations that disagree about params

Two startup paths both implement "requeue", against different tables, and they do **not** agree on
whether the re-enqueued op keeps its original params:

| path | entry | params on re-enqueue |
|---|---|---|
| registry, walks **v2** rows | `Registry.Start` → `resumeAfterStartup` → `resumeRequeue` (`internal/operations/registry/resume.go`) | `Params: row.Params` — carried forward |
| server, walks **v1** rows | `resumeInterruptedOperations` → `resumeV2Op` (`internal/server/server_lifecycle.go:122-127`) | `EnqueueOp(ctx, opType, nil)` — **literal `nil`** |

The `nil` is deliberate and commented (`server_lifecycle.go:103-108`): the concrete params type is
not known at that call site because `LoadParams` is generic over `T`. Harmless for an op whose
restart semantic is "do the whole thing" (e.g. `library.scan`). **Not** harmless for any op with a
`dry_run` parameter — `DryRun` unmarshals to Go's zero value `false`, silently converting an
interrupted preview into a real mutation. That is the same defect class as the
`SaveParams`/`dry_run` bug that `maintenance_dispatcher.go:180` already exists to prevent.

**19 ops declare `ResumeRequeue`** today (10 under `internal/plugins/dedup/`, 4 under
`internal/plugins/maintenance/`, plus acoustid/deluge/itunes), so the branch is live code rather
than dead.

✅ **MEASURED 2026-08-23 (was "Unverified"): `resumeV2Op` is unreachable for maintenance, and now
for everything.** Reaching it takes a v1 `operations` row whose `Type` resolves via
`opRegistry.Def()`. For maintenance that is structurally impossible: v1 rows are typed
`maintenance:<job>` (colon) while v2 defs are `maintenance.<job>` (dot), and `RegisterOp` *rejects*
ids containing `:`. More broadly, PR #2784 retired the last live `CreateOperation` call site, so
**nothing mints a v1 row at all** — `resumeV2Op` has one caller, fed only from
`GetInterruptedOperations()`, which now returns only pre-deploy rows. Latent trap, not an active
bug. The `nil`-params defect above is real but no longer reachable on this path.

~~Blocks the `ResumeRequeue` upgrade for 5 of the 6 `CanResume`-but-checkpointless maintenance
jobs~~ — **RESOLVED 2026-08-23.** All five (`bulk-deluge-import`, `cleanup-empty-folders`,
`refetch-missing-authors`, `repair-missing-files`, `scan-composer-tags`) now declare
`ResumeRestart`, which updates the row **in place** and so never reconstructs params — the
`nil`-params hazard cannot apply to it by construction. This was not optional: PR #2784 deleted
`resumeLegacyOp`'s default branch, which had been their only working resume, so leaving them
`ResumeDrop` meant they resumed never. `bulk-fetch-metadata` moved `ResumeRequeue` → `ResumeRestart`
in the same change, because its skip-set is keyed on the op id and requeue was moving its own
resume anchor. `gatedByDryRun` in `internal/maintenance/jobs/policy_declaration_test.go` is now
empty.

Two source-level defects were fixed alongside, because `ResumeRestart` was otherwise
knowingly wrong for these jobs. The watchdog's `uncheckpointed` strike gated on
`ResumePolicy` while its own comment claimed it gated on `MinCheckpointInterval`, so
it fired against every `ResumeRestart` op forever — including the nine maintenance
jobs and `metadata.candidate-fetch`, none of which can checkpoint at all
(`maintenance.ProgressReporter` has no `Checkpoint` method) — into a strike table
that has an `InsertOpStrikeV2` and **no reader anywhere**. And `high_water_progress`
was written only by `UpdateOpCheckpointV2`, so it stayed 0 for those same ops and
`checkInfiniteRestart` force-dropped them at `resume_count>=3` regardless of work
done. Both now gate on what the def actually declared.

⚠️ **Scope bound, measured while making the above change:** a correct `ResumePolicy` is
only consulted on one path. `resumeAfterStartup` takes its candidates from
`ListActiveOperationsV2()` = the `opv2:act:` index = `queued|running`, and **every** clean
shutdown writes a status that deletes that key (clean drain → `canceled`; shutdown timeout
→ `interrupted_quiesced`; worker abandonment → same). So a job stopped by a deploy is
invisible to the sweep no matter what it declares, and only a hard kill leaves a row the
sweep can act on. Pre-existing, affects every v2 op, and it is the v2 twin of the v1 bug
already fixed in `isResumableOpStatus`. Tracked in
`todo.d/20260823-v2-resume-sweep-is-blind-to-interrupted-rows.md` — do **not** fix it in a
maintenance PR.

⚠️ Do not confuse `internal/maintenance/jobs/repair_missing_files.go` (job `repair-missing-files`,
one of the 37, **repoints**, zero delete calls) with
`internal/plugins/maintenance/missing_file_repair.go` (op `maintenance.missing-file-repair`,
already v2-native, **deletes** via `DeleteBookFilesByIDs`). Near-mirror-image filenames, opposite
mutations, different lanes.

Fix direction: resolve the divergence rather than test around it — have `resumeV2Op` read the v1
row's saved params and pass them through, so both paths replay params identically. Then add a
conformance test: one fixture, both implementations, assert the resumed params are equal.

**Compounds with a second defect in the same job.** `repair-missing-files` tier 2 accepts a unique
basename match with no same-book check (`repair_missing_files.go:299-301`), so it can repoint a row
at an unrelated book's audio — see
`todo.d/20260817-repair-missing-files-tier2-cross-book-repoint.md`. A silent preview→apply
transition on *that* job is worse than either defect alone, and it is why the prod-ops lane read
the tiers statically rather than running a prod dry run to test them. Fix the params divergence
before anyone is asked to trust a dry run of a repointing job.

- [ ] **Store-parameter narrowing: 54 declarations remain.** Re-measured 2026-08-17 by AST.
      Supersedes the earlier "24 remain" fragment, which was wrong — the method count (7)
      was right, the free-function count was low by 3, and it counted only the maintenance
      packages. Corrected totals:
      - **Maintenance: 8 left** of 27. The 19 `OK`-tier (no propagation) declarations in
        `internal/plugins/maintenance` are done. The remaining 8 are `PROP`-tier — their
        callees must be narrowed first or propagation re-widens them:
        `firstAudioFile`, `linkProbedFolder`, `relinkOne`, `vgFixAuthorDirPath`,
        `ApplyMultidisc`, `migrateOne`, `ddMergeDuplicateBook`, `processTranscribePage`.
      - **Outside maintenance: 65** across 24 packages. Largest: `internal/server` 12 +
        `internal/server/handlers` 6, `internal/dedup` 6, `internal/versions` 5,
        `internal/reconcile` 4, `internal/plugins/acoustid` 4, `internal/metafetch` 4.
      - **30 of those 65 do not need narrowing at all — the `database.Store` parameter is
        entirely unused.** Delete the parameter instead. (138 declarations repo-wide have an
        unused store param; 66 are `internal/database` migrations whose signature the runner
        fixes, so those stay.)
      Not narrowable and excluded from every count above: 37 `MaintenanceJob.Run` methods
      (an interface method's parameter type is fixed for all implementers) and the migration
      runner's signature.
      Pattern guidance: **B (narrow interface) by default** — it is one line per site and
      changes zero call sites. **Do not sweep C** (split-the-decision); see
      `.claude/notes/2026-08-17-option-b-vs-c-comparison.md`.

### Missing-file lane — follow-ups after the report-only change (#2614)

- [ ] **Run the classify pass in prod** and record the numbers.
      `POST /api/v1/operations/v2 {"def_id":"maintenance.missing-file-audit","params":{"classify":true}}`.
      This is the first figure that actually sizes the recoverable population — the
      earlier sample could not, because it is clustered by iteration order. Off by
      default; it doubles the stat load on the NAS, so do not run it during a scan.
- [ ] **Build the re-point repair.** It must UPDATE `file_path` to the flat name the
      classify pass derived, never delete a row. The tombstone comment at the bottom of
      `internal/plugins/maintenance/missing_file_repair.go` says so at the site. Gate it
      on the classify pass having run clean (controls unresolved) for the rows it touches.
- [ ] **Decide what happens to the 16,265 fully-broken books** (every file entry dead).
      Still untouched, still needs a human call. They are now structurally impossible to
      delete by accident.
- [ ] **Missing-file audit Phase 1a still has no PR and is not mutation-tested.**
      Committed as `9b43f598` on `feat/persist-missing-file-verdict` (`.worktrees/auditpersist`).
      Either finish it or delete the branch — a committed-but-unmerged change to an op
      that runs against prod is the worst of both states.

- [ ] **`database.Store` is grouped, not yet unreachable.** `.interface-width-baseline`
      is at 0 and `Store` declares six domain composites instead of forty embeds, but it
      still transitively carries all 398 methods and the six composites are only a
      relabelling. The actual split is still the plan of record —
      `docs/plans/2026-08-19-split-the-pebblestore-surface.md`. Do not read the 0 as
      that job being done.

- [x] **GOFMT-SWEEP** `gofmt -l` reported **43 unformatted Go files across 24
      packages** on `main` (measured 2026-08-20, excluding `web/`). Root cause was
      the same one behind `sdkguard` and the bench build: **`gofmt` was verified
      nowhere** — `grep -rn 'gofmt' .github/workflows/ Makefile` returned zero
      hits, so there was no format check in CI and no `make fmt`/`fmt-check`
      target, and drift accumulated silently.

      **Done.** Both steps landed together, in the required order: the 43 files
      were swept, and only then did a `make fmt-check` target join `make ci` and
      the CI job (renamed `Repo Guards`, since it now covers three checks). The
      gate could not have preceded its own sweep without being red on 43
      pre-existing files.

      Verified semantically inert: `gofmt` is idempotent on the result, and all
      24 affected packages pass `go test -short` (22 with tests, 2 without).
      Note the sweep was **not** whitespace-only, as first assumed — alongside
      indentation, `gofmt` split `stmt; os.Exit(1)` onto separate lines, expanded
      inline struct definitions, and normalised doc comments to the Go 1.19+
      heading form. `git diff -w` was therefore not empty; the tests are what
      establish inertness, not the whitespace-ignoring diff.

- [ ] **AudiobookShelf-compatible API: series are broken, and collections/playlists are
      empty stubs.** Owner report 2026-08-09: *"series are broken on the audioshelf server
      stuff, because all of them report zero books, and when you click on them they just
      give you a random list of books… We need full collection support… Same with
      playlists."* Root causes located in the code below — this is server-side, as the
      owner suspected.

      > **STATUS 2026-08-14:** §1 (series) — the list now embeds `books` (2026-08-13
      > fixes; a residual `numBooks>0` with `books: []` defect on prod is tracked in the
      > 2026-08-14 task breakdown as B20). §3 (playlists) — SHIPPED in #2366
      > (`h.LibraryPlaylists` + `GET /api/playlists/:id`). **Remaining: §2 collections**
      > — still `h.EmptyPage`, the only stub route on the ABS surface; see
      > `docs/reference/abs-implementation-status.md`.

      ## 1. Series report zero books and open the wrong list

      `internal/server/handlers/abs/browse.go:464` `LibrarySeries` builds each series DTO
      with:

      ```go
      "books":         []any{},          // <- ALWAYS EMPTY, hardcoded
      "totalDuration": 0,                // <- likewise
      "numBooks":      counts[s.ID],
      ```

      Two distinct defects, and they explain both halves of the report:

      **(a) `books` is hardcoded empty.** The client is handed a series with no members.
      "Click a series and get a random list of books" is the client doing something
      reasonable with nothing — most ABS clients fall back to an unfiltered library query
      when the series carries no items. The books are not random; they are *the library*.

      **(b) `numBooks` comes from `GetAllSeriesBookCounts()`, whose error path is
      silent:** — ⚠️ **instrumented 2026-08-22 (PR #2699, TASK-089): the `slog.Warn`
      this asked for now exists**, so a failing count query is no longer
      indistinguishable from an empty library. The *behaviour* is unchanged and
      deliberately so — the fallback still reports 0. Defect **(a)** (`books` hardcoded
      empty) is untouched, so this parent item stays open.

      ```go
      counts, err := h.library.GetAllSeriesBookCounts()
      if err != nil {
          // "not worth failing the page over; report 0 books rather than 500"
          counts = map[int]int{}
      }
      ```

      If that call errors, **every** series reports 0 — which is exactly the symptom.
      The fallback is defensible as a design choice but it is **unobservable**: there is no
      log line, so a total failure of the count query looks identical to a library with no
      series members. Whatever the fix, add a `slog.Warn` here; a silent zero is how this
      went unnoticed. (It is also possible the counts are keyed differently from `s.ID` —
      check that before assuming the error path fired.)

      **Do:** populate `books` (at minimum the item IDs/minified items the ABS schema
      expects), fix or instrument the count path, and verify against a real client rather
      than by reading the JSON — the two failure modes look the same from the payload.

      ## 2. Collections are a stub

      `internal/server/handlers/abs/handler.go:386`:

      ```go
      r.GET("/api/libraries/:libraryId/collections", auth, h.EmptyPage)
      ```

      The route exists and answers 200 with an empty page. Nothing behind it.

      **Wanted** (owner): real collections — *"we may want to make a collection of scifi
      books that don't have stupid characters"*. That is a **user-curated, arbitrary set**,
      not a saved query: the membership rule ("no stupid characters") is a judgement the
      user makes per book and cannot be expressed as a filter. So this needs persisted
      membership, not a dynamic query.

      Needs: storage for collection + ordered membership, CRUD endpoints, and the ABS
      collection DTO shape on `GET /api/libraries/:id/collections` (and the single-collection
      and add/remove-item endpoints the clients call).

      ## 3. Playlists are the same stub

      `internal/server/handlers/abs/handler.go:387` — also `h.EmptyPage`.

      **Note the overlap:** `todo.d/20260805_214200_playlists_full_support.md` (already
      folded into `TODO.md`) covers playlists broadly — import of `.m3u`/`.m3u8`, static and
      **dynamic** (stored-query) playlists, and their value as grouping evidence. **This
      item is narrower and additive:** whatever that work builds must also be *served over
      the ABS API*, because today the endpoint returns empty regardless of what exists
      internally. Do not duplicate the design — extend it with the API surface.

      ## Shared design note

      Collections and playlists are close cousins (an ordered set of items with a name) and
      the ABS schema treats them similarly. Worth designing the storage once with a
      discriminator rather than twice — but **check the ABS DTOs first**, because clients
      distinguish them (playlists carry playback semantics, collections do not) and
      returning the wrong shape produces exactly the class of silent client-side weirdness
      seen in §1.

      **Acceptance:** in a real ABS client — series show correct counts and open their own
      books; a hand-made collection appears and lists its members; a playlist likewise.
      Verified in the client, not by curling the endpoint.

- [x] **CORRECTED and FIXED — this was reported as an active crash and it was not.**

      ## What the original entry claimed

      > The Authors page crashes on any author record without `aliases`. `Authors.tsx:89`,
      > `:120`, `:121` read `a.aliases.length` unguarded — one bad row takes the whole page
      > to the error boundary. **Reachable from a real API response that omits or nulls the
      > field.**

      The first half is true. **The last sentence is not, and it is the part that made this
      read as urgent.**

      ## What is actually the case

      `Authors.tsx` fetches from exactly one place — `api.getAuthorsWithCounts()` — and the
      handler behind it has guarded the field since **2026-03-10**, five months before this
      was filed (`internal/audiobooks/author_series.go:108`):

      ```go
      aliases := aliasesByAuthor[a.ID]
      if aliases == nil {
          aliases = []database.AuthorAlias{}   // never marshals to null
      }
      ```

      A Go nil slice marshals to JSON `null`, and `null.length` throws — so the concern was
      the right shape. But the only endpoint feeding this page has been returning `[]`
      rather than `null` all along. **The page was not crashing, and there was no "real API
      response" that would make it crash.**

      The original entry was written from reading the frontend and reasoning about what the
      backend *might* send, without checking what it does send. That is the same
      reason-instead-of-measure error that produced four wrong diagnoses during the
      2026-08-09 CI work.

      ## What was still worth fixing

      The frontend fragility is real even though nothing currently triggers it. TypeScript's
      `aliases: AuthorAlias[]` is a **compile-time claim about runtime data from an HTTP
      response** — it validates nothing. One new endpoint returning `AuthorWithCount`
      without that nil guard, or one API shape change, and the page dies at the error
      boundary.

      So the six reads in `Authors.tsx` are now guarded (`a.aliases?.length ?? 0`,
      `(a.aliases ?? []).map(...)`, etc.). Behaviour is identical when the field is present,
      which it always is today.

      ## Corrected elsewhere

      The overstated claim also appears in `docs/audits/2026-08-09-e2e-repair-and-ui-regressions.md`
      (finding 3) and the 2026-08-09 executive summary ("a page that crashes outright if a
      single author record is missing one optional field"). Both are corrected in the same
      change.

      **The lesson worth keeping:** "unguarded field access" is a real code smell, but
      "therefore it crashes" is a claim about the *server*, and needs the server checked.
      Severity asserted from one side of an API boundary is a guess.

- [ ] **`book-detail.spec.ts` "soft delete, restore, and purge flow" fails only in the full
      parallel suite, never in isolation.** Surfaced 2026-08-09 as the last remaining
      failure after the e2e repair took the suite to 551 passed / 1 failed / 16 skipped of
      568 across chromium + webkit.

      **This is deliberately NOT fixed.** It is not spec rot — the test passes 6/6 alone —
      so changing the test to tolerate it would be papering over an unknown, and unlike the
      webkit pagination flake there is **no measurement yet establishing the app is
      correct**. Per the no-papering-over rule this gets written up and left red.

      **The failure:**

      ```
      [webkit] › book-detail.spec.ts:423 › soft delete, restore, and purge flow
      Error: expect(page).toHaveURL(expected) failed
        Expected pattern: /\/library$/
        Received string:  "http://127.0.0.1:8484/dashboard"
        - unexpected value "http://127.0.0.1:8484/login"
      ```

      After "Purge Permanently" the test expects `/library`. Instead the page went to
      `/login` and settled on `/dashboard` — the signature of an auth guard firing, not of
      a broken navigation.

      **What has been ruled out (each by measurement, not reasoning):**

      | hypothesis | result |
      |---|---|
      | The test itself is stale / selector drift | **No** — 6/6 passes on webkit in isolation, `--repeat-each=6` |
      | `auth-flow.spec.ts:90` pollutes shared server state by creating an admin account | **No** — that test `test.skip`s itself unless `requires_auth && !has_users`, and it skipped in every run examined. Confirmed by arithmetic: the full run's 16 skips = 7 `test.fixme` × 2 browsers + this bootstrap test × 2 browsers. It never executed, so it mutated nothing |
      | Reproducible by pairing the two specs under parallel workers | **No** — `book-detail` + `auth-flow`, `--repeat-each=4`, webkit: 24 passed / 4 skipped |

      **What is still open.** The suite runs `fullyParallel: true` with `workers: 2`
      (`playwright.config.ts:18-20`) against a **single shared Go server on :8484**. Every
      spec mocks at the browser layer (`page.route` or a `window.fetch` patch), but the
      server underneath is common to all of them. Something in a concurrently-running spec
      plausibly moves real server auth state — but the obvious candidate is now excluded,
      so the actual polluter is unidentified.

      **The artifact was lost, and that is the main obstacle.** Playwright clears
      `test-results/` at the start of every run, so the `error-context.md` and trace from
      the failing run were overwritten by the isolation re-runs before they were read. That
      is the one procedural mistake here: **read the artifact before re-running.** A repeat
      full-suite run with `--trace=retain-on-failure` is the way to recapture it.

      **Next steps, in order:**

      1. Re-run the full suite with `--trace=retain-on-failure` until it reproduces, and
         read `test-results/*book-detail*/error-context.md` **first**. That artifact
         discriminates the two live possibilities and a pass/fail count cannot: was the
         `/login` hop a client-side route guard, or a document load? Did `/auth/status`
         return something different from the mock?
      2. If it is shared-server auth state, the fix is isolation, not tolerance — either a
         per-worker server, or a fixture that asserts the server's auth posture is
         unchanged at test start.
      3. **Frequency: 1 occurrence in 1,136 test executions.** A second full-suite run with
         `--trace=retain-on-failure` came back **552 passed / 0 failed / 16 skipped, exit
         0** — the whole suite green on both browsers, and this test among them. So it did
         not reproduce, no artifact was captured, and the rate is at most ~0.1% of runs of
         this test.

         That changes the priority but not the conclusion. It is rare enough that it should
         **not** block calling the suite green, and rare enough that hunting it by repeated
         full-suite runs is poor value. The right trigger is opportunistic: the next time
         CI or a local full run goes red on this test, **read
         `test-results/*book-detail*/error-context.md` before doing anything else** — that
         is the artifact that was lost the first time and it is what discriminates the
         remaining possibilities.

      **Do not** add a retry, a URL tolerance, or a `test.fixme` to this test on the
      strength of "it passes alone."

- [x] **Change Log rows lost their visible "Compare snapshot" affordance and are
      mouse-only.** *(DONE 2026-08-23 — TASK-090, PR #2807. Took the FIRST option
      below: a real sibling `<Button>` in the Actions stack next to Revert. The
      SECOND option this entry offers — `role="button"` + `tabIndex={0}` +
      `aria-label` on the row — was investigated and is **actively wrong here**,
      so do not "improve" it back. A `role="button"` element carries "Children
      Presentational: True" per the ARIA spec, so nesting the row's real
      interactive controls (Revert, and now Compare snapshot) inside one is
      undefined for assistive tech; and an `aria-label` on the row would
      override the accessible name computed from its own text, making every
      actionable row announce as just "Compare snapshot, button" and nothing
      else. The row keeps its `onClick` as a mouse-only convenience. This
      entry's own closing warning about double-firing was the real risk and is
      pinned by test: 4 tests cover Tab-reachability, Enter AND Space
      activation, absence on non-actionable rows, and that Enter on Revert does
      not fire `onCompareSnapshot`. Mutation-verified 2/2 — dropping Revert's
      `stopPropagation` and rendering the button unconditionally each fail,
      control green.)*
      `web/src/components/ChangeLog.tsx:135-154` renders each entry as a
      plain `<Box onClick={...}>` that fires `onCompareSnapshot` for `tag_write` /
      `metadata_apply` entries. There is no `role`, no `tabIndex`, no keyboard
      handler, and no label — the old "Compare snapshot" link that used to sit in the
      row was removed. The flow itself still works end-to-end (verified in
      `web/tests/e2e/files-history.spec.ts`: clicking the row does raise
      `snapshot-comparison-banner` in the open format tray), so this is purely a
      discoverability/accessibility gap, not a broken feature. Deciding what replaces
      it is a product call: restore a visible link/button, or keep the row click and
      give it `role="button"` + `tabIndex={0}` + Enter/Space activation + an
      `aria-label`. Note the row already contains a Revert `<Button>` that calls
      `stopPropagation`, so any keyboard handler has to not double-fire there.

- [ ] **Dead `expanded` state in `TagComparison`.** `web/src/components/TagComparison.tsx:69`
      is `useState(true)` and `setExpanded` is never called, so the `<Collapse in={expanded}>`
      at line 249 is always open. Either drop the state and the `Collapse`, or wire up the
      toggle that was evidently intended (the e2e suite still had a `tag-comparison-toggle`
      testid assertion for it until 2026-08-09).

- [ ] **Delete the unreachable "Bulk Fetch Metadata" dialog and its handler.**
      `web/src/components/library/LibraryDialogs.tsx:920` renders
      `<Dialog open={bulkFetchDialogOpen}>`, but `setBulkFetchDialogOpen(true)` appears
      **nowhere** in `web/src` — the state is initialised to `false` at
      `web/src/pages/Library.tsx:352` and is only ever set back to `false` (by
      `handleCancelBulkFetch`). The dialog can never open. `handleBulkFetchMetadata`
      (`Library.tsx:1218`), the `bulkFetchProgress` state, and the props threaded
      through `LibraryDialogs` for them are reachable only from that dead dialog.
      The flow it belonged to was replaced: **Fetch Selected** now calls
      `batchFetchCandidates` and toasts "Click Review when complete", and a separate
      **Review** button opens the candidates dialog once the cache is populated. Five
      e2e tests covering the old synchronous progress dialog were deleted on
      2026-08-09 rather than rewritten, since rewriting them against the new
      async flow would be new coverage rather than repair. Removing the dead code is
      a separate change from the e2e repair and was deliberately not bundled with it.

- [x] ~~**Audit `setupMockApi` for more branches shadowed by earlier prefix catch-alls.**~~
      — closed 2026-08-22 (PR #2710, TASK-093): audited all 10 `startsWith()` catch-alls,
      **0 shadowed branches** found. Branch census reconciles exactly (67 exact + 10
      catch-all + 21 + 3 = 101 = every `pathname` condition). Both detectors were verified
      against a deliberately-broken copy (exit 1) and the real file (exit 0), so this is
      evidence of absence, not a dead check. No dispatcher change needed. Original text:
      `web/tests/e2e/utils/test-helpers.ts` had `pathname === '/api/v1/audiobooks/batch'`
      sitting *below* `pathname.startsWith('/api/v1/audiobooks/') && method === 'POST'`,
      so every batch update silently got the generic `{ message: 'OK' }` back and
      Library's toast read "Updated metadata for 0 audiobooks." Fixed 2026-08-09 by
      moving the specific branch above the prefix one, but the same ordering hazard
      applies to every other `startsWith` catch-all in that dispatcher — a specific
      branch placed after one is dead and fails silently rather than loudly. Worth one
      pass to confirm no others are shadowed.

- [x] **FIXED (#2267).** **Edit Metadata shows Year and ISBN-13 as empty boxes whatever is stored — and the
      obvious fix corrupts `print_year`.** `mapBookToAudiobook`
      (`web/src/pages/BookDetail.tsx:762`) builds the object handed to
      `MetadataEditDialog` and omits `year`, `isbn10` and `isbn13`. `genre` had the same
      problem and was fixed on 2026-08-09; the other three were deliberately left alone,
      because they are not equally safe.

      `genre` was safe because it does not appear in the payload `handleEditSave` builds,
      so populating it cannot change what a save writes. **Year is not.** The dialog seeds
      its Year box from `audiobook.year`, and `handleEditSave` computes:

      ```ts
      payload.print_year = updated.year || book.print_year;
      ```

      So mapping `year: current.audiobook_release_year` would make every save overwrite
      `print_year` with the audiobook release year — on books the user never touched the
      Year field of. Two genuinely different dates (`print_year`, the original
      publication; `audiobook_release_year`, when the recording came out) collapsing into
      one is silent metadata corruption across the library.

      Fixing the display therefore means untangling that precedence first: decide which
      date the dialog's single "Year" box represents, and have the save path write only
      that one. `Audiobook` already carries `print_year` and `audiobook_release_year` as
      separate fields (`web/src/types/index.ts:16-17`) alongside the legacy `year`, so
      the type is not the obstacle.

      ISBN is a smaller version of the same shape: the payload does
      `isbn: updated.isbn13 || updated.isbn10 || book.isbn`, which currently falls through
      to `book.isbn` precisely *because* the mapped object has neither. Populating them
      changes which field wins.

      `tests/e2e/metadata-provenance.spec.ts` carries a `test.fixme` covering this, so it
      will start failing (loudly, as an unexpected pass) the moment it is fixed.

      > ### What it actually was — worse than a blank box
      >
      > The dialog has ONE "Year" box, declared as `audiobook_release_year` in
      > `FIELD_TO_API`. But `handleEditSave` fed `updated.year` into **two** fields:
      >
      > ```ts
      > audiobook_release_year: … || updated.year || …,
      > print_year:             updated.year || book.print_year || undefined,
      > ```
      >
      > `print_year` is when the **book** was first published; `audiobook_release_year` is
      > when the **recording** came out — decades apart for a classic. So typing a year in
      > that dialog silently replaced the original publication year with the audiobook's.
      > Same corruption class as the 2026-07-13 write-up, still live on this path. The blank
      > box masked it for *display* but not for *writes*.
      >
      > Fixed in the safe order: remove the bad write first (`print_year` is now
      > preserve-only — the dialog has no print-year field, so nothing there should change
      > it), which then makes seeding the box safe. Doing it the other way would have turned
      > a latent corruption into one firing on every save.
      >
      > **And the blank box had a second, separate cause:** the e2e fixture supplied
      > `year: 2024`, a field the Go API never emits (`bookcore.go:44-45` has `print_year`
      > and `audiobook_release_year` only). The dialog was correctly reading
      > `audiobook_release_year` and finding nothing. Mock rot, not app behaviour.
      >
      > `test.fixme('year and ISBN-13 populate in the edit dialog')` is now passing:
      > metadata-provenance 13 passed / 0 failed / 0 skipped, exit 0.

- [x] **The Library fetched page 1 twice on every mount — FIXED.** Found 2026-08-09 while
      chasing three flaky `library-browser.spec.ts` pagination tests on webkit. On a large
      library that is a second full page query for nothing, on every single load.

      **Cause.** `SearchBar` re-parsed its value on mount and handed back a NEW
      `ParsedSearch` object that was semantically identical to the one `Library.tsx` had
      seeded its state with. Storing it changed `parsedSearch`'s *identity* → recreated
      `buildFieldFilters` → recreated `loadAudiobooks` → re-fired the "load when filters
      change" effect. Confirmed by instrumenting the dependency array:
      `DEPCHANGE buildFieldFilters,parsedSearch` fired once after mount on 4 runs of 4, and
      stopped firing entirely once the setter bailed on an unchanged value.

      **Fix** (#2241): `Library.tsx` now wraps the setter so an equal value keeps the
      previous object reference. `/api/v1/audiobooks` is hit exactly once per mount, 4 runs
      of 4.

      This belongs with the other client-side over-fetching in
      `todo.d/20260809-search-drops-filters-and-debounce.md` — that one is ten queries per
      search, this was two per page load.

      ---

      **TWO CORRECTIONS, both worth reading — this fragment was wrong twice.**

      **Correction 1.** The original fragment claimed the duplicate fetch *caused* the
      swallowed pagination click: the re-render from the second response was said to detach
      the button mid-click. Measured like-for-like over 24 webkit runs of the same three
      tests, eliminating the duplicate moved the failure rate **16/24 → 11/24**. A real
      improvement, but the flake survived, so the causal claim was at best incomplete.

      **Correction 2 — and this is the one that matters.** The residual flake is **not a
      product defect at all.** It is a Playwright/webkit harness artifact. Five probes:

      | probe | result |
      |---|---|
      | failing run instrumented | previous-page click → no request, no URL change (swallowed) |
      | click twice | first swallowed, second works — looked like a stale closure |
      | same probe on chromium | first click works; webkit-specific |
      | Playwright click vs in-page DOM `.click()` | **Playwright 4/4 failed, DOM 4/4 passed** |
      | both clicks via in-page DOM | **6/6 clean** |

      The application handles pagination correctly. Playwright's *synthesised pointer
      event* on MUI's `PaginationItem` is what is unreliable on webkit — the same DOM, the
      same handlers, the same React state, driven by a real DOM click, works every time.

      **So the tests were made robust** (`clickPagination` helper in
      `library-browser.spec.ts`): re-check the URL, click, assert, and retry once. This does
      **not** violate the no-papering-over rule, because the measurement establishes there
      is no product bug to paper over. Result: **24/24 on webkit**, against an 11/24 failure
      baseline.

      **What the helper does NOT protect against — stated plainly, because the first draft
      of this fragment claimed the opposite.** It originally said the helper "still fails
      loudly if the app ever genuinely stops responding, since the second click would be
      swallowed too." That is **false**, and falsified by the very probe above: the observed
      webkit signature is *first click swallowed, second click works*. A product regression
      with that same shape — pagination responding only on every other click — would be
      silently masked. The helper is not a detector for that case.

      What it does still catch: a control that is missing, disabled, covered, or entirely
      unresponsive, since both clicks fail and actionability checks are deliberately
      preserved (no `force: true`, no `dispatchEvent`). And to keep the masking observable
      rather than invisible, **every retry logs a `[clickPagination]` warning and pushes a
      `flaky-click-retry` annotation** — so a rising retry rate in CI is the signal that
      something changed, even though the suite stays green.

      **The lesson, which is the reusable part:** three separate causal hypotheses were
      filed here with confident evidence attached, and two of them were wrong. Each was
      killed by a measurement, never by reasoning. Before filing a UI flake as a product
      defect, do the Playwright-click-vs-DOM-click A/B — it is two minutes and it separates
      "the app is broken" from "the driver cannot press this particular button."

- [ ] **You cannot sort the library from the UI.** The "Sort by" and "Order"
      comboboxes are gone. `SearchBarProps`
      (`web/src/components/audiobooks/SearchBar.tsx:124-131`) has no `onSortChange`
      prop at all, and `web/src/components/library/LibraryBookGrid.tsx:133` receives
      the handler as `_handleSortChange` — underscore-prefixed to mark it deliberately
      unused. Everything downstream still works: `Library.tsx` holds `sortBy`/`sortOrder`,
      writes them to the URL as `sort`/`order`, restores them on load, and passes them
      to the API. So sorting is fully functional and completely unreachable — the only
      way to change it is to hand-edit the URL.
      `SearchBar.test.tsx:43` asserts "does not render sort controls when `onSortChange`
      is absent", which now passes vacuously since the prop cannot be supplied.
      Four `library-browser.spec.ts` tests were repointed at the URL on 2026-08-09 so
      the sort *behaviour* stays covered while the control is missing.
      **Was this intentional?** If so the dead state and the vacuous unit test should
      be cleaned up; if not, the control needs restoring.

- [x] **RESOLVED — it was worker contention, not a defect.** This fragment previously
      claimed "a MUI Select's menu does not close on the ubuntu runner — suspected REAL
      defect". **That was wrong.** Kept rather than deleted, because the sequence of wrong
      answers is the useful part.

      ## What it actually was

      Two chromium tests failed on ubuntu and passed on macOS, both 30s `locator.click`
      timeouts with a MUI modal backdrop still intercepting pointer events. Measured in the
      official Playwright linux image pinned to 2 CPUs:

      | configuration | result |
      |---|---|
      | `--workers=2` | `library-browser` + `scan-import` **FAIL** (3 separate runs) |
      | `--workers=1` | **27 passed, 0 failed** |

      Two browser workers plus the Go server on two cores starve the close **transition**,
      so the backdrop outlives any timeout worth setting. The menu does close; the machine
      is simply too busy to animate it. **Neither the app nor the tests are wrong** — a real
      user is not running two headless browsers on two pinned cores.

      **Fix:** `workers: process.env.CI ? 1 : 2` in `playwright.config.ts`, with the
      measurement recorded inline. Costs wall-clock (chromium ~4.5min → ~9min), which is the
      right trade for a gate meant to block merges.

      ## Four wrong answers, and what killed each

      Worth keeping, because every one of them looked well-evidenced at the time:

      | # | hypothesis | killed by |
      |---|---|---|
      | 1 | MUI close-transition race → add `waitForMenuClosed` at all 18 option sites | CI: failure count unchanged at 3, failures merely moved to the new wait |
      | 2 | The Selects are `multiple`, so the menu stays open by design | Reading `FilterSidebar.tsx` — they are single (`:143`, `:181`); the only `multiple` is `:222`, another control |
      | 3 | **"The menu never closes on linux — suspected real defect"** (this fragment's original claim) | A probe in the linux image: menu gone in <500ms and the value lands ("Stormlight Archive") |
      | 4 | The Drawer backdrop is the sole culprit → wait on `.MuiDrawer-modal` | Strict-mode violation — the sidebar renders twice, so the selector matched 2 nodes; and library-browser's blocker was the Select menu, a different overlay |

      **The lesson is about method, not MUI.** Every hypothesis came from reading a call log
      and reasoning about what *should* follow. What finally settled it was changing one
      variable and measuring: workers 2 → 1. The cheap discriminating experiment was
      available from the beginning and was reached fourth.

      **Second lesson: build the repro before iterating.** Three of the four rounds cost a
      ~6-minute CI cycle each because there was no way to run linux locally. Building that
      (Go binary compiled in a `golang` container because CGO/`libtag` blocks
      cross-compilation, then the official Playwright image) took one round and turned the
      loop into seconds. It should have come first.

      Runner script: `<scratchpad>/linux-probe.sh` — copies the tree in, `npm ci` inside
      (the host `node_modules` is a symlink to another worktree full of darwin binaries),
      starts the prebuilt binary, runs Playwright against it with `CI` unset so
      `reuseExistingServer` attaches instead of trying to `go build`.

- [ ] **Per-field "Use File" / "Use Fetched" one-click apply is gone from Book Detail
      — confirm that was intended.** `web/src/pages/BookDetail.tsx:1014-1015` now renders
      exactly two tabs (Info, Files & History). The old Tags/Compare tab listed every
      metadata field as a row with one-click **Use File** and **Use Fetched** buttons,
      each showing its own inline "Applying…" spinner while only that field's write was
      in flight. Neither string appears anywhere in `web/src` today. Fetched values are
      still *surfaced* — `MetadataEditDialog.tsx:188-198` labels a field's source as
      "Fetched" and pre-fills from `fetched_value` — but applying one now means opening
      the dialog and saving the whole form, so there is no way to accept a single fetched
      field. Two e2e tests covering the old flow were deleted on 2026-08-09 rather than
      left permanently skipped. If the loss was unintentional, this is the third
      capability this session's e2e sweep has found missing from Book Detail (the others:
      version management, and the Change Log "Compare snapshot" link — see
      `todo.d/20260809-changelog-row-compare-affordance.md`).

- [ ] **Visual-regression goldens exist only for darwin.**
      `web/tests/e2e/dynamic-ui-interactions.spec.ts-snapshots/` holds
      `scan-button-loading-chromium-darwin.png` and `-webkit-darwin.png` and nothing for
      linux, so `Button loading states visual check` cannot pass on CI runners — it will
      report a missing snapshot. The chromium-darwin golden was regenerated 2026-08-09
      after the spinner was masked; the **webkit-darwin one is now stale** and could not
      be regenerated locally because the webkit browser is not installed on this machine
      (`npx playwright install webkit`). Either commit linux goldens generated in CI, or
      scope this test to a single platform so it stops being a permanent red on the
      nightly e2e workflow.

- [x] **FIXED — both halves.** Typing in the library search box silently dropped every
      active filter and the sort order, and queried on every keystroke.

      > ### ✅ The debounce half of this is FIXED (#2264) — and the original diagnosis was wrong
      >
      > This fragment said the search box "is not debounced at all". **A 300ms debounce
      > existed the whole time.** What was actually happening is worse and more specific:
      > `useLibraryQuery.ts:165` reads
      >
      > ```ts
      > const searchText = parsedSearch ? parsedSearch.freeText : debouncedSearch;
      > ```
      >
      > so the moment a search parses — which is always, once you type — the debounced
      > value is **ignored** and the raw parsed value is used. `parsedSearch` also sits in
      > that hook's `useCallback` dep array, so `loadAudiobooks` was recreated per
      > keystroke. The debounce was real, correct, and **dead code on the only path that
      > matters.**
      >
      > Fixed by moving `parsedSearch` and `searchQuery` off the same 300ms timer, rather
      > than debouncing one and leaving the other raw — debouncing only the free text would
      > let it disagree with the field filters mid-flight. `SearchBar`'s own UI still gets
      > the raw value so chips react instantly; `useLibrarySelection` gets the debounced one,
      > because "select all matching" must mean the query that produced the visible rows.
      >
      > `test.fixme('search debounces input to avoid excessive requests')` is now a real
      > passing test (search-and-filter.spec.ts: 11 passed / 1 skipped, exit 0).
      >
      > ### ✅ The filter-dropping half is ALSO fixed (#2265)
      >
      > It was a **branch**, not a missing capability:
      >
      > ```ts
      > searchText
      >   ? api.searchBooksPage(searchText, itemsPerPage, offset, filters.showFailed, signal)
      >   : api.getBooks(itemsPerPage, offset, { sortBy, sortOrder, tags, libraryState, filters, ... })
      > ```
      >
      > Every option lived on the `getBooks` side only, so typing one character crossed to
      > a call that sends four parameters — dropping `library_state`, tags, field filters
      > and the sort order.
      >
      > **The server was never the problem.** `GetAudiobooks` applies the same post-filters
      > on the search path (`service_query.go:226`); it was simply never told about them.
      >
      > Fixed by collapsing the branch rather than adding nine parameters to
      > `searchBooksPage`: `getBooks` hits the same endpoint with the same
      > `is_primary_version`, so it only needed a `search` option. **One code path now** —
      > which also means a future filter cannot be added to one branch and forgotten in the
      > other, which is exactly the class of bug this was.
      >
      > `searchBooksPage` had exactly one production caller (checked); it is now
      > `@deprecated` with the reason rather than removed.
      >
      > `test.fixme('search works with other filters combined')` is a real passing test.
      > Verified: search-and-filter + library-browser, **33 passed / 0 failed / 0 skipped,
      > exit 0.**
      >
      > Lesson worth keeping: "feature X is missing" and "feature X exists but is bypassed"
      > look identical from the outside and have completely different fixes. Grep for the
      > mechanism before concluding it is absent. `useLibraryQuery.ts:192-193` branches on whether there is search text:

      ```ts
      searchText
        ? api.searchBooksPage(searchText, itemsPerPage, offset, filters.showFailed, signal)
        : api.getBooks(itemsPerPage, offset, { sortBy, sortOrder, tags, libraryState, filters, ... })
      ```

      `api.searchBooksPage` (`web/src/services/api.ts:1023-1037`) sends only `search`,
      `limit`, `offset`, `is_primary_version` and optionally `show_quarantined`. **No**
      `library_state`, **no** `filters` (author/series/genre/language), **no** `tags`,
      **no** `sort_by`. So filtering to Organized and then searching an author returns
      matches from every state — while the Filters button keeps showing its count, so the
      filter still looks applied. Same family as the Deleted-filter cache bug fixed in
      #2230: a filter that silently does nothing is indistinguishable from one that
      matched everything. Covered by a `test.fixme` in
      `web/tests/e2e/search-and-filter.spec.ts`.

- [x] **SUPERSEDED — duplicate of the entry above, fixed in #2264** (debounce existed and
      was bypassed on the parsed path; verified checked-off 2026-08-14). Original text:
      **The library search is not debounced at all.** Measured 2026-08-09: typing the ten
      characters of "Foundation" fires **ten** requests to `/api/v1/audiobooks?search=…`,
      exactly one per keystroke. The e2e test is literally named "search debounces input
      to avoid excessive requests" and asserts `<= 3`; it has been marked `test.fixme` so
      it fails loudly as an unexpected pass once a debounce lands. On a large library each
      of those is a full-text query, so this is directly relevant to the backend-filtering
      work — no amount of server-side improvement helps if the client sends ten queries
      for one search. Related: the richer-backend-filtering TODO item.

- [ ] **Replace library sorting with server-side Go sorting.** Owner decision 2026-08-09:
      *"I want the system to not suck and I want sorting replaced, and done by go."*
      Recorded in full with the code evidence in §0a of
      `docs/design/2026-08-09-search-backend-options.md`.

      ## Where sorting lives today — three places, one of them correct

      | where | what | verdict |
      |---|---|---|
      | `internal/audiobooks/service_filtering.go:130` `applySorting` | Go, server-side, over the full filtered set | **Correct.** Keep and extend |
      | `web/src/components/common/ConfigurableTable.tsx:201` | `[...rows].sort(...)` on the client | **Replace.** Sorts the *current page* |
      | `web/src/services/api.ts` `searchBooksPage` | sends no `sort_by` | **Fix.** Search drops the sort order entirely |

      **Why the client-side one is broken by design, not merely misplaced:** it sorts the
      rows already fetched. On a paginated library that is the 50 books you can see, not
      the library. "Sort by title descending" hands you the *wrong 50 books*, correctly
      ordered among themselves — which looks plausible, which is why it survives.

      ## Scope carefully — not every client sort is wrong

      There are **15** `.sort()` sites in `web/src`. Most are legitimate: a book's own file
      list by track number (`BookDetailFilesTab.tsx:250`), tag clouds by count
      (`TagCloud.tsx:76`), metadata candidates by score. Those sort complete, small,
      already-fetched sets. **The rule is: a sort over a paginated slice of the library is
      wrong; a sort over a complete set the client already holds is fine.**

      ## Two things that must land with it

      1. **Sort must be applied BEFORE pagination**, which is the same defect as filters
         being applied after pagination (§2.2 of the design doc). Moving the sort to Go
         without pushing it into the query would produce a correctly-sorted page of the
         wrong rows — a subtler bug than the one being fixed.
      2. **There is no sort control in the UI at all.** `SearchBarProps`
         (`web/src/components/audiobooks/SearchBar.tsx:124-131`) has no `onSortChange`, and
         `LibraryBookGrid.tsx:133` takes the handler as `_handleSortChange` —
         underscore-prefixed to mark it deliberately unused. The state, URL round-trip and
         API parameter all still work; only the affordance is missing. So "replace sorting"
         is partly **restore the control**, not only move the logic.
         `SearchBar.test.tsx:43` asserts the control is absent and now passes vacuously —
         that assertion has to be inverted, or it will defend the bug.

      **Acceptance:** choosing a sort reorders the whole library (verify by sorting
      descending and checking page 1 holds the true last items, not the reversed first
      page); the sort survives a search; `sort_by` appears on the request; no `.sort()`
      remains over a paginated library slice.

- [ ] **The checked-in `.api-token` no longer authenticates, and it blocked a real
      verification.** Found 2026-08-09 while grounding
      `docs/design/2026-08-09-search-backend-options.md`.

      `.api-token` (the shared per-worktree API key created by the `server-bootstrap`
      skill and documented in `CLAUDE.md`) returns:

      ```
      {"error":"invalid session","code":"UNAUTHORIZED","status":401}
      ```

      while `/api/v1/health` returns 200 — so the server is up and it is the credential
      that is stale, not the endpoint. The file dates from 2026-07-14.

      **Why this matters beyond convenience.** It blocked a specific question that is worth
      answering: **is the Bleve search index complete?** The engine is confirmed *open* in
      production (`msg="Search index opened"` on the current process and every restart back
      to Aug 07), but an index that opens fine while missing books produces confidently
      wrong results. The other route to that answer — reading the index directory — needs
      root, and `sudo` on the prod host requires interactive authentication.

      **Do:**
      1. Regenerate `.api-token` via the documented bootstrap path.
      2. With it, compare a broad search's result count against the same term reached
         through a filter-only path. A large gap means index drift.
      3. Consider whether a *silent* search degradation is acceptable: `Open()` failures
         are downgraded to warnings so the server boots without search
         (`internal/search/register.go`), so a fallback to the O(N) substring scan would
         run indefinitely with only a startup warning to show for it. With `/metrics`
         currently unscraped (see the Prometheus gap), nothing would surface it. That is
         the same failure shape as the six e2e specs that sat disabled for four months.

      See §6 Q1 of `docs/design/2026-08-09-search-backend-options.md`.

- [x] **RESOLVED — all three fixed; the PR gate now blocks.** Superseded by
      `todo.d/20260809-webkit-scan-import-drawer-backdrop.md`, which tracks the single
      remaining webkit failure. Kept for the causes, which were all different.

      **Outcome 2026-08-09, measured on the real runner:**

      | configuration | before | after |
      |---|---|---|
      | chromium (PR path) | 269 passed / 3 failed | **272 passed / 0 failed / 8 skipped** |
      | chromium + webkit | not measured | **543 passed / 1 failed / 16 skipped** |

      | # | blocker | resolution |
      |---|---|---|
      | 1 | missing linux visual golden | Generated for BOTH engines in the Playwright linux image (#2250, #2251). Also found the goldens were **Git LFS pointers** — `*.png filter=lfs` meant CI checked out a text pointer and Playwright reported "Could not decode expected image as PNG", so the test could never have passed on CI for either browser |
      | 2 | `library-browser` click timeout | **Worker contention**, not a defect. `workers: 1` on CI (#2249) |
      | 3 | `scan-import-organize` click timeout | Same cause; fixed on chromium by the same change. **Persists on webkit** → the successor fragment |

      `pull_request` trigger restored and the job made blocking on that path;
      `continue-on-error` is now conditional so the unproven both-engine configuration is
      not handed a green light it has not earned.

      ---

      **Original entry, for the record.** Measured
      2026-08-09 by dispatching the E2E workflow against current `main` — not inferred
      from the nightly, which was stale.

      **The numbers.** CI (ubuntu, chromium): **269 passed / 3 failed / 8 skipped of 280.**
      The same suite locally (macOS, chromium): **272 passed / 8 skipped of 280, 0 failed.**
      So exactly **3 failures exist only on linux.**

      | # | test | symptom |
      |---|---|---|
      | 1 | `dynamic-ui-interactions.spec.ts:449` — Button loading states visual check | `A snapshot doesn't exist at …/scan-button-loading-chromium-linux.png` |
      | 2 | `library-browser.spec.ts:382` — combines multiple filters | `locator.click: Test timeout of 30000ms exceeded` |
      | 3 | `scan-import-organize.spec.ts:259` — complete workflow: add import path → scan → organize | `locator.click: Test timeout of 30000ms exceeded` |

      **#2 and #3 are new information and the important part.** They pass on macOS and hang
      on linux. That is the whole reason this measurement was worth taking: a suite that is
      green locally is not evidence that CI is green, and this project has already been
      burned once by exactly that inference. Do NOT assume they are "just CI slowness"
      without looking — a 30s click timeout is a long time for a mocked page, and both are
      `locator.click`, which is suspicious enough to be a shared cause.

      **#1 is mechanical.** There are only two goldens in the repo, both `-darwin`
      (`scan-button-loading-chromium-darwin.png`, `…-webkit-darwin.png`), and Playwright
      fails rather than writes when `CI=true`. Generating a linux golden needs a container,
      because `playwright.config.ts`'s `webServer` builds the Go binary and that needs CGO +
      `libtag1-dev` — so it is a two-stage build (compile in a Go image, run in the official
      Playwright image), not a one-liner. Alternatively let CI produce it once and upload it
      as an artifact to be committed.

      **Two workflow defects found while measuring, worth fixing in the same PR as the flip:**

      1. **`conclusion: success` on this workflow means nothing.** `continue-on-error: true`
         makes the job succeed no matter how many tests fail. Every nightly to date reports
         green. Anyone glancing at the Actions tab would reasonably conclude the suite is
         passing — this morning's nightly reported `success` with **179 failures**. That is
         a green light attached to a red suite, which is the same shape as the incident this
         work exists to prevent.
      2. **The job name misreports what ran.** It renders
         `E2E (chromium + webkit)` for any non-`pull_request` trigger, including a
         `workflow_dispatch` with `projects=chromium`. The `projects` input *is* honoured by
         the test step — only the label is wrong. A label that does not match what executed
         is precisely how the 2026-08-08 false green was believed.

      **Order of work:** fix #2 and #3 first (they are real and may share a cause), then
      #1, then flip `continue-on-error: false` **and** restore the `pull_request` trigger in
      the same change — the workflow comment is explicit that they go together, because a
      non-blocking check people learn to ignore is worse than no check.

      **Acceptance:** a dispatched run against `main` reports 280 passed / 0 failed for
      chromium, and a PR touching `web/**` or `**.go` gets a blocking E2E check.

- [ ] **You can no longer navigate between versions of a book.** Book Detail used to
      have a "Versions" tab listing the group's other versions, each clickable to jump
      to it. `web/src/pages/BookDetail.tsx:1014-1015` now renders only Info and
      Files & History, and `BookDetailVersionGroup.tsx` contains no `RouterLink` — the
      version titles are plain text. `VersionManagement.tsx` (the dialog) has no
      `navigate()` call either. The only per-version action left is
      **"Move to: \<title\>"** (`BookDetailVersionGroup.tsx:457-464`), which moves
      *files* between versions — a destructive operation, not navigation, sitting where
      users previously clicked to browse. Getting from the M4B to the MP3 of the same
      book now means going back to the library and finding the other card.

- [ ] **The version-group summary lost its count and its "you are here" marker.**
      `Part of version group with N books.` and `(Current)` appear nowhere in `web/src`.
      All that survives is a bare **"Version Group Linked"** chip
      (`BookDetailHeader.tsx:172`) — it tells you a group exists but not how big it is
      or which member you are looking at.

- [ ] **The library card's overflow menu button has no accessible name.**
      `web/src/components/audiobooks/AudiobookCard.tsx:183` is an `IconButton` with only
      a `<MoreVertIcon/>` inside — no `aria-label`, no tooltip. Screen readers announce
      it as an unlabelled button, and it is now the **only** route to Manage Versions,
      Edit, Fetch Metadata and Parse with AI. The e2e suite has to locate it via
      `button:has([data-testid="MoreVertIcon"])` because there is nothing else to match on.

Context: `version-management.spec.ts` was repointed at the surviving entry point on
2026-08-09 (4 of 6 tests). The two covering navigation and the group summary were
deleted rather than rewritten, since the capabilities themselves are gone. Related:
`todo.d/20260809-changelog-row-compare-affordance.md`,
`todo.d/20260809-per-field-use-fetched-affordance.md`.

- [x] **RESOLVED — webkit was marginal on TIMING, and its own 60s budget fixed the
      class.** The nightly now blocks too; `continue-on-error` is a plain `false`.

      **Final measurement on the real runner:**

      | configuration | result |
      |---|---|
      | chromium (PR path) | **272 passed / 0 failed / 8 skipped** |
      | chromium + webkit | **544 passed / 0 failed / 16 skipped** |

      The fix was one line — `timeout: 60 * 1000` on the webkit project only, chromium
      keeping 30s — and it was chosen as the *discriminating experiment* for the
      population hypothesis rather than as a workaround. It came back green, so the
      hypothesis is confirmed: webkit had several tests close to the shared 30s limit and
      roughly one lost per run.

      **Why this is headroom and not blindness:** a genuinely broken test does not finish
      in 60s either. What changed is that a slow-but-correct one stopped being reported as
      a failure. Chromium keeps the tighter budget because it had margin to spare once CI
      dropped to one worker.

      **Cheaper than the plan this fragment originally proposed.** It suggested three
      measurement runs to characterise the failing set first; one config change answered
      the same question and fixed it. Worth remembering: when a hypothesis implies a
      one-line change, the change often IS the measurement.

      ---

      **Original entry, for the record.**

      ## The update that changes the shape of this problem

      The drawer fix landed and **worked** — `scan-import-organize.spec.ts:259` passed on
      CI. But the same both-engine run came back with an identical score and a different
      casualty:

      | run | result | which test failed |
      |---|---|---|
      | before the fix | 543 / 1 / 16 | `[webkit] scan-import-organize.spec.ts:259` |
      | after the fix (#2253) | 543 / 1 / 16 | `[webkit] itunes-bidirectional-sync.spec.ts:121` |

      Different spec file, so the fix cannot have caused it. **The conclusion: webkit under
      CI has several tests sitting close to their timeouts, and roughly one loses per run.**
      Fixing them individually is a treadmill — each fix is real, and the score does not
      move.

      **So do not chase individual webkit failures.** Find out why webkit is marginal as a
      class:

      1. **Measure the margin.** Dispatch the webkit suite on CI 3+ times and collect the
         failing set. If it is large and varies run to run, this is systemic timing, not N
         separate bugs.
      2. **Consider a per-project timeout.** The config uses one 30s `timeout` for both
         engines, but webkit is measurably slower here — chromium stopped failing at
         `workers: 1` and webkit did not. A per-project override is a one-line change that
         would settle whether headroom is all that is missing.
      3. Only then decide whether individual tests need waits.

      ## Original entry — the drawer case, now FIXED (#2253)

      **Measured on the real runner:**

      | configuration | result |
      |---|---|
      | chromium (the PR path) | **272 passed / 0 failed / 8 skipped** |
      | chromium + webkit | **543 passed / 1 failed / 16 skipped** |

      The one failure: `[webkit] scan-import-organize.spec.ts:259` — *complete workflow: add
      import path → scan → organize*. After `page.keyboard.press('Escape')` closes the
      filter drawer, the `Select All` click times out at 30s because MUI's full-page modal
      backdrop is still intercepting pointer events:

      ```
      <div class="MuiBackdrop-root MuiModal-backdrop"> from
      <div aria-hidden="true" class="MuiDrawer-root MuiDrawer-modal MuiModal-root">
      subtree intercepts pointer events
      ```

      `workers: 1` (#2249) fixed the identical failure on chromium. **Webkit is slower and
      it persists there.**

      ## 🚨 Read this before you start: the local container is NOT a valid oracle

      The linux repro container (`<scratchpad>/linux-probe.sh`, `--cpus=2`) is **harsher
      than the GitHub runner** and invents failures CI does not have. Across four runs of
      the same spec it produced four different signatures:

      | attempt | what failed |
      |---|---|
      | baseline | `:259` drawer backdrop (matches CI) |
      | after `toHaveCount(0)` fix | `:259` **plus** `:386` "Cancel Scan" — a test CI passes |
      | after re-run | `:259` plus `:390` |
      | after visible-filter fix | `:259` failing **earlier**, on the Filters button itself |

      Tuning against it is a treadmill — it was exited deliberately, not because the problem
      was solved. **Iterate against a dispatched CI run, or a container with more CPU.**

      ## Three assertion shapes already ruled out, by measurement

      Do not re-try these:

      | shape | why it fails |
      |---|---|
      | `expect(locator).toBeHidden()` | **Strict-mode violation.** Sidebar renders its content twice (temporary Drawer + permanent one), so the selector matches 2 nodes |
      | `expect(locator).toHaveCount(0)` | **Never converges.** Count sits at 2 forever — MUI keeps the backdrop MOUNTED and merely hides it |
      | `.filter({ visible: true })` + `toHaveCount(0)` | Failure moved earlier (to the Filters button) in the container; **unvalidated against CI**, so this one is "unproven", not "disproven" |

      ## Suggested next steps

      1. Re-test the `.filter({ visible: true })` variant **on CI**, not in the container.
         It is the only shape that is semantically right for a hidden-not-unmounted
         backdrop, and it was abandoned because of an unreliable oracle rather than
         evidence against it.
      2. If that is not enough, consider whether the test should dismiss the drawer by
         clicking its close control rather than pressing Escape — a more deterministic
         path than relying on a transition finishing.
      3. **Do not add a blind retry.** The app does close the drawer; the wait is legitimate
         and should assert the closed state, not paper over it.

      **When this is green, `continue-on-error` in `.github/workflows/e2e.yml` becomes a
      plain `false`** and the nightly blocks too. The conditional expression there exists
      only because of this one test.

- [ ] **`scan-import-organize.spec.ts` (7 failures) — Settings tab deep-link
      fixed, but that was NOT the blocker. Count unchanged at 7.**
      Investigated 2026-08-09.

      **Applied and kept (correct, but insufficient):** the tests navigated to
      `/settings` and immediately clicked "Add Import Path". Settings is tabbed
      now and defaults to **Library**; the button is rendered by
      `components/settings/PathsSettingsTab.tsx:229`, mounted from
      `pages/Settings.tsx:832`, i.e. only when the **Paths** tab is active.
      `tabFromHash()` (`Settings.tsx:96`) maps a URL hash to a tab index via
      `TAB_KEYS`, so `'/settings#paths'` is the app's own supported deep link.
      All four navigations now use it.

      **It did not help — still 7 failures**, all still timing out on
      `getByRole('button', { name: 'Add Import Path' })`. So the Paths tab is
      not rendering, or the Settings page is not reaching a usable state at
      all. The change is kept because it is verifiably more correct than
      `/settings`, not because it fixed anything.

      **Next step, and do this before writing any code:** capture the DOM for
      one of these failures specifically. `test-results/` was dominated by
      other tests' directories, so the Settings page snapshot was never
      actually read — which, given that reading the snapshot has found every
      real cause in this effort, is the obvious gap. Run just this spec and
      open `test-results/<dir>/error-context.md` for the workflow test.

      Candidates worth checking once the snapshot is in hand: whether Settings
      renders an error boundary from an unmocked endpoint, whether it redirects
      (auth), and whether the tab panel is lazily mounted such that the
      hash-selected index is applied after the click is attempted.

- [ ] **E2E repair progress — measured 2026-08-09. Supersedes the stale
      "146 failures across 22 files" triage.**

      **Suite: 66 failed / 218 passed / 4 skipped of 288 chromium tests.**
      Down from 146 failed / 138 passed. **80 fixed, 55%**, across 8 merged
      PRs (#2211-#2221). No spec has been deleted or skipped.

      **Fully green now:** `dedup` (was 26), `dedup-operations` (was 8).

      **Current distribution:**

        12  library-browser          3  unified-dedup-tab
        11  transcode-and-counting   3  scan-import-organize
         8  batch-operations         2  search-and-filter
         6  version-management       2  error-handling
         6  dynamic-ui-interactions  1  settings-configuration
         4  metadata-provenance      1  library-enhancements
         4  files-history            1  import-paths
                                     1  diagnostics
                                     1  auth-flow

      **Untouched, and therefore the best value per hour:**
      `version-management` (6), `dynamic-ui-interactions` (6),
      `files-history` (4), `unified-dedup-tab` (3), plus the tail of 1s and 2s.
      Files already worked have had their cheap causes taken; what remains in
      them is harder.

      **THE METHOD, which is the most transferable thing here.** Run ONLY the
      spec you are fixing, so `test-results/` is not buried under other tests'
      directories, then read `test-results/<dir>/error-context.md` BEFORE
      forming a hypothesis. Every genuine cause in this effort was found that
      way:

      - `/dedup` redesigned, tabbed UI behind a "Legacy View" toggle
      - `metadata-provenance` and `scan-import-organize` rendering the LOGIN
        screen because their window.fetch shims never mocked /auth/status
      - book sub-resources returning the BOOK object, crashing on `.length`
      - a fixture saying `library_state: 'import'` where the app checks
        `'imported'`, so a button disabled itself correctly

      Every wasted cycle came from reasoning about what *should* be on the page.
      Two whole cycles were spent that way on `transcode-and-counting` and the
      first pass at `scan-import-organize`.

      **Second lesson: scope fixes narrowly.** Three separate times a correct
      fix applied too broadly made things worse — a blanket `getByLabel` →
      `getByRole` sweep took one file from 5 failures to 7; a blanket
      "Fetch Metadata" rename broke the dialog button, which was never renamed.
      Verify each site rather than pattern-replacing.

      **Known causes are recorded per file** in the other todo.d fragments,
      including two hypotheses already tested and REJECTED for
      `transcode-and-counting` — read those before retrying it.

      **Two open questions that are arguably product issues, not test rot:**
      whether the library is still sortable without switching views (the
      "Sort by" control does not exist anywhere), and whether the "N selected"
      chip rendering twice is intentional.

- [ ] **`version-management.spec.ts` (6 failures) — version management MOVED off
      the book detail page. The spec needs a rewrite, not a selector tweak.**
      Fully diagnosed 2026-08-09; no code changed, because the fix is a real
      rewrite and a half-finished one is worse than none.

      **What the tests do:** `openBookDetail()` navigates to `/library/<id>`
      and each test then clicks `getByRole('button', { name: 'Manage
      Versions' })` to open the linking UI.

      **What the app does now:**

      - `pages/BookDetail.tsx` does **not** import `VersionManagement` at all.
        It renders `components/bookdetail/BookDetailVersionGroup.tsx`, which is
        **read-only** — Bitrate, Duration, File, Origin, Path, Sample Rate,
        Size. There is no link/unlink affordance on book detail.
      - The interactive `VersionManagement` component is rendered from
        `components/library/LibraryDialogs.tsx` and `pages/Library.tsx` — i.e.
        from the **Library** page.
      - "Manage Versions" is a **MenuItem inside the card's overflow menu**
        (`components/audiobooks/AudiobookCard.tsx:336`), so its role is
        `menuitem`, not `button`, and the menu must be opened first.

      So the tests are driving a capability that page no longer has. The book
      detail header still shows a "Version Group Linked" chip, which is why the
      page *looks* right in the snapshot — it displays version state but cannot
      change it.

      **The rewrite:** point `openBookDetail()` (5 call sites) at `/library`,
      open the target card's overflow menu, then click the **menuitem**
      "Manage Versions". The dialog interactions after that point are likely
      still valid, since `VersionManagement.tsx` itself was not replaced — only
      relocated.

      **Worth asking before doing it:** is losing version management from book
      detail intentional? Managing versions of the book you are looking at is a
      natural place for it, and it now requires going back to the library and
      finding the card. That is a product question, not a test question.

- [x] **`Library.tsx:707` — an `exhaustive-deps` warning whose suggested fix
      would silently undo the URL filter-drop guard.** Introduced 2026-08-10 by
      PR #2271; noticed while linting an unrelated branch. **DONE — PR #2273.**

      `npx eslint .` in `web/` reports:

          707:6  warning  React Hook useEffect has a missing dependency:
                 'searchParams'. Either include it or remove the dependency
                 array   react-hooks/exhaustive-deps

      The omission is deliberate. That effect is the URL **writer**, and #2271
      added a guard at the top of it that reads `searchParams` precisely to
      detect "the URL changed under us since the last commit":

          const currentSearch = searchParams.toString();
          const urlChangedUnderUs = currentSearch !== seenSearch.current;
          if (urlChangedUnderUs && currentSearch !== lastWrittenSearch.current) return;

      Reading a value without depending on it is the whole point — the guard
      needs the *current* URL compared against a ref that a **later** effect
      advances, so effect declaration order is load-bearing. See the comment on
      `seenSearch` and the one inside the write effect.

      **RESOLVED 2026-08-10 (PR #2273).** Suppressed with an explicit
      `// eslint-disable-line react-hooks/exhaustive-deps` on the deps line,
      carrying the reason. The dependency array is byte-identical to before —
      the diff is comments only.

      **Two claims in the original write-up turned out to be wrong; corrected
      here so nobody acts on the stale version:**

      1. It said whether adding the dependency actually breaks the guard was
         "not established". It is now. **With `searchParams` added to the
         array, `library-sidebar-filters.spec.ts` ran 36/36 green on webkit**
         (9 tests × 4 repeats). It does **not** break that spec. It was still
         not adopted, because that effect owns URL writes for the whole Library
         page and one spec file is not evidence about the rest of it — with the
         dep added the writer also re-runs on its own echo and rewrites
         identical params. Anyone wanting that form must verify it page-wide.

      2. It said to use `// eslint-disable-next-line`. **That does not work
         here.** A `-next-line` directive placed above a multi-line explanation
         applies to the *comment*, not to the deps array — the original warning
         survives and lint reports an additional "Unused eslint-disable
         directive". Only `reportUnusedDisableDirectives` made that visible.
         Use `eslint-disable-line` on the deps line itself, which is also what
         the sibling read effect at `Library.tsx:632` already does.

      **Control, re-measured under the pinned Playwright 1.62.1** (the earlier
      "4 of 6 / 24 consecutive" figures were taken in a worktree that had
      silently resolved a stray 1.57.0 from `$HOME`, so they were discarded):

          guard intact,   guard test ×8         8 passed,  exit 0
          guard disabled, guard test ×8         4 failed / 4 passed, exit 1
          guard intact,   whole spec file ×4   36 passed,  exit 0

      Only one test in `library-sidebar-filters.spec.ts` exercises this guard —
      `the filter never disappears from the URL while the effects settle`
      (`:234`, webkit). The two deep-link tests in the same file pass **6 of 6
      with the guard disabled**; they are invariant coverage, not regression
      guards, and are labelled as such in the file. Running those and seeing
      green proves nothing about this dependency array.

      eslint after the change: **24 warnings, 0 errors** (was 25/0 — exactly
      this warning removed, none added). `tsc --noEmit` exit 0.

- [x] **🔴 The search index silently drops updates when its queue fills — 56,537 dropped
      in seven days.** Measured on prod 2026-08-10 from `journalctl`. This was a
      **blocking prerequisite** for pushing filters/sort into Bleve (design doc option
      A1), and it changed the ordering of that plan.

      **✅ FIXED — reconciliation shipped.** Owner chose a dirty-set drained on a ticker,
      persisted to Pebble, with an adaptive batch size. Steps 1, 2 and 4 below are done;
      step 3 (filter/sort pushdown) is now unblocked.

      Implementation: `internal/database/pebble_store_search_dirty.go` (durable set,
      `idx:sidx:dirty:{id}`, mirroring the existing `idx:upl:dirty:` playlist idiom) and
      `internal/server/search_reconciler.go` (ticker + adaptive drain).

      ## 🔑 The root cause was a false comment, not just a missing feature

      Three separate comments — `indexed_store.go:14`, `indexed_store.go:100` and
      `server.go:225` — asserted that "a startup reindex will heal any gaps". **It does
      not.** `buildSearchIndexIfEmpty` opens with `if count > 0 { return }`, so it runs
      only when the index has ZERO documents. On a populated library it has never run.

      The drop was therefore designed as safe *under a guarantee that was never true*.
      That is why all three comments were corrected in place, with the old claim quoted
      and refuted, rather than quietly rewritten: the next person to read the old
      reasoning must not re-derive the same wrong conclusion.

      ## Two things the implementation measured rather than assumed

      1. **`pebble.Sync` on the mark was a latency bug.** The first version synced every
         mark; a test writing 2,500 IDs took **13.9s** (~180/sec). Drops arrive in bursts
         on the write path while `enqueueIndex` holds `indexQueueMu.RLock`, so that would
         have added ~5ms to every write during exactly the overload the drop relieves.
         Switched to `NoSync` (still WAL-backed, survives process crash): the same test
         now takes **0.13s** — 107× faster.
      2. **A 1%-per-tick adaptive drain was too slow to matter.** At 1%, a 56,537 backlog
         drains ~565/tick — indistinguishable from the fixed 500 floor, ~50 minutes total,
         and it decays so the tail is slowest. Implemented at 10% clamped to
         [500, 5000]: the same backlog clears in ~11 ticks (~5.5 min).

      ## The measurement

      ```
      level=WARN msg="search index queue full, dropped (delete)" bookID=01KXXVGZ90PS78ZWJZJY62EFCJ del=false
      ```

      | window | dropped index operations |
      |---|---|
      | last 7 days | **56,537** |
      | days affected | Aug 03 and Aug 07 only |
      | since the Aug 09 10:33 restart | 0 |

      **The zero is not reassuring.** The queue is empty because the process restarted and
      no bulk operation has run since. Both affected days were bulk-operation days; the
      next scan, merge wave or dedup run refills it and drops again.

      ## The mechanism

      `internal/server/indexed_store.go:113-122` — a non-blocking send onto a 1024-deep
      channel, with `default:` as the overflow branch:

      ```go
      select {
      case s.indexQueue <- indexRequest{bookID: bookID, delete: del}:
      default:
          atomic.AddInt32(&s.indexWorkerBusy, -1)
          slog.Warn("search index queue full, dropped (delete)", "bookID", bookID, "del", del)
      }
      ```

      Dropping under pressure is a defensible choice — the alternative is blocking a write
      path on the indexer. **What is not defensible is that nothing reconciles afterwards.**
      A dropped update is lost permanently; there is no retry, no dirty-set, and no
      periodic re-sync. The index diverges from the database and stays diverged until
      something happens to rewrite that book.

      Note the log message says `(delete)` while `del=false` — the label is wrong for the
      upsert case, which makes the warning harder to interpret than it needs to be.

      ## Why this blocks A1

      Today a dropped update means **stale relevance** — a book ranks oddly or misses a
      match. Bad, tolerable, invisible.

      After A1 pushes filters and sort into the Bleve query, a dropped update means
      **wrong rows**. A book whose `library_state` changed to `organized` but whose index
      entry still says `imported` will be *absent from the Organized filter and present in
      Imported*. The user sees a library that is missing books, with no error.

      **This is the difference between an index that is a relevance dependency and one that
      is a correctness dependency** — exactly the risk flagged as open item 3 in
      `docs/design/2026-08-09-search-backend-options.md`, now with a measured failure rate
      attached.

      ## What to do, in order

      1. **Make the drop visible.** A counter and a metric, not just a WARN that scrolls
         past 56,537 times. Right now the only way to know is to grep journald.
      2. **Reconcile.** Any of: a dirty-set of book IDs that failed to enqueue, drained on
         a ticker; a periodic full re-index; or a generation counter per book compared
         against the index on read. A dirty-set is the cheapest and matches the existing
         "cached aggregates + dirty flag" idiom in this codebase.
      3. **Then and only then**, push filters/sort into the index.
      4. Fix the `(delete)` label while touching this.

      **Do not size the queue bigger and call it fixed.** 1024 → 100,000 moves the
      threshold; it does not add reconciliation. The bulk days dropped 56K operations,
      which no reasonable buffer absorbs.

      ## Also settles an open question

      Open item 4 of the design doc asked whether the index is complete. **It is not**, and
      now there is a mechanism and a number rather than a suspicion. The `.api-token` is
      still stale, but this answer did not need it.

- [ ] **⚖️ DECIDE which sort indexes to enable — the design-doc cost estimate was ~10×
      optimistic.** The machinery is built, tested and merged behind
      `enabled_sort_indexes`, defaulting to empty (today's behaviour exactly). What is
      left is choosing what to turn on, and that needs the real number rather than the
      one the decision was originally made on.

      ## What was decided, and on what basis

      On 2026-08-09 the owner selected nine sort fields to index — author, narrator,
      series, created_at, updated_at, year, duration, file_size, bitrate — from a design
      doc that estimated **"tens of MB per sort field"** against ~1.25 GB resident, i.e.
      "low single-digit percent each".

      ## What it actually costs

      Measured, 100,000 books, identical fixture on both sides
      (`TestSortIndexCost`, `internal/database/memdb_sort_index_cost_test.go`):

      | | without | all nine | delta |
      |---|---|---|---|
      | heap per book | 2,645 B | 6,395 B | **+142%** |
      | at 366,916 books | 925.6 MB | 2,237.8 MB | **+1,312 MB** |
      | insert 100K | 335 ms | 935 ms | **2.8× slower** |

      That is **~146 MB per sort key**, not "tens of MB". memdb is already ~1.25 GB
      resident with a 107.9 s warmup, so all nine roughly doubles it.

      **And this is a LOWER bound.** The fixture leaves `Author` and `Series` unset, so
      two of the six physical indexes store the 1-byte "missing" key for nearly every
      row. A library with populated author/series data pays more than this.

      ## Why the estimate was wrong

      The doc reasoned that "a secondary index stores keys and IDs, not books", which is
      true and led to sizing by key length. But go-memdb is an **immutable** radix tree:
      every insert path-copies the nodes from root to leaf. Cost is dominated by node
      allocation, so a short key is not a cheap key. Roughly 417 B per book per index
      regardless of what the key contains.

      ## The decision

      Not "should we index" — the pagination-disabled full-set sort is genuinely bad.
      It is **which fields earn ~146 MB each**, and there is no usage data to answer it:
      nobody has measured which sorts real users pick. Options, cheapest first:

      1. **Enable none for now** (current default). Costs nothing, changes nothing.
      2. **Instrument first** — log `sort_by` values for a week, then enable only the
         fields that actually appear. This is the option that replaces a guess with
         evidence, and the instrumentation is small.
      3. **Enable a chosen subset.** `created_at`/`updated_at` are the most likely to be
         worth it ("what's new" is a real browsing pattern); the numeric triage fields
         (duration/file_size/bitrate) are plausibly rare enough to leave on the slow path.
      4. **Enable all nine** and accept ~2.5 GB resident. Only with headroom confirmed on
         the host, and re-measure warmup — 107.9 s is already not short.

      After enabling anything: re-measure warmup and RSS on prod, because the
      extrapolation from 100K is linear-by-assumption and 366,916 is 3.7× further out.

      ## Also worth knowing

      `CanPushDownSort` consults the **enabled** set, not the known set, so a field that
      is not indexed correctly falls back to the existing path instead of asking memdb
      for an index that was never registered. `SetEnabledSortIndexes` must be called
      before the store opens — it is, from the single `cobra.OnInitialize` hook in
      `cmd/root.go`.

      Related: `docs/design/2026-08-09-search-backend-options.md` §2.3 (which still
      carries the old estimate in its prose and should be corrected to point here).

- [x] **DONE 2026-08-09 — the e2e suite runs in CI and BLOCKS on every trigger.**
      `continue-on-error: false`, `pull_request` trigger live with its paths filter
      (#2258). A change that breaks the browser suite can no longer merge.

      **Final state, measured on the real ubuntu runner — not locally:**

      | configuration | result |
      |---|---|
      | chromium (PR path) | **272 passed / 0 failed / 8 skipped** |
      | chromium + webkit | **544 passed / 0 failed / 16 skipped** |

      Baseline was 146 failed / 138 passed of 288 chromium tests. 26 PRs,
      #2224–#2258.

      **The three sub-items in the original entry are all closed:**

      1. ~~Establish the real number with a full-output run~~ — done; it was 146, not
         the ~4 the fragment guessed.
      2. ~~Triage the failures~~ — done, plus **eleven product regressions** found that
         were not test problems (audit:
         `docs/audits/2026-08-09-e2e-repair-and-ui-regressions.md`).
      3. ~~Flip `continue-on-error` off~~ — done, together with restoring the
         `pull_request` trigger, exactly as the entry required.

      **⚠️ The CI-only failures were ALL environmental, not product defects** — and two
      were filed as product bugs before measurement said otherwise:
      worker contention starving MUI transitions (`workers: 1` on CI), a shared 30s
      timeout too tight for webkit (its own 60s budget), and visual goldens stored as
      **Git LFS pointers** no runner could decode. That last one meant the visual test
      could never have passed on CI, for either browser.

      **🚨 "Green locally" ≠ green on CI.** The suite was 0-failing on macOS while CI
      had 179 failures. `conclusion: success` on that workflow meant nothing while
      `continue-on-error` was true. Dispatch
      `gh workflow run e2e.yml --ref <branch> -f projects=chromium` and read the counts.

      ---

      **Original entry, for the record.** Found
      2026-08-08 while adding sidebar-filter coverage. `grep -rl
      "test-e2e\|test:e2e\|playwright test" .github/workflows/` returns
      **nothing**. The suite exists, is maintained (43 specs were repaired
      across #2185/#2187/#2191 this week), and gates nothing. A regression in
      any of it lands on `main` unnoticed until someone runs `make test-e2e` by
      hand.

      That is exactly how the six spec files broken by the `_page` fixture error
      stayed dead **from April to August 2026** — roughly four months of silent
      rot, only noticed because #2178 happened to unmask it.

      **Two traps to fix at the same time, or CI will lie to you:**

      1. **`reuseExistingServer: !process.env.CI`** in
         `web/tests/e2e/playwright.config.ts`. Locally this attaches to whatever
         already listens on 127.0.0.1:8484 instead of building. On 2026-08-08 a
         server left running since **00:31** was silently reused for hours,
         producing a fully green 130-test suite that had exercised a frontend
         bundle predating the fixes under test — and it was reported as
         verification before the mistake was caught. The flag is already
         disabled under `CI`, so CI itself is safe; the hazard is local runs and
         anyone trusting them. Consider making the config refuse a server older
         than the working tree, or dropping the flag entirely.
      2. **Browser binaries drift per worktree.** A fresh `npm ci` in a new
         worktree installed a Playwright wanting `webkit-2336`, which was not in
         `~/Library/Caches/ms-playwright`, so every webkit test errored with
         "Executable doesn't exist" — which reads like a test failure but is an
         environment failure. CI needs an explicit `npx playwright install
         --with-deps` step, and the distinction should be obvious in the logs.

      **Cost consideration.** A full run is ~20 minutes (chromium + webkit) and
      rebuilds frontend + Go binary, so it does not belong in the fast PR gate
      alongside Minimal CI. Options worth weighing: chromium-only on PRs with
      both engines nightly; or a required-but-slower job that runs in parallel
      with the rest. Decide deliberately rather than defaulting to "everything
      on every PR" and then disabling it when it gets annoying.

      **Acceptance:** a PR that breaks any e2e spec fails a check, and the
      failure names the spec rather than surfacing as a browser-launch error.

- [x] **The e2e suite is roughly HALF RED in a clean environment — 146 failed /
      138 passed. Triage it, then make the CI gate blocking.** Discovered
      2026-08-08 by the first-ever clean-environment run, immediately after
      wiring the suite into CI (#2202).

      ✅ **DONE 2026-08-09 across 17 PRs (#2224–#2244).** Final measurement on
      merged `main`: **552 passed / 0 failed / 16 skipped of 568**, exit 0,
      **both browsers green**. (Intermediate state after the first 14 PRs was
      chromium 272/7/0 and webkit 268/7/4; the webkit tail was closed by #2242
      and #2244.) The 16 skips are 7 `test.fixme` markers × 2 browsers, each
      attached to a real product defect so they report as *unexpected passes*
      once fixed, plus a real-server bootstrap smoke test × 2 that skips itself
      unless the server is un-bootstrapped. **Nothing was deleted or silently
      skipped.** Full audit with file:line evidence:
      `docs/audits/2026-08-09-e2e-repair-and-ui-regressions.md`.
      ✅ Sub-item 3 (flip `continue-on-error` off) is now DONE too — #2258.

      **The webkit tail was not what it looked like.** 3 of the 4 were a
      Playwright/webkit harness artifact, not a product defect: its synthesised
      pointer click on MUI's `PaginationItem` failed 4/4 while an in-page DOM
      click on the identical buttons passed 6/6. Fixed in #2242 (24/24 on
      webkit, from an 11/24 failure baseline) with retries logged so the
      masking stays visible. This had been filed as a product bug **twice**;
      both claims are corrected on the record in
      `todo.d/20260809-library-double-fetch-swallows-clicks.md`.

      **The 4th is deliberately still open and NOT fixed** (1 occurrence in
      1,136 executions, did not reproduce): `book-detail.spec.ts` purge flow.
      It passes 6/6 in isolation, so there is no measurement establishing the
      app is correct, and tolerating it in the test would be papering over an
      unknown. See `todo.d/20260809-book-detail-purge-suite-only-flake.md`.

      **This contradicts what was believed on 2026-08-08 morning.** The
      executive summary
      `docs/executive-summaries/2026-08-08-the-safety-net-that-had-stopped-catching-executive-summary.md`
      states the suite "can be trusted as a gate again" and that "it is now safe
      to require these". That conclusion rested on a local run reporting **130
      passed / 0 failed**, and that run was wrong in two independent ways:

      1. It silently reused a server that had been running for **hours**
         (`reuseExistingServer: !process.env.CI`), so it exercised a stale
         frontend bundle. This part was already retracted in #2198.
      2. **It also reported only 137 of 576 tests as having run** — 130 passed
         + 7 skipped, against 576 collected (288 chromium + 288 webkit).

      **Correction, same day:** the second point was first filed here as a
      "collection gap", i.e. a suspicion that the config collected fewer tests
      locally than in CI. **That is wrong and should not be chased.**
      `npx playwright test --list` locally reports **288 chromium / 576 both**,
      exactly matching CI. Collection is fine.

      What actually happened is worse and simpler: that run was invoked as
      `npm run test:e2e ... | tail -60`, which caused two separate failures of
      observation at once.

      - **The exit code was `tail`'s, not Playwright's.** A shell pipeline
        returns the status of its LAST command, so the "exit code 0" that made
        the run look successful only proved `tail` worked. Use
        `${PIPESTATUS[0]}`, or capture full output to a file and grep the file
        afterwards — never pipe a test command into a truncating filter and
        read the result as a verdict.
      - **The summary header scrolled out of the 60-line window.** What survives
        is the tail of a long list of webkit tests followed by "7 skipped / 130
        passed". The list is almost certainly Playwright's "did not run"
        section, but the header naming it was truncated away, so **why 439
        tests did not run is undetermined from that log** and will need a fresh
        full-output run to establish. Do not guess it from the fragment above.

      **The CI job currently runs NIGHTLY ONLY, with `continue-on-error:
      true`.** It was first wired to run on every PR; that had to be undone
      within the hour. `continue-on-error` stops a job failing the *workflow*
      but the individual check still reports red, so every PR would have
      carried a permanently-failing E2E check. That is worse than no check —
      people habituate to a red they cannot act on, which is the same failure
      that let six specs rot for four months, only louder.

      Nightly gives a daily signal without poisoning every PR. Both the
      `pull_request` trigger (commented out, paths filter preserved) and
      `continue-on-error` should be restored/flipped **together**, once the
      suite is green.

      **Work, in order:**

      1. ~~Re-run the full suite locally with output captured properly.~~
         ✅ **DONE 2026-08-08 18:47.** Fresh build, port 8484 confirmed clear,
         full output to a file, exit code read from Playwright itself
         (`PLAYWRIGHT_EXIT=1`). Result: **146 failed / 138 passed / 4 skipped**
         — *identical to CI*. Local and CI now agree test-for-test, so local
         triage is trustworthy again. The earlier "130 passed" was entirely an
         artifact of the truncating pipe plus the stale server; there was never
         a collection problem.
      2. **Triage the 146 failures.** ⚠️ **The "expect a small number of root
         causes" guess above was wrong and is corrected here.** It is not one
         cascading bug. It is **the same failure CLASS spread across 22 spec
         files that nobody has refreshed yet**:

           26 dedup                 7 scan-import-organize   3 diagnostics
           14 library-browser       7 backup-restore         2 itunes-import
           12 metadata-provenance   6 version-management     2 error-handling
           11 transcode-and-counting 6 dynamic-ui-interactions 2 auth-flow
           11 batch-operations      5 settings-configuration  1 settings-ai-persistence
           10 search-and-filter     4 itunes-bidirectional-sync 1 library-enhancements
            8 dedup-operations      4 files-history           1 import-paths
                                    3 unified-dedup-tab

         Error signatures across all 146: `toBeVisible` 67, `element(s) not
         found` 64, `locator.click` 50, `strict mode violation` 9. That is
         overwhelmingly "the test looks for an affordance the app no longer
         renders" — the exact drift already fixed in waves 1 and 2.

         **The strong evidence that this is tractable:** 13 spec files are
         **fully green**, and they include *every* file repaired in waves 1
         and 2 — `dashboard`, `book-detail`, `file-browser`,
         `import-audiobook-file`, `operation-monitoring` — plus the two new
         specs added 2026-08-08. The repair pattern works and holds; it has
         simply never been applied to the other 22 files. This is 22 files of
         known, mechanical work, not 146 mysteries.

         Suggested order: biggest first (`dedup` 26, `library-browser` 14,
         `metadata-provenance` 12), since shared helpers in those will likely
         drop several files at once — that is how one `{ data: ... }` envelope
         fix cleared 24 of 34 in wave 2.
      3. **Flip `continue-on-error` off** once green, and say so in the PR.
         ⏳ **STILL OPEN.** The workflow was moved to nightly-only rather than
         made blocking, because a permanently-red check on every PR is worse
         than no check. Now that chromium is green this can be reconsidered —
         but the 4 webkit failures and the missing linux visual goldens have to
         be dealt with first or the gate goes red on day one.
      4. **Correct the executive summary** rather than leaving a claim on the
         record that the safety net is restored when half of it is on the floor.
         ✅ **DONE 2026-08-09.** A correction banner was added to
         `2026-08-08-the-safety-net-that-had-stopped-catching-executive-summary.md`
         and the outcome written up in
         `2026-08-09-the-half-red-safety-net-executive-summary.md`.

      **Do not "fix" this by deleting or skipping the failing specs.** Six files
      were disabled-by-accident for four months and that is the incident this
      whole thread of work exists to prevent.

- [ ] **Audit every e2e mock handler against whether its app-side reader
      unwraps `body.data`.** Likely the dominant cause of the 146 e2e failures,
      and a systematic fix rather than 22 separate spec refreshes.

      ⚠️ **PARTIALLY DONE 2026-08-09 — the prediction was half right.** The
      envelope gap was indeed the single most common cause and was fixed
      wherever it appeared (#2224, #2226, #2229, #2236). But it was **not** a
      systematic fix: specs that mock by patching `window.fetch` inherit nothing
      from `setupMockApi`, so each one needed its own copy. Note the two
      exceptions that bite in the opposite direction — `getBookTags` and
      `getBookExternalIDs` read the **top-level** body, so enveloping those
      breaks them.

      A related hazard found the same night and worth its own pass: a specific
      branch in `setupMockApi` placed *below* a `startsWith(...)` prefix
      catch-all is unreachable and fails **silently** with a 200. Three separate
      instances existed (`/audiobooks/batch`, `/audiobooks/<id>/files`,
      `/authors*`). See `todo.d/20260809-dead-bulk-fetch-dialog.md`.

      **Confirmed for `dedup.spec.ts` (26 failures, the largest single file).**
      `src/services/api.ts:1402` reads:

          const body = await response.json();
          const data = body.data;
          return { groups: data.groups || [], ... };

      while `test-helpers.ts:825` mocks `/api/v1/authors/duplicates` as:

          jsonResponse({ groups: dedup.groups, needs_refresh: ... })

      — **unwrapped**. So `body.data` is `undefined`, the page renders zero
      groups, and every assertion looking for an author heading fails. The spec
      itself is fine; it passes real fixture groups in.

      **Why this is probably not just dedup.** Wave 2 (#2191) fixed exactly this
      envelope for **eight** endpoints — `/auth/status`, `/import-paths`,
      `/authors`, `/series`, `/audiobooks/soft-deleted`, bare `/audiobooks/:id`,
      `/audiobooks/:id/versions`, `/filesystem/*` — and those spec files are now
      green. But **80 endpoints in `api.ts` unwrap `body.data`**. Eight are
      covered. The remaining ~72 are unaudited, and `/authors/duplicates` being
      broken is the first one anybody checked.

      **⚠️ Confidence, stated honestly.** The dedup cause is *verified* by
      reading both sides. The claim that this explains the other 21 files is
      *plausible and unverified*. This estimate has already been revised twice
      — first "a few cascading root causes", then "22 files of independent
      drift" — so verify a second and third file before planning around it.
      Cheapest check: pick a failing test in `library-browser.spec.ts` (14) and
      `metadata-provenance.spec.ts` (12), find the endpoint its page calls, and
      compare the mock's shape to the reader's.

      **Suggested approach if it holds:** rather than hand-patching handlers one
      at a time, make the envelope the default. A single helper — e.g. wrap
      every `jsonResponse` body as `{ ...body, data: body }` unless the handler
      opts out — matches what wave 2 already did piecemeal in
      `test-helpers.ts` and would cover all ~72 at once. Opt-out matters:
      endpoints that legitimately return bare arrays or non-envelope shapes must
      not be double-wrapped.

      **Do not skip or delete failing specs to make this go away.** Six files
      were disabled-by-accident for four months and that is the incident this
      entire thread exists to prevent.

- [x] **The e2e failures have MIXED causes — do not plan a single systematic
      fix.** Sampled 2026-08-08 after a fragment filed an hour earlier
      speculated that one data-envelope gap might explain most of the 146.
      **It does not.** Four files sampled, at least three distinct causes:

      ✅ **CONFIRMED CORRECT and closed 2026-08-09.** All four predictions held,
      and the fourth paid off: the hunch that `search-and-filter` was "the only
      one of the four that might indicate a real product defect" was **right**.
      Two real defects sit behind it — `searchBooksPage` sends no
      `library_state`, `filters`, `tags` or `sort_by`, so typing in the search
      box silently discards every active filter and the sort order; and the
      search is not debounced at all (ten requests for a ten-character query).
      Both are in `todo.d/20260809-search-drops-filters-and-debounce.md`, and
      the two tests are `test.fixme` rather than rewritten to match broken
      behaviour. The final cause tally across all 22 files was four shapes, not
      one: missing envelope; a mock branch shadowed by a prefix catch-all; UI
      relocated rather than removed; and mock field name ≠ book field name.

      - **`dedup.spec.ts` (26)** — the data-envelope bug, *verified*.
        `api.ts:1402` reads `body.data.groups`; the mock returns
        `/authors/duplicates` unwrapped. Fix: wrap the handler.
      - **`library-browser.spec.ts` (14)** — genuine affordance drift. The test
        clicks `getByRole('combobox', { name: 'Sort by' })`; no such control
        exists. Nothing to do with data shape.
      - **`metadata-provenance.spec.ts` (12)** — book-detail page renders no
        heading for the fixture book. Could be an envelope gap on a book
        endpoint or a navigation change; not yet traced.
      - **`search-and-filter.spec.ts` (10)** — behavioural, not structural. The
        test searches, then asserts a non-matching book disappears; "The Hobbit"
        stays visible. Either the mock never implements filtering, or search is
        genuinely not filtering. **Worth tracing first** — it is the only one of
        the four that might indicate a real product defect rather than test rot,
        and it sits next to the known server-side filtering weakness (an
        unrecognised filter param returns the entire library with HTTP 200).

      **Estimate history, kept deliberately.** This has now been framed three
      ways in one evening: "a few cascading root causes" → "22 files of
      independent drift" → "probably one envelope gap". The middle one was
      closest. The third came from verifying exactly ONE file and generalising,
      which is the same error each time — concluding from the first sample that
      agreed with a convenient theory. Whoever picks this up should assume mixed
      causes and re-sample rather than trust any single framing, including this
      one.

      **Practical consequence:** budget per-file work, not one sweep. The
      envelope fix is still worth doing (it is cheap and clears the largest
      file), but it will not clear the other 21.

- [x] **CLOSED 2026-08-14 — the repair wave finished; the suite reached 552 passed /
      0 failed / 16 skipped on 2026-08-09 (see the DONE entry above), which includes this
      file.** Kept for the mock-vs-server rhyme note below. Original:
      **`search-and-filter.spec.ts` (10 failures) is a MOCK gap, not a product
      defect — the e2e mock's `/audiobooks` handler ignores every filter
      param.** Traced 2026-08-08. This downgrades the earlier flag that it
      "might indicate a real product defect"; it does not.

      **The chain, verified end to end:**

      - `api.searchBooksPage()` (`src/services/api.ts:1023`) issues
        `GET /audiobooks?search=<q>&limit=&offset=&is_primary_version=true`.
        It does **not** call `/audiobooks/search`.
      - The mock's `/api/v1/audiobooks` handler
        (`tests/e2e/utils/test-helpers.ts:768`) reads **only** `offset` and
        `limit`. `search`, `filters`, `tags`, `library_state`,
        `fingerprint_status` and the rest are all ignored; it returns
        `mockState.books.slice(offset, offset+limit)`.
      - So a search returns the whole library, the non-matching book stays on
        screen, and `expect(...).not.toBeVisible()` fails. The app is behaving
        correctly; the fake server is not.
      - Dead code worth deleting or wiring up: the mock has a
        `/api/v1/audiobooks/search` handler that filters properly, and
        **nothing in `src/` calls that endpoint**.

      **Explicitly ruled out:** the empty-state fix (#2195), which now preserves
      the last known-good page when a load fails, is NOT implicated. The search
      request succeeds — it just returns everything — so the preserved-list
      behaviour never engages here.

      **Fix:** teach the mock's `/audiobooks` handler to honour the params the
      app actually sends. Minimum for this spec is `search`; doing `filters`
      (the JSON field-filter array) at the same time is worth it, because the
      In Progress / Finished sidebar filters ride on that param and any future
      test of them would hit the identical wall.

      **Note for the real backend work.** This is the mock, so it says nothing
      about the server. But it rhymes with the open server-side task: an
      unrecognised filter param on the real API returns the entire
      44,874-book library with HTTP 200 rather than failing closed. Two
      different layers, same failure mode — a filter that is ignored rather
      than rejected looks exactly like a filter that matched everything.

- [x] **"Browse by Tag" should start collapsed, or show only the top few tags.**
      Reported by the owner 2026-08-08: *"Browse by tag should start minimized
      as we have tons of tags or only show the top 5."* On a library this size
      the tag cloud renders as a wall of chips that pushes the actual book grid
      below the fold.

      **Current behaviour** (`web/src/components/library/TagCloud.tsx`):

      - Line 41: `const [expanded, setExpanded] = useState(true)` — it defaults
        to **open**.
      - It renders `availableTags.map(...)` with **no cap**: every tag in the
        library, every time.
      - The collapse machinery already exists (header row toggles, `Collapse`,
        rotating chevron, correct `aria-label`), so "start minimized" is
        essentially a one-word change.

      **Two options the owner offered; they are not exclusive and the good
      version is both:**

      1. **Start collapsed** — flip line 41 to `useState(false)`. Trivial, and
         it makes the component honest: a disclosure control whose default is
         "already disclosed" is not doing anything.
      2. **Show the top N (5) when collapsed-ish** — render a short preview row
         of the highest-count tags with a "Show all (N)" affordance, so the
         feature is still discoverable without costing a screenful. This is the
         better UX of the two, because a fully collapsed panel gives no hint
         that tags are worth browsing.

      **⚠️ Verify sort order before slicing.** `availableTags` is passed
      straight through from `Library.tsx` (lines 1971 and 1993 — note it feeds
      **both** `TagCloud` and `FilterSidebar`) and **it has not been confirmed
      that it arrives sorted by count descending**. `TagCloud` currently only
      uses `count` for font size, where order does not matter, so a latent
      sort bug would be invisible today and would silently make "top 5" mean
      "first 5 alphabetically". Sort explicitly in the component rather than
      trusting the caller.

      **Persist the open/closed choice** in `localStorage` alongside the other
      Library view preferences (`STORAGE_KEYS`), so someone who opens it does
      not have to re-open it on every visit. Without that, "start collapsed"
      trades one annoyance for another.

      **Acceptance:** on a fresh visit to /library the book grid is visible
      without scrolling past the tag cloud; tags remain reachable in one click;
      and if a top-N preview is used, the tags shown are genuinely the most
      common ones, verified against a library with many tags rather than a
      handful of fixtures.

      **✅ SHIPPED same day.** Implemented as *both* options rather than either:

      - Starts **collapsed** (`useState(readStoredExpanded)`, default false).
      - Collapsed still shows the **top 5 by count** plus a "Show all (N)"
        button, so the feature stays discoverable. A disclosure control that
        reveals nothing tells the user nothing.
      - **Sorted explicitly in the component** (`count` desc, then name), which
        the note above flagged: the caller's order was never guaranteed and
        slicing an unsorted list would have quietly shown "the first five".
      - **Persisted** via `STORAGE_KEYS.LIBRARY_TAG_CLOUD_EXPANDED`, wrapped in
        try/catch so private-browsing storage failures fall back to collapsed
        rather than throwing.
      - The header shows the total tag count while collapsed, so the panel says
        how much is hidden.
      - **Selected tags outside the top 5 are always shown** while collapsed.
        This was not in the original request but is required for correctness:
        hiding an active filter leaves the user looking at a filtered list with
        no visible control to clear it.

- [ ] **The library "Sort by" control no longer exists — 4 e2e tests target a
      surface that is gone.** Found 2026-08-09 while repairing
      `library-browser.spec.ts`.

      **Test side ✅ DONE (#2230); product decision ⏳ STILL OPEN.** The four
      tests now drive sort through the URL (`?sort=…&order=…`), which is the
      only surviving mechanism, so the sort *behaviour* stays covered. What is
      unresolved is whether losing the control was intentional. Hard evidence:
      `SearchBarProps` (`web/src/components/audiobooks/SearchBar.tsx:124-131`)
      has no `onSortChange` prop at all, and
      `web/src/components/library/LibraryBookGrid.tsx:133` receives the handler
      as `_handleSortChange` — underscore-prefixed to mark it deliberately
      unused. `SearchBar.test.tsx:43` asserts "does not render sort controls
      when `onSortChange` is absent", which now passes vacuously because the
      prop cannot be supplied. Either restore the control, or delete the dead
      state and that vacuous unit test.
      Full write-up: `todo.d/20260809-library-sort-control-missing.md`.

      `sorts books by title ascending` / `title descending` / `author` /
      `date added` all do:

          await page.getByRole('combobox', { name: 'Sort by' }).click();
          await page.getByRole('option', { name: 'Title' }).click();

      There is **no such control anywhere in the library UI**. Grepping the
      components turns up no `Sort by` label and no sort dropdown in
      `FilterPanel`, `LibraryToolbar` or `SearchBar`. Sorting now happens
      through the table view's column headers
      (`LibraryBookGrid`'s `handleColumnSortChange` → `ConfigurableTable` /
      `AudiobookList`), which the default grid view does not show at all.

      **This is a rewrite, not a selector tweak**, which is why it was left out
      of the mock-fix PR: the tests must switch to list/table view first and
      then drive column-header sorting, and the assertions about resulting
      order need to match however that view renders.

      The mock now honours `sort_by` / `sort_order` correctly (added
      2026-08-09), so once the tests drive the real control the backend half of
      this is already in place.

      **Check before rewriting:** confirm sorting is genuinely still reachable
      by a user in the default grid view. If it is not, that is a product
      question — "you can no longer sort the library without switching views"
      — and should be raised rather than encoded into a test.

- [ ] **The unified Dedup view has no e2e coverage at all.** Found 2026-08-09
      while repairing `dedup.spec.ts`.

      `/dedup` was redesigned: it now renders a unified candidate surface
      (bands All / Certain / High / Medium / Review, a candidate table, "Find
      Duplicates" / "Rescore" / "Force Full Rescan" actions). The old tabbed
      Books / Authors / Series UI still exists but sits behind a **"Legacy
      View"** toggle persisted as `sessionStorage.dedup_show_legacy`.

      Every test in `dedup.spec.ts` covers the **legacy** view — they now opt
      into it explicitly via `enableLegacyDedupView()`. That was the right fix
      (the legacy features are still shipping and were previously untested by
      accident), but it means the surface a user actually lands on has **zero**
      automated coverage.

      Worth covering: band filtering, the candidate table, merge/dismiss
      actions, and the legacy toggle itself round-tripping through
      sessionStorage.

      Note the gap was invisible for the same reason the last one was: the
      specs did not fail with "this UI no longer exists", they failed with
      `element(s) not found`, which reads identically to a broken selector.

- [x] **CLOSED 2026-08-14 — suite green 552/0/16 on 2026-08-09 includes this file (metadata-provenance 13/0/0 recorded in the #2267 entry).** Original: **4 remaining failures in `metadata-provenance.spec.ts`** (down from 12).
      Diagnosed but not fixed 2026-08-09; stopped deliberately rather than
      keep iterating.

      Fixed in that pass (8 of 12): the spec mocks by patching `window.fetch`
      rather than using `page.route`, so it gets none of the shared handlers
      and needed its own `/auth/status` (without it the app rendered the LOGIN
      screen), its own `{ data: ... }` envelope, and explicit handlers for the
      book sub-resources — the generic "URL contains the book id" branch was
      swallowing `/files`, `/versions`, `/tags`, `/segments` and handing each
      of them the BOOK object, so the page crashed on `.length` of undefined.

      **Still failing, with what is known:**

      1. `dialog opens with all fields populated` — the Author textbox is
         empty. The dialog reads `formData.author`, and the detail page renders
         "Unknown Author", so the payload shape is wrong somewhere between the
         fixture and `Audiobook`. Adding `author_name`/`series_name` alongside
         the short names did NOT fix it, so the mapping is elsewhere — trace
         how `formData` is initialised from the API response rather than
         guessing again.
      2. `locked fields show orange lock icon` — walks the DOM relative to
         `getByLabel('Title *')` (`'..'` → `'..'` → `button`) to reach the lock
         icon. Fragile by construction: it depends on the exact wrapper depth
         MUI renders. Better fixed by giving the lock button a stable
         `data-testid` than by counting parents.
      3. `editing a field automatically locks it` and 4. `year field shows
         error for non-numeric input` — both start from the same field
         locators; likely fall out with (1) and (2).

      **Locator rule established in this file, worth keeping:** to READ or FILL
      a field use `getByRole('textbox', { name, exact: true })` —
      `getByLabel('Title *')` is a strict-mode violation because each field has
      an adjacent lock button labelled "Lock Title *" and getByLabel
      substring-matches. The lock tests still use `getByLabel` on purpose,
      because they traverse relative to it. A blanket sweep converting all of
      them broke passing tests; the note in the spec says so.

- [x] **CLOSED 2026-08-14 — suite green 552/0/16 on 2026-08-09 includes this file.**
      The caution below is the part worth keeping. Original: **8 remaining failures in
      `batch-operations.spec.ts`** (down from 11), and a caution about how they were approached.

      **Fixed (3):** the "N selected" chip is rendered TWICE in the tree, so
      `getByText('1 selected')` was always a strict-mode violation. Assertions
      now use `.first()` — the behaviour under test is that the count shows, not
      how many places show it. *(If that duplication is itself unintended, it is
      a UI question worth asking separately.)*

      **Verified renames applied:** the toolbar button "Fetch Metadata" is now
      **"Fetch Selected"**, and "Deselect All" is now **"Deselect"** — read off
      the app's rendered accessible names, not guessed.

      **⚠️ Trap, hit and recorded:** the confirm button INSIDE the "Bulk Fetch
      Metadata" dialog is **still "Fetch Metadata"** (`LibraryDialogs.tsx`
      renders `Fetching…` / `Fetch Metadata`). A blanket find-and-replace of
      "Fetch Metadata" → "Fetch Selected" therefore breaks the dialog-scoped
      references. Only the toolbar button was renamed. The spec now carries a
      comment at that call site.

      **Still failing (8):** `deselects all books`, the five bulk-fetch tests,
      `batch updates metadata field`, and `disables batch operations when no
      books selected`. The count did NOT move across three separate attempts
      (chip fix, renames, dialog-scope correction), which means the remaining
      cause has not actually been found yet — the failures are almost certainly
      NOT more label drift.

      **Do this next, and do it first:** open the Playwright DOM snapshot for
      one bulk-fetch failure (`test-results/*/error-context.md`) and read what
      is actually on the page at the moment of failure. Every real cause found
      in this repair effort — the dedup page being redesigned behind a "Legacy
      View" toggle, metadata-provenance rendering the LOGIN screen, the book
      sub-resources returning the book object — was found that way, and every
      wrong guess came from reasoning about what *should* be there instead.

- [x] **`transcode-and-counting.spec.ts` (11 failures) — two hypotheses tested
      and REJECTED. Read this before trying either again.**
      Investigated 2026-08-09; no code shipped, because nothing that was tried
      improved the count and one attempt made it worse.

      **What the page actually shows** (from the Playwright DOM snapshot, which
      is the only thing that has reliably told the truth in this effort): the
      Dashboard renders normally but **every count is 0** — Library Books,
      Import Path Books, Authors, Series.

      **Rejected hypothesis 1 — missing `{ data: ... }` envelope on the specs'
      inline `page.route` overrides.** Real in principle: these tests call
      `route.fulfill` directly, bypassing `jsonResponse()` in test-helpers, and
      `api.getSystemStatus()` reads `body.data`. Wrapping all 11 success
      payloads changed the result by **zero**. The envelope is not what is
      breaking this file.

      **Rejected hypothesis 2 — the mock ignoring `is_primary_version`.**
      Reasoning looked sound: `api.getBooks()` always sends
      `is_primary_version=true`, `countBooksFiltered()` reads
      `body.data?.count` from that same endpoint, and the fixture is exactly 2
      primary + 1 non-primary against an expected count of 2. Teaching the mock
      to honour the param took failures from **11 to 12** — a regression. It
      was reverted. Do not re-apply without understanding why it hurt.

      **The one solid lead:** "Library Books" does NOT come from
      `/system/status`, which is what these tests mock. It comes from
      `api.countBooksFiltered()` → `GET /audiobooks?...` → `body.data?.count`
      (`services/api.ts:1058`). So a test that overrides `/system/status` to
      control the dashboard count is mocking the wrong endpoint entirely. That
      is worth confirming as the root cause before writing any more code.

      **Method note.** Every real cause found in this repair effort came from
      reading `test-results/*/error-context.md` and looking at what was on the
      page. Every wrong guess — including both above — came from reasoning
      about what should be there. Look first.

      ✅ **RESOLVED 2026-08-09 (#2229): 11 → 0.** The "one solid lead" recorded
      above was **also wrong**. It concluded that "Library Books" comes from
      `countBooksFiltered` rather than `/system/status`, and therefore that
      these tests mock the wrong endpoint. `Dashboard.tsx:97` reads
      `systemStatus.library_book_count ?? systemStatus.library.book_count`, so
      `/system/status` *is* the right endpoint. What was wrong was the **shape**:
      both overrides returned a flat, un-enveloped body, `api.getSystemStatus`
      returns `body.data`, and Dashboard then threw on `.library` — which is why
      *every* count read 0, including Authors and Series that have their own
      endpoints entirely. That is exactly the misdirection that made rejected
      hypothesis 1 look like it changed nothing: the envelope alone is not
      enough without the nested `library.book_count` shape. Both together took
      it 11 → 9; the rest were the Manage Versions relocation, two
      route-patterns that never matched, and a button that relabels itself to
      "Converting..." mid-assertion.

- [ ] **Stop Deluge writing in-progress downloads directly into the new-books
      import directory.** A torrent that is still downloading is visible to the
      scanner as a book, so a partial file gets imported as if it were complete:
      wrong duration, wrong file size, a truncated or absent intro clip, and a
      transcription/fingerprint pass that runs against bytes which will change
      underneath it.

      Fix: give Deluge a staging directory OUTSIDE the watched tree and have it
      **move** the completed torrent into the import directory only on
      completion. A move within the same filesystem is an atomic rename, so the
      scanner can never observe a half-written book. A copy across filesystems
      is NOT atomic — if staging and import must live on different filesystems,
      copy to a dotfile/temp name inside the import dir and rename into place as
      the final step.

      Deluge supports this natively: set "Download to" = staging path and "Move
      completed to" = import path.

      Also worth adding as defence in depth, since Deluge is not the only way
      files arrive:
      - Scanner ignores partial-download suffixes (`.part`, `.!ut`, `.tmp`) and
        dotfiles.
      - Quarantine a candidate whose size or mtime changed between the scan and
        the import rather than importing it.

      🔴 Suspected to be a real source of existing bad rows — worth measuring how
      many books have a duration or file size inconsistent with their format
      before assuming this is only a forward fix. Silently-truncated books would
      also explain some fraction of the `[SILENCE]` sentinels and short/failed
      intro transcriptions.

- [ ] **Require every operation to support `dry_run`, and enforce it at the
      registry rather than by convention.** Any op that mutates state must be
      runnable in a mode that computes and reports exactly what it WOULD do,
      writing nothing — so it can be tested independently and reviewed before it
      touches prod.

      **Motivating case (2026-08-07).** Three maintenance ops were run in one
      session and they did not agree with each other:

        maintenance.repair-transcribe-status      dry_run, defaults TRUE
        maintenance.intro-migrate-single-file     dry_run, defaults TRUE
        maintenance.transcribe-book-intros        NO dry_run at all

      The first two could be previewed, reconciled bucket-by-bucket against the
      full book count, and gated on real numbers. The third — a reparse that
      rewrites parsed title/author/narrator across the library — had no preview
      mode whatsoever: dispatching it IS applying it. The only reason that was
      acceptable was an unrelated internal guard (reparse only ever upgrades),
      which is luck, not design.

      **What "supported" should mean** — a bare `dry_run` bool is not enough:

      - **Declared, not optional.** Put it on `OperationDef` (e.g.
        `SupportsDryRun bool`, or better, make the param struct embed a shared
        `DryRunParams`). An op declaring `CapLibraryWrite` without dry-run
        support should fail registration, so the gap is caught at startup rather
        than discovered while someone is deciding whether to hit apply.
      - **Default TRUE for destructive ops.** Both ops that had it defaulted to
        dry-run; that is the right default and should not be per-author choice.
      - **Report per-reason counts that RECONCILE.** The value of the two
        previewable ops was that every item landed in exactly one labelled
        bucket and the buckets summed to the population — 11,315 + 19,505 + 0 +
        12,587 + 1,463 + 7 = 44,877 exactly. "would change 30,820" with no
        account of the rest is the shape of report that hides a bug. Consider a
        shared result type so this is structural rather than remembered.
      - **Same code path.** The dry run must execute the identical decision
        logic and diverge only at the write, or it is testing something other
        than what will run. Both existing ops do this correctly (classify, then
        branch on `dryRun` immediately before the store call) — that pattern is
        the one to generalise.

      Related: the write-set/scheduler-conflict work
      (`OperationDef.Writes []Resource`). Both are the same idea — an op should
      DECLARE what it does, and the system should enforce it, instead of every
      author re-deciding by hand.

- [x] **Re-run the CPU-node Whisper benchmark in POOL configuration, during the
      day.** ✅ **DONE 2026-08-08.** Full sweep run on U1 (`ssh u1`); raw log
      `/opt/whisper-bench/pool.log`, scripts `bench_pool.py` (phase 1) and
      `bench_pool2.py` (phase 2). Same methodology as the evening run: 10 real
      prod clips, base.en, beam 5, VAD on, mirroring `scripts/whisper_server.py`.

      **Measured (clips/min, and projected days for the 260k-file tier-3 tail):**

        shape        int8_float32              float32
        1 x 48        ~2.04  (~89 days)         2.39  (75.4 days)
        4 x 12        44.13  ( 4.1 days)        —
        8 x 6         63.86  ( 2.8 days)       40.48  (4.5 days)
        12 x 4        67.52  ( 2.7 days)       45.80  (3.9 days)
        16 x 3        67.82  ( 2.7 days)        —
        24 x 2        75.83  ( 2.4 days)  <--  47.91  (3.8 days)
        32 x 1        75.09  ( 2.4 days)        —
        48 x 1        76.31  ( 2.4 days)       47.92  (3.8 days)

      **➡️ Recommended config: 24 workers x 2 threads, `int8_float32`.**
      Throughput saturates at ~75-76 clips/min across 24, 32 and 48 workers —
      three points spanning a 2x range, so this is a real ceiling and not two
      adjacent samples that happened to agree. 24x2 reaches it with the fewest
      processes and the fewest resident models, so it is the cheapest way to
      buy the plateau.

      **The tier-3 tail drops from ~92 days to ~2.4 days.**

      **Corrections to the assumptions this entry previously recorded:**

      - The estimate of "~10-14 clips/min, ~13-18 days" was **~5x pessimistic**.
        Actual is 75.83 and 2.4 days. Pooling did not merely scale linearly off
        the single-process number, because the single-process number was itself
        crippled.
      - "**One process cannot use 48 cores**" — confirmed, and it is the whole
        story. Every row above uses the same 48 total threads. The only variable
        is how they are divided, and it moves throughput **32x** (2.39 -> 76.31).
        This is not a hardware headroom finding; it is ctranslate2 being unable
        to use a wide intra-op thread count.
      - "**int8 buys nothing on this host**" — needs qualifying, because **the
        compute-type winner flips with configuration**. Single-process,
        `float32` is fastest (2.39 vs ~2.04 for int8_float32 vs 1.96 for int8).
        In every pool shape measured, `int8_float32` wins decisively — 63.86 vs
        40.48 at 8x6, 67.52 vs 45.80 at 12x4, 75.83 vs 47.91 at 24x2, and
        76.31 vs 47.92 at 48x1 — a consistent ~50-59% advantage at four
        separate shapes. Both compute types have their OWN plateau, and the
        gap persists there: int8_float32 tops out ~75-76 clips/min while
        float32 tops out ~47.9 (47.91 at 24x2 and 47.92 at 48x1, which is
        about as clean a plateau as this harness can show). Since a pool is
        what would actually ship, the original note is closer to right than a
        single-process comparison suggests. Working hypothesis (NOT measured):
        concurrent workers saturate memory bandwidth and int8 weights halve that
        traffic, while a single 48-thread process is compute-starved by poor
        thread scaling so bandwidth never becomes the limit.

      **Methodology note worth keeping.** The original script mapped the 10
      clips once per config. That cannot measure a 12-worker pool — two workers
      would idle — and at 8 workers each does barely one clip, so the number
      measures pool spin-up and imbalance rather than throughput. Every pool run
      now replicates the clip list to at least 4 tasks per worker. Compute per
      clip is identical on repeat (model resident, audio decode + inference
      still run), so tasks/wall stays a fair throughput figure.

      **⚠️ These are upper bounds for the transcription step alone.** The
      harness reads local WAVs and discards the text. A production run also
      fetches/decodes real audiobook files, writes results to Pebble, and
      updates per-file status — none of which is in these numbers. Treat 2.4
      days as the floor for the compute, not a schedule for the operation.

- [ ] **TODO-MUI-1** MUI upgrade Step 1 — `@mui/*` 5.14 → 6.x (brief:
      `docs/plans/2026-08-07-mui-upgrade-path.md`; requires TODO-MUI-0 merged;
      do NOT continue to v7 in the same session/PR)
  - `cd web && npm install @mui/material@6 @mui/icons-material@6`
  - Codemods (run from repo root):
    `npx @mui/codemod@latest v6.0.0/sx-prop web/src` and
    `npx @mui/codemod@latest v6.0.0/theme-v6 web/src/theme.ts`.
    Skip `v6.0.0/grid-v2-props` (we have zero Grid2 — legacy Grid stays as-is
    until v7) and `v6.0.0/list-item-button-prop` (0 `<ListItem button` measured).
  - Expected hand-fixes (from the 2026-08-07 inventory):
    - Test churn from the v6 ripple rework: `fireEvent` interactions on
      Button/Checkbox/Chip/Radio/Switch/Tabs may need
      `await act(async () => fireEvent...)` — fix failing Vitest specs, don't
      skip them.
    - `Typography color=` (405 usages): palette tokens keep working; audit
      only non-palette CSS values (move those into `sx`).
    - Accordion summary now renders a heading/button — check
      `grep -rln Accordion web/src` sites for snapshot/E2E fallout.
  - Do NOT adopt Pigment CSS; Emotion remains the engine.
  - Gate: `make build` (embedded UI serves), `make test-all`, `make test-e2e`,
    manual smoke of Library, Book Detail, Activity Log, System > Maintenance,
    Dedup tabs; note (don't fix) new MUI deprecation warnings in the PR body.
  - Rollback: `git revert` of this single PR (lockfile reverts with it).

- [ ] **TODO-MUI-2** MUI upgrade Step 2 — `@mui/*` 6.x → 7.x including the
      one-time Grid conversion (brief: `docs/plans/2026-08-07-mui-upgrade-path.md`;
      requires TODO-MUI-1 merged; do NOT continue to v9 in the same session/PR)
  - `cd web && npm install @mui/material@7 @mui/icons-material@7`
  - Grid: convert legacy Grid → new Grid NOW (do not rename to `GridLegacy` —
    it is removed in v9 and we'd pay twice):
    `npx @mui/codemod@latest v7.0.0/grid-props web/src`
    Inventory says 175 `<Grid item` and 35 `<Grid container` across 23 files;
    codemod output is `item xs={12} sm={6}` → `size={{ xs: 12, sm: 6 }}`,
    `xs` → `size="grow"`. After it runs, `grep -rn "<Grid item" web/src`
    must return 0.
  - Hand-verify layout on every Grid file: new Grid spaces with CSS `gap` and
    containers no longer stretch full-width by default — compare against the
    TODO-MUI-0 smoke baseline. Highest-risk files: `web/src/pages/Series.tsx`,
    `web/src/pages/Authors.tsx`, `web/src/pages/Dashboard.tsx`,
    `web/src/components/settings/ITunesImport.tsx`.
  - `npx @mui/codemod@latest v7.0.0/input-label-size-normal-medium web/src`
    (idempotent, cheap).
  - Build must confirm icon path imports still resolve under the v7 package
    layout (TODO-MUI-0 normalized the `.js` suffixes; if `npm run build`
    still errors on `@mui/icons-material/X`, switch those files to named
    barrel imports).
  - Known no-ops for this repo (verified 0 usages 2026-08-07): `Hidden`,
    deep >1-level imports, `createMuiTheme`, `onBackdropClick`, `@mui/lab`,
    `CssVarsProvider` mode behavior.
  - Gate: `make build` (embedded UI serves), `make test-all`, `make test-e2e`,
    manual smoke with EXTRA attention to spacing/layout on Library, Book
    Detail, Activity Log, System > Maintenance, Dedup tabs.
  - Rollback: `git revert` of this single PR.

- [x] ~~**TODO-MUI-3** MUI upgrade Step 3 — React 18 → 19~~ (OPTIONAL but
      recommended; brief: `docs/plans/2026-08-07-mui-upgrade-path.md`; requires
      TODO-MUI-2 merged — MUI v7 supports React 19, v5/v6 pairings are riskier;
      do NOT combine with the v9 bump in the same session/PR)
      — closed 2026-08-22. PR #2703 (TASK-097) removed the last outstanding
      sub-bullet, the `react-is` override. Because that PR was a one-line
      `package.json` edit and this item is the whole React 18→19 upgrade, every
      other bullet was re-checked at HEAD before closing rather than closing on
      the strength of the last edit: `react`/`react-dom` are `^19.2.8`,
      `@types/react`/`@types/react-dom` are `^19.2.x`, `overrides` retains only
      `minimatch` and `brace-expansion` (no `react-is`), and `grep -rn "test-utils"
      web/src` is empty. All bullets satisfied.
  - Why: MUI v9 does NOT require React 19 (peers `^17 || ^18 || ^19`), but
    upgrading first deletes the `react-is` override hack, matches the
    combination MUI tests first-class, and pre-positions for the post-v9
    styling-layer refactor.
  - `cd web && npm install react@19 react-dom@19 && npm install -D @types/react@19 @types/react-dom@19`
  - `npx codemod@latest react/19/migration-recipe` (covers
    `ReactDOM.render` → `createRoot`, `react-dom/test-utils` `act` →
    `react`'s `act`, propTypes/defaultProps removal on function components).
  - Hand-check afterwards: `grep -rn "test-utils" web/src`,
    `grep -rn "defaultProps" web/src`, `grep -rn "useRef()" web/src`
    (React 19 `useRef` requires an argument), and Vitest setup files under
    `web/src/test/`.
  - Remove the `react-is` override added in TODO-MUI-0 from
    `web/package.json` (no longer needed on React 19) and `npm install`.
  - Gate: `make build` (embedded UI serves), `make test-all`, `make test-e2e`,
    manual smoke of Library, Book Detail, Activity Log, System > Maintenance,
    Dedup tabs; zero new console errors.
  - Rollback: `git revert` of this single PR.

- [ ] **TODO-MUI-4** MUI upgrade Step 4 — `@mui/*` 7.x → 9.x (final hop; there
      is NO Material UI v8 — v7 jumps straight to v9 to align with MUI X; brief:
      `docs/plans/2026-08-07-mui-upgrade-path.md`; requires TODO-MUI-2 merged,
      TODO-MUI-3 recommended first; single PR, nothing else in the session)
  - `cd web && npm install @mui/material@9 @mui/icons-material@9`
  - If still on React 18 (TODO-MUI-3 skipped): KEEP the
    `"overrides": { "react-is": "^18.2.0" }` in `web/package.json` — MUI v9
    ships react-is@19 internally and mismatches cause runtime prop-type
    errors on React 18.
  - System props removed from Box/Stack/Typography/Grid/Link/DialogContentText
    — ~381 direct-prop usages measured 2026-08-07. Run the v9 system-props
    codemod (confirm exact name on
    https://mui.com/material-ui/migration/upgrade-to-v9/ or via
    `npx @mui/codemod@latest --help`), converting e.g.
    `<Box mt={2} color="primary.main">` → `<Box sx={{ mt: 2, color: 'primary.main' }}>`.
    Then hand-sweep for leftovers:
    `grep -rnE '<(Box|Stack|Typography|Grid|Link)[^>]*\s(mt|mb|ml|mr|mx|my|m|pt|pb|pl|pr|px|py|p|gap|bgcolor|display)=\{' web/src --include='*.tsx' | grep -v 'sx='`
    Misses fail SILENTLY (ignored prop → styling vanishes), so eyeball the
    smoke pages, don't trust compile success.
  - Slot-prop removals: `InputProps` (24 usages) → `slotProps.input`,
    `componentsProps` (4) → `slotProps`. Run the relevant
    `npx @mui/codemod@latest deprecations/<component>-props web/src` codemods
    for TextField/Input plus anything tsc flags; hand-fix the remainder.
  - Grid checks: `grep -rn "GridLegacy" web/src` must be 0 (TODO-MUI-2
    converted us), and `grep -rnE '<Grid[^>]*direction="column' web/src`
    must be 0 (removed in v9 — replace with Stack).
  - Emotion remains the default engine in v9; no Pigment CSS work.
  - Gate: `make build` (embedded UI serves), `make test-all`, `make test-e2e`,
    manual smoke of Library, Book Detail + Metadata Review dialog, Activity
    Log, System > Maintenance, Dedup tabs, checking specifically for
    silently-dropped spacing/color styling.
  - Rollback: `git revert` of this single PR.

- [x] **Refresh the remaining content-drift e2e failures unmasked by the `_page` fix.**
      PR #2178 (2026-08-07) fixed the fixture error that had silently killed six
      e2e spec files since April 2026. With the mask gone the suite fails
      honestly: all failures are pre-existing assertion drift — tests assert
      hardcoded UI text the app no longer renders. Wave 1 (2026-08-07) fixed
      Dashboard (6) and Book Detail (3): the api layer's `{ data: ... }`
      response envelope, the unmocked `/api/v1/system/storage` endpoint, the
      `/operations` → `/activity` route rename, and unmocked auth endpoints.
      Wave 2 (2026-08-08) cleared the remaining **34** chromium failures across
      four files — Error Handling 3, File Browser 8, Import Audiobook File 13,
      Operation Monitoring 10. (The per-file counts recorded here originally
      said File Browser 9 / Import 14; the measured baseline was 8 / 13, total
      34.) Root cause for 24 of them was the same missing `{ data: ... }`
      envelope in `web/tests/e2e/utils/test-helpers.ts` — `/auth/status` in
      particular meant `AuthContext` never initialized, degrading every mocked
      page. The rest was renamed-affordance drift. `operation-monitoring.spec.ts`
      needed a full rewrite: its target page was deleted in afe18e8f and
      `/operations` is now a redirect to `/activity`. No product code changed.

      **✅ VERIFIED 2026-08-08 06:48–07:08.** #2191 was merged with its suite
      result explicitly unverified — the agent that wrote the fixes stalled
      before it could run a final full pass. A complete `npm run test:e2e` has
      now been run against `main` at `60030428`:

          130 passed, 7 skipped, 0 failed, 0 flaky  (19.8m)

      The wave-2 counts were measured on **chromium only**, but `test:e2e` runs
      `--project chromium --project webkit`, so the suite is green on both
      engines.

      **⚠️ CORRECTION (2026-08-08 08:00).** The paragraph above originally also
      claimed the run "covers the Library changes merged after #2191 (#2193,
      #2195), confirming neither regressed the e2e suite." **That claim was
      false and has been removed.**

      `playwright.config.ts` sets `reuseExistingServer: !process.env.CI`, so a
      local run attaches to whatever already listens on 127.0.0.1:8484 instead
      of building. The process serving that port had been started at
      **00:31:50** — hours before #2193 (merged 05:11) and #2195. Every local
      e2e run after that point, including the 06:48 "verification" run, served
      a **stale frontend bundle** predating both fixes.

      What survives: **#2191 is still genuinely verified.** Its changes are spec
      and helper files, which Playwright loads from disk rather than from the
      server, so those ran as written. What does not survive is any claim about
      #2193 or #2195 — the served bundle did not contain them.

      **The trap is the lesson.** `reuseExistingServer` fails silently and looks
      exactly like success: a fully green suite that exercised week-old code.
      Anyone verifying a frontend change locally must confirm the server was
      built from their commit — check the listener's start time
      (`ps -o lstart -p $(lsof -ti :8484)`) or kill it first. Consider dropping
      the flag, or having the config refuse to reuse a server older than the
      working tree.

      **What this run does NOT verify** — recorded so the green result is not
      over-read:

      - There is **no e2e test for the In Progress / Finished sidebar filter**
        on `main`. #2193 is covered by unit tests only.

        A spec is drafted on branch `test/e2e-in-progress-filter` (commit
        `a167205e`, deliberately **not merged** — 1 of 5 tests green). The one
        that passes is the decisive one: *"clicking In Progress survives the URL
        settling with page=1"*, run against a **freshly built** app. That is the
        first real-browser evidence that #2193's harder half — the stuck
        `isInternalUpdate` guard that discarded the click — is genuinely fixed.
        The other four fail on `toHaveClass` against a filtered locator, which
        is a test-authoring problem rather than a product bug: MUI nests the
        label so the computed accessible name does not match, and the filtered
        locator does not resolve to the element carrying `Mui-selected`.
        Finishing those four would close the "not verified interactively"
        caveat permanently.
      - There is **no e2e test for the empty-state / warmup recovery** either.
        That acceptance test requires restarting the backend mid-session, which
        the suite does not do.

- [ ] **Move library filtering/search into the Go server as a real, declared
      query engine — and make unknown filters a hard error instead of a silent
      no-op.** Today the browser pulls pages of books and narrows them
      client-side. That does not scale to a 44,874-book / 284,735-file library:
      any filter that is not expressible as one of the handful of query params
      the Go handler happens to recognise either degrades into "fetch a lot and
      filter in JS" or silently does nothing at all. The server has the indexes,
      the memdb, and 48 cores; the browser has one thread and a network hop.

      **The hard constraint is browser memory.** The reporter's requirement is
      blunt and it is the thing to design against: *a single web page must not
      sit on ~10GB of RAM.* Client-side filtering over this library implies
      pulling the library — or a large fraction of it — into the tab, and at
      44,874 books that is not a tuning problem, it is the wrong architecture.
      The browser should never hold more than the page it is displaying plus a
      small window. Which query language wins (below) is genuinely open; this
      constraint is not.

      **Measured on prod 2026-08-08** against `GET /api/v1/audiobooks`
      (envelope is `{"data":{count,items,limit,offset}}`):

        limit=1                              count=44874   <- baseline
        limit=1&library_state=imported       count=18998   <- honoured
        limit=1&library_state=in_progress    count=0       <- honoured, no such value
        limit=1&status=in_progress           count=44874   <- IGNORED
        limit=1&progress=in_progress         count=44874   <- IGNORED
        limit=1&bogus_param_xyz=nonsense     count=44874   <- IGNORED

      The last three are the finding. **An unrecognised filter param returns the
      entire unfiltered library with HTTP 200.** There is no 400, no warning, no
      `applied_filters` echo — so a frontend that sends a param the backend does
      not implement is indistinguishable from a frontend that sends no filter,
      and the bug surfaces to the user as "this button does nothing" (see the
      companion In-Progress filter task). A typo'd param name is equally
      invisible. Note the second failure mode too: `library_state=in_progress`
      IS a recognised param, but `in_progress` is not one of its values, so it
      silently returns zero books rather than rejecting the value.

      **What to build:**

      - **A declared filter schema, server-side.** Enumerate the filterable
        fields, their types, and their legal operators/values in one place, so
        the handler can validate a request against it rather than
        `c.Query("...")` per field scattered through the handler.
      - **Reject what you cannot honour.** Unknown param, unknown field, or
        illegal value for a known field → `400` naming the offending param and
        listing what is valid. Fail closed. The current fail-open behaviour is
        why a broken filter can sit in the UI unnoticed.
      - **Echo what was applied.** Return `applied_filters` (and ideally
        `ignored`) in the response envelope so the client can render active
        filter chips from what the server actually did, not from what the client
        hoped it did.
      - **Composable predicates, not one param per question.** The user's ask
        was "maybe some GraphQL-like thing so we can do stuff dynamically." The
        requirement is dynamic AND/OR over typed fields with comparison
        operators, sorting, and pagination — evaluate a structured POST
        `/audiobooks/query` body (a small typed filter AST) against adopting
        GraphQL wholesale. A filter AST is far less machinery than a GraphQL
        server and keeps the existing REST surface; GraphQL earns its cost only
        if arbitrary client-chosen field selection is also wanted. Decide this
        explicitly and write down why.
      - **Server-side full-text/substring search** over title/author/narrator/
        series, so `search=` is not a client-side scan. Check what the existing
        `search=title:` syntax already supports before adding a second dialect.
      - **Concurrency.** Per CLAUDE.md, any predicate evaluated across the whole
        library must use a bounded worker pool (`errgroup` + `SetLimit` sized to
        `runtime.NumCPU()`), not a plain `for range books`.

      **Acceptance:**

      - Every filter the Library UI can express is computed by the Go server and
        returns a correct `count` for the whole library, not just the fetched
        page.
      - An unsupported filter or illegal value returns `400` rather than the
        full library.
      - No filtering pass over all books runs single-threaded.
      - **Measured:** with any filter or search applied, the browser tab's heap
        stays flat and bounded — take a DevTools heap snapshot with the Library
        page open on the full 44,874-book library and confirm the tab holds one
        page of results, not the library. This is the acceptance criterion the
        reporter actually cares about; the query-language choice is subordinate
        to it.

- [ ] **Fix the Library "In Progress" nav item — the selection highlight never
      moves, and the click is a genuine no-op.**
      🟡 **BOTH BUGS FIXED in #2193 (2026-08-08); two acceptance items remain —
      see "Still open" at the end of this entry.** Reported 2026-08-08, root-caused
      the same night. These are **two independent bugs** that happen to share a
      symptom. The control is not on the Library page: it is the Library sub-nav
      in the sidebar, `web/src/components/layout/Sidebar.tsx:53-62`:

          56: { text: 'All Books',   path: '/library?reset=1', matchPath: '/library' },
          57: { text: 'In Progress', path: '/library?search=read_status:in_progress' },
          58: { text: 'Finished',    path: '/library?search=read_status:finished' },

      **Bug 1 — the highlight can never move.** `Sidebar.tsx:163`:

          selected={location.pathname === (item.matchPath ?? item.path)}

      `location.pathname` never contains the query string. "In Progress" has no
      `matchPath`, so this compares `'/library'` against
      `'/library?search=read_status:in_progress'` — **always false**. "All Books"
      declares `matchPath: '/library'`, so it is **always true on any /library
      URL**. The indicator is therefore permanently pinned to "All Books".
      "Finished" is broken identically.

      Note: the obvious "compare pathname + search" fix is a trap. Once bug 2 is
      fixed the URL settles at `?search=read_status%3Ain_progress&page=1` — the
      write effect re-encodes the colon (`Library.tsx:605`) and unconditionally
      appends `page` (`614`) — so a raw string compare still fails. **Match on
      the decoded `search` param value, not the path string.**

      **Bug 2 — the click is a permanent no-op.** There is no dedicated
      selection state; the filter lives entirely in the URL `?search=` param,
      consumed by `pages/Library.tsx` (`useSearchParams` at 118; `searchQuery`
      seeded at 121/152; `parsedSearch` at 179; URL→state effect at 551-594;
      state→URL effect at 602-627; `isInternalUpdate` ref set at 624, consumed
      at 570-573).

      The ref **gets stuck at `true` after mount and stays true**. react-router
      7.18.2 rebuilds `setSearchParams` whenever `location.search` changes, and
      it is in the write effect's dep array (`Library.tsx:627`), so that effect
      re-fires on URL changes it did not cause. On a plain `/library` load: the
      write effect always appends `page` (614) and sets the flag; the next
      commit re-runs it, producing an identical `page=1` and re-arming the flag;
      because `location.search` is then unchanged, `useSearchParams` returns the
      same object and the sync effect never runs again to clear it.

      Clicking "In Progress" then hits the guard at `Library.tsx:570-572` and
      the incoming `search` is **discarded**, while the write effect rewrites
      the URL back to `page=1`. No `searchQuery`, no `parsedSearch`, no chip, no
      change to the request.

      **The asymmetry corroborates this:** "All Books" works and "In Progress"
      does not *from the same machinery*, because the `reset=1` branch is read
      at line 558 — **before** the guard at 570 — while `search` is read at 576,
      **after** it.

      **Cheap falsifiable checks — run these first; they also give users a
      workaround, and if any fails the diagnosis above is wrong:**

      - "Finished" is broken in exactly the same way.
      - A **hard refresh** of `/library?search=read_status:in_progress` **works**
        (mount-time seeding at 121/179 bypasses both effects).
      - **Dashboard → In Progress works** (Library mounts fresh);
        **/library → In Progress does not.**

      **The backend is fine — do not "fix" it.** Had `parsedSearch` ever been
      populated, `buildFieldFilters` (`Library.tsx:629-641`) would serialize to
      the `filters` param (`useLibraryQuery.ts:140` → `services/api.ts:964`),
      and the Go side splits per-user fields correctly
      (`internal/server/handlers/audiobooks/handler.go:435-448` →
      `internal/audiobooks/service_query.go:356-365`). `in_progress` is spelled
      consistently across `utils/searchParser.ts:59`, `Sidebar.tsx:57`, and
      `internal/audiobooks/service_types.go:124` — **the value-mismatch theory
      was investigated and disproved.**

      **Separate latent hazard, worth fixing while here.** Probing prod
      2026-08-08 showed `GET /api/v1/audiobooks` **fails open on unknown query
      params**: `bogus_param_xyz=nonsense` returned the entire 44,874-book
      library with HTTP 200, as did `status=in_progress` and
      `progress=in_progress`. Meanwhile `library_state=in_progress` is a
      recognised param with no such value and silently returns **zero** books.
      That did not cause this bug, but it is why a filter that silently does
      nothing can ship unnoticed — see the companion backend-filtering task.

      **Acceptance:** clicking the item moves the highlight, adds a filter chip,
      and changes the result count; the count reflects the whole library rather
      than the fetched page; and "Finished" works too. Also render the sub-items
      in collapsed-sidebar mode, where they currently are not rendered at all
      (`Sidebar.tsx:126-139`).

      ---

      **✅ Shipped in #2193 (2026-08-08).** Both root causes above are fixed.
      Bug 1: selection now goes through an exported `isSubItemSelected()` that
      compares the parsed, decoded `search` param instead of `location.pathname`
      — which also sidesteps the percent-encoding/`page=1` trap noted above.
      Bug 2: the stuck one-shot `isInternalUpdate` boolean is replaced by
      `lastWrittenSearch`, which compares the query string actually written;
      being idempotent, repeated identical writes are harmless and a genuinely
      different URL always gets through. "Finished" is fixed by the same change.
      The backend was confirmed not at fault and was not touched. Verified:
      432/432 frontend tests, `tsc --noEmit` clean, eslint 0 errors, plus a new
      `Sidebar.test.tsx` (11 cases) covering the encoded settled URL and a
      one-item-selected invariant.

      **🟡 Still open — do not close this entry yet:**

      1. **Collapsed-sidebar mode still does not render the sub-items at all**
         (`Sidebar.tsx:126-139`). Untouched by #2193, so In Progress/Finished
         remain unreachable when the sidebar is collapsed.
      2. **The result count still reflects the fetched page, not the whole
         library.** That is the companion backend-filtering task, not a sidebar
         concern — close it there rather than duplicating the work here.
      3. **Not verified interactively.** #2193's fix is reasoned from the code
         and covered by unit tests; nobody has driven the real app. The
         falsifiable predictions above double as the manual check: "Finished"
         should now work, and both should work whether arriving from Dashboard
         or from `/library`.

- [ ] **The Library must never show an empty "no items" state unless the
      library is genuinely empty (true first startup). Every other case shows a
      loading state and keeps retrying until books arrive.** Reported
      2026-08-08. Today a transient backend condition renders as "there are no
      books," which is the most alarming possible way to display a temporary
      failure to someone with a 44,874-book library.

      **Why this happens — measured 2026-08-08.** After `make deploy` restarted
      the service, `GET /api/v1/system/status` was **unreachable for roughly 40
      seconds** (curl exit with HTTP `000`, i.e. connection refused / no
      response) before it began returning `200`. The backend does a full memdb
      warmup over 44,874 books and 284,735 files on boot. So there is a
      guaranteed ~40s window on every single deploy during which the frontend's
      requests fail outright. Any UI that renders its empty state on
      `!loading && books.length === 0` will show "no books" during that window,
      because a failed request leaves the list empty without leaving it loading.

      **Root cause, located.** `web/src/components/library/LibraryBookGrid.tsx`
      line 183:

          {audiobooks.length === 0 && !loading && !searchQuery ? (

      That is the predicted bug shape exactly, and there is no error branch
      anywhere near it. The component's props (line 43) carry only
      `loading: boolean` — **there is no error/status prop at all**, so
      `LibraryBookGrid` is structurally incapable of telling "the request
      failed" apart from "the library is empty." The `manualImportError` /
      `bulkOrganizeError` state in `pages/Library.tsx` (lines 343, 372) covers
      import and organize actions, not the book-list fetch. So when the fetch
      fails during warmup, `loading` flips to false, `audiobooks` is empty,
      `searchQuery` is unset, and the page confidently announces an empty
      library.

      Fixing this therefore is not a one-line condition change: a fetch
      status/error has to be threaded from the data layer into this component
      first. Line 335 has the sibling branch for the searched-and-empty case and
      will want the same treatment.

      **The distinction the UI must make.** Three states are currently being
      collapsed into one:

        a) request in flight            -> spinner / skeleton
        b) request failed or server not ready -> "still loading…", keep retrying
        c) request succeeded, count == 0      -> the ONLY case that may say "no books"

      Only (c) is a real empty library, and it should additionally be
      distinguishable as first-run (nothing ever imported) versus "your filters
      matched nothing" — those want different copy and different affordances.

      **What to build:**

      - Gate the empty state on a **successful** response whose `count` is 0 —
        never on `books.length === 0` alone. An errored or not-yet-settled query
        must fall through to the loading branch.
      - **Retry with backoff, indefinitely, while the failure looks transient**
        (network error, 502/503, connection refused). Cap the delay (a few
        seconds) so recovery is prompt after warmup finishes, and surface a
        quiet "reconnecting…" note once the first retry fails rather than
        leaving a silent spinner forever. Do not retry forever on a 4xx — that
        is a real client bug and should surface.
      - Consider a **readiness signal from the Go side**: have the server return
        `503` with a `Retry-After` while memdb warmup is in progress, instead of
        refusing connections or returning an empty 200. An explicit "not ready
        yet" is far easier for the client to handle correctly than a dropped
        connection, and it makes the correct client behaviour obvious. Check
        whether a readiness/health endpoint already distinguishes "process up"
        from "warmup complete" — `systemctl is-active` reported the service
        healthy well before the API answered, so process-liveness is already
        known to be a misleading signal here.
      - Distinguish **first-run empty** from **filtered-to-empty** in copy.

      **Acceptance:** restart the backend with the Library page open. The page
      must show a loading/reconnecting state for the whole warmup window and
      then populate on its own with no user interaction — at no point may it say
      the library is empty.

      ---

      **✅ Shipped in #2195 (2026-08-08).** The core fix is in. `useLibraryQuery`
      no longer calls `setAudiobooks([])` on failure, so a failed refresh keeps
      the last known-good page instead of blanking the shelf. The empty-state
      decision moved out of the JSX into a pure `libraryContentState()` helper
      whose branch ORDER is the fix — `reconnecting` is evaluated before
      `empty`, so only a load that RESOLVED with zero results can claim the
      library is empty. Failed loads now retry with exponential backoff from
      500ms capped at 5s, indefinitely for transient failures (network,
      connection refused, 5xx) and never for a 4xx. Explicit cancel stops the
      loop; the timer is cleared on unmount. Verified: 442/442 frontend tests,
      `tsc --noEmit` clean, eslint 0 errors, plus `libraryContentState.test.ts`
      with an exhaustive sweep asserting `empty` is reachable only from a clean,
      settled, genuinely-zero result.

      **🟡 Still open — do not close this entry yet:**

      1. **The acceptance test above has NOT been run.** The fix is reasoned and
         unit-tested; nobody has restarted the backend with the Library page
         open and watched it recover. Until someone does, this is unverified
         against the actual failure it was written for.
      2. **No readiness signal from the Go side.** The server still refuses
         connections during memdb warmup rather than returning `503` +
         `Retry-After`. The client now copes either way, but an explicit "not
         ready yet" would be far more honest — and `systemctl is-active` is
         still a misleading liveness signal, reporting healthy ~40s before the
         API answers.
      3. **first-run empty vs filtered-to-empty copy is unchanged.** The
         existing state only branches on `importPaths.length === 0`.

- [ ] **Never accumulate more than 10 RCs on a version — cut the stable release
      instead.** Owner directive, 2026-08-08: *"we are never to get above 10
      RCs. Right now we have massive changes all bunched together. Doing it that
      way we have consistent releases."*

      **What triggered this.** On 2026-08-08 the repo was sitting on
      **`v0.217.9-rc.87`** — eighty-seven release candidates on a single
      version — while the last actual stable release was **`v0.217.4`, cut on
      2026-07-06**. A month of work, including several data-loss fixes and a
      library-wide reparse, had piled into one undifferentiated blob that nobody
      could review, bisect, or roll back to a known-good point. Three duplicate
      broken draft releases had also accumulated against the unused `v0.217.9`
      tag. (Cut as `v0.218.0` that night; drafts deleted.)

      **The rule.** When the RC counter for a version reaches **10**, the next
      step is a stable release, not `rc.11`. Every merge to `main` already mints
      an RC via `.github/workflows/prerelease.yml`, so ten RCs is roughly ten
      merged PRs — a reviewable unit.

      **Make it enforced, not remembered.** A rule that depends on someone
      noticing a counter is the same class of failure that let it reach 87:

      - **Fail or warn in CI at the threshold.** Have the prerelease workflow
        check the RC ordinal it just minted and, at `>= 10`, either fail loudly
        or open/refresh a "cut a release" issue. A passive dashboard will be
        ignored; the signal has to appear where the work is happening.
      - **Consider auto-promoting.** If ten RCs are green, cutting the stable
        release is mechanical — `release-prod.yml` already takes
        `release-type` and `previous-version`. Weigh auto-promotion against
        wanting a human gate; if a human gate is kept, the reminder must be
        unmissable.
      - **Clean up RCs on promotion.** `cleanup-rc-releases.yml` exists; verify
        it actually runs on stable promotion and prunes the superseded RC
        releases and tags, or 87 stale prereleases will keep cluttering the
        releases page.
      - **Watch for the duplicate-draft bug.** Three identical broken drafts for
        `v0.217.9` accumulated because the release path created a new draft
        rather than updating the existing one. Fixed for this repo by pinning
        `.github/ghcommon-ref.txt`, but confirm the draft path specifically.

      **Why it matters beyond tidiness.** With one stable release a month, "roll
      back to the last good version" means discarding a month of fixes. With a
      release every ~10 merges, a bad change is bounded by a handful of PRs, the
      release notes are short enough to actually read, and `git bisect` between
      two stable tags is a tractable search rather than 87 candidates.

      **Acceptance:** the RC ordinal never exceeds 10 without either a stable
      release being cut or CI complaining; and the releases page does not
      accumulate superseded RC entries.

- [ ] **~180 "bracketed series" are actually one shattered book each** — found by
  the `maintenance.series-denumber` dry run, 2026-08-06.

  The dry run flagged 198 series names carrying a bracketed number
  (`Dragon Born [04]`, `… Called Peace (12)`). Roughly 18 are genuine series
  positions. The rest are **one novel exploded into per-chunk series rows**:

  | Target base | Rows | Books | Reality |
  |---|---:|---:|---|
  | `Megan E. O'Keefe - Catalyst Gate` | 80 | 80 | one novel |
  | `Listening-to-ClassA-Threat-by-Dan-Sugralinov--Scribd` | 36 | 36 | scraped page titles |
  | `Listening-to-Arcane-Kingdom-Online-Dark-Magic-by-Jakob-Tanner--Scribd` | 27 | 27 | scraped page titles |
  | `The Light We Lost` | 25 | 25 | one novel |
  | `Arkady Martine - A Desolation Called Peace` | 12 | 12 | one novel |
  | `Dragon Born`, `Warbreaker`, `Guardian`, `Otherworld Academy`, … | ~18 | ~24 | **genuine** |

  🔴 **Do not resolve this by applying `applyMedium`.** That would manufacture an
  80-volume "Catalyst Gate" series out of a single book, and a 36-volume series
  out of a Scribd listing page. The denumber op deliberately holds them; the
  parser is behaving correctly, the *shape* is a lie.

  These belong to the **combine-into-one-book** track (The Successors class), not
  the series track: a bracketed `(47)` on a novel title is a disc/chunk marker
  that leaked into the series field. The `Listening-to-…--Scribd` rows are a
  distinct, narrower bug — a web scrape wrote page titles into series names, and
  those need their own cleanup rather than any kind of merge.

  Start from the report:
  `/var/lib/audiobook-organizer/series-denumber-2026-08-06.tsv`
  (`shape=bracketed`, group by `into_name`, anything with >3 rows is suspect).

- [ ] **Give the 466 low-confidence series positions somewhere to go** — deferred
  deliberately on 2026-08-06 (owner decision), revisit after owner items 1 and 2.

  `maintenance.series-denumber` now reports 466 series names carrying a bare
  number (`08. Battle for the Abyss` → position 8, `Station 64: The Doll Dungeon`
  → position 64) covering ~580 books. They are correct often enough to be worth
  surfacing and wrong often enough that they can never apply themselves —
  `86—EIGHTY-SIX` is a real series name in this library with the identical shape.

  Today they exist only in the `reportPath` TSV. Nothing consumes them.

  🔑 **Why no review-queue Kind was built yet:** a new Kind needs frontend
  mapping, and `review_apply_enabled` is OFF in production, so approving such a
  hold would mutate nothing. Wiring these in only makes sense once holds have
  real per-item actions — i.e. after owner item 1 (recommendations) and item 2
  (overrides). Doing it in that order avoids building a producer for a consumer
  that cannot act.

  Note for whoever picks this up: for `08. Battle for the Abyss` the parsed base
  is `Battle for the Abyss` — the BOOK's title. The real series (`Horus Heresy`)
  is not present in the string at all. So the low tier cannot be resolved by
  better parsing; it needs evidence from outside the name (sibling books sharing
  a folder/author with different leading numbers — spec D4, unbuilt), or a human.

  Design: [`docs/specs/2026-08-06-series-embedded-positions-design.md`].

- [ ] **react-router GHSA-qwww-vcr4-c8h2 — accepted, not reachable, do not
  re-litigate.** The v6 → v7.18.2 upgrade (2026-08-06) closed three advisories
  and opened this one. It is **expected**, it was a deliberate trade, and the
  analysis is recorded here so the next person to see the alert does not redo it.

  **The trade.** No single version closes all four. The three originals
  (open redirect via backslash in `<Link>`/`useNavigate`, open-redirect-to-XSS,
  arbitrary constructor injection via `deserializeErrors()`) are first patched at
  **7.18.0**. This one — RSC-mode CSRF bypass, vulnerable range 7.12.0–8.2.0 — is
  only fixed at **8.3.0**.

  **Why we did not go to v8.** It requires `react >= 19.2.7` (repo is on 18.2.0),
  and `react-router-dom` does not exist at 8.x at all (E404), so it would also
  mean rewriting all 49 import sites. That is a React-major migration, not a
  dependency bump.

  **Why the residual is not reachable.** It is an RSC-mode vulnerability and this
  is a plain SPA. Verified zero hits for `@react-router/*`, `react-server`,
  `unstable_RSC`, and `createStaticHandler`. The three closed advisories, by
  contrast, were in `<Link>` and `useNavigate` — code paths used constantly.
  Closing reachable risk while accepting unreachable risk is the right direction
  even though the alert count goes the wrong way.

  Revisit when the app moves to React 19 for its own reasons. Do not take the
  React major *for* this advisory.

- [ ] **Two frontend navigation sinks are unvalidated and safe only by
  accident.** Found 2026-08-06 while auditing the react-router open-redirect
  advisories. Neither is exploitable today. Both rest on an invariant that a
  future change breaks **silently** — nothing fails, nothing warns, the sink just
  becomes live.

  1. `web/src/pages/Login.tsx:78-81` — `location.state?.from` is passed straight
     to `navigate()` with no validation. Safe **only because nothing writes
     `state.from`** (zero writers in the codebase). Wire a `?returnTo=` param
     into it and it is immediately exploitable.
  2. `web/src/pages/BookDetail.tsx:938,968` — `sessionStorage`'s
     `library_return_url` goes to `navigate()` unvalidated. Safe **only because
     the writer runs on the exact routes** `/library` and `/fingerprints`.
     Changing that to `/library/*` makes it exploitable.

  The remedy is to validate at the sink rather than rely on the writer's reach:
  the Go side already does exactly this, and does it well —
  `sanitizeReturn` (`internal/server/handlers/oauth_login.go:260-271`) implements
  the backslash guard the advisory describes, and `abs/openid.go:246-257`
  validates `redirect_uri` before error redirects too. Mirror that on the client.

  🔴 **Do not "fix" [[TODO-SSO-EDGE]] / the OAuth-callback entry at `TODO.md`
  around line 1040 by loosening `sanitizeReturn`.** That entry is a *functional*
  gap — the guard correctly rejecting a custom-scheme return — not a
  vulnerability. Loosening it would convert a working defence into one of these.

- [x] **FIXED (#2178, 2026-08-07; verified checked-off 2026-08-14 — later entries in this
  file record the repair waves and the blocking CI gate #2258).** Original: **The Playwright
  e2e suite is broken on `main` and gates nothing.** Every
  test dies at fixture collection with `unknown parameter "_page"` — 49 errors.
  Confirmed pre-existing on 2026-08-06: the identical failure reproduces on the
  pre-react-router-v7 tree with unchanged specs, and the v7 PR touched zero files
  under `web/tests/`.

  Why this matters beyond the red: the react-router v6 → v7 upgrade merged with
  **no runtime routing signal at all**. `tsc` was clean and 402 frontend unit
  tests passed, but nothing exercised actual navigation. A routing major landing
  without e2e coverage is precisely the case the suite exists for.

  Fix the fixture signature, then re-run against the v7 tree to retroactively
  confirm the upgrade — and treat `make test-e2e` as a required gate for any
  future routing or auth-flow change.

- [x] **`UpsertBookToMemDB` holds go-memdb's global writer mutex across Pebble
  I/O.** Found 2026-08-06 while profiling `dedupe-book-file-rows` (fixed in
  #2161). This is a **system-wide ceiling on every `UpdateBook`**, not something
  specific to that op, and it is the natural next performance win. — closed
  2026-08-21: `git log --oneline -- internal/database/memdb_sync.go | grep 8eb8c0c1`
  → `8eb8c0c1 perf(database): hoist Pebble reads out of the memdb writer mutex (C816)`;
  `internal/database/memdb_sync.go:139-141` now fetches `GetBookAuthors`/`GetBookNarrators`/`loadBookFilesForBookID` before `p.memSync(...)` takes the writer lock.

  go-memdb has a single global writer mutex (`memdb.go:34-35`, `:73-76` — one
  writer at a time, `Txn(true)` takes `db.writer.Lock()`). Inside that lock,
  `UpsertBookToMemDB` performs three Pebble reads: `GetBookAuthors`
  (`memdb_sync.go:72`), `GetBookNarrators` (`:85`), and `loadBookFilesForBookID`
  (`:98` — a full prefix scan that unmarshals every remaining fingerprint-bearing
  row). Every other writer in the process waits on that I/O.

  Fix: fetch first, then take `Txn(true)`. Consequence worth stating — this is
  also why adding worker pools to book-level maintenance ops buys far less than
  `NumCPU×`: the workers serialize here regardless.

- [ ] **`DeleteBookFilesForBook` leaves stale memdb rows behind.** It never calls
  `DeleteBookFileFromMemDB` or `MarkQuickQueryDirty`, so Pebble and memdb diverge
  after it runs. Noticed 2026-08-06 while modelling `DeleteBookFilesByIDs` on it
  (#2161) — the new method does both; its model does not.

  Latent, and it pairs badly with the known "corrected aggregates are invisible
  until memdb refreshes" problem: a divergence here looks exactly like that
  staleness, so the two will be confused during diagnosis.

- [ ] **The 3 dangerous multidisc holds are DUPLICATES, not series — feed them to
  the duplicate-detection track.** Measured 2026-08-06 from a full pre-apply
  snapshot of all 132 pending `regroup.multidisc` holds (4,146 member books,
  zero unreadable).

  `TODO.md` predicted ~9 holds with book-length members, presumed to be series
  the guard would have caught. There are **3**, and all three are two-member
  holds whose members have near-identical runtimes:

  | hold | members |
  |---|---|
  | `01KXF8BNKENR530AKMMKJYD5E1` | `Brother Wulf` 6.30 h + `Brother Wulf - Joseph Delaney` 6.30 h |
  | `01KXF8BNKACGA6ZAEBPCQK09FX` | `Sevenfold Sword` 20.56 h + `Sevenfold Sword` 21.47 h |
  | `01KXF8BNHY7AE56CPZWY9VW9VF` | `The Warring Son` 11.77 h + `The Warring Son` 11.77 h |

  Same title, same runtime, two rows. That is the never-delete / re-associate
  shape, not a series of distinct novels.

  **The recommender emits `separate` for them, and that is the correct default.**
  Separate destroys nothing and leaves them for a signal that can actually
  establish identity. 🔴 Do NOT tune the recommender toward `duplicate-of` on
  runtime similarity — equal runtimes are not identity evidence. Two different
  books can share a runtime, and acting on that would merge distinct works
  through a path that hard-deletes the absorbed row.

  These 3 are a clean, small, real test set for
  [[never-delete-re-associate]] — use them rather than inventing fixtures.

- [ ] **Frontend framework versions — how far behind we actually are, and the
  order to fix it in.** Surveyed 2026-08-06 at owner request ("are we on
  TypeScript 7 and the latest React?"). Answer: **no to both.**

  | Package | Installed | Latest | Behind |
  |---|---|---|---|
  | `typescript` | 5.9.3 | **7.0.2** | 2 majors |
  | `react` / `react-dom` | 18.3.1 | **19.2.8** | 1 major |
  | `@mui/material` | 5.18.0 | **9.3.1** | **4 majors** |
  | `jsdom` | 23.2.0 | **30.0.1** | **7 majors** |
  | `vite` | 7.3.6 | 8.2.1 | 1 major |
  | `eslint` | 9.39.5 | 10.8.0 | 1 major |
  | `zustand` | 4.5.7 | 5.0.14 | 1 major |
  | `react-router` | 7.18.2 | 8.x | 1 major (gated on React 19) |
  | `vitest` | 4.1.10 | 4.1.10 | current |

  **React 19 is worth more than it looks, because it is also a security fix.**
  [[react-router-v8-residual-advisory]] (GHSA-qwww-vcr4-c8h2) is only patched in
  react-router **v8**, which requires `react >= 19.2.7` and does not publish
  `react-router-dom` at all. So "upgrade React" and "close that open high-severity
  alert" are one piece of work, not two. That changes its cost/benefit — do not
  price the React major as pure maintenance.

  **TypeScript 7 is not a version bump.** It is the native Go compiler rewrite —
  roughly 10× faster type-checking, but a different implementation with its own
  compatibility surface. Budget it as a migration.

  **MUI 5 → 9 is the largest single lift.** Four majors, and MUI majors move the
  styling engine and component APIs. `@mui/material` is imported across most of
  the UI, so this is the one that is genuinely days rather than hours.

  Suggested order, cheapest-value-first:
  1. **React 19 + react-router 8** — closes a live advisory, moderate scope.
  2. **jsdom + eslint + zustand + vite** — cheap, can ride along with (1).
  3. **TypeScript 7** — real migration, big payoff in CI time.
  4. **MUI 9** — largest, purely maintenance, do last.

  🔴 **Do not attempt any of this until the e2e suite is fixed.** See
  [[e2e-suite-broken-on-main]] — it currently dies at fixture collection and
  gates nothing, which is why the react-router v6 → v7 upgrade merged with zero
  runtime navigation coverage. A React major without e2e is exactly the change
  that suite exists to catch.

- [ ] **Per-file intro transcription as the primary book-identity signal** — owner
  design 2026-08-06. Storage and the first-file sort fix are **DONE** (PRs #2168);
  the parser, the tiered backfill, and the wiring are open.

  **The idea.** An audiobook opens with a spoken *"&lt;Title&gt; by &lt;Author&gt;, read by
  &lt;Narrator&gt;"* announcement. That announcement marks a book **start**. A file
  without one is a continuation. That is direct identity evidence, where the
  current classifier only has runtime — a proxy.

  **Why it needed per-file storage.** Transcripts lived on `Book`, so only ONE
  file's opening was ever captured and "12 files that are one book" was
  indistinguishable from "12 files that are 12 books". Measured on prod, one
  folder's files read:

  ```
  file 1: "This is a reading of Overlord, Book 7. This part includes the prologue and Chapter 1."
  file 2: "This is a reading of Overlord Volume 7. This part includes Chapter 2."
  file 3: "Hello... This is Overlord Volume 7, Chapter 3."
  ```

  Per-file that sequence is proof of continuation; per-book it is invisible. It
  also explains the measured **45.8%** credit-parse rate across 1,476 review-queue
  members — the op sampled one arbitrary file per book.

  ### Remaining work

  - [x] **Three-outcome parser.** ✅ DONE 2026-08-07 — `ClassifyIntro`
        (`internal/transcribe/classify.go`) returns credits / chapter / prose /
        unknown with a typed reason, confidence, and chapter number. **Position
        is a weight, never a veto**: credits at ordinal >0 IS the shattered-book
        signal, so vetoing it would hide the very finding this was built to
        surface. Both confirmed prod false positives are covered — the *Girls
        with Rebel Souls* case is reclassified as **misfiled** rather than
        mis-parsed (`IsLikelyMisfiled`: the announcement was read correctly, the
        FILE is in the wrong folder), and prose-containing-"by" now fails
        plausibility gates (case-sensitive prose markers, so "Meet **Me** in
        Paradise" survives while "...and **he** wasn't amused" does not).
        The corpus surfaced a larger defect than either: **24.8% of stored
        titles carried a leaked credit verb** ("Awakened Essence 1 Written")
        because the split landed *inside* `written by` — the library's most
        common credit variant (24.1%), absent from the pattern list entirely.
        Backed by a 188-transcript production corpus
        (`internal/transcribe/testdata/intro_corpus.jsonl`), invariant tests, a
        distribution canary, and a fuzz target (165k execs clean).
        🔴 `reparseStoredIntros` now **only upgrades, never clears**: 1.4% of
        987 sampled books (~644 library-wide) hold a parse their *current*
        transcript cannot regenerate, because `applyOutcome` overwrites the
        transcript unconditionally but the parsed fields only on success.
  - [ ] **Tiered backfill.** Naive "every file" is ~284,000 files ≈ 12–14 days of
        GPU. Tiers: **0** single-file books migrate by copy (zero GPU, ~32,600
        books); **1** assembled multi-file books probe the first 3 files only;
        **1b** escalate to the full set if all 3 carry credits — which is what
        makes the cheap tier *safe*, since it cannot silently be wrong; **2**
        bookless/shattered/queue members get every file; **3** a lazy, indefinite
        full sweep so every file eventually has a transcript.
  - [ ] **Wire into the regroup classifier**, outranking runtime where both exist.
        Validate by diffing against the 356 holds already measured under the
        runtime rule.
  - [ ] **Wire into First Aid** as a tier-2 signal beside the duration probe, and
        let the verdict pick the fixer.

  ### Measured facts worth keeping

  - 72.7% of books are single-file; 11.3% have 21+ files and hold most of the
    317,054 rows. The signal is precisely targeted at the fraction that is
    actually ambiguous.
  - **195 of 204** "untranscribed" review-queue members have ZERO `book_file`
    rows — unlinked, not un-transcribed. **Relink before transcribing** or they
    need a second pass. [[first-aid-library-validate-repair]]'s probe already
    found 434 of 1,019 directory-shaped books confidently linkable.
  - The WAV clip cache is keyed by **file path**, so clips already extracted
    survive the per-file move and ffmpeg is skipped on re-run.
  - Book-level transcription is already **saturated** — a full `only_missing` run
    over 221 pages transcribed 0 books. There is no warm-up value left; the
    per-file pass is the entire remaining work.

  🔴 **Absent transcript means "cannot verify", never "continuation".** This
  codebase has now been bitten by absent-value-read-as-evidence four separate
  ways: `DurationSec == 0` read as "short" (disabled the series guard across 97.5%
  of the queue), a 404 body read as "zero files", `memPtr == nil` read as "nothing
  to do" (silently dropped writes for the process lifetime), and an empty
  `intro_transcription` read as "needs transcribing" when it meant "has no file".

- [ ] **Stand up a second Whisper worker on the spare CPU node.** Owner request
  2026-08-06. Host prepared, worker not built. (Host address and credentials are
  fleet-internal — see the private infra notes, not this repo.)

  **Why it is cheap to try:** the transcription backend is already a pluggable
  HTTP service — `WHISPER_REMOTE_URL` points at a faster-whisper instance on the
  GPU host. Adding a second worker is a deployment question, not a code change.
  `internal/transcribe/batch.go:51` reads a single URL today, so the only code work
  is fanning out across several endpoints.

  **The node as measured 2026-08-06:** Ubuntu 26.04, 48 cores, 251 GB RAM,
  **no GPU**. Its Tdarr node registers CPU-only with `transcodegpu: 0`, the Tdarr
  queue is **empty** (`table1Count: 0`), and both node processes sit at 0.0% CPU —
  so nothing needs stopping to free it. Python 3.14.3 with pip 25.1.1 and uv 0.12.2
  (both installed 2026-08-06).

  🔴 **CPU-only is the whole caveat.** faster-whisper with int8 quantisation on 48
  cores is real, but it is **not** a second GPU. **Benchmark against a real clip
  batch before promising throughput** — do not assume it halves the backfill.

  **Prefer an HTTP endpoint over the in-process `uv` path.** `whisper.go` also has
  `runPythonWhisper` (`uv run --with openai-whisper whisper`), and uv is now
  installed so that route works — but `batch.go:54-57` warns it loads the full
  model into RAM and *"reliably OOMs the server"* at batch sizes of 100–200. That
  warning was written about the **web-serving host**; the spare node has 251 GB and
  serves nothing, so the reasoning does not transfer directly. Even so, a second
  HTTP endpoint matches the existing interface, avoids the OOM class entirely, and
  needs no special batch sizing.

  **Point it at tier 3 first.** The lazy full sweep in
  [[per-file-intro-identity-signal]] has no deadline, which makes it the natural
  consumer for a slower worker — "slower than GPU" costs nothing there, while the
  decision-critical tiers keep the GPU.

- [ ] **Investigate: 79% of books with a stored transcript are marked
  `whisper_error`.** Found incidentally while sampling a corpus for the
  three-outcome parser (2026-08-07), not chased — it is out of scope for the
  parser work but nobody will stumble on it otherwise.

  **The measurement.** A random offset-based sample of **987 distinct books that
  all have non-empty `intro_transcription`** breaks down by `transcribe_status`
  as:

  | status | count | share |
  |---|---|---|
  | `whisper_error` | 783 | **79.3%** |
  | `ok` | 177 | 17.9% |
  | `unparsed` | 26 | 2.6% |
  | `empty` | 1 | 0.1% |

  Every one of those 783 rows **has transcript text stored** while its status
  says the transcription failed. Status and content have drifted apart across
  what looks like most of the library.

  **Why it probably happens.** `applyOutcome`
  (`internal/plugins/maintenance/intro_transcribe.go`) writes
  `TranscribeStatus` on every outcome, but only writes `IntroTranscription` when
  the outcome carries a transcript. So a book transcribed successfully once and
  then re-attempted later — after the file moved, the GPU host went away, or the
  batch failed — keeps its old text and acquires a failure status. That is the
  same *shape* as the parse-vs-transcript divergence the parser PR guards
  against, one field over.

  **Why it matters.** Anything filtering on `transcribe_status == "ok"` is
  currently ignoring ~4 out of 5 books that actually have usable transcript text.
  Worth checking whether the tiered backfill's "needs work" query is one of them
  before it is sized — it would massively over-count the work remaining.

  **Do not assume it is a live failure.** The status could be a stale record of a
  historical outage rather than an ongoing one. Check
  `transcribe_attempted_at` vs `intro_transcribed_at` on the affected rows first:
  if attempted is consistently much later than transcribed, this is drift from
  old re-runs, not a currently-failing pipeline. 🔴 The distinction changes the
  fix completely, so measure before concluding.

  Related: [[per-file-intro-identity-signal]].

- [x] 🔴 **Data race: `UpsertBookToMemDB` retains the CALLER's `*Book` and
  dereferences it later on the warmup goroutine.** Caught by the race detector
  on CI during PR #2170 (a parser PR that touches no database file).
  ✅ FIXED 2026-08-07 (fix/memdb-warmup-caller-pointer-race): snapshot copied at
  enqueue time in `UpsertBookToMemDB` and every same-shape sibling upsert
  (BookFile/Author/Series/Narrator/ImportPath/AuthorAlias/BlockedHash + slice
  copies in ReplaceBookAuthors/NarratorsInMemDB). Regression test
  `TestUpsertBookToMemDB_SnapshotsCallerBookAtEnqueue` forces the interleaving
  deterministically under `-race`; verified it fires the race on the unfixed
  code and is green on the fix.

  **The race, verbatim from CI:**

  ```
  WARNING: DATA RACE
  Read at 0x00c000a96388 by goroutine 13725:
    database.stripBookForMemdb()        memdb_strip.go:33      // cp := *src
    database.UpsertBookToMemDB.func1()  memdb_sync.go:123
    database.applyMemSync()             memdb_sync.go:92
    database.publishWarmMemStore()      memdb_pending.go:211
    database.NewPebbleStore.func1()     pebble_store.go:320    // async warmup

  Previous write at 0x00c000a96388 by goroutine 13700:
    database.(*PebbleStore).UpdateBook() pebble_store.go:1827  // book.ID = id
    database.TestBook_TranscribeFields_RoundTrip()
                                        transcribe_stats_test.go:99
  ```

  **Mechanism.** `UpsertBookToMemDB` (`memdb_sync.go:114`) captures the caller's
  `book` pointer in a **closure** and hands it to `p.memSync`. While the store is
  still warming, that closure is not run inline — it is queued as a pending op
  and applied later by `publishWarmMemStore` → `applyMemSync`. So
  `stripBookForMemdb(book)`'s `cp := *src` reads the caller's **live** struct at
  an arbitrary later time. `CreateBook` (`pebble_store.go:1812`) and
  `UpdateBook` (`:2060`) both pass the caller's pointer in, and `UpdateBook`
  itself writes to it (`book.ID = id`, `:1827`).

  **Why it matters beyond the test.** This is not a test-only bug. Any caller
  doing the ordinary

  ```go
  b := &Book{...}
  store.CreateBook(b)
  b.SomeField = x        // caller mutates its own struct
  store.UpdateBook(b.ID, b)
  ```

  races with warmup whenever the store is still warming — which is exactly
  startup, when backfills and migrations run. A torn read here writes a
  half-updated Book projection into memdb. Same family as the memdb warmup
  write-loss fixed in #2166 and [[feedback_memdb_roundtrip_footgun]].

  **The fix (one line, at the enqueue boundary):** snapshot the struct when the
  op is *queued*, not when it is *applied*.

  ```go
  func (p *PebbleStore) UpsertBookToMemDB(ctx context.Context, book *Book) {
      if book == nil { return }
      snapshot := *book // copy NOW — the closure may run much later, on another goroutine
      p.memSync("UpsertBook", func(txn memTxn) error {
          if err := txn.Insert(memTableBooks, stripBookForMemdb(&snapshot)); err != nil {
  ```

  Check the sibling upserts (`UpsertBookFileToMemDB`, author/series equivalents)
  for the same shape before calling it done — the closure-captures-caller-pointer
  pattern is likely repeated.

  **Reproduction is timing-dependent.** The full `internal/database` package
  under `-race` passed locally (0 races, 305s) and `TestBook_TranscribeFields_
  RoundTrip` passed 15/15 in isolation; it fired on CI under coverage
  instrumentation. 🔴 Do NOT treat a green local run as evidence the race is
  gone — the regression test must force the interleaving (e.g. mutate the caller
  struct immediately after `CreateBook` while warmup is still pending) rather
  than hoping to catch it.

- [ ] **Version-group acoustic audit op** — verify that books marked as VERSIONS
  of each other are acoustically close enough to actually be the same work, and
  auto-fix ones that are not. Requested by owner 2026-08-05; not scheduled.

  Structurally different from the rest of the First Aid roster: every other op
  *finds* problems, this one *audits assertions* — including First Aid's own
  writes. Tier 3 creates version groups from duration matching; this re-checks
  them with a signal that took no part in that decision, so a wrong grouping
  becomes findable instead of permanent. Also covers groups created by any other
  path (`ApplyVersionGroup`, manual, historical imports).

  Signals: (1) AcoustID fingerprint similarity across members —
  `BookFile.AcoustIDFingerprint` plus `AcoustIDSeg0..6`; (2) Whisper
  transcription content (owner suggestion) — an *independent* signal, not a
  refinement of the acoustic one, which is what makes agreement meaningful.
  ~96.5% transcribed but ~40% low-quality/unparsed, so filter before trusting.

  🔴 **Absent evidence must mean "cannot verify", never "refuted".** ~65% of
  books were unfingerprinted as of 2026-07-02. Reading a missing fingerprint as
  "not a match" would ungroup correct version groups wholesale — the same failure
  as `DurationSec == 0` silently disabling the regroup series-guard across 97.5%
  of the review queue. Emit verified / refuted / insufficient-evidence.

  Auto-fix is safe here in a way deletion is not: the remedy is to UNGROUP (clear
  `VersionGroupID`, restore `IsPrimaryVersion`), destroying no rows and no files,
  and itself reversible. Still gate behind a confidence threshold and prefer a
  review hold when the two signals disagree.

  Home: tier 2 of the First Aid funnel (expensive, runs only over version-grouped
  books), feeding a tier-3 ungroup fixer. See
  `.worktrees/link-integrity/PLAN.md`.

- [ ] **Verify the server actually returns chapters to clients** — confirm the
  ABS-compatible surface serves chapter data wherever a client expects it, and
  that it is populated rather than an empty array. Owner request 2026-08-05.
  *(2026-08-14: tracked as B06 in the task breakdown; `mapper.go` serves stored
  chapters else synthesizes — a book with no stored chapters is indistinguishable
  from one having none until the backfill above runs.)*

  Chapter extraction and persistence shipped in the ABS sync work (Phase 1,
  chapter-extraction + scanner chapter hook), so the plumbing exists — what is
  unverified is the end-to-end path: extracted → persisted → serialized into the
  item payload → rendered by AudioBooth / Absorb.

  Check specifically:
  - the item detail response includes a populated `chapters` array (start/end/
    title), not `[]`, for books that genuinely have chapters
  - single-file M4Bs with embedded chapter atoms
  - multi-file books, where "chapters" and "tracks" are different concepts and
    the client may expect one, the other, or both
  - what a client sees for a book with NO chapter data — a graceful absence, not
    a malformed payload

  ⚠️ An empty array and a missing field are different failures to a client, and
  the ABS conformance harness (`internal/syncapi/conformance`) checks field
  presence and type rather than just values — use it rather than eyeballing JSON.

  Feeds [[chapters-backfill-from-duplicates]]: knowing which books lack chapters
  is the input to deciding which ones to repair.

- [ ] **Backfill chapters into files that lack them, using a duplicate as the
  source of timings** — owner request 2026-08-05. Turn a chapterless M4B into a
  properly chaptered one by borrowing structure from another copy of the same
  book that already encodes it.

  Sources of chapter timings, in preference order:
  1. **Audible/provider chapter data** — check whether the metadata providers we
     already query expose chapter titles WITH start offsets. If they do this is
     by far the cleanest path and needs no duplicate at all.
  2. **A per-chapter duplicate.** A chapterless `Book.m4b` alongside a duplicate
     stored as N mp3s, one per chapter: each file's duration gives a chapter
     length, and the cumulative sum gives the offsets. Filenames often give the
     titles.
  3. **A playlist with timings** (see [[playlists-full-support]]) — cue sheets
     and some playlist formats carry explicit offsets.

  🔴 **GATE ON NEAR-EXACT ACOUSTIC MATCH.** Owner was explicit. Chapter offsets
  borrowed from a *different edition* — different narrator, abridged vs
  unabridged, a remaster with different silence padding — are worse than no
  chapters at all: they read as correct and silently mis-seek. Require an
  AcoustID fingerprint match well above the ordinary dedup threshold, and reject
  on ANY duration mismatch beyond a small tolerance. Absent fingerprint must mean
  "cannot apply", never "assume it matches" — same rule as
  [[version-group-acoustic-audit]].

  Also verify the summed chapter durations reconcile to the target file's total
  runtime before writing; a shortfall means the duplicate is incomplete (the
  Successors debris covered 12 of 13 tracks, which would have silently truncated).

  Write path: chapters go into the M4B container. Treat it as a tag write with
  the usual safety — this repo's dominant incident class is write-back wipes, and
  `books/itunes/**` remains hands-off regardless.

  Depends on [[chapters-served-to-clients]] to know which books lack chapters.

- [ ] **Playlists — implement the whole surface** — owner request 2026-08-05:
  "basically implement everything to do with playlists, dynamic playlists,
  static, etc."

  Scope:
  - **Import** existing playlist files found during scan — `.m3u` / `.m3u8`,
    `.pls`, `.cue`, `.xspf`. Resolve their entries to `book_file` rows rather
    than storing raw paths, so a later reorganise does not break them.
  - **Static playlists** — user-curated, explicit ordered membership.
  - **Dynamic playlists** — a stored query (by author, series, narrator, genre,
    unfinished, recently added, rating…) evaluated at read time.
  - **CRUD + reorder** via API, and expose over the ABS-compatible surface so
    iOS clients see them. Check what ABS calls these and match its shape — the
    conformance harness (`internal/syncapi/conformance`) is the tool for that.
  - **Export** back to `.m3u`.

  Two reasons this is worth more than it looks:
  1. **Cue sheets and some playlists carry explicit timings**, which makes them a
     third source of chapter offsets for [[chapters-backfill-from-duplicates]].
  2. An imported playlist is **evidence about grouping** — a playlist listing 13
     files in order is a human-authored assertion that those files belong
     together, which is exactly the signal the regroup classifier lacks and has
     to infer from filenames.

  ⚠️ Playlist entries pointing at files with no `book_file` row will silently
  drop — 38.2% of books were in that state on 2026-08-05, so sequence this after
  relink or import will look lossy for reasons that have nothing to do with
  playlists.

- [ ] **Reading status and review/rating must sync from the app back to the
  server** — owner request 2026-08-05: set it in the app, it persists server-side.
  Mirror how Audiobookshelf does it rather than inventing a shape.
  *(2026-08-14: the READ-STATUS half exists — `IsFinished`/merge semantics live in
  `handlers/abs/progress.go` and the progress-write endpoints are registered
  (verified against `router.Routes()`). Remaining scope: the review/rating half.)*

  Two distinct things:
  - **Reading status** — not-started / in-progress / finished, plus the
    finished-at timestamp. ABS models this as `isFinished` + `finishedAt` on the
    media-progress record, and clients set it both explicitly ("mark finished")
    and implicitly (progress crossing a completion threshold).
  - **Review status** — the user's own rating and/or written review. ABS core
    does NOT have a first-class review object, so check what the iOS clients
    actually send before designing; this may need to be our own field exposed in
    a way clients tolerate.

  Prior art in-repo: Phase 6 ABS progress writes already landed (6 endpoints,
  `hideFromContinueListening` PATCH persistence, bookmarks — PR #2102), and
  `remove-from-continue-listening` was fixed in #2116. Reading status likely
  belongs alongside that media-progress work rather than as a new subsystem —
  look there first.

  Verify against real clients, not just the spec: AudioBooth and Absorb differ in
  which endpoints they call and when. The conformance harness checks field
  presence and type, which is what catches a client silently ignoring a field we
  thought we were sending.

  ⚠️ Round-trip matters more than write-once here. A finished flag that persists
  but never comes back on the next sync reads to the user as data loss, and it is
  the kind of bug that only shows up after reinstalling the app.

- [ ] **Use Deluge as a metadata and identity source** — owner idea 2026-08-05:
  "connect to deluge, see all the audiobooks it has, the titles it has, any other
  information and use that as well as other things to really figure out and match
  a book."

  Deluge's RPC exposes, per torrent: the torrent NAME, the save path, total size,
  the full file list, and dates. That name is often far richer than anything in
  the file's own tags — release names routinely carry author, series, volume
  number, narrator, edition (Unabridged), year, and format, in a structured-ish
  convention.

  Why this is a genuinely different signal from everything we have: every current
  identity source is downstream of the file itself (embedded tags, filename,
  folder, audio fingerprint). The torrent name is an **external, human-authored
  assertion made at acquisition time**, before any of our import processing could
  mangle it. For books whose tags were destroyed by the iTunes import, it may be
  the only surviving record of what the thing actually is.

  Work:
  - Deluge RPC client (read-only), credentials handled like other secrets — env,
    never the config blob.
  - Match torrents to library books by save path first (exact and prefix), then
    by file size, then by fuzzy title.
  - Parse release names into candidate metadata, and treat the result as a
    *scored candidate* feeding the existing matcher — never an authoritative
    overwrite. Scene naming is inconsistent and a confident parse of a wrong name
    would be worse than no parse.

  Pairs with [[deluge-file-parts-grouping-check]], which uses the same connection
  for a different purpose.

- [ ] **Use Deluge's per-torrent file list as ground truth for GROUPING** — owner
  idea 2026-08-05: "Deluge shows you all the file parts, we could easily pull
  that for all torrents and then match them to their files and if some groups are
  wildly wrong we know something is fucked up."

  This is the more valuable half of the Deluge idea, and it is a different kind
  of signal from [[deluge-metadata-source]].

  **A torrent's file list is an externally-authored statement that these N files
  belong together.** Everything the regroup classifier does is an attempt to
  RE-DERIVE exactly that fact from filenames and durations, after the fact, with
  known failure modes — it nearly merged 41 of 43 candidate groups that were
  really separate novels. Where a torrent covers a book, we do not have to infer
  the grouping; we can read it.

  Uses, in increasing ambition:
  1. **Audit** — compare our grouping against torrent membership. A torrent whose
     files we split across many books, or several torrents we merged into one
     book, flags a grouping error. This is a cheap, high-signal correctness check
     over a population we currently have no independent check for.
  2. **Evidence** — feed torrent membership into the regroup classifier as a
     strong positive grouping signal, outranking filename heuristics.
  3. **Repair** — propose regroups directly from torrent membership (review-gated
     like every other regroup proposal; never auto-applied).

  Caveats worth stating up front:
  - Coverage is partial — only books acquired this way, still seeded, still known
    to Deluge. Absent coverage must mean "no opinion", never "wrong".
  - A torrent may contain SEVERAL books (a series pack, an author collection), so
    torrent membership is an upper bound on one book, not proof of one book. Same
    over-merge trap as the folder heuristic — pair it with the duration guard.
  - Files may have been moved or renamed since; match on size and content, not
    only on path.

  Blocked on the same read-only Deluge RPC client as [[deluge-metadata-source]].

- [x] **Give review holds a real recommendation, and let the human override it**
  — owner items 1 and 2 (2026-08-05, shipped 2026-08-06 in PR #2163). Two halves
  of one change, done together as planned.

  **Outcome.** 286 of 356 pending holds now carry a decisive recommendation with
  the numbers behind it. The prerequisite this entry warned about turned out to
  be already met: the 2026-08-05 relink populated `book_file` rows, and a probe
  of all 1,831 member books found 1,593 with a real summed duration. The queue's
  item count had barely moved (367 → 356), which read like nothing had changed —
  but the *evidence* had arrived and the classifier simply was not using it.

  🔴 **A data-loss path was found and closed on the way.** The chosen action was
  originally stored nowhere: `SetReviewItemStatus` wrote status only, and
  `ReplayApprovedItems` re-derived the action from the payload's
  `recommendedAction`. A `combine` hold overridden to `separate` would have been
  recorded as plain `approved` and later **replayed as `combine`**, hard-deleting
  rows for books a human explicitly said to keep apart — with nothing connecting
  the destruction back to the click. Fixed by persisting the decision
  (`ReviewItem.ChosenAction` + `SetReviewItemDecision`, status and action in one
  Pebble batch). Two paired replay tests pin it, and both fail under mutation.

  ⚠️ **Read before flipping [[multidisc-apply-canary]].** Dispatch now keys on
  the chosen action rather than `Kind`. Under `Kind` dispatch `regroup.ambiguous`
  had no handler and could never merge; now an ambiguous hold recommending
  `combine` (24 of 356) reaches `ApplyMultidisc`. Intended, but it widens the
  blast radius of turning `review_apply_enabled` on.

  **The problem.** `proposedAction` is one generic string on **762 of 777** holds
  ("review: flat folder shares a title but ordering is unclear") and
  `survivorTitle` is frequently wrong. A queue where every row says the same
  thing is a queue nobody can work.

  **Recommendation.** Add structured fields to `regroupPayload`:
  `recommendedAction` (`combine` / `separate` / `duplicate-of` /
  `insufficient-evidence`), `recommendationReason`, and
  `recommendationEvidence` — the numbers that produced it (member durations,
  distinct stem count, part/disc marker count, folder shape). The evidence field
  is what makes the queue workable; a reason alone is just a nicer generic string.

  **Override.** `ApproveReviewItem` takes an optional body `{action: "..."}`
  defaulting to `recommendedAction`, and dispatch keys off the CHOSEN action.
  Today `approveOne` (`internal/server/handlers/review/handler.go`) dispatches on
  `item.Kind`, so this is the structural change that makes override possible.
  Keep the four `Kind` strings unchanged — they are load-bearing and the frontend
  maps them verbatim.

  `separate` needs no apply handler: every member is already its own book, so
  "separate into N" is a status transition, and `UpsertReviewItem`'s dedup-key
  idempotency keeps it decided across re-scans.

  **Also fix `deriveSurvivorTitle`**, which reads the folder name only and so
  returns author names ("C. T. Phipps"), "Volume 1", and wrong volume numbers
  ("…Vol. 01" on a folder whose files say Vol. 9). `folderNamedAfterBook` and
  `dominantPrefix` are already computed a few lines above — use the folder name
  when the former is true, the dominant member title when it is not, and emit
  empty rather than a wrong title when neither is trustworthy.

  🔴 **Sequencing.** The decisive signal is member `DurationSec`, which was ZERO
  for 97.5% of the queue because those books had no `book_file` rows. Do this
  AFTER [[relink-unlinked-books]] and a regroup re-run, or the recommendations
  are computed on blank evidence — the same failure that let 41 of 43 "confident"
  candidates propose merging distinct novels.

- [ ] **Canary the multidisc applies behind a before/after snapshot** — owner
  item 3 (2026-08-05). 138 pending `regroup.multidisc` holds; running them
  requires flipping `review_apply_enabled`, which is OFF in prod.

  🔴 **SNAPSHOT TO A FILE ON DISK BEFORE FLIPPING THE FLAG.** Capture, per
  candidate: every member book ID, title, duration, file path, and which ID
  `pickPrimary` will select (smallest ULID —
  `internal/plugins/maintenance/regroup_apply.go`). The apply path **hard-deletes
  absorbed rows**, so post-hoc reconstruction is impossible; the on-disk snapshot
  is the only record.

  That snapshot is not theoretical caution: it is what caught **41 of 43**
  "confident" multidisc candidates that would have merged distinct novels into
  single books. Do not skip it because the classifier looks better now.

  🔴 **Approve by explicit `ids:[...]`, never kind-scoped.** The frontend's
  `handleBulkAction(kind, 'approve')` approves EVERY pending item of a kind — one
  click with the flag on fires 138 `CombineBooks` calls. Start with a handful of
  groups verifiable by ear, diff the snapshot, then widen.

  Note a separate finding worth checking first: a 2026-08-05 measurement found
  **9 of 138** multidisc holds have members that are individually book-length,
  meaning the series-guard would fire on them if it were evaluated. The guard
  only applies to the flat branch — the disc and chapter/edition branches do not
  check it. Those 9 are near-misses still sitting in the queue.

  Depends on [[review-queue-recommendations-and-overrides]] (per-item action
  selection) so approval targets one hold at a time.

- [x] **Series names that are really book numbers** — owner item 4
  (2026-08-05, shipped + applied to production 2026-08-06, PR #2156).

  `maintenance.series-denumber` now reads the embedded shapes as well as the
  trailing ones, each scored by confidence. Applied on production: **25 series
  merged into 21 base series, 52 books given a real series position, 0 failures**;
  a re-run confirmed the high tier drained 25 → 0 with the other tiers untouched.

  🔴 **This was a DATA bug, not a display bug** — the number belongs in the
  series *position* field, not baked into the series *name*. Kept here because
  the owner corrected that reading twice; do not re-derive it.

  What the tiers are for, in the production data:
  - **high** (keyword-vouched, e.g. `Evil Genius: Book 4: …`) — applied.
  - **medium** (bracketed, e.g. `Dragon Born [04]`) — 198 rows, **NOT applied**.
    ~180 of them turned out to be shattered-book debris, not series positions.
    See the follow-up task below.
  - **low** (bare number, e.g. `08. Battle for the Abyss`) — 466 rows, reported
    only, and unappliable by construction. `86—EIGHTY-SIX` is a real series name
    in this library with the identical shape.

  Rollback artefacts on the server:
  `/var/lib/audiobook-organizer/series-denumber-{,APPLY-,VERIFY-}2026-08-06.tsv`.

- [ ] **"First Aid" — one sequenced library validate + repair system** — owner
  design 2026-08-05: *"one big system that basically had a investigation →
  retesting with more advanced situations → fixers."* Architecture and locked
  decisions: [`.claude/notes/2026-08-05-first-aid-architecture.md`].

  **Three tiers, separated by what they can afford PER BOOK:**
  - **Tier 1 — investigation.** All ~44,887 books. Budget: one DB read + one
    `os.Stat`. Cannot afford duration probing, hashing, or cross-book comparison.
  - **Tier 2 — escalation.** Only tier 1's flagged set (thousands), so it CAN
    afford probing real durations, matching a candidate's tracks against other
    books, and fingerprint comparison.
  - **Tier 3 — fixers.** One per confirmed verdict, small and independently
    testable.

  **Convergence is the property that matters.** Rather than hard-coding
  "relink before regroup", run fixers then RE-INVESTIGATE; the next pass sees the
  new durations and reclassifies. Re-run until investigation returns nothing
  actionable — idempotent by construction.

  **Sub-tasks still open:**
  - [ ] Tier-2 duration probe for the **1,019** directory-shaped books that went
    to review purely because `classifyUnlinked` passes `nil` durations. They are
    un-probed, not unknowable.
  - [ ] Duplicate detection + **combine-by-template** + version-group (the
    Successors class) — see [[never-delete-re-associate]] below.
  - [ ] Orchestrator + frontend button, dry-run by default, no schedule.
  - [ ] **Missing-input triggering:** when a check's input is absent, ENQUEUE the
    op that produces it. `OperationDef.Requires` already supports
    `ReqOpCompleted` (with `AllFiles`) and `ReqFieldSet`, with a dependency graph
    and `waiting_deps` parking — but parking WAITS and never enqueues the
    producer. First Aid must own that step. ⚠️ That subsystem shipped flag-OFF
    and dormant (#1442) with `dedup.check-book` as its only consumer; its one
    review caught three real bugs including a promote path that never dispatched.

  **Roster — ops to sequence** (tier 1) `relink-unlinked-books` ·
  `reconcile-scan` · `orphan-book-files-cleanup` · `dedupe-book-file-rows` ·
  `purge-millisecond-durations` · `booksig-recovery-audit`; (tier 2)
  `duration-reextract` · `file-integrity-check` · `malformed-m4b-remux/transcode`;
  (tier 3) `duration-backfill` · `repair-junk-titles` · `title-repair` ·
  `title-backfill` · `series-denumber` · `regroup-shattered-ai`; (tier 4, GATED)
  author/series identity ops → `metadata-refresh` · `isbn-enrichment` ·
  `auto-match-transcribed`.

  **Excluded as janitorial** (server health, not book correctness):
  `purge-deleted` · `tombstone-cleanup` · `temp-file-cleanup` ·
  `cleanup-activity-log` · `purge-old-logs` · `cleanup-old-backups` ·
  `trash-cleanup` · `archive-sweep` · `db-optimize` · `optimize` ·
  `batch-poller` · `bulk-write-back` · `intro-transcribe` · `extract-wav-clips`.

  Dedup subsystem stays SEPARATE but shares the duplicate-matching logic — it has
  its own queue, gold labels and calibrated thresholds, and folding it in
  wholesale is how 57 ops accumulated.

- [ ] **Never delete — re-associate (duplicate resolution)**. Deleting a
  redundant book row is **not idempotent**: rescan regenerates a book for any
  file no `book_file` row claims, so deleted rows come back. `block_hash`
  (`DoNotImport`) suppresses that but makes real audio permanently unrecoverable.
  Resolution: (1) detect that a group's tracks map onto a better-assembled book;
  (2) combine the debris into one book using that book's track list as a
  **template**, matching by duration instead of guessing boundaries from
  filenames; (3) version-group them, primary = most complete (ties to earliest
  ULID). Debris is not always a clean copy — The Successors debris was 11 rows /
  17 files covering 12 of 13 tracks with 5 internally-redundant files.

- [x] **Warm the metadata-results build at boot** — owner item 6 (2026-08-05,
  shipped 2026-08-06).

  The metadata-results build took **34 s cold**. It was memoised (60 s TTL, PR
  #2142) but not warmed at startup, so the first person to open the match UI
  after a restart ate the full 34 s.

  Shipped as `warmMetadataResultsCache`
  ([`internal/server/metadata_results_warmer.go`](internal/server/metadata_results_warmer.go)),
  enrolled in `startCacheWarmers` alongside the authors/series/facets/library-list
  warmers, with `metadata_results_warmer_test.go` asserting both that it degrades
  rather than panics on a nil store and that the cache is genuinely populated
  afterwards.

  ⚠️ Note for anyone auditing this later: the stale-while-revalidate work merged
  the same night (PRs #2153/#2154, 46× measured on prod, 28.9 s → 0.63 s) is a
  **different** fix for the same symptom. SWR keeps a warm cache from going cold
  under load; it does not help the first request after a restart. Both were
  needed, and only the warmer closes this item.

- [x] **Relink unlinked books — detector + repair op** — owner item 5
  (2026-08-05). Op `maintenance.relink-unlinked-books` shipped in PR #2147.

  **The measurement.** A whole-library survey found **17,149 of 44,887 books
  (38.2%)** own ZERO `book_file` rows — not the ~1,300 originally estimated.
  Disk check of every one of those paths: **16,027 resolve to a real file, 1,029
  to a directory, 93 are genuinely missing.** They are **unlinked, not orphaned**
  — the remedy is to relink, never to delete.

  **Why no existing op saw them.** `maintenance.reconcile-scan` flags a book only
  when `os.Stat` on its path FAILS. These all stat fine, so it walked past every
  one and reported the library healthy.

  🔴 **Why this blocked everything else.** `regroup-shattered-ai` derives
  `DurationSec` by summing `book_file` rows, and its `membersAreBookLength`
  series-guard — the check that stops distinct novels being merged — cannot fire
  when that sum is zero. With **97.5% of the review queue** made of these books,
  the guard was inert and the queue was built on blank evidence.

  ⚠️ **Do not measure this with `Book.duration`.** It is a snapshot and is
  populated (16,596 of the 17,149 have `duration > 0`), so coverage looks ~85%
  when the classifier's real coverage was ~2.5%. Measuring the wrong field is how
  this stayed invisible. `total_file_count` on the LIST DTO is a validated proxy
  (100% agreement vs per-book `/files` across 4,774 books); the single-book
  endpoint does not populate it.

- [ ] **Remaining after the first apply:** 1,019 directory-shaped books held for
  review and 93 missing reported only (already `reconcile-scan`'s remit; some may
  be offline mounts rather than deleted audio).

  **The tier-2 probe now exists and has been measured** — `maintenance.probe-directory-books`,
  PR #2162, dry-run against production 2026-08-06 (op `01KZC8A30Z22B81R8NKDHBFZFX`):

  ```
  examined=1019  actioned=0  skipped=1019  errors=0
  actions: link=434  review=585
  ```

  **434 of the 1,019 are confidently linkable.** They were in review only because
  `classifyUnlinked` passed `nil` durations, so `ClassifyDir`'s series guard could
  never fire — the classifier existed and was correct, it was simply being called
  with the one argument that disables it. 585 correctly remain in review.

  What is left here is the **apply**, which needs a human gate. The 93 missing are
  untouched by this and stay with `reconcile-scan`.

- [ ] **Re-run `regroup-shattered-ai` after relink and re-measure the queue.**
  With durations present the series-guard becomes live for the first time across
  most of the queue. Baseline to compare against: 357 pending holds — 217
  ambiguous / 138 multidisc / 1 anthology / 1 version-group. This measurement
  tells us how much of owner item 1 was a DATA problem rather than a classifier
  problem, and should be taken before investing in recommendation tuning.

- [x] ✅ 🐛 **`GetBooksByVersionGroup` silently under-reports group membership, which
  breaks the one-primary-per-group invariant.** Found in production 2026-08-06
  while version-grouping the two copies of *The Successors*.
  **FIXED 2026-08-10 — see RESOLUTION at the end of this entry.**

  **Symptom.** Two books both carry `version_group_id =
  01KNDBPNB289W2Y6TMXS2DDSEG`, but `GET /api/v1/version-groups/<gid>` returns
  only ONE member. `PUT /audiobooks/<id>/set-primary` therefore leaves BOTH books
  flagged `is_primary_version = true`, so the library shows two tiles for one
  book. Re-running set-primary does not help — it demotes only what the lookup
  returns.

  **Root cause** (`internal/database/pebble_store.go`, `GetBooksByVersionGroup`).
  The fast path iterates a `book:versiongroup:<gid>:<id>` index, then falls back
  to a full scan **only when the index yields ZERO results**:

      if len(books) > 0 { sortVersions(books); return books, nil }
      // Fallback: full scan for groups whose index hasn't been backfilled yet

  A *partially* populated index — some members indexed, some not — returns the
  partial set and never falls back. The zero-result guard reads like a correct
  fallback and is exactly wrong for partial data.

  The index is only refreshed by `UpdateBook` when `VersionGroupID` **changes**,
  so a book that acquires a group through a path that does not trip that
  comparison never gets an index entry. Re-POSTing
  `/audiobooks/<id>/versions` does not repair it: the group ID is already
  correct, so nothing changes and no index write occurs.

  **Blast radius is wider than the one endpoint.** `ApplyVersionGroup`
  (`internal/plugins/maintenance/regroup_apply.go`) uses the same function to
  "enumerate every current member and demote strays" — the safety net that keeps
  one primary per group when a `regroup.version-group` hold is approved. With a
  partial index that net silently does nothing, so approving a version-group hold
  can leave two primaries behind.

  **Fix directions** (pick after measuring):
  1. Make the fallback trigger on *suspected incompleteness*, not just zero — e.g.
     always cross-check against the authoritative rows, or verify the returned
     count against a group-size counter.
  2. Write the index entry on every `UpdateBook` where `VersionGroupID` is
     non-empty, not only when it changes (idempotent write).
  3. Add a repair op that rebuilds `book:versiongroup:*` from the Book rows, and
     run it once — existing groups are already affected.
  4. *(added 2026-08-10)* **Read through memdb instead.**
     `internal/database/memdb_schema.go:176` already declares
     `memIdxVersionGroupID` over `Book.VersionGroupID`, memdb stores full
     `*Book` values (`memdb_reads.go:606,622`), and `GetBooksByVersionGroup`
     **never calls `p.mem()`** — it goes straight to Pebble. That index is
     complete by construction and costs O(|group|), so it needs neither a
     completeness heuristic (1) nor a backfill (3). Not adopted unreviewed: it
     necessarily returns MORE members than today, and `metafetch`
     (`service_apply.go:303`, `service_writeback.go:872`) enumerates siblings
     through this call before writing to them. Returning the correct set is the
     point, but it widens what those write paths touch, so it wants an owner's
     eyes rather than an autonomous merge.

  **REPRODUCED 2026-08-10** (deterministically, in a throwaway probe — see
  "why this is not committed" below). Create two books in one group, then
  `pebble.Delete` exactly ONE `book:versiongroup:<gid>:<id>` row, leaving both
  authoritative `book:<id>` rows untouched:

  | Index state | `GetBooksByVersionGroup` |
  |---|---|
  | both members indexed | **2** ✓ |
  | **one** row dropped | **1** ✗ — the second book vanishes |
  | **both** rows dropped | **2** ✓ — the documented fallback engages |

  **Losing more index data produces a more correct answer.** That third row is
  the crux: the `len(books) > 0` guard cannot distinguish "found everything"
  from "found something", so an empty index is safe and a partial one is not.
  Damage in the range 1..n-1 is the only damage that is invisible. The probe
  also confirmed the authoritative row still carried the right
  `VersionGroupID` throughout — the truth was present and simply not consulted.

  **That third row also discriminates between the four fix directions**, and is
  the most decision-relevant thing measured here. Because a fully-empty index
  returns the correct set, direction 3 (rebuild `book:versiongroup:*`) is
  *provably sufficient against the read path exactly as it stands* — it needs no
  code change at all to a function that `metafetch` writes through, only a repair
  run. Directions 1 and 4 change that read path's results; direction 2 fixes only
  future writes. So the real question for the owner is narrower than four
  options: **is a one-off prod repair enough, or does the read path also need to
  stop trusting a non-empty index?** Anything that can drop index rows again
  (or any group that acquires members through a path not tripping the
  `VersionGroupID`-changed comparison) re-opens the hole, which argues for doing
  3 now and 2 or 4 as the durable guard.

  Confirmed unaffected by memdb warmth: the enumeration reads `p.db` directly,
  so the repro does not depend on warmup timing.

  **Why this is not committed as a test yet.** It is red against `main`, and a
  knowingly-red test on `main` is the same class of "green means nothing" defect
  this backlog keeps turning up. It belongs in
  `internal/database/pebble_store_index_consistency_test.go` — which already has
  the `store.(*PebbleStore)` / `ps.db` raw-index pattern and the sibling
  soft-delete cases — landing in the SAME PR as whichever fix direction is
  chosen. Ready to paste:

  ```go
  vg := "VG0000000000000000000PROBE"
  a, _ := store.CreateBook(&Book{Title: "Alpha", FilePath: "/probe/a.mp3", VersionGroupID: strPtr(vg)})
  b, _ := store.CreateBook(&Book{Title: "Beta",  FilePath: "/probe/b.mp3", VersionGroupID: strPtr(vg)})
  ps := store.(*PebbleStore)
  // Simulate partial backfill: drop ONE member's index row.
  _ = ps.db.Delete([]byte(fmt.Sprintf("book:versiongroup:%s:%s", vg, b.ID)), pebble.Sync)
  got, _ := store.GetBooksByVersionGroup(vg)
  if len(got) != 2 {
      t.Errorf("live book %s absent from its own version-group listing: got %d %v", b.ID, len(got), titles(got))
  }
  _ = a
  ```

  Note `dbtest.AssertStoreInvariants` invariant (b) — "a LIVE book must be
  discoverable by its own version-group listing" — is *already* exactly this
  assertion and passes everywhere it is called. It cannot catch this: its
  package doc states it uses only the exported Store surface and so "cannot see
  raw secondary-index rows", meaning no caller has ever constructed a partial
  index for it to inspect. The invariant was never wrong; nothing ever put it in
  front of the failing state.

  **RESOLUTION 2026-08-10.** Owner directed "fix the root cause" rather than
  taking the one-off prod repair. Directions **2 and 3 shipped; 1 and 4 did
  not** — see below for why each was left alone.

  - **Direction 2 (write path, the durable guard).** `UpdateBook` now writes the
    current group's index row unconditionally; only the delete of the *old* row
    is still gated on `oldVG != newVG`. Any book touched by any write path now
    repairs its own index entry, so a missing row can no longer persist.
  - **Direction 3 (repair).** The backfill sentinel moved
    `versiongroup_index_v1_done` → `..._v2_done`, so every deployment rebuilds
    the index once on next start. This *is* the prod repair — no manual op, no
    maintenance run. Bump again if the key format or indexed set changes.
  - **Direction 1 (read-path completeness oracle) — NOT done, deliberately.**
    The `len(books) > 0` guard stays. Gating the fallback on the backfill
    sentinel was drafted and rejected: a genuinely missing row would then return
    EMPTY instead of the full scan's correct answer, trading a silent
    under-report for a silent zero on a path that also feeds `/versions`,
    `regroup_apply`, and metafetch writeback. A real fix needs a completeness
    signal that does not depend on the result being non-empty (an authoritative
    per-group member count); that does not exist yet. The limitation is now
    documented in-code at the fallback rather than left implicit.
  - **Direction 4 (read through memdb) — NOT done.** Unchanged reasoning: it
    necessarily returns MORE members than today and metafetch enumerates
    siblings through this call before writing to them. Still wants owner review.

  Also fixed in passing: all three writers of this index (`CreateBook`,
  `UpdateBook`, `BackfillVersionGroupIndex`) now store the **book ID** as the row
  value. `CreateBook`/`UpdateBook` previously stored a full serialized `Book`
  "to eliminate point lookups", but the read path had long since stopped
  trusting that copy and takes the ID from the key instead — so it was never
  read. Making the update write unconditional would have doubled write
  amplification had the fat value stayed.

  **Tests** — `internal/database/pebble_store_versiongroup_index_test.go` (new).
  Damages exactly ONE index row, asserts the under-report reproduces (2 of 3),
  then asserts a same-group `UpdateBook` heals it back to 3. Negative control
  run: with the self-heal reverted the suite exits 1 on
  `self-heal failed: want 3 books after a same-group update, got 2`. Two
  companion tests pin the empty-index fallback (still returns the correct 3) and
  `CreateBook`'s index write + value format. The whole-index-wipe case is
  covered precisely because it passes *with the bug present* — it is the reason
  the defect stayed invisible.

  **Also needs an invariant test**: after linking N books into a group and
  setting one primary, exactly one member must have `IsPrimaryVersion == true`.

  Related: [[version-group-acoustic-audit]] (which will read group membership and
  would inherit this under-reporting), [[first-aid-library-validate-repair]].

- [x] **PERF: `maintenance.dedupe-book-file-rows` spends ~45 seconds per book, and
      that is enough to blow its own 2-hour timeout.**
      RESOLVED 2026-08-06 in PR #2161 — but almost every premise below was wrong,
      so read this correction before trusting the analysis that follows it.

      **It never hit the 2-hour `Timeout`.** It hit the 5-minute `ProgressTimeout`
      watchdog, at book 19/194. Both liveness bugs were already fixed on main
      (heartbeat `1908396b`, worker pool `df20b8d6`).

      **Total work is ~1.3 h, not 2.4 h.** Real denominator from the scan line:
      `redundant_rows=2901` across 194 books. The "194 × 45 s" extrapolation
      double-counted skew — books process in sorted-ID order and the first 19
      happened to carry 22–47 duplicates against a mean of 15.

      **The unit is per-ROW, not per-book:** ~1.35 s fixed per deleted row, +~7 ms
      per remaining file. Proven twice — the dry run of the identical read loop over
      all 194 books took 2.2 s total, and the per-row delta stays flat
      (1.85/1.94/1.42/1.66/1.54 s) as `total_files` falls 65 → 34, which rules out
      the O(R²) re-read hypothesis below.

      **Actual cause:** `DeleteBookFile` fires `notifyBookFileChange` per row, and
      each runs the full `RecomputeBookAggregates` → `UpdateBook` chain: two
      `pebble.Sync` commits, a full copy-on-write `book_ver` snapshot of the entire
      old Book, and two global go-memdb write transactions. Hypothesis 1 below was
      right about the *structure* and wrong about the cost (`InvalidateLibraryStats`
      is a lazy single NoSync delete, not a recompute); hypothesis 2 was refuted
      outright — `RecomputeBookAggregates` reads only the book's own files.

      **Fix:** `DeleteBookFilesByIDs` — one batch, one Sync, one notify per affected
      book. Salvage deliberately NOT folded into the batch: rescued keeper fields
      must commit *before* donors are deleted, and an atomic batch would remove the
      skip-on-failure escape.

      Measured on the full production run (2026-08-04, op
      `01KZ6W1H46696CZDBHCZF10W6C`): 9 books in ~7 minutes, steady. Extrapolated over
      the 194 affected books that is **~2.4 hours against a `Timeout: 2 * time.Hour`**
      declared in `dedupeBookFileRowsDef()`, so the op cancels itself with roughly
      the last 40 books unprocessed and needs a second invocation to finish.

      Not a correctness problem — each book is committed independently and the op is
      idempotent, so a re-run simply picks up the remainder. But an op that cannot
      complete its own workload in one pass is mis-sized, and it will get worse, not
      better, as the library grows.

      **~45s to delete ~15 rows from one book is the anomaly worth explaining.** The
      per-book work is small: one `GetBookFiles` (Pebble-direct), a handful of
      `DeleteBookFile` calls, one `RecomputeBookAggregates`. Suspects, cheapest to
      check first:

      - `DeleteBookFile` → `notifyBookFileChange` may trigger a library-stats
        invalidation and full recompute **per row deleted**, not per book.
      - `RecomputeBookAggregates` re-reads the book's files; if it re-reads the whole
        library-level aggregate instead, that is the 5.6s full-scan class of bug
        already seen in `CountPrimaryBooks` (see
        [[project_countprimarybooks_cpu_fix]] — same shape, different caller).
      - The book loop is sequential. Per `CLAUDE.md`'s concurrency rule this is
        exactly a whole-library-scale loop doing meaningful per-item DB work, so it
        should have been a bounded `errgroup` pool from the start. Partition by book
        ID — books are disjoint, so parallel workers cannot touch the same row.

      Fixing the per-book cost is the real answer; raising the timeout only hides it.

- [ ] **Corrected book aggregates are invisible until memdb refreshes.**
      Observed on the first `maintenance.dedupe-book-file-rows` canary
      (2026-08-03): 338 redundant rows were deleted from 10 books and every
      duration was **unchanged** immediately afterwards. `total_file_count` still
      read 50 for a book whose files endpoint already returned 26. A service
      restart surfaced the corrected values — e.g. "Defending the Lost"
      158.00h → **12.15h** — so the data in Pebble was right the whole time and
      only the memdb-backed read was stale.

      Where to look: `DeleteBookFile`
      (`internal/database/pebble_store_bookfiles.go:730`) does the right things in
      the right order — Pebble delete, `DeleteBookFileFromMemDB`, then
      `notifyBookFileChange`. The suspect is
      `RecomputeBookAggregates`
      (`internal/database/pebble_store_book_aggregates.go:131-134`), which
      **early-returns without calling `UpdateBook`** when the recomputed values
      equal the stored ones. `UpdateBook` is what triggers `UpsertBookToMemDB`,
      and that is the call which reloads `book_files` from Pebble
      (`internal/database/memdb_sync.go:53-55`). Skip the write and memdb keeps
      the stale file set.

      Why it matters beyond this op: any caller that deletes book_files and
      relies on the aggregate being visible has the same blind spot, and the
      library list computes duration from the memdb file map, not the stored
      field.

      Until it is fixed, `dedupe-book-file-rows` says so in its completion
      message rather than letting an operator conclude the run did nothing.

      **Traced 2026-08-10 — the stated suspect does not fit the symptom. Read
      this before spending time on `RecomputeBookAggregates`.** Four things were
      verified by reading the code at `65e63135`; **none of this is a
      reproduction**, and the bug is NOT explained yet.

      1. **The op does not call `DeleteBookFile`.** `dedupe-book-file-rows` uses
         the batched `store.DeleteBookFilesByIDs`
         (`internal/plugins/maintenance/dedupe_book_file_rows.go:368`). The entry
         above says "where to look: `DeleteBookFile`" — that is a different code
         path from the one the canary actually ran.
      2. **The batched path already does the memdb delete.**
         `DeleteBookFilesByIDs` (`pebble_store_bookfiles.go:990`) calls
         `s.DeleteBookFilesFromMemDB(resolvedIDs)` at :1073 and then
         `notifyBookFileChange(bookID)` per affected book at :1078. So the
         book_file rows ARE removed from memdb on the delete path, independently
         of whether any later `UpdateBook` runs.
      3. **`total_file_count` is not a stored field**, so a skipped `UpdateBook`
         cannot stale it. It is derived at read time —
         `enriched[i].TotalFileCount = len(files)`
         (`internal/server/audiobooks_helpers.go:95`, and again at
         `internal/server/handlers/audiobooks/handler.go:387`) — from
         `FetchBookFilesForBooks` → `GetBookFilesForIDsCore`, whose memdb
         implementation (`memdb_reads.go:917`) reads `memTableBookFiles` by
         `memIdxBookID`.
      4. Consistent with that, `RecomputeBookAggregates` never touches
         `TotalFileCount` at all — its early return at
         `pebble_store_book_aggregates.go:131-134` compares only `Duration` and
         `FileSize`.

      Taken together: if the delete path removes the rows from memdb (2) and the
      count is derived from memdb at read time (3), then the early return in
      `RecomputeBookAggregates` cannot be what left `total_file_count` at 50.
      Something else kept those rows visible.

      **Where to look next**, in rough order of suspicion — all unverified:
      `DeleteBookFilesFromMemDB` routes through `memSync`, which during warmup
      either buffers or, on buffer overflow, abandons memdb entirely
      (`memdb_pending.go`). The canary ran against a production-sized library
      where warmup takes ~2 minutes, so a delete landing in that window is the
      first thing to rule in or out — including whether a warmup snapshot taken
      before the delete could be published after it. Note the observed fix was a
      **service restart**, which is consistent with a memdb-population problem
      and not with a missed `UpdateBook`.

      **To reproduce**, the shape that matters is a delete concurrent with
      warmup, not a delete on a quiet store — a quiet-store test will likely pass
      and prove nothing, the same way `dbtest` invariant (b) passes everywhere
      while the version-group under-report is real.

- [x] ~~**Restore the duration on `The Trapped Mind Project`**~~ **RETRACTED
      2026-08-04 — nothing to restore.** The original claim here was that the
      canary kept a fingerprinted row whose `Duration` was 0 and deleted the 129
      twins holding the real value. Probing the audio disproves it: the book's
      entire content is a 13.5-second, 91,958-byte MP3, and the surviving row
      (`file_size=91958`, `duration=13`) matches it exactly. 0.00h is simply what
      13 seconds looks like. The op behaved correctly; the error was reading a
      rounded display value as evidence of loss without checking the file.

- [x] **Flaky: `TestApplyPIDRepairSameFile`** (`internal/itunes`) failed
      `Minimal CI / Go Tests (short, race)` on PR #2126 — a PR that touches only
      `internal/server/server_maintenance_deps.go` and cannot affect the iTunes
      package.
      Verified as a flake, not a regression: **10 consecutive passes on the PR
      branch and 10 on `main`**, both with `-race` exactly as CI runs it.
      This is the **second** flake found on 2026-08-03; see
      [[2026-08-03-flaky-backfill-syncids-race-sanity]]. Two independent flaky
      tests blocking unrelated PRs in one evening suggests a shared cause worth
      one investigation rather than two: both are concurrency tests, both pass
      locally, both fail only under CI load. Suspect a shared fixture, a fixed
      sleep, or an unsynchronised goroutine handoff that only loses the race on
      a slower/contended runner.
      Do NOT keep re-running them — that is how a flake becomes permanent and
      how a real regression eventually gets waved through. Related:
      [[project_ci_gotests_intermittent_stalls]].

      **CLOSED 2026-08-10. The "shared cause" guess was right.** This test builds
      its store with `newRepairTestStore`, which was one of the three helpers
      that skipped `WaitForWarmup`; its sibling flake used `newSyncPebbleStore`,
      another of the three. That helper now carries the reason in a comment:
      *"Without this the repair tests read back a book_file that is in Pebble but
      missing from memdb."* Both helpers were fixed in #2131, and the underlying
      write-loss was structurally eliminated in `587b2fd0` (2026-08-06) — writes
      arriving during warmup are buffered and replayed before memdb publishes
      (`memdb_pending.go`), so the window no longer drops anything.

      **Evidence for closing** (gathered 2026-08-10):

      - `make test-short` runs `go test ./... -short -race`, so this test executes
        on every `Coverage Floor` and `Go Tests (short, race)` run. Neither this
        test nor its sibling has a `testing.Short()` guard — checked, so the runs
        below genuinely exercised them rather than skipping them.
      - **50 completed `Continuous Integration` runs since `587b2fd0`, 0 failures**
        (10 further runs cancelled by `cancel-in-progress`; those are evidence of
        nothing either way and are not counted).
      - The fix is covered by a 6-test acceptance suite,
        `internal/database/memdb_warmup_writeloss_test.go`, which pins the
        invariant in both directions (dropped create, phantom after dropped
        delete, concurrent writers, buffer-overflow degrades loudly, Reset not
        undone) and guards against vacuous passes by skipping when the warmup
        window was too narrow to exercise.

      **What is NOT claimed:** this particular flake was never reproduced red, and
      its mechanism is inferred from the shared helper rather than observed. The
      case rests on mechanism + fix + regression suite + streak, not on a
      reproduction. If it recurs, reopen — do not re-run it.

- [x] **Flaky: `TestBackfillSyncIDsJob_ConcurrentRaceSanity`** (`internal/maintenance/jobs`)
      failed the Coverage Floor gate on PR #2123, a PR that touches only
      `internal/server/middleware/absauth.go` and cannot affect this package.
      Verified as a flake, not a regression: **10 consecutive passes on the PR
      branch and 10 on `main`** locally. It fails only under CI load, which fits
      a timing-sensitive concurrency assertion.
      Do not just keep re-running it — find the timing assumption (likely a
      fixed sleep or an unsynchronised goroutine handoff) and make the test wait
      on a condition instead of a duration. Related: [[project_ci_gotests_intermittent_stalls]].

      **Update 2026-08-04 — a sibling test failed the same way, and there is now a
      concrete mechanism to test.** `TestBackfillSyncIDsJob_FreshLibrary` (same
      file) failed CI on PR #2129, which touches only `internal/plugins/maintenance`
      and docs — a different package entirely. It seeds 20 books and then asserts
      each has a syncID; one did not:

      ```
      backfill_sync_ids_test.go:102: Should be true
        Messages: book 01KZ6QV6AZPW2AE93P7M0TRVFN has no syncID
      ```

      25/25 passes locally with `-race`, so it is timing-dependent like its sibling.

      **Mechanism — CONFIRMED by reading the warmup path; fix shipped in #2131.**
      The job enumerates with `store.ListBookIDs()`, and its comment
      (`backfill_sync_ids.go:61-64`) correctly rules out the `GetAllBooksFrom`
      pagination cap — but `ListBookIDs` still takes the memdb fast path
      (`pebble_store.go:594`). `NewPebbleStore` starts warmup in a goroutine and
      publishes only at the very end (`memPtr.Store(memStore)`, `pebble_store.go:291`).
      Until it does, `mem()` is nil — which makes *reads* safe, since they fall back
      to Pebble, but silently no-ops every *write's* memdb write-through. A test
      seeding books in that window leaves them in Pebble while the published memdb
      never saw them, so those books are never enumerated and never get a syncID.

      `PebbleStore.WaitForWarmup` documents this as mandatory for tests
      (`pebble_store.go:147-152`) and three helpers were skipping it —
      `newSyncPebbleStore`, `newPebbleTestStore`, `newRepairTestStore`. #2131 adds
      the call to all three.

      **Keep this item open until a green CI streak earns closing it.** The fix rests
      on the documented invariant plus a matching failure signature, *not* on a
      reproduced red test: on an empty temp DB the window is sub-millisecond, and 40
      iterations under 20× CPU contention would not force it. Calling `WaitForWarmup`
      is correct regardless of whether it proves to be the whole story.

      Production is not affected — warmup is a one-time startup affair there and
      reads fall back to Pebble until it publishes.

      Note this is a *different* mechanism from
      `todo.d/2026-08-01-assignorphanvgs-offset-pagination.md`, which is about offset
      arithmetic over a swapping snapshot. Same underlying async-warmup design, two
      distinct failure modes; a fix should consider both.

      **CLOSED 2026-08-10 — the green streak this item was waiting on has been
      earned, and the cause is gone rather than merely quiet.**

      `WaitForWarmup` in the three helpers (#2131) was the first half. The second
      half landed in `587b2fd0` (2026-08-06): writes arriving during warmup are
      now buffered and replayed before memdb publishes (`memdb_pending.go`), so
      the lost-update window is structurally closed rather than avoided. The
      `WaitForWarmup` doc comment records the demotion — it is *"no longer
      required for CORRECTNESS"*, only for making tests deterministic about which
      read path they exercise.

      **Evidence** (gathered 2026-08-10):

      - **50 completed `Continuous Integration` runs since `587b2fd0`, 0 failures.**
        10 more were cancelled by `cancel-in-progress`; those prove nothing and are
        excluded.
      - Both this test and `TestBackfillSyncIDsJob_FreshLibrary` run under
        `go test ./... -short -race` with no `testing.Short()` guard, so every one
        of those runs actually executed them.
      - `internal/database/memdb_warmup_writeloss_test.go` pins the invariant in
        six shapes, including the mirror cases the original write-up did not
        cover: phantom rows after a dropped delete, buffer overflow refusing to
        publish and logging at ERROR, and Reset not being undone by an in-flight
        warmup. It also guards against vacuous passes — each test skips rather
        than passes when the warmup window closed before any write landed.

      The open sibling item above (`TestApplyPIDRepairSameFile`) is closed on the
      same evidence; its helper was one of the same three.

## DATA: BookFile rows are duplicated 2× AND their durations are milliseconds, not seconds

Found 2026-08-02 while chasing why the app showed **Hyperion at "0%, 48h 31m
remaining"** on its Continue Listening shelf. Two independent defects compound, and
either alone corrupts every duration-derived number on the ABS surface.

### Measured, on `01KNDBK4MM369VJXA1QKQ6YR8S` ("Hyperion")

```
total BookFile rows: 298
distinct tracks:     149   | tracks with >1 row: 148
duplication factor:  2.00x

duration min=521  max=1803755
rows >50000 (impossible as SECONDS for one track — that is >13h): 297 of 298
sum as-is       = 41276.8 h      <- what the code computes today
sum if ms       =    41.3 h
halved + ms     =    20.6 h      <- Hyperion's actual length ✓
```

### Defect 1 — every track has two BookFile rows

One from the organized tree, one from the iTunes tree:

```
464039s track=1  data/books/audiobook-organizer/Dan Simmons/Hyperion/Hyperion
464065s track=1  /iTunes Media/Audiobooks/Dan Simmons/01 Hyperion 001-149.mp3
```

The pair's durations differ by ~26 ms, so they are the same audio measured twice —
not two genuine files.

### Defect 2 — durations are stored in milliseconds

`BookFile.Duration` is **seconds** by contract: the committed oracle fixture uses
`Duration: 9975` for a 9975-second book, and `seedOracleLibrary` uses `1662` for
~27-minute tracks. But 297 of these 298 rows are 6–7 digit values that only make
sense as ms. Track 144 is the smoking gun — it carries **both** forms:

```
521534s   track=144   (milliseconds)
   521s   track=144   (seconds — same value, correct unit)
```

### Why it matters

`durationFor` (`abs/userdata.go`) and the mapper both sum `BookFile.Duration` as
seconds, and §5b makes that sum the ONE authoritative duration for `media.duration`,
the play session, `startOffset`, synthesized chapters, and the progress fraction. With
a ~2000× inflated denominator, `currentTime / duration` rounds to zero — which is
exactly the reported **"0%"** — and the remaining-time readout is nonsense.

### Scope — MEASURED 2026-08-03

Both defects were measured library-wide, and the two turned out to have very
different shapes than this single-book sample suggested.

**Defect 2 (units) is small and was mostly a _display_ bug, not stored corruption.**
Only ~2% of rows actually hold milliseconds. The library-wide symptom — 25,938 books
showing absurd totals — came from `service_filtering.go`, which divided **every**
duration by 1000 unconditionally while summing, and truncated each row to an integer
before adding. Correct second-valued rows were the ones being destroyed, on read.
Fixed in #2125 by routing the sum through `database.NormalizeDurationSec`, which
classifies **per row** from the bitrate the file size implies — exactly the
idempotent, per-row test this entry demanded. Zero of 843 multi-file books still show
the symptom.

**Defect 1 (duplication) is real, larger, and is NOT a uniform 2×.** The "2.00x"
figure was an artifact of the one book sampled. The true shape is a single file
duplicated up to **130 times**: `The Trapped Mind Project` had 130 rows for one file,
and one m4b's runtime was being counted 26 times (`568,802s = 26 × 21,877` exactly).
Addressed by a new dry-run-by-default op, `maintenance.dedupe-book-file-rows`
(#2128), which finds candidates on the cheap memdb path and then re-reads each group
Pebble-direct before deciding, because the memdb projection strips
`AcoustIDFingerprint`.

### Do not fix blind

- Deduping BookFile rows is a **destructive prod mutation** — it needs a dry-run and
  an explicit decision, and it interacts with the dedup subsystem and with
  `books/itunes/**` being HANDS-OFF.
- A units migration must be **idempotent and detectable**: track 144 proves both units
  already coexist, so a blanket `/1000` would corrupt the rows that are already
  correct. Any repair has to classify per row, not per book.
- Fixing units without deduping (or vice versa) leaves the duration wrong by 2×,
  which is still enough to misplace every chapter boundary.

### Status 2026-08-04

- [x] **Defect 2 — units.** Fixed on the read path (#2125) plus 798 stored durations
      corrected. `NormalizeDurationSec` classifies per row, so it is idempotent and
      cannot corrupt already-correct rows.
- [x] **Dry-run op for Defect 1.** `maintenance.dedupe-book-file-rows` shipped
      (#2128), dry-run by default, mirroring `maintenance.title-repair`'s `Apply=false`.
- [x] **Canary applied — 10 books, 338 rows deleted.** Every corrected total verified
      after restart (`Defending the Lost` 158.00h → 12.15h, `San Kuo` 294.05h → 19.66h)
      with `fingerprinted_file_count` unchanged on all 10.
- [x] **~~Canary defect — keeper lost data.~~ RETRACTED 2026-08-04 — there was no
      data loss.** The claim was that `The Trapped Mind Project` dropped to 0.00h
      because ranking kept a fingerprinted row whose `Duration` was 0. Checking the
      actual audio disproves it: that book's entire content is a **13.5-second,
      91,958-byte MP3**, and both the surviving row and the file on disk agree.

      ```
      iTunes copy       91958 bytes   duration=13.485s   bit_rate=54554
      surviving DB row  file_size=91958                  duration=13
      ```

      130 rows × 13s ≈ 1,690s ≈ 0.47h inflated → 13s after dedupe. **0.00h is the
      correct answer for a 13-second file**, and the op behaved exactly as designed.
      The error was reading "0.00h" as lost data without checking the audio.
- [x] **Keeper field-merge shipped anyway (#2129).** It is still right on its own
      merits — ranking selects a whole *row*, so a keeper genuinely can lack a field a
      twin holds, and merging is strictly additive. But it is **hardening against a
      latent hazard, not a repair of an observed loss**; no such loss has been
      demonstrated.
- [x] **DONE 2026-08-04 — duplicate `book_file` rows are gone library-wide.** Final
      verification dry run, after a restart so memdb was warm:

      ```
      314,153 rows scanned, 0 books affected, 0 redundant rows, would delete 0,
      failed 0
      ```

      Total across all runs: **204 books, 3,239 redundant rows deleted, 0 failures**,
      and "salvaged fields on 0 keepers" every time — no keeper anywhere was missing a
      field one of its twins held, which is the third independent confirmation that the
      data-loss finding was correctly retracted.

      The run needed three attempts for reasons worth remembering:
      1. cancelled at book 19/194 by the stuck-op watchdog (progress reported once per
         book, one book took >5m) → fixed in #2133;
      2. hit the op's own 2-hour `Timeout` at book 78/176 running sequentially at
         ~1.7 min/book;
      3. finished **95 books in 9.5 minutes** once the book loop was parallelised
         (#2135) — the same work the sequential pass took two hours to half-finish.

- [x] **⚠️ Duplicate rows were only half the inflation.** Deduping fixed 8 of the 10
      sampled books (`Shades of Glory` 144.71h → 12.06h, `The Undying Illusionist`
      261.61h → 17.26h, `Darkness Rises` 205.41h → 14.78h). **Two did not**, because
      their stored durations are milliseconds, not seconds:

      ```
      dur=241110   size=1600709   → 0.1 kbps as seconds |  53.1 kbps as ms
      dur=1307193  size=7997209   → 0.0 kbps as seconds |  48.9 kbps as ms
      ```

      Every row lands at 48–53 kbps read as ms — a spoken-word MP3 — and
      9,906h ÷ 1000 ≈ 9.9h, a real audiobook. #2125 fixed the **display** path via
      `NormalizeDurationSec`; the **stored** rows were never rewritten. Measured
      prevalence from a 2,733-row sample: **1.9% (53 rows)**, so roughly 6,000
      library-wide.

      **DONE 2026-08-04 (#2137).** Fixed in two parts:

      1. **`UpdateBookFile` now normalises to seconds.** It was the *last* write path
         that did not — `CreateBookFile`, `UpsertBookFile` and `BatchUpsertBookFiles`
         all did — so an update could reintroduce the very corruption those three
         exist to prevent. This also closes the tracked "unguarded `UpdateBookFile`"
         defect. The unit invariant now holds at the store, not per caller.
      2. **`maintenance.purge-millisecond-durations`** backfilled the historical rows.

      ```
      apply : 314,153 rows scanned, 214 books affected, 1,384 ms rows,
              converted 1,384, recomputed 214 books,
              skipped 9,352 (already seconds), failed 0
      verify: 314,153 rows scanned, 0 millisecond durations found — nothing to do
      ```

      The two books that survived deduping are now right:
      `01KNDB9V04D7MBTFVDKYWX286E` 19,294.11h → 9,906.11h → **9.90h**, and
      `01KNDB9ZHJSMBY7D98Y82PQTK0` 15,556.96h → 8,049.06h → **8.05h**. All ten sampled
      books now read 8–17h.

      ⚠️ **Correct the earlier estimate:** the "1.9% ≈ 6,000 rows" figure extrapolated
      from a 2,733-row sample was **wrong by ~4×**. The real count is **1,384 rows
      (0.44%)** — that sample was a targeted dump, not a random one, so its rate did
      not generalise. Prefer a full scan over an extrapolated sample for anything
      load-bearing.

      The 9,352 skipped rows are the reassuring part: they sit *inside* the same 214
      affected books and were correctly left alone, so the predicate discriminates per
      row, not per book.
- [ ] **`The Trapped Mind Project` is a 13-second stub, not an audiobook**
      (`01KNDB97CWFSMSEY68P82VDRBF`). Nothing to restore — but two things about it are
      still wrong and worth chasing as a class:
      its book-level `file_size` reads **532,805,172** (532 MB) for a 91 KB file, and
      the API reports `file_exists: true` for a `file_path` that is absent from disk.
      Both are book-level fields disagreeing with the underlying file. See the
      duration/filesize aggregation item — same family of defect.
- [ ] **5 books are multi-copy, not row-duplicated** — distinct paths for the same
      book (`Wind and Truth` 426 files, `Ajax's Ascension` 272). Deduping rows is the
      wrong tool; these need regrouping and should surface in the review queue.
- [ ] **`Call to Arms` (9,957h)** — 96 *distinct* files, unchanged by the dedupe run.
      A third shape, not yet diagnosed.
- [ ] **Corrected aggregates are invisible until memdb refreshes** — see the
      2026-08-04 entry on `RecomputeBookAggregates`. Not a duration bug, but it makes
      every duration fix look like a no-op until a restart.

## BUG: `AssignOrphanVGs` can silently skip books — offset pagination over an async memdb snapshot

**Severity:** correctness bug in a full-library maintenance op. Surfaces as a CI
flake, but the same defect skips real books in production.

`internal/reconcile/reconcile.go:1292` enumerates with offset arithmetic:

```go
for offset := 0; ; offset += pageSize {
    books, err := store.GetAllBooksCore(pageSize, offset)
```

and `GetAllBooksCore` (`internal/database/pebble_store.go:439`) reads **memdb**
when `UseMemDB` is set:

```go
if p.UseMemDB && p.mem() != nil {
    return p.mem().GetAllBooksCore(limit, offset, nil)
}
```

The memdb snapshot is republished **asynchronously** (`memdb warmup starting
(async)` → `memdb warmup published`). Offset pagination is only sound over a
stable collection: if the snapshot is swapped between page N and page N+1, the
offset no longer refers to the same position and rows are skipped or repeated.

**Observed**, CI run 30702594886, `TestAssignOrphanVGs_RealStoreConcurrent`:

```
reconcile_orphanvg_test.go:213: Assigned = 39, want 40
reconcile_orphanvg_test.go:226: book 01KYYSX09WES7849SHVVBN8H4N VersionGroupID not set
... assign-orphan-vgs summary total_checked=39 assigned=39 skipped=0 errors=0
```

`total_checked=39` for 40 books is the tell: the book was never **enumerated**,
so this is not a write race or a lost update — the op simply never saw it. It
therefore reports success while having skipped work, which is the dangerous
shape: no error, no retry, no signal.

Does not reproduce locally (5/5 passes) — it needs the scheduling pressure of a
loaded CI runner to land the snapshot swap mid-iteration.

**Fix:** enumerate with `ListBookIDs` + `registry.RunItems` rather than
offset-paging a mutable snapshot. This is the pattern the repo already mandates
for full-library jobs, for exactly this reason — see
[[feedback_getallbooksfrom_memdb_cap]] ("cursor pagination silently capped at
2×limit on prod memdb path", fixed in #1647) and the concurrency section of
CLAUDE.md. An ID list is a stable set; paging positions in a snapshot that can
be replaced underneath you is not.

**Also worth auditing:** every other `GetAllBooksCore(pageSize, offset)` caller
that walks the whole library has the same exposure. Grep for the offset-loop
shape before assuming this is the only one.

## ⚠️ DEPLOY GATE: /metrics now requires auth — configure Prometheus BEFORE the next deploy

**PR #2092 is merged but NOT deployed.** Deploying it without doing the below breaks
metrics collection silently.

There is a **live Prometheus + Grafana on the origin host**, scraping
`http://127.0.0.1:8484/metrics` every 15s with **1 year / 500GB retention**
(`--storage.tsdb.path=/mnt/cache/metrics/metrics2/`). It was found only by checking
`ps` — nothing in this repo references it, and `deploy/prometheus/` is documented as
"examples/snippets… nothing in this repo scrapes it", which is now false.

Since #2092 gates `/metrics` behind authentication, the next `make deploy` makes every
scrape 401 and leaves a gap in the series. Prometheus does not alert on its own scrape
failing unless a rule exists for it.

### Do this first (needs interactive sudo — that is why it was not done unattended)

1. Mint an API key in the UI: **Settings → API keys**. It looks like `abk_…`.
2. Install it readable only by Prometheus:
   ```bash
   sudo install -m 0600 -o prometheus -g prometheus /dev/null /etc/prometheus/abo.token
   printf '%s' 'abk_…' | sudo tee /etc/prometheus/abo.token >/dev/null
   ```
3. Add to the audiobook-organizer job in `/etc/prometheus/prometheus.yml`:
   ```yaml
       authorization:
         type: Bearer
         credentials_file: /etc/prometheus/abo.token
   ```
   Use the `_file` form: Prometheus re-reads it each scrape, so rotating the key needs
   no reload and the secret never lands in `prometheus.yml`.
4. `sudo systemctl reload prometheus`
5. Confirm the target is UP in Prometheus → Status → Targets, THEN deploy.

### Verify after deploying

```bash
curl -ksS -o /dev/null -w '%{http_code}\n' https://<server>:8484/metrics            # want 401
curl -ksS -o /dev/null -w '%{http_code}\n' -H 'Authorization: Bearer abk_…' \
     https://<server>:8484/metrics                                                  # want 200
```

### Also update

`deploy/prometheus/README.md` claims nothing in this repo scrapes `/metrics`. A real
scraper exists on the production host; the sentence is misleading and should say so.

## LATENT: web OAuth callback silently discards a custom-scheme `return`, falling back to `/`

**Severity:** latent. No shipped client currently exercises this path — see
"Why this is not urgent" below. Filed so it is not rediscovered from scratch.

`internal/server/handlers/oauth_login.go:145` picks the post-login destination:

```go
dest := "/"
if payload.Return != "" { dest = payload.Return }
http.Redirect(c.Writer, c.Request, dest, http.StatusFound)
```

`payload.Return` was set at `Start` via `sanitizeReturn(c.Query("return"))`, and
`sanitizeReturn` requires a single leading slash:

```go
if ret == "" || !strings.HasPrefix(ret, "/") { return "" }
```

So a native-app deep link such as `audiobooth://oauth` becomes `""`, `dest`
falls back to `"/"`, and the caller is sent to the web SPA root. **No error is
raised and nothing is logged** — the redirect target is simply replaced. A client
expecting to be handed back to its own URL scheme instead lands on the web UI,
which surfaces as an opaque "it logged me into the website" rather than as a
failure.

### Why this is not urgent

Production logs over 7 days show **zero** requests to `/auth/oauth/*` — the web
provider flow is reached only by the SPA's login buttons, which legitimately want
same-site paths. Audiobookshelf clients use `/auth/openid` +
`/auth/openid/callback` (`internal/server/handlers/abs/openid.go`) instead, and
that path already handles custom schemes correctly via `oidcRedirectAllowed` and
`oidcRedirect`.

This was misdiagnosed on 2026-08-01 as the cause of the AudioBooth login failure.
It was not — the real cause was Cloudflare Access intercepting
`/auth/openid/callback` before it reached the origin, fixed with a scoped Access
bypass on that single path. Recording the distinction here so the next
investigation does not repeat it: **a redirect-to-web-root symptom has two
plausible causes, and only traffic logs distinguish them.**

### Fix, if a client ever needs it

Do **not** loosen `sanitizeReturn` — it is the open-redirect guard and the reason
`d87cbf37` (account takeover via unregistered `redirect_uri`) cannot recur here.

Instead mirror the ABS path: on an allowlisted deep link, mint a single-use
PKCE-bound code via the `abs` package's existing code store and 302 to
`audiobooth://oauth?code=…&state=…`, letting the client redeem it at the existing
`/auth/openid/callback`. Two constraints that a naive implementation gets wrong:

1. **Gate on `redirect_uri` AND `code_challenge` together.**
   `/auth/oauth/:provider/start` is the unauthenticated web login endpoint; if a
   bare `redirect_uri` could trigger a 400, anyone could break web login by
   appending a query param to a link.
2. **There are two distinct PKCE exchanges** — server↔IdP (verifier already in
   `StatePayload.Verifier`) and app↔server (the app's own challenge). Conflating
   them either breaks the upstream token exchange or issues codes with no
   app-side proof of possession.

Unverified assumption to settle before building: whether
`ASWebAuthenticationSession` returns the `SameSite=Lax` `oauth_state` cookie on
the hop back from the IdP. If it does not, `Callback` dies at
`oauth_state_missing` regardless. Only a real-device test can answer it.

## GAP: only ~19.5% of books have cover art, so most ABS clients show placeholders

**Severity:** cosmetic but pervasive. Not a code defect — `GET /api/items/:id/cover`
behaves as designed.

Observed 2026-08-02: AudioBooth's library grid rendered, and every cover request in
the sample 404'd:

```
GET /api/items/cb6e44f7-…/cover  → 404
GET /api/items/7840afbd-…/cover  → 404      (5 of 5 in the window)
```

On prod, `/mnt/bigdata/books/audiobook-organizer/covers/` holds **7,885** files
against a library of roughly **40,400** books — about **19.5%** coverage.

### Why this is not a bug

`Handler.ItemCover` resolves via `metadata.CoverPathForBook`, which globs
`<RootDir>/covers/<bookID>.{jpg,jpeg,png,webp,gif}` and returns `""` when nothing
matches. The handler then answers 404, and its own comment records that as intended:
*"A 404 here is correct and harmless: both clients fall back to a placeholder."*

**Not yet confirmed:** whether those 5 specific items lack cover files, or whether the
sync-UUID → Book-ULID resolution is picking the wrong ID. With 19.5% coverage, 5
consecutive misses has a ~34% chance of being pure luck, so this is *likely* a data
gap but has NOT been proven. Verify by resolving one of those sync IDs to its Book
ULID and checking for `covers/<ULID>.*` before investing in a backfill — a mapping bug
and an empty directory look identical from the client.

### If it is the data gap

A cover backfill over ~32,500 books is a full-library maintenance op and must be
written to the repo's concurrency rules from the start (CLAUDE.md): bounded worker
pool, `registry.RunItems`, never a plain `for range books`. Network-bound if it
fetches from a metadata provider, so size concurrency to that provider's rate limits
rather than `runtime.NumCPU()`.

Look for an existing parallel sibling before writing a new loop — the acoustid
backfill (`internal/plugins/acoustid/backfill.go`) is the established pattern.

## UNSPECIFIED: play counts and listening history have no designed ABS surface

Raised while building the Phase 6 write half (2026-08-02). The owner's goal statement
names "play counts" as one of "all the backend features the application expects."
**The design spec defines no endpoint for them**, so nothing was invented — this
records the gap rather than guessing at a shape.

### What exists today

- `UserBookState.TotalListenedSeconds` accumulates per (user, book) and is written by
  the ABS sync path.
- `IncrementBookPlayStats` / `IncrementUserListenStats` /
  `GetBookStats` / `GetUserStats` exist in `pebble_store_playback.go` but are **not**
  wired to the ABS surface.
- `Book.ITunesPlayCount` is an imported scalar from iTunes, unrelated to listening
  recorded by this server.

### What real ABS exposes (and why we currently 404 it deliberately)

`GET /api/me/listening-stats` and `GET /api/me/item/listening-sessions/:id` are the
surfaces a client asks for. Both are **intentionally 404** today per spec §1.8.6: they
carry ~12 non-optional fields, callers wrap them in `try?`, and a half-correct body is
worse than none. AudioBooth polled `/api/me/listening-stats` 7 times in the 2026-08-01
window and tolerated every 404 without user-visible breakage.

### Decision needed before building

1. Is a play *count* even the right primitive here, or is `TotalListenedSeconds`
   (already recorded) what the owner actually wants surfaced?
2. If the ABS-shaped endpoints are to be implemented, all ~12 fields must be produced —
   a partial body is a regression from the current honest 404.
3. `POST /api/session/local[-all]` (offline replay) is the other half of an honest
   listening history and is itself unbuilt; `progress.MergeOfflineReplay` exists and is
   tested but has no HTTP caller.

**Do not implement piecemeal.** Half a stats surface reads to a client as a broken
server rather than an absent feature.

## ✅ SHIPPED — ABS progress-mutation endpoints exist (verified against `router.Routes()` 2026-08-14: `PATCH/DELETE /api/me/progress/:id`, batch update, all four remove-from-continue-listening forms, bookmarks CRUD — see `docs/reference/abs-implementation-status.md`)

**Severity:** user-visible feature gap, not a regression. Reported from AudioBooth
on 2026-08-02 immediately after the client reached a fully working state (SSO login,
library browse, and playback all confirmed the same night).

Observed in production:

```
01:13:17  GET /api/me/progress/44669fab-6544-4414-ae2d-fa8eba7c52f3  → 404
```

`remove-from-continue-listening` was reported as also not working. Its call does not
appear in the log window that was checked, so it is recorded here from the spec
rather than from an observation — confirm the exact path and method against
AudioBooth before implementing.

### This is planned work, not a defect

`docs/specs/2026-07-29-abs-sync-api-design.md:839` puts all of it in **Phase 6**:

> Progress + bookmarks: adapt playback store, `/api/me`, `PATCH /api/me/progress/:id`,
> `/api/me/progress`, bookmarks CRUD (new), remove-from-continue-listening; §5 merge
> policy

Phase 6's read half shipped — `/api/me` and `POST /api/authorize` both serve the
complete `mediaProgress` list from `UserDataProvider`. The **write** half was never
built, so every client-side progress mutation 404s.

### Endpoints to add

- `PATCH /api/me/progress/:id` — update progress for one item
- `GET`/`DELETE` on `/api/me/progress/:id` — AudioBooth issued a `GET`; check whether
  reset is a `DELETE` and the `GET` is only a pre-read
- `/api/me/progress` — batch
- `…/remove-from-continue-listening`
- bookmarks CRUD

### Constraints that already apply

- **`absReservedPaths`.** `/api/me/` is already a reserved *prefix*, so these inherit
  the exclusion and will not 301 into `/api/v1`. No new reservation needed — unlike
  `/api/authorize`, which needed an exact-path entry (see PR #2100).
- **§1.8.1 still governs the read side.** Any handler that returns a user payload must
  return the COMPLETE `mediaProgress` list or a 5xx. A mutation endpoint that responds
  with a truncated user object destroys local progress exactly as `/api/me` would.
- **`…/remove-from-continue-listening` needs a non-empty body** — `{}` suffices
  (spec:318). An empty `200` is fatal to these decoders (§1.8.6).
- **§5 merge policy** applies to writes: device↔device sync is explicitly out of scope
  for the phase, but the merge rules for a single device's updates are specified.

### Not a bug, do not "fix"

`GET /api/me/listening-stats` → 404 and `GET /api/me/item/listening-sessions/:id` →
404 are **correct**. The spec prefers 404 for the stats endpoints (~12 non-optional
fields; callers use `try?`), and a half-correct body is worse than none.

## MISSING (op now built, run pending): no book had stored chapters — `maintenance.chapters-backfill` shipped (#2364, fixed #2368/#2370, path-fallback #2372) but has NOT been run library-wide; the run decision is tracked as E02 in the 2026-08-14 task breakdown

Reported by the owner 2026-08-02: "don't we extract chapters from the files that have
them and then use the tracks for others? I'm not seeing the chapters in the app."

The extraction code **is** implemented and correct. It has simply never run against the
existing library.

### Evidence chain (all four links verified 2026-08-02)

1. **`SaveChaptersForBook` has exactly one caller:**
   `scanner.PersistChaptersForBook` (`internal/scanner/process_file.go:259`).
2. **That function is only invoked from a scan** — `internal/scanner/scanner.go:851`
   and `:1035`, both inside the per-book scan worker. Nothing else calls it.
3. **`library.scan` has not run in 14 days.** All 31 occurrences of `id=library.scan`
   in the journal are the op-*registration* line emitted at startup; there are zero
   run records. **There is also no chapter backfill op** — no registered op id
   contains "chapter" except the unrelated `dedup.quarantine-chapter-artifacts`.
   (Phase 4 of the ABS spec called for a `registry.RunItems` backfill; it was never
   built.)
4. **So `GetChaptersForBook` always returns empty**, and
   `abs/mapper.go:loadChapters` falls through to synthesizing chapters on the fly.

### 🔑 The important part: a backfill only helps SINGLE-FILE books

This is the non-obvious bit, and it decides whether a backfill is worth building.

| Book shape | Stored (scan) path | Live fallback (today) | Visible difference |
|---|---|---|---|
| **single-file** (m4b w/ embedded markers) | `probeSingleFileChapters` → the file's **real** embedded chapters | `SynthesizeChapters` over 1 track → **one** chapter for the whole book | 🔴 **Large.** 6 real chapters vs. 1. |
| **multi-file** (mp3 set) | `synthesizeMultiFileChapters` → `SynthesizeChapters`, one per file | `SynthesizeChapters`, one per file | ⚪ **None.** Same count, same titles; only sub-second boundaries differ (re-probed unrounded duration vs. stored `DurationSec`). |

Both paths call the **same** `audioutil.SynthesizeChapters`. So for a multi-file book a
backfill is a no-op as far as the user can see.

⚠️ **The book the owner was actually playing (`44669fab-6544-4414-ae2d-fa8eba7c52f3`)
is multi-file** — production traffic shows it streaming `/public/session/…/track/1`
and `/track/2`. **A backfill would change nothing for that book.**

### Decision needed

1. **Populate chapters** — pick one:
   - run `library.scan` (populates as a side effect, but does a great deal else, and
     has not run in 14 days for reasons nobody has written down); or
   - build the dedicated bounded-pool backfill op the Phase 4 spec called for
     (`registry.RunItems`, one ffprobe per single-file book).
   Either way, scope it to **single-file books** — that is where the entire visible
   gain is, and it avoids ~40k pointless ffprobe calls.
2. **Decide whether multi-file books should use their per-file embedded chapters.**
   `synthesizeMultiFileChapters` deliberately ignores them ("never from that file's own
   embedded sub-chapters, even when present — real ABS ground truth, spec §1.8.5").
   `audioutil.ShiftChapters` exists precisely to rebase them onto the whole-book
   timeline and is **unused** on this path. If the owner wants real chapters inside a
   multi-file audiobook, that is a **separate feature**, not a backfill — and it means
   deliberately diverging from real-ABS behaviour.

**Do not run a whole-library backfill without answering (1) first** — a scan touches
far more than chapters.

- [x] **TODO-ABS-MODEB** A Cloudflare **service-token** assertion is rejected as
      invalid, so the documented "Mode B" (edge service token + our own bearer
      token) cannot work at all. A `non_identity` Access JWT carries
      `common_name` and **no `email` claim**, so
      `internal/oauth/cfaccess.go:59-60` fails it, and
      `internal/server/middleware/absauth.go:166-171` turns *any* Verify error
      into a terminal 401 that deliberately never falls through to the bearer
      path — so the request 401s **even when it also carries a valid ABS bearer
      token**, and `internal/server/handlers/abs/login.go:53-55` makes password
      login unreachable too. Fix: have `Verify` distinguish a cryptographically
      *valid* but non-identity assertion (sig/iss/aud/exp all pass, no email)
      from an invalid one via a typed sentinel (`ErrNonIdentityAssertion`), and
      map only that sentinel to a `(nil, nil)` fall-through in
      `ResolveCFAssertion` — every other Verify failure must stay a terminal
      401. Tests: (a) forged assertion still 401; (b) valid non-identity + valid
      bearer → 200 via jwt mode; (c) valid non-identity, no bearer → 401
      `no-credential`; (d) login with non-identity assertion + password body
      reaches the password path. Revert-validate (b) and (d). — closed 2026-08-21:
      `oauth.ErrNonIdentityAssertion` sentinel implemented (`internal/oauth/cfaccess.go`),
      consumed by `ResolveCFAssertion` (`internal/server/middleware/absauth.go:193`) to
      fall through to the bearer path; all 4 acceptance scenarios covered by
      `TestABSAuth_NonIdentity_ForgedAssertionStillHard401`, `_WithBearerIsAdmitted`,
      `_WithoutBearerIs401`, `_LoginReachesPasswordPath`, `_LoginStillRejectsForgedAssertion`
      — `go test ./internal/server/middleware/... -run TestABSAuth_NonIdentity -v` all PASS
      (commit `ec2a6c83 fix(abs): let a non-identity Access assertion fall through to the bearer`).

- [ ] **TODO-SSO-EDGE** Neither native-app auth mode is actually configured at
      the Cloudflare edge, despite both being fully written up in
      `jdfalk/cloudflare-one` `access/audiobook-app-policies.md`. Measured via
      the CF API on 2026-07-31: the `books.jdfalk.com` Access app has exactly
      **one** policy (precedence 1, `allow`, email allowlist) — there is **no
      `non_identity` service-token policy** and **no service tokens exist on the
      account at all**; app-level `allow_authenticate_via_warp` is unset and
      org-level is `false`; and no cover-art bypass app exists (confirmed live —
      the cover path 302s to Access instead of reaching the origin). That fully
      explains the measured `service_token_status:false, is_warp:false,
      auth_status:NONE`. So `scripts/setup-audiobook-apps.sh` never ran against
      this account, or was rolled back — the doc describes a **design**, not the
      live state. Recommended path is **Mode C (WARP)**: it delivers a real
      identity JWT with an `email` claim, which satisfies `cf` mode exactly as
      already coded — no app changes, no `/status` change, no password. Mode B
      additionally needs TODO-ABS-MODEB fixed before it can work.

- [x] **TODO-DEPS-VULN** GitHub reports 5 Dependabot vulnerabilities on the
      default branch (2 high, 3 moderate). Triage and bump. — closed 2026-08-21:
      `gh api repos/jdfalk/audiobook-organizer/dependabot/alerts --paginate -q '.[] | .state' | sort | uniq -c`
      → `36 fixed`, 0 open.

- [ ] **TODO-SEC-BIND** The service binds every interface
      (`ExecStart=… serve --host 0.0.0.0 --port 8484`), so anything on the LAN
      reaches the origin directly and **Cloudflare Access is not a boundary** —
      the edge is only enforced for traffic that arrives through the tunnel.
      Bind loopback (or the tunnel-facing interface only) in
      `deploy/local.conf` so Access becomes the single front door, then verify
      the tunnel still serves `books.jdfalk.com`. Note in the PR that
      direct-to-LAN verification is no longer possible **by design** after this.
      The tunnel connector runs on rpi1-3, not on the origin host, so the
      loopback bind must account for that hop.

- [ ] **TODO-SEC-JWT** Rotate `ABS_JWT_SECRET` — it was pasted in plaintext into
      a chat transcript on 2026-07-31. It signs every ABS session token. Rotate
      it in `deploy/local.conf` (gitignored — never commit or print it; redact
      with `sed -E 's/(SECRET|TOKEN|KEY)=[^ ]*/\1=<redacted>/g'` when dumping a
      unit), redeploy, and confirm previously-issued tokens are rejected.

- [ ] **TODO-SEC-SYSTEMD** The unit has `User=audiobook`, `NoNewPrivileges`,
      `ProtectKernelTunables`, `ProtectControlGroups` and `PrivateTmp`, but no
      `ProtectSystem=strict`, no `ReadWritePaths`, no `CapabilityBoundingSet`,
      no `SystemCallFilter` and **no egress restriction**. `IPAddressDeny=any`
      plus a narrow allowlist is what stops a compromised process reaching the
      rest of the LAN. It needs the Whisper host on `:19847` and Ollama on
      `:11434`, plus outbound HTTPS for OpenLibrary/AcoustID — an over-tight
      rule silently breaks metadata and transcription, so test before claiming
      it works.

- [ ] **TODO-SRVTIMEOUT** Split or speed up the `internal/server` test package —
      it runs 434–480 s against Go's 600 s default per-package timeout, leaving
      under 30% headroom. Any concurrent load on the machine tips the whole
      package into a timeout that is indistinguishable from a deadlock: the
      panic dump names whichever goroutine happened to be mid-teardown
      (`operations/registry.(*Registry).Shutdown` blocked on `sync.WaitGroup.Wait`
      at `registry.go:1030` in the observed case), which reads as a real hang and
      sent a 2026-07-31 investigation down a false trail on PR #2083. Verified
      not a deadlock: the same commit passes in 480 s when run without competing
      load. Either shard the package, or set an explicit generous `-timeout` in
      the Makefile test targets so a slow run fails as "too slow" rather than
      masquerading as a lock bug.

      **The `-timeout` half is DONE.** #2270 put `-timeout 25m` on the Makefile's
      four `./...` targets (with a comment above `coverage:` explaining this exact
      masquerade); #2278 did the last live invocation that lacked it,
      `scripts/run-all-tests.sh`. A repo-wide sweep found no other. A bare
      `go test ./internal/server/` still runs on Go's 10m-per-package default.

      **Measured 2026-08-10 — the premise has drifted and the proposed fix is
      aimed at the wrong thing.**

      - Runtime is now **543 s** solo (`real 553 s`), not 434–480 s. Headroom
        against the 600 s default is **9.5%**, not "under 30%" — it has gotten
        worse, and that is *without* the `./...` contention the entry blames.
      - **~85% of wall time is idle**: `user 40.7 s + sys 40.0 s ≈ 81 s` CPU
        against 553 s wall. The package is not compute-bound; it is waiting.
      - **There is no slow test to fix.** 855 top-level tests summing to 540.5 s
        — so the time is inside tests, not compile or global fixture. The
        distribution: **4** tests ≥5 s (slowest `TestServerStartGracefulShutdown`
        at 14.1 s), **296** at 1–5 s, 89 at 0.1–1 s, 466 under 0.1 s. The top 25
        tests account for only ~85 s of 543 s.
      - **The cost is a fixed per-test fixture charge.** `setupTestServer` +
        `cleanup`, timed directly over 10 iterations, is **1.44 s mean**. There
        are **261 static call sites** (250 `setupTestServer`, 11
        `setupTestServerWithStore`), so ≈ **376 s, about 69% of the package**.
        That matches the 296-test 1–5 s band independently.

      **Which phase costs what** — measured per iteration over 5 iterations by
      timing each step of the fixture separately. The phases sum to **1.4425 s**,
      independently reproducing the 1.44 s figure above:

      | Phase | Mean | Share |
      |---|---|---|
      | `RunMigrations` | **828 ms** | **57.4%** |
      | `NewServer` (hub, queue, write-back batcher, fileIO pool) | 473 ms | 32.8% |
      | `NewPebbleStore` (disk-backed) | 134 ms | 9.3% |
      | `pools + store.Close` | 5.0 ms | 0.3% |
      | `RemoveAll` | 1.4 ms | 0.1% |
      | `MkdirTemp` | 0.44 ms | 0.0% |
      | `opRegistry.Shutdown` | **0.30 ms** | **0.0%** |
      | `opRegistry.Start` | 0.08 ms | 0.0% |

      **The registry teardown is NOT the cost.** It is tempting to connect the
      slowness to `opRegistry.Shutdown` blocking on `sync.WaitGroup.Wait`, since
      that is the goroutine the #2083 panic dump named — and an earlier draft of
      this entry asserted exactly that. The measurement refutes it: `Shutdown` is
      **297 µs**, four orders of magnitude below the fixture cost. The #2083
      panic dump named a goroutine that is normally free and only blocks under
      the contention that caused the timeout. Slowness and the deadlock-shaped
      panic are two separate phenomena that happen to name the same symbol.

      **This redirects the fix**, and not where sharding points. Sharding
      redistributes a 69% fixture charge without removing it; each shard still
      pays 1.44 s per test. **90% of that charge is `RunMigrations` +
      `NewServer`,** so those are the levers:

      1. **Migrations (57%)** — every test replays the FULL migration chain onto
         an empty store, producing a byte-identical result every time. Build one
         migrated Pebble directory once per package and copy/clone it per test,
         or share a migrated store where tests do not mutate global state.
      2. **`NewServer` (33%)** — construct the hub/queue/batcher/fileIO pool
         lazily, or let tests that only exercise handlers skip the parts they
         never touch.
      3. **Pebble open (9%)** — tmpfs or an in-memory VFS; worth doing only after
         the first two.

      Skipping `opRegistry.Start` — a lever an earlier draft proposed — would
      save **0.08 ms** and is not worth doing. Any of 1–3 is an
      isolation-sensitive refactor across ~260 call sites (`setupTestServer` also
      sets `database.SetGlobalStore`, so shared state is exactly where isolation
      would break) and wants its own plan, not a drive-by.

      *Not claimed:* the 543 s figure is a **single sample** on one idle Mac
      (the 1.44 s fixture cost has two independent samples that agree); 261 is
      **static call sites**, not dynamic invocations; and the phase table is one
      run of 5 iterations, so treat the shares as approximate rather than the
      millisecond values as exact.

      **🔑 CORRECTION 2026-08-10 — the package is not slow. The Mac's temp
      filesystem is. Lever 3 was ranked last and is actually the whole thing.**

      Same commit (`62b43c4e`), identical command
      (`go test ./internal/server/ -count=1`), three runs:

      | Run | Go package time | real | user | sys |
      |---|---|---|---|---|
      | Mac, normal `TMPDIR` (APFS) | **532.524 s** | 538.72 | 18.36 | 42.32 |
      | Mac, `TMPDIR` on a RAM disk | **33.704 s** | 36.32 | 6.82 | 7.59 |
      | U1 (Linux, 48-core) | **35.453 s** | 50.66 | 114.42 | 30.96 |

      **A 15.8× speedup from one environment variable**, landing within 2 s of
      an independently-measured Linux box. The Mac spent ~61 s of CPU across
      538 s of wall clock — **11% utilisation**. It was blocked, not computing,
      and `sys` fell 42.32 s → 7.59 s.

      This does not overturn the phase table above; it explains it. Migrations
      dominate *because* they write, and the write is what is expensive on
      APFS. The three levers were ranked by share of a cost that is itself an
      artifact of where the temp directory lives:

      - Levers 1 and 2 (migration snapshotting, lazy `NewServer`) are an
        isolation-sensitive refactor across ~260 call sites, and would buy less
        than moving the temp dir.
      - **Lever 3 — "tmpfs or an in-memory VFS; worth doing only after the
        first two" — is the fix, not the afterthought.** It was ranked at 9%
        because that is Pebble *open* time alone, but a memory-backed temp dir
        removes the durability cost from every phase that writes, migrations
        included.
      - **Sharding the package remains the wrong target.** It redistributes a
        cost that is not CPU-bound in the first place.

      *Not claimed:* this measures **the temp filesystem**, not `F_FULLFSYNC`
      specifically — the syscall was never isolated, so macOS full-barrier
      fsync is the *likely* mechanism, not a verified one. The cross-platform
      `user`-time comparison is also not trustworthy (rusage attribution for
      grandchildren differs between macOS and Linux); the argument rests on
      wall clock and Go's own package timer. Each row is a single sample, but
      the effect is far larger than any plausible run-to-run variance.

      **The `-timeout 25m` work is NOT invalidated** — it remains correct for
      CI and for any macOS developer without a RAM disk. What changes is that
      the timeout is a guard, not a workaround for something unfixable.

      **Open question for the owner (do NOT decide alone):** whether to make
      this the default — a `TMPDIR`-on-tmpfs Makefile target, or test-only
      Pebble sync settings. Both alter shared test infrastructure and one
      weakens durability guarantees in tests, so they are a judgement call, not
      a drive-by. The measurement is the deliverable here; the policy is yours.

## SEC: origin is reachable from the LAN — "bind loopback" is NOT achievable as specified

**Status:** finding, not yet fixed. Needs an owner decision between two options.

The origin listens on `*:8484`, so anything on the LAN reaches it directly and
Cloudflare Access is not a boundary for those callers. The standing task says to
"bind loopback instead of `0.0.0.0`". **That specific change cannot work here**, and
it is worth writing down why so nobody tries it again:

`cloudflared` does not run on the origin host. It runs on rpi1-3 and dials the origin
over the LAN. So the listener must be reachable from another machine by definition.
Binding `127.0.0.1` makes the tunnel unable to connect at all — the site goes down.
And binding the host's LAN address instead of `0.0.0.0` is **exactly as exposed**:
both accept connections from anywhere on the LAN. There is no bind address that is
simultaneously "not reachable from the LAN" and "reachable from rpi1-3 over the LAN."

Two options actually accomplish the intent. Both are host-level changes outside
`deploy/local.conf`, and both need interactive-sudo, so neither was applied:

1. **Firewall the port** (recommended, smallest change). An nftables/ufw rule
   restricting `:8484` to the rpi source addresses. Keeps the current topology; the
   origin stops answering everything else on the LAN. Care required: touch only 8484,
   never 22, or you lock yourself out of the box.
2. **Move `cloudflared` onto the origin host.** Then `127.0.0.1:8484` is genuinely
   correct and the port disappears from the LAN entirely. Larger change — it moves
   the tunnel off the rpi fleet and changes where tunnel outages come from.

**Note for whoever does this:** after either change, verifying the origin by curling
it directly from a workstation stops working *by design*. That is the success
condition, not a regression. Verify through `books.jdfalk.com` instead.

- [x] **DONE — verified 2026-08-14 by code read: `wireABSRoutes` builds `NewUserData`
  and FAIL-CLOSES (os.Exit) if the provider cannot be built or is not wired
  (`wire_abs_routes.go:342-378, :448-453`); `HasUserDataProvider` is asserted after
  registration.** Original: **ABS-SYNC (Phase 6, DATA LOSS if skipped): wire a
  `UserDataProvider` into the ABS auth handler.** `internal/server/handlers/abs` currently constructs with
  `UserData: nil` (`internal/server/wire_abs_routes.go`), so `/api/me`, `/login` and
  `/auth/refresh` report `mediaProgress: []`. That is correct **only** while the server
  holds zero ABS progress records — §1.8.1 of the design spec: AudioBooth *deletes*
  every local progress row absent from the server's list, so the moment Phase 6 starts
  persisting progress without wiring the provider, every device loses its listening
  positions on the next home-screen refresh. The interface is already defined
  (`MediaProgress`/`Bookmarks`, both must return the COMPLETE list; returning an error
  makes the handler answer 5xx rather than serve a truncated list). A startup
  `slog.Warn` flags the gap until it is wired.

- [ ] **ABS-SYNC: exempt the ABS surface from `BasicAuth()` when `basic_auth_enabled`
  is on.** The ABS group hangs off `s.router`, so it inherits the global
  `servermiddleware.BasicAuth()`. With basic auth enabled (off by default) every ABS
  client would need to send `Authorization: Basic …`, which collides with the ABS
  bearer token on the same header — the clients would be unable to connect and the
  cause would be invisible. Either exempt the ABS paths in `basicauth.go` or document
  that the two features are mutually exclusive.

- [ ] **ABS-SYNC: prune expired `abs_sess:` records on a schedule.**
  `PebbleStore.DeleteExpiredABSSessions` exists and is tested but has no caller. Add it
  to the same maintenance sweep that calls `DeleteExpiredSessions` for the browser
  keyspace, or revoked/expired ABS sessions accumulate forever.

- [ ] **ABS-SYNC: consolidate the two DRM detection paths, and wire one into the
  scanner.** PR #2067 adds extension-based `DetectDRM` in `internal/audioutil/drm.go`,
  but `internal/diagnosis/probe.go` already has an unrelated, richer mediainfo-based
  probe (`HasActiveDRM`). Two DRM code paths will drift. Decide which is authoritative,
  then wire it into the scanner so Audible AAX/AAXC files surface as
  **unplayable-with-reason** instead of importing and failing at play time. Note the live
  bug this fixes: `.aax`/`.aaxc` are **already** in the default `SupportedExtensions`
  (`internal/config/config.go` ~:2016) with zero DRM awareness. Caution: ffmpeg's `aax`
  demuxer is **CRIWARE game audio, not Audible** — do not key detection off it.

- [ ] **ABS-SYNC TASK-12 (P1, data-loss class): close the three identity gaps so §4.3's
  ID-durability claim is actually true.** Owner decided (2026-07-30) to hook **all three**
  paths, not just the worst one. Today only `merge.Service.MergeBooks` repoints sync IDs;
  these three still orphan a device's listening position:
  1. **`dedup.MergeBooks`** (`internal/dedup/book_dedup.go:395`) — a separate, still-live
     path used by `internal/reconcile/itunes_heal.go` that **HARD-DELETES**. An
     unrepointed sync ID here is unrecoverable: there is no surviving row to repoint later.
  2. **`CombineBooks`** — same file as the hooked merge, unhooked.
  3. **Untagged move** — `internal/scanner/scanner.go` (~2078-2099) mints a fresh Book
     ULID via `CreateBook` + version-link and never calls `RepointSyncItem`.
  Primitives already exist and are merged (`RepointSyncItem` in #2070,
  `RepointSyncFile` in #2068). Note `internal/merge/serialize.go` already provides a
  process-wide `mergeSerializeMu`, so no extra book-ID partitioning is needed — run
  inside that existing critical section. Requires a `-race` test exercising concurrent
  merges (`MergeBooks` has a prior race history in this repo).

- [ ] **ABS-SYNC: wave 2 — scanner + merge wiring.** Briefs in
  `docs/agent-tasks/abs-sync/`. TASK-03 (merge-follow hook into
  `merge.Service.MergeBooks`), TASK-07 (extract + persist chapters at scan time via
  `internal/scanner/process_file.go`), TASK-09 (bookmarks CRUD — ~~no bookmark feature exists today~~ SHIPPED: full CRUD registered and value-asserted, see `docs/reference/abs-implementation-status.md` 2026-08-14). Wave 1 merged: #2070, #2068, #2069.
- [ ] **ABS-SYNC: wave 3 — backfill + survival proof.** TASK-04 (idempotent sync-ID
  backfill over the existing library; MUST use a bounded worker pool per the CLAUDE.md
  concurrency rule), TASK-05 (ID-survival suite: rename / move tagged+untagged / retag /
  merge / file-replace). TASK-05 is the acceptance bar for §4.
- [ ] **ABS-SYNC: TASK-11 — auth core, both credential modes.** Brief not yet written.
  Unified identity resolution per spec §3.0.1: verified `Cf-Access-Jwt-Assertion` →
  user, else our own JWT, else 401. Mode B needs JWT + DB-backed sessions + **30d**
  access TTL (NOT 1h — see §1.6) + argon2id; Modes C/A trust the CF assertion with JIT
  provisioning against the allowlist, fail closed. Mandated test: the ABS router group
  must NOT inherit the `/api/v1` fail-open `cfaccess` behaviour — that would be an
  authentication bypass. Only this task may touch `go.mod`.
- [ ] **ABS-SYNC: Phase 3 — DTO mapping + library browse.** Depends on waves 1–2 and
  TASK-11. Must honour the verified client contract (§1.7–1.8): `publishedYear` as a
  **String**, non-null `userDefaultLibraryId`, **never paginate `user.mediaProgress`**
  (it deletes client-side progress), integer `total`/`numBooks`, real JSON booleans,
  flat `authorName`/`narratorName`, and never an empty `audioTracks: []` (omit the key
  instead). Gated by the merged conformance harness.
- [ ] **ABS-SYNC: Phase 5b — playback routes.** `POST /api/items/:id/play`,
  `GET /api/items/:id/file/:ino`, and the **unauthenticated**
  `GET /public/session/:id/track/:index` that AudioBooth streams from (§1.8.3). Uses the
  merged `internal/httputil` Range helper. Direct play only; HLS must degrade cleanly.
- [ ] **ABS-SYNC: Phase 7 — socket.io (Absorb only).** AudioBooth needs no websocket at
  all (verified against its `Package.swift`), but Absorb goes offline after 5 failed
  reconnects, and expects `emit('auth', <raw token string>)`. Deprioritized: the primary
  client ships without it.
- [ ] **ABS-SYNC: Phase 8 — topology, runbook, migration guide.** Cloudflare Access
  service token in a **dedicated Service Auth policy ordered FIRST** (the trap that bit
  users in both clients' issue trackers), the cover/image bypass (§1.9.5), tunnel-level
  JWT enforcement, and the client compatibility matrix. Runbook must record: never trust
  an app's reachability checkmark (Access returns HTTP 200 with HTML, so failures look
  like JSON decode errors), and AudioBooth's first-server-add cover bug is upstream, not
  ours.

- [ ] **REGROUP-PARTCHAPTER-PARSER** The Mistborn-style "Ambiguous folder" case
      (`01 P0-C0.mp3`, `07 P1-C6.mp3` — Part/Chapter naming, non-contiguous numbers)
      has no parser and stays classified as ambiguous (unaffected by the disc/track
      fix). Consider a Part→disc / Chapter→track parser as a fast-follow so these
      collapse with correct numbering too.

- [ ] **iTunes 2-way-sync P3 (cleanup) — decision: MEASURE-AND-STOP, no removal machinery.**
  The P0 cleanup provenance census ran on prod (97,999 `.itl` tracks): **provable merge
  orphans = 1, SHA-gated removable = 0** (`pid-census --merge-provenance`). P3 retires the
  unsafe `cleanup_merged.go` handler as a guarded no-op; do NOT build bulk removal. The
  count is a floor — prod has no durable merge-provenance trail (`merge.Service.MergeBooks`
  writes neither the `AutoMergeJournalEntry` journal nor `MergedIntoBookID`; the journal is
  empty). FOLLOW-ONS (not blocking): (1) if provenance-anchored cleanup is ever wanted, FIRST
  make the merge path record losers durably, THEN re-run this census; also a latent
  unmerge/audit gap. (2) Classify the 13,464 `no_live_owner` tracks by audiobook genre to
  separate the user's non-AO music/podcasts from severed orphans (doesn't change the P3
  decision). See `docs/specs/2026-07-23-itunes-2way-p0-findings.md` §F4.
- [ ] **iTunes 2-way-sync — remaining P0 measurements.** (a) Cross-type PID collisions
  (audiobook vs non-audiobook sharing a PID) — confirm PID-on-multiple-primaries stays 0
  post pid-repair. (b) Bookmark/field-preservation byte-proof: run a relocate AND a
  track-remove through `SafeWriteITL` on a ZFS clone, byte-compare every untouched track's
  record, assert ZERO changes. Then P1 (partitioned count-refresh, re-derive PID sample) /
  P2 (relocate-only sync-cycle op + oracle = MVP end).

- [ ] **iTunes 2-way-sync P2 — relocate-only sync cycle (MVP end).** All prerequisites are
  merged: 4-state `LibrarySet` config (#2040), cleanup census → P3 no-op (#2041),
  cross-type + preservation proofs (#2042), relocate oracle `VerifyRelocateWrite` (#2043),
  P1 `RefreshLibraryIdentity`+`PartitionedTrackCount` (#2044), F7 guard scope
  `ContractConfig.AllowedWritebackRoot` (#2045). Compose the cycle: (1) read AO `.itl` +
  `RefreshLibraryIdentity` → ExpectedIdentity; (2) plan relocate from DB `book_file`
  locations vs `.itl` 0x0D (existing relocate op → `[]ITLLocationUpdate`, 0 adds/0 removes);
  (3) `SafeWriteITL` with `ContractConfig{AllowedWritebackRoot:<AO media root>,
  ExpectedIdentity:<refreshed>, ExpectedTrackCount: PartitionedTrackCount →
  planAudiobook+liveNonAudiobook, Force:false}` + `.bak` + bounded-delta capped at
  `len(LocationUpdates)`; (4) `VerifyRelocateWrite(before,after,relocatedPIDs)` BEFORE the
  atomic rename; (5) oracle OK → rename, else restore `.bak` + alert. Single-flight lock; never
  concurrent with manual relocate/pid-repair/cleanup. Wire `AllowedWritebackRoot` from the AO
  library's own media root (LibrarySet). See `docs/specs/2026-07-23-itunes-2way-p0-findings.md`
  (P0 status table) + `docs/specs/2026-07-23-itunes-2way-sync-system-design.md` §4–6.

- [ ] **`isAudiobookITL` under-classifies audiobooks (fail-safe, but fix carefully).**
  P0 cross-type census (§F5) found it misses `Audio Book`/`audio book` (it checks the
  substring `"audiobook"` with NO space — 705 tracks on prod) and every literary-genre
  audiobook (Science Fiction, Fantasy, Suspense, Comedy, …) — 3,436 AO-owned audiobooks
  total classified non-audiobook. Impact: for `GuardRebuildTarget` this is FAIL-SAFE
  (inflates the non-audiobook count → guard more likely to block), so no urgent safety bug.
  But: (a) never use `isAudiobookITL` as a relocate/cleanup targeting filter; (b) if fixing
  the heuristic (add the space variant, broaden genres), it LOWERS the non-audiobook count
  and could drop a real library below `GuardRebuildTarget`'s "looks real" threshold — so
  re-derive those thresholds in the SAME PR and re-test the guard. See
  `internal/itunes/library_shape.go:35` + `docs/specs/2026-07-23-itunes-2way-p0-findings.md` §F5.

- [ ] **🚧 P2 BLOCKER — location-form guard rejects the entire live AO library (F7).** The
  `location-form` safety guard (`internal/itunes/itl_safety_contract.go:562`) rejects any
  `SafeWriteITL` when a track's 0x0D/0x0B contains `.itunes-writeback/`. On the live AO
  library that is **82,976 tracks** — because the AO library physically lives at
  `W:\audiobook-organizer\.itunes-writeback\` so its iTunes media folder legitimately is
  `…\.itunes-writeback\iTunes Media\`. The guard was built to catch a staging path leaking
  into the hands-off Original library (damaged-4); in the hard-cutover design (iTunes pointed
  AT the AO library) the substring is correct and unavoidable. Result: the P2 relocate op
  **cannot write the library at all** (`Force` does not override location-form — only the
  bounded-delta guard). FIX (owner decision): (1, preferred) scope the staging-marker check to
  the write TARGET using the P0 4-state `LibrarySet` mode facts — reject `.itunes-writeback/`
  only when writing the Original library, or only when the path's `.itunes-writeback/` root
  differs from the AO library's own root; or (2) physically move the AO library + media out
  from under a `.itunes-writeback/` dir (invasive). Reproduced by
  `TestITLRelocateContractStatus` (env-gated). See
  `docs/specs/2026-07-23-itunes-2way-p0-findings.md` §F7.

- [ ] **iTunes 2-way-sync — continuation (P3 redefine + reverse sync + footgun audit).**
  P1 relocate is applied+verified on prod (6,414). Still open, per
  `docs/plans/2026-07-23-itunes-2way-sync-continuation.md`: (1) redefine the P3
  merged-track removal to provable-duplicates-only (version_group/MergedIntoBookID
  linkage) — current `IsPrimaryVersion==false` criterion is UNSAFE (would delete real
  chapter files); explain the 4,298 shared-PID oddity. (2) Build the reverse sync
  (iTunes → writeback → AO) so media added/played/playlisted in iTunes syncs back once
  it's used full-time; decide the source-of-truth model + import from the writeback
  library not `books/itunes/`. (3) Guard/deprecate the destructive `/rebuild` +
  `/rebuild-full` against the now-real library; define the adopt-base steady-state.
  Dry-run + sample + owner sign-off before any destructive apply.

- [ ] **iTunes book_file PID uniqueness — apply the backfill repair (gated).** Forward
  invariant shipped (`CreateBookFile` transfers a duplicate PID to the new row) + census
  (`GET /itunes/pid-integrity`) + repair (`POST /itunes/pid-repair`) endpoints built and
  tested. Prod census: 8,987 duplicate PIDs (8,762 same-file, 225 diff-file, 94 on multiple
  primaries). Repair dry-run auto-resolves 8,984 (3 ambiguous left for review), clears 9,050
  redundant PID copies, DB-field-only (no file/row deletion). REMAINING: deploy → run
  `pid-repair?dry_run=true` on prod to confirm → owner runs the apply with `!` → re-run the
  census to confirm 0 duplicates → review the 3 ambiguous groups by hand. See
  `docs/specs/2026-07-23-itunes-2way-sync-continuation-findings.md` §1.5b.

- [ ] **iTunes 2-way sync writeback (edit-in-place, preserve play-state).** The deployed
  `rebuild-full` writeback regenerates the library (12,193 tracks / 14 playlists) vs the real
  97,782 / 356 — valid but lossy (no play counts, ratings, playback bookmarks, music/podcasts,
  user playlists). Redirect to surgical edit-in-place via `UpdateITLLocations`, scope-gated by
  `IsAudiobook`, per `docs/specs/2026-07-22-itunes-2way-sync-writeback-design.md` (draft PR #2033).
  Phased P0–P4; resolve §8 open decisions (PID persistence, bookmark mhod, read-back scope, base
  selection, cadence) before implementation. Discard the current 2 MB prototype library.

The 2026-H1 TODO history (3,220 lines) is frozen verbatim at
[`docs/archive/todo-2026-H1.md`](docs/archive/todo-2026-H1.md).
Source anchors below (`H1:NNN`) cite line numbers of the **original** TODO.md;
in the frozen archive copy add 6 (banner block) to each number.

This file lists the 49 items confirmed ACTIVE by the 2026-07-17 docs audit, plus
the 2026-07-17 multi-discipline review-findings backlog (crash-recovery record,
last section).
Everything shipped or obsolete was dropped, including every stale 380K/384K/387K
dedup-candidate figure — the real backlog is **15,269 pending / 9,074
exact-pending** (see [`docs/dedup/STATUS.md`](docs/dedup/STATUS.md)).
Corrections applied per the audit: review-queue **PR-B2 is MERGED (#1953)**;
INIT completion is **~46/50 briefs** (not "35 remaining"); the managed
tool-lifecycle **IS built** (`internal/tools/*`, `/api/v1/tools`, Settings → Tools).

Companion docs:
- Run-on-prod queue: [`docs/operations/pending-prod-actions.md`](docs/operations/pending-prod-actions.md)
- Human-decision queue: [`docs/plans/DECISIONS-PENDING.md`](docs/plans/DECISIONS-PENDING.md)
- Dedup state: [`docs/dedup/STATUS.md`](docs/dedup/STATUS.md)
- 2026-07-17 multi-discipline findings: [`docs/audits/2026-07-17-multi-discipline-review.md`](docs/audits/2026-07-17-multi-discipline-review.md)

## Dedup (10)

1. ~~**CONS-10 / INIT-2 T6 — prod drain/triage of the exact-candidate backlog**~~ — ✅ **RAN ON
   PROD 2026-07-18**, verified 2026-08-12 from the prod journal, not from a status doc:
   `dedup triage: complete scanned=10319 purgeable=7891 keep=278 review=2150 apply=true
   dismissed=7891 dismiss_errors=0`, `outcome=completed duration_ms=3860`
   (op `01KXV22ZJ6QWWZ1SF1FZGXBC82`, 12:48:54–12:48:58 EDT). This entry previously read
   "code merged, run NOT executed" and was **wrong** — see
   [`docs/dedup/STATUS.md`](docs/dedup/STATUS.md).
   **Open follow-up instead:** the backlog has re-accumulated. Post-run exact-pending was
   **1,311**; measured **5,947** on 2026-08-12 (dismissed 8,258). The drain worked; whatever
   produces the junk candidates was never stopped, so this needs a *source* fix, not another
   drain. Filed as a `todo.d` fragment rather than added here directly (new tasks are
   add-only via fragments).
2. **PH-2 — run `maintenance.dedup-exact-triage` on prod + review populations; PH-2b
   per-population purge wave** (H1:916) — never blanket-purge; four residual
   populations (see `docs/dedup/STATUS.md`). **Apply path now exists** (T03-BUILD):
   `maintenance.dedup-exact-triage {"apply":true}` dismisses purgeable classes
   (stub/title_leak) via `UpdateCandidateStatus(id, "dismissed")` — dry-run
   (`apply=false`, the default) is unchanged report-only. Unblocks brief T03's
   sandbox purge wave.
3. **REVIEW-band candidate producer for the review queue** (H1:35) — B2 fast-follow;
   no commit yet.
4. **Flip `review_apply_enabled` ON in prod** — apply path merged (#1953) but default
   OFF (6f2f7ce0); gated human decision (see DECISIONS-PENDING).
5. **C8 — auto-file issues per `not_dup` cluster** (H1:1332; INIT-10 T5) — deferred.
6. **INIT-1 T05 follow-up — per-kind confidence field in `DedupSignalConfig`** (H1:250)
   — **persistence scaffolding DONE** (2026-07-18): `config.DedupSignalConfig.Confidence`
   + `unified.SetKindConfidenceOverrides` (mirrors `SetBandThresholds`) + `registry_wire.go`
   wiring, so a per-kind confidence bound now survives `UpdateConfig`/restart. **Still
   blocked**: `unified.ComposeScore` ignores `cfg.Signals[kind]` bounds entirely (reads
   `Signal.Confidence` verbatim), so the field has no effect on live scoring yet, and
   `dedup.calibrate-composite`'s Round 2 sweep still doesn't write it — decision needed
   on whether `ComposeScore` should clamp against it (see
   [`docs/plans/DECISIONS-PENDING.md`](docs/plans/DECISIONS-PENDING.md) row 10).
7. **Async breakdown-refresh for bulk/cluster dismiss** (H1:1877) — per-pair synchronous
   refresh may need an async variant at scale (latency note).
8. **Omnibus detection + dedup** — spec-only
   ([`docs/superpowers/specs/`](docs/superpowers/specs/) 2026-05-31); not started.
9. ~~**Regression tests for the 2 untested deluge hydrate sites**~~ (H1:568) — optional.
   — ✅ DONE 2026-08-22 (PR #2705, TASK-141).
10. **Hide system-sourced tags from the Browse-by-Tag cloud** (H1:433) — UX preference,
    not a bug.

## Identification / metadata (5)

11. **AI-enrichment tier for the ambiguous regroup pile** (H1:35) — B2 fast-follow;
    blocked on local Ollama capacity.
12. **Cover recovery fast-follow** (H1:35) — B2 fast-follow.
13. **Community audiobook fingerprint index (INIT-8)** — spec-only
    ([spec](docs/specs/2026-07-10-community-fingerprint-index-design.md));
    STOP-FOR-HUMAN brainstorm/review session required.
14. **Description fetch campaign — ~29,083 books without descriptions** (H1:790).
15. **LLM/embeddings backend-mode toggle** (extracted from the archived 2026-07-02
    status doc) — config enum + FE selector (disable-all / OpenAI-only / local-only /
    OpenAI+local-fallback) + model-download prompt; local target qwen2.5:7b-instruct
    on the GPU box. Status unverified — check before building.

## Pipeline (8)

16. ~~**Library heavy-filter + non-title-sort returns 0 books** (H1:301-330)~~ —
    **FIXED** (fix/library-filter-zero-results): root cause was `GetAudiobooks`
    re-applying an already-pushed-down filter against BookSummary→Book
    projections missing fields like Language/Genre/FingerprintStatus; the
    re-check silently dropped every row. Now skips the redundant re-filter and
    sort+paginates the pushdown result directly. Left a new backlog item (16b)
    for the separately-discovered author/series-by-name FieldFilter gap found
    during this investigation.
16b. ~~**Advanced-search `FieldFilters` on `Field: "author"`/`"series"` always
    return 0 books** (found during #16's investigation)~~ — **FIXED**
    (fix/fieldfilter-author-series-hydration): confirmed root cause —
    `fieldMatchesValue` (`internal/audiobooks/service_filtering.go:274`) reads
    `book.Author.Name`/`book.Series.Name`, but per `database.Book`'s own doc
    comment those are "Related objects (populated via joins, not stored in
    DB)" — the memdb-resident `*Book` never carries them (only
    AuthorID/SeriesID), and even the Pebble `GetBookByID` raw-JSON fallback
    doesn't hydrate them either, so every author/series FieldFilter compared
    against `""` and rejected every row. Fix: `buildAuthorSeriesNameMaps`
    fetches all authors/series once per query (cheap — small, fully in-memory
    collections, same `GetAllAuthors`/`GetAllSeries` accessor
    `author_series.go`'s `ListSeriesWithCounts` already uses) and
    `hydrateAuthorSeriesNames` populates a per-book copy's Author/Series from
    those maps before `fieldMatchesValue` runs, at the single choke point
    (`matchesFieldFiltersWithStrippedFallback`) both the memdb pushdown
    predicate and the mock/non-pushdown post-filter path go through — no
    per-book store call. `CountAudiobooksFiltered` shares the same predicate
    builder so the paginated total is fixed too.
17. **iTunes path-heal residuals** (H1:899-906) — 3,720 ambiguous / 5,349 not-found /
    4,734 doubled-path records still unresolved.
18. **AP-1b — physically co-locate survivor's files after Combine** (H1:936) — inside
    RootDir only.
19. **AP-3 duration-reextract ~721-book tail** (H1:949) — re-enqueue apply
    (see pending-prod-actions).
20. ~~**AP-3b — consolidate the 3 duration extractors into one** (H1:954).~~ DONE —
    `internal/audioutil.ProbeDurationSeconds` is now the single ffprobe
    implementation shared by `internal/mediainfo`, `internal/fingerprint`, and
    `internal/transcode`; each call site keeps its own unit/error contract.
21. **CONS-18 Part 2 — file-tag duration write-back** (H1:1019; spec 2026-06-19 DRAFT)
    — config-gated; deferred until dedup re-scope settles.
22. **Torrent relocation INIT-5 T2–T7** ([plan](docs/plans/2026-07-10-torrent-relocation.md))
    — T1 shipped (18570a39); T2 = human-gated Deluge spike blocks T3–T7.
23. **Fingerprint UI verifications ×2** (H1:1383-1384) — [hold] verify the 14K
    false-positive purge is visible in dedup UI; book-sig coverage % renders.
    **PARTIAL 2026-08-22 (PR #2708, TASK-172):** half 2 done — a frontend test now
    asserts the book-sig coverage % badge renders (`EmbeddingDedupTab`, not
    `DedupEmbeddingTab` as the brief called it). Half 1 — the 14K false-positive purge
    being visible in the dedup UI — is a **live-prod** verification and is still open.
    Item stays open until that runs.

## Workflow / ops (4)

24. **Workflow system WF-0/2/3/4/5 (INIT-6)** (H1:1128-1133;
    [plan](docs/plans/2026-07-10-workflow-system.md)) — STOP-FOR-HUMAN spec review;
    WF-6 closed NOT-DOING. Implementation plan (owner-approved 2026-07-18, PR #1935):
    [`docs/plans/2026-07-13-workflow-system-implementation-plan.md`](docs/plans/2026-07-13-workflow-system-implementation-plan.md)
    — grounds the spec against HEAD; recommends **build WF-2, defer WF-3/WF-4/WF-5**
    (INIT-1 T5+T6 shipped, so WF-3's headline use case exists without it; the spec's
    completeness gate is blind to the nested-config `label_refinement` family).
25. **PD-1 — subprocess isolation via parent-RPC bridge + MDA3 `Isolate:false` revert**
    (H1:1554-1561, 1435-1438; [spec](docs/specs/subprocess-isolation-rpc.md)) — [hold].
26. **INTERNAL-SERVER-PKG-STALL structural decision** (H1:849-877) — leak fixed;
    residual needs an owner decision: raise timeout / split package / migrate ~60 call
    sites to `newTestServer` (see DECISIONS-PENDING).
27. **Responses-API migration AI-RESP-A/B/E/F (INIT-7)** (H1:2596-2603) — [hold,
    do-not-start-without-greenlight]. AI-RESP-C/D closed.

## Logging / verification / security-ops (5)

28. **SLOG-PROD-VERIFY** (H1:2038; [runbook](docs/operations/slog-prod-verify.md)) —
    live prod smoke test of the op-activity chain.
29. **SLOG-W13 residual ~1,346 raw slog calls** (H1:2037) — remaining calls enumerated
    out-of-scope (no-ctx funcs, lifecycle, background); candidate to CLOSE with a
    scope note.
30. **SEC-AUDIT-11 — CodeQL bulk-dismissal rationales** (H1:2267) — GitHub-console
    action.
31. **PD-3 — post-deploy prod verification checklist** (H1:1568-1574;
    [checklist](docs/pd3-prod-verification.md)) — checklist exists, never filled in.
32. **I1 + I6 — prod pprof verification** (H1:1515, 1538) — measure chromem-lazy
    effect + heap re-audit; measurement only.

## Infra (5)

37. ~~**CPU busy-loop: `CountPrimaryBooks` full-scan on the 5s metrics ticker**~~ — ✅ DONE
    (2026-07-18): the server burned ~2 cores continuously while idle because
    `CountPrimaryBooks` (`internal/database/pebble_store.go`) full-scans + `json.Unmarshal`s
    all ~44K books (~5.6s) and the 5s status ticker
    (`internal/server/server_lifecycle.go`) called it every tick, running scans
    back-to-back (presented as ~189% CPU with only `sweep tick waiting_count=0` logs; also
    made `/api/v1/health` ~5.6s). Fixed with a 30s in-memory TTL cache + recompute gate on
    `CountPrimaryBooks` (regression test `TestPebbleCountPrimaryBooksTTLCache`). Diagnosed
    while health-checking the (now torn-down) dedup sandbox. — closed 2026-08-21: re-verified
    against HEAD — `primaryCountCacheTTL = 30 * time.Second` (`internal/database/pebble_store.go:188`)
    and `TestPebbleCountPrimaryBooksTTLCache` (`internal/database/pebble_store_test.go:706`) both present.

33. **REPO-SIZE-1 decision** ([plan](docs/plans/2026-07-10-repo-size-history-rewrite-plan.md),
    [package](docs/plans/2026-07-12-repo-size-targeted-purge-package.md)) —
    STOP-FOR-HUMAN; plan recommends Option (d) forward-only + GitHub Support gc.
34. ~~**Execution-manifest human gates**~~
    ([manifest](docs/plans/2026-07-10-execution-manifest.md)) — the residual gated
    tasks: INIT-5 T2 spike sign-off, INIT-6 spec review, INIT-7 greenlight, INIT-8
    review, REPO-SIZE-1. — ✅ DONE 2026-08-22 (PR #2715, TASK-058): all five gates
    verified settled at HEAD against two independent checked-in artifacts
    (`docs/plans/DECISIONS-PENDING.md`'s recorded-2026-08-21 AskUserQuestion table, and
    `SCOUT-INSTRUCTIONS.md:10-14`), and the manifest updated to match.
    **Two corrections the verification forced, both worth keeping:** INIT-7 is recorded
    as *HOLD CONFIRMED*, **not** PARKED — the owner answered "KEEP ON HOLD", and the
    scout package's `ON HOLD → "parked"` mapping is its own briefing convention, not the
    owner's word. And INIT-6's PR #1935 is *merged*, but it was the plan doc "for owner
    sign-off" — the STOP-FOR-HUMAN spec review itself was never held, so a bare "merged"
    would have read as approved.
    Follow-up filed separately: `DECISIONS-PENDING.md` still lists rows 1–5 in its **open**
    table and still says #1935 "stays open", both wrong at HEAD.
35. **Consultancy wave 4+ residuals** ([roadmap](docs/consultancy/00-ROADMAP.md)) —
    unverified; needs a close-out sweep against shipped work.
36. ~~**Op-progress Prometheus metric (T12 follow-up)**~~ — ✅ DONE (PR #2014,
    2026-07-18): added `audiobook_organizer_op_items_processed{op_id,op_type}`
    + companion `audiobook_organizer_op_items_total{op_id,op_type}` gauges
    (`internal/metrics/metrics.go`, `SetOpProgress`/`ClearOpProgress`), set on
    every `dbReporter.UpdateProgress` call
    (`internal/operations/registry/reporter_db.go`) and deleted on every
    terminal transition via `registry.publishOpTerminal`
    (`internal/operations/registry/registry.go`) so stale op_ids never
    accumulate. Uncommented + finalized the "op stalled" alert in
    `deploy/prometheus/alert-rules.yml` (`AudiobookOrganizerOpStalled`,
    `rate(audiobook_organizer_op_items_processed[30m]) == 0` for 30m —
    existence of the series itself proxies "op is active" since it's deleted
    at terminal, so no separate `op_active` gauge was needed). Closes the
    observability gap behind the 3+ hour `dedup.full-scan` hang and the 9hr
    Pebble write-stall freeze — both were only noticed by a human watching
    the UI. — closed 2026-08-21: re-verified against HEAD — `SetOpProgress`/`ClearOpProgress`
    (`internal/metrics/metrics.go:216,227`) and the `AudiobookOrganizerOpStalled` alert
    (`deploy/prometheus/alert-rules.yml:147`) both present.

## UX (4)

36. **1.16 — resizable/sortable columns** (H1:2750) — remaining: dedup results,
    activity log, iTunes write-back preview, metadata review.
37. **1.17 — product rename/branding sweep** (H1:2751) — blocked on name decision.
38. **3.8 Plex-style media server API; 3.9 LLM series detection; 3.10 AI cover art**
    (H1:2772-2774) — all [hold].
39. **Fleet-tasks reconciliation** — real residuals = 030/031/036 (≡ 4.10/4.8/1.16);
    032–035 are stale-shipped and need closing in the fleet tracker.

## Other / close-out (10)

40. **4.8 — Store ISP sweep** (H1:2787) — **RE-SCOPED 2026-07-18; the "~38-file + 18
    noop" count below was pre-reorg and is WRONG.** Re-audit found `database.Store` is a
    field/param in **~151 prod + 35 test files** (a package reorg since the April plan
    split `internal/server` into `internal/audiobooks|metafetch|merge|organizer|
    maintenance/jobs|server/handlers/*`, obsoleting the file lists in
    `docs/archive/superpowers/plans/2026-04-17-store-iface-sweep.md` — whose COMPLETE
    stamp reflected a deliberate "diminishing returns on the hubs" stop that STILL holds
    post-reorg). **Decision 2026-07-18: do the DI-seams + shallow-consumer subset only**
    (narrow the 8 `internal/server/handlers/*/interfaces.go` + `internal/server/
    interfaces.go`, plus genuinely-shallow post-April consumers; leave hubs/bootstrap/
    wiring/decorators wide with justification comments) — NOT the full 151-file sweep.
    Type-only change (no runtime/data impact); existing `mocks.Store` already satisfies
    every sub-interface so no wave triggers a mockery regen. Old sweep tooling
    (`scripts/{check_store_noops,narrow_struct_services,apply_narrowing}.py`) survives but
    its hardcoded file lists must be regenerated. ~~Not started~~ — **PARTIALLY DONE
    2026-08-17 (PR #2534): the `internal/maintenance/jobs` consumer is narrowed.**
    `MaintenanceJob.Run` now takes a 12-sub-interface `maintenance.JobStore` (187 methods)
    instead of `database.Store` (40 sub-interfaces, 398 methods) — a 53.0% cut across all
    37 jobs, with zero `Run` body changes and 8 job files dropping their
    `internal/database` import entirely. The union was measured from call sites (a lower
    bound) and then PROVEN by the type checker: the build only passes if every job and
    every helper it reaches fits inside the 12. Also confirmed the note above: no mockery
    regen was triggered, and exactly **1** test fake needed editing.
    ⚠️ Correcting an overclaim carried in earlier notes: **this does NOT delete
    `database.MockStore`** — it is imported far beyond this package and satisfies
    `JobStore` too, so the 14 job tests that build one still compile unchanged.
    **Still not started:** the 8 `internal/server/handlers/*/interfaces.go` +
    `internal/server/interfaces.go` DI seams, which remain the bulk of this item.
41. ~~**4.10 — MergeService mock-store unit tests** (H1:2789)~~ — DONE: `internal/merge`
    coverage 70.3%→96.6%. Added 34 tests across external-ID reassignment, ITL-removal
    enqueue, loser soft-delete, nil/empty-override wipe-safety, version-group integrity
    (incl. a real bug found: `MergeBooks` didn't de-dupe `bookIDs`, so a caller passing
    the primary twice — the exact class PR #2007 patched only at one caller — silently
    demoted the winner to non-primary with no soft-delete; fixed defensively in
    `Service.MergeBooks` itself), CombineBooks file-transfer/author-override error paths,
    and the merge-family serialization lock helpers.
42. ~~**2026-05-01 re-audit block close-out pass**~~ (H1:3137-3177) — closed 2026-08-22;
    every sub-item re-verified again 2026-08-22 at HEAD `95d6db6ee`.
    TEST-2 resolved — `TestStoreAdditionalCoverageSQLite` is PebbleStore-backed via
    `setupTestDB` (no longer SQLite) **and green**:
    `go test ./internal/database/ -run TestStoreAdditionalCoverageSQLite -count=1` → `ok`.
    The original finding was a *failure*, so "the test exists" was never the check that
    settles it; it has now actually been run.
    CTX-4 resolved (10 hits — every `ActivityStore` impl takes `ctx` on
    `Summarize`/`CompactByDay`). LOG-5 resolved (0 `fmt.Printf`/`log.Printf` in
    database/playlist/organizer; `sqlite_store.go` gone). R-9 moot — its two stale
    `// TODO: Implement in N1-2` comments lived in `sqlite_store.go`, which no longer
    exists (0 repo-wide hits). R-10 resolved (0 capitalized error strings across the 6
    metadata providers). DEP-1a-d resolved: `Book.ITunesPath` has no production reader
    (measured by `gopls references`, not a name grep — the receiver-name grep in the scout
    package cannot see `b.`/`c.` call sites; the 5 real references are `bookcore.go:207,321`
    plus 3 test-only writes in `importer_mock_test.go`).
    **Correction 1 — DEAD-1 is 3-of-4, not resolved.** The three symbols the earlier
    close-out grepped for are gone (`legacySaveConfigToDatabase_REMOVED` /
    `bookTagKeyspace` / `bookSummarySelectColumnsQualified`, 0 hits) and both SA4006 pairs
    are clean (`staticcheck -checks SA4006 ./...` → 0 findings). But DEAD-1 named a
    *fourth* symbol, and it survives: `linkAsVersion`
    (`internal/itunes/service/importer.go:1780`; listed as R-5/DEAD-1 evidence at
    `docs/archive/codebase-evaluation.md:107`). `gopls references` gives it exactly 2
    references and **both are tests** (`importer_error_paths_test.go:531,562`) — dead
    production code kept alive by its own coverage, which is also why `staticcheck` U1000
    stays silent on it. Spun out below as its own item rather than swept up here. The
    earlier note checked 3 of the 4 named symbols and read the zero hits as the whole
    answer.
    **Correction 2 — DEP-1e is not moot.** An earlier note called it "moot (post-SQLite
    removal)"; it is not. The `books.itunes_path` *column* half is genuinely moot (no SQL
    remains), but `Book.ITunesPath` is still declared (`internal/database/store.go:220`) and
    still round-tripped (`bookcore.go:207,321`) at HEAD. Spun out as its own item via a
    `todo.d/` fragment rather than closed.
    **Correction 3 — PERF-1 was superseded, not done,** and the distinction matters. The
    finding asked to *paginate* 20+ unbounded `GetAllBooks(0,0)` calls; `19e129d48` moved
    eleven more whole-library ops **to** the unbounded form on purpose, to stop fixed-limit
    truncation. There are now 58 unbounded whole-library call sites, up from ~20 — the
    prescribed direction was rejected, not executed. What actually retired the OOM risk is
    the type change, not pagination: the full-`Book` `GetAllBooks` method **no longer exists
    in production at all** — its 32 remaining non-test occurrences are every one of them
    comments, and `internal/database/mock_store.go:39` records why ("GetAllBooks was removed
    from the interface in STOREFID W5z"); the only surviving declarations are three stale test
    mocks. Every whole-library read now goes through the Core-typed `GetAllBooksCore`, ~50x
    lighter per `internal/database/pebble_store.go:703`. Residual memory exposure is reduced,
    not eliminated; if it ever bites, re-open it as a new finding rather than reviving this one.
43. **WaitForWarmup hazard note** (H1:3118) — latent create-then-read-memdb test
    hazard; document or fix.
44. **GFO-4 — graceful-file-ops sub-op phase tracking** — last open graceful-file-ops
    item.
45. **Performance items #1/#2/#6** (2026-04-14 set) — still open.
46. ~~**Duration/filesize aggregation**~~ — Book fields show snapshots instead of sums;
    likely stale (F5-T026 shipped) — verify then close. — closed 2026-08-21: verified —
    `git log --oneline -- internal/database/pebble_store_book_aggregates.go` shows
    `a25b802e feat(database): recompute book duration/filesize aggregates from BookFiles (fable5 T026)`;
    `RecomputeBookAggregates` sums Duration/FileSize from BookFiles and is wired at every
    BookFile create/update/delete chokepoint via `notifyBookFileChange`
    (`internal/database/pebble_store_bookfiles.go:280,391,870,919,1091`), plus a dedicated
    backfill job (`internal/maintenance/jobs/recompute_book_aggregates.go`).
    - ~~**46b. `/audiobooks` LIST endpoint mis-serializes `duration`** (found
      2026-07-19)~~ **DONE 2026-08-03 (#2125).** The reported symptom — list says
      `duration: 4` where the detail endpoint says `4680` — was the arithmetic itself:
      `4680 / 1000` truncates to `4`. `service_filtering.go:923` divided every
      duration by 1000 unconditionally while aggregating, so the rows it corrupted
      were the *correct* second-valued ones. Now routed through
      `database.NormalizeDurationSec`, which classifies per row from the implied
      bitrate. Same fix applied to `handlers/versions.go` and
      `handlers/audiobooks/handler_files.go` (×2). Far from low-severity: it affected
      25,938 books.
47. **Library centralization backlog** — needs a brainstorming session; future work.
48. **Transcription quality filter** — ~40% of transcripts low-quality/unparsed; list
    endpoint omits transcription fields.
49. **iTunes heal Layer-6 re-trigger** (H1:897) — re-run after path-heal residuals
    shrink (see pending-prod-actions).

## Dedup + review consolidation (3) — 2026-07-18 owner request

Owner directive (2026-07-18) while reviewing the live dedup/review experience: the
current dedup page is too heavy, the review UI is poor, and obvious near-identical
duplicates (same file, differing by a character or two) should be auto-confirmed by
audio fingerprint. Investigate read-only first (dedup page vs review page component
boundaries; current review-queue flow) and present a plan before building — this is
frontend + backend feature work, not a mechanical change.

> **2026-07-19 — item 50 is now folded into a full design spec:**
> [`docs/specs/2026-07-19-fingerprint-driven-reconciliation-design.md`](specs/2026-07-19-fingerprint-driven-reconciliation-design.md)
> (DRAFT) — fingerprint-driven library reconciliation via a 3-signal (fingerprint /
> source-folder ground-truth / Whisper) convergence loop; use-cases = shattered-book
> reassembly, dedup-on-import, iTunes decommission, near-dupe confirm. Verified live:
> 94% fp coverage, the 39-way *Aces Abroad* shatter. Items 51–52 (review UX +
> dedup-page consolidation) remain as scoped below.

50. **Fingerprint-confirmed dedup + shattered-book reassembly against the original
    source** (GROUNDED 2026-07-19 via read-only prod verification). Two related tests,
    added as signals on existing candidates — not a new pipeline:
    - **(a) Acoustic confirm** — where both sides of a candidate pair are fingerprinted,
      use `WholeFileSimilarity` closeness as a *confirming* signal to auto-promote the
      "same file, one extra character" title-leak near-dupes to auto-merge; distinct
      pairs fall back to today's scoring. Per-file acoustic signals already feed scoring
      (`exact_acoustid`/`lsh_acoustid`); this extends them + strengthens the
      `auto_resolve` gate (behind the existing `AutoResolveEnabled` kill-switch).
    - **(b) Shattered-book reassembly** — for a book split into many fragments (author-
      first shards of a multi-author anthology), match the fragments' per-file
      fingerprint **set** against the assembled ORIGINAL source folder (set containment
      `fragments ⊆ source_folder`) via the existing `fpidx` LSH index → the source
      folder whose file-set contains them identifies the true whole book. Metadata
      (album/iTunes-XML/PID/version-group) is the primary regroup key; the fingerprint-
      set match is the safety confirmation that makes the auto-regroup safe.
    - **Design constraints (owner, 2026-07-19):** dedup AGAINST the original source as the
      identity reference, but keep the organized (primary) copy canonical; reflink new
      files on import. **NEVER mutate the active iTunes tree** — read-only at most (see
      [[feedback_itunes_active_library_hands_off]]).
    - **VERIFIED (prod, read-only, 2026-07-19):** file-level raw-fingerprint coverage is
      **94%** (296,010 / 315,013 files; zero-duration count == 0, so the old Seg0
      over-count worry is moot — the "~65%" figure was stale/pair-level, NOT a current
      file-level blocker). **PREREQUISITE / the one real gap:** the assembled source-
      download root is NOT a configured scan path, so its folders are on disk but not in
      the DB (title search for a known source book = 0 hits). **Step 1 = scan + fpcalc-
      fingerprint the source root as a read-only REFERENCE corpus** (cheap — reflinks;
      distinct root from iTunes so the guardrail holds) and index into `fpidx`; only then
      does (b) have ground truth to match against. See
      [[project_dedup_assembled_source_ground_truth]].
    - Cross-ref: `internal/dedup/engine.go`, `internal/dedup/unified/auto_resolve.go`,
      `internal/dedup/split_book_detector.go`, `internal/fingerprint/`,
      `internal/plugins/acoustid/`.
51. **Overhaul the review interface ("make it not suck")** — the review page UX is a
    pain point. Needs a concrete redesign spec: read-only audit of the current review
    page (what it shows today, interaction friction, per-hold actions) → propose
    redesign. Ties to the review-queue track (A1/A2/B1 shipped; B2 apply path merged
    #1953, default OFF — see [[project_review_queue_regroup]]). Prereq for item 52.
52. **Consolidate the dedup page into the review page** — slim the dedup page down to
    run-control only (start/stop dedup runs + run status/progress); move ALL candidate
    and result display + review actions into the review page so there is one place to
    review everything. Depends on item 51 (the review UI must be good enough to absorb
    the dedup results first). Investigate current dedup-page vs review-page component
    boundaries before committing to a plan.

## 2026-07-17 review findings — remaining (post-fix-wave)

The 2026-07-17 multi-discipline review produced 66 findings
([`docs/audits/2026-07-17-multi-discipline-review.md`](docs/audits/2026-07-17-multi-discipline-review.md)).
The same-day fix wave closed most of them across PRs #1972–#1986 — see
[`docs/status/2026-07-17-error-correction-session.md`](docs/status/2026-07-17-error-correction-session.md)
for the PR↔finding map and the sandbox verification results. **Remaining work is
specified as weak-model-proof task briefs T01–T13 in
[`docs/agent-tasks/error-correction-2026-07/TASKS.md`](docs/agent-tasks/error-correction-2026-07/TASKS.md)**
— work from the briefs, tick lines here as they land.

### Fixed (2026-07-17 → 07-18 waves — do not re-fix)

**2026-07-17 wave:** F1 (#1973) · F2 (#1976) · F3/F4/F5/C7 (#1977) ·
title-repair op (#1978) · R-2/C-3/C-2/C-4/C-5/C-1 (#1980) · C1/C6/C4/C5/C3 (#1981) ·
breakdown-backfill op + title-leak relax (#1982) · devops IP-scrub/template/hook/
smoke (#1983) · DL-5/C-6/C-7/M5/M6 (#1984) · R-4/H5/R-5/H6/DL-4/M8 (#1985) ·
DL-1/DL-2/DL-3/M4 (#1986).

**2026-07-18 coordination wave (T05–T12):** R-1 (T06) + R-3/R-7/P-2 (T08) (#2002) ·
devops follow-ups T12 (#2001) · F7/R-9/R-8 (T11) (#2004) · R-6 orphan-VG pool (T07) (#2003) ·
dep-fail SSE publisher (T06-fu) (#2005) · C2/H7 reporter threading (T09) (#2006) ·
F6 legacy book-merge rerouted off hard-delete → soft-delete + external-ID reassignment
+ ITL removal (T10) (#2007) · triage purge-apply op (T03-BUILD) (#2008) ·
H1/H2/H3/H4/H8/H9/M1/M2/M3/M7 logging batch (T05) (#2010).

### Remaining — execution state (briefs)

- [x] **T01** — organizer data-loss fixes landed (#1986)
- [x] **T02** — **sandbox** triage measured: purgeable **7,878** (title_leak) / genuine 278 /
      fragment 392 / unknown 1,756 **of 10,304** (was purgeable=1, unknown=9,950 pre-work —
      the title-repair → breakdown-backfill → relaxed-triage chain is proven). Formal
      doc recording folded into T13.
      > ⚠️ **7,878 is the SANDBOX number. Prod's is 7,891 of 10,319.** These are two
      > populations, not a drift — the sandbox replica had 15 fewer candidates. A 2026-08-11
      > docs audit mistakenly reported them as a contradiction; they are both correct.
- [ ] **T03** — **sandbox** purge wave: `maintenance.dedup-exact-triage {"apply":true}` (dismiss
      ~7,878 purgeable, op merged in #2008) → purge-stale → full-scan → measure vs 9,074
      baseline. Needs sandbox redeploy with current main first. NOT yet run **on the sandbox**
      — note prod (T04) went ahead and ran, so this is now a validation-parity gap, not a
      blocker.
- [x] **T04** — ✅ **prod deploy + dry-run + apply ALL DONE 2026-07-18.** Verified 2026-08-12
      from the prod journal: deploy `v0.217.8-rc.80-2-g0b474707`, then
      `maintenance.dedup-exact-triage` with `apply=true` → `dismissed=7891 dismiss_errors=0`,
      `outcome=completed`. This box previously read "nothing deployed since 2026-07-17" and
      was **wrong**.
- [x] **T05** — logging H/M batch: H1 H2 H3 H4 H8 H9 M1 M2 M3 M7 (#2010)
- [x] **T06** — R-1: `op.terminal` SSE backend publisher (#2002) + dep-fail publisher (#2005)
- [x] **T07** — R-6: AssignOrphanVGs worker pool + VG clobber guard (#2003)
- [x] **T08** — R-3 (reporter logBuf cap) · R-7 (dead scan-checkpoint deleted) · P-2 (RunItems completion counter) (#2002)
- [x] **T09** — C2 (remux/transcode reporter threading + fail-on-error) · H7 (external-id backfill) (#2006)
- [x] **T10** — F6: legacy book-merge rerouted off hard-delete to soft-delete + external-ID reassignment + ITL removal (#2007)
- [x] **T11** — F7 (quarantine → RunItems) · R-9 (path_repair pool + 3 concurrency hazards) · R-8 (unknown-duration group guard) (#2004)
- [x] **T12** — devops: 8 IP-scrub scripts · op-stall alert (commented; metric TBD, Infra #36) · coverage floor on PR gate · systemd dedupe · credential entropy (#2001)
- [ ] **T13** — docs truth-up with measured sandbox/prod numbers (dedup/STATUS.md, pending-prod-actions.md, exec summary) — in progress
      **PARTIAL 2026-08-22 (PR #2712, TASK-060) — box stays open by design, T03 has not run.**
      Found `docs/dedup/STATUS.md`'s "Sandbox validation results" table carrying
      **production's** numbers under a sandbox heading: its purge-apply row
      (`purgeable=7,891, keep=278, review=2,150` over `10,319`) is the prod journal line
      verbatim, while the sandbox purge-apply wave (T03) has never run. Proven by
      arithmetic — sandbox `7,878+278+392+1,756 = 10,304` and prod `7,891+278+2,150 = 10,319`
      each sum exactly to their own total, 15 apart (`keep=278` is identical in both and
      does *not* discriminate). Table split, prod rows moved under the prod section with the
      journal line as provenance, T03-dependent sandbox rows marked `PENDING` rather than
      back-filled. Also corrected the present-tense claim that ~1,311 is the review backlog:
      prod measured **5,947** on 2026-08-12 (~4.5× regrowth). No figure was re-measurable at
      HEAD (all are prod measurements reachable only via the live server), so each now carries
      an inline source and as-of date instead of being laundered forward as current. One
      question left explicitly unresolved in the doc rather than guessed: whether the sandbox
      baseline was truly 10,319 or had copied prod's number.
