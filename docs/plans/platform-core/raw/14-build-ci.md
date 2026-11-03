<!-- file: docs/plans/platform-core/raw/14-build-ci.md -->
<!-- version: 1.0.0 -->
<!-- guid: f51940fb-f486-4cc0-b093-addc4a7f64a1 -->
<!-- last-edited: 2026-11-03 -->

# Raw findings 14: build, CI, release, tooling

Anchors are repo-relative `path:LINE` at commit 748fd787a (checkout is 57fd77ffc, two docs-only commits ahead; build/CI files unchanged).

## 1. Makefile (`Makefile`, 961 lines, v2.49.4)

Header facts: `export GOTOOLCHAIN := go1.27.2` (Makefile:43) applies to every recipe, including those in the included `Makefile.local`. Build flag vars: `GO_BUILD_TAGS := pprof` (:74), `GO_BUILD_FLAGS := -trimpath` (:75), `GO_LDFLAGS := -X main.version=$(VERSION)` (:76), `GO_STRIP_LDFLAGS := -s -w` (:77). `VERSION` = `git describe --tags --always --dirty` (:7). `-include Makefile.local` is at :80, AFTER the flag vars, so a `:=` in Makefile.local overrides them. Default goal `all: build` (:94). The Makefile says the Dockerfiles, `.goreleaser.yml` and CI workflows do NOT use these flag vars (:62-65).

### Targets (line = recipe line; purpose)

