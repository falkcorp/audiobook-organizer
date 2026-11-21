### Fixed

- Main CI: gofmt-clean `internal/arch/layering_test.go` and `internal/books/topics/topics.go` (Repo Guards), report the index close error in `searchindextest` cutover instead of discarding it (Errcheck Ratchet, 684 back to 683), and list `searchindextest` as a deadcode-ratchet test-helper directory.
### Fixed

- Main CI: also handle the three discarded errors in the whisper server adapter (`mime.ParseMediaType`, `io.ReadAll`, `io.Copy`).
