### Fixed

#### Release notes are no longer empty ("No commits available")

Every release and draft this repo produced listed its changes as
`- No commits available`, and drafts silently stopped refreshing — the
`v0.217.9` draft was stuck quoting `rc.61` while the repo was on `rc.87`, and
three duplicate broken drafts accumulated against that tag.

Root cause: `reusable-release.yml` checks out the ghcommon helper scripts at a
ref taken from `.github/ghcommon-ref.txt`, falling back to a hardcoded SHA when
that file is absent. This repo had no such file, so it ran the fallback
`e04c222a` — a version of `release_workflow.py` predating the changelog fix. In
that version the diff base is:

    describe = _run_git(["describe", "--tags", "--abbrev=0"])
    last_tag = describe.stdout.strip()

`git describe --tags` returns the newest tag reachable from `HEAD`, and by the
time the changelog step runs, that is *the tag the job just created*. So the
range was always `vX.Y.Z..HEAD` against itself — empty by construction. That
version also had no `PREVIOUS_VERSION` support, which is why passing
`previous-version=v0.217.4` to `release-prod.yml` was accepted and silently
ignored (confirmed in the v0.218.0 run log: the env var was set correctly, and
the output still read "Commits since v0.218.0").

Fixed by adding `.github/ghcommon-ref.txt` pinned to
`d0c3326b96557c8ea9117c1c196b628e5e028186` — deliberately the *same* SHA
`prerelease.yml` and `release-prod.yml` already pin `reusable-release.yml` to,
so the workflow and the scripts it executes now come from one commit rather
than drifting apart. That revision selects the previous stable tag correctly
and honours the `previous-version` override.

Note: the v0.218.0 release notes were generated before this fix and are
therefore still empty; its contents are the month of work between v0.217.4 and
v0.218.0.
