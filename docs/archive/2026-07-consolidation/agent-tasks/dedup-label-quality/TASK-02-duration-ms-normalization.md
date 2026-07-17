<!-- file: docs/agent-tasks/dedup-label-quality/TASK-02-duration-ms-normalization.md -->
<!-- version: 1.0.0 -->
<!-- guid: cc7f15f6-17e3-4183-9675-6cd0a4b1b4b7 -->
<!-- last-edited: 2026-07-10 -->

# TASK-02 — Duration ms/sec normalization at the mining boundary (write-boundary repair VERIFIED, not re-added) (INIT-1 T2)

**Gate:** SPEC -> GATED APPLY. Code PRs run autonomously (worktree/PR/CI). EVERY prod-data mutation (T7 rebuild-gold-labels re-mine, any calibration threshold/config apply, prod re-runs) is dry-run FIRST, then a real AskUserQuestion apply decision — never a text-reply approval.
**File-ownership:** none owned by another initiative. Do NOT touch `internal/database/pebble_store.go`, `internal/database/embedding_store.go`, or `internal/dedup/engine.go` (INIT-2-owned). Do NOT touch `internal/database/pebble_store_bookfiles.go`, `internal/plugins/maintenance/duration_backfill.go`, or `internal/itunes/service/importer.go` either — those are VERIFY-ONLY in this task (see Goal). This task shares `internal/dedup/dataset/builder.go` + `builder_test.go` with TASK-01 — it MUST start only after TASK-01's PR merges; rebase on origin/main first.

**Priority:** P0 · **Effort:** S · **Recommended subagent:** Sonnet-class · small helper + one integration point with unit-semantics edge cases · **Why:** tiny diff but per-file-vs-aggregate semantics are correctness-sensitive · **Depends on:** TASK-01

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dedup-label-quality-duration-ms-normalization" -b agent/dedup-label-quality-duration-ms-normalization origin/main
cd "$REPO/.worktrees/dedup-label-quality-duration-ms-normalization"
git rebase origin/main
# Precondition: TASK-01 must already be merged. Verify — if this returns 0 hits, STOP:
grep -n "func SharesIdentity" internal/dedup/dataset/rules.go
```

## Goal

Make ms-scale duration corruption impossible to propagate into gold labels. **What already exists (verify, never re-add, never weaken):** the implied-bitrate heuristic is ALREADY shared and exported — `database.DurationLooksLikeMillis` in `internal/database/duration_sanity.go:36` (CONS-18); the BookFile write chokepoints (`CreateBookFile`/`UpsertBookFile`/`BatchUpsertBookFiles`) ALREADY repair ms-scale durations in place AND `slog.Warn` via `normalizeBookFileDuration` (`duration_sanity.go:61`, called at `pebble_store_bookfiles.go:192`/`:785`/`:849`); `duration_backfill.go:33` is a thin wrapper already delegating to the shared heuristic; the iTunes importer already converts ms→sec (`trackDurationSeconds`, CONS-16). **The genuinely-new work is one thing:** historical rows written BEFORE CONS-18 still hold ms-scale values, and the dataset builder consumes them raw — so normalize at READ time in `buildFeatures`, **per file, before summing** (the bitrate test is a per-file contract: this file's size vs this file's duration; a book with one corrupted file among clean files would defeat any aggregate-level check). Add one small exported pure helper `database.NormalizeDurationSec` beside the heuristic — do NOT create a new package, do NOT move `DurationLooksLikeMillis`, do NOT invent a second heuristic.

## Background (verify before editing)

- Live prod still holds ms-scale rows written before the CONS-18 chokepoint repair landed (Jul-8 findings: 20,810,840 vs 21,171 "sec" on the same-ASIN pair) — the chokepoint only fixes rows as they are (re)written, so the mining boundary must normalize what it reads.
- The heuristic to reuse: `database.DurationLooksLikeMillis(fileSize int64, durationSec int) bool` (`internal/database/duration_sanity.go:36`) — millis when duration-as-seconds implies < 4 kbps for the known file size; it double-checks the /1000-corrected value lands inside the 4 kbps–3 Mbps band (duration_sanity.go:46-52), so genuine low-bitrate files are never flagged. Do NOT duplicate its constants or logic anywhere.
- The store repair to NOT touch: `normalizeBookFileDuration` (`duration_sanity.go:61`) mutates `file.Duration /= 1000` + warns at the three chokepoints. It is shipped, correct, and deliberately a REPAIR (not warn-only). Weakening it to warn-only would regress CONS-18; adding a second warn beside it would be redundant noise.
- The mining consumer: `buildFeatures` (`internal/dedup/dataset/builder.go:129`) sums per-file durations into `f.TotalDurationSec` (~builder.go:169). Two branches per file: fpcalc-measured `fl.AcoustIDFingerprintDurationSec` (float, trusted as-is — leave alone) and the fallback `fl.Duration` (int container seconds — the corruptible one). `fl.FileSize` is right there for the per-file bitrate test. `durationRatio` (builder.go:174) then consumes the sums — do not modify it.
- Unit semantics (repeat: also in acceptance): `fileSize <= 0` or `duration <= 0` → UNKNOWN, return the value unchanged, never normalize. Unknown is non-disqualifying.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n 'func DurationLooksLikeMillis\|func normalizeBookFileDuration' internal/database/duration_sanity.go   # both exist, 2 hits
  grep -n 'normalizeBookFileDuration' internal/database/pebble_store_bookfiles.go                                # shipped chokepoint repair, 3 hits
  grep -n 'database.DurationLooksLikeMillis' internal/plugins/maintenance/duration_backfill.go                   # wrapper already delegates, >=1 hit
  grep -n 'func trackDurationSeconds' internal/itunes/service/importer.go                                        # importer fix intact, 1 hit
  grep -n 'func buildFeatures\|func BuildExample\|func durationRatio' internal/dedup/dataset/builder.go          # edit target, 3 hits
  ```
  If any grep returns 0 hits, STOP and report — do not guess. (The first three greps are the "already shipped" proof — if any is missing, the premise of this task changed; report instead of adding code.)

