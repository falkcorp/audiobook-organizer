### Fixed

- Extended the app-owned-directory walk guard from PR #2974 to the 16 remaining
  library-root walkers that never called `pathutil.ShouldSkipDir` at all. Cleanup,
  remux, transcode, reconcile, provenance-capture, path-repair and library-size
  walks no longer descend into the database backup directory or the OpenLibrary
  dump directory when those live inside `root_dir`.
- `cleanup-empty-folders` and `cleanup-organize-mess` delete by EMPTINESS rather
  than by filename, so an empty directory inside the backup or OpenLibrary dump
  tree was being removed. That is now prevented.
- The reported library size no longer counts database archives and OpenLibrary
  dumps as audiobook storage.
