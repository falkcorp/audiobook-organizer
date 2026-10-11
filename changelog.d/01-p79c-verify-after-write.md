### Changed

`verify_after_write` (default on) is now honoured by `tagger.WriteTagsSafe`.
After the atomic rename the file is re-read and every tag key that was written
is compared (keys case-insensitively, values as ordered lists; a key written
with no values must read back absent). A mismatch returns
`tagger.ErrVerifyAfterWrite` (a `*tagger.VerifyAfterWriteError` naming the
differing keys) and, when the file has a `book_file` row and a provenance
recorder is available, records a `FileEventObserved` event with the diff. The
file stays as written. A caller that used to get `nil` from a lossy write now
gets an error. The cgo writers in `internal/metadata/taglib_cgo.go` call
`fileops.WriteTagsSafe` directly and are not verified by this change; neither
are `WriteTagsInPlace` or `WriteImageSafe`. Set `verify_after_write: false` to
turn the check off.
