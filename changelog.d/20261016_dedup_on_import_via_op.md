The three post-create dedup checks (importer, the scanner/iTunes/manual
create hook `fireDedupOnImport`, and the post-rename check after a metadata
apply) now share one helper, `dedup.EnqueueCheckBook`. With
`dedup.on_import_via_scheduler` on, each enqueues the batchable
`dedup.check-book` op, so bursts coalesce into one visible, cancellable run.
Before this, only the importer honored the flag and the other two always
started a bare goroutine. With the flag off, all three keep their existing
goroutine paths.

The default stays `false`. `dedup.check-book` requires `book_sig_v1`, and a
book that never gets a signature (no fpcalc, an undecodable container, no
current-era prints) is re-bucketed on every batch window with no expiry. So
with the flag on, its Layer 1 exact-hash check would never run. Flipping the
default waits on an owner decision about that gap.
