### Fixed

#### Tests: the ops timeline property test no longer pushes internal/database past go test's 10-minute timeout

- `TestOpsV2Timeline_EquivalenceRandomHistories` runs each seed as a parallel subtest. Its seeds are independent stores, but they ran one after another, taking 145-317s of the package's 343-600s in CI's `-race` Go job; on main run 37263356760 the package hit the 10-minute timeout and failed go-ci. Locally under `-race` the test drops from 163s to 47s (67s with GOMAXPROCS=4, the CI runner's core count). Seed counts and assertions are unchanged.
