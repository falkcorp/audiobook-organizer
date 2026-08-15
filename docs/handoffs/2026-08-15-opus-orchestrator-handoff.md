<!-- file: docs/handoffs/2026-08-15-opus-orchestrator-handoff.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5b8d3f26-9e14-4a70-c2b5-7f0a4d8e1c39 -->
<!-- last-edited: 2026-08-15 -->

# Orchestrator handoff — 2026-08-15 (~00:45 EDT)

For the next orchestrator session. Predecessor: the 2026-08-14 marathon —
**48 PRs merged (#2421–#2466)**, five prod deploys, the whole E-wave executed
and verified. Companion docs: the task board
([`2026-08-14-task-board.md`](2026-08-14-task-board.md), v1.2.0 — statuses
below supersede it where they differ) and the session-close handoff
([`2026-08-14-session-close-handoff.md`](2026-08-14-session-close-handoff.md)).
The live claim ledger is `.claude/notes/2026-08-14-tasks/STATUS.md`
(local-only, gitignored).

## Read these before ANY edit

- `CLAUDE.md` — worktree-per-task is MANDATORY; never commit to main; every
  PR needs a HEADERLESS `changelog.d/` fragment; new TODOs go in `todo.d/`
  (also headerless); version headers on every other file, bumped per edit.
- Required checks (fast set): `Minimal CI / Minimal CI Summary`,
  `Require changelog fragment`, `TODO Fragment Headers`. E2E is advisory.
  Merge with `--rebase`. The OWNER also merges PRs by hand — run
  `gh pr list` before assuming state.
- **Mutation-verify every new test** — commit the implementation FIRST, then
  mutate/`git checkout --`/restore (an uncommitted base gets silently wiped;
  it happened once on 2026-08-14).
- Prod: debug builds only (`make deploy-debug`); `make deploy*` builds the
  LOCAL tree — `git pull` first. The API is Bearer-authed (token file
  `.api-token`, never echo it); responses nest under `data`. Maintenance
  jobs: POST `/api/v1/maintenance/jobs/<id>` with `{"dry_run":...}`;
  registry ops: POST `/api/v1/operations/v2` with `{"def_id","params"}`.
  `journalctl -u audiobook-organizer` (no sudo) is the truth for op results —
  the legacy op LIST shows finished jobs as "pending" forever (fragment
  `20260814-legacy-op-rows-stuck-pending`, HIGH VALUE fix).

## In flight RIGHT NOW

1. **UA-purge dry-run** (op `01M01607KW…`) has been interior-probing since
   18:33 (6h+ at ~350% CPU): 20,117 Unknown-Author books vs 502,929
   candidate files. A waiter polls the journal for
   `purge-ua-duplicates: complete`. **Do not restart the service until it
   reports** — that is the only reason the next deploy is HELD.
2. **Deploy pending**: main (`#2466`) carries the organizer store-wiring fix
   — until deployed, prod's #2457 author gate defers most bulk organizes
   (slim books look authorless). Deploy IMMEDIATELY after the census lands,
   then verify boot (no index divergence; organize of an AuthorID-only book
   resolves the author).
3. After the census: present the verified/kept split to the OWNER before any
   UA-purge apply. Expect kept_no_twin ≳ 314 (the UA-only survivors).
   If verified ≈ 0 or ≈ everything, suspect the instrument first.

## The big picture finding worth knowing

The Unknown-Author disaster chain is now fully explained and half-repaired:
`Organizer.SetStore` had ONE caller, so AuthorID books organized as
"Unknown Author" (root cause, fixed #2466) → #2457 gates placeholder renames
→ #2458's `purge-unknown-author-duplicates` cleans the debris (census
running) → the ~12K dangling-series repair op (C610 follow-up) is still
unbuilt. The audit doc
`docs/audits/2026-08-13-mass-reorganize-duplicated-14tb-under-unknown-author.md`
is the constitution for anything touching that tree: interior-content probes
only, twin-on-disk required, never delete on size match, ~314 UA-only
survivors are sacred, no reclamation claims (blocks are ZFS-cloned).

## Outstanding work, ranked

**P0 — prod follow-through**
- Deploy #2466 post-census (above), boot checks, re-run
  `fix-file-modes` dry-run (should be ~0 new 0600 files now the WriteTagsSafe
  fix is live).
- UA-purge: owner-gated apply after census review.
- Author row 46583 (`&#169`, 1 book): reattribute the book to its real
  author, then delete the row (51870 already deleted).
- 124 residual 0600 files = stale-path defect — investigate rows whose
  recorded path is ENOENT while the real file lives elsewhere (fragment
  `20260814-writeback-residue-and-stale-file-paths`; owner one-liner repair
  inside).

**P1 — owner-reported product issues (all have fragments with evidence)**
- Metafetch fan-out: the metadata FETCH is the serialized foreground half —
  background op + 3-4 network-bound workers + per-request start jitter
  (`20260814-metafetch-fanout-with-jitter`). This also kills the false
  "signed out — no changes were made" symptom
  (`20260814-matcher-skip-all-and-false-signout`).
- Matcher: skip-all/hide-multiples triage control (same fragment).
- Multi-file write-to-files → dispatch as `maintenance.bulk-write-back` op
  (`20260814-matcher-writeback-background-job`) — but note bulk-write-back
  itself needs diff-skip + in-op parallelism first
  (`20260814-bulk-writeback-needs-diff-skip-and-parallelism`, ~35 s/book
  serial + unconditional writes measured).
- Activity log: 7-day auto-compaction setting on the log screen
  (`20260814-activity-log-auto-compact-setting`); summaries drop slog attrs
  and never name the book (`20260814-activity-summaries-drop-attrs`, has
  file:line root cause).
- Legacy op rows stuck "pending" (`20260814-legacy-op-rows-stuck-pending`)
  — misled the owner twice in one day; propagate terminal status to
  LegacyOpID rows + backfill.

**P2 — security/CI**
- CA12 wave 2: 316 go/log-injection alerts survive because CodeQL
  weak-updates variadic `[]any` — needs a model-pack sanitizer row; VERIFY
  the extensible predicate name exists in the pinned `codeql/go-all` before
  shipping (`20260814-codeql-loginjection-mad-model`). Check whether #2454's
  module-path fix closed the 5 path-injection alerts (that's the
  models-work-at-all signal).
- A04: pick a mutating op that actually calls `CreateOperationChange`
  (`20260814-a04-probe-records-activity-not-changes`) and verify the audit
  trail on prod.
- H110: Coverage Floor job runs the test suite twice (`make test-short`'s
  separate coverage pass) — single `-coverprofile` run fixes the 35-min
  timeouts. Evidence in the 2026-08-14 scratchpad covfloor logs (gone with
  the session; re-derive from the Makefile).

**P3 — backlog (specs in `.claude/notes/2026-08-14-tasks/` waves)**
- C514 activity-channel drop accounting; C212 delete-guards; C610 repair op
  (~12K dangling refs, dry-run gated); C111 census fragment says author-46627
  verification is now unblocked (nil flags gone); B-wave ABS work (B10–B50);
  E04 title-match false-series repair (build + dry-run, never delete on
  book-count); E08 full run after its prerequisites; memdb warmup-publish
  race repro (offset-walker fragment §2); F-wave stays PARKED (owner's
  lowest priority).

**Owner-side (do not do these for them)**
- The 2 ambiguous Alcatraz PID groups (hand pick).
- `.api-token` rotation (advised, deprioritized).
- UA-purge apply sign-off; E08 full-run timing.

## Verification culture (non-negotiable, learned the hard way this week)

Dry-run first, always; re-dry-run as the negative control after every apply
(C111 went 5702/41/0 → applied → 0/0/0; E01 went 26,027 → 0; E02 went
209 → 2). Never enumerate with the instrument under test. Reproduce with the
client's actual params — `pid-repair` took the APPLY path from a JSON body
until #2447. When a count surprises you, verify the instrument with a bogus
value before believing it. Report counts, never adjectives.
