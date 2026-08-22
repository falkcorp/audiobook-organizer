<!-- file: docs/agent-tasks/todo-completion/config/TASK-021-scan-and-fingerprint-the-assembled-source-downlo.md -->
<!-- version: 1.0.0 -->
<!-- guid: bfa6a086-1d69-49d0-8fbe-25c6c70f8388 -->
<!-- last-edited: 2026-08-21 -->

# TASK-021 — Scan and fingerprint the assembled-source download root as a read-only reference corpus (TODO.md L10750)

**Priority:** P1 · **Effort:** L · **Recommended subagent:** Opus-class · config subagent · **Why:** new config surface (a second scan root with different semantics — read-only, non-organizing, non-mutating) plus a new maintenance op that must never touch the active iTunes/organized tree; high blast-radius if the read-only guarantee is violated · **Depends on:** none · **Wave:** 7 · **REVIEW-CRITICAL (prod-data path): PR stays open for the owner; never weak-tier**

Source: `TODO.md` line 10750 as of commit 46628240 (later edits shift lines) — re-find it with `grep -n -F "**Fingerprint-confirmed dedup + shattered-book rea" TODO.md` (line numbers drift; the grep is built from the line's own text). Scope file: `scope-15.json`.

## ⛔ START HERE (do this first, exactly)

```bash
# ⛔ START HERE — do not touch code before this block succeeds
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/config-021-scan-and-fingerprint-the-assembled-source-downlo" -b agent/config-021-scan-and-fingerprint-the-assembled-source-downlo origin/main
cd "$REPO/.worktrees/config-021-scan-and-fingerprint-the-assembled-source-downlo"
git rebase origin/main
# Go task: do NOT run 'go work init .' — it breaks the build (ambiguous genproto imports)
```

(Protocol also in `docs/agent-tasks/ORCHESTRATION.md` — the inline block above is authoritative for this task.)

## Goal

Add a new config field (e.g. `ReferenceCorpusPaths []string` or a single `SourceCorpusRoot string`) distinct from `RootDir`, and a new maintenance op (e.g. `maintenance.scan-reference-corpus`) that walks it, computes AcoustID fingerprints via the existing fpcalc pipeline, and indexes them into `fpidx` — WITHOUT creating `Book`/`BookFile` rows in the primary organized-library tables and WITHOUT ever writing to the scanned files (per the standing NEVER-mutate-iTunes-tree rule, extended here to this new root too, since it is explicitly framed as a reference/identity corpus, not a library to organize).

## Background (verify before editing)

- Scope text: '94% file-level raw-fingerprint coverage... the one real gap: the assembled source-download root is NOT a configured scan path, so its folders are on disk but not in the DB.'
- Owner design constraint (2026-07-19, restated in the scope text): 'dedup AGAINST the original source as the identity reference, but keep the organized (primary) copy canonical; reflink new files on import. NEVER mutate the active iTunes tree — read-only at most.'
- internal/plugins/acoustid/backfill.go already has a bounded-worker-pool fingerprinting pipeline (registry.RunItems-based) per this repo's mandatory concurrency pattern for whole-library loops — reuse its fpcalc-calling logic rather than writing a new one.
- The existing `fpidx` LSH index (internal/database/pebble_store_lsh.go, internal/fingerprint/lsh.go) is a general-purpose fingerprint→candidate index; indexing the reference corpus into the SAME index (tagged with a distinct source/kind marker so query results can tell reference-corpus hits from organized-library hits) is simpler than building a parallel index.

- **Re-verify these anchors before editing** — line numbers drift; a zero-hit grep means STOP and report:
  ```bash
  grep -n 'RootDir ' internal/config/config.go   # 1 struct-field hit ~L623 — only one scan root exists in config today
  grep -rln fpidx internal/database internal/fingerprint internal/plugins/acoustid   # ≥3 files — fpidx LSH index already exists and is the target index for this reference corpus
  ```

### Reuse — don't invent

- Use `existing AcoustID backfill worker-pool pattern (registry.RunItems)` in `internal/plugins/acoustid/backfill.go` (verify: `grep -n RunItems internal/plugins/acoustid/backfill.go internal/operations/registry/run_items.go`) — do NOT write a parallel helper.

## Step-by-step

1. Add a new config field for the reference-corpus root(s) in internal/config/config.go, distinct from RootDir, with its own viper binding (mirroring RootDir's `viper.GetString("root_dir")` at L1635) and its own env var (e.g. `AUDIOBOOK_REFERENCE_CORPUS_ROOT`, per this repo's os.Getenv-not-viper convention for new env-driven config — verify which pattern RootDir itself actually uses before copying).
2. Design a minimal record shape for a reference-corpus file: enough to know its path + AcoustID fingerprint + which folder/'book' it groups under (for the containment match in part 3), WITHOUT reusing the full Book/BookFile schema (to keep 'not organized-library rows' true) — likely a new small table/keyspace, e.g. `internal/database` keys under a `refcorpus:` prefix.
3. Write a new maintenance op `maintenance.scan-reference-corpus` (new file under internal/plugins/maintenance/) that: walks the configured reference-corpus root (read-only, os.Open/ReadDir only, never os.Rename/os.Remove/os.WriteFile on anything under that root), computes fpcalc fingerprints via the same call reused from internal/plugins/acoustid/backfill.go, and indexes each into `fpidx` tagged with a `source="refcorpus"` marker plus the refcorpus folder key.
4. Bound the walk + fingerprint loop with a worker pool sized to runtime.NumCPU() (mandatory per this repo's concurrency rule for whole-library-scale loops) — follow registry.RunItems's existing shape rather than a raw `for range` loop.
5. Add a dry-run-first default (apply=false is meaningless here since nothing mutates the corpus itself, but DO gate actually writing into fpidx behind an explicit flag so a first run can be a report-only count of files-that-would-be-indexed) — consistent with this project's report-then-apply convention for maintenance ops.
6. Bump file-header versions on all new/touched files.

Then, always:
- Keep the change purely additive — do not touch adjacent code, do not reorder imports beyond the formatter, do not change signatures unless a step above says so explicitly.
- Bump the file header (`version` + `last-edited: 2026-08-21`) on every file you touch; keep existing guids. New files get a fresh guid (`uuidgen | tr A-Z a-z`).
- Add a changelog fragment `changelog.d/20260821_config_021.md` (NO file header; format per `changelog.d/README.md`: a `### Fixed|Changed|Added` heading, a `####` title, one paragraph).
- Do NOT edit `TODO.md` — the coordinator closes the source item in one commit per wave (every brief in a wave would otherwise collide on it). In your final report, state the exact `TODO.md` line text to check off. Never add new TODO items directly — use a `todo.d/` fragment (no header).

### Edge-case semantics (conservative defaults — treat unknown as unknown, never as disqualifying)

- A file under the reference-corpus root that is also, coincidentally, already present under RootDir (same content) — the op must not conflate them; tag by source so downstream matching (part 3) can distinguish 'this is the reference copy' from 'this is the organized copy'.
- The reference-corpus root does not exist on disk / is misconfigured — the op must fail loudly (clear error) rather than silently indexing zero files and reporting success.

## Tests

- internal/plugins/maintenance/scan_reference_corpus_test.go (new) — seed a temp dir with 2-3 fake audio fixtures, run the op, assert fpidx contains entries tagged source=refcorpus and no Book/BookFile rows were created in the primary store.
- A guard test asserting the op NEVER calls any file-mutating function (os.Remove/os.Rename/os.WriteFile) on paths under the reference-corpus root — e.g. via a fake filesystem wrapper that panics on any write call, run through the op end-to-end.

Anti-over-suppression test: `test: 'the read-only guard actually panics/fails the op if any write syscall were attempted against the reference-corpus root' (not just a happy-path assertion that no writes happened to happen)` — a known-good input still passes with the new guard active.

## How to test

```bash
go build ./... && go vet ./... && go test ./internal/config/... ./internal/plugins/acoustid/... ./internal/scanner/... -count=1
```
Do NOT use `make ci` as the gate: it is red on `main` from 10 pre-existing staticcheck findings unrelated to this task. Run `staticcheck ./<changed-pkg>/...` and fix only findings in files you touched. A failing test in a package you did not change is not yours — report it, do not fix it.

## Acceptance criteria

- [ ] `grep -n refcorpus internal/database/*.go` shows the new keyspace/marker.
- [ ] Running the op against a fixture dir populates fpidx entries queryable via the existing LSH lookup API, with zero new rows in the `book`/`book_file` tables.
- [ ] make ci passes.
- [ ] Anti-over-suppression test: `test: 'the read-only guard actually panics/fails the op if any write syscall were attempted against the reference-corpus root' (not just a happy-path assertion that no writes happened to happen)` — a known-good input still passes with the new guard active.
- [ ] Edge cases above hold (nil/empty/unknown never disqualify; a test asserts it where a filter/guard is added).
- [ ] Gate green: `go build ./... && go vet ./... && go test ./internal/config/... ./internal/plugins/acoustid/... ./internal/scanner/... -count=1` exits 0; `go vet`/lint clean.
- [ ] File headers bumped on every changed file (`grep -n "last-edited: 2026-08-21" <file>` hits for each).
- [ ] Changelog fragment present: `test -f changelog.d/20260821_config_021.md`.

## Commit message

```
feat(config): Scan and fingerprint the assembled-source download root as a (TODO L10750)

<why the change was needed; what it protects; what it deliberately does NOT change>

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>
```

## Done

STOP — report done with exact counts (`COMPLETED: n — ...` / `REMAINING: n — ...` / `BLOCKED: n — ...`); the coordinator owns push/PR/merge. Do NOT run `git push`, `gh pr`, or any merge.

## Idempotency / Rollback

**This task touches persisted data, files on disk, or an apply path. `git revert` does NOT restore data.** Mandatory: (1) the op/endpoint defaults to dry-run / `apply=false` and prints what it WOULD change; (2) every mutation is journaled through the existing undo ledger (`CreateOperationChange` — verify: `grep -rn "func.*CreateOperationChange" internal/database/*.go`) so `internal/undo` can replay it — a mutation without a journal row is a defect; (3) acceptance includes a test that applies on a fixture and then undoes via `internal/undo` and asserts the fixture is byte-identical; (4) the apply path refuses to start while a `library.scan` operation is running or queued (check the registry for an active scan before mutating — a running scan clobbers applied metadata). Idempotency: re-running in dry-run must report 0 pending changes after a successful apply. Rollback of the CODE = `git revert`; rollback of the DATA = the undo ledger, which is why (2) is not optional. PR stays open for the owner — the coordinator never admin-merges it.

If the first acceptance check below already passes at HEAD (``grep -n refcorpus internal/database/*.go` shows the new keyspace/marker.`), this task is already applied — run the acceptance checks instead of re-applying. Rollback = `git revert` the single commit; pre-existing behaviour is untouched (purely additive change).

## Coordinator notes

This is the load-bearing prerequisite for parts 2 and 3 of this same TODO item and for the whole shattered-book-reassembly feature (part 3 below) — without it there is no ground truth to match fragments against. review_critical=true given the standing 'NEVER mutate the active iTunes tree' rule extends to this new root by explicit owner design constraint.
