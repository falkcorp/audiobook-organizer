### Fixed

#### Changelog collection works again — release pin bumped past the EPIPE guard bug

`release-prod.yml` and `prerelease.yml` both pinned
`falkcorp/github-common`'s `reusable-release.yml` at `375d3b9e`, whose
"are there any fragments?" guard was written as
`! find changelog.d ... | grep -q .` under `set -o pipefail`. `grep -q` exits on
the first match, the still-writing `find` dies on `EPIPE`, and pipefail reports
the pipeline as failed *even though grep succeeded* — so the `!` fired the "no
fragments to collect" branch precisely when there were the most fragments. It
`exit 0`s, so six consecutive releases went green while the changelog was never
assembled and the backlog grew to 675.

Both pins now point at `66924760`, which replaces that guard with
`find -print -quit` (no pipeline at all) and closes four more instances of the
same shape found by sweeping the file — including one in the superseded-release
cleanup where an EPIPE would skip the "spare this keep-listed RC" branch and let
the tag fall through to `gh release delete --cleanup-tag`.

`prerelease.yml` matters as much as `release-prod.yml` here: the RC path is where
that release-deleting site lives.
