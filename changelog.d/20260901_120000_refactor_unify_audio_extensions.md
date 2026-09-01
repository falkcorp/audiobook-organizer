### Fixed

#### `supported_extensions` was ignored by fifteen code paths, so half the library's formats were watched, relinked and repaired by nothing

`supported_extensions` ships with fifteen audio extensions and is user-editable.
The ingest scanner reads it. Almost nothing downstream did. The filesystem
watcher held its own 8-entry list; the iTunes heal path held a 7-entry list in
three places; `backfill-book-files`, `relink-missing-to-itunes`,
`repair-missing-files`, `refetch-missing-authors`, `scan-composer-tags` and the
relink report each held their own 5-to-8-entry list; the file-provenance capture
held another; `metafetch.AudioFilesInDir` globbed a private 8-pattern list.

The consequence was not an error. A library holding `.aax`, `.aaxc`, `.aiff`,
`.aif`, `.mka`, `.oga` or `.wav` books — all shipped defaults — got those books
scanned and imported, and then never watched for changes, never given
`book_file` rows by the backfill job, never relinked to their iTunes tracks,
never repaired when their path went stale, and never provenance-captured. Every
one of those jobs reported a clean run over a library it had silently
half-skipped.

The canonical list now lives in one leaf package, `internal/audioext`, and every
predicate that asks "is this file part of the library?" resolves against the
configured value through `config.SupportedExtensionSet()`.

Three further defects fell out of the consolidation:

* **`supported_extensions: []` disabled file recognition entirely.** `InitConfig`
  guarded with `viper.IsSet`, which is true for an explicitly empty list, so a
  user's empty value was written straight into `AppConfig` and every predicate
  then answered "not audio" for every file. The guard is now `len > 0`, matching
  what `internal/config/persistence.go` already did, and `audioext.Resolve` falls
  back to the compiled-in default for a nil or empty list — `AppConfig` is a
  package-level zero value, so nil is the state of any binary that has not run
  `InitConfig`.
* **`metafetch.AudioFilesInDir` could not see two whole classes of file.**
  `filepath.Glob` is case-sensitive on Linux, so `Chapter 01.MP3` was invisible
  in production and visible on a developer's case-insensitive Mac; and a folder
  whose name contains a glob metacharacter — `The Hobbit [Unabridged]` — made
  every pattern match nothing, so the book appeared to have no audio at all. It
  now reads the directory and tests the extension.
* **`reconcile.FindUntrackedFiles` read `config.AppConfig` without the lock.** It
  goes through the locked accessor now.

Extension lists that answer a *capability* question rather than a membership one
are deliberately left narrow and renamed to say so:
`fingerprint.FingerprintableExtensions`, `acoustid.fpcalcDecodableExtensions`,
`maintenance.transcribableExtSet`, `metafetch.coverEmbeddableExts`. Widening
those from config would be a regression, not a de-duplication — `.aax` and
`.aaxc` are library extensions and are DRM-encrypted, which
`internal/audioutil.DetectDRM` documents this application cannot decode at all,
so a fingerprint or transcription list that excludes them is correct. The MIME
and DRM mapping tables (`abs/mapper.go`, `audioutil/drm.go`) are untouched for
the same reason.

`.mp4` is deliberately **not** added to the canonical list: it feeds the ingest
scanner, and it is overwhelmingly a video container, so adding it would import a
trailer sitting in a library folder as an audiobook. The two callers that
recognise `.mp4` today — `linkintegrity/classify.go` and
`maintenance/junk_title_derive.go` — are therefore left alone rather than routed
through config, which would have silently removed that recognition.
