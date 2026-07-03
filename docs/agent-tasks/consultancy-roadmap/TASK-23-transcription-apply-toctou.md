<!-- file: docs/agent-tasks/consultancy-roadmap/TASK-23-transcription-apply-toctou.md -->
<!-- version: 1.0.0 -->
<!-- guid: fc1d8998-b00b-45c1-a8a2-f4a5bbfa4016 -->
<!-- last-edited: 2026-07-03 -->

# TASK-23 — TOCTOU fix in `ApplyTranscriptionCandidate` (consultancy-roadmap)

**Priority:** P2 · **Effort:** S · **Recommended subagent:** Sonnet · go-backend subagent · **Depends on:** none

## ⛔ START HERE (do this first, exactly)

```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/cr-23-transcription-apply-toctou" -b agent/cr-23-transcription-apply-toctou origin/main
cd "$REPO/.worktrees/cr-23-transcription-apply-toctou"
git rebase origin/main
```

## Goal

Close the TOCTOU window in the transcription auto-match apply path
(consultancy findings MATCH-6 / BUG-3 / QUAL-3, verified real). Today
`ApplyTranscriptionCandidate` ignores the `candTitle`/`candAuthor` identity of
the candidate that was actually gated by `runAutoMatchTranscribed`, and
instead blindly re-reads the metadata cache and applies whatever sits in
`Candidates[0]` at apply time. If the cache is refreshed between the gate and
the apply (a concurrent metadata search, another maintenance op, a UI-driven
re-fetch — the cache is shared and keyed only by book ID), an **ungated**
candidate gets applied and `MetadataReviewStatus` may be set to
`audio_confirmed` on the wrong metadata, while the "applied" log line still
reports the (stale, gated) title. Fix: make `ApplyTranscriptionCandidate`
actually use the identity parameters it already receives — verify the
re-read cache slot 0 still matches the gated `candTitle`/`candAuthor` before
applying, and error out (which the caller already treats as "skip + log")
on mismatch.

## Background (verify before editing)

