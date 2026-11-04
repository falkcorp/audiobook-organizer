<!-- file: docs/agent-tasks/platform-core/PC-04-03-core-listeners-verbatim-copy.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7f3d2aa8-e4be-43e4-96a4-19d708c56e57 -->
<!-- last-edited: 2026-11-04 -->

# PC-04-03 — Listeners in core/httpapi: TLS, HTTP/2, HTTP/3, port-80 redirect and the peer allow-list, copied verbatim (dark)

| Field | Value |
|---|---|
| ID | PC-04-03 |
| Tier | **T2 Sonnet/high** — a near-verbatim copy of listener code and its tests into core; the only judgement is turning ABO globals into parameters without changing behaviour |
| Wave | W04 (S2, release `S2r1`), serialized step 2 (beside PC-04-05: no shared file) |
| Depends on | PC-04-02 |
| Trailer | `Stage-Release: S2 1` |
| Spec | 09 §9.4 S2 "Moves" (`server_lifecycle.go:903-1078` → `core/httpapi` with `internal/httpmw`); 05-http-api §8.4 row `internal/server/peer_allowlist.go`, §4.2 stage 0, §3.5 (`TrustedPeer`), §10 rows `core.http.listen`, `core.http.peer_allowlist`, `core.http.trusted_proxies`, `core.http.read_header_timeout`/`idle_timeout`; 09 §9.11 S2 row "Listener move changes TLS/H3/peer-allowlist behaviour" (mitigation: move verbatim first, tests move with it) |
| Digest | `docs/plans/platform-core/digests/S2.md` §3 (the `mediacore/httpapi` row) |

**Gate.** Open and make green any time; merge only after OWNER-03's `S1` row is in `ci/soak.txt` and PC-04-02 has
merged.

