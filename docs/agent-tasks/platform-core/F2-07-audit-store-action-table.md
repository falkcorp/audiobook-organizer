<!-- file: docs/agent-tasks/platform-core/F2-07-audit-store-action-table.md -->
<!-- version: 1.1.3 -->
<!-- guid: dbe44289-51c8-493f-bbe3-5173191430f8 -->
<!-- last-edited: 2026-11-05 -->

# F2-07 — `core/audit`: `audit.Store` built before P4b, and 05-rbac's normative action table (`core.storage.*` and `core.settings.update` included)

| Field | Value |
|---|---|
| ID | F2-07 |
| Release / flip | R2, foundation PRs (F2), **dark** |
| Wave | W24 |
| Tier | **T2** (Sonnet / high): a store with a fixed API and layout, a fixed action table, one async writer; every rule is in the spec |
| Depends on | OWNER-03 |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f2-07-audit
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
```

No `go work init` (CLAUDE.md). Run core tests from inside `core/`.

## Goal

Create the public package `github.com/falkcorp/mediacore/audit` (`core/audit/`) exactly as 05-rbac §3.5 declares it,
with the storage layout of §7.2, the volume control of §7.3, the redaction of §7.7 and the normative action table of
§7.1, and put the kernel side that builds `audit.Store` **before P4b** (05-rbac §5 item 2) in `core/kernel/audit.go`,
so F3's
`core.settings.update` and every cutover step's `core.storage.cutover_step` record have a writer (03 §3.3 `audit` row;
IMPL-3). The **boot call** that invokes `core/kernel/audit.go` from the boot sequence is F2-16's (the F2 wiring row,
one owner of `core/kernel/boot.go` and `kernel.go` in its wave). Dark: nothing records yet; serving (the query route,
auth's records) starts at F4.

## Spec to read first

05-rbac §3.5 (the Go block: copy it), §5 items 2 and 7 (when the store is built; async writer start; prune kind only
from the tasks flip), §7.1 (the action table, normative), §7.2 (keyspace `c/audit/`, keys, `Sequence`), §7.3 (async
queue: 4096, batches of 256 or 100 ms, coalescing within `core.audit.coalesce_window`, drop counted), §7.4 (no hash
chain), §7.5 (retention: the prune kind is not registered in this PR), §7.7 (redaction), §8 (error semantics), §9
(concurrency), §10 (config keys `core.audit.*`), §11 (metrics `<ns>_audit_*`), §12 (tests named below); 03 §3.6
(action grammar: `<ns>.<noun>.<verb>`, present tense); 05-storage §5S.3.2 (`Sequence`, `RecordIn` shares the caller's
batch, COR2-12).

## Base state: re-verify before editing

```bash
test ! -e core/audit && echo absent                                  # "absent", else see Idempotency
grep -rn 'type Ref struct' core/entity/                              # 1 hit (R1-09), else STOP
grep -rn 'type Sequence\b\|type Sequence ' core/storage/             # >=1 (R1-20), else STOP
grep -rn 'func (b \*Batch) ' core/storage/batch*.go | head -3         # Batch API present, else STOP
ls core/kernel/*.go | head                                            # the kernel files R1-13/R1-20 created
grep -rn 'c/storage/' core/kernel/*.go | head -3                      # how the kernel registers its own core keyspace (copy this pattern for c/audit/)
grep -rn '"core.audit' core/kernel/ | head -2                         # 0 or more: F2-09 (same wave) declares the core.audit section
```

If `storage.Sequence` does not exist, STOP: the allocation rule (persist the high-water mark before handing out IDs,
DATA-19) needs it and this PR must not invent it.

## Files

Create: `core/audit/audit.go` (types and interfaces of §3.5), `core/audit/store.go` (`Store`, `New`), `core/audit/actions.go`
(the action table), `core/audit/redact.go`, `core/audit/async.go`, tests. Create: `core/kernel/audit.go`.
Modify nothing else: F2-08, F2-09, F2-10 and F4-04 edit other `core/kernel/*.go` files in this wave, and the boot
call is F2-16's. Do not edit `core/kernel/boot.go`, `core/kernel/kernel.go` or any other shared kernel file.

## Code contract

Copy the Go block of 05-rbac §3.5 verbatim: `Outcome` (`Success`, `Failure`, `Denied`, `Error`), `Actor`, `Event`,
`Record`, `Logger` (`Record`, `RecordIn`, `Emit`), `Filter`, `Reader` (`Query`), `New(ctx, opts Options, deps Deps)
(*Store, error)`, `WithActor`, `ActorFrom`.

`Options` and `Deps` are not spelled out by the spec. Use exactly these fields (names coined here, from 05-rbac §7.3,
§10 and §11):

```go
type Options struct {
	AsyncBuffer     int           // core.audit.async_buffer, default 4096
	CoalesceWindow  time.Duration // core.audit.coalesce_window, default 1m
	DetailsMaxBytes int           // core.audit.details_max_bytes, default 4096
	BatchMax        int           // 256 (§7.3); not a config key
	BatchWait       time.Duration // 100ms (§7.3); not a config key
}

type Deps struct {
	Keyspace storage.Keyspace // c/audit/
	Seq      *storage.Sequence // or the type R1-20 exports; allocation persists the high-water mark first
	Meter    metric.Meter      // or core/metrics' Scope, whichever R1-10 exports for core packages
	Log      *slog.Logger
	Now      func() time.Time  // nil = time.Now
}
```

Add `func (s *Store) Start(ctx context.Context) error` and `func (s *Store) Stop(ctx context.Context) error`
(names coined here): `Start` launches the one async writer goroutine (05-rbac §5 item 7); `Stop` drains the queue,
bounded 5 s, and counts the rest as dropped.

Storage layout (§7.2): `rec/<seq:8B big-endian>` → record JSON; `idx/actor/<userID>/<seq>`,
`idx/action/<action>/<seq>`, `idx/target/<module>/<kind>/<id>/<seq>` (empty values). `Record.ID` is a ULID.
`Seq` unique and increasing in allocation order, gaps allowed. `RecordIn(ctx, b, e)` stages into the caller's batch
`b`, never a copy, so a discarded batch leaves no record (and a gap).

Action grammar: `^[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*\.[a-z][a-z0-9_]*$`. `Record`, `RecordIn` and `Emit` refuse an action
that fails it (`Emit` counts and drops).

The action table: `func CoreActions() []string` (name coined here) returns exactly these 61 actions of §7.1, sorted,
and `TestAuditAction_Grammar` checks every one:

```text
core.auth.login core.auth.logout core.auth.setup
core.bootstrap.redeem
core.identity.link core.identity.unlink core.identity.displace
core.templogin.mint core.templogin.consume
core.invite.create core.invite.accept
core.session.revoke
core.apikey.create core.apikey.revoke core.apikey.set_status core.apikey.require_confirmation
core.user.create core.user.disable core.user.enable core.user.delete core.user.reset_password core.user.set_roles
core.role.create core.role.update core.role.delete core.role.update_seed core.role.accept_seed core.role.review_admin
core.permission.deny
core.task.approve core.task.reject core.task.cancel
core.settings.update
core.storage.cutover_step core.storage.quarantine_purge core.storage.cutover_blocker_fix core.storage.cutover_blocker_delete core.storage.checkpoint_deleted
core.oidc_provider.create core.oidc_provider.update core.oidc_provider.delete
core.integration.create core.integration.update core.integration.change_url core.integration.write_secret core.integration.delete core.integration.test core.integration.local_migrated
core.secret.rotate
core.backup.create core.backup.download core.backup.import
core.restore.stage core.restore.apply core.restore.revoke
core.system.factory_reset
core.update.apply core.update.rollback
core.diagnostics.download
core.audit.export core.audit.prune
```

`core.role.reset` is **not** in the list: §7.1 marks it "with seed-role editing (§3.6)", a deferred-build member.
Re-derive the list from 05-rbac §7.1 at your base; if the spec's table differs from this list, the spec wins and you
note the difference in the PR body.

Redaction (§7.7): `Details` keys matching `(?i)(password|secret|token|key|cookie|authorization)` → `"[redacted]"`;
string values with a credential prefix `mcs_`, `mck_`, `mcb_`, `mci_`, `mct_`, `abk_`, `abbs_` → `"[redacted]"`
regardless of key; `Details` capped at `DetailsMaxBytes` of JSON.

Metrics (§11, under the App's namespace): `audit_records_total{namespace,outcome}`, `audit_dropped_total`,
`audit_queue_depth`, `audit_write_errors_total{path}`, `audit_pruned_total`. This row adds core's `audit` metrics scope,
so it adds `core/audit/testdata/series.golden` for that scope, asserted by `metricstest.AssertSeries` (R1-10's
`core/metrics/metricstest`; 05-metrics §5 item 5).

Kernel (`core/kernel/audit.go`): the functions the boot sequence will call (names coined here, e.g. `buildAudit`,
`startAudit`, `stopAudit`): register the `c/audit/` core keyspace the same way the kernel registers `c/storage/` (see
the base-state grep), build `audit.New` **after the store opens and before P4b** (05-rbac §5 item 2; 06 §6.5.1),
keep the `*audit.Store` for later phases, start its async writer at Run step 7 and stop it on shutdown. Test them
directly from `core/kernel/audit_test.go`; F2-16 adds the calls from `boot.go` and `kernel.go`.
Do **not** register `core.audit.prune` (no task kind is registered before F5; 05-rbac §5 item 7) and do not mount
`/api/core/v1/audit` (F4-05).

Concurrency (§9; CLAUDE.md): one writer goroutine, bounded channel, `Emit` never blocks; `Record` and `RecordIn` safe
for concurrent use; no package-level mutable state (R5).

## TDD: tests to write first (names copied from 05-rbac §12)

- `TestAudit_SyncInSameBatch` — a failing batch leaves neither change nor record.
- `TestAudit_SeqUniqueAcrossCrash` — `Sequence` reservation: after a simulated crash (`CrashClone`), no `seq` reissued.
- `TestAudit_DenialCoalescing` — `testing/synctest` clock; identical denials within the window fold into one record
  with `Coalesced = n`.
- `TestAudit_RedactsSecretsByKeyAndPrefix`
- `TestAudit_QueueFullDropsWithoutBlocking`
- `TestAuditAction_Grammar` — every literal in `CoreActions()` and every `"core.` action literal in `core/` matches the
  grammar.
- `TestNoGlobals` over `core/audit` (the archtest R1-01 ships; if it takes a package list, add `core/audit`).
- `TestAudit_QueryFilters` (name coined here) — `Query` by actor, action prefix, outcome, target, `BeforeSeq` cursor,
  newest first.

- `TestAuditSeries_Golden` (name coined here): `metricstest.AssertSeries` over the `audit` scope's provider against
  `core/audit/testdata/series.golden`.

`TestAudit_RestoreCopiesForwardRecords` and `TestAudit_CopyForwardThenWriteNoOverwrite` need audit's restore hook,
which is not this PR's: F4-11 writes the hook body (`core/audit/restore_hook.go`) and its restore-hook runner (the P4d
step 3 kernel function), F4-08 wires it at the identity flip, and F4-11 owns the two tests.

## Steps

1. Base-state greps; plan in the PR body.
2. Tests first.
3. Types, store, action table, redaction, async writer, metrics.
4. The kernel-side functions in `core/kernel/audit.go` (construction before P4b, start at step 7, stop on shutdown),
   tested directly; no edit to `boot.go` (F2-16 calls them).
5. Headers; changelog fragment; acceptance.

## Acceptance

```bash
cd core
go build ./... && go vet ./audit/... ./kernel/...                                  # exit 0
go test ./audit/... -race -count=1                                                 # ok  github.com/falkcorp/mediacore/audit
go test ./audit/ -run 'TestAudit_SyncInSameBatch|TestAudit_SeqUniqueAcrossCrash|TestAudit_DenialCoalescing|TestAudit_RedactsSecretsByKeyAndPrefix|TestAudit_QueueFullDropsWithoutBlocking|TestAuditAction_Grammar' -race -count=1 -v | grep -c '^--- PASS'   # 6
go test ./audit/ -run TestAuditAction_Grammar -v -count=1 | grep -c PASS           # >=1
grep -rn 'core.role.reset' audit/ | wc -l                                          # 0 (deferred)
go test ./kernel/... -race -count=1                                                # ok
cd ..
make -C core ci                                                                    # exit 0
make ci                                                                            # exit 0
```

## Out of scope

The boot call (F2-16); the audit query and export routes (F4-05); auth, rbac and tasks recording their events (their
flips); audit's restore hook and its two tests (F4-11 writes them; F4-08 wires the hook); `core.audit.prune` (registered with the tasks flip); any ABO edit.

## Idempotency / rollback

If `core/audit/audit.go` exists with `type Logger interface`, run the acceptance instead. Rollback = revert: no caller
records yet; the `c/audit/` keyspace holds nothing.

## Repo rules

File headers on every new file; conventional commit (`feat(core/audit): audit store and action table (dark)`) with
`Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; `changelog.d/` fragment. The kernel's
construction adds no boot-time durable write (the store writes only when a caller records), so the crash matrix is
unchanged; if your implementation writes at boot (for example, a `Sequence` initialisation record), add its row to the
crash matrix in this PR (09 §9.3 "Boot writers extend the crash matrix").
