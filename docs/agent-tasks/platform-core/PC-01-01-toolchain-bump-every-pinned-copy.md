<!-- file: docs/agent-tasks/platform-core/PC-01-01-toolchain-bump-every-pinned-copy.md -->
<!-- version: 1.0.0 -->
<!-- guid: db67a726-2498-449e-a821-b7e393b656af -->
<!-- last-edited: 2026-11-04 -->

# PC-01-01 — Toolchain bump to `go1.27.3`, every pinned copy together

| Field | Value |
|---|---|
| ID | PC-01-01 |
| Tier | **T0 (Haiku/low)**: a mechanical edit of 34 machine-checked copies, 9 prose copies and 3 image digests, verified by the existing guard script; every command is given |
| Wave | W01 (S0, release `S0`); **the first PR of S0** (09 S0 "Toolchain") |
| Depends on | none |
| Priority / effort | P1 / S |
| Branch | `toolchain/go1.27.3` |
| PR title | `toolchain: go1.27.3` (13 §13.2.2 names it) |
| Trailer on every commit | `Stage-Release: S0 1` |
| Owning spec | `docs/specs/platform-core/13-toolchain.md` §13.2.1–§13.2.4; `09-abo-on-core.md` S0 "Toolchain" and §9.4.1 row 1 |

