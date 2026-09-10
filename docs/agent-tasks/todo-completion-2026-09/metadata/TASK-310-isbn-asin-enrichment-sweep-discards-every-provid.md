<!-- file: docs/agent-tasks/todo-completion-2026-09/metadata/TASK-310-isbn-asin-enrichment-sweep-discards-every-provid.md -->
<!-- version: 1.6.0 -->
<!-- guid: ab005c24-90d8-49f2-84cc-46b9b784a45c -->
<!-- last-edited: 2026-09-10 -->

# TASK-310 — ISBN/ASIN enrichment sweep discards every provider search error, making a circuit-breaker-open or throttled provider indistinguishable from a legitimate zero-result search (SF-03)

> **Status 2026-09-10:** 🆕 NEW — Wave 3 audit finding `SF-03` (audit_silent_failures_pipeline.json) · adversarial re-check 2026-09-10: **CONFIRMED**

**Priority:** P0 · **Effort:** S · **Recommended subagent:** Haiku-class · metadata subagent · **Depends on:** none · **Wave:** per ../orchestration.md (collision-aware) 

Source: Wave 3 audit finding `SF-03` (audit_silent_failures_pipeline.json) · adversarial re-check 2026-09-10: **CONFIRMED**. Verified at HEAD `42d187168` on 2026-09-10; line numbers drift — re-verify with the greps below before editing.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # the primary checkout (same path convention as every carried brief)
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/metadata-310" -b agent/metadata-310-isbn-asin-enrichment-sweep-discards-ever origin/main
cd "$REPO/.worktrees/metadata-310"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol in `../ORCHESTRATION.md` and `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Stop discarding the error: log it (at minimum a sampled warning naming the source and error), and consider not counting a book as "checked" (or track it separately) when every source call errored rather than returned zero results, so a provider outage is visible in EnrichMissingISBNs' own summary line.

Why it matters: The nightly EnrichMissingISBNs sweep (isbn.go:174-281) walks the whole library and reports `checked N, updated M` with no way to tell "M books genuinely have no ISBN/ASIN anywhere" from "every provider call this run was throttled or circuit-broken and we never actually searched." This is the same failure shape as the 2026-09-08 empty-refetch incident this package's cache.go was specifically hardened against (an empty/failed response getting treated as an authoritative negative) — except unlike cache.go, isbn.go has no SourceHash/preserve-on-empty defense at all, and unlike the AI batch phase (ai_batch_phase.go), there is no failure counter, no abort threshold, and no per-book "why" recorded. A book can sit unenriched indefinitely while every log line says the sweep is healthy.

## Background (verify before editing)

- searchSourceForISBN (isbn.go:394-414) and searchSourceForASIN (isbn.go:418-435) call `results, _ = src.SearchByTitleAndAuthor(ctx, title, author)` (lines 399, 423) and `results, _ = src.SearchByTitle(ctx, title)` (lines 402, 426), discarding the error return entirely. The metadata.MetadataSource these run against is typically internal/metadata.ProtectedSource (circuitbreaker.go:220-234, :236-250), whose SearchByTitle/SearchByTitleAndAuthor return a real error whenever `ps.allowThrottle(ctx)` rejects the call (rate limit) or `ps.breaker.AllowRequest()` rejects it (circuit open after repeated failures) — i.e. exactly the conditions this package's own sibling code (isbn.go's cousin, service_apply.go, the throttle registry) treats as first-class, loggable events elsewhere. Here the error is thrown away before the caller (EnrichBookISBN, isbn.go:87-114 and :117-134) ever sees it, so a provider outage renders as `isbn == ""` / `asin == ""`, the same value a genuine "no such book" search produces.
- Anchor: `internal/metafetch/isbn.go:399` (audit `SF-03`, confidence high, severity critical).
- **Adversarial re-check (2026-09-10, `state/final/adversarial_top11.json`): CONFIRMED** — All four `results, _ = src.Search…` discards confirmed; ProtectedSource.SearchByTitle/SearchByTitleAndAuthor (circuitbreaker.go:219-245) return non-nil errors when throttled or breaker-open.
  - Blast radius: EnrichMissingISBNs (isbn.go:174-281), EnrichBookISBN (:87-134); no test targets this path.
  - Existing tests to extend: none for this path
  - Standing-ban contact: none; isbn.go touched by 3 of the last 5 commits on main — rebase carefully

- **Re-verify these anchors before editing** — a zero-hit grep means STOP and report:
  ```bash
  test -e internal/metafetch/isbn.go   # the file the finding is anchored to still exists (-e: a directory anchor is valid too)
  sed -n '81,120p' internal/metafetch/isbn.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  sed -n '388,441p' internal/metafetch/isbn.go   # expect the code described under Background (drifted lines: re-find by the quoted text)
  ```

## Step-by-step

1. Re-run the re-verify anchors; read the surrounding function end-to-end and confirm the finding still holds at HEAD (if it does not, STOP and report).
2. Implement the fix described under Goal in `internal/metafetch/isbn.go` (and any sibling that shares the same shape — grep for the pattern before assuming there is one copy).
3. Write the regression test first (it must fail against the pre-fix code), then make it pass.
4. Run the gate; add the changelog fragment; bump headers; report with exact counts.

Then, always:
- Keep the change purely on-target — do not touch adjacent code, do not "clean up while you're in there".
- Bump the file header (`version` + `last-edited: 2026-09-10`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260910_metadata_310.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave. In your final report, state the exact `TODO.md` line text (or `todo.d` fragment) to check off.

## Tests

- A regression test that reproduces the defect described in Background and fails on the pre-fix code.
- Existing package tests stay green (`-count=1`).

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/metafetch/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched.

## Acceptance criteria

- [ ] Every re-verify anchor above still hits (or the report says which moved and where).
- [ ] The tests listed above exist and pass; a regression test reproduces the original defect and fails on the pre-fix code.
- [ ] Gate green: the command in **How to test** exits 0; `go vet`/lint clean on touched packages.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-09-10" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260910_metadata_310.md`.

## Commit message

```
fix(metadata): ISBN/ASIN enrichment sweep discards every provider search error, makin (SF-03)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5.1 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

Decide this FIRST and write the answer in your report: **does the fix add or change a path that writes, moves, or deletes persisted data or files** (an apply/repair/delete/migration path)?

- **NO** — the fix is a lock, a bound, a check, an error propagated, a header, a config value: pure code change. Rollback = `git revert` the commit. Already-done check = the re-verify anchors above show the new code (add the exact `grep -n '<new symbol or string>' <file>` you used to your report). Do NOT invent a dry-run/`apply` parameter that the Goal did not ask for.
- **YES** — stop and report before implementing: this brief was classified as a standard-lane code change, and a new write path needs the review-critical protocol (dry-run default, undo journal, owner hold).

## Coordinator notes

Standard lane: coordinator may admin-merge on a green gate.
