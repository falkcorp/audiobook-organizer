### Changed

#### The tree is gofmt-clean, and stays that way

`gofmt -l` reported **43 unformatted Go files across 24 packages**. All are now
formatted, and a new `make fmt-check` target asserts it — wired into `make ci`
and into the CI job alongside `sdkguard` and `bench-check`.

This is the third instance of the pattern that produced those two guards, and it
was found while fixing them: `grep -rn 'gofmt' .github/workflows/ Makefile`
returned **zero hits**. Formatting was verified in neither CI nor the developer
command, so drift accumulated with nothing to report it.

The sweep and the gate landed together, in that order deliberately: a format gate
that precedes its own sweep is red on 43 pre-existing files from its first run,
which is the failure mode the `--enable-only nolintlint` comment in `ci.yml`
describes for errcheck — a permanently-red job gets switched off by whoever sees
it next.

`fmt-check` reports rather than rewriting. A gate that silently reformats a
contributor's working tree conceals the drift it exists to surface.

**On inertness.** The sweep is *not* whitespace-only — a first pass assumed so and
was wrong. Alongside indentation, `gofmt` split `stmt; os.Exit(1)` onto separate
lines, expanded inline struct definitions to multi-line form, and normalised doc
comments to the Go 1.19+ heading style. `git diff -w` is therefore not empty. What
establishes that nothing changed semantically is that all 24 affected packages
pass `go test -short` (22 with tests, 2 without) and `gofmt` is idempotent on the
result.

The CI job is renamed from `SDK Deps & Bench Build` to **`Repo Guards`**, since it
now covers three checks rather than two. The old name was not yet referenced
anywhere — it had not been added to the required-checks list.

**Still reports rather than blocks.** As with the other two guards, `Repo Guards`
is not among main's required checks (`Minimal CI / Minimal CI Summary`, `Require
changelog fragment`, `TODO Fragment Headers`), so a change that re-breaks
formatting goes red and can still merge. Making it binding is a branch-protection
change.