**Dark.** ABO keeps serving through its own copy in `internal/server` until the flip (PC-04-07), which switches
`serve` to these listeners and deletes ABO's copy. This PR touches no ABO file.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add ../audiobook-organizer-pc-04-03 -b feat/pc-04-03-core-listeners origin/main
cd ../audiobook-organizer-pc-04-03 && git rebase origin/main && git worktree list
make web-deps
```

Never run `go work init` (CLAUDE.md).

## Exact files

| Action | Path |
|---|---|
| create | `core/httpapi/listen.go` |
| create | `core/httpapi/listen_test.go` |
| create | `core/internal/httpmw/peer.go` |
| create | `core/internal/httpmw/peer_test.go` |
| create | `changelog.d/<date>_pc_04_03_core_listeners.md` (headerless) |
| modify (only if needed) | `core/go.mod`, `core/go.sum` — add `github.com/quic-go/quic-go` at **the same version as root `go.mod`** (`v0.64.1` at `f07f516be`); `check_module_skew.py` (S0) fails on a mismatch |

## Re-verify (counts at `f07f516be`; rerun at your base)

```bash
grep -n '^func (s \*Server) configureAndStartHTTP\|^func http3ServerQUICConfig\|^func redirectServerHandler\|^func (s \*Server) peerGatedHandler\|^func (s \*Server) bindMainListener' internal/server/server_lifecycle.go
#   expect 5 lines (~903, ~1065, ~1072, ~1078, ~1085)
grep -n '^func parsePeerAllowList\|^func (l peerAllowList) contains\|^func (l peerAllowList) openPrefixes\|^func validatePeerAllowList\|^func peerAllowedNow\|^func newPeerAllowListHandler\|^func startupPeerAllowList' internal/server/peer_allowlist.go
#   expect 7 lines
grep -c '^func Test' internal/server/peer_allowlist_test.go          # 16
grep -n '^func parseTrustAddr\|^func (p connectorPeers) contains\|^type onceSet struct\|^func newOnceSet' internal/server/client_trust.go   # 4
grep -n 'github.com/quic-go/quic-go ' go.mod                          # 1 (v0.64.1 at f07f516be)
ls core/httpapi/router.go core/app core/coretest/boot.go               # exist (PC-04-02)
ls core/httpapi/listen.go core/internal/httpmw 2>&1 | grep -c 'No such file'   # 2
```

**STOP and report** if any grep differs from the expected count (HEAD's listener code changed since this brief
was written: re-read the moved functions before copying), or if PC-04-02's files are absent.

## Steps

1. **Copy, do not redesign.** Copy the bodies of `configureAndStartHTTP`, `http3ServerQUICConfig`,
   `redirectServerHandler`, `peerGatedHandler` and `bindMainListener` (`internal/server/server_lifecycle.go`) into
   `core/httpapi/listen.go`, and `peer_allowlist.go`'s parse, contains, open-prefix, validate, decision, handler and
   startup functions plus the helpers they call (`parseTrustAddr`, `connectorPeers.contains`, `onceSet`) into
   `core/internal/httpmw/peer.go`. Keep every comment that states a reason (TLS 1.2 floor, 0-RTT off, 425 for early
   data, once-per-peer logging capped at 1024, the IPv6 /64 key, "load the key pair before READY").
2. **Replace ABO dependencies with parameters**, nothing else:
   - `s.httpServer`, `s.http3Server`, `s.httpRedirectServer`, `s.httpBoundAddr` → fields of a `Listeners` value.
   - `cfg ServerConfig` → `ListenConfig` (contract below).
   - `s.router` → the `http.Handler` argument. HEAD adds the `Alt-Svc: h3=":<port>"; ma=2592000` header with a
     gin middleware when `HTTP3Port != ""`; in core wrap the handler with an `http.Handler` that sets the same
     header before calling it.
   - `config.AllowedPeerCIDRs()` → a `func() string` the caller supplies (read per request, as HEAD does, so a hot
     reload keeps working); `tunnelConnectorPeers()` → `ListenConfig.TrustedProxies` (a `func() []httpapi.TrustedPeer` the caller supplies);
     `lifecycleLog`/`clientTrustLog` → the `*slog.Logger` in `ListenConfig`, keeping each message's text.
   - `startupLoginSaturated()` stays in ABO (it is not listener code); PC-04-07 calls it before starting these
     listeners.
   - `httpmw` cannot import `httpapi` (`httpapi` imports `httpmw`); define the zone-carrying peer type locally in
     `httpmw` and convert from `httpapi.TrustedPeer` in `listen.go`.
3. **Move the tests with the code.** Port all 16 tests of `internal/server/peer_allowlist_test.go` that exercise
   the moved functions into `core/internal/httpmw/peer_test.go` (allow-list decisions, headers cannot admit a peer,
   empty list allows all, invalid entries, zoned link-local, open prefixes flagged, own-interface address refused,
   early data refused) and into `core/httpapi/listen_test.go` (QUIC 0-RTT off, redirect handler gated and
   redirecting, every listener gated, plain HTTP gated, refuses to start on a bad allow-list). Keep their names
   with the receiver prefix dropped where it named an ABO type (for example
   `TestConfigureAndStartHTTP_EveryListenerIsGatedByThePeerAllowList` → `TestStartListeners_EveryListenerIsGatedByThePeerAllowList`).
   ABO's own tests stay where they are and keep passing.
4. **HTTP/3 smoke.** Add `TestStartListeners_HTTP3PortServes` (coined) in `core/httpapi` (external test package
   `httpapi_test`): a self-signed cert in `t.TempDir()`, `HTTP3Port: "0"` (as HEAD's test does), start, send one
   request over QUIC with `http3.Transport`, assert 200 and `Alt-Svc` on the TCP response; then `Shutdown`. Boot it
   through `coretest` only if `coretest` exposes a listener option at your base; otherwise call `StartListeners`
   directly with a trivial handler and say so in the PR description.
5. Headers on new files; changelog fragment: "Internal: core gains the listener and peer allow-list code (unused
   until the S2 r1 flip)."

## Code contract (names coined here; the behaviour is HEAD's)

```go
// core/httpapi/listen.go
type ListenConfig struct {
	Host, Port, HTTP3Port, TLSCertFile, TLSKeyFile string
	ReadHeaderTimeout, WriteTimeout, IdleTimeout time.Duration
	PeerAllowList  func() string        // core.http.peer_allowlist, read per request
	TrustedProxies func() []TrustedPeer // core.http.trusted_proxies, always admitted (HEAD's connector rule)
	Log            *slog.Logger
}
type Listeners struct{ /* main, http3, redirect servers; bound addr */ }
func StartListeners(cfg ListenConfig, h http.Handler) (*Listeners, error) // returns once the main port accepts
func (l *Listeners) BoundAddr() net.Addr
func (l *Listeners) Shutdown(ctx context.Context) error // HTTP/3 Close, main Shutdown(ctx), redirect Shutdown(ctx), in HEAD's order
```

`StartListeners` keeps HEAD's contract that when it returns nil the port already accepts connections (the comment
above `configureAndStartHTTP`), which the kernel relies on for readiness.

## TDD tests to write first

Port the tests of step 3 first (they fail to compile until `listen.go` and `peer.go` exist), then copy the code.

## Acceptance

```bash
(cd core && go build ./... && go vet ./httpapi/ ./internal/httpmw/)                      # exit 0
(cd core && go test ./internal/httpmw/ -count=1 -v 2>&1 | grep -c '^--- PASS')           # >= 11 (the moved peer tests)
(cd core && go test ./httpapi/ -run 'Listen|HTTP3|Redirect|QUIC' -count=1)                # ok
go test ./internal/server/ -run 'PeerAllowList|ConfigureAndStartHTTP|HTTP3|Redirect' -count=1   # ok (ABO copy untouched)
python3 scripts/ci/check_module_skew.py                                                   # exit 0
git diff --stat origin/main -- internal/ cmd/ main.go                                     # no output
make -C core ci                                                                           # exit 0
make ci                                                                                   # exit 0
```

## Out of scope

Stage 0's client-address resolution and `ClientAddrFrom` (PC-05-01); wiring these listeners into the kernel and
deleting ABO's copy (PC-04-07); any change of default ports, timeouts or TLS settings.

## Rollback

Revert the PR; ABO does not use the copied code yet.

## Repo rules

Worktree per task off fresh `origin/main`; 4-line headers on new files; headerless changelog fragment; conventional
commit `feat(httpapi): copy listeners and peer allow-list into core (dark)` ending with `Stage-Release: S2 1` and
`Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; never squash; never `--admin` past a
required check. No library-scale loop is added (CLAUDE.md concurrency).

## Planner notes

- The row's done-check says "an --http3-port smoke test in coretest", but `core/coretest/` is not in this row's
  files (PC-04-02 owns `core/coretest/boot.go`); this brief puts the smoke test in `core/httpapi/listen_test.go`
  and uses `coretest` only if it already offers a listener option.
- If core lacks `quic-go` at the base, this PR must edit `core/go.mod`/`core/go.sum`, which the row does not list.
