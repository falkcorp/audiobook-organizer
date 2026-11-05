<!-- file: docs/agent-tasks/platform-core/F2-14-production-shaped-store-boot-time.md -->
<!-- version: 1.1.3 -->
<!-- guid: 5062c77e-999c-4b26-b5e1-1a0f6f399bdd -->
<!-- last-edited: 2026-11-05 -->

# F2-14 — Production-shaped store generated at the P1 counts and sizes, run on `unit-lifecycle`'s triggers and nightly, with the boot-time measurement per migration step

| Field | Value |
|---|---|
| ID | F2-14 |
| Release / flip | R2, foundation PRs (F2); a generator, one committed count file and CI jobs |
| Wave | W25 |
| Tier | **T2** (Sonnet / high): a generator at counts the owner measured, plus two CI jobs and one measurement test |
| Depends on | OWNER-04 (P1 recorded), F2-12 |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f2-14-production-shaped
git -C "$REPO" fetch origin --tags
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
```

## Goal

Build the third leg of 09 §9.3 item 1: a **production-shaped store**, generated at the P1 counts and sizes (09 §9.7 P1:
keys, raw key bytes and raw value bytes per family), which the R2 steps run over on `unit-lifecycle`'s triggers and nightly,
for the per-family checks and **the boot-time measurement** (09 §9.3 "Boot time": the R2 binary's time per migration step,
`<namespace>_kernel_boot_phase_seconds{phase,step}`, and its steady boot, restart to `"ready":true` on `/health`). The
result sets the R2 release note's expected first-boot time and the runbook's `DEPLOY_READY_TIMEOUT` (default 300 s). At F2
there is no cutover step yet, so the job measures the current build's boot on the shaped store; every step registered from
F3-07 on shows up in the same measurement (14 K10, E1).

## Spec to read first

09 §9.3 item 1 (the production-shaped store sentence), "Boot time"; 09 §9.2.1 (the `extraction-gates` row: the shaped-store
run executes only on `unit-lifecycle`'s triggers and nightly; jobs ≤ 30 min; the `unit-lifecycle` trigger path list), §9.7
P1; 06 §6.9 (`kernel_boot_phase_seconds`: label set `phase` exactly `P0a` … `P8`, `step` per migration step, `""` for the
phase total); 14 E1, K10.

## Inputs (owner-provided; STOP if missing)

OWNER-04's recorded P1 answer: one JSON object per family `{prefix, keys, raw_key_bytes, raw_value_bytes}` (counts only,
never values; 09 §9.7). No file in the repo records it before this PR: the lead hands it to you with this brief. If it lists an `(unregistered)` prefix, include it with its counts and
name it in the PR body (09 §9.7 P1 branch B: it must be registered before F3).

## Base state

```bash
test -f internal/books/testdata/r2corpus/manifest.json && echo ok      # "ok" (F2-12), else STOP
test -f .github/workflows/extraction-gates.yml && echo ok              # "ok" (R1-02), else STOP
grep -c 'timeout-minutes' .github/workflows/extraction-gates.yml       # >=1 (each job has one)
test -f .github/workflows/unit-lifecycle.yml && echo ok                # "ok" (R1-28): its `on:` paths are the triggers to copy
grep -n "cron: '0 4 \* \* \*'" .github/workflows/nightly.yml           # 1 hit at f7ac5b11e (nightly schedule)
grep -rn 'kernel_boot_phase_seconds' core/kernel/ | head -2            # >=1 (R1-13), else STOP
test ! -e scripts/r2corpus/gen_production_shaped.py && echo absent     # "absent"
```

## Files

Create: `scripts/r2corpus/gen_production_shaped.py`, `internal/books/testdata/r2corpus/p1_counts.json`, the measurement
test `internal/books/production_shaped_test.go`, the generator's test `scripts/tests/test_gen_production_shaped.py`, and the Go row-writer under `scripts/r2corpus/shaped/` (Python cannot write Pebble rows, and R0's API is
too slow to reach production counts) with the build constraint `//go:build r2corpus_shaped` (coined here), built at the
R0 tag like F2-13's overlay program. Modify: `.github/workflows/extraction-gates.yml` (one new job),
`.github/workflows/nightly.yml` (one new job). **Do not touch** `scripts/r2corpus/overlay/` (F2-13's, same wave).

## Design (fixed here; deviations need a reason in the PR body)

1. `p1_counts.json` = OWNER-04's P1 rows verbatim, plus `{"_header": {...}, "source": "OWNER-04 P1", "recorded": "<date>"}`.
2. `gen_production_shaped.py --counts p1_counts.json --out <dir>`: copies F2-12's stage-1 store into `<dir>` and grows each
   family to its P1 key count with keys and values of the P1 average sizes (`raw_key_bytes/keys`, `raw_value_bytes/keys`),
   using row templates taken from the stage-1 rows of that family so every generated row decodes as R0 wrote it. A family
   with no stage-1 template is filled with the overlay program's encoding for it if F2-13 has one, else skipped and listed
   in the output report. The generated store is **never committed**; the job generates it each run. Before copying, the
   generator checks stage 1's store against `manifest.json`'s `stores` entry for it (every file, size and sha256; exit
   non-zero on a mismatch, so a store `.gitignore` thinned out is caught here too). After writing, it records the
   generated store in `<dir>/store_manifest.json` in the same per-file format as F2-12's `stores` (`[{"path", "size",
   "sha256"}]`, every file except itself; file name coined here). It does not edit `manifest.json`: the shaped store is not committed, so it has no
   entry there.
3. Measurement test `TestProductionShapedStore_BootTime` (coined here), skipped unless `R2_SHAPED_STORE` is set: boots the
   current build in process on a copy of the store, first runs F2-12's `verifyStoreFiles` helper (same package, in
   `internal/books/r2corpus_manifest_test.go`; do not edit that file) on the copy against `store_manifest.json` so the
   boot never runs on a partial store, then waits for readiness, and writes
   `$R2_SHAPED_OUT/boot_time.json` = `{"phases": [{"phase", "step", "seconds"}, ...], "ready_seconds": n}` (format
   coined here): every `kernel_boot_phase_seconds` sample plus the restart-to-ready seconds.
4. CI jobs (both upload `boot_time.json` as an artifact; both `timeout-minutes: 30`):
   - in `extraction-gates.yml`: job `production-shaped-store` (name coined here), run only when the change set matches
     `unit-lifecycle`'s path triggers (reuse that workflow's path list or the `changes` classifier R1-02 added; do not invent a
     second list);
   - in `nightly.yml`: job `production-shaped-store`, every night.
   Pin every action to a SHA (CLAUDE.md "GitHub Operations"); copy the SHAs the file already uses.
5. F2-15 (which depends on this row) extends the same job to run the cutover suite over `R2_SHAPED_STORE`; leave the job
   name and the env names stable.

## Tests

- `scripts/tests/test_gen_production_shaped.py` (name coined here): with a tiny counts file, the generator produces exactly
  the requested key count per family and the average sizes within 10 %; unknown families are reported, not invented; a
  stage-1 store with one file missing or changed against the manifest makes the generator exit non-zero; the
  `store_manifest.json` it writes lists every file of the generated store.
- `TestProductionShapedStore_BootTime` itself, run locally on a small generated store.

## Acceptance

```bash
python3 -m pytest scripts/tests/test_gen_production_shaped.py -q                                   # passed
SHAPED=$(mktemp -d); OUT=$(mktemp -d)
python3 scripts/r2corpus/gen_production_shaped.py --counts internal/books/testdata/r2corpus/p1_counts.json --out "$SHAPED" --scale 0.01   # exit 0; report lists every family
test -f "$SHAPED/store_manifest.json" && echo ok                                                   # ok
R2_SHAPED_STORE="$SHAPED" R2_SHAPED_OUT="$OUT" go test ./internal/books/ -run TestProductionShapedStore_BootTime -count=1   # ok
python3 -c "import json,sys;d=json.load(open(sys.argv[1]+'/boot_time.json'));print(any(s['phase']=='P4b' for s in d['phases']))" "$OUT"   # True
grep -c 'production-shaped-store' .github/workflows/extraction-gates.yml .github/workflows/nightly.yml   # >=1 each
make ci                                                                                            # exit 0
```

(`--scale` is a coined option for local runs; CI runs at scale 1.)

## Out of scope

The cutover suite and per-family checks (F2-15, from F3-07 on); setting `DEPLOY_READY_TIMEOUT` (R2-01's runbook uses the
measured number); any product code.

## Rollback

Revert the PR; the two jobs disappear.

## Repo rules

File headers on the script, the test, both workflow files (bump version and `last-edited`, keep guids) and `p1_counts.json`
(`_header` object); conventional commit (`ci(r2): production-shaped store and boot-time measurement`) with
`Co-Authored-By: Claude <noreply@anthropic.com>`; push workflow changes with `git push`, never the contents API (CLAUDE.md);
`gh pr merge <n> --rebase`; `changelog.d/` fragment.
