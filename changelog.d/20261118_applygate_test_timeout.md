### Fixed

- The `Go CI` job no longer times out in `internal/applygate` (`panic: test timed out after 25m0s` in `TestCheckTitle_AddedTextNeverAgrees`). The title check had no super-linear path; it repeated cheap work, and every regular-expression match costs far more under the race detector (every CI run), where `sync.Pool` drops a share of its Puts and the matcher's state is allocated again (97% of the bytes a title check allocated under `-race`). The title tokenizer and the segment splitter are now scanners that return exactly what the expressions returned (kept in the tests as the reference and compared over exhaustive and generated strings), and the sequence-number readers in `internal/seqnum` skip an expression when the text cannot contain what it needs (a marker word, a digit, a bracket), with a test that holds every such guard to its expression. No gate outcome changes. The `internal/applygate` package under `-race` with coverage went from 498 s to 179 s on the same machine, and from 56 s to 20 s without `-race`.

### Added

- `BenchmarkCheckTitle` and `BenchmarkCheckTitle_Length` in `internal/applygate` price one title check per outcome shape and over titles of 4 to 256 words, so a regression in the check's cost, or a super-linear step, shows in a benchmark before it shows as a CI timeout.
