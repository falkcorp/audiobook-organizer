### Changed

- The guards in front of the sequence-number expressions in `internal/seqnum` are now derived from each compiled expression instead of written beside it by hand. A walk of the expression's syntax tree gives the text it needs (a keyword, a digit, a bracket, a dash; adjacent literals are multiplied out first, so `no` followed by `te` needs `note` and not an `n`, an `o`, a `t` and an `e` somewhere), and an expression that needs nothing is left unguarded. Before, a keyword added to an expression and not to its hand-written list made the guard hide a match while the tests stayed green; the 26 hand-written lists are gone, and the new tests compare each derived condition with the one it replaced, hold the walker to the `regexp` package over thousands of generated expressions, and check that a keyword added to an expression is found. Readers return what they returned before.
- `isRomanNumeral` in `internal/applygate` is one `switch` over the units, and its comment no longer says units can begin with an `x` (none can).

### Added

- A CI job, `Full-Size Tests (applygate, seqnum)`, runs those two packages at full size without the race detector (about half a minute). Under `-race` their generated texts are a fifth as many and their exhaustive walks one length shorter, so a regression only the larger draw finds would otherwise reach main.

### Removed

- `refSegTokens` and its whole-function test in `internal/applygate`: a second copy of `segTokens`, kept only to compare against; the scanner-by-scanner tests that remain cover the same ground.
