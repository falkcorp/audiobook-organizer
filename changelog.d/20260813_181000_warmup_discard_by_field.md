### Changed

#### Warmup byte accounting now says WHICH field the discarded bytes belong to

The aggregate shipped earlier the same day established the size of the problem:
on production, **1,853 MB of the 2,436 MB** read in the `book_files` warmup
phase — **76%** — is decoded and then thrown away by `stripBookFileForMemdb`.
That phase is 113,778 ms of a 134,572 ms warmup.

It does not say which field, and the candidates are not interchangeable.
`AcoustIDFingerprint` is a `[]byte` inline in the row; moving it to a sidecar
key means rewriting ~13 read sites and retiring the write-back preserve-guards
in `pebble_store_bookfiles.go` that exist *because* a bare memdb round-trip once
wiped fingerprints in production — the two writes would have to become one
atomic batch. `IntroTranscription` and the diagnostic strings are `*string`
fields with far fewer readers and no such guard. Recommending the expensive,
data-loss-sensitive option on the strength of an aggregate that might be
dominated by the cheap one would repeat exactly the error the aggregate caught:
the call graph implied ~99% and the measurement said 76%.

So the warmup log gains `discarded_field_mb`, splitting the total across six
field groups, and the `books` phase — 729 MB across 67,824 rows, the
second-largest and previously unmeasured — is now accounted too.

One finding fell out of writing the tests. **`AcoustIDSeg0..6` cannot be seeded
through `CreateBookFile` at all**: the write path deliberately omits them from
the stored row and keeps them only in the `book_file_acoustid:` secondary index
(already pinned by `TestCreateBookFile_StoredValueLacksSegs`). A row written by
current code contributes exactly zero to that group. So a nonzero
`acoustid_seg0_6` in production is **not** ongoing cost that a schema change
would remove — it measures how much un-migrated legacy data is still in the
keyspace, and the remedy is a backfill, not a redesign. That is recorded as its
own test, seeding a legacy row as raw JSON because the supported API refuses to
produce one.

`BookSigV1` and `BookSigV1Mask` are charged their plain length, not
`EncodedLen`: they are already base64 *strings* in the struct, so encoding them
again would overstate the books phase against `book_files` by 4/3.
