### Fixed

#### A crash in the metadata-apply path, and three checks that were not checking

`staticcheck` reported 11 findings across 10 sites, all pre-existing, which meant
`make ci` could not pass on `main`. It now reports none. Three of the ten were real
defects rather than lint noise.

**A nil dereference in applying metadata.** `service_apply.go` read `updatedBook.Title`
immediately after `UpdateBook` returned, while a `!= nil` check on the same variable sat
thirty lines further down — after the read that would already have crashed. The real
store never returns a nil book without an error, but `database.MockStore` returns exactly
that whenever its `UpdateBookFunc` is unset, so any test driving this path through the
default mock would panic. The call site now fails with a clear message instead of
dereferencing, and the misplaced check is gone.

**A regression guard with a hole in it.** All five iTunes operations are unimplemented
stubs. `TestStubOps_NoCronSchedule` exists to keep them from carrying cron schedules —
the incident it was written for had stubs burning a green "success" row every 10 and 30
minutes without doing any work. The test covered three of the five; `itunes.path-repair`
was covered by nothing, which is why it was the only one the linter could see as unused.
It is now covered.

**A test that would have passed on wrong data.** The regroup dry-run test seeded six
shattered chapter books plus one lone single-file book, collected all seven IDs, and then
never looked at them. It asserted that exactly one review hold was created and that the
hold named the right folder — but never that the hold covered the right *books*. A hold
that swept in the lone single-file book it is supposed to skip would have passed. It now
asserts membership exactly.

A fourth case of the same shape: the author-conjunction repair's write recorder declared
a field for book updates and never populated it, so a dry run that wrote a book was
invisible to the test built to "assert on silence."

The remaining findings were genuinely dead code and were removed, including a v2-to-legacy
operation converter that nothing called and nothing should — the operations registry keeps
the legacy row up to date by writing it, so converting at read time would have been a
second source of truth for the same row.
