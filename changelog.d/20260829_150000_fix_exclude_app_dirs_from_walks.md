### Fixed

#### Library walks now skip application directories by path, not by name

Library sweeps were kept out of the application's own directories by a **naming
coincidence** rather than a rule. `pathutil.ShouldSkipDir` skipped anything
dot-prefixed, and the case it was written for — multi-GB database backups — was
protected only because `backup_dir` happened to default to
`<root_dir>/.backups`. But `backup_dir` is an operator-settable absolute path:
point it at `<root_dir>/backups` (same location, no dot) and every walker
descends into the archives, hashing them and considering them for import.

Worse, one application directory never had a dot at all.
`openlibrary_dump_dir` defaults to `<root_dir>/openlibrary-dumps`, and was
being walked on **every scan** — dump archives plus a nested embedded database
of its own, thousands of files, none of them library content. `playlist_dir`
has the same shape.

`ShouldSkipDir` now takes a required `pathutil.AppDirs` value holding the
resolved, absolute application directories, and skips them and everything
beneath them regardless of what they are named. The parameter is **required,
not variadic or optional**, on purpose: an optional parameter can be silently
omitted at a new call site, which reproduces this exact bug in a new place with
no diagnostic. Making it required turns "a new walker forgot the exclusion"
into a compile error.

All four production walkers were updated — the scanner's discovery walk, the
scanner's file-count walk, the organizer's temp-file cleanup, and the
filesystem watcher.

The watcher needed a second fix. `addRecursive` was called both with the
library root and, from `handleEvent`, with a newly-created **subdirectory**.
Since `ShouldSkipDir` never skips its own walk root (returning `SkipDir` on the
first callback would abandon the whole walk), passing the subdirectory as the
root exempted the very directory being judged: a create event inside the
OpenLibrary dump directory would have re-added the entire dump tree to the
watch set. The library root and the walk start are now separate parameters.

Handled deliberately, and covered by tests:

- **An empty setting never matches.** `filepath.Clean("")` returns `"."`, a
  live relative prefix — cleaning an unset setting before checking it for
  emptiness could have silently skipped the entire library. The empty check
  happens first, and with a zero `AppDirs` the walkers behave exactly as they
  did before, with only the dot rule applying.
- **Sibling prefixes are safe.** Matching is component-wise via `filepath.Rel`,
  not `strings.HasPrefix`, so `<root>/backups2` is not skipped when
  `<root>/backups` is the backup directory.
- **A directory configured outside the library root** is simply never reached,
  and is not an error.
- **Relative paths are dropped rather than guessed at.** Resolving one against
  the process working directory could fabricate a match on a subtree nobody
  configured; failing open costs I/O, failing closed is silent data loss.
- `backup.ResolveDir` remains the single authority for resolving `backup_dir`,
  and the new `internal/appdirs` package is the single place that builds the
  directory set, so the scanner's discovery and count walks cannot drift apart.

One further hole was found in review and closed in the same change: exempting
only the walk root itself bought nothing. `filepath.WalkDir`'s first callback
survived, and then every *descendant* was matched and skipped — so a library
laid out as `author/title/` with `backup_dir` set to the library root scanned to
**zero books**, the same silent outcome as abandoning the walk, reached one
callback later. This was reachable without anyone setting `backup_dir =
root_dir`: the scanner walks each enabled import path as its own root, so an
import path added at or under `<root_dir>/openlibrary-dumps` would have
silently contributed nothing. An application directory that equals or contains
the walk root is now ignored entirely, on the same principle the root exemption
already encodes — an explicitly configured walk root is a deliberate choice, and
exclusion applies to application directories found *inside* the tree being
walked, never to the tree itself.
