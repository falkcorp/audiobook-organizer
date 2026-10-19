### Fixed

#### Exact census saves a finished sub-range before its closing budget wait

`countRange` in `internal/database/census_exact.go` counted a sub-range and then
made the final read-budget wait before anything saved the resume position. A
cancel, or a deadline the limiter refused, in that wait threw away every count
since the last save (up to one 64 MB sub-range, a few seconds of reading) and
the next run read it again. The counts were never wrong, only re-read. The last
pass over a sub-range now saves the sub-range's end first, the way a refresh
already did. A save at the open end of the key space moves the position to the
next piece, because a nil resume position means "restart the piece" and would
have counted it twice.
