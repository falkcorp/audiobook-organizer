The iTunes PID integrity audit (`itunes.pid_integrity`, the merge-orphan
census) no longer files a PID on a live non-primary version as a stale owner
when that version's group still has a live primary: it is a grouped version and
counts as healthy. Only a PID on a soft-deleted book, or on a non-primary book
whose group has no live primary (or no group), is a stale owner. A merge that
crowns an organized member over a PID-holding survivor therefore no longer
misfiles the survivor's track as a merge orphan. The audit reads each distinct
version group once, concurrently, and fails if a group cannot be read.
