- [ ] **AUTHOR-REFCOUNT-SPLIT (owner decision)** #3309 split author reference counts into live,
      trashed and dangling. Junction rows whose book no longer exists are dangling, and
      `author-duplicate-merge` no longer holds an author back for dangling rows alone. Three
      readers still use the flat sum and therefore still hold dangling-only authors:
      `internal/plugins/maintenance/author_purge_empty.go` (held_by_refs, 4,163 on production
      2026-09-12), `author_whitespace_collision_report.go`, and
      `internal/server/handlers/entities/author_refcount.go`. Decide whether dangling-only
      authors should become purgeable. If so, the dangling junction rows need cleaning up in the
      same pass, or the delete leaves them behind.
