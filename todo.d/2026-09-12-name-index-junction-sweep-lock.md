- [ ] **NAME-INDEX-LOCK-SPLIT** Stop holding `nameIdx.author` across `DeleteAuthor`'s full
      junction sweep (review follow-up on #3303). Every `CreateAuthor` from metadata apply,
      handlers and AI ops currently waits up to one full `book_authors` sweep per new name.
      Do NOT just move the sweep outside the lock: that reopens the concurrent-delete race on
      the whole-array rewrite of `book_authors:<id>` that #3303 closed. The intended shape is a
      dedicated junction-sweep mutex held across sweep and commit, with lock order
      `junctionSweepMu → nameIdx.author → nameIdx.alias`, and `nameIdx.author` covering only
      the row read, ownership check and commit. Related: `internal/metadata/enhanced.go`
      (~L288-313) holds `resolveMu` across `GetAuthorByName` and `CreateAuthor`, so one blocked
      worker freezes every other worker's resolve, fast-path hits included. Narrow it.
