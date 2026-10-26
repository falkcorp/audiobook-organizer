- [ ] **TODO-BF-DELETE-SHARED-INDEX-GUARD** `deleteSingleOwnerIndexIfOwned`
      (`internal/database/pebble_store_bookfiles.go`) reads the shared `book_file_hash:`
      or `book_file_path:` key at stage time, before the owner stripes and the
      single-owner index mutex are taken, and stages a delete if the value names the
      row being deleted. Another row that claims the same key between that read and
      the apply has its entry deleted: the compare-and-swap on the deleted row does
      not cover the index key, and the index mutex serializes the APPLIES of writers
      of these keys but does not re-check what the delete decided from. Older than
      PR #4169.
      Fix: record the observed index value the way the retired-version writers do
      (`indexCAS.note` / `indexCAS.verify`) and commit the delete through
      `commitBookFileBatchVerified`, so the value is re-read under the stripes and
      the index mutex and the batch is refused as stale (re-staged) when it moved.
      Cost: `commitBookFileDeletes` / `commitDeleteWithWindowCascade` and the
      move paths gain a verify hook and an `indexCAS` each, and the stage helpers
      (`deleteBookFileSecondaryIndexes` and its callers) must record the keys they
      read; `indexCAS` today lives with the retired-version writers and would move to
      a shared file. Writers that take no stripe are already ordered against the
      apply by the index mutex, so only the stage-to-apply window is left to close.