- `internal/server/server_maintenance_deps.go` implements
  `maintenance.ServerDeps` on `*Server`. Two methods are relevant:
  - `SearchTranscriptionCandidate(_ context.Context, bookID, _, _ string) (string, string, float64, bool, error)`
    — note the transcribed-title/author parameters are already discarded
    (`_, _`); it reads `s.metadataFetchService.GetCachedCandidates(bookID)`
    and returns `Candidates[0]`'s title/author/score. **This method's own
    query-param-ignoring behavior is a separate, lower-priority issue — out
    of scope for this task.** Do not change its signature or behavior.
  - `ApplyTranscriptionCandidate(_ context.Context, bookID, _, _ string) error`
    — this is the actual bug target. Its `candTitle`/`candAuthor` parameters
    are *also* discarded (`_, _`), even though the caller
    (`runAutoMatchTranscribed` in
    `internal/plugins/maintenance/auto_match_transcribed.go`) passes the
    exact gated candidate's title/author into it at the call site
    (`p.deps.ApplyTranscriptionCandidate(ctx, id, candTitle, candAuthor)`).
    The function re-reads `GetCachedCandidates(bookID)` independently and
    applies `Candidates[0]` from that fresh read — which may differ from the
    candidate that was gated moments earlier.
  - Gates applied by the caller before calling `ApplyTranscriptionCandidate`
    (see `runAutoMatchTranscribed`, roughly lines 130-183): Gate 1 — score
    `>= minScore`; Gate 2 — `util.NormalizeTitle(candTitle) ==
    util.NormalizeTitle(transTitle)`; Gate 3 — author containment via
    `strings.Contains` (case-insensitive) when `transAuthor` is longer than 3
    chars. These gates run against the `candTitle`/`candAuthor` returned by
    `SearchTranscriptionCandidate` — the exact same values then thrown away
    by `ApplyTranscriptionCandidate`.
  - **The fix does NOT require changing the `maintenance.ServerDeps`
    interface or the call site** — the identity parameters
    (`candTitle`, `candAuthor`) already flow end-to-end; they are simply
    unused (`_, _`) inside `ApplyTranscriptionCandidate`. Rename them to
    real parameter names and use them.
  - `util.NormalizeTitle` (`internal/util/normalize.go`) does
    `strings.ToLower(strings.TrimSpace(s))` — reuse it for the title
    comparison (matches Gate 2's normalization exactly, so a book that
    passed the caller's gate cannot spuriously fail the apply-time check due
    to a different normalization rule). `internal/util/normalize.go` also
    has `NormalizeAuthor` — reuse it (or the same `strings.Contains`
    substring-containment rule the caller's Gate 3 uses) for the author
    comparison; do not require exact author equality since Gate 3 itself
    only requires containment.
  - `metafetch.MetadataCandidate` (`internal/metafetch/service.go`) has
    `Title string` and `Author string` fields (JSON `title`/`author`) — this
    is the type `Candidates[0]` unmarshals into in both methods.
  - The caller (`runAutoMatchTranscribed`) already treats a non-nil error
    from `ApplyTranscriptionCandidate` as "non-fatal, skip and log a warning
    with `applyErr`" — see the `log.Warn("auto-match-transcribed: apply
    failed", ...)` branch. Returning a descriptive error on mismatch is
    therefore sufficient; no new caller-side handling is needed.

- **Re-verify these anchors before editing** — line numbers drift:
  ```bash
  grep -n "func (s \*Server) SearchTranscriptionCandidate\|func (s \*Server) ApplyTranscriptionCandidate" internal/server/server_maintenance_deps.go
  grep -n "p.deps.ApplyTranscriptionCandidate\|p.deps.SearchTranscriptionCandidate\|// Gate 1\|// Gate 2\|// Gate 3" internal/plugins/maintenance/auto_match_transcribed.go
  grep -n "ApplyTranscriptionCandidate\|SearchTranscriptionCandidate" internal/plugins/maintenance/deps.go
  ```
  Confirm `ApplyTranscriptionCandidate`'s current body still discards its
  last two string params and unmarshals `entry.Candidates[0]` directly into
  a `metafetch.MetadataCandidate` with no comparison against those params
  before calling `s.metadataFetchService.ApplyMetadataCandidate(...)`.

## Step-by-step

1. In `internal/server/server_maintenance_deps.go`, change
   `ApplyTranscriptionCandidate`'s signature from
   `(_ context.Context, bookID, _, _ string) error` to name the last two
   parameters (e.g. `gatedTitle, gatedAuthor string`) — do NOT change the
   parameter types, count, or return type (the `maintenance.ServerDeps`
   interface in `internal/plugins/maintenance/deps.go` is unchanged).
2. After unmarshaling `entry.Candidates[0]` into `cand
   metafetch.MetadataCandidate` (existing code), add a check before the
   `ApplyMetadataCandidate` call:
   - If `util.NormalizeTitle(cand.Title) != util.NormalizeTitle(gatedTitle)`,
     OR the author check fails (mirror the caller's Gate 3: only enforce
     when `gatedAuthor` is non-trivial, e.g. `len(gatedAuthor) > 3`, using a
     case-insensitive substring-containment check consistent with
     `auto_match_transcribed.go`'s Gate 3 — do not require exact author
     equality),
   - then return a descriptive, non-nil error (e.g.
     `fmt.Errorf("cached candidate for book %s changed since gating (want %q/%q, got %q/%q)", bookID, gatedTitle, gatedAuthor, cand.Title, cand.Author)`)
     and do **not** call `ApplyMetadataCandidate`.
   - Add a `slog.Warn` (or reuse the existing logger convention in this
     file — check whether this file already imports `log/slog`; if not, add
     it) at the point of mismatch, with fields `book_id`, `gated_title`,
     `cache_title` so the "apply failed" warning downstream in
     `auto_match_transcribed.go` has enough context without needing a second
     log line.
3. Leave `SearchTranscriptionCandidate` untouched — it is out of scope for
   this task (see Background).
4. Leave `auto_match_transcribed.go`'s call site
   (`p.deps.ApplyTranscriptionCandidate(ctx, id, candTitle, candAuthor)`)
   untouched — it already passes the correct identity; only the callee
   needed to start using it.
5. Add a regression test that reproduces the TOCTOU window and proves it is
   closed. Recommended shape, in a new file
   `internal/server/server_maintenance_deps_test.go` (verify this file does
   not already exist before creating it — `ls internal/server/ | grep
   maintenance_deps` — if it exists, extend it instead):
   - Build a `*database.MockStore` whose `GetMetadataCacheFunc` returns a
     **different** `MetadataCandidateCache` on its second invocation than its
     first (use a call counter closure) — simulating a cache refresh between
     the gate (first read, via `SearchTranscriptionCandidate`) and the apply
     (second read, inside `ApplyTranscriptionCandidate`). Also stub
     `GetBookByIDFunc` to return a minimal valid `*database.Book` (needed by
     `ApplyMetadataCandidate`) and `GetBookTagsFunc` to return no tags (see
     `internal/metafetch/service_apply.go`'s `ApplyMetadataCandidate` for
     what it reads from the store — follow the existing pattern in
     `internal/server/metadata_ops_fastpath_test.go` for constructing
     `&Server{store: store, metadataFetchService: metafetch.NewService(store)}`).
   - Call `s.SearchTranscriptionCandidate(ctx, bookID, "irrelevant", "irrelevant")`
     to get `(candTitle, candAuthor, score, found, err)` from the first cache
     read (this is what the real caller does).
   - Call `s.ApplyTranscriptionCandidate(ctx, bookID, candTitle, candAuthor)`
     — this triggers the second (now-different) cache read internally.
   - Assert: the call returns a non-nil error, AND
     `ApplyMetadataCandidate`/the underlying store write was never invoked
     with the second candidate's data (e.g. assert on a
     `PutBookFunc`/write-path spy not being called with the mismatched
     title, or that the returned error message names the mismatch).
   - Add a second case: cache returns the **same** candidate on both reads —
     assert `ApplyTranscriptionCandidate` succeeds (no regression / no
     over-suppression of the legitimate, unmodified-cache path).
