### Fixed

#### The interface-width ratchet no longer reports a number it cannot measure

`scripts/check-interface-width.sh` could fail with "interface width went UP
(4 -> 6)" on a tree where nothing had gone up. Run from `.worktrees/pathrepair`
it counted 6 findings; CI counted 4, and CI was right.

The two extra findings were `BookReader` (`internal/database/iface_book.go`) and
`ServerDeps` (`internal/plugins/maintenance/deps.go`). Both carry an explained
`//nolint:interfacebloat` at exactly the line each was reported against — in the
live tree. golangci-lint had attributed them to
`../absplit/internal/database/iface_book.go` and
`../absplit/internal/plugins/maintenance/deps.go`, and `.worktrees/absplit` had
been deleted.

Root cause: golangci-lint's result cache is keyed by file *content*, and every
git worktree of this repo declares the same module path with byte-identical
files, so an unchanged file replays whichever path was recorded first — outliving
the worktree that produced it. `//nolint` suppression is resolved by re-reading
the source at the reported position, so when that path is gone the directive is
never read and the finding it was suppressing leaks into the count.
`golangci-lint cache clean` restored the true count of 4.

This also corrects `.golangci.yml`, which documented the cross-worktree
attribution problem but concluded "it does not change the number". That was true
when written — it holds only while no declaration carries a `//nolint`, and
suppressed findings are exactly the ones it fails for. The `\.worktrees/`
exclusion cannot cover the case either: it matches only from the repo root, and
the script cds to the worktree root, where a sibling renders as `../name/`.

The gate now checks that every reported path resolves to a file and exits 2 —
"the instrument did not run" — rather than reporting a count. A poisoned cache is
wrong in both directions, so neither number is a measurement. This joins the
existing exit-2 case for a v1 binary reading the v2 config; exit 1 remains
reserved for a genuine ratchet violation, verified against both a real increase
and a real decrease.
