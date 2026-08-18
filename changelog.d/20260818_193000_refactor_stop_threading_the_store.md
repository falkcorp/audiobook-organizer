### Changed

#### Callbacks no longer take a store at all; five more parameters narrowed

Two callback types threaded a database interface through their signature purely so
the implementation could reach a couple of methods:

- `undo.OnFileMovedFunc` — an inline `database.BookReader` + `database.BookVersionStore`, 44 methods
- `versions.NotifyDelugeFunc` — `database.Store`, all 398

Neither needed a store in the signature. The implementor already has one, so both
callbacks now take only their data (`bookID`, paths, versions) and the caller closes
over whatever it needs. That removes the dependency rather than narrowing it: there
is no interface to name, nothing to keep in sync, and no cross-package import. The
existing test callback in `internal/undo` never touched its store parameter either.

This matters beyond tidiness. Function types must match identically for assignment,
so a store threaded through a callback forces every implementor to accept the same
wide type. Dropping the parameter is what makes that constraint disappear.

Where a store genuinely is needed, the parameter is now sized to measured usage:

| site | demanded | used |
| --- | --- | --- |
| `undo.PreflightUndoConflicts` / `checkFileMoveConflict` | 398 | 2 |
| `undo.RunUndoOperation` / `revertChange` | 90 | 4 |
| `sweep.SweepArchivedBooks` | 78 | 3 |
| `deluge.NotifyDelugeAfterUndo` | 44 | 2 |
| `deluge.NotifyDelugeAfterVersionSwap` | 44 | 1 |
| `deluge.NotifyDelugeAfterOrganize` | 9 | 1 |