| Target | Line | Purpose |
|---|---|---|
| all | 94 | alias for `build` |
| help | 97 | prints target list |
| install | 146 | = `web-install` |
| build | 154 | `web-build`, then `go build -tags "embed_frontend pprof"` -> `./audiobook-organizer` |
| build-api | 160 | go build without `embed_frontend` (API only) |
| build-bench | 166 | go build `-tags "bench pprof"` (dedup bench CLI) |
| build-linux | 172 | `web-build`, cross-compile linux/amd64 with musl gcc, `CGO_ENABLED=1`, tags `embed_frontend fts5 native_taglib pprof`, static link, stripped -> `dist/` |
| run / run-api | 182 / 186 | build then `serve` |
| web-install | 192 | `npm install` in web/ |
| web-build | 198 | `npm run build` (tsc 7 + vite) -> web/dist |
| web-typecheck | 204 | `npm run typecheck` |
| web-dev | 210 | Vite dev server |
| web-test | 214 | `npm run test -- --run` (vitest) |
| web-lint | 220 | eslint |
| web-lint-memory | 226 | `scripts/check-memory-leaks.py` |
| fixtures / fixtures-check | 241 / 246 | fetch sha256-pinned audio fixtures (`scripts/fetch_fixtures.py`) |
| audiobooth-decode | 255 | macOS Swift decode proof of ABS fixtures (needs Swift 6.2+) |
| test | 263 | `vet` then `go test ./... -v -race -timeout 25m` (full, includes slow property tests) |
| test-fast / test-fast-short | 275 / 281 | same with TMPDIR on a RAM disk (`scripts/with-ramdisk-tmpdir.sh`) |
| test-short | 304 | `vet` then `go test ./... -short -race -coverprofile=coverage.out -covermode=atomic -timeout 25m` |
| test-short-shard | 321 | `scripts/ci/short_test_shards.py --shard k/N` -> `coverage-k.out` (CI matrix of 4) |
| test-fixtures | 351 | `scripts/ci/fixture_test_packages.py --run -- -race -count=1`: tests that skip under `-short`, no `-short`, optional `FIXTURE_SHARD=i/n` |
| vet | 358 | `go vet ./...` |
| vet-integration | 366 | `go vet -tags integration ./...` (compile gate for integration-tagged tests) |
| mutate / mutate-dry / mutate-matrix | 397 / 415 / 439 | gremlins mutation testing on one `PKG=`; refuses to run in the primary checkout |
| mocks | 456 | regenerate mockery v3.8.0 mocks from `.mockery.yaml` |
| mockery-install | 471 | build pinned mockery from `tools/mockery/go.mod` |
| mocks-check | 479 | regenerate then `git diff` over `internal/**/mocks/**`, `internal/ai/mock_*_test.go`, `internal/metadata/mock_*_test.go`; fail if stale |
| staticcheck-install / staticcheck | 512 / 524 | build pinned staticcheck (`tools/staticcheck`), run `./...` |
| oplint | 530 | `go run ./tools/cmd/oplint ./...` (ops v3 definition analyzer) |
| reconcile-paths | 536 | build the read-only reconcile tool to `bin/` |
| sdkguard | 542 | `go run ./tools/cmd/sdkguard/main.go` (no new `internal/` deps from `pkg/plugin/sdk`) |
| generate-ops / ops-check | 549 / 553 | `tools/cmd/opsgen` regenerate / `-check` web/src/generated/ops.ts |
| tygo-install / generate-types / types-check | 561 / 565 / 570 | tygo (pinned in `tools/tygo`), regenerate web/src/types/generated, `scripts/ci/check_generated_types.sh` |
| bench-check | 591 | `go build -tags bench -gcflags=-e ./...` typecheck of bench-tagged code |
| pprof-check | 597 | `go vet -tags pprof .` then `go test -tags pprof -run Pprof -count=1 .` |
| fmt-check | 618 | fail if `gofmt -l .` (excluding web/) is non-empty |
| test-all / test-all-short / test-nightly / test-frontend | 631 / 634 / 637 / 640 | `test web-test` / `test-short web-test` / `test web-test coverage-check` / `web-test` |
| test-everything | 646 | backend + vitest + playwright, prints matrix verdict, kills stale :8484 server first |
| test-e2e / test-e2e-demo | 684 / 690 | `npm run test:e2e` / chromium-record project |
| manual-smoke, smoke-create-books, smoke-run-demo | 696-704 | manual demo data + run |
| coverage | 725 | `go test ./... -coverprofile` + html report |
| coverage-check | 736 | full-suite coverage, fixed 30% floor |
| coverage-check-short | 751 | reads `coverage.out`; floor from `.ci/coverage-floor.txt` (currently 30); warns on drop vs `.ci/coverage-last.txt`; rewrites last file |
| ci | 785 | see section 2 |
| ci-woodpecker / ci-remote | 795 / 798 | `scripts/ci_woodpecker.py`, `scripts/ci_remote.py` (run CI on remote nodes) |
| build-mtls-bridge / -windows | 802 / 807 | `go build ./cmd/mtls-bridge` (windows variant `CGO_ENABLED=0`) |
| clean | 812 | rm binary + coverage files |
| docker / docker-run / docker-stop | 820 / 826 / 832 | docker build `audiobook-organizer:latest`, compose up/down |
| version | 839 | prints VERSION |
| release-dry-run / release-snapshot | 843 / 850 | `web-build` then `goreleaser check` + snapshot release |
| backup | 857 | ssh tar of the Pebble store from `DEPLOY_HOST`, scp to `BACKUP_DIR` |
| rollback | 892 | swap `$(DEPLOY_BIN).prev` back via ssh after `scripts/storage_format_guard.py`, wait on readiness (`scripts/deploy-wait-ready.sh`) |
| t, c, b, v | 921-924 | aliases for test, coverage, build, version |
| lint-errcheck(-full) / lint-errcheck-ratchet | 937-943 | golangci-lint errcheck; ratchet = `scripts/check-errcheck-ratchet.sh` vs `.errcheck-baseline` |
| deadcode-install / lint-deadcode-ratchet | 951 / 954 | `scripts/deadcode-ratchet.py` (new unreachable production funcs fail) |
| lint-width(-full) | 957-960 | golangci-lint `interfacebloat,nolintlint` |

### Makefile.local.example (`Makefile.local.example`, 168 lines, v1.9.1)

- Template, copied to the gitignored `Makefile.local`; included by the committed Makefile (Makefile:80). It holds ONLY machine-local deployment: `DEPLOY_HOST`, `DEPLOY_BIN`, `DEPLOY_DB`, `DEPLOY_URL` (values in the example are placeholders; the host default in the template is an IP and is not reproduced here: `<ip-1>`), and the targets `deploy`, `deploy-debug`, `pprof-dropin`, `pprof-off`.
- `deploy`: `scripts/deploy-preflight.sh origin/main`, `make build-linux`, scp to host, `scripts/deploy-match-unit-type.sh`, keep previous binary as `.prev`, `systemctl restart`, wait for `/health` ready (300 s), then compare `/api/v1/system/version` with `git describe`.
- It deliberately sets no GOTOOLCHAIN because the committed Makefile exports it first (Makefile.local.example:21-27).
- `DEPLOY_*` are declared `?=` empty in the committed Makefile (Makefile:46-58), so `backup`/`rollback` fail loudly when unset.