## Step-by-step

1. `internal/database/duration_sanity.go` — add ONE exported pure helper directly below `DurationLooksLikeMillis` (bump the file header):
   ```go
   // NormalizeDurationSec returns durationSec/1000 when DurationLooksLikeMillis
   // reports the value is milliseconds, else durationSec unchanged. Inputs
   // <= 0 (unknown) are returned unchanged. Pure read-time twin of
   // normalizeBookFileDuration (which repairs at the write chokepoint); used by
   // the dedup dataset builder to normalize historical pre-CONS-18 rows at
   // read time.
   func NormalizeDurationSec(fileSizeBytes int64, durationSec int) int
   ```
   Body: `if DurationLooksLikeMillis(fileSizeBytes, durationSec) { return durationSec / 1000 }; return durationSec`. Touch nothing else in the file — `normalizeBookFileDuration` and the constants stay exactly as shipped.
2. `internal/dedup/dataset/builder.go` — in `buildFeatures`' per-file summing loop, normalize the fallback branch PER FILE before adding: where the loop does `total += float64(fl.Duration)`, use `total += float64(database.NormalizeDurationSec(fl.FileSize, fl.Duration))` (adapt to actual local names — use the greps). Leave the `AcoustIDFingerprintDurationSec` branch untouched. Do NOT normalize the summed total, and do not modify `durationRatio`.
3. Verify-only (no code): confirm the three shipped protections via the anchor greps above (chokepoint repair, wrapper delegation, importer conversion). They are cited in the PR description as verified, not changed.
4. Purely additive elsewhere: no signature changes, no new package, no touching the memdb/MemStore twins. Run the FULL short suite because a shared package (`internal/database`) gained an exported symbol.
5. Tests:
   - `internal/database/duration_sanity_test.go` (exists — extend): `TestNormalizeDurationSecMillisDetected` (ms-scale value ÷1000), `TestNormalizeDurationSecPlausibleUnchanged` (sane seconds value untouched), `TestNormalizeDurationSecUnknownUnchanged` (fileSize 0 / duration 0 → unchanged).
   - `internal/dedup/dataset/builder_test.go` — `TestBuildExampleNormalizesDurationRatio`: an ms-vs-sec fixture pair (e.g. 20810840 ms-scale vs 20810 sec with realistic file sizes) yields ratio ≈ 1.0, not ≈ 0.001. `TestBuildFeaturesMixedCorruptFiles`: one ms-corrupted file among clean seconds-files → `TotalDurationSec` equals the correct per-file-normalized sum (this fixture is the proof that normalization is per-file; an aggregate-level implementation fails it). Anti-over-suppression: a genuinely short part (300 sec vs 20000 sec, plausible sizes) STILL yields a low ratio.
   - `internal/plugins/maintenance` and `internal/database` existing tests must stay green unmodified (nothing there changed behavior).
