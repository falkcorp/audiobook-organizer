<!-- file: docs/handoffs/2026-08-16-overnight-silent-failure-fixes.md -->
<!-- version: 1.1.0 -->
<!-- guid: 7d1a4b60-3e92-4c58-b0a7-2f6c9d81e534 -->
<!-- last-edited: 2026-08-16 -->

# Overnight session, 2026-08-16 — deploy + four silent-failure fixes

## Read this first

**Production was deployed** and is running the current `main`. You gave standing
authorisation mid-session ("You may always deploy the normal and the debug versions to
prod"), so the deploy that the previous handoff had deliberately left undone is now done.

- Deployed **twice**: first from `9a2f1ae4` at 00:34, then again from `5aeb02a8` at
  **01:37:18 EDT** once the last two PRs merged, so prod now runs everything below.
- Health `{"status":"ok"}`. pprof live on `localhost:6060`. Search index 59826/59826.
- Built with `make deploy-debug`, per the standing "prod stays on DEBUG" rule.
- **This is the first production binary containing the target-path unification (#2479)
  and the write-back I/O fixes (#2468–#2470)**, plus all four of tonight's fixes.
- **Prod runs `5aeb02a8`. `main` is ahead, at `20023a5d`.** The difference is docs and a
  Makefile change only — nothing behavioural is missing from prod, so **no redeploy is
  needed**. Stated here rather than only in the tally below, because this is where you'd
  look before deciding.
- The only ERROR in the logs is `"Failed to start HTTP/3 server" err="http: Server closed"`
  from the *outgoing* pid during the restart — a shutdown artifact, not a startup failure.
  Worth knowing so you don't chase it: it appears on every restart.

### ⚠️ The library-wide relocation has NOT happened — and will NOT happen on its own

This is the most operationally important part of this document, and it got more important
while I was verifying it.

Measured on the running prod binary: **0 organize runs, 0 renames, 0 taglib invocations.**
So the relocation is *pending*, not done, and `native_taglib` still has never executed in
production despite linking.

I first assumed that meant "it will fire on the next organize run." **It won't.** Startup
logs a warning that three scheduled tasks are enabled but structurally unable to run:

```
level=WARN msg="Scheduled task is ENABLED but can NEVER run — it has no interval and the
maintenance window does not reach it; set scheduled.<task>.interval, or add the task to
maintenanceOrder if it is meant to run in the nightly window"
  taskName=library_organize        interval=0s declaresMaintenanceWindow=false inMaintenanceOrder=true
  taskName=library_size_refresh    interval=0s declaresMaintenanceWindow=false inMaintenanceOrder=true
  taskName=metadata_upgrade        interval=0s declaresMaintenanceWindow=false inMaintenanceOrder=true
```

**This is pre-existing, not caused by tonight's changes** — the same warning appears 15
times in the logs before this boot. But it means these three tasks have been reading as
"enabled" while doing nothing, which is the same species of defect as everything else in
this document, just at the config layer rather than in code. `metadata_upgrade` is the one
I'd look at first: it is the one you are most likely to have assumed was running nightly.

**I did not change this.** Giving `library_organize` an interval would kick off a
whole-library relocation, unattended, while you were asleep — that is your call to make
awake, not mine to make at 2am. The three tasks need either an `scheduled.<task>.interval`
or `declaresMaintenanceWindow=true`; deciding which, and in what order to let them run, is
the actual decision here.

> **Instrument note, because I got this wrong twice.** `grep -c organize` over the journal
> reported 582 hits that meant nothing — it matches the process name `audiobook-organizer`
> in *every* line. Switching to the `organiz` stem "with a bogus-value control returning 0"
> did **not** fix it and still reported 394: a bogus control only proves the grep isn't
> inert, not that the pattern isn't matching the process name. The working form strips the
> syslog prefix first and uses *both* a positive and a negative control:
>
> ```bash
> j() { journalctl -u audiobook-organizer --since "<t>" --no-pager \
>        | sed 's/^.*audiobook-organizer\[[0-9]*\]: //'; }
> j | grep -icE "organiz"   # 26 — all startup/registration + the path /mnt/.../audiobook-organizer
> j | grep -icE "renam"     # 0
> j | grep -ic  "INFO"      # 351  <- positive control: the instrument CAN see things
> j | grep -ic  "zzqqxx"    # 0    <- negative control: it isn't matching everything
> ```

## The two lessons this session kept re-teaching

Worth stating plainly, because they are what makes this document more than a PR list:

1. **A rule that lives in one caller is a rule every other caller silently lacks.** Three
   separate defects tonight were the same shape — `planTargetPaths` rows, the
   `OrganizeBookDirectory` empty result, and `LegacyOpID` propagation. Each had one caller
   that did the right thing and several that had never grown the check. Every fix moved the
   rule *inside* the callee. That is also exactly what #2479 was.
2. **A green unit test proves the unit, not the wiring.** Twice a test proved a function was
   correct while proving nothing about whether production called it — and in one case the
   mutation survived on the exact line the fix touched. Both now have tests that drive the
   real production path end to end.

## What was fixed

All four are the same species: **an operation that failed and reported success.** Three
came out of the target-path work; the fourth was owner-reported.

### 1. `ApplyMetadataFileIO` had no return value — PR #2480 ✅ merged

The rename runs inside it. A pipeline failure was swallowed into a `slog.Warn`, so none
of its six callers could see it, and `applyCachedCandidateForBook` returned
`Applied: true` regardless of what happened on disk. **The batch-apply API said the apply
succeeded while the files had never moved**, and the batch op's `write_failed` counter
could never be incremented by a rename failure.

Now returns an `error`. `Applied` stays true (the database change is real and durable —
`runApplyPipeline` persists rows for every rename that *did* succeed before returning the
failure), and `WriteBackFailed` is set, which is exactly the distinction that field
already existed to draw. Three interfaces, five call sites, two regenerated mocks.

### 2. An empty organize reported success — PR #2481 ✅ merged

`OrganizeBookDirectory` `MkdirAll`s its target directory *before* copying and skips
sources that have vanished, so a book whose files were all gone returned
`(targetDir, empty pathMap, nil)`. **Reachable without any row being flagged `Missing`.**

Of three callers, only `OrganizeDirectoryBook` checked. The other two took the directory
at face value: `ensureLibraryCopy` created a version-linked **book record pointing at the
empty directory**, and `organizeMultiFileBook` assigned it to `book.FilePath`.

The check moved *inside* the function, and the error now names the book and says why.

**Process note worth keeping:** this PR failed CI on a test I had not run. I had run the
`organizer`, `metafetch` and `itunes` packages locally — but
`TestOrganizeDirectoryBook_AllSourceFilesMissing` lives in `internal/server` and asserted
the old literal error string. Selecting packages by where the *change* is misses tests that
assert on it from elsewhere. The fix was to run the full `go test ./... -short` (exit 0)
before re-pushing, which is what I did for every subsequent PR.

### 3. Activity summaries dropped their data — PR #2482 ✅ merged

Your 2026-08-14 screenshot: "cover art saved to" (to where?), "ISBN enrichment succeeded
for" (for what?), and a neighbouring row with a raw slog line and a stray quote pasted in.

**Those are one bug, not two.** The message was extracted with
`strings.LastIndexByte(msg, '"')` — the last quote in the *whole remaining line*. With no
quoted attribute after `msg=` it lands correctly by luck; with one, the message swallows
the rest of the line. The filed diagnosis reasonably concluded there were two
inconsistent bridges; there is one parser.

Lines are now scanned once into ordered `key=value` attrs (handling quoted values with
spaces, `=`, escaped quotes). Attrs are rendered into the summary **and** stored in
`details` so they stay queryable. A message ending in a preposition or colon takes its
first attr bare — *"cover art saved to /lib/Asimov/cover.jpg"*; anything else gets
`key=value` appended.

**The near-miss:** the mutation test here **survived** at first. Replacing
`Summary: RenderSummary(parsed)` with `Summary: parsed.Message` — the exact line the fix
touches in production — left every test green, because the tests called `RenderSummary`
directly and never proved the writer *used* it. A test now drives a real slog line through
the real `io.Writer` and asserts on the persisted row; the mutation then died.

### 4. Legacy operation rows were write-only after creation — PR #2483 ✅ merged

Your report: every maintenance-job row of 2026-08-14 sat at **"pending"** in the ops UI,
including `fix-file-modes` and `normalize-primary-flags`, both of which had completed with
journalled summaries. It misled you twice in one day.

Jobs dispatched through `maintenance.job` and the scheduler create a **v1** `operations`
row and then enqueue a **v2** op carrying that row's id in its params as `legacy_op_id`.
Nothing ever wrote the v1 row again. The ops UI reads v1. A handful of ops did the update
by hand at their own call sites (`itunes_ops.go`, `diagnostics_ops.go`,
`folder_autoscan_op.go`); everything the scheduler dispatched did not, and never would
have — which is the argument for fixing it centrally rather than at a twenty-first call
site.

The propagation now runs from `publishOpTerminal`, which every terminal path already
funnels through, so an op cannot reach a terminal state without passing it. Details worth
knowing:

- The v1 surface is discovered by **type assertion**, not added to `OpsV2Store` — several
  registry test fakes implement `OpsV2Store` and nothing else, and widening the interface
  would break them for a concern none of them models. `PebbleStore` already satisfies it.
- Existing progress counters are **preserved**. `UpdateOperationStatus` overwrites progress
  and total positionally, so passing `0,0` would trade "stuck at pending" for "finished at
  0%", which is no more honest. A completed run with no counters is written as `1/1`.
- Rows already terminal at the target status are not rewritten.
- v2's `interrupted_ask` / `interrupted_dropped` both collapse to v1's single
  `interrupted`, matching what `server_lifecycle.go` already writes on restart sweeps.

**Adjacent observation, not fixed:** prod's maintenance window was cancelled by the
watchdog at 331s idle, and *then* the plugin logged "completed successfully (100%)". That
disagreement is pre-existing, but it now has a consequence it didn't have before — the
legacy row for that op will be mirrored as `canceled` while the op's own log claims
success. That is the next thing to look at in this area.

**Verified live in production.** After the redeploy, the scheduler fired `purge-deleted`
and `temp-file-cleanup` — both of which are exactly the class of op that never updated its
legacy row — and both logged
`registry: mirrored terminal status onto legacy op row ... status=completed`. Reading the
rows back through `/api/v1/operations` (the endpoint the ops UI uses) gives a clean
before/after on one page:

```
01M04H6CRCZF  purge-deleted        status=completed  1/1   <- after the fix
01M04H6CRBP3  temp-file-cleanup    status=completed  1/1   <- after the fix
01M04FC6YWT8  archive-sweep        status=pending    0/0   <- before
01M04FC5ZDYZ  trash-cleanup        status=pending    0/0   <- before
01M04FC4ZY2R  temp-file-cleanup    status=pending    0/0   <- before
01M04F29YGNN  cleanup_activity_log status=pending    0/0   <- before
01M04F22VZEJ  maintenance-window   status=pending    0/0   <- before
01M04DJFDQDM  purge-deleted        status=pending    0/0   <- before
```

That also confirms the counter fallback: these ops carry no counters, so they read
`completed 1/1` rather than a dishonest `completed 0/0`.

**No backfill was run.** The stale `pending` rows above are exactly what a backfill would
repair, and there are more than one page of them. A write loop over production op rows is a
deliberate, supervised action, not something to fold into a behaviour fix unattended. See
"Still open".

## Corrections to previously filed notes

- `todo.d/2026-08-15-organize-rename-silent-failures.md` says F7 spans **two** interfaces.
  It is **three** — `internal/server/handlers/metadata/interfaces.go:108` was missed.
- `todo.d/20260814-activity-summaries-drop-attrs.md` says "two inconsistent bridges
  exist". There is one bridge with a quote-scanning bug. See above.

## Still open

| Item | State |
|---|---|
| **3 scheduled tasks enabled but inert** | `library_organize`, `library_size_refresh`, `metadata_upgrade` — `interval=0s` and outside the maintenance window's reach. Pre-existing. **Needs your decision, deliberately not changed.** See the top of this document. |
| **F5-remainder** | Adopt-on-equal-**size** rather than content hash, at `organizer.go:765`. `scanner.ComputeFileHash` exists. Deliberately **not** attempted overnight: it changes file-adoption behaviour, unattended, on an instance that is one organize run away from relocating the whole library. Do it awake. |
| **Backfill for stuck legacy rows** | #2483 fixes the forward path only. The already-stuck rows need a one-off supervised pass. |
| **Maintenance-window watchdog vs. plugin success** | Cancelled at 331s idle; plugin then logged "completed successfully (100%)". Pre-existing disagreement, newly consequential — see #4 above. |
| **Activity summaries, second half** | Metadata-apply rows leading with the book title rather than a bare "book →" link, and `(none) → 2021` instead of a dangling arrow. Separate change in the emitters + frontend. |
| ~~**H110 coverage double-run**~~ | ✅ **Fixed and merged — PR #2485.** See below. |

## 5. `make test-short` ran the whole suite twice — PR #2485 ✅ merged

Not a correctness bug, but it shares the species: the second run discarded its output with
`>/dev/null 2>&1`, so a failure occurring *only* under the coverage run produced a silent
non-zero exit with nothing to read.

`test-short` ran `go test ./...` once with `-race`, then again with `-coverprofile`.
Measured on an idle machine (a first attempt was **discarded as invalid** — another full
suite was running concurrently and contending for CPU, which is the same mistake that
would have produced a confident wrong number):

| Config | Time |
|---|---|
| `-race` alone | 493s |
| `-coverprofile` alone | 473s |
| **both together** | **500s** |

One combined pass is **466s cheaper per invocation (966s → 500s, −48%)** and only 7s more
than `-race` by itself. Coverage is byte-identical either way — 47.0%, 81950 profile lines
— so `coverage-check-short`'s floor is unaffected, and `-covermode=atomic` was already
required by `-race`. Verified end to end before pushing: 512s, exit 0, coverage 47.0%,
gate passes. CI then ran the new target on the PR itself.

Also gitignored `.ci/coverage-last.txt`, which the gate writes as local running state and
which was neither tracked nor ignored — so running the documented target left a dirty tree.
I swept it into a commit with `git add -A` and had to back it out, which is the footgun
that rule exists for.

## COMPLETED / REMAINING / BLOCKED

**COMPLETED: 7** — prod deploy ×2 (debug build, verified healthy, zero real errors);
PR #2480 (F7 file-I/O error return); PR #2481 (F6 empty organize is an error);
PR #2482 (activity summary attrs); PR #2483 (legacy op terminal status, **verified live in
prod**); PR #2484 (this handoff); PR #2485 (test-short single pass, −48%).
**All six PRs merged**, each with 21–22 checks passing and zero failures. Worktrees
removed, branches deleted. Prod runs `5aeb02a8`; `main` is at `20023a5d` (the two commits
since prod are docs + the Makefile, neither shipping in the binary).

**REMAINING: 5** — 3 scheduled tasks enabled-but-inert (`library_organize`,
`library_size_refresh`, `metadata_upgrade` — needs your decision); F5-remainder (content
hash, deliberately deferred to a waking session); backfill for already-stuck legacy op
rows; maintenance-watchdog vs. plugin-success disagreement; activity summaries second half
(emitters + frontend).

**BLOCKED: 0**

## If you only read one thing

The relocation from #2479 has **not** run, and `library_organize` is enabled but cannot
run, so it will not start on its own. That is the decision waiting for you.
