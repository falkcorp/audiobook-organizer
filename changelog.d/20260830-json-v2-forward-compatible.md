### Fixed

#### `json.DiscardUnknownMembers` removed — the code now compiles on Go 1.27

`encoding/json/v2` declared `DiscardUnknownMembers` in Go 1.26 and **dropped it
in 1.27**, where jsonv2 graduates from a `GOEXPERIMENT` to the standard library.
Three call sites used it, so the repo would have failed to build on 1.27 for a
reason entirely of its own making, on top of the separate dependency blocker in
`github.com/cockroachdb/swiss`.

The option was a no-op: unknown members are ignored by default in both v1 and
v2, so `DiscardUnknownMembers(true)` only ever requested the behaviour already
in effect. Deleting it is therefore semantically identical on 1.26 and compiles
on 1.27 — verified on both toolchains rather than reasoned about.

This is a deliberate backport rather than an upgrade. The repo stays on 1.26
until `cockroachdb/swiss` (transitive via `pebble/v2/internal/cache`) lifts its
`//go:build (go1.20 && !go1.27)` gate, which is upstream's call. Doing the
source-compatible half now means that when the dependency does move, the
toolchain bump is a one-line change to `go.mod` and CI rather than a hunt for
API drift.

Audited the rest of the `encoding/json/v2` surface in use — `Marshal`,
`Unmarshal`, `MarshalWrite`, `UnmarshalRead`, `RejectUnknownMembers` — against
both toolchains. `DiscardUnknownMembers` was the only symbol that changed.
