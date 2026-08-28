<!-- file: docs/superpowers/plans/2026-08-28-bulk-split-book-merge.md -->
<!-- version: 1.0.0 -->
<!-- guid: 17c3ddd7-c7e5-47dd-8d98-8618f8509b53 -->
<!-- last-edited: 2026-08-28 -->

# Bulk Split-Book Merge Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Safely turn reviewed persisted split-book candidate IDs into one durable, preview-capable bulk merge operation while extending detection for same-folder numbered chapter fragments.

**Architecture:** The handler validates candidate IDs, resolves every candidate, proves the groups are disjoint, and snapshots instructions before one operation is queued. The operation runs snapshots sequentially, reports one outcome per candidate, and deletes only fully successful candidates. A filename-first detector lane produces more preview candidates without mutation.

**Tech Stack:** Go, Gin, UOS operation registry, Pebble, React, Vitest.

**Spec:** `docs/superpowers/specs/2026-08-28-bulk-split-book-merge-design.md`

## Global Constraints

- Mutation accepts persisted candidate IDs, never arbitrary book groups.
- Reject unknown, duplicate, invalid, stale, or overlapping candidates before enqueueing.
- Snapshot candidate IDs, book IDs, keeper, and title in operation parameters.
- Retain partial/error candidates; production apply is not authorized.
- Use `GOTOOLCHAIN=go1.26.0` for Go verification.

---

### Task 1: Retain incomplete candidates

**Files:**
- Modify: `internal/server/handlers/split_book.go`
- Modify: `internal/server/handlers/split_book_test.go`

**Produces:** a shared complete-success guard used before candidate deletion.

- [ ] Write a failing test where `MergeSplitBookCluster` returns source-move errors and assert `candStore.Delete` is not called.
- [ ] Run `GOTOOLCHAIN=go1.26.0 go test ./internal/server/handlers -run TestMergeSplitBookCandidate_KeepsCandidateWhenMergeReportsErrors -count=1`; verify it fails because the current handler deletes the candidate.
- [ ] Implement `complete := len(result.Errors) == 0 && result.MergedSrcCount == len(srcIDs)` and call `Delete` only when `complete`.
- [ ] Re-run the focused tests and commit `fix(dedup): retain partial split-book candidates`.

### Task 2: Durable batch operation

**Files:**
- Create: `internal/plugins/dedup/split_book_bulk_merge.go`
- Create: `internal/plugins/dedup/split_book_bulk_merge_test.go`
- Modify: `internal/plugins/dedup/plugin.go`

**Produces:** `dedup.split-book-bulk-merge` accepting these immutable snapshots:

```go
type BulkSplitBookMergeItem struct {
    CandidateID string   `json:"candidate_id"`
    BookIDs     []string `json:"book_ids"`
    KeepID      string   `json:"keep_id"`
    SuggestedTitle string `json:"suggested_title"`
}
type BulkSplitBookMergeParams struct {
    Items []BulkSplitBookMergeItem `json:"items"`
    DryRun bool `json:"dry_run"`
}
```

- [ ] Write failing tests proving a dry run moves/deletes nothing and a batch continues after a failed candidate while retaining it.
- [ ] Run `GOTOOLCHAIN=go1.26.0 go test ./internal/plugins/dedup -run TestRunSplitBookBulkMerge -count=1`; verify failure before implementation.
- [ ] Implement a sequential cancellable runner, progress per item, dry-run outcomes, and delete only fully successful persisted candidates.
- [ ] Register the operation, re-run focused tests, and commit `feat(dedup): add durable bulk split-book merge operation`.

### Task 3: Candidate-ID preflight API

**Files:**
- Modify: `internal/server/handlers/split_book.go`
- Modify: `internal/server/handlers/split_book_test.go`
- Modify: `internal/server/wire_library_routes.go`

**Produces:** `POST /dedup/split-book-candidates/bulk-merge`, accepting:

```json
{"candidate_ids":["candidate-ulid"],"keep_ids":{"candidate-ulid":"book-ulid"},"dry_run":true}
```

- [ ] Write failing tests for duplicate IDs, unknown IDs, a keep ID outside its candidate, and overlapping candidate book sets; each must make zero enqueue calls.
- [ ] Write a failing success test asserting the enqueued payload is the resolved snapshot, not bare IDs.
- [ ] Run `GOTOOLCHAIN=go1.26.0 go test ./internal/server/handlers -run TestBulkMergeSplitBookCandidates -count=1`; verify failure before code.
- [ ] Implement validation, snapshot construction, route registration, focused green test run, and commit `feat(dedup): enqueue preflighted split-book merge batches`.

### Task 4: Filename-first preview candidates

**Files:**
- Modify: `internal/dedup/split_book_detector.go`
- Modify: `internal/dedup/split_book_detector_test.go`
- Modify: `internal/plugins/dedup/split_book_scan.go`

**Produces:** preview-only candidate groups for three or more same-folder siblings with a near-sequential leading number and a shared normalized filename stem, regardless of polluted author/series IDs.

- [ ] Write a failing positive test for `01 - My Book.mp3` through `03 - My Book.mp3` with mismatched/nil metadata IDs.
- [ ] Write a failing negative test proving `1984.m4b` or unrelated numeric siblings cannot form a group.
- [ ] Run `GOTOOLCHAIN=go1.26.0 go test ./internal/dedup -run TestDetectSplitBooks -count=1`; verify the positive test is red.
- [ ] Implement filename-first extraction, title fallback, shared non-empty stem, and existing sequence coverage checks; re-run green and commit `feat(dedup): preview numbered sibling split-book groups`.

### Task 5: JSON import client and verification

**Files:**
- Modify: `web/src/services/api.ts`
- Modify: existing split-book review component and test
- Modify: `docs/audits/2026-08-27-library-reliability-current-status.md`
- Create: `changelog.d/20260828-bulk-split-book-merge.md`

- [ ] Write a failing UI/API test that normalizes a JSON array of candidate IDs into one dry-run request.
- [ ] Implement local JSON validation, the bulk API helper, and an upload affordance defaulting to dry run.
- [ ] Run `npm test -- --run web/src && npm run build`.
- [ ] Run `GOTOOLCHAIN=go1.26.0 go test ./internal/dedup/... ./internal/plugins/dedup/... ./internal/server/handlers/...` and `GOTOOLCHAIN=go1.26.0 make ci`.
- [ ] Update audit/changelog, commit, push `codex/bulk-split-book-merge`, and open a draft PR. Do not deploy or invoke the endpoint.

## Rollback

Do not invoke the new endpoint to disable mutation. Reverting the feature removes the API and operation registration while preserving every persisted candidate. The preview detector does not alter books or files.