6. Bump the file header (version + last-edited) on every file you touch; keep existing guids.

## How to test

```bash
go test ./internal/database/... ./internal/dedup/dataset/... -race
go test ./... -short   # full suite — internal/database gained an exported symbol
make ci
```
staticcheck is red on main (pre-existing backlog #1796) — scope staticcheck to files you changed; the merge gate is Minimal CI green.

## Acceptance criteria

- [ ] `grep -n "func NormalizeDurationSec" internal/database/duration_sanity.go` hits (1 declaration, beside `DurationLooksLikeMillis`)
- [ ] `grep -n "NormalizeDurationSec" internal/dedup/dataset/builder.go` hits INSIDE the per-file summing loop (mining boundary normalized per file, not on the aggregate)
- [ ] `go test ./internal/dedup/dataset/ -run 'TestBuildExampleNormalizesDurationRatio|TestBuildFeaturesMixedCorruptFiles' -v` passes, including the anti-over-suppression genuine-part case (a real short part still gets a low ratio)
- [ ] unknown semantics: `TestNormalizeDurationSecUnknownUnchanged` passes (fileSize ≤ 0 or duration ≤ 0 → unchanged)
- [ ] verify-only files untouched: `git diff origin/main --name-only` contains NEITHER `pebble_store_bookfiles.go` NOR `duration_backfill.go` NOR `importer.go`; `grep -n "normalizeBookFileDuration" internal/database/pebble_store_bookfiles.go` still shows 3 chokepoint call sites (shipped repair intact)
- [ ] `grep -n "func trackDurationSeconds" internal/itunes/service/importer.go` still hits (importer fix intact, untouched)
- [ ] `go test ./... -short` green; `make ci` green
- [ ] File headers bumped on every changed file

## Commit message

```
fix(dedup): normalize ms/sec durations per-file at the mining boundary (INIT-1 T2)

Historical BookFile rows written before the CONS-18 write-chokepoint repair
still hold millisecond-scale durations, and the dataset builder summed them
raw into TotalDurationSec, so durationRatio saw 1000x unit mismatches. Adds
database.NormalizeDurationSec (pure read-time twin of the shipped
normalizeBookFileDuration repair, same DurationLooksLikeMillis heuristic) and
applies it per file, before summing, in buildFeatures. The shipped chokepoint
repair, backfill wrapper, and importer conversion are verified untouched.

Co-Authored-By: Claude <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/dedup-label-quality-duration-ms-normalization
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `grep -n "NormalizeDurationSec" internal/dedup/dataset/builder.go` hits, this task is already applied — run the acceptance checks instead of re-applying. Rollback = revert the commit; no store write path is touched by this task (the chokepoint repair is prior shipped CONS-18 behavior, outside this package), so zero stored bytes differ either way — only the read-time mining behavior reverts.
