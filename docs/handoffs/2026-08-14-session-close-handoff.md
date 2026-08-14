<!-- file: docs/handoffs/2026-08-14-session-close-handoff.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2e9c7b53-4f18-4d0a-b6e2-8a51d3c97f26 -->
<!-- last-edited: 2026-08-14 -->

# Session-close handoff — 2026-08-14 (~14:15 EDT)

State dump for the next session. The companion status snapshot is
[`2026-08-14-task-board.md`](2026-08-14-task-board.md) (v1.1.0); this file is
the *delta* since that board plus everything in flight at close.

## Where things stand

- **30 PRs merged today (#2421–#2448), #2449 (C111 normalize-primary-flags)
  auto-merging at close** — a background waiter merges it when the three fast
  required checks pass. If it's still open at restart: `gh pr checks 2449`,
  then `gh pr merge 2449 --rebase`, remove worktree
  `../audiobook-organizer-c111`, delete branch `feat/normalize-primary-flags`.
- **Production runs the 13:40 EDT build** (all of today's merges through
  #2447). Boot verified: search index reconciled (3,983 stale docs deleted,
  index == library, no divergence warning on the next boot), dedup-refresh
  6h ticker live, config snapshot survived (review_apply on, intervals
  restored). **#2449's job is NOT deployed yet** — next deploy picks it up.
- **E09 Prometheus: DONE.** Scrape is `health=up` since 14:02. The yml block
  pre-existed; the whole fix was a valid key in `/etc/prometheus/abo.token`
  (minted via the API straight into a server-side file, never in any
  transcript). Rotation = overwrite that file, no reload. The staged helper
  script on the server was patched in place to v1.0.1 (its yml-edit step had
  a guaranteed `ValueError` on list-style job entries — a dead indentation
  block calling `.index('-')` on pure whitespace; the block was removed).
  The repo's `scripts/setup-prometheus-auth.py` should be checked for the
  same pattern before anyone reuses it (fragment filed).

## In flight at close

- **E08 canary (bulk-write-back, 100 books)** — op `01M00PGZKA0KBMPTZMAJTEKPD5`,
  ~23/100 at ~14:07, 0 failures, ~35 s/book. A waiter task watches it.
  **Verification procedure** (do this at restart if the session died first):
  the three before-state samples are in `/tmp/e08_before.txt` locally; re-run
  the same ffprobe over the three FILE paths and diff — the second sample
  (an *Abaddon's Gate* file whose embedded album tag said *Nemesis Games*)
  is the live specimen: its album tag flipping to Abaddon's Gate proves the
  repair works.
- **E08 FULL RUN: the nightly plan as approved is NOT executable.** Two
  findings from the canary, filed as a fragment:
  1. ~35 s/book, strictly serial (op logs process one book at a time) —
     library-wide (~40K organizer-tree books after excluding the hands-off
     iTunes tree; a large share of list rows live IN that tree) is **weeks**,
     not a night.
  2. 23/23 processed→written suggests the op rewrites tags unconditionally
     rather than skipping files whose tags already match the DB.
  Required before the full run: a tag-diff skip (probe ≈1 s vs rewrite
  ≈35 s; only mismatched files get rewritten) and a bounded worker pool per
  the concurrency mandate. `RunBulkWriteBack` lives in
  `internal/server/server_maintenance_deps.go:44`. The op's ConcurrencyKey
  serializes concurrent runs, so parallelism must go INSIDE the op.
  **Owner approved canary + nightly full run — the full run is deferred on
  these grounds, not on approval.**

## Next queue (post-restart order)

1. Confirm #2449 merged; clean its worktree; redeploy (`make deploy-debug`
   after `git pull`) so normalize-primary-flags is available.
2. **C111 prod run**: dry-run `maintenance.normalize-primary-flags` — expect
   `nil_ungrouped≈5702, false_ungrouped≈41, nil_grouped_left_for_election=0`.
   Nonzero nil_grouped or wildly different counts = STOP, re-census. Then
   apply. This also unblocks re-verifying the author-46627 merge (the
   instrument was blind on nil flags).
3. **E01 BookSig canary apply**: `maintenance.booksig-sidecar-migrate` with
   `{"dryRun":false,"limit":100}`; verify pair (GetBookByID non-nil BookSigV1
   AND stripped `book:` row), then full apply, then re-dry-run ≈0. Dry-run
   already measured: would migrate 26,159 of 63,841 (gate passed).
4. **E02 whole-library chapters**: cohort is complete (47/77 already had
   chapters). Whole-library = dry-run first (`apply:false`, no bookIds),
   expect ≈14.6K candidates, then apply. Do NOT schedule recurring until the
   "probed, found none" freshness marker exists.
5. **A04 tonight-class check**: run a mutating op (series-prune is the
   designated probe) and confirm `operation_changes` rows appear.
6. **CA12 fan-out gate**: after the next CodeQL analysis on main lands
   (baseline 321 open go/log-injection), count closures. If the #2445 barrier
   fix registered, shard the remaining per-site wraps (metafetch 99,
   server+handlers ~80). If it did NOT register, stop and re-diagnose — do
   not fan out blind.
7. **E08 full-run prerequisites**: tag-diff skip + bounded parallelism in
   the write-back path (see fragment), then schedule the real nightly run.
8. Backlog next picks: C514, C212, C413, C414, G110, F114, H115, C610
   repair-op follow-up (~12K dangling refs), 5 remaining offset-walkers
   (fragment `20260814-offset-walker-followups`). F-wave stays parked.

## Owner-side / decisions outstanding

- E07 residue: **2 ambiguous duplicate-PID groups** (same Alcatraz content
  in both trees) need a human pick of canonical file; everything else in
  that census resolved itself (the 8,984 record was stale).
- `.api-token` rotation still advised (old value echoed into a local
  transcript twice earlier today; LAN-only exposure, owner deprioritized).
- E04 title-match false-series repair: build + dry-run remain unstarted.

## Standing cautions (unchanged but load-bearing)

- Fragments in `todo.d`/`changelog.d` are HEADERLESS.
- Never `git checkout --` files in a worktree whose base isn't committed —
  it cost one re-implementation today (C510). Commit BEFORE mutation-testing.
- `POST /api/v1/itunes/pid-repair`: dry_run was query-only until #2447;
  older curl habits with a JSON body silently took the APPLY path.
- Prod stays on the debug build (`make deploy-debug`); `make deploy` pulls
  the LOCAL tree — always `git pull` first.
