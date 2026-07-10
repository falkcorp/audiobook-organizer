<!-- file: docs/agent-tasks/dedup-label-quality/TASK-01-rules-not-dup-guards.md -->
<!-- version: 1.0.0 -->
<!-- guid: 1bf0df61-83ef-4e04-9a79-adf3aac5397f -->
<!-- last-edited: 2026-07-10 -->

# TASK-01 — Guard the not_dup mining rules: partVsWhole identity guard, missingFile → unsure (INIT-1 T1) [⚠ review-critical]

**Gate:** SPEC -> GATED APPLY. Code PRs run autonomously (worktree/PR/CI). EVERY prod-data mutation (T7 rebuild-gold-labels re-mine, any calibration threshold/config apply, prod re-runs) is dry-run FIRST, then a real AskUserQuestion apply decision — never a text-reply approval.
**File-ownership:** none of this task's files are owned by another initiative. Do NOT touch `internal/dedup/engine.go` (INIT-2 owns it; the engine-side ratio alignment is TASK-08, a later wave) and do NOT touch `internal/database/embedding_store.go`.

**Priority:** P0 · **Effort:** M · **Recommended subagent:** Sonnet-class · rules-logic + tests subagent · **Why:** changes label ground truth for all future mining — needs judgment, not mechanics; coordinator line-reviews the diff · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-label-quality-rules-not-dup-guards" -b agent/dedup-label-quality-rules-not-dup-guards origin/main
cd "$REPO/.worktrees/dedup-label-quality-rules-not-dup-guards"
git rebase origin/main
```

## Goal

Stop the two deterministic mining rules in `internal/dedup/dataset/rules.go` from emitting contaminated `not_dup` gold labels: (a) `missingFile` must return `unsure` instead of `not_dup` (file absence is evidence-free for dup-ness), and (b) `partVsWhole` must return `unsure` — never `not_dup` — when the pair shares an ASIN, a version-group, or an identical primary file path (the 2026-07-08 calibration hand-verified that every such pair it flagged was a REAL duplicate mislabeled by a ms/sec duration corruption). To make (b) possible, add `ASIN` and `VersionGroupID` snapshot fields to `database.BookFeatures` and populate them in the dataset builder. REUSE the existing constant `partVsWholeRatioMax` (do NOT add a new ratio constant) and the existing `database.LabeledExample`/`BookFeatures` types — extend, never fork.

## Background (verify before editing)

- `partVsWhole` in `internal/dedup/dataset/rules.go` (~line 106) returns `not_dup` when both `TotalDurationSec` > 0 and `ex.DurationRatio < partVsWholeRatioMax` (const = 0.5, ~line 17). It misfires when one side's duration is stored in milliseconds (ratio ≈ 0.001): verified prod mislabels include Way of the Wolf ×3 (ASIN B002V8MAAM, same file path, 21171 vs 20810840 "sec"), Foundation and Empire (B003FCV4O6, 34535 vs 57989869), Alcatraz vs the Evil Librarians (B005GGGC3M, same path). Source: `.claude/notes/2026-07-08-dedup-calibration-findings.md`.
- `missingFile` in the same file (~line 62) returns `("not_dup", "side A/B has no resolvable files", true)` when a side has `FilesExist == false` and implausible file size. 194 identical-title `not_dup` labels came from this rule.
- 100% of `not_dup` gold labels are `label_source=rule` (zero human) — no human decision is contradicted by this change.
- `BookFeatures` in `internal/database/dedup_label.go` has NO identity fields today (Title/Author/PrimaryPath/durations/etc. only). `database.Book` carries `ASIN *string` and `VersionGroupID *string` — nil means "unknown", which is NON-DISQUALIFYING: an empty identity field must make the guard fall through to today's behavior, never force `unsure`.
- The builder that populates `BookFeatures` is `buildFeatures` inside `internal/dedup/dataset/builder.go`, called from `BuildExample` (~line 72).
- Downstream note (do NOT change it): `internal/plugins/dedup/dataset_backfill.go` dismisses rule-confirmed `not_dup` candidates; `unsure` rows are simply not dismissed — that is the intended safer behavior.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'func partVsWhole\|func missingFile\|partVsWholeRatioMax' internal/dedup/dataset/rules.go
  grep -n 'func BuildExample\|ex.DurationRatio = durationRatio\|func durationRatio' internal/dedup/dataset/builder.go
  grep -n 'dedupLabelPfx = "dedup:label:"' internal/database/dedup_label.go
  grep -n "type BookFeatures struct" internal/database/dedup_label.go        # edit target, 1 hit
  grep -n "func buildFeatures" internal/dedup/dataset/builder.go             # edit target, 1 hit
  grep -n "ASIN" internal/database/bookcore.go                               # Book identity fields exist, >=1 hit
  grep -n "VersionGroupID" internal/database/bookcore.go                     # >=1 hit
  ```
  If any grep returns 0 hits, STOP and report — do not guess.

## Step-by-step

1. `internal/database/dedup_label.go` — inside `type BookFeatures struct` (grep above), append two fields (purely additive, keep every existing field and tag untouched):
   ```go
   // ASIN is the book's Amazon/Audible ID ("" when the book has none).
   ASIN string `json:"asin,omitempty"`
   // VersionGroupID links books that are versions of the same work ("" when ungrouped).
   VersionGroupID string `json:"version_group_id,omitempty"`
   ```
   Old stored rows unmarshal these as `""` — that is the intended "unknown" value.
