<!-- file: docs/specs/2026-07-10-ux-small-items-design.md -->
<!-- version: 1.0.0 -->
<!-- guid: d916d571-f20b-4ed5-9c53-b6d3d37e7ee1 -->
<!-- last-edited: 2026-07-10 -->

# Small Open UX/Feature Items (INIT-10) — Design Spec

**Status:** Approved — ready for implementation planning, at Gate 2
**Scope:** Go backend (one new op, one logging sweep), React/TypeScript frontend (one detail-view feature), docs authoring, TODO.md closeouts, one prod verification (TASK-08). No schema changes. Prod-data mutations: exactly two possible, both human-gated — (1) the C8 issue-creation apply (AskUserQuestion + confirm-hash), and (2) TASK-08's OPTIONAL metadata-domain-tag verification, which requires a `metadata-fetch` op that is fetch+APPLY (verified: it rewrites book identity + enqueues iTunes write-back) and therefore runs only after AskUserQuestion with a named throwaway test book; TASK-08's autonomous path uses a read-only op (`scan-duration-mismatch` maintenance job) and writes nothing.
**Parent task:** INIT-10 — Small open UX/feature items (`.claude/notes/2026-07-10-remaining-work-master-plan.md`)

---

## Motivation

INIT-10 is the long tail of the remaining-work master plan: eight small items that are each
individually cheap but collectively noisy in TODO.md and the issue tracker. Anchor
verification at HEAD `fce58498` (2026-07-10, `scratchpad/anchors-init-10.json`) found that
**three of the eight are already shipped and only need verified closeouts**, one is a
mechanical sweep, one is docs authoring, one is an operational prod check, and only two
involve genuinely new product surface:

