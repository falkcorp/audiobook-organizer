### Fixed

#### `storage prune-checkpoints` and `diagnostics reserved-prefs --set`/`--delete` run only as the service's user

Both now follow the rule the other database-changing commands already
follow: they run only as the owner of the state directory, which is the
user the service runs as. Run as anyone else, they refuse before changing
anything and print the command to use instead (`sudo -u <owner>
audiobook-organizer ...`).

Before this, a prune run as root before the service's first checkpoint
sweep (after an upgrade that failed before the service came up, for
example) created a checkpoint lock file the service could not open. From then on
the service stopped clearing old upgrade checkpoints, with no error in its
log. Every later `storage restore-offline` also waited 30 seconds and then
failed, saying another prune was running when none was. The reserved-prefs
changes opened the database read-write as whoever ran them, which left
files in the database folder that the service could not replace.

#### A checkpoint lock file left by another user is reported, not waited on

If the checkpoint folder's `.sweep.lock` is not a plain file owned by the
service's user, for example one an earlier root run left behind, the
service logs an error and `storage prune-checkpoints` and
`storage restore-offline` refuse at once. Each names the file's owner and
gives the fix: chown the file to the service's user, or remove it while
nothing is pruning. A lock file that cannot be opened is no longer reported
as "another sweep is running" either.

#### `diagnostics query --raw` and the reserved-prefs listing open the database read-only

They no longer add files to the database folder, and `query --raw` no
longer creates an empty database at a mistyped path. The service-user rule
does not apply to them, though, like any tool that opens the database, they
still need the service's user or root to open its lock file.

#### The HNSW clean-stop marker's errors say what to do

When the similarity index's snapshot folder cannot be checked or synced
before a restore or a command-line change, the error now tells you to run
as the service's user, or to delete the marker by hand, and then to run
again.