2. `internal/dedup/dataset/builder.go` — in `buildFeatures`, populate the two fields from the `*database.Book` argument, dereferencing the `*string` pointers with nil → `""`. Do not reorder or modify any existing assignment.
3. `internal/dedup/dataset/rules.go` — add an EXPORTED helper (name it exactly `SharesIdentity` — TASK-04's suspicious-label predicate reuses it from `internal/server/handlers/dedup`, so it must be exported; one implementation, two consumers):
   ```go
   // SharesIdentity reports whether the pair carries matching hard-identity
   // evidence (same ASIN, same version group, or identical primary path).
   // Empty values are UNKNOWN and never match — unknown is non-disqualifying.
   // Consumers: partVsWhole's unit-corruption guard (this file) and the
   // suspicious-label queue predicate (TASK-04).
   func SharesIdentity(ex database.LabeledExample) bool
   ```
   Match rules: `ex.A.ASIN != "" && ex.A.ASIN == ex.B.ASIN`; OR `ex.A.VersionGroupID != "" && ex.A.VersionGroupID == ex.B.VersionGroupID`; OR `ex.A.PrimaryPath != "" && ex.A.PrimaryPath == ex.B.PrimaryPath`.
4. In `partVsWhole`: BEFORE the existing ratio branch returns `not_dup`, check `SharesIdentity(ex)`; when true return `("unsure", fmt.Sprintf("duration ratio %.3f but pair shares identity — suspected unit corruption", ex.DurationRatio), true)`. Keep the existing `not_dup` return for non-identity pairs exactly as is (same constant, same reason string).
5. In `missingFile`: change the label in each `not_dup` return to `unsure`, keeping the reason strings ("side A has no resolvable files" / "side B ...") and the `true` matched flag unchanged. No conditional — this rule can no longer emit `not_dup` at all.
6. Edge-case semantics (spelled out; also asserted in acceptance): both-identity-empty pair with ratio < 0.5 → still `not_dup` (guard falls through); one-side-empty ASIN with matching paths → `unsure` (path arm fires); durations unknown (either ≤ 0) → rule does not match at all (existing behavior, keep it).
7. `internal/dedup/dataset/rules_test.go` — add regression tests using fixtures shaped like the three Jul-8 verified mislabels (name them in the test): `TestPartVsWholeSharedASINGoesUnsure` (B002V8MAAM shape: same ASIN + same path + ratio ≈ 0.001), `TestPartVsWholeSharedVersionGroupGoesUnsure`, `TestPartVsWholeSharedPathGoesUnsure` (B005GGGC3M shape), `TestMissingFileGoesUnsure`, and the anti-over-suppression case `TestPartVsWholeGenuineStillNotDup` (disjoint ASINs/paths, sane durations, ratio 0.3 → still `not_dup`).
8. `internal/dedup/dataset/builder_test.go` — assert `buildFeatures` populates `ASIN`/`VersionGroupID` (set and nil pointer cases).
9. Keep the change purely additive elsewhere — do not touch `wholeBookSignatureMatch`, `Classify` ordering, any signature, or `internal/dedup/engine.go`.
10. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
go test ./internal/dedup/dataset/... ./internal/database/... -race
go test ./... -short   # full suite — BookFeatures is a shared type; run everything
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -n "func SharesIdentity" internal/dedup/dataset/rules.go` hits (1 declaration, exported — TASK-04 depends on it)
- [ ] `grep -c "not_dup" internal/dedup/dataset/rules.go` shows `missingFile` no longer returns it: `grep -n 'no resolvable files' internal/dedup/dataset/rules.go` lines sit inside `unsure` returns (inspect)
- [ ] `grep -n 'asin,omitempty' internal/database/dedup_label.go` hits
- [ ] `go test ./internal/dedup/dataset/ -run 'TestPartVsWhole|TestMissingFile' -v` — all new tests pass, including `TestPartVsWholeGenuineStillNotDup` (anti-over-suppression: a genuine part-vs-whole pair with disjoint identity is STILL labeled `not_dup`)
- [ ] both-identity-empty + low ratio still yields `not_dup` (unknown is non-disqualifying — asserted in `TestPartVsWholeGenuineStillNotDup` or a dedicated case)
- [ ] `go test ./... -short` green; `make ci` green; vet clean on changed packages
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026" <file>` shows today)

## Commit message

```
fix(dedup): guard not_dup mining rules — identity-sharing pairs go unsure (INIT-1 T1)

partVsWhole misfired on ms/sec-corrupted durations and labeled verified real
duplicates (shared ASIN/version-group/path) as not_dup; missingFile labeled
pairs not_dup on file absence alone. Both now emit unsure in those cases, so
the gold-label set stops poisoning calibration at the mining source.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/dedup-label-quality-rules-not-dup-guards
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "func SharesIdentity" internal/dedup/dataset/rules.go` hits, this task is already applied — run the acceptance checks instead of re-applying. Rollback = revert the commit; no stored label row changes until the operator-gated TASK-07 re-mine, and `wholeBookSignatureMatch`/`Classify` ordering remain untouched.
