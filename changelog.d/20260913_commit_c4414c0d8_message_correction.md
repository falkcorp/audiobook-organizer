### Changed

#### Correction: commit c4414c0d8 is mislabeled

Commit `c4414c0d8` on main carries the subject "feat(database): add the
book_atpath multi-valued path index", but its content is part of #3342
("fix(database): fail set reads on an unreadable member"): the partial-read
helper in `internal/database/partial_read.go`, fail-closed reads in the
ABS-session, auth and playback stores, and the matching organizer, API-key
sweep, ABS `me` and reading-handler changes. Two tasks shared one
commit-message file, so the wrong message was used. Main is not rewritten.

The book_atpath index itself landed in `9b183d1d3` and the follow-up commits
from #3346 (`291fdee4c`, `5c79e5074`, `4b05c7a8d`, `0daa949d2`, `6f6019f02`).
When bisecting or reading history, treat `c4414c0d8` as #3342.
