<!-- file: docs/superpowers/fleet-tasks/034-arch-7-1-tag-policies.md -->
<!-- version: 1.0.0 -->
<!-- guid: d079cd9c-8878-4568-9903-01988c1640e2 -->
<!-- last-edited: 2026-08-22 -->

# Task 034: 7.1 — Tag-based policies / preference inheritance

**Depends on:** none (7.2 is already complete)
**Estimated effort:** L
**Wave:** 9 (architecture)

## Goal

Implement tag-based policies: books tagged with certain tags (e.g., "auto-organize", "no-writeback",
"preferred-source:audible") inherit processing preferences — affecting organize, metadata fetch,
write-back behavior.

## Context

- Book tags: `book_tags` table (migration 37), `AddBookUserTag`, `GetBookTags` methods exist
- 7.2 (language filter) is already done — tag-based language filtering is the starting reference
- Policy application points: organizer, metadata apply pipeline, write-back batcher
- Policies are hints, not hard rules — they can be overridden by explicit user actions

## Files to modify/create

- `internal/policy/policy.go` (new package) — `BookPolicy` struct + `EvaluatePolicy(tags []BookTag) BookPolicy`
- `internal/organizer/` — respect `NoOrganize` policy
- `internal/metafetch/service_apply.go` — respect `PreferredSource` and `NoMetadataFetch` policies
- `internal/writeback/` — respect `NoWriteback` policy
- `internal/server/` — `GET /api/v1/policy/tags` explaining available policy tags
- `web/src/pages/Settings.tsx` or docs — list the recognized policy tags

## Instructions

### 1. Define `BookPolicy`

```go
// internal/policy/policy.go
package policy

type BookPolicy struct {
    NoOrganize      bool   // tag: "policy:no-organize"
    NoWriteback     bool   // tag: "policy:no-writeback"
    NoMetadataFetch bool   // tag: "policy:no-metadata"
    PreferredSource string // tag: "policy:source:audible" → "audible"
    Priority        int    // tag: "policy:priority:high" → 10
}

// EvaluatePolicy derives a BookPolicy from the book's tags.
func EvaluatePolicy(tags []database.BookTag) BookPolicy {
    var p BookPolicy
    for _, t := range tags {
        switch t.Name {
        case "policy:no-organize":
            p.NoOrganize = true
        case "policy:no-writeback":
            p.NoWriteback = true
        case "policy:no-metadata":
            p.NoMetadataFetch = true
        case "policy:source:audible":
            p.PreferredSource = "audible"
        case "policy:source:google":
            p.PreferredSource = "google"
        case "policy:priority:high":
            p.Priority = 10
        }
    }
    return p
}
```

### 2. Apply in organizer

In `internal/organizer/` find the `OrganizeBook` function. Before organizing:
```go
tags, _ := s.store.GetBookTags(ctx, book.ID)
pol := policy.EvaluatePolicy(tags)
if pol.NoOrganize {
    slog.Info("skipping organize: policy:no-organize tag", "book_id", book.ID)
    return nil
}
```

### 3. Apply in metadata fetch

In `internal/metafetch/service_apply.go`, before fetching metadata:
```go
if pol.NoMetadataFetch {
    return nil, fmt.Errorf("metadata fetch disabled by policy:no-metadata tag")
}
if pol.PreferredSource != "" {
    // Bias candidate scoring toward preferred source
    scorer.SetPreferredSource(pol.PreferredSource)
}
```

### 4. Apply in write-back

In `internal/writeback/`, before adding to the write-back queue:
```go
if pol.NoWriteback {
    slog.Info("skipping write-back: policy:no-writeback tag", "book_id", book.ID)
    return nil
}
```

### 5. Document recognized tags

Add `GET /api/v1/policy/tags` that returns the list of recognized policy tags with descriptions.
Also add a section to the Settings UI explaining the policy tags.

## Test

```bash
go test ./internal/policy/... -v -count=1
go test ./internal/organizer/... -v -count=1
make ci
```

## Commit

```
feat(policy): tag-based processing policies (no-organize, no-writeback, preferred-source) (7.1)
```

## PR title

`feat(policy): tag-based book processing policies — 7.1`

## After merging

Mark `- [ ] **7.1**` as `- [x]` in `TODO.md`.
