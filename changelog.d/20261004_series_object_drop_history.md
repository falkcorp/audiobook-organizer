### Fixed

#### Dropped stale series objects are recorded in the book's history

The store's series rule (#3698) drops or replaces an embedded `Book.Series`
object that does not match `Book.SeriesID` on every book write. When the
stored row's object was the only record of a series (a stale object older
builds left: `SeriesID` nil, or naming another series), the drop left no
trace. The store now writes one change-history row for the lost stored object (field `series_object`,
change type `series-object-drop`, source `series_invariant`, old name in the
previous value, old id in `previous_ref`) in the same Pebble batch as the book
row. An ordinary series clear or move does not get a second row: the object
it drops is the old link's own, which the writer records as `series`. Only
stored state counts: an object only the writer passed (a stale copy read
earlier, a create from a copy of a stale book, a snapshot revert) is refused
without a row, so one loss is never recorded twice. A write that links the
book to the very series the stored object names (a renamed object) records
nothing either: the series is kept by its link. The
queued apply ignores the new change type (it is not a later edit), the
activity changelog renders it as "Stale series object dropped — was …", and
the book history dialog labels it and offers no undo (the server refuses one).

#### Repairs: stale-series fixer lists books whose series id and stored object disagree

`maintenance.relink-stale-series` gains a held class,
`held-series-id-mismatch`: a book with a `SeriesID` whose stored object names
another series. The row shows both series (linked id and name, embedded id and
name, whether the embedded series still exists); the owner decides. The plan
now point-reads every live book, not only the series-less ones. On prod, the
52 books the swapped title/author fixer held as `skipped_relink_series_first`
(dry run 01M42PT0K9KXH6YB0ECR9H3077) are all in this class.

#### History reads no longer cut fields out by name

The version-group fixer's resume check and the retire hand-off read one
field's newest history row (`GetMetadataChangeHistory(book, field, 1)`)
instead of a 200-row window of the whole book's history. The store orders
that history by field before applying the limit, so the window could cut the
field the check needed. The revert-metadata-fetch job reads a book's whole
history instead of its first 50 rows, for the same reason: its dry runs will
now show HIGHER "would revert" counts, because fields such as author_name,
the release year and the ISBNs used to fall outside the 50-row window. The
book changelog (`GET /audiobooks/:id/changelog`) reads the whole history too
(it was 100 rows) and still shows the newest 50 entries by time.
