### Fixed

#### Running a maintenance job twice no longer starts two copies at once

Clicking "Run" on a maintenance job while that same job was already running started
a second copy immediately, with both copies working over the same books at the same
time. Because each copy reads a record, changes part of it, and writes the whole
record back, two copies running together could overwrite each other's work — the
second one finishing last would silently undo whatever the first had just saved.

A second request now waits for the first to finish and then runs, instead of running
alongside it. Nothing is dropped or ignored: both runs still happen, one after the
other. A run started with different settings than the one already in progress — a
dry run versus a real run, say — is still kept as its own separate run rather than
being folded into the first.

The cause was that all 37 maintenance jobs left their "don't run this alongside
itself" marker blank, and every one of the three checks that would have queued the
second request is skipped when that marker is blank. So none of the protections had
ever applied to maintenance jobs at all. Each job now carries its own marker, and
jobs still run freely alongside *different* jobs — only alongside themselves are
they held back.
