### Tooling / CI

- [ ] **~44 repos still pin `.standards` at a stale commit; none of them is at
      current.** Surveyed 2026-08-30 across every repo carrying the
      `falkcorp/.github` submodule. audiobook-organizer was fixed in #2996; the
      rest were not touched.

  | pin | dated | repos |
  | --- | --- | --- |
  | `664ae68` | 2026-06-12 | ~35 |
  | `5a59803` | 2026-08-10 | 8 (incl. this repo, now bumped) |
  | `7bdfd13` | 2026-08-30 | 0 |

  A submodule is a pinned commit, not a live link, and **there is no sync
  automation anywhere in the org** — the pin has only ever moved when someone
  remembered. Between 2026-08-10 and 2026-08-30 nobody did, while
  `instructions/go.md` gained 266 lines (Go version policy and the 1.26
  minimum, the `io/ioutil` ban, the `wg.Go` rule, the testing-isolation table,
  `omitempty` vs `omitzero`). Every repo whose CLAUDE.md calls `.standards/`
  authoritative was serving v1.0.0 rules.

  The fix per repo is the same two-line change #2996 made: bump the pin, and
  add a `gitsubmodule` ecosystem to `.github/dependabot.yml`.

- [ ] **Decide the sweep's real scope before running it — it is not free after
      it lands.** Adding `gitsubmodule` to ~45 repos means ~45 recurring PRs
      every week, forever. A large share of those repos (`gha-release-go`,
      `gha-detect-languages`, `release-strategy-action`, and the other
      single-purpose action repos) plausibly do not consume Go or TypeScript
      standards at all, so the pin being stale there costs nothing and the
      weekly PR costs review attention.

  Worth pricing an alternative first: fix `project-template` so new repos start
  current, plus a one-time bump for the handful of repos that people actually
  develop in (audiobook-organizer, subtitle-manager, gcommon, transcoderr,
  ubuntu-autoinstall-agent, overnight-burndown, magnet-handler,
  apt-cacher-go). That covers the repos where a stale standard misleads someone
  without signing up for a permanent PR stream across the action fleet.

- [ ] **`falkcorp/magnet-handler` `main` has a `toolchain` directive below its
      `go` directive** — `go 1.26.0` with `toolchain go1.24.2`. Per
      `.standards/instructions/go.md` v1.3.0 this is ours and a bug, not a
      blocker to wait on.

  It is currently latent, not breaking: its CodeQL `Analyze (go)` passes only
  because the repo has no root build script, so autobuild fell through to
  `go get ./...`, which self-healed (`go: downloading go1.26.0`, `go: removed
  toolchain go1.24.2`). A repo *with* a Makefile gets `make build` under
  `GOTOOLCHAIN=local` and fails outright — that is exactly how
  overnight-burndown #76 broke. **So the green check means only that nothing
  invoked the pinned toolchain.**

  Not fixed at the time of writing because magnet-handler's CI is already red
  for unrelated pre-existing reasons and a PR there would land on a red base.
  Recording it so the inconsistency with the standard is not silently dropped.
  Note its test suite writes `/usr/local/bin/magnet-handler-wrapper.sh` to the
  developer's machine — do not run `go test ./...` there casually.
