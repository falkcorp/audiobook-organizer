### Added

#### Audit: manual-mock inventory and remediation backlog

Swept every Go test double in `internal/`, `cmd/`, and `pkg/` to find where a
hand-written mock is used in place of a mockery-generated one, and wrote the
result to `docs/audits/2026-08-16-manual-mock-inventory.md`. Documentation only —
no code or config changed.

The sweep found four things worth acting on.

**Two live policies contradict each other.** `.standards/instructions/go.md:49`
says "do not hand-write mocks"; `docs/CODING_STANDARDS.md:599-605` (added
2026-06-23, TOOL-5) says to *prefer* narrow hand-written fakes for new
interfaces. So a raw count of hand-written doubles is not a count of violations.
The audit records the decision taken — adopt the threshold rule now, with the
org-wide rule as the declared destination — and restates the destination rule in
a form that can actually be satisfied (the current wording cannot be: it has no
scope limit, so third-party interfaces like `tag.Metadata` and production Null
Objects both read as violations, and it has no exemption mechanism).

**CI cannot see any of this.** Both mock gates verify that *listed* mocks are
fresh; neither can detect an interface that was never listed, so every
hand-written double is green by construction. Separately, `check-mock-fresh` is
inert: it runs `go generate ./internal/database/...` and diffs the output, but
there are zero `//go:generate` directives in the repository, so the diff is
always clean and the step can never fail. Its error message tells the reader to
run `make generate`, a target that does not exist.

**One interface is hand-mocked in 13 different files.**
`internal/operations/registry.Reporter` (aliased as `pkg/plugin/sdk.Reporter`)
has 13 hand-written implementations under 8 different names, and
`internal/operations/registry` appears zero times in `.mockery.yaml`. One config
entry retires all 13. This is distinct from the generated
`MockProgressReporter`, which targets a *different* 3-method interface — that one
has its own cluster of duplicates and zero references.

**42% of the largest generated file is unused.** `.mockery.yaml` generates
standalone mocks for all 45 `internal/database` interfaces; only 8 are ever
referenced. The other 37 account for 22,001 of `mock_store.go`'s 52,384 lines.
They go unused because production constructors take the whole `database.Store`
god-interface rather than narrow ones, so nobody ever needs a `MockBookReader`.

One methodological caveat is recorded prominently in the audit: the census was
built from a naming-convention grep, and an orthogonal signature-based sweep
proved that pattern misses roughly a third of the population (types like
`recordingReporter`, `b3FaultStore`, `p2ProgReporter` match no naming
convention). All counts in the audit are stated as lower bounds, and the
recommended CI check detects doubles by signature rather than by name.
