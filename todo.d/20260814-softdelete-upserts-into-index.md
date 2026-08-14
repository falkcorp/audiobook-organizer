## Soft-deleting a book UPSERTS it into the search index

Found during C715: `DeleteAudiobook(SoftDelete: true)` sets the flags via
`store.UpdateBook`, and the `indexedStore` decorator enqueues a REINDEX on
every UpdateBook — so soft-deleting a book refreshes its search doc instead
of removing it. The trashed book stays searchable until the next boot's
coverage reconcile (set-based since C715) deletes the stale doc.

- [ ] Make the soft-delete transition enqueue a Bleve DELETE instead: either
      teach `indexedStore.UpdateBook` to check `MarkedForDeletion` on the
      updated row, or have the soft-delete path call the index delete
      explicitly. Mirror-image: RestoreAudiobook's UpdateBook reindex is
      CORRECT — don't break it.
- [ ] Regression test: soft-delete an indexed book, assert a title probe
      returns nothing WITHOUT a boot reconcile.