6. Bump the file header (version bump + `last-edited` date) on every file
   you touch, per `.standards/instructions/file-headers.md`.

## How to test

```bash
go build ./...
go test ./internal/server/... ./internal/plugins/maintenance/... -count=1
go vet ./internal/server/... ./internal/plugins/maintenance/...
```

## Acceptance criteria

- [ ] `ApplyTranscriptionCandidate` no longer discards its `candTitle`/
      `candAuthor` parameters — it compares them against the re-read cache's
      `Candidates[0]` before applying.
- [ ] On a title/author mismatch (simulating a cache refresh between gate
      and apply), `ApplyTranscriptionCandidate` returns a non-nil,
      descriptive error and does **not** call `ApplyMetadataCandidate`.
- [ ] On a matching cache read (no concurrent refresh), behavior is
      unchanged — `ApplyTranscriptionCandidate` still succeeds and applies
      the candidate.
- [ ] `maintenance.ServerDeps` interface signature is unchanged; `auto_match_transcribed.go`'s
      call site is unchanged (it already passes the right values).
- [ ] `SearchTranscriptionCandidate` is untouched (out of scope).
- [ ] New/updated regression test covers both the mismatch case and the
      matching case; `go test ./internal/server/... ./internal/plugins/maintenance/...`
      is green; `go vet` is clean.
- [ ] File headers bumped on every changed file.

## Commit message

```
fix(server): verify gated candidate identity before apply in ApplyTranscriptionCandidate (MATCH-6/BUG-3/QUAL-3)

ApplyTranscriptionCandidate discarded its candTitle/candAuthor parameters and
re-read the metadata cache independently, applying whatever sat in
Candidates[0] at apply time. A cache refresh between the auto-match gate and
the apply call (concurrent metadata search, another maintenance op) could
apply an ungated candidate while the "applied" log line still reported the
gated title. Use the identity parameters the caller already passes to verify
the re-read cache slot still matches before applying; error (skip + log) on
mismatch instead of blindly applying slot 0.

Co-Authored-By: Claude Sonnet 5 <noreply@anthropic.com>
```

## PR + merge

```bash
git push -u origin agent/cr-23-transcription-apply-toctou
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

If `ApplyTranscriptionCandidate` already compares its `candTitle`/
`candAuthor` parameters against the re-read `Candidates[0]` before calling
`ApplyMetadataCandidate` (i.e. the parameters are no longer named `_, _`),
this task is done — verify with:
```bash
grep -n "func (s \*Server) ApplyTranscriptionCandidate" -A 20 internal/server/server_maintenance_deps.go
```
and confirm a title/author comparison appears between the cache unmarshal
and the `ApplyMetadataCandidate` call. Rollback = revert the commit; the
caller-side gates (score/title/author in `runAutoMatchTranscribed`) and
`SearchTranscriptionCandidate` are untouched by this change and remain in
effect either way.