Do exactly the commands below, in order. Do not edit anything else. If any expected output differs, STOP and report
the command and its output.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-01-01" -b toolchain/go1.27.3 origin/main
cd "$REPO/../audiobook-organizer-pc-01-01"
git worktree list
npm ci --prefix web
```

Never run `go work init` here.

## Exact files (all modify; nothing is created)

`Makefile`, `.envrc`, `.vscode/settings.json`, `Dockerfile`, `Dockerfile.build-cgo`, `.github/workflows/ci.yml`,
`.github/workflows/binary-smoke.yml`, `.github/workflows/codeql.yml`, `.github/workflows/e2e.yml`,
`.github/workflows/frontend-ci.yml`, `.github/workflows/nightly.yml`, `.github/workflows/ratchet-lower.yml`,
`.github/workflows/security.yml`, `.github/workflows/vulnerability-scan.yml`, the nine files
`.woodpecker/checks-build.yaml`, `checks-lint.yaml`, `test-database.yaml`, `test-fixtures-1.yaml`,
`test-fixtures-2.yaml`, `test-fixtures-3.yaml`, `test-native-taglib.yaml`, `test-rest.yaml`,
`test-server-scanner.yaml`, `scripts/ci_remote.py`, `CLAUDE.md`, `agents/go-specialist.md`,
`skills/project-context/SKILL.md`, `Makefile.local.example`, `.github/codeql/README.md`, `.github/dependabot.yml`
(see Planner notes), plus one new `changelog.d/` fragment.

## Step 0: preconditions (13 §13.2.1, §13.2.4)

```bash
curl -sS https://proxy.golang.org/golang.org/toolchain/@v/list | grep -c 'go1\.27\.3\.linux-amd64'   # expect: 1
GOTOOLCHAIN=go1.27.3 go version                                     # expect: go version go1.27.3 linux/amd64
git grep -n -E '1\.27\.2' -- . ':!docs' ':!CHANGELOG.md' | wc -l    # expect: 57
git grep -n -E '1\.27\.3' -- . ':!docs' ':!CHANGELOG.md' | wc -l    # expect: 0
python3 scripts/check_toolchain_versions.py | tail -1               # expect: Toolchain versions consistent
docker buildx version                                               # any version line; if docker is missing, STOP
```

If `go1.27.4` or a `go1.28` release now exists on the proxy, still bump to `go1.27.3` (the spec's target) and say so
in the PR description.

## Step 1: resolve the two new image digests (13 §13.2.2 "Image digests")

```bash
NEW_ALPINE=$(docker buildx imagetools inspect golang:1.27.3-alpine --format '{{.Manifest.Digest}}')
NEW_BOOKWORM=$(docker buildx imagetools inspect golang:1.27.3-bookworm --format '{{.Manifest.Digest}}')
echo "$NEW_ALPINE"; echo "$NEW_BOOKWORM"      # each: sha256: followed by 64 hex characters
TODAY=$(date -u +%F)
```

## Step 2: the edits (one command each; run them all)

```bash
sed -i 's/^export GOTOOLCHAIN := go1\.27\.2$/export GOTOOLCHAIN := go1.27.3/' Makefile
sed -i 's/^export GOTOOLCHAIN=go1\.27\.2$/export GOTOOLCHAIN=go1.27.3/' .envrc
sed -i 's/"GOTOOLCHAIN": "go1\.27\.2"/"GOTOOLCHAIN": "go1.27.3"/' .vscode/settings.json
sed -i "s/go-version: '1\.27\.2'/go-version: '1.27.3'/" .github/workflows/*.yml
sed -i 's/GOTOOLCHAIN: go1\.27\.2$/GOTOOLCHAIN: go1.27.3/' .woodpecker/*.yaml
sed -i "s/golang:1\.27\.2-bookworm@sha256:[0-9a-f]\{64\}/golang:1.27.3-bookworm@$NEW_BOOKWORM/" .woodpecker/checks-lint.yaml
sed -i 's/runs gofmt 1\.27\.2,/runs gofmt 1.27.3,/' .woodpecker/checks-build.yaml
sed -i 's/^GO_TOOLCHAIN = "go1\.27\.2"/GO_TOOLCHAIN = "go1.27.3"/' scripts/ci_remote.py
sed -i "s/^# SHA pinned [0-9-]* (golang:1\.27\.2-alpine manifest-list)\./# SHA pinned $TODAY (golang:1.27.3-alpine manifest-list)./" Dockerfile Dockerfile.build-cgo
sed -i "s/^FROM golang:1\.27\.2-alpine@sha256:[0-9a-f]\{64\} AS go-builder$/FROM golang:1.27.3-alpine@$NEW_ALPINE AS go-builder/" Dockerfile Dockerfile.build-cgo
sed -i 's/every build path pins `go1\.27\.2`/every build path pins `go1.27.3`/' CLAUDE.md
sed -i 's/toolchain pinned `go1\.27\.2` everywhere/toolchain pinned `go1.27.3` everywhere/' agents/go-specialist.md
sed -i 's/toolchain pinned go1\.27\.2 in/toolchain pinned go1.27.3 in/' skills/project-context/SKILL.md
sed -i 's/(`export GOTOOLCHAIN := go1\.27\.2`)/(`export GOTOOLCHAIN := go1.27.3`)/' Makefile.local.example
sed -i 's/GOTOOLCHAIN=go1\.27\.2/GOTOOLCHAIN=go1.27.3/g' .github/codeql/README.md
```

Check:

```bash
git grep -n -E '1\.27\.2' -- . ':!docs' ':!CHANGELOG.md' | wc -l    # expect: 14
git grep -n -E '1\.27\.3' -- . ':!docs' ':!CHANGELOG.md' | wc -l    # expect: 43  (34 guarded + 9 prose)
git grep -n -E '1\.27\.2' -- . ':!docs' ':!CHANGELOG.md' | cut -d: -f1,2
```

The 14 remaining lines must be exactly the "Not a pin (leave alone)" rows of 13 §13.2.2, at these HEAD positions
(the spec cites `Makefile:463,507` at `748fd787a`; four lines were added above them since, so they are now
`:467,511`): `Makefile:467`, `Makefile:511`, `.github/workflows/ci.yml:525`, `.woodpecker/checks-lint.yaml:73`,
`internal/diag/flight/flight.go:628`, `internal/diag/flight/flight_test.go:494`, `internal/pathutil/protected.go:52`,
`scripts/check-errcheck-ratchet.sh:121`, `scripts/check-interface-width.sh:128`, `scripts/setup-mockery.sh:11`,
`internal/audiobooks/audiobook_service_unit_test.go:2`, `internal/config/persistence_test.go:2`,
`scripts/check_toolchain_versions.py:39`, `scripts/tests/test_check_toolchain_versions.py:31`. Any other line: STOP.

## Step 3: the two stale Dependabot rules (13 §13.2.2 "Two stale Dependabot rules must be fixed in the same bump PR")

```bash
sed -n '30,34p' .github/dependabot.yml   # expect exactly: "    ignore:", two comment lines, '      - dependency-name: "golang"', '        versions: [">=1.27"]'
sed -n '80,89p' .github/dependabot.yml   # expect exactly: the six "# Hard-pin the build toolchain. vite 8 ..." comment lines, then the "vite" and "@vitejs/plugin-react" entries with their versions lines
sed -i '80,89d;30,34d' .github/dependabot.yml
grep -c '"golang"\|"vite"\|"@vitejs/plugin-react"' .github/dependabot.yml   # expect: 0
python3 -c "import yaml" 2>/dev/null && python3 -c "import yaml;yaml.safe_load(open('.github/dependabot.yml'))" && echo yaml-ok   # expect: yaml-ok (skip this line if PyYAML is not installed)
```

If either `sed -n` does not print exactly those lines, STOP (the file moved). The `"*"` major-version rule and the
`jsdom` pin stay.

## Step 4: headers

Bump the patch number of the `version:` header and set `last-edited:` to today in every changed file that has a header
(`.vscode/settings.json` has none; do not add one). Save this one-off helper outside the repository and run it:

```bash
cat > /tmp/bump_headers.py <<'EOF'
import re, sys, datetime
today = datetime.date.today().isoformat()
for path in sys.argv[1:]:
    lines = open(path).read().split('\n'); done = False
    for i in range(min(8, len(lines))):
        m = re.search(r'version: (\d+)\.(\d+)\.(\d+)', lines[i])
        if m and not done:
            lines[i] = lines[i][:m.start()] + 'version: %s.%s.%d' % (m.group(1), m.group(2), int(m.group(3)) + 1) + lines[i][m.end():]
            done = True
        lines[i] = re.sub(r'last-edited: \d{4}-\d{2}-\d{2}', 'last-edited: ' + today, lines[i])
    open(path, 'w').write('\n'.join(lines)); print(('bumped ' if done else 'NO HEADER ') + path)
EOF
python3 /tmp/bump_headers.py $(git diff --name-only | grep -v '^.vscode/settings.json$')
```

Expected: 29 `bumped <path>` lines and no `NO HEADER` line.

## Step 5: changelog fragment

Create `changelog.d/<TODAY>_toolchain_go1.27.3.md` (no header):

```markdown
### Changed

#### Go toolchain go1.27.2 → go1.27.3, every pinned copy together

The first PR of platform-core stage S0 (13 §13.2.2): the 34 copies the toolchain guard checks, the 9 prose copies
and the golang alpine and bookworm image digests move together. Two stale Dependabot rules are removed: the
`golang >=1.27` docker ignore ("Go 1.27 does not compile this repository" stopped being true on 2026-09-01) and the
`vite >=8` / `@vitejs/plugin-react >=5` pins written for a React 18 app (the app is on React 19 and Vite 8), which
also blocked security updates.
```

## Acceptance (commands and expected output)

```bash
python3 scripts/check_toolchain_versions.py | tail -1                    # Toolchain versions consistent
python3 -m unittest discover -s scripts -p 'test_check_toolchain_versions.py' 2>&1 | tail -1   # OK
git grep -n -E '1\.27\.2' -- . ':!docs' ':!CHANGELOG.md' | wc -l          # 14 (only 13 §13.2.2's not-a-pin rows)
grep -n '^export GOTOOLCHAIN := ' Makefile                                 # 43:export GOTOOLCHAIN := go1.27.3
GOTOOLCHAIN=go1.27.3 go build ./... && GOTOOLCHAIN=go1.27.3 go vet ./...  # exit 0
GOTOOLCHAIN=go1.27.3 go test -race -short -count=1 -timeout 25m ./...     # exit 0 (13 §13.2.4: the -race run is a precondition of this PR)
docker build -f Dockerfile -t abo-toolchain-check .                      # exit 0 (the new alpine digest resolves)
make ci                                                                 # last line: ✅ All CI checks passed!
```

Record in the PR description: the two digests, the three command outcomes of Step 0, and that `make ci` passed
(13 §13.2.3 asks the PR to prove mockery, staticcheck and golangci-lint still read go1.27.3 export data).

## Out of scope

- `go.mod`'s `go 1.27.0` directive, `tools/*/go.mod`, `.github/repository-config.yml`, `.github/workflow-config.yaml`
  (`'1.27'`, unchanged by a patch bump, 13 §13.2.2).
- The guard changes for `core/` (13 §13.2.2 items 1–4): PC-01-02.
- `.standards/` (a submodule; not edited here).

## Rollback

Revert the PR (09 §9.4.1 row 1: revert PR). No runtime behaviour changes.

## Repo rules

- Conventional commit `chore(toolchain): go1.27.3, every pinned copy together`, ending:
  ```
  Stage-Release: S0 1
  Co-Authored-By: Claude <noreply@anthropic.com>
  ```
- Push with `git push` (CLAUDE.md: never push workflow changes through the MCP contents API).
- `gh pr create --title "toolchain: go1.27.3"`, wait for every check, `gh pr merge <n> --rebase`; never squash.
- Remove the worktree after merge.

## Planner notes

- The inventory row omits `.github/dependabot.yml`, but 13 §13.2.2 requires the two stale Dependabot rules to be
  fixed "in the same bump PR". It is added here. PC-01-02 and PC-01-06 edit the same file later in the W01 chain, so
  the serial order already covers the collision.
- 13 §13.2.2 anchors two not-a-pin lines at `Makefile:463,507` (@748fd787a); at `ac4368bdb` they are `Makefile:467,511`
  (a 4-line comment was added above them on 2026-11-03). The count of 14 is unchanged.
