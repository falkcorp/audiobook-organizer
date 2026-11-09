### Fixed

- Tests: `TestPerimeter_ParityWithHEAD` and `TestPerimeterShim_AltSvcOnUnroutedAnswers` no longer flake when a second boundary falls between the two `/health` requests of a parity case. `/health` stamps its body with the wall-clock second, so the byte-for-byte body comparison saw `timestamp` differ by one. The system handler gains a `WithClock` setter (default `time.Now`, unchanged in production) and the parity stacks pin it, so the timestamp stays in the comparison and every other field is still compared exactly.
