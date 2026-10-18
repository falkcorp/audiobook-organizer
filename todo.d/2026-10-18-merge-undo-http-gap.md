- [ ] **01-P81H-UNDO-GAP** Give combines and sibling moves an undo path again
      after 01-P81h retired `POST /api/v1/merge/undo/:journal_id` and
      `POST /api/v1/merge/sibling-undo/:journal_id` (410 for one release). Only
      the `dedup.split-books` Repairs fixer writes a `merge_combine_journal`
      pointer row, so only its combines can still be reversed (through
      `POST /api/v1/operations/:id/revert`). A manual combine
      (`POST /api/v1/audiobooks/combine`), a regroup apply and a review-queue
      combine journal in `merge:combine-journal:` but write no pointer row,
      and a sibling-move journal (`merge:sibling-journal:`) has no revert
      handler at all. Options: (a) have those paths journal the same pointer row
      so the operation revert covers them, and add a sibling-journal change type
      that calls `merge.Service.UndoSiblingMove`; (b) build the undo UI the
      retired routes never had. `merge.Service.UndoSiblingMove` is kept for this
      and is currently called only by tests.
