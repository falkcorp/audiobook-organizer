- [ ] **CI never fetches Git LFS, so every audio-fixture test runs against a
      129-byte pointer.** `.gitattributes:1-5` tracks `*.m4b`, `*.m4a`, `*.mp3`,
      `*.flac` and `*.png` with LFS, and **no** workflow passes `lfs: true` to
      `actions/checkout` (checked every checkout step in `.github/workflows/`;
      zero matches for `lfs` across all of them). So on CI,
      `testdata/fixtures/test_sample.m4b` is 129 bytes of ASCII beginning
      `version https://git-lfs.github.com/spec/v1`.

      **Why this is silent rather than red.** `metadata.ExtractMetadata` does
      not error on an unparseable file — measured, not assumed: given 74 bytes
      of pointer text it returns a **nil error** and derives `Title` from the
      filename. So a test that imports the pointer gets a book, a `book_file`
      row with `Format` taken from the extension and `FileSize` 129 (which is
      `> 0`), and every plausible assertion passes. Green for the wrong reason.

      **Ten test files depend on the fixture**: `internal/server/`
      (`e2e_workflow`, `server_more`, `scan_edge_cases`, `organize_integration`,
      `itunes_integration`, `scan_integration`), `internal/audioutil/drm_test.go`,
      `internal/scanner/process_file_test.go`,
      `internal/metadata/real_audio_test.go`, and — until 2026-08-25 —
      `internal/importer/bookfile_on_import_test.go`.

      `testutil.CopyFixture` (`internal/testutil/integration.go:150-159`) is the
      shared chokepoint and validates only that the read succeeded, not that the
      bytes are audio. The `t.Skipf("fixture not found")` idiom used by
      `process_file_test.go` and `real_audio_test.go` guards the failure mode
      that cannot happen (missing file) and misses the one that does.

      ⚠️ `.gitattributes` carries a comment recording that this repo **has
      already been bitten by this exact thing** with PNGs and Playwright
      goldens. This is the third occurrence of one root cause.

      **The fix is two-part and the order matters.** Adding `lfs: true` alone
      could turn ten currently-green files red at once, because none of them has
      ever run against real audio in CI:
      1. Add a validating helper (reject a `version https://git-lfs` prefix) to
         `internal/testutil`, route `CopyFixture` and the `t.Skipf` sites
         through it, and make it **fail** rather than skip.
      2. Then add `lfs: true` to the `actions/checkout` steps, and fix whatever
         that surfaces.

      Done 2026-08-25 for `internal/importer/bookfile_on_import_test.go` only,
      and not by validating the fixture but by **dropping the dependency**: that
      test needs a file that exists, has a supported extension, and has a known
      size, so it now synthesises one. Worth considering for the other nine —
      several may not need real audio either, and the ones that genuinely do
      (`real_audio_test.go`, `drm_test.go`) are the ones the validating helper
      is for.