## 2. `make ci` step by step (Makefile:785)

`ci: mocks-check staticcheck oplint sdkguard ops-check types-check bench-check pprof-check fmt-check test-all-short coverage-check-short lint-errcheck-ratchet lint-deadcode-ratchet`
Prerequisites run serially left to right (no `-j` in the Makefile), then prints "All CI checks passed". Prerequisite chains expand as:

| # | Target | Line | What actually runs |
|---|---|---|---|
| 1 | mocks-check (via mockery-install) | 479 (471) | build mockery from tools/mockery; regenerate; git diff must be empty |
| 2 | staticcheck (via staticcheck-install) | 524 (512) | `go -C tools/staticcheck install tool`; `staticcheck ./...` |
| 3 | oplint | 530 | `go run ./tools/cmd/oplint ./...` |
| 4 | sdkguard | 542 | `go run ./tools/cmd/sdkguard/main.go` |
| 5 | ops-check | 553 | `go run ./tools/cmd/opsgen -check` |
| 6 | types-check | 570 | `scripts/ci/check_generated_types.sh` (tygo into tmp dir, `diff -r` vs web/src/types/generated) |
| 7 | bench-check | 591 | `go build -tags bench -gcflags=-e ./...` |
| 8 | pprof-check | 597 | `go vet -tags pprof .`; `go test -tags pprof -run Pprof -count=1 .` |
| 9 | fmt-check | 618 | gofmt cleanliness |
| 10 | test-all-short -> test-short -> vet | 634 -> 304 -> 358 | `go vet ./...`; `go test ./... -short -race -coverprofile=coverage.out -covermode=atomic -timeout 25m` |
| 11 | test-all-short -> web-test | 634 -> 214 | vitest single pass |
| 12 | coverage-check-short | 751 | total coverage >= `.ci/coverage-floor.txt` (30) |
| 13 | lint-errcheck-ratchet | 943 | `scripts/check-errcheck-ratchet.sh` |
| 14 | lint-deadcode-ratchet (via deadcode-install) | 954 (951) | `scripts/deadcode-ratchet.py` |

Not in `make ci`: `web-lint`, `web-typecheck`/`web-build` (typecheck), e2e, `vet-integration`, `test-fixtures`, `lint-width`, golangci-lint. Those are covered by GitHub/Woodpecker jobs below. Note `make test-short` costs ~8 min on idle hardware per the Makefile's own measurement (Makefile:292-299).

CI gate registry: `ci/gates.txt` (32 lines) lists 9 gates read by `scripts/ci/run_gates.py` (errcheck, interface-width, deadcode-ratchet, store-width, store-references, layering, appconfig-reads, slog-guard, generated-types), columns name/command/baseline/kind/lower-command/job (ci/gates.txt:10-30). Of these, `layering` runs `go test ./internal/arch/` (an architecture test package that exists in-tree), and `store-width`/`store-references` are ratchets on the `database.Store` interface size: directly relevant to carving a core, since they pin how wide Store is. `ratchet-lower.yml` weekly lowers baselines automatically (ratchet-lower.yml:83).

## 3. Build tags and CGO

Counted by `grep '^//go:build'` across all .go files (excluding web/node_modules).

