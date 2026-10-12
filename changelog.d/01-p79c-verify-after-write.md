### Changed

`verify_after_write` (default on) is now honoured by every tag writer that
replaces a file's tags, in both build variants. After the atomic rename the
file is re-read and every tag key that was written is compared (keys
case-insensitively, values as ordered lists). A mismatch returns
`tagger.ErrVerifyAfterWrite` (a `*tagger.VerifyAfterWriteError` naming each
differing key with the value wanted and the value read) and records a
`FileEventObserved` event carrying that diff (bounded in length) and the
file's post-write SHA-256 and size. The event is keyed on the `book_file` row
when the file has one, else on the digest, so `AdoptOrphanEvents` can attach it
later. The file stays as written. A caller that used to get `nil` from a lossy
write now gets an error.

Verified writers: `tagger.WriteTagsSafe` (the default build's
`WriteMetadataToFile`, `WriteSingleTag`, `WriteTagProperties`, and the server's
movement-atom cleanup); in the `-tags native_taglib` build that production
uses, `writeMetadataWithTaglib`, `writeSingleTagWithTaglib` and
`writePropertiesWithTaglib`, which share the same read-back code
(`SafeWriteDeps.VerifyWritten`); and the metafetch write-back
(`metadata.WriteMetadataToFileAtomic`), when the native taglib writer made the
write.

Not verified: `tagger.WriteTagsInPlace` and `metadata.WriteMetadataToFileInPlace`
(they write a caller's temp copy; the metafetch write-back verifies the file
after its own rename instead), `tagger.WriteImageSafe` (cover art is not a tag
map), and any write the AtomicParsley, eyeD3 or metaflac fallback made after
native TagLib failed (those tools write a different tag set, so the native map
is not what they were asked for).

Comparison rules. A key written as `{}` or `{""}` is a delete (TagLib erases a
key whose joined value is empty) and must read back absent. MP4 stores
`TRACKNUMBER`, `DISCNUMBER`, `BPM` and `COMPILATION` as integers, so `03` and
`3`, or `03/10` and `3/10`, compare equal for those keys. Two keys that differ
only by case in one write map (`title` and `TITLE`) are folded before the write
(the upper-case spelling wins, else the first in sorted order) instead of being
written in map-iteration order.

`WriteMetadataToFileContext` no longer falls back to AtomicParsley, eyeD3 or
metaflac after a native write that failed only verification: the file is already
written, and the fallback would rewrite it with a different tag set.
`WriteTagProperties` reads the file back once (it used to read back twice in the
default build) and always verifies, even with `verify_after_write` off, because
operation reverts rely on a failed removal being an error. Set
`verify_after_write: false` to turn the check off elsewhere.
