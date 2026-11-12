// file: core/library/doc.go
// version: 1.1.0
// guid: f270ac67-f593-4ab0-a2f5-42435176815e
// last-edited: 2026-11-11

// Package library is the media core's library roots and safe filesystem access
// (05-library). A module never names a host path: it names a Path (a root ID and
// a clean root-relative name) and acts through the Scope the kernel binds to its
// owner.
//
// The package holds, beside the path-safety leaves it began with (SafeJoin,
// WithinRoot, SecureJoin, SecureJoinResolved, CleanAbsolutePath,
// SanitizeFilename, IsDangerousRoot, SafePath, Mounted, ReportPathNotify,
// EnsureDirWritable, Call, Stats and the DirMode, FileMode and SecretFileMode
// constants):
//
//   - the roots: RootSpec, Role, the registry (New, Reload, Bind), overlap and
//     sentinel checks, and Root with its confined RootFS (os.Root underneath);
//   - transfers: Move, Copy, Hardlink, Reflink, Symlink and Auto, journaled in
//     c/library/transfer, no-clobber at every step, crash-safe (Recover and the
//     core.library.transfer-recover task), with TransferIn committing the end of
//     the journal in the caller's batch;
//   - Hash, Quarantine and Release, the PathMapper, and Watch;
//   - the Scope that binds one owner, and the metrics of 05-library 5L.8.
//
// The byte-level primitives (the no-replace rename, clone, chunked copy) are in
// internal/libfs. The package is dark: nothing at HEAD imports it from the app
// yet (the F7 integrations flip wires it). It imports core's config, metrics,
// storage, rbac and tasks packages, fsnotify for Watch, and the standard
// library.
package library