| Tag | Files | Purpose | Enabled by |
|---|---|---|---|
| `embed_frontend` | web_embed.go:1, internal/server/static_embed.go (and static_embed_recovery_test.go:1) | embed `web/dist` and serve SPA | make build, build-linux, Dockerfiles, goreleaser, e2e |
| `!embed_frontend` | web_nonembed.go:1, internal/server/static_nonembed.go | empty FS / no-op | default (API-only, tests) |
| `native_taglib` | internal/metadata/taglib_cgo.go:10 | CGO TagLib C bindings for tag writing | build-linux, Dockerfile*, `.woodpecker/test-native-taglib.yaml` |
| `!native_taglib` | internal/metadata/taglib_support.go:9 | pure-Go WASM TagLib (go.senan.xyz/taglib) writer, no CGO | default, goreleaser (`CGO_ENABLED=0`) |
| `bench` | cmd/dedup_bench*.go (9 files, lines 5-6), internal/server/bench.go:6 | dedup experiment CLI | `make build-bench`, `bench-check` |
| `!bench` | internal/server/bench_nobench.go:5 | stub counterpart | default |
| `pprof` | pprof_debug.go:6, pprof_debug_test.go:6, internal/diag/flight/flight_pprof_test.go:6 | compiles in the loopback-only pprof listener, active only if `ABK_PPROF_ADDR` is set and loopback | `GO_BUILD_TAGS := pprof` default in all Make builds (Makefile:74) |
| `integration` | internal/server/transcode_integration_test.go:6, internal/itunes/integration_test.go:4, internal/transcode/transcode_integration_test.go:6, internal/testutil/synthetic_audio.go:6 | ffmpeg/real-fixture tests | `make vet-integration` compiles only |
| `race` / `!race` | internal/{database,dedup,matcher}/race_enabled_*_test.go, database/pebble_store_ops_v2_timeline_*race_test.go | test knobs that differ under -race | go test -race |
| `fts5` | no Go source references it (grep of *.go and go.mod finds none); passed in `build-linux` and both Dockerfiles only | vestigial (a SQLite-era tag; no sqlite driver in go.mod) | Makefile:176, Dockerfile:75 |
| OS tags | `windows` (9), `!windows` (12), `linux` (8), `darwin` (8), `unix` (6), `!unix` (4), `linux || darwin` etc. | platform splits (umask, fileops, disk stats) | GOOS |

CGO: needed only for `native_taglib`. Link flags `-ltag_c -ltag -lz -lstdc++ -lm -lgcc -lgcc_eh` against static libs in `third_party/taglib/lib` (internal/metadata/taglib_cgo.go:15-16); the repo tracks only `third_party/taglib/build-static.sh` and `include/tag_c.h`; libs are built by `scripts/ci/build_taglib_static.py` (TagLib 2.0.2) or in the Dockerfile. Default builds, goreleaser and `go test ./...` need no CGO. Only one `import "C"` exists in the module (taglib_cgo.go:20). Pebble is pure Go (go.mod:7).

## 4. Dockerfiles and compose

| File | Facts | Anchor |
|---|---|---|
| Dockerfile | 3 stages: `node:26-alpine@sha256` frontend-builder (`npm ci`, `npm run build`); `golang:1.27.2-alpine@sha256` go-builder builds TagLib 2.0.2 + utfcpp 4.0.6 from source with sha256 checks, copies `libtag*.a` to `third_party/taglib/lib`, then `CGO_ENABLED=1 go build -tags "embed_frontend fts5 native_taglib"` static stripped; runtime `alpine:3.24@sha256` + ca-certificates, tzdata, ffmpeg, non-root uid 1000, `VOLUME /data`, port 8484, wget HEALTHCHECK on `/health`, `CMD serve --host 0.0.0.0 --db /data/audiobooks.pebble` | Dockerfile:13,27,31-52,74-78,82-104 |
| Dockerfile.build-cgo | same build for release binaries via `docker buildx ... --output type=local`; adds TARGETOS/TARGETARCH args, no sha256 verification of the TagLib tarballs, no runtime stage | Dockerfile.build-cgo:1-12,25-29,39-40,68-69 |
| docker-compose.yml | one service building the Dockerfile, `AO_PORT` (default 8484) published, volumes `ao-data:/data` and library dir mounted read-only at `/audiobooks`, env `AO_DIR`, `AO_DB`, optional basic auth (`BASIC_AUTH_*`), restart unless-stopped, wget healthcheck | docker-compose.yml:7-31 |
| deploy/ | systemd unit `deploy/systemd/audiobook-organizer.service`, launchd plists (3), `deploy/grafana`, `deploy/prometheus`, `deploy/local.conf.example`, README | deploy/ |
| .dockerignore | present (not inspected) | .dockerignore |

## 5. Version pins

### Go (every copy; canonical pin is Makefile:43)

