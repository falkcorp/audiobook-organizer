// file: internal/books/roots/doc.go
// version: 0.1.0
// guid: 4323bbfc-ee16-4c65-afa6-c90403a79100
// last-edited: 2026-11-20

// Package roots is books' native view of its library roots (F7-09 slice T7,
// 05-library 5L.10). After the F7 flip the books library and every import root
// are entries of core's core.library.roots list, reached through the
// library.Scope the kernel binds to books. This package is what books builds on
// top of that Scope, in four pieces and one accessor:
//
//   - Reader lists the roots as books sees them: the library root (the
//     accessor RootDir, shaped for the readers of HEAD's AppConfig.RootDir) and
//     the import roots as ImportRoot, a view with the fields of HEAD's
//     ImportPath (ID, Path, Name, Enabled, CreatedAt, LastScan, BookCount) so a
//     caller that used database.ImportPath changes its source and not its
//     logic. The scan fields come from the rootscan row below.
//   - ScanStore is the rootscan:<root ID> family: books' own per-root scan
//     state (Enabled, CreatedAt, LastScan, BookCount), which is books' and not
//     library's, so it lives in a books family that M6 creates. Its writers
//     are field-level (RecordScan, SetEnabled) and serialized per root. HEAD's
//     scanner and autoscan op read the whole import-path row, set one field
//     and wrote it back, and two of them finishing together lost an update.
//   - Watcher is the adapter from HEAD's internal/watcher.Supervisor onto
//     library.Scope.Watch: one watch per enabled, watchable import root,
//     reconciled on a tick, re-subscribed when a registry reload replaces a
//     root, a rescan event treated as a trigger and never dropped.
//   - The browse half: core's GET /api/core/v1/library/browse lists
//     directories and nothing else, so it replaces HEAD's directory picker but
//     not the rest of HEAD's fileops browse service. This package carries what
//     is left and books-specific: the .jabexclude marker (Excluded,
//     ExcludedAmong, CreateExclusion, RemoveExclusion) through the confined
//     RootFS instead of os.* on host paths, and AllowedPath, the
//     Scope.Resolve replacement for fileops.ValidateUserPath and IsAllowedPath.
//     The prescope names this half without defining it; this is the reading
//     taken, stated in the PR. The "a path an admin is registering" case of
//     05-library 5L.3.8 is not here: it needs a host path outside every root
//     and is the fold's decision.
//
// The package is dark: nothing imports it until the F7 fold wires it into
// books' module and switches the scanner, autoscan, the filesystem handlers
// and the watcher over. It imports only core's library and storage packages and
// the standard library, so it is a leaf of the layering table; a test, not this
// code, pins its rootscan shape to the m6 step that writes the rows.
package roots
