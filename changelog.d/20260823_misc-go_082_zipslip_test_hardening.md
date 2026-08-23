### Fixed

#### Backup restore — the zip-slip guard's filesystem assertions could not fail

The `go/zipslip` containment guard on `RestoreBackup` (alert #13) was already in
place: `internal/backup/backup.go` normalises each tar entry name and routes the
join through `safepath.Join`, which rejects anything that escapes the restore
root after cleaning. Landed in `e5cf51f5` and hardened onto `safepath` in
`bbb9f503`. That part needed no change.

What did need changing was the test suite defending it. Three of the tests
claimed a filesystem-level assertion and none of them could fail.
`TestRestoreBackupValidatesAllEntries` used an entry named `../../escape` and
then stat'ed `tempDir/extract/escape` — a path *inside* the restore directory,
i.e. the single location that entry provably cannot reach.
`TestRestoreBackupRejectsZipslipAttack` stat'ed `tempDir/escaped.txt` for an
entry that resolves four levels higher, and `TestRestoreBackupRejectsDotDotInPath`
asserted only on the error string. All three passed whether or not the guard
existed.

Each malicious entry now escapes exactly one level, into the test's own
`t.TempDir()`, and the assertion is a directory listing of that parent with an
allow-list rather than a stat of one guessed path — so an escape under any name
is caught, not just the name the test author predicted. The filesystem check
runs *before* the error-contract check and uses `t.Error` rather than `t.Fatal`,
because a `t.Fatal` on the error return short-circuits the only assertion that
cannot pass vacuously. Verified by mutation: replacing the guard with a bare
`filepath.Join` makes all three report the escaped file by name and path.

Also added `TestRestoreBackupIgnoresSymlinkEntries`, pinning the fact that
`tar.TypeSymlink` and `tar.TypeLink` entries fall through to the default branch
and are skipped — an archive cannot plant a symlink out of the restore root and
write through it later. Nothing enforced that before, so link support could have
been added without anyone noticing the containment check does not cover
`Linkname`.

### Removed

#### `isPathWithinTarget` in `internal/backup`

Dead outside its own tests since the restore path moved to `safepath.Join`, and
its containment test was the weaker `strings.Contains(rel, "..")` form rather
than the `filepath.Rel` escape check the live path uses — a second, drifting
copy of a security check, with six tests exercising a function production no
longer calls. One of them, `TestIsPathWithinTargetRejectsAbsolutePath`, asserted
in its body the opposite of what its name claimed. Removed along with its tests;
`safepath` remains the single containment mechanism.