| Item | Master-plan framing | Verified reality at HEAD `fce58498` |
|---|---|---|
| User Ratings UI (TODO.md:1857) | "API + UI pending" | **STALE** — all 5 sub-items RATE-1..5 checked `[x]`; RATE-1..4 cite PRs #542/#552/#553/#554; RATE-5 shipped as `web/src/components/audiobooks/BulkRatingDialog.tsx` (commits `399ea3f9`/`bd6848e2`, TODO checked by `cd2b3eb8`) but the checkbox carries no citation and the section header still reads "API + UI pending" |
| Audible runtime-vs-duration mismatch (TODO.md:1876) | open detection item | **SHIPPED** — all 5 sub-items `[x]` (PRs #549/#561 per TODO.md's own citations); the bulk scan is a maintenance JOB `scan-duration-mismatch` (`internal/maintenance/jobs/scan_duration_mismatch.go`, registered via `maintenance.Register`; only param `dry_run`; threshold hardcoded 120s; count emitted via slog, not HTTP JSON), dispatched via `POST /api/v1/maintenance/jobs/scan-duration-mismatch` — [this-session verified] `server_lifecycle.go:1249`; there is NO bespoke GET route and NO `max_delta_min` param. The DUR-1 line's cited anchor `MetadataReviewDialog.tsx:604` has drifted to ~:774 |
| Audible category ladders (TODO.md:1940-1950) | "mostly shipped; confirm no residual" | all 5 items `[x]` (PRs #548/#561/#1728); code anchors verified in `internal/metadata/audible.go` — needs only a confirm-no-residual pass |
| HASH-CHAIN-2 (#1270, TODO.md:2057) | show hash chain in book-file detail view | genuinely open: all 4 hash fields exist in `internal/database/store.go` with JSON tags; frontend `BookFile` type has `file_hash`/`original_file_hash`/`post_metadata_hash` but NOT `download_hash` (the `Book` type has NEITHER `download_hash` NOR `post_metadata_hash` — the chain must render from `BookFile`, never `Book`); zero UI rendering of any hash anywhere under `web/src`. Anchor reconciliation: the anchors JSON names `BookDetailFilesTab.tsx` as the detail view; [this-session verified] FilesTab delegates per-file rendering to `BookDetailVersionGroup.tsx`, passing `bookFiles: BookFile[]` down (~:156) — VersionGroup is the render site, `bookFiles` the data source |
| C8 (#1447, TODO.md:810) | auto-file GitHub issue per `not_dup` cluster | genuinely open: zero implementation in `internal/`; prior design brief exists (`docs/agent-tasks/dedup-dataset/TASK-05-autobug-not-dup-clusters.md`, marked DEFERRED); label quality now depends on INIT-1's re-mine |
| DOCS-1 (#1276) | comprehensive system docs | open; docs authoring, not weak-model agent work |
| SLOG-W13 (#1254, TODO.md:1408) | wire raw `slog.*` to `logging.*` | open; fresh count at HEAD: **1162** `slog.Info/Warn` occurrences across **212** files (TODO's "~1363/193" figure is stale); scope is op-context flows only |
| SLOG-PROD-VERIFY (#1255, TODO.md:1409) | prod smoke-test op-ID chain | open; `GET /api/v1/operations/:id/activity` exists — [this-session verified] `wire_library_routes.go:39` (`ListOperationActivity`). CAUTION: the procedure doc `docs/operations/slog-prod-verify.md` triggers a `metadata-fetch` op which is fetch+APPLY (a prod write) — the autonomous run must substitute a read-only op; metadata-domain tags only via an AskUserQuestion-gated run (see plan TASK-08) |

**Goal:** close all eight INIT-10 items — three by verified closeout, two by small shippable
features (hash-chain UI, gated C8 op), one by a scoped `/parallel-sweep` logging sweep, one by
strong-model docs authoring, and one by a read-only prod smoke-test — leaving TODO.md and the
issue tracker accurate.

## Goals

- TODO.md:1857 User Ratings header corrected; RATE-5 line carries its shipping evidence.
- Book-file detail view renders the hash chain (Download → Original → Post-metadata → Current)
  per file, with `download_hash` added to the frontend `BookFile` type (#1270 closed).
- Audible runtime-mismatch section closed out with a drift-corrected citation and a fresh
  read-only prod scan report attached.
- Category-ladders residual confirmed zero (report with exact grep evidence).
- C8 op implemented behind a dry-run-first + AskUserQuestion gate, dispatched only after
  INIT-1's label re-mine lands (#1447).
- `docs/system/` comprehensive documentation set authored (#1276).
- All raw `slog.Info/Warn/Error/Debug` calls **inside op-context flows** rerouted to
  `logging.*` (#1254); code outside op flows explicitly stays raw slog.
- Prod op-ID chain smoke-tested over SSH (#1255) using a READ-ONLY op on the autonomous path
  (the procedure doc's `metadata-fetch` op writes — its use is AskUserQuestion-gated) and the
  TODO item checked off.

## Non-goals (v1)

- Backend hash computation changes — all four hash fields already exist and populate; only
  display is in scope for #1270. Deferred: nothing.
- Converting startup/background-goroutine slog calls (outside `logging.WithOp` flows) — TODO.md:1408
  explicitly scopes them out; they stay raw slog.
- Actually creating GitHub issues from C8 without a human decision — issue creation is an
  outward action and is AskUserQuestion-gated forever, not just in v1.
- Any change to the duration-mismatch detection logic — it shipped in PR #549; INIT-1 owns the
  upstream ms/sec duration bug.
- Building new ratings API/UI — verified already shipped; closeout only.

## Decisions (locked during design)

1. **Ratings item is a closeout, not a build:** the master-plan framing "API + UI pending" was
   verified stale; the losing alternative (re-implementing RATE-1..5) would duplicate PRs
   #542/#552/#553/#554 and `BulkRatingDialog.tsx`. Locked by the anchor-verification drift
   correction.
2. **Hash chain renders in the existing files tab, no new endpoint:** the book-files API
   already serializes all four hashes (JSON tags in `internal/database/store.go`); the losing
   alternative (a dedicated `/hash-chain` endpoint) adds surface for zero data gain. Frontend
   only + `download_hash` type addition.
3. **C8 stays deferred behind a three-part blocker, and issue creation is dry-run +
   AskUserQuestion:** dispatch requires (a) INIT-1's mining-rule PR merged, (b) the gold-label
   re-mine APPLIED ON PROD, and (c) an explicit human go-ahead — all three (the TASK-05 brief
   is authoritative and strictest; do not dispatch on any weaker reading). Filing issues from
   prod clusters mined with the CURRENT contaminated `not_dup` rules (the Jul 8 calibration
   root cause) would file garbage. The losing alternative — ship now against current labels —
   is explicitly rejected.
4. **SLOG-W13 runs as a scoped /parallel-sweep, not a repo-wide rewrite:** 1162 call sites
   across 212 files far exceeds the ≥20-call-site sweep trigger, but only files reachable from
   `logging.WithOp` op flows are converted; `internal/server/metadata_ops.go` is excluded until
   the INIT-3 file-ownership check clears, AND `internal/dedup/**` + `internal/plugins/dedup/**`
   + `internal/database/embedding_store.go` are excluded until BOTH INIT-1 and INIT-2 merge
   (master-plan §INIT-1 partition: INIT-2 owns `engine.go`/`embedding_store.go` structural
   edits — `engine.go` alone has 76 slog sites; rules.go/dataset/calibration are INIT-1's) —
   trailing shards cover both exclusion sets afterwards. Losing alternative: one giant serial
   PR (rejected — the exact shape of the 2026-07-05 concurrency-audit serial-hotspot failure,
   in review form).
5. **DOCS-1 is SINGLE-AGENT strong-model work; SLOG-PROD-VERIFY is NOT AGENT WORK:** docs
   synthesis needs whole-system context a weak model cannot hold; the prod smoke-test is an
   operational SSH task the coordinator session runs directly. Neither is dispatched to a
   weak-model executor.
6. **Every item got a task; one is gated/deferred-in-place rather than dropped:** C8 (TASK-05)
   is hard-blocked behind INIT-1's re-mine + human confirmation and cannot start now — that is
   the initiative gate's "defer with a written note" branch, encoded as a blocked wave rather
   than a dropped item. The other seven are dispatchable per the plan's waves; five of the
   eight are verification/closeout-sized already.

## Data model

No new persistent types. Existing fields consumed by TASK-02 (verbatim from
`internal/database/store.go`, re-verify: `grep -n "DownloadHash\|OriginalFileHash\|PostMetadataHash" internal/database/store.go`):

```go
// BookFile hash-chain fields (already shipped — HASH-CHAIN-1 #1722; display-only work remains)
// DownloadHash is the hash of the originally-downloaded file. It is
// distinct from DelugeHash (the torrent info-hash).
DownloadHash     string  `json:"download_hash,omitempty"`      // as-downloaded
OriginalFileHash *string `json:"original_file_hash,omitempty"` // after iTunes/external tagger
// PostMetadataHash is the SHA-256 of the file immediately after a metadata write.
PostMetadataHash string  `json:"post_metadata_hash,omitempty"`
FileHash         *string `json:"file_hash,omitempty"`          // current
```

Frontend gap (verify: `grep -n "download_hash" web/src/types/index.ts` → currently 0 hits):
the `BookFile` interface at `web/src/types/index.ts` (re-verify: `grep -n "interface BookFile" web/src/types/index.ts`)
carries `file_hash` / `original_file_hash` / `post_metadata_hash` but must gain
`download_hash?: string;`.

### Persistence

- None added. No keyspace changes anywhere in this initiative.

## Components

### C1. Hash-chain display (`web/src/components/bookdetail/BookDetailVersionGroup.tsx`)

**Data source rule (load-bearing):** the chain renders per FILE from the component's
`bookFiles: BookFile[]` prop — NEVER from `version` (`versions: Book[]`; the frontend `Book`
type has no `download_hash` and no `post_metadata_hash`, so `version.*` reads would yield
`undefined` on every file and the fail-open dashes would conceal it permanently). Anchor
reconciliation: the anchors JSON names `BookDetailFilesTab.tsx` as the book-file detail view;
[this-session verified] FilesTab delegates per-file expanded rendering to
`BookDetailVersionGroup` and passes `bookFiles={bookFiles}` down (~:156); VersionGroup maps
`bookFiles` into per-file rows on its `isCurrent` branch (re-verify:
`grep -n "bookFiles" web/src/components/bookdetail/BookDetailVersionGroup.tsx` — prop at ~:54,
mapping at ~:207). The chain hooks into that per-BookFile mapping; non-current versions'
segment rows carry no hash data and are out of scope for v1.

Each file gains a compact hash-chain line: `Download → Original → Post-metadata → Current`
(from `download_hash` / `original_file_hash` / `post_metadata_hash` / `file_hash` on the
`BookFile`), each hash truncated to 12 chars with a full-value MUI `Tooltip` (already imported
in that file — re-verify:
`grep -n "Tooltip" web/src/components/bookdetail/BookDetailVersionGroup.tsx`).
Missing/empty hashes render as `—` (em dash), never hidden rows — a file with only
`file_hash` still shows the chain with three dashes. **Default: chain hidden behind the
already-expanded state; no new fetch** — data rides the existing book-files response.
Fail-open: absent fields are cosmetic dashes, never errors — but acceptance REQUIRES a
positive render test (a `BookFile` fixture WITH a `download_hash` renders its truncated
value), so the fail-open rule cannot mask a wired-to-the-wrong-type regression.

### C2. C8 auto-bug-filing op (`internal/plugins/dedup/autofile_notdup_issues.go` — NEW)

Op ID `dedup.autofile-notdup-issues`, registered exactly like the sibling
`dedup.cleanup-orphan-author-embeddings` (re-verify:
`grep -rn "dedup.cleanup-orphan-author-embeddings" internal/plugins/dedup/cleanup_orphan_author_embeddings.go`).
Two-phase: **dry-run (default)** aggregates `not_dup` label clusters where rule-suppressed
count exceeds a threshold param and emits a report (cluster key, count, sample book IDs) —
read-only, no outward action. **Apply mode** (param `apply=true`) composes the `gh issue create`
payloads but is only ever dispatched after a real AskUserQuestion decision; the op itself
refuses `apply=true` unless an explicit `confirm` param matches the dry-run's report hash,
making an un-reviewed apply structurally impossible. **Where the hash lives (no persistence,
no keyspace change):** the report hash is NOT stored between runs — the dry-run prints
`report_hash` (SHA-256 over the canonically-serialized cluster report) in its output, the
human copies it into the apply run's `confirm` param, and the apply run RECOMPUTES the
aggregation + hash from current data and refuses on mismatch. A mismatch therefore also
catches stale reviews (data changed since the reviewed dry-run), which is the desired
behavior, and the "no keyspace changes" invariant holds. Fail-closed on any GitHub API error:
stop, report partial counts, never retry-spam issue creation. Supersedes but reuses the design
in `docs/agent-tasks/dedup-dataset/TASK-05-autobug-not-dup-clusters.md`.

### C3. SLOG-W13 sweep (many files under `internal/`)

Mechanical transform at each in-scope call site: `slog.Info(msg, args...)` →
`logging.Info(ctx, msg, args...)` (same for Warn/Error/Debug), using the existing
`internal/logging/structured.go` API (re-verify:
`grep -n "func Info(ctx context.Context" internal/logging/structured.go`). In-scope = files on
call paths below a `logging.WithOp` (re-verify: `grep -n "func WithOp" internal/logging/opcontext.go`).
A call site with no `ctx` in scope is OUT of scope — do not thread new ctx params (that is a
signature change, forbidden in a sweep shard). Sharded by package for `/parallel-sweep`.

### C4. Closeout tasks (TODO.md edits + reports)

TODO.md-only transforms with grep-checkable before/after text (TASK-01, TASK-03) and
report-only verifications with zero repo changes (TASK-04). TASK-08 is an operational prod
smoke-test, then a one-line TODO checkoff — but NOT verbatim per `docs/operations/slog-prod-verify.md`:
that doc's `metadata-fetch` op is fetch+APPLY (a prod write). The autonomous path substitutes
the read-only `scan-duration-mismatch` maintenance job to verify the opID→journalctl→
`/operations/:id/activity` chain; the metadata-domain-tag checklist items run only via an
AskUserQuestion-gated metadata-fetch on a named throwaway test book (see plan TASK-08).

## Migration / integration

No callers move. The only Before/After mechanical shape is the C3 sweep:

Before: `slog.Info("scan-duration-mismatch complete mismatches", "mismatches", mismatches)`
After:  `logging.Info(ctx, "scan-duration-mismatch complete mismatches", "mismatches", mismatches)`
(plus the `internal/logging` import; drop the `log/slog` import only if it becomes unused).
Exact shard file lists get pinned at TASK-07 dispatch time from a fresh
`grep -rln "slog\.\(Info\|Warn\|Error\|Debug\)(" internal/ --include="*.go"`.

## Milestones

Milestones are THEMATIC groupings, not shipping units. **The shipping unit is the per-task PR,
in the plan's wave order (the plan's task-skeleton table is authoritative)** — M1's members
deliberately span waves 1/3/4 because of the TODO.md same-file serialization, with M2's
TASK-02 interleaved in wave 2. Do not attempt to ship a milestone as one unit and do NOT
reorder waves to make milestones contiguous (the wave order IS the collision-avoidance).

- **M1 — Closeouts (TASK-01 wave 1, TASK-03 wave 3, TASK-04 wave 1, TASK-08 wave 4).**
  Verified TODO.md corrections + prod/report verifications (TASK-08's prod-write branch is
  human-gated; see C4); additive — no existing behavior changes.
- **M2 — Hash-chain UI (TASK-02, wave 2).** Frontend-only, additive display of already-served data.
- **M3 — Docs (TASK-06, wave 1).** `docs/system/` authoring; additive — no code.
- **M4 — SLOG sweep (TASK-07, wave 5).** Behavior-preserving log rerouting; the only "wide"
  change, gated per-shard by `make ci` + Minimal CI green.
- **M5 — the ONE outward-acting milestone: C8 (TASK-05, wave 6).** Gated by the
  dry-run→AskUserQuestion protocol (apply structurally refused without a reviewed report hash —
  default **off**) AND blocked on the three-part external blocker (INIT-1 mining-rule PR merged
  AND re-mine applied on prod AND explicit human go-ahead); validate the dry-run report
  carefully before any apply.

Every task-PR is individually additive/revertable until M5.

## Files modified

| File | Change |
|---|---|
| `TODO.md` | TASK-01/-03/-05/-07/-08 closeout edits (serialized — see plan collision matrix) |
| `web/src/types/index.ts` | add `download_hash?: string;` to `BookFile` |
| `web/src/components/bookdetail/BookDetailVersionGroup.tsx` | render hash-chain line per file |
| `internal/plugins/dedup/autofile_notdup_issues.go` | NEW: C8 op, dry-run default |
| `internal/plugins/dedup/autofile_notdup_issues_test.go` | NEW: dry-run/refusal/threshold tests |
| `docs/system/*.md` | NEW: DOCS-1 documentation set |
| ~150-200 files under `internal/` (pinned at dispatch) | SLOG-W13 mechanical reroute |

## Testing

| Test | Asserts |
|---|---|
| `TestBookFileTypeHasDownloadHash` (or tsc compile gate) | frontend type accepts `download_hash`; `npm run build` green |
| C1 positive render test (component test, `BookFile` fixture with all four hashes) | a file WITH a `download_hash` actually renders its truncated value — guards against wiring the chain to `Book` (which lacks the fields) and shipping permanent dashes |
| Playwright/`make test-e2e` existing suite | files tab still renders (anti-over-suppression for C1) |
| `TestAutofileNotdupIssuesDryRunReadOnly` | dry-run performs zero GitHub calls; report populated |
| `TestAutofileNotdupIssuesRefusesUnconfirmedApply` | `apply=true` without matching `confirm` hash → error, zero issues filed |
| `TestAutofileNotdupIssuesThreshold` | clusters below threshold excluded; above included (happy path) |
| `go test ./... -short` per SLOG-W13 shard | behavior-preserving reroute; no test asserts on logger identity break |

## Rollback

**Initiative gate (verbatim):** PLAN -> EXECUTE AUTONOMOUSLY where cheap (worktree/PR/CI per item); defer with a written note where not. SLOG-PROD-VERIFY (#1255) is read-only prod verification — allowed autonomously via SSH per house rules, but any prod file/data change discovered as needed -> AskUserQuestion.

Every task is a single revertable PR (rebase/FF merges). M1-M4 touch no data: `git revert`
restores prior state instantly. M5 (C8) stays dormant — the op defaults to dry-run and
structurally refuses un-reviewed applies; if issues were filed and must be undone, they are
closed via `gh issue close` (the op's report lists every issue it created for exactly this
purpose). SLOG-W13 shards revert independently; a reverted shard returns those files to raw
slog with zero behavior change. The prod smoke-test (TASK-08) changes nothing to roll back on
its autonomous read-only path; if the human-gated metadata-fetch branch ran, revert = restore
the designated test book's pre-run metadata JSON (captured before the run, per the gate).

## Open questions (resolved — recorded for the plan)

1. ~~Is RATE-5 actually shipped or just checked off?~~ → Shipped: `web/src/components/audiobooks/BulkRatingDialog.tsx` exists at HEAD; commits `399ea3f9`/`bd6848e2` ("feat(ui): bulk rating dialog from library row selection (RATE-5)"), TODO checked by `cd2b3eb8`. Closeout cites commits (no PR number was attached to the checkbox — the commit SHAs are the citation).
2. ~~Does the Audible runtime-mismatch item need a new detection op?~~ → No: detection shipped (TODO.md cites PR #549) as the `scan-duration-mismatch` maintenance job (`internal/maintenance/jobs/scan_duration_mismatch.go`, dispatched via `POST /api/v1/maintenance/jobs/scan-duration-mismatch`, count read from the op's logs); the task is a closeout plus a fresh read-only prod scan run as the report.
3. ~~Is SLOG-W13 above the /parallel-sweep trigger?~~ → Yes: 1162 Info/Warn occurrences across 212 files measured at HEAD `fce58498` (≥20 threshold), but the in-scope subset (op-context flows) is pinned at dispatch.
4. ~~Can C8 ship now?~~ → No: gated on INIT-1's mining-rule fix + re-mine (Jul 8 finding: `not_dup` labels are contaminated); filing issues from contaminated clusters is rejected.
