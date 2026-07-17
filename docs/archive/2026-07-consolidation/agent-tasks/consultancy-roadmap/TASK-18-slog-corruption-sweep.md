<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-18-slog-corruption-sweep.md -->
<!-- version: 1.0.0 -->
<!-- guid: c139ec52-9bbb-4ece-809f-bce1de4b3e77 -->
<!-- last-edited: 2026-07-03 -->

# TASK-18 — Repo-wide slog-corruption sweep: duplicate keys, `"value0"` literals, dropped args, `!BADKEY` (QUAL-1/BUG-4)

**Priority:** P1 · **Effort:** M · **Recommended subagent:** Haiku ×N (parallel-sweep), Sonnet coordinator · **Wave:** 5 · **Depends on:** none functionally, but runs ALONE after waves 1–4 complete and merge (touches dozens of files repo-wide — do not run concurrently with any other wave to avoid rebase storms)

## ⛔ START HERE (do this first, exactly)

This brief is a **sweep spec**, not a single-worktree task — it is meant to be
handed to the `/parallel-sweep` slash command (see
`docs/superpowers/specs/parallel-sweep.md`), which will create one worktree +
branch **per package** listed in "Wave partitioning" below. Do NOT hand-edit
dozens of files in one worktree.

Coordinator (Sonnet) setup:

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-18-slog-corruption-sweep" -b agent/cr-18-slog-corruption-sweep origin/main
cd "$REPO/.worktrees/cr-18-slog-corruption-sweep"
git rebase origin/main
```

Each Haiku child spawned by `/parallel-sweep` gets its OWN worktree +
sub-branch (e.g. `agent/cr-18-slog-corruption-sweep/internal-ai`) and owns one
or more **whole packages** — never a partial file, never a file another child
also touches. The coordinator worktree above is only for running the
repo-wide detection pass and final rollup PR bookkeeping.

## Goal

A mass printf→slog conversion (pre-dating this task) corrupted logging calls
across dozens of files. Fix every occurrence of these four defect classes
without changing any non-logging behavior:

- **(a) Data dropped entirely** — a slog message string still contains a
  `%`-verb (`%q`, `%s`, `%d`, ...) but the call passes **zero** args, so the
  interpolated value is unrecoverable from logs.
- **(b) `%`-verb left in the message with args passed positionally after
  it** — the message text still has `%q`/`%s` placeholders, and the
  corresponding data is appended as bare (non-keyed) args after already-keyed
  attrs. `log/slog`'s alternating key/value parser then treats the first bare
  arg as an attr **key** and the next as its **value**, corrupting the log
  record (this is the `!BADKEY` failure mode when the types don't line up).
- **(c) Literal `"value0"` (and `"value1"`, `"value2"`, ...) used as an attr
  key** — the conversion tool synthesized placeholder key names and never
  replaced them; worse, several sites also pass the literal string
  `"value0"` as the *value* of the `"value0"` key (`"value0", "value0", ...`),
  meaning the real original datum was discarded and replaced with the
  placeholder string itself.
- **(d) Duplicate real-looking keys in one call** — the same key literal
  (`"id"`, `"count"`, `"value"`, `"response"`, ...) appears twice or three
  times in a single `slog.*` call, each time paired with a *different*
  variable, silently overwriting one field with another in structured-log
  output (breaks JSON log parsing / log-query filters).

None of these are behavior bugs — only log quality/diagnostics — but they sit
directly on the merge/quarantine/metafetch/fingerprint/plugin-registry paths
where prod debugging happens (see QUAL-1/BUG-4 in
`docs/consultancy/04-code-quality.md:158-181`), and per CTR-4
(`docs/consultancy/04-code-quality.md:207-216`) the plugin-registry instance
(class a) is actively hiding which plugin double-registers.

## Background (verify before editing — counts drift as code changes)

Verified against the worktree on 2026-07-03. Re-run these before starting
work; do not trust the numbers below without re-running:

```bash
# Class (c): literal "value0" key — the cleanest, highest-confidence signature.
grep -rnE '"value0"' internal --include='*.go' | grep -v _test.go | wc -l
# → 55 call sites across 23 non-test files as of this writing:
grep -rlE '"value0"' internal --include='*.go' | grep -v _test.go | sort

