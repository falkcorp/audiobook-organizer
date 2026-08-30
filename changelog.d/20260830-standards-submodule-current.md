### Fixed

#### `.standards` was pinned three versions behind, so nobody could read the standards

`CLAUDE.md` names `.standards/instructions/` authoritative and points
contributors and agents at it. A git submodule is a **pinned commit**, not a
live link, and this one had not moved since 2026-08-10. It served
`instructions/go.md` at version 1.0.0 while upstream had reached 1.3.0 — a gap
of 266 lines covering the Go version policy and the 1.26 minimum, the
`io/ioutil` ban and the rest of the deprecated-stdlib table, the
`wg.Go(fn)`-over-`Add(1)`/`defer Done()` rule, the testing-isolation table, and
the `omitempty` vs `omitzero` guidance for `encoding/json/v2`.

None of that was reaching anyone. Every rule was merged upstream and then read
by no consumer, because the pin is what the repo actually checks out.

A survey of the ~45 repositories carrying this submodule found **zero** pinned
at current: roughly 35 sit at a 2026-06-12 commit and 8 at 2026-08-10. There is
no sync automation anywhere in the org — the pin has only ever moved when
someone remembered.

So this bumps the pin *and* removes the need to remember it: a `gitsubmodule`
entry in `.github/dependabot.yml` puts `.standards` on the same weekly
multi-ecosystem schedule as the Go, npm, Docker and Actions dependencies.
Falling behind now opens a PR instead of going quietly unnoticed.

Nothing in CI or any script reads `.standards` — it is documentation consumed
by humans and agents — so the bump carries no build risk. Verified by grepping
every workflow, Makefile and script in the repo before making the change.
