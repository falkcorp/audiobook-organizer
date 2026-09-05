## `TestCopyFile_PinnedNonce_OnlyOneWriterOpensTheTemp` is flaky in CI (2026-09-05)

- [ ] `internal/organizer/copyfile_race_test.go:201` failed once on PR #3072's
      `Go Tests (short, race)` job — the losing writer got a link `ErrExist` instead
      of the expected outcome — and passed 30/30 locally and on re-run. The test races
      two writers on a pinned temp-file nonce; on the CI runner's filesystem the
      loser's `os.Link` can observe the winner's rename before its own open fails.
      Decide whether the assertion is too strict for the real contract (exactly one
      writer opens the temp; the loser may fail at open OR at link) and fix the test,
      or fix the code if two writers can both reach `link`. Do not `t.Skip` it.
