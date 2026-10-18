- [ ] **01-P81H-UNDO-GAP** Give every combine and sibling move an undo path
      again after 01-P81h retired `POST /api/v1/merge/undo/:journal_id` and
      `POST /api/v1/merge/sibling-undo/:journal_id` (410 for one release). Only
      the `dedup.split-books` Repairs fixer writes a `merge_combine_journal`
      pointer row (`split_books_fixer.go`), so only its combines can be
      reversed through `POST /api/v1/operations/:id/revert`. These combine
      paths journal in `merge:combine-journal:` but write no pointer row, so
      the operation revert cannot reach them:
      `POST /api/v1/audiobooks/combine`; the regroup apply
      (`ApplyMultidisc` and `ApplyDuplicateOf` in
      `internal/plugins/maintenance/regroup_apply.go`);
      `maintenance.merge-chapter-groups` (`merge_chapter_groups.go`);
      `POST /api/v1/dedup/split-book-candidates/:id/merge`
      (`handlers/split_book.go`, called from `api.ts`);
      `dedup.split-book-bulk-merge`
      (`internal/plugins/dedup/split_book_bulk_merge.go`). A sibling-move
      journal (`merge:sibling-journal:`) has no revert handler at all.
      Options: (a) have those paths journal the same pointer row so the
      operation revert covers them, and add a sibling-journal change type that
      calls `merge.Service.UndoSiblingMove` (kept for this, currently called
      only by tests); (b) build the undo UI the retired routes never had.
      Note the permission change: the replacement route needs
      `library.organize`, the retired undo routes needed
      `library.edit_metadata`.
