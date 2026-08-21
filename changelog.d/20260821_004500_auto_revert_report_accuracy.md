### Fixed

- **Auto-revert bug reports no longer assert a cause they cannot know.** A commit
  with no CI run was labelled as carrying a skip-ci marker. That is only one of
  several reasons a commit has no run — GitHub starts workflows for the tip of a
  push only, so every interior commit of a rebase-merged PR also lands with zero
  runs. Issue #2652 blamed a marker on three such commits, none of which had one.
  The label is now "no gate run — never verified", with no cause attached.
- **A failed job lookup can no longer put a raw HTTP error body into the bug
  report.** `gh api` prints its error body on stdout, so a plain redirect captured
  it and `|| true` hid that anything went wrong. A dry run filed a report whose
  "Failing jobs" list was the four lines of a 404 error object. The output is now
  used only when the call actually succeeds, and a manual dispatch (which has no
  gate run to read) skips the lookup instead of 404-ing into it.
