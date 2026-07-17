&lt;!-- file: docs/agent-tasks/dedup-dataset/TASK-04-jsonl-export.md --&gt;
&lt;!-- version: 1.0.0 --&gt;
&lt;!-- guid: 7fbc1c2d-4a5e-4f6a-8b9c-0d1e2f3a4b5c --&gt;
&lt;!-- last-edited: 2026-07-01 --&gt;

# TASK-04 — JSONL export endpoint for labeled examples (C7)

**Priority:** P3 · **Effort:** S · **Recommended subagent:** Haiku · go-backend subagent · **Depends on:** none

## ⛔ START HERE (do this first, exactly)
```bash
REPO=/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer   # adjust to your clone
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/dd-jsonl-export" -b agent/dd-jsonl-export origin/main
cd "$REPO/.worktrees/dd-jsonl-export"
git rebase origin/main
```
Do all work inside that worktree. Never edit `main` or the primary checkout.

## Goal

There is currently no JSONL export of the `dedup:label:` keyspace (the labeled
training examples used for the dedup classifier). The only existing exports are
candidate export (CSV/JSON, `ExportDedupCandidates`) and unrelated diagnostics
batch JSONL — neither includes labeled examples with their `formula_version`.
Add a **read-only, admin-only** HTTP endpoint that streams all
`dedup:label:` labeled examples as JSONL (one JSON object per line), reusing
the existing `EmbeddingStore.ListLabeledExamples` method. No mutation.

## Background (verify before editing — line numbers drift)

- `internal/database/dedup_label.go`:
  - `LabeledExampleFilter` struct (around line 74) — the filter type accepted
    by `ListLabeledExamples`.
  - `func (s *EmbeddingStore) ListLabeledExamples(f LabeledExampleFilter) ([]LabeledExample, error)`
    (around line 138-139) — already does a prefix scan over `dedup:label:`.
  - The `LabeledExample` struct itself (find it in the same file) — check for a
    `FormulaVersion` / `formula_version` field (or equivalent feature-version
    field) to confirm it's already present on the struct — it must be, since
    the goal requires exporting it.
- `internal/server/handlers/dedup/handler.go`:
  - `ExportDedupCandidates` (around line 428-445) — use this as the style
    reference for a new export handler (route registration pattern, admin auth
    check, streaming response, error handling).
- `internal/server/handlers/dedup/label_review.go` — around line 38, an
  existing call to `es.ListLabeledExamples(filter)`; use this as a second
  reference for how the filter is normally constructed/used from a handler.
- Find where dedup routes are registered (likely `internal/server/routes.go`
  or similar under `internal/server/`) to see how `ExportDedupCandidates` is
  wired to a path, and how admin-only auth middleware is applied to it.

Run these to confirm the current state before editing:
```bash
grep -n "func (s \*EmbeddingStore) ListLabeledExamples\|type LabeledExampleFilter\|type LabeledExample struct" internal/database/dedup_label.go
sed -n '1,50p' internal/database/dedup_label.go
grep -n "func.*ExportDedupCandidates" internal/server/handlers/dedup/handler.go
sed -n '420,470p' internal/server/handlers/dedup/handler.go
grep -rn "ExportDedupCandidates" internal/server/ | grep -v _test.go
```

## Step-by-step

1. In `internal/server/handlers/dedup/handler.go` (or a new file
   `internal/server/handlers/dedup/label_export.go` if `handler.go` is already
   very large — check with `wc -l`), add a new handler function, e.g.
   `ExportLabeledExamples(c *gin.Context)`, that:
   - Applies the same admin-only auth check pattern as `ExportDedupCandidates`
     (copy the exact pattern — do not invent a new auth check).
   - Optionally accepts query params mirroring `LabeledExampleFilter`'s fields
     (e.g. `label_source`, date range — check the struct fields and only wire
     the ones that make sense as query filters; if unsure, support no filters
     initially and export everything).
   - Calls `es.ListLabeledExamples(filter)`.
   - Sets `Content-Type: application/x-ndjson` (or `application/jsonl` if that's
     the convention used elsewhere in this repo — grep for existing JSONL
     content-type usage first: `grep -rn "ndjson\|x-jsonlines\|jsonl" internal/server/`).
   - Writes one `json.Marshal`-ed `LabeledExample` per line, separated by `\n`,
     streaming directly to `c.Writer` (do not buffer the entire result set into
     one giant string if `c.Writer` supports incremental writes — mirror
     whatever streaming pattern the diagnostics batch JSONL export already uses,
     if one exists: `grep -rn "jsonl\|NDJSON" internal/ --include=*.go -il`).
2. Register the new route in the same routes file where
   `ExportDedupCandidates` is registered, using the same admin-route grouping,
   e.g. `GET /api/v1/dedup/labels/export`.
3. Do NOT add any endpoint that deletes, mutates, or re-labels examples — this
   task is strictly read-only export.
4. Bump the file header `version` and `last-edited` on every file you touch.

## How to test

Add a test in `internal/server/handlers/dedup/handler_test.go` (or a new
`label_export_test.go` alongside it), mirroring `TestExportDedupCandidates_CSV`
(around line 291) and `TestExportDedupCandidates_BadFormat` (around line 304)
for structure. Assert: (a) response has the expected content-type; (b) each
line of the body is valid JSON and unmarshals into a `LabeledExample`;
(c) the number of lines matches the number of examples seeded into a fake/mock
store; (d) non-admin requests are rejected (mirror the existing auth-check test
pattern in the same test file).

```bash
go build ./...
go test ./internal/dedup/... ./internal/database/... ./internal/server/handlers/dedup/... -count=1
go vet ./internal/server/handlers/dedup/...
```

## Acceptance criteria

- [ ] New admin-only, read-only endpoint streams all `dedup:label:` examples as
      JSONL (one example per line), including the formula/feature version
      field.
- [ ] No mutation path added; endpoint only reads via `ListLabeledExamples`.
- [ ] Non-admin requests are rejected using the existing auth pattern.
- [ ] New test(s) cover a happy-path export and the auth rejection case, and
      pass.
- [ ] `go test ./internal/dedup/... ./internal/database/... ./internal/server/handlers/dedup/... -count=1` passes; `go vet` clean.
- [ ] File headers bumped on every changed file.

## Commit message
```
feat(dedup): add JSONL export endpoint for labeled examples (C7)

No export existed for the dedup:label: keyspace; the only exports covered
candidates, not labeled training examples. Add a read-only, admin-only
endpoint that streams ListLabeledExamples results as JSONL, including the
formula/feature version, for offline dataset analysis and training.

Co-Authored-By: Claude Opus 4.8 <noreply@anthropic.com>
```

## PR + merge
```bash
git push -u origin agent/dd-jsonl-export
gh pr create --fill
gh pr merge <number> --rebase
```

## Idempotency / Rollback

Idempotency: if an `ExportLabeledExamples` (or equivalently named) handler
already exists and is registered on a route, this task is already done —
verify a test exists and stop.

Rollback: revert the single commit; the change adds a new handler + route only,
no existing behavior is modified.