# Class (a)/(b): %-verb still present inside a slog message string.
grep -rnE 'slog\.(Debug|Info|Warn|Error)\("[^"]*%[a-zA-Z]' internal --include='*.go' | grep -v _test.go
# → 4 sites as of this writing: internal/plugin/registry.go:34,
#   internal/itunes/service/validate.go:81,
#   internal/server/file_io_pool.go:338 (message text stale, args already
#   correctly keyed — cosmetic only, see note below),
#   internal/maintenance/jobs/scan_composer_tags.go:196

# Class (d): duplicate key literals — NOT reliably greppable across
# multi-line calls; grep only surfaces candidates, every hit needs a Read to
# confirm the call boundaries and that both instances are truly duplicate
# keys within the SAME call (not two adjacent calls):
grep -rnE '"(value|count|id|response)", .*"(value|count|id|response)",' internal --include='*.go' | grep -v _test.go | wc -l
# → 41 candidate lines as of this writing (many are true positives per the
#   citations below, but confirm each one — do not blind-fix).
```

Do NOT trust the consultancy doc's line numbers as ground truth — re-verify
every citation with the greps above and a `Read` of the surrounding function
before editing. Citations below were spot-checked 2026-07-03 and matched
exactly, but drift is expected as other waves land first.

**Confirmed exemplar sites** (verified by direct read, one per defect class):

- Class (a), total data loss:
  `internal/plugin/registry.go:34` — `slog.Warn("plugin %q already registered, skipping duplicate")`
  with **zero** args. The duplicate plugin ID is unrecoverable. This is also
  CTR-4's cited blocker for diagnosing double-registration.
- Class (b), verb-in-message + bare positional args:
  `internal/itunes/service/validate.go:81` —
  `slog.Info("iTunes test-mapping from%q to%q", req.From, req.To)` — `req.From`
  becomes an attr **key**, `req.To` its value; the message's `%q` verbs are
  never interpolated (slog ignores them, they aren't `fmt`).
  `internal/maintenance/jobs/scan_composer_tags.go:196` —
  `slog.Info("scan-composer-tags COMPOSER %q→%q", "opID", opID, "composer", composer, willWrite, w.filePath)`
  — the trailing bare `willWrite, w.filePath` after already-keyed pairs makes
  `willWrite` (the tag value just written) become an attr **key**.
- Class (c), `"value0"` literal key+value:
  `internal/ai/embedding_client.go:215` —
  `slog.Warn("embedding cache get failed (hash)", "value0", "value0", "value1", hash[:8], "err", err)`
  — the intended key was almost certainly `"hash"` (paired with `hash[:8]`);
  `"value1"` should simply be dropped as a key, its value re-keyed.
  `internal/ai/embedding_client.go:235` —
  `slog.Debug("embedding cache / hits, 0 API calls", "value0", "hits", "hits", hits, "value1", len(texts))`
  — note `"hits"` appears BOTH as the value of `"value0"` and as its own
  correct key two args later; drop the spurious `"value0", "hits"` pair
  entirely and rekey `"value1"` → `"total"` (matches the sibling log two
  lines down: `"hits", hits, "total", len(texts)`).
  `internal/ai/embedding_client.go:260` — same shape as :215, key should be
  `"hash"`.
- Class (d), duplicate real keys:
  `internal/metafetch/service_apply.go:66` — `"value", extractedAuthor,
  "value", *book.Narrator, "name", existingAuthor.Name, "id", book.ID` — two
  different variables both keyed `"value"`; rename to `"extracted_author"` and
  `"narrator"`.
  `internal/metafetch/service_scoring.go:607` — `"value", epsilon, "value",
  bestScore` → rename to `"epsilon"`, `"best_score"`.
  `internal/itunes/service/validate.go:118` — `"response", response.Tested,
  "response", response.Found, "count", len(response.Examples)` → rename to
  `"tested"`, `"found"`.

**Re-verify anchors before editing** (do not assume any line number in this
brief without re-running):

```bash
grep -n "already registered" internal/plugin/registry.go
grep -n 'value0\|value1' internal/ai/embedding_client.go
grep -n '"value"' internal/metafetch/service_scoring.go internal/metafetch/service_apply.go
```

## Fix rules per defect class

Apply these mechanically, but every rename requires reading the enclosing
function to pick a key name that reflects what the variable actually is —
never leave a `"value0"`/`"value1"`/duplicate key in the diff.

1. **Class (a) — zero args, verb(s) in message:** Strip the `%`-verbs from
   the message (make it a plain, verb-free string), and add explicit
   `key, value` pairs for whatever data was being interpolated (read the
   surrounding code — e.g. `p.ID()` for `registry.go:34` → add `"id", p.ID()`).
2. **Class (b) — verb(s) in message + bare trailing args:** Strip the
   `%`-verbs from the message. Convert every bare trailing arg into an
   explicit `"key", value` pair using a key name derived from the variable
   name or its role in the message (e.g. `req.From`/`req.To` → `"from"`,
   `"to"`; `willWrite`/`w.filePath` → `"newValue"`, `"filePath"` — do not
   collide with the existing `"composer"` key already in that call).
3. **Class (c) — literal `"value0"`/`"value1"`/... keys:** Delete the
   `"valueN"` key and, if its paired value is itself the literal string
   `"value0"`/`"hits"`/etc. (i.e., the real datum was already deleted by the
   original conversion), recover the real key name from context (adjacent
   sibling log lines in the same function are often the best evidence — see
   the `embedding_client.go:235` example above) and re-key the *next* pair
   (currently keyed `"valueN+1"`) with that recovered name. If the original
   datum truly cannot be recovered from context, keep the value but give it
   the most accurate key you can infer from the variable name at the call
   site (never leave `"valueN"` in the diff).
4. **Class (d) — duplicate key literal in one call:** Rename each duplicate
   occurrence to a distinct, meaningful key based on the variable each is
   paired with (e.g. two `"id"` keys where one variable is a book ID and the
   other a candidate/library-copy ID → `"book_id"`, `"candidate_id"`). Keep
   key naming consistent with existing non-duplicated keys already used
   elsewhere in the same file (e.g. this repo already uses `book_id`,
   `bookID`, `id` inconsistently — match whatever convention the enclosing
   file already uses for that call's package rather than inventing a new one).

Do not touch: log level (`Debug`/`Info`/`Warn`/`Error` — never change),
whether a log call fires at all, any non-logging control flow, or any
already-correct keyed attrs in a call you're editing (only fix what's broken).

## Wave partitioning (per-package worktrees, no same-file overlap)

Coordinator runs the full-repo detection grep, then assigns whole packages
(never partial files) to Haiku children as separate `/parallel-sweep` tasks.
Based on the 2026-07-03 detection pass, partition into these child tasks
(re-run the detection grep first — this list may have grown/shrunk):

- `internal/ai` (embedding_client.go, aijobs/aijobs.go, dedup_review.go)
- `internal/plugin` (registry.go, events.go)
- `internal/itunes/service` (validate.go, track_provisioner.go)
- `internal/metafetch` (service_apply.go, service_scoring.go, service_search.go,
  service_writeback.go, service_fetch.go, service_files.go, service.go,
  openlibrary.go)
- `internal/quarantine` + `internal/reconcile` (service.go, reconcile.go)
- `internal/server` (file_io_pool.go, itl_rebuild.go, middleware/auth.go,
  server_search.go, handlers/audiobooks/handler_files.go,
  handlers/operations/handler.go)
- `internal/maintenance/jobs` (scan_composer_tags.go)
- `internal/sweep` + `internal/remux` + `internal/transcode` + `internal/updater`
  + `internal/scheduler` + `internal/watcher` + `internal/openlibrary`
  (archive_sweep.go, sweeper.go, temp_cleanup.go, remux.go, transcode.go,
  updater.go, scheduler.go, scheduler.go (updater), watcher.go, downloader.go,
  store.go — grouped because each is a single small file with few hits)

Each child's task prompt: "Run `grep -n '\"value0\"\|%[a-zA-Z]' <files>` in
your assigned package, fix every hit per the four fix-rule classes above, run
`go build ./... && go test ./<pkg>/... -count=1 && go vet ./<pkg>/...`, commit,
push, open PR against the sweep's integration branch." No child touches a
file outside its assigned package list.

## How to test (per child, and again at rollup)

```bash
go build ./...
go test ./<changed-package>/... -count=1
go vet ./...
```

Additionally, at rollup (coordinator, after all children merge), re-run the
three detection greps from "Background" above and report before/after counts:

```bash
echo "value0 sites remaining:"; grep -rnE '"value0"' internal --include='*.go' | grep -v _test.go | wc -l
echo "verb-in-message sites remaining:"; grep -rnE 'slog\.(Debug|Info|Warn|Error)\("[^"]*%[a-zA-Z]' internal --include='*.go' | grep -v _test.go | wc -l
```

Target: both counts at 0 (class (d) duplicate-key candidates require manual
confirmation per-hit and won't hit exactly 0 via grep alone — report the
before/after count and spot-check a sample of remaining hits to confirm
they're false positives, e.g. two adjacent-but-separate calls each using
`"id"` once).

## Acceptance criteria

- [ ] `grep -rnE '"value0"' internal --include='*.go' | grep -v _test.go` returns 0 hits.
- [ ] `grep -rnE 'slog\.(Debug|Info|Warn|Error)\("[^"]*%[a-zA-Z]' internal --include='*.go' | grep -v _test.go` returns 0 hits.
- [ ] All four cited exemplar sites (`internal/plugin/registry.go:34`,
      `internal/itunes/service/validate.go:81` and `:118`,
      `internal/ai/embedding_client.go:215/235/260`,
      `internal/maintenance/jobs/scan_composer_tags.go:196`,
      `internal/metafetch/service_apply.go:66`,
      `internal/metafetch/service_scoring.go:607/612/642/677`) have distinct,
      meaningful, non-duplicated keys and no stray `%`-verbs in their messages.
- [ ] No log level, call-site behavior, or already-correct attr was changed.
- [ ] `go build ./...`, `go vet ./...` clean repo-wide; `go test ./...` green
      (or at minimum every changed package's tests green — full suite gated by
      whatever CI already gates on `main`).
- [ ] Before/after counts for both detection greps reported in the rollup PR
      description.
- [ ] File headers bumped on every changed file per
      `.standards/instructions/file-headers.md`.
- [ ] (Stretch, not blocking) a `sloglint`-style check or a small analyzer
      script added to `make ci` to prevent recurrence — only if time permits;
      do not let this block merging the sweep itself.

## Commit message

Each child commits with its own scoped message; coordinator's rollup (if a
rollup commit/PR is used) follows this shape:

```
fix(logging): repair corrupted slog call sites from printf-to-slog conversion (QUAL-1/BUG-4)

A mass printf->slog conversion left duplicate attr keys, literal "value0"
placeholder keys/values, dropped interpolation args, and %-verbs still
embedded in message strings across dozens of call sites on the
merge/quarantine/metafetch/fingerprint/plugin-registry paths. Fix is
log-quality only — no behavior, level, or control-flow changes.

Co-Authored-By: Claude Haiku 4.5 <noreply@anthropic.com>
```

## PR + merge

Per-child (parallel-sweep default):

```bash
git push -u origin agent/cr-18-slog-corruption-sweep/<package-slug>
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If a re-run of the detection greps in "How to test" already returns 0 for
both `"value0"` and verb-in-message patterns, this sweep is done — verify with
the two grep commands above before starting any child task, and skip any
package where they're already clean. Class (d) duplicate-key candidates
should also be spot-checked with a fresh
`grep -rnE '"(value|count|id|response)", .*"(value|count|id|response)",' internal --include='*.go' | grep -v _test.go`
— if the hit count is unchanged from a prior run and every remaining hit is a
false positive (two separate calls, not one duplicated call), the sweep is
complete. Rollback = revert the relevant child's PR commit; each child's
changes are independent (different packages), so a single revert never
affects another child's files.