| Copy | Value | Anchor |
|---|---|---|
| Makefile | `export GOTOOLCHAIN := go1.27.2` | Makefile:43 |
| .envrc | `export GOTOOLCHAIN=go1.27.2` | .envrc:10 |
| .vscode/settings.json | two `GOTOOLCHAIN` entries | .vscode/settings.json:7,10 |
| go.mod | `go 1.27.0` (minimum; no `toolchain` line) | go.mod:3 |
| tools/{deadcode,mockery,staticcheck,tygo}/go.mod | `go 1.27.0` | tools/*/go.mod:8 |
| Dockerfile | `golang:1.27.2-alpine@sha256:...` | Dockerfile:27 |
| Dockerfile.build-cgo | same image and digest | Dockerfile.build-cgo:23 |
| .github/workflows/ci.yml | `go-version: '1.27.2'` | ci.yml:129,211,277,328,436,531,690,790,876 |
| other workflows | `go-version: '1.27.2'` | security.yml:54, vulnerability-scan.yml:28, frontend-ci.yml:66, binary-smoke.yml:33, ratchet-lower.yml:47, nightly.yml:33, e2e.yml:154, codeql.yml:42 |
| .woodpecker | `GOTOOLCHAIN: go1.27.2` in 8 files; image `golang:1.27.2-bookworm@sha256:...` | test-rest.yaml:51, checks-build.yaml:44, test-native-taglib.yaml:58, checks-lint.yaml:50,52, test-fixtures-{1,2,3}.yaml:61, test-database.yaml:47, test-server-scanner.yaml:44 |
| scripts/ci_remote.py | `GO_TOOLCHAIN = "go1.27.2"` | scripts/ci_remote.py:77 |
| .github/repository-config.yml | `go: ['1.27']` (major.minor for shared reusable-workflow matrix) | .github/repository-config.yml:44 |
| .github/workflow-config.yaml | `'1.27'` | .github/workflow-config.yaml:11,44,52 |
| Drift guard | `scripts/check_toolchain_versions.py` (tiers: exact patch / major.minor / go.mod minimum), run by test-action-integration.yml | scripts/check_toolchain_versions.py:1-50 |

### Node / frontend

Node 26: `.github/repository-config.yml:19,45` (canonical, `versions.node`), every `node-version: '26'` in workflows (ci.yml:130,376; e2e.yml:159; nightly.yml:34; security.yml:78; binary-smoke.yml:38; vulnerability-scan.yml:57), `node:26-alpine@sha256` in both Dockerfiles (not covered by the drift guard). frontend-ci.yml reads the version from `falkcorp/gha-get-frontend-config` (frontend-ci.yml:44). Package versions: see 13-frontend.md.

## 6. GitHub Actions workflows (`.github/workflows`, 24 files)

External reusable workflows come from the org repo `falkcorp/github-common` pinned by SHA (`.github/ghcommon-ref.txt` holds the ref); first-party actions `falkcorp/gha-*`. Third-party actions pinned by SHA.

| Workflow | Trigger | Jobs / purpose |
|---|---|---|
| ci.yml ("Continuous Integration") | push/PR to main,master; dispatch | `changes` (path classify go/web), `ci` (= reusable-ci-minimal: go lint with `--enable-only nolintlint`, super-linter, frontend; `run-go-tests: false`), `interface-width`, `errcheck-ratchet`, `deadcode-ratchet`, `knip-report` (non-blocking), `repo-guards` (sdkguard, bench-check, fmt-check, ops-check, types-check), `mocks-check`, `todo-header-lint`, `go-test-short-shards` (matrix 4/4, `make test-short-shard`), `coverage` (`make coverage-check-short`, merges 4 profiles), `fixture-tests` (matrix 3/3, `make test-fixtures`) | 
| frontend-ci.yml | push/PR on `web/**` to main,develop | `frontend-config`, `frontend` (reusable-ci), `frontend-gate` |
| e2e.yml | PR on web/go paths; nightly 05:17 UTC; dispatch (projects/snapshots inputs) | Playwright chromium on PR, chromium+webkit nightly |
| nightly.yml | cron 04:00 UTC | `full-ci` (reusable-ci full, coverage threshold 30, go-test-timeout 25m, force-all) |
| binary-smoke.yml | push/PR main | build binary, verify CLI help |
| codeql.yml | push/PR, weekly Mon | analyze go, js, actions |
| security.yml | push/PR, weekly Sun | advanced-security, dependencies-go, dependencies-npm |
| vulnerability-scan.yml | daily 06:00 UTC | scan-go (govulncheck on binary), scan-npm |
| changelog-check.yml | PR | `require-fragment` (changelog.d), `scriv-collect-dry-run` |
| memory-leak-scan.yml | weekly, PR/push on web/src | python leak scan |
| prerelease.yml | push to main (paths-ignore md/docs) | `prerelease` (reusable-release, rc), `check-rc-ordinal`; also dispatches release-prod when promoting |
| release-prod.yml | dispatch (release-type auto/major/minor/patch, draft, previous-version) | `release` via reusable-release with go-enabled, frontend-enabled, docker-enabled; `pre-build-script: cd web && npm ci && npm run build` |
| cleanup-rc-releases.yml | release published; dispatch | delete old RCs for a stable tag |
| cleanup-orphan-rc-tags.yml | dispatch | delete orphan RC tags (dry-run default) |
| auto-revert.yml | workflow_run of ci.yml | revert failing main commit (max span 3) |
| auto-revert-backstop.yml | every 15 min | backstop for auto-revert |
| ratchet-lower.yml | weekly Mon | lowers ratchet baselines via `scripts/ci/run_gates.py --lower` and opens PR |
| nightly-burndown.yml / hard-burndown.yml / triage-poll.yml | 08:00+20:00 UTC daily / Sun 10:00 UTC / every 30 min | automation bots (draft-only default mode) |
| todo-collect.yml | daily 07:17 UTC | assembles `todo.d` fragments (`scripts/assemble_todo.py`) |
| stale.yml | weekly | stale issue handling |
| test-action-integration.yml | push/PR on workflow/config/pin paths | validates `gha-get-frontend-config` output and `check_toolchain_versions.py` |

Required checks: UNKNOWN from the tree. `.github/branch-protection.json` has `required_status_checks: null`, `enforce_admins: true`, no required reviews (.github/branch-protection.json:3-5); actual GitHub branch-protection state was not queried. The `frontend-gate` job exists to serve as one stable check name for path-filtered runs (frontend-ci.yml:99). `CODEOWNERS`, `dependabot.yml`, `labeler.yml` exist.

## 7. Woodpecker CI (`.woodpecker/`, 9 files)

All trigger on `pull_request` and `manual`; `labels: heavy: "true"` pins to heavy agents; clone depth 50, no LFS; each ends with a cache-trim step (`scripts/ci/trim_go_cache.py`, 40 GB cap).

| File | Steps |
|---|---|
| checks-build.yaml | `vet` (vet + vet-integration), `fmt-sdkguard-bench` (fmt-check, sdkguard, ops-check, types-check, bench-check, pprof-check), `web-test` (npm ci, vitest) |
| checks-lint.yaml | `tools` (installs golangci-lint v2.14.0), `staticcheck`, `errcheck-ratchet`, `deadcode-ratchet`, `mocks-check` |
| test-database.yaml | `scripts/ci/go_test_shards.py --pkg ./internal/database:8` (8 shards of the database package) |
| test-server-scanner.yaml | `go_test_shards.py` with `./internal/server:8`, `./internal/scanner:8`, `./internal/server/handlers/abs:6` shards |
| test-rest.yaml | `go test $PKGS -short -race -covermode=atomic -timeout 25m` over the remaining packages |
| test-fixtures-1/2/3.yaml | `make test-fixtures FIXTURE_SHARD=i/3 FIXTURE_TIMEOUT=25m` with ffmpeg/ffprobe/fpcalc present |
| test-native-taglib.yaml | builds static TagLib, `go test -tags native_taglib -race ./internal/metadata/... ./internal/tagger/... ./internal/metafetch/...` |

Python helpers for CI: `scripts/ci/*` (short_test_shards.py, fixture_test_packages.py, go_test_shards.py, run_gates.py, build_taglib_static.py, trim_go_cache.py, check_generated_types.sh, apt_install.sh, update_woodpecker_agents.py, weights JSONs). Other CI: `ci/gates.txt` only; there is no `ci/` Dockerfile or script. `make ci-remote` / `scripts/ci_remote.py` push the local tree to remote nodes (not a hosted CI). `super-linter-ci.env` / `super-linter-pr.env` configure super-linter used inside reusable-ci-minimal.

## 8. Release process

| Step | Evidence |
|---|---|
| Version source | git tags; `main.version` injected via `-X main.version=$(VERSION)` | Makefile:76, .goreleaser.yml (ldflags) |
| Prerelease | every push to main (excluding md/docs) builds an RC through `reusable-release.yml` | .github/workflows/prerelease.yml:32-58 |
| Production | manual `release-prod.yml` dispatch (auto/major/minor/patch); Docker enabled here only | .github/workflows/release-prod.yml:43-65 |
| goreleaser | `.goreleaser.yml` v2: `CGO_ENABLED=0`, linux+darwin x amd64+arm64, `tags: [embed_frontend]`, `-s -w -X main.version/commit/date`, tar.gz with LICENSE, README, CHANGELOG, checksums.txt, conventional-commit changelog groups; `make release-dry-run`/`release-snapshot` | .goreleaser.yml:1-60, Makefile:843-854 |
| Note | goreleaser binaries use the WASM tagger (no `native_taglib`); Docker/`build-linux` binaries use native TagLib. Release asset path for the reusable workflow (whether it calls goreleaser or `go build`) is inside `falkcorp/github-common`: UNKNOWN from this repo. |
| Changelog | scriv fragments in `changelog.d/` (CI-enforced), TODO fragments in `todo.d/` | changelog-check.yml:23, todo-collect.yml:29 |
| Deploy | personal `Makefile.local` `deploy` target over ssh + systemd, `.prev` rollback | Makefile.local.example, Makefile:892 |

## 9. `tools/` directory

Separate Go modules (own go.mod, `tool` directive) pin dev binaries so `go install` does not use the main module's graph: `tools/deadcode` (golang.org/x/tools/cmd/deadcode), `tools/mockery` (vektra/mockery v3.8.0 with raised x/tools), `tools/staticcheck` (honnef.co/go/tools), `tools/tygo` (gzuidhof/tygo) (tools/*/go.mod:10).

`tools/cmd/*` live in the MAIN module (import paths `github.com/falkcorp/audiobook-organizer/tools/cmd/...`):

| Cmd | Kind | Purpose |
|---|---|---|
| oplint | CI gate | go/analysis analyzer for ops v3 definitions (analyzer.go + testdata) |
| opsgen | codegen + gate | writes web/src/generated/ops.ts from internal/operations/state tables; `-check` |
| sdkguard | CI gate | asserts pkg/plugin/sdk has no unexpected internal/ deps (internal-deps.txt allowlist); SDK is "a stable public contract backed by type aliases into internal/operations/registry and internal/auth" |
| reconcile-paths | read-only ops CLI | find books whose FilePath is missing and propose locations (CSV) |
| reconcile-book-counts | read-only diagnostic | ListBookIDs vs live counts |
| orphan-nonprimary-census | read-only diagnostic | books with no version group |
| dedup-dataset-audit | read-only analysis | oracle-labeling coverage over dedup candidates |
| itunes-group-preview | dry-run | iTunes XML grouping preview |
| merge-split-books | operator CLI | triggers split-book backfill on a running server |

Other binaries under `cmd/`: root.go (cobra root), child_mode.go, diagnostics.go, fp_worker.go, seed.go, umask_*.go, plus separate mains `itl-audit-encoding`, `itl-check`, `itl-diff`, `mtls-bridge`, `pebble-inject-skip`, `pid-census`, `verify-suite`. `scripts/` holds 110 files (python/shell).

## 10. Evidence relevant to extracting a shared core

1. A public-ish seam already exists: `pkg/plugin/sdk` guarded by sdkguard (tools/cmd/sdkguard/main.go), plus `internal/arch` layering tests registered in ci/gates.txt; both can be extended to guard a new `core` module boundary.
2. `database.Store` width is ratcheted by CI (ci/gates.txt `store-width`, `store-references`), so interface shrinkage is a measurable, enforced goal.
3. Single Go module (go.mod:1) with 4 auxiliary tool modules; there is no go.work (CLAUDE.md forbids it). A core in a second module would need `replace` or a tagged release, and every Go-pin copy in section 5 would need to move together (the drift guard already automates detection).
4. The release/CI backbone (reusable workflows) lives in a separate org repo, so a Sonarr/Radarr sibling can reuse it without copying.
5. Build-tag matrix to preserve in any split: `embed_frontend` (per-app SPA), `native_taglib` (audiobook-specific, CGO), `bench` (dedup-specific), `pprof` (generic, default on), `integration`.
6. `fts5` is dead weight and `make ci` omits frontend lint/e2e: worth noting when defining the core's CI contract.
