<!-- file: docs/plans/2026-07-29-abs-sync-phase0-oracle.md -->
<!-- version: 1.0.0 -->
<!-- guid: ec50565c-7a14-46a8-a6a9-49376e63f89f -->
<!-- last-edited: 2026-07-29 -->

# ABS Sync Phase 0 — Reference Oracle & Conformance Harness — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Stand up a real Audiobookshelf 2.36.x server in Docker as a reference oracle, capture golden request/response fixtures from it, and build a field-presence-and-type-aware conformance differ that will gate every later ABS phase.

**Architecture:** A pinned ABS Docker container scans a small sample library built from the repo's existing LibriVox testdata (the Odyssey, present both as a 6-file mp3 set and as a 115 MB m4b with 6 real embedded chapters). A Python capture script authenticates against that server, walks every endpoint in the ABS surface, and writes normalized JSON fixtures to `testdata/abs-fixtures/`. A Go package `internal/absync/conformance` provides (a) a normalizer that canonicalizes volatile values while **preserving their JSON types**, and (b) a differ that reports missing fields, type mismatches, extra fields, and array-shape problems by JSON path. No ABS endpoints are implemented in this phase — this phase builds the measuring instrument.

**Tech Stack:** Go 1.26 (stdlib `encoding/json` only for the differ — no new Go deps), Python 3 + `requests` for capture, Docker Compose, `ghcr.io/advplyr/audiobookshelf` pinned to 2.36.x, ffprobe (already installed) for asset verification.

## Global Constraints

- **File version headers are MANDATORY on every file created or modified.** Go files use `// file:`, `// version:`, `// guid:`, `// last-edited:` before `package`. All other file types use the `<!-- ... -->` form. Bump `version:` and `last-edited:` on every change. Generate GUIDs with `uuidgen | tr '[:upper:]' '[:lower:]'`.
- **Conventional commits**: `type(scope): description`. Types: feat, fix, refactor, test, docs, chore, perf, ci.
- **Never commit to main.** All work happens in the worktree at `/Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-abs-sync` on branch `feat/abs-sync-spec` (or a child branch). PR-based merges, rebase/FF only.
- **Go module path** is `github.com/falkcorp/audiobook-organizer` (NOT `jdfalk/`). Import accordingly.
- **Add a changelog fragment** under `changelog.d/` for the phase (CI check `changelog-check.yml` requires one per PR). Do **not** hand-edit `CHANGELOG.md`.
- **Do not add Go dependencies in this phase.** The differ and normalizer use only the standard library.
- **The arranged oracle library and captured large binaries must be gitignored** — never commit media. Fixtures are JSON only.
- **`testdata/audio/librivox/**` is existing committed testdata — read it, never modify or move it.**
- **The iTunes tree (`books/itunes/**`) is hands-off** and is irrelevant to this phase; do not touch it.
- Spec this plan implements: `docs/specs/2026-07-29-abs-sync-api-design.md` §6 (oracle & fixtures) and §7 Phase 0.

## File Structure

| Path | Responsibility |
|---|---|
| `testdata/abs-oracle/docker-compose.yml` | Pinned ABS oracle container + volumes |
| `testdata/abs-oracle/README.md` | How to start/stop the oracle, credentials, port |
| `testdata/abs-oracle/build-library.sh` | Arranges existing LibriVox testdata into an ABS-shaped library (gitignored output) |
| `internal/absync/conformance/jsontype.go` | JSON type classification (one tiny responsibility) |
| `internal/absync/conformance/diff.go` | Structural differ: missing/extra/type/length findings by path |
| `internal/absync/conformance/diff_test.go` | Tests for the differ |
| `internal/absync/conformance/normalize.go` | Volatile-value canonicalizer that preserves JSON type |
| `internal/absync/conformance/normalize_test.go` | Tests for the normalizer |
| `internal/absync/conformance/fixture.go` | Loads a captured fixture file into `want` for the differ |
| `internal/absync/conformance/fixture_test.go` | Tests fixture loading against a committed sample fixture |
| `scripts/abs_capture_fixtures.py` | Captures golden fixtures from the running oracle |
| `testdata/abs-fixtures/*.json` | Committed golden fixtures (JSON only, normalized) |
| `docs/reference/abs-client-network-audit.md` | Findings: per-client custom-header support incl. the streaming path |

---

### Task 1: ABS oracle Docker stack + sample library

**Files:**
- Create: `testdata/abs-oracle/docker-compose.yml`
- Create: `testdata/abs-oracle/build-library.sh`
- Create: `testdata/abs-oracle/README.md`
- Modify: `.gitignore` (append the oracle's derived directories)

**Interfaces:**
- Consumes: existing `testdata/audio/librivox/odyssey_butler_librivox/` (read-only).
- Produces: an ABS server reachable at `http://localhost:13378` with a scanned library; `testdata/abs-oracle/library/` (gitignored) laid out as `Author/Title/files`.

- [ ] **Step 1: Verify the source assets exist and have the properties this phase depends on**

Run:
```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-abs-sync
ls testdata/audio/librivox/odyssey_butler_librivox/
ffprobe -v error -show_chapters -print_format json \
  testdata/audio/librivox/odyssey_butler_librivox/odyssey_complete.m4b | head -20
```
Expected: six `odyssey_0N_homer_butler_64kb.mp3` files plus `odyssey_complete.m4b`; the ffprobe output lists a non-empty `chapters` array whose first entry has `start_time` `"0.000000"` and a `tags.title`. If either is missing, STOP and report — the rest of this plan assumes both.

- [ ] **Step 2: Write the library build script**

Create `testdata/abs-oracle/build-library.sh`:
```bash
#!/usr/bin/env bash
# file: testdata/abs-oracle/build-library.sh
# version: 1.0.0
# guid: 3f1c9e2a-8b47-4d1e-9c62-7a5f0d3b8e14
# last-edited: 2026-07-29
#
# Arranges the repo's existing LibriVox testdata into an Audiobookshelf-shaped
# library for the reference oracle. Output is gitignored derived data.
#
# ABS expects: <library root>/<Author>/<Title>/<audio files>
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_ROOT="$(cd "${SCRIPT_DIR}/../.." && pwd)"
SRC="${REPO_ROOT}/testdata/audio/librivox/odyssey_butler_librivox"
DEST="${SCRIPT_DIR}/library"

if [[ ! -d "${SRC}" ]]; then
  echo "ERROR: source testdata not found: ${SRC}" >&2
  exit 1
fi

# Multi-file book: exercises the cumulative startOffset timeline.
MULTI="${DEST}/Homer/The Odyssey"
# Single-file m4b: exercises embedded-chapter extraction and Range seeking.
SINGLE="${DEST}/Homer/The Odyssey (Single File)"

rm -rf "${DEST}"
mkdir -p "${MULTI}" "${SINGLE}"

for f in "${SRC}"/odyssey_0*_homer_butler_64kb.mp3; do
  cp "${f}" "${MULTI}/"
done
cp "${SRC}/odyssey_complete.m4b" "${SINGLE}/"

echo "Built oracle library at: ${DEST}"
find "${DEST}" -type f | sed "s|${DEST}/|  |"
```

- [ ] **Step 3: Make it executable and run it**

Run:
```bash
chmod +x testdata/abs-oracle/build-library.sh
./testdata/abs-oracle/build-library.sh
```
Expected: prints `Built oracle library at: .../testdata/abs-oracle/library` followed by 7 files (6 mp3 under `Homer/The Odyssey`, 1 m4b under `Homer/The Odyssey (Single File)`).

- [ ] **Step 4: Write the oracle compose file**

Create `testdata/abs-oracle/docker-compose.yml`:
```yaml
# file: testdata/abs-oracle/docker-compose.yml
# version: 1.0.0
# guid: 9d4b7f01-2c6a-4e38-b5d9-1f8a06c47e3b
# last-edited: 2026-07-29
#
# Reference-oracle Audiobookshelf server. This is the ONLY trustworthy spec for
# the ABS API (the published docs at api.audiobookshelf.org are stale), so the
# version is pinned deliberately -- do not float this tag.

services:
  abs-oracle:
    image: ghcr.io/advplyr/audiobookshelf:2.36.0
    container_name: abs-oracle
    ports:
      - "13378:80"
    volumes:
      - ./library:/audiobooks:ro
      - abs-oracle-config:/config
      - abs-oracle-metadata:/metadata
    environment:
      - TZ=UTC
    restart: "no"

volumes:
  abs-oracle-config:
  abs-oracle-metadata:
```

- [ ] **Step 5: Gitignore the derived library**

Append to `.gitignore`:
```
# ABS reference-oracle derived data (media must never be committed)
testdata/abs-oracle/library/
```

- [ ] **Step 6: Start the oracle and confirm it answers**

Run:
```bash
cd testdata/abs-oracle && docker compose up -d && cd -
sleep 15
curl -fsS http://localhost:13378/ping
curl -fsS http://localhost:13378/status
```
Expected: `/ping` returns JSON containing `success`; `/status` returns JSON containing `isInit` and `serverVersion`. Record the exact `serverVersion` string — the spec requires we only report a version whose features we implement.

If `/status` shows `"isInit": false`, complete first-run setup in a browser at `http://localhost:13378` (create user `oracle` / password `oracle-dev-only`), add a library named `Books` pointing at `/audiobooks`, and wait for the scan to finish.

- [ ] **Step 7: Write the README**

Create `testdata/abs-oracle/README.md`:
```markdown
<!-- file: testdata/abs-oracle/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6a2e8c5b-4f19-4d73-8b1c-90e7f24a3d68 -->
<!-- last-edited: 2026-07-29 -->

# Audiobookshelf Reference Oracle

A pinned, real Audiobookshelf server used as the **only trustworthy spec** for the
ABS API. The published docs at api.audiobookshelf.org are stale and unmaintained;
golden fixtures are captured from this container instead.

## Why pinned

We target ABS **2.36.x**. Clients gate behavior on `serverVersion`, so the image tag
is pinned deliberately. Do not float it — a version bump invalidates every fixture.

## Start / stop

```bash
./build-library.sh          # arrange testdata into an ABS-shaped library (once)
docker compose up -d        # start on http://localhost:13378
docker compose down         # stop (keeps config/metadata volumes)
docker compose down -v      # stop and DESTROY config (forces first-run setup again)
```

## First-run setup (once per fresh volume)

1. Open <http://localhost:13378>.
2. Create the root user: `oracle` / `oracle-dev-only` (dev-only credentials; this
   server is never exposed beyond localhost).
3. Add a library named `Books` with folder `/audiobooks`.
4. Wait for the scan to complete (two books).

## The sample library

Built by `build-library.sh` from committed LibriVox testdata:

| Book | Shape | Exercises |
|---|---|---|
| `Homer/The Odyssey` | 6 × mp3 | multi-file cumulative `startOffset` timeline |
| `Homer/The Odyssey (Single File)` | 1 × m4b, 115 MB, 6 embedded chapters | chapter extraction, Range seeking on a large file |

`library/` is gitignored derived data — media is never committed.
```

- [ ] **Step 8: Commit**

```bash
git add testdata/abs-oracle/docker-compose.yml testdata/abs-oracle/build-library.sh \
        testdata/abs-oracle/README.md .gitignore
git commit -m "test(abs-sync): add pinned Audiobookshelf reference oracle + sample library"
```

---

### Task 2: JSON type classification

**Files:**
- Create: `internal/absync/conformance/jsontype.go`
- Test: `internal/absync/conformance/jsontype_test.go`

**Interfaces:**
- Produces: `func JSONType(v any) string` returning exactly one of `"object"`, `"array"`, `"string"`, `"number"`, `"bool"`, `"null"`. Used by Task 3's differ and Task 4's normalizer.

Rationale: `encoding/json` unmarshals into `map[string]any`, `[]any`, `string`, `float64`, `bool`, `nil`. Conformance cares about *type* as much as value, so this classification is its own tiny, well-tested unit.

- [ ] **Step 1: Write the failing test**

Create `internal/absync/conformance/jsontype_test.go`:
```go
// file: internal/absync/conformance/jsontype_test.go
// version: 1.0.0
// guid: c7e41a90-5b62-4d8f-9a13-e806b2f5d74c
// last-edited: 2026-07-29

package conformance

import (
	"encoding/json"
	"testing"
)

func TestJSONType(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{"object", `{"a":1}`, "object"},
		{"array", `[1,2]`, "array"},
		{"string", `"hi"`, "string"},
		{"number", `3.5`, "number"},
		{"integer is still number", `7`, "number"},
		{"bool", `true`, "bool"},
		{"null", `null`, "null"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var v any
			if err := json.Unmarshal([]byte(tc.raw), &v); err != nil {
				t.Fatalf("unmarshal %s: %v", tc.raw, err)
			}
			if got := JSONType(v); got != tc.want {
				t.Errorf("JSONType(%s) = %q, want %q", tc.raw, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./internal/absync/conformance/ -run TestJSONType -v`
Expected: FAIL — build error, `undefined: JSONType`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/absync/conformance/jsontype.go`:
```go
// file: internal/absync/conformance/jsontype.go
// version: 1.0.0
// guid: 1b8f6d3c-90a5-4e27-b6c8-4f21e90d7a35
// last-edited: 2026-07-29

// Package conformance diffs this server's Audiobookshelf-compatible API
// responses against golden fixtures captured from a real Audiobookshelf
// server. It checks field presence and type, not just values, because ABS
// clients hard-require specific fields and fail opaquely when they are absent
// or the wrong shape.
package conformance

// JSONType classifies a value produced by encoding/json into a stable type
// name. Conformance compares types as well as values, so this is the shared
// vocabulary used by both the differ and the normalizer.
func JSONType(v any) string {
	switch v.(type) {
	case map[string]any:
		return "object"
	case []any:
		return "array"
	case string:
		return "string"
	case float64:
		return "number"
	case bool:
		return "bool"
	case nil:
		return "null"
	default:
		return "unknown"
	}
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./internal/absync/conformance/ -run TestJSONType -v`
Expected: PASS (all 7 subtests).

- [ ] **Step 5: Commit**

```bash
git add internal/absync/conformance/jsontype.go internal/absync/conformance/jsontype_test.go
git commit -m "test(abs-sync): add JSON type classification for conformance diffing"
```

---

### Task 3: Structural conformance differ

**Files:**
- Create: `internal/absync/conformance/diff.go`
- Test: `internal/absync/conformance/diff_test.go`

**Interfaces:**
- Consumes: `JSONType(v any) string` from Task 2.
- Produces:
  - `type FindingKind string` with constants `KindMissingField`, `KindExtraField`, `KindTypeMismatch`, `KindLengthMismatch`, `KindValueMismatch`.
  - `type Finding struct { Path string; Kind FindingKind; Want string; Got string }`
  - `type Options struct { CompareValues bool; IgnoreExtra bool }`
  - `func Compare(want, got any, opts Options) []Finding`
  - `func (f Finding) String() string`

Design notes the implementer must honor:
- **Presence and type are the primary signal.** Values are normalized (Task 4) before comparison, so `Options.CompareValues` defaults to false.
- A **missing** field in `got` that exists in `want` is the highest-severity finding — that is precisely what makes an ABS client fail.
- An **extra** field in `got` is reported but is usually benign (we may return more than ABS does); `IgnoreExtra` suppresses it.
- Arrays are compared **element-wise up to the shorter length**, plus a length finding. A `want` array that is non-empty while `got` is empty is a real failure and must surface.
- Paths use dotted keys and bracketed indices: `media.metadata.title`, `libraries[0].id`.

- [ ] **Step 1: Write the failing test**

Create `internal/absync/conformance/diff_test.go`:
```go
// file: internal/absync/conformance/diff_test.go
// version: 1.0.0
// guid: 8e35b1c7-6a24-4f90-bd18-2c7e504a9f63
// last-edited: 2026-07-29

package conformance

import (
	"encoding/json"
	"testing"
)

// mustJSON unmarshals a JSON literal for use in table-driven tests.
func mustJSON(t *testing.T, raw string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("unmarshal %s: %v", raw, err)
	}
	return v
}

// findingAt returns the first finding at path with the given kind, or nil.
func findingAt(fs []Finding, path string, kind FindingKind) *Finding {
	for i := range fs {
		if fs[i].Path == path && fs[i].Kind == kind {
			return &fs[i]
		}
	}
	return nil
}

func TestCompareDetectsMissingField(t *testing.T) {
	want := mustJSON(t, `{"id":"x","title":"T"}`)
	got := mustJSON(t, `{"id":"x"}`)

	fs := Compare(want, got, Options{})

	if findingAt(fs, "title", KindMissingField) == nil {
		t.Fatalf("expected missing_field at %q, got %v", "title", fs)
	}
}

func TestCompareDetectsMissingNestedField(t *testing.T) {
	want := mustJSON(t, `{"media":{"metadata":{"title":"T","narrator":"N"}}}`)
	got := mustJSON(t, `{"media":{"metadata":{"title":"T"}}}`)

	fs := Compare(want, got, Options{})

	if findingAt(fs, "media.metadata.narrator", KindMissingField) == nil {
		t.Fatalf("expected missing_field at media.metadata.narrator, got %v", fs)
	}
}

func TestCompareDetectsTypeMismatch(t *testing.T) {
	// ABS returns duration as a number; a string would break clients.
	want := mustJSON(t, `{"duration":123.5}`)
	got := mustJSON(t, `{"duration":"123.5"}`)

	fs := Compare(want, got, Options{})

	f := findingAt(fs, "duration", KindTypeMismatch)
	if f == nil {
		t.Fatalf("expected type_mismatch at duration, got %v", fs)
	}
	if f.Want != "number" || f.Got != "string" {
		t.Errorf("want number/string, got %s/%s", f.Want, f.Got)
	}
}

func TestCompareReportsExtraFieldAndCanIgnoreIt(t *testing.T) {
	want := mustJSON(t, `{"id":"x"}`)
	got := mustJSON(t, `{"id":"x","ourExtra":1}`)

	if findingAt(Compare(want, got, Options{}), "ourExtra", KindExtraField) == nil {
		t.Errorf("expected extra_field to be reported by default")
	}
	if findingAt(Compare(want, got, Options{IgnoreExtra: true}), "ourExtra", KindExtraField) != nil {
		t.Errorf("expected extra_field to be suppressed by IgnoreExtra")
	}
}

func TestCompareChecksArrayElementShape(t *testing.T) {
	want := mustJSON(t, `{"tracks":[{"index":1,"startOffset":0.0}]}`)
	got := mustJSON(t, `{"tracks":[{"index":1}]}`)

	fs := Compare(want, got, Options{})

	if findingAt(fs, "tracks[0].startOffset", KindMissingField) == nil {
		t.Fatalf("expected missing_field at tracks[0].startOffset, got %v", fs)
	}
}

func TestCompareFlagsEmptyArrayWhenFixtureHasElements(t *testing.T) {
	// A client that expects chapters and receives none is a real failure.
	want := mustJSON(t, `{"chapters":[{"start":0.0}]}`)
	got := mustJSON(t, `{"chapters":[]}`)

	fs := Compare(want, got, Options{})

	f := findingAt(fs, "chapters", KindLengthMismatch)
	if f == nil {
		t.Fatalf("expected length_mismatch at chapters, got %v", fs)
	}
	if f.Want != "1" || f.Got != "0" {
		t.Errorf("want 1/0, got %s/%s", f.Want, f.Got)
	}
}

func TestCompareIgnoresValuesByDefaultAndComparesWhenAsked(t *testing.T) {
	want := mustJSON(t, `{"title":"Odyssey"}`)
	got := mustJSON(t, `{"title":"Iliad"}`)

	if fs := Compare(want, got, Options{}); len(fs) != 0 {
		t.Errorf("expected no findings when CompareValues is false, got %v", fs)
	}
	if findingAt(Compare(want, got, Options{CompareValues: true}), "title", KindValueMismatch) == nil {
		t.Errorf("expected value_mismatch when CompareValues is true")
	}
}

func TestCompareCleanWhenIdentical(t *testing.T) {
	want := mustJSON(t, `{"a":1,"b":{"c":[true,null]}}`)
	got := mustJSON(t, `{"a":1,"b":{"c":[true,null]}}`)

	if fs := Compare(want, got, Options{CompareValues: true}); len(fs) != 0 {
		t.Errorf("expected zero findings for identical documents, got %v", fs)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/absync/conformance/ -run TestCompare -v`
Expected: FAIL — build error, `undefined: Compare`, `undefined: Options`, `undefined: Finding`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/absync/conformance/diff.go`:
```go
// file: internal/absync/conformance/diff.go
// version: 1.0.0
// guid: 4c9a7e18-3b56-42df-9017-8ea6b3createme
// last-edited: 2026-07-29

package conformance

import (
	"fmt"
	"reflect"
	"strconv"
)

// FindingKind classifies a single conformance defect.
type FindingKind string

const (
	// KindMissingField is the highest-severity finding: the fixture captured
	// from real ABS has a field our response omits. Clients hard-require
	// specific fields and fail opaquely when they are absent.
	KindMissingField FindingKind = "missing_field"
	// KindExtraField means we return a field ABS does not. Usually benign.
	KindExtraField FindingKind = "extra_field"
	// KindTypeMismatch means the field exists but has the wrong JSON type.
	KindTypeMismatch FindingKind = "type_mismatch"
	// KindLengthMismatch means two arrays differ in length.
	KindLengthMismatch FindingKind = "length_mismatch"
	// KindValueMismatch is only produced when Options.CompareValues is set.
	KindValueMismatch FindingKind = "value_mismatch"
)

// Finding is one conformance defect located by JSON path.
type Finding struct {
	Path string
	Kind FindingKind
	Want string
	Got  string
}

func (f Finding) String() string {
	if f.Path == "" {
		return fmt.Sprintf("%s: want %s, got %s", f.Kind, f.Want, f.Got)
	}
	return fmt.Sprintf("%s at %s: want %s, got %s", f.Kind, f.Path, f.Want, f.Got)
}

// Options tunes comparison strictness.
type Options struct {
	// CompareValues also compares scalar values. Off by default because
	// fixtures are normalized (volatile values are canonicalized), so
	// presence and type are the meaningful signal.
	CompareValues bool
	// IgnoreExtra suppresses KindExtraField findings.
	IgnoreExtra bool
}

// Compare walks want (the ABS fixture) against got (our response) and returns
// every conformance defect found. A nil/empty result means conformant.
func Compare(want, got any, opts Options) []Finding {
	var out []Finding
	compareValue("", want, got, opts, &out)
	return out
}

func compareValue(path string, want, got any, opts Options, out *[]Finding) {
	wt, gt := JSONType(want), JSONType(got)
	if wt != gt {
		*out = append(*out, Finding{Path: path, Kind: KindTypeMismatch, Want: wt, Got: gt})
		return
	}

	switch wt {
	case "object":
		compareObject(path, want.(map[string]any), got.(map[string]any), opts, out)
	case "array":
		compareArray(path, want.([]any), got.([]any), opts, out)
	default:
		if opts.CompareValues && !reflect.DeepEqual(want, got) {
			*out = append(*out, Finding{
				Path: path, Kind: KindValueMismatch,
				Want: fmt.Sprintf("%v", want), Got: fmt.Sprintf("%v", got),
			})
		}
	}
}

func compareObject(path string, want, got map[string]any, opts Options, out *[]Finding) {
	for k, wv := range want {
		child := joinPath(path, k)
		gv, ok := got[k]
		if !ok {
			*out = append(*out, Finding{
				Path: child, Kind: KindMissingField, Want: JSONType(wv), Got: "absent",
			})
			continue
		}
		compareValue(child, wv, gv, opts, out)
	}
	if opts.IgnoreExtra {
		return
	}
	for k, gv := range got {
		if _, ok := want[k]; !ok {
			*out = append(*out, Finding{
				Path: joinPath(path, k), Kind: KindExtraField, Want: "absent", Got: JSONType(gv),
			})
		}
	}
}

func compareArray(path string, want, got []any, opts Options, out *[]Finding) {
	if len(want) != len(got) {
		*out = append(*out, Finding{
			Path: path, Kind: KindLengthMismatch,
			Want: strconv.Itoa(len(want)), Got: strconv.Itoa(len(got)),
		})
	}
	n := len(want)
	if len(got) < n {
		n = len(got)
	}
	for i := 0; i < n; i++ {
		compareValue(fmt.Sprintf("%s[%d]", path, i), want[i], got[i], opts, out)
	}
}

func joinPath(parent, key string) string {
	if parent == "" {
		return key
	}
	return parent + "." + key
}
```

**Note to implementer:** the `guid:` line above contains the placeholder `8ea6b3createme`. Replace the whole GUID with a fresh one from `uuidgen | tr '[:upper:]' '[:lower:]'` before committing.

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/absync/conformance/ -v`
Expected: PASS — all `TestCompare*` and `TestJSONType` subtests green.

- [ ] **Step 5: Run vet and the race detector**

Run:
```bash
go vet ./internal/absync/conformance/
go test ./internal/absync/conformance/ -race
```
Expected: no vet output; tests PASS.

- [ ] **Step 6: Commit**

```bash
git add internal/absync/conformance/diff.go internal/absync/conformance/diff_test.go
git commit -m "test(abs-sync): add field-presence and type-aware conformance differ"
```

---

### Task 4: Volatile-value normalizer

**Files:**
- Create: `internal/absync/conformance/normalize.go`
- Test: `internal/absync/conformance/normalize_test.go`

**Interfaces:**
- Consumes: `JSONType(v any) string` from Task 2.
- Produces:
  - `func DefaultVolatileKeys() map[string]bool`
  - `type Normalizer struct { Volatile map[string]bool }`
  - `func NewNormalizer() *Normalizer`
  - `func (n *Normalizer) Normalize(v any) any`

Design notes the implementer must honor:
- **Type must be preserved.** Normalizing must canonicalize the *value* while keeping its JSON type, because Task 3's differ compares types. A string becomes `"<volatile>"`, a number becomes `0`, a bool becomes `false`, `null` stays `null`. Replacing everything with a single sentinel string would destroy the type signal and make the differ useless.
- Normalization is **recursive** and applies inside arrays and nested objects.
- Keys are matched **case-insensitively** — ABS is inconsistent (`libraryId` vs `libraryID` appear across endpoints).
- `Normalize` must **not mutate its input**; it returns a new value. Fixtures are loaded once and compared many times.

- [ ] **Step 1: Write the failing test**

Create `internal/absync/conformance/normalize_test.go`:
```go
// file: internal/absync/conformance/normalize_test.go
// version: 1.0.0
// guid: 5d0b8f31-7c92-4a6e-b418-3fa95e27c60d
// last-edited: 2026-07-29

package conformance

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNormalizePreservesTypeWhileCanonicalizingValue(t *testing.T) {
	n := NewNormalizer()
	in := mustJSON(t, `{"id":"abc123","addedAt":1720000000000,"title":"Odyssey"}`)

	got := n.Normalize(in).(map[string]any)

	// Volatile string keeps string type.
	if JSONType(got["id"]) != "string" {
		t.Errorf("id should stay a string, got %s", JSONType(got["id"]))
	}
	if got["id"] == "abc123" {
		t.Errorf("id should have been canonicalized")
	}
	// Volatile number keeps number type.
	if JSONType(got["addedAt"]) != "number" {
		t.Errorf("addedAt should stay a number, got %s", JSONType(got["addedAt"]))
	}
	if got["addedAt"] != float64(0) {
		t.Errorf("addedAt should canonicalize to 0, got %v", got["addedAt"])
	}
	// Non-volatile values are untouched.
	if got["title"] != "Odyssey" {
		t.Errorf("title should be untouched, got %v", got["title"])
	}
}

func TestNormalizeRecursesIntoNestedObjectsAndArrays(t *testing.T) {
	n := NewNormalizer()
	in := mustJSON(t, `{"libraries":[{"id":"L1","name":"Books"},{"id":"L2","name":"Pods"}]}`)

	got := n.Normalize(in).(map[string]any)
	libs := got["libraries"].([]any)

	for i, raw := range libs {
		lib := raw.(map[string]any)
		if lib["id"] == "L1" || lib["id"] == "L2" {
			t.Errorf("libraries[%d].id should have been canonicalized, got %v", i, lib["id"])
		}
		if JSONType(lib["id"]) != "string" {
			t.Errorf("libraries[%d].id should stay a string", i)
		}
	}
	if libs[0].(map[string]any)["name"] != "Books" {
		t.Errorf("name should be untouched")
	}
}

func TestNormalizeMatchesKeysCaseInsensitively(t *testing.T) {
	// ABS is inconsistent about libraryId vs libraryID across endpoints.
	n := NewNormalizer()
	in := mustJSON(t, `{"libraryID":"X","LibraryId":"Y"}`)

	got := n.Normalize(in).(map[string]any)

	if got["libraryID"] == "X" {
		t.Errorf("libraryID should have been canonicalized")
	}
	if got["LibraryId"] == "Y" {
		t.Errorf("LibraryId should have been canonicalized")
	}
}

func TestNormalizeDoesNotMutateInput(t *testing.T) {
	n := NewNormalizer()
	raw := `{"id":"keepme","nested":{"token":"secret"}}`
	in := mustJSON(t, raw)

	_ = n.Normalize(in)

	original := mustJSON(t, raw)
	if !reflect.DeepEqual(in, original) {
		t.Errorf("Normalize mutated its input: %v", in)
	}
}

func TestNormalizedFixturesCompareClean(t *testing.T) {
	// Two responses differing ONLY in volatile fields must be conformant.
	n := NewNormalizer()
	a := mustJSON(t, `{"id":"aaa","updatedAt":1,"title":"T","tracks":[{"ino":"11","index":1}]}`)
	b := mustJSON(t, `{"id":"bbb","updatedAt":2,"title":"T","tracks":[{"ino":"22","index":1}]}`)

	fs := Compare(n.Normalize(a), n.Normalize(b), Options{CompareValues: true})

	if len(fs) != 0 {
		t.Errorf("normalized documents should compare clean, got %v", fs)
	}
}

func TestDefaultVolatileKeysCoverTheObviousOnes(t *testing.T) {
	keys := DefaultVolatileKeys()
	for _, k := range []string{"id", "token", "refreshtoken", "createdat", "updatedat", "addedat", "ino"} {
		if !keys[k] {
			t.Errorf("expected %q in DefaultVolatileKeys (lowercased)", k)
		}
	}
	// currentTime is MEANINGFUL progress data, never volatile.
	if keys["currenttime"] {
		t.Errorf("currentTime must not be treated as volatile -- it is real progress data")
	}
	_ = json.Marshal
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/absync/conformance/ -run 'TestNormalize|TestDefaultVolatile' -v`
Expected: FAIL — build error, `undefined: NewNormalizer`, `undefined: DefaultVolatileKeys`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/absync/conformance/normalize.go`:
```go
// file: internal/absync/conformance/normalize.go
// version: 1.0.0
// guid: 7f26a4d9-1e83-4b05-9c7a-62d0fb495e18
// last-edited: 2026-07-29

package conformance

import "strings"

// volatilePlaceholder replaces volatile string values. Values are canonicalized
// rather than removed so that JSON *type* survives normalization -- the differ
// compares types, so erasing them would defeat the whole harness.
const volatilePlaceholder = "<volatile>"

// DefaultVolatileKeys returns the lowercased field names whose values differ
// between two runs of the same request and therefore carry no conformance
// signal: identifiers, timestamps, inodes, and secrets.
//
// Deliberately NOT volatile: currentTime, duration, progress, startOffset,
// isFinished -- these are real playback/progress data whose values matter.
func DefaultVolatileKeys() map[string]bool {
	keys := []string{
		// identifiers
		"id", "libraryid", "libraryitemid", "folderid", "userid", "sessionid",
		"episodeid", "ino", "authorid", "seriesid", "collectionid",
		// secrets / tokens
		"token", "refreshtoken", "accesstoken", "apikey", "password",
		// timestamps
		"createdat", "updatedat", "addedat", "lastupdate", "lastseen",
		"startedat", "finishedat", "birthtimems", "mtimems", "ctimems",
		"scanversion", "lastscan",
		// host-dependent
		"path", "relpath", "contenturl", "coverpath", "metadatapath",
	}
	out := make(map[string]bool, len(keys))
	for _, k := range keys {
		out[k] = true
	}
	return out
}

// Normalizer canonicalizes volatile values so that two captures of the same
// endpoint compare equal.
type Normalizer struct {
	Volatile map[string]bool
}

// NewNormalizer returns a Normalizer using DefaultVolatileKeys.
func NewNormalizer() *Normalizer {
	return &Normalizer{Volatile: DefaultVolatileKeys()}
}

// Normalize returns a deep copy of v with volatile values canonicalized. The
// input is never mutated.
func (n *Normalizer) Normalize(v any) any {
	return n.normalize(v, false)
}

// normalize deep-copies v. When volatile is true, scalar values are replaced
// with a canonical value of the SAME JSON type.
func (n *Normalizer) normalize(v any, volatile bool) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, child := range t {
			out[k] = n.normalize(child, n.isVolatile(k))
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, child := range t {
			// Array elements inherit the parent key's volatility so that
			// e.g. "tags": ["a","b"] under a volatile key is canonicalized.
			out[i] = n.normalize(child, volatile)
		}
		return out
	case string:
		if volatile {
			return volatilePlaceholder
		}
		return t
	case float64:
		if volatile {
			return float64(0)
		}
		return t
	case bool:
		if volatile {
			return false
		}
		return t
	default:
		// nil and unknown types pass through; nil already carries no value.
		return v
	}
}

func (n *Normalizer) isVolatile(key string) bool {
	return n.Volatile[strings.ToLower(key)]
}
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `go test ./internal/absync/conformance/ -v`
Expected: PASS — every test in the package, including the earlier differ tests.

- [ ] **Step 5: Commit**

```bash
git add internal/absync/conformance/normalize.go internal/absync/conformance/normalize_test.go
git commit -m "test(abs-sync): add type-preserving volatile-value normalizer"
```

---

### Task 5: Fixture capture script

**Files:**
- Create: `scripts/abs_capture_fixtures.py`
- Create: `testdata/abs-fixtures/README.md`
- Create (generated, committed): `testdata/abs-fixtures/*.json`

**Interfaces:**
- Consumes: the running oracle from Task 1 at `http://localhost:13378`.
- Produces: one JSON file per captured endpoint, named `<method>_<slugified-path>.json`, each shaped:
  ```json
  {
    "request": {"method": "GET", "path": "/api/libraries", "body": null},
    "response": {"status": 200, "headers": {"content-type": "application/json"}, "body": {}}
  }
  ```
  Task 6 loads these. Bodies are stored **raw** (not normalized) — normalization happens at compare time in Go, so fixtures stay faithful to what ABS actually returned.

Per CLAUDE.md, non-trivial scripting is Python, not shell.

- [ ] **Step 1: Write the capture script**

Create `scripts/abs_capture_fixtures.py`:
```python
#!/usr/bin/env python3
# file: scripts/abs_capture_fixtures.py
# version: 1.0.0
# guid: 2a9c6e04-8d71-4f36-b920-5e83c1740af6
# last-edited: 2026-07-29
"""Capture golden API fixtures from the reference Audiobookshelf oracle.

The published ABS API docs are stale, so the running server is the only
trustworthy spec. This walks the endpoint surface we intend to implement and
records each request/response pair verbatim. Normalization happens later, in
Go, at compare time -- fixtures stay faithful to what ABS actually returned.

Usage:
    python3 scripts/abs_capture_fixtures.py \
        --base-url http://localhost:13378 \
        --username oracle --password oracle-dev-only
"""

from __future__ import annotations

import argparse
import json
import pathlib
import re
import sys

try:
    import requests
except ImportError:
    sys.exit("requests is required: python3 -m pip install requests")

FIXTURE_DIR = pathlib.Path(__file__).resolve().parent.parent / "testdata" / "abs-fixtures"


def slugify(method: str, path: str) -> str:
    """Build a stable filename from a method and path."""
    slug = re.sub(r"[^a-zA-Z0-9]+", "_", path).strip("_").lower()
    return f"{method.lower()}_{slug or 'root'}.json"


def write_fixture(method: str, path: str, body, resp) -> None:
    """Persist one request/response pair."""
    try:
        parsed = resp.json()
    except ValueError:
        parsed = {"__non_json_body__": resp.text[:2000]}

    fixture = {
        "request": {"method": method, "path": path, "body": body},
        "response": {
            "status": resp.status_code,
            "headers": {
                k.lower(): v
                for k, v in resp.headers.items()
                if k.lower() in ("content-type", "accept-ranges", "etag", "cache-control")
            },
            "body": parsed,
        },
    }

    FIXTURE_DIR.mkdir(parents=True, exist_ok=True)
    out = FIXTURE_DIR / slugify(method, path)
    out.write_text(json.dumps(fixture, indent=2, sort_keys=True) + "\n")
    print(f"  {resp.status_code}  {method:5s} {path}  ->  {out.name}")


def main() -> int:
    ap = argparse.ArgumentParser()
    ap.add_argument("--base-url", default="http://localhost:13378")
    ap.add_argument("--username", default="oracle")
    ap.add_argument("--password", default="oracle-dev-only")
    args = ap.parse_args()

    base = args.base_url.rstrip("/")
    sess = requests.Session()

    print("== discovery (pre-auth) ==")
    for path in ("/ping", "/status"):
        write_fixture("GET", path, None, sess.get(f"{base}{path}", timeout=30))

    print("== auth ==")
    login_body = {"username": args.username, "password": args.password}
    # Mobile clients send x-return-tokens to get the refresh token in the body.
    login = sess.post(
        f"{base}/login",
        json=login_body,
        headers={"x-return-tokens": "true"},
        timeout=30,
    )
    write_fixture("POST", "/login", login_body, login)
    if login.status_code != 200:
        return f"login failed ({login.status_code}); is the oracle initialized?"

    payload = login.json()
    token = payload.get("user", {}).get("token") or payload.get("accessToken")
    if not token:
        return f"could not find an access token in the login response: {list(payload)}"
    sess.headers["Authorization"] = f"Bearer {token}"

    print("== user ==")
    for path in ("/api/me", "/api/me/progress", "/api/me/bookmarks"):
        write_fixture("GET", path, None, sess.get(f"{base}{path}", timeout=30))

    print("== libraries ==")
    libs = sess.get(f"{base}/api/libraries", timeout=30)
    write_fixture("GET", "/api/libraries", None, libs)
    library_ids = [lib["id"] for lib in libs.json().get("libraries", [])]
    if not library_ids:
        return "no libraries found on the oracle; add one pointing at /audiobooks"

    item_id = None
    for lib_id in library_ids:
        for suffix in ("items?limit=10&page=0", "personalized", "series", "authors", "search?q=odyssey"):
            path = f"/api/libraries/{lib_id}/{suffix}"
            write_fixture("GET", path, None, sess.get(f"{base}{path}", timeout=60))
        if item_id is None:
            items = sess.get(f"{base}/api/libraries/{lib_id}/items?limit=10&page=0", timeout=60)
            results = items.json().get("results", [])
            if results:
                item_id = results[0]["id"]

    if item_id is None:
        return "no library items found; did the oracle finish scanning?"

    print("== item detail + playback ==")
    detail = f"/api/items/{item_id}?expanded=1&include=progress"
    write_fixture("GET", detail, None, sess.get(f"{base}{detail}", timeout=30))

    play_body = {
        "deviceInfo": {"clientName": "conformance-capture", "deviceId": "capture-001"},
        "mediaPlayer": "unknown",
        "forceDirectPlay": True,
    }
    play = sess.post(f"{base}/api/items/{item_id}/play", json=play_body, timeout=60)
    write_fixture("POST", f"/api/items/{item_id}/play", play_body, play)

    if play.status_code == 200:
        session_id = play.json().get("id")
        if session_id:
            sync_body = {"currentTime": 12.5, "timeListened": 10, "duration": 9975.48}
            write_fixture(
                "POST",
                f"/api/session/{session_id}/sync",
                sync_body,
                sess.post(f"{base}/api/session/{session_id}/sync", json=sync_body, timeout=30),
            )
            write_fixture(
                "POST",
                f"/api/session/{session_id}/close",
                None,
                sess.post(f"{base}/api/session/{session_id}/close", timeout=30),
            )

    print("== progress + bookmarks ==")
    prog_body = {"currentTime": 42.0, "duration": 9975.48, "progress": 0.004, "isFinished": False}
    write_fixture(
        "PATCH",
        f"/api/me/progress/{item_id}",
        prog_body,
        sess.patch(f"{base}/api/me/progress/{item_id}", json=prog_body, timeout=30),
    )

    bm_body = {"time": 100, "title": "conformance bookmark"}
    write_fixture(
        "POST",
        f"/api/me/item/{item_id}/bookmark",
        bm_body,
        sess.post(f"{base}/api/me/item/{item_id}/bookmark", json=bm_body, timeout=30),
    )
    write_fixture(
        "GET",
        f"/api/me/bookmarks/{item_id}",
        None,
        sess.get(f"{base}/api/me/bookmarks/{item_id}", timeout=30),
    )

    print(f"\nWrote fixtures to {FIXTURE_DIR}")
    return 0


if __name__ == "__main__":
    result = main()
    if isinstance(result, str):
        sys.exit(f"ERROR: {result}")
    sys.exit(result)
```

- [ ] **Step 2: Run the capture against the oracle**

Run:
```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer-abs-sync
python3 -m pip install --quiet requests
python3 scripts/abs_capture_fixtures.py
```
Expected: a printed list of captured endpoints with `200` statuses, ending with `Wrote fixtures to .../testdata/abs-fixtures`. If login fails, finish the oracle's first-run setup (Task 1 Step 6) and retry.

**If any endpoint returns a non-200**, do not "fix" it by changing the script's expectations — record it and report. A 404 here is real information about the 2.36.x surface (e.g. an endpoint that moved), and the spec must be corrected rather than the fixture faked.

- [ ] **Step 3: Inspect one fixture to confirm the shape is useful**

Run:
```bash
python3 -c "import json;d=json.load(open('testdata/abs-fixtures/get_api_libraries.json'));print(json.dumps(d,indent=2)[:800])"
```
Expected: a `request`/`response` envelope whose `response.body` contains a `libraries` array with real library objects.

- [ ] **Step 4: Write the fixtures README**

Create `testdata/abs-fixtures/README.md`:
```markdown
<!-- file: testdata/abs-fixtures/README.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0e7d3b96-5a28-4c14-8f7b-91c6a04e2d53 -->
<!-- last-edited: 2026-07-29 -->

# ABS Golden Fixtures

Request/response pairs captured verbatim from a **real Audiobookshelf 2.36.x server**
(see `../abs-oracle/`). These are the specification our ABS-compatible API is tested
against, because the published docs at api.audiobookshelf.org are stale and unmaintained.

## Regenerating

```bash
cd testdata/abs-oracle && docker compose up -d && cd -
python3 scripts/abs_capture_fixtures.py
```

## Why bodies are stored raw

Fixtures are **not** normalized on disk. Normalization (canonicalizing volatile ids,
timestamps, inodes) happens at compare time in `internal/absync/conformance`. Keeping
the raw capture means a fixture stays a faithful record of what ABS actually returned,
and the normalizer's own rules stay reviewable and changeable without a recapture.

## What conformance checks

Field **presence** and **type**, not just values — an ABS client that is missing a
field it hard-requires fails opaquely, so a missing field is the highest-severity
finding. See `internal/absync/conformance/diff.go`.
```

- [ ] **Step 5: Commit**

```bash
git add scripts/abs_capture_fixtures.py testdata/abs-fixtures/
git commit -m "test(abs-sync): capture golden fixtures from the ABS 2.36.x oracle"
```

---

### Task 6: Fixture loading wired to the differ

**Files:**
- Create: `internal/absync/conformance/fixture.go`
- Test: `internal/absync/conformance/fixture_test.go`

**Interfaces:**
- Consumes: `Compare`, `Options`, `Finding` (Task 3); `NewNormalizer` (Task 4); the fixture files from Task 5.
- Produces:
  - `type Fixture struct { Request FixtureRequest; Response FixtureResponse }`
  - `type FixtureRequest struct { Method string; Path string; Body any }`
  - `type FixtureResponse struct { Status int; Headers map[string]string; Body any }`
  - `func LoadFixture(path string) (*Fixture, error)`
  - `func (f *Fixture) CompareBody(got any, opts Options) []Finding` — normalizes both sides, then diffs.

This closes the loop: later phases assert `len(fixture.CompareBody(ourResponse, Options{IgnoreExtra: true})) == 0`.

- [ ] **Step 1: Write the failing test**

Create `internal/absync/conformance/fixture_test.go`:
```go
// file: internal/absync/conformance/fixture_test.go
// version: 1.0.0
// guid: 6b14e8a7-2d59-4370-9c86-af250d63b71e
// last-edited: 2026-07-29

package conformance

import (
	"os"
	"path/filepath"
	"testing"
)

// writeTempFixture creates a fixture file and returns its path.
func writeTempFixture(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "get_api_libraries.json")
	if err := os.WriteFile(p, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	return p
}

const sampleFixture = `{
  "request": {"method": "GET", "path": "/api/libraries", "body": null},
  "response": {
    "status": 200,
    "headers": {"content-type": "application/json"},
    "body": {"libraries": [{"id": "lib-1", "name": "Books", "mediaType": "book"}]}
  }
}`

func TestLoadFixtureParsesEnvelope(t *testing.T) {
	f, err := LoadFixture(writeTempFixture(t, sampleFixture))
	if err != nil {
		t.Fatalf("LoadFixture: %v", err)
	}
	if f.Request.Method != "GET" || f.Request.Path != "/api/libraries" {
		t.Errorf("unexpected request: %+v", f.Request)
	}
	if f.Response.Status != 200 {
		t.Errorf("status = %d, want 200", f.Response.Status)
	}
	if f.Response.Headers["content-type"] != "application/json" {
		t.Errorf("unexpected headers: %v", f.Response.Headers)
	}
	if JSONType(f.Response.Body) != "object" {
		t.Errorf("body should be an object, got %s", JSONType(f.Response.Body))
	}
}

func TestLoadFixtureErrorsOnMissingFile(t *testing.T) {
	if _, err := LoadFixture(filepath.Join(t.TempDir(), "nope.json")); err == nil {
		t.Fatal("expected an error for a missing fixture file")
	}
}

func TestCompareBodyIgnoresVolatileIDs(t *testing.T) {
	f, err := LoadFixture(writeTempFixture(t, sampleFixture))
	if err != nil {
		t.Fatalf("LoadFixture: %v", err)
	}
	// Our response has a DIFFERENT id but the same shape -- must be conformant.
	got := mustJSON(t, `{"libraries":[{"id":"01HXYZ","name":"Books","mediaType":"book"}]}`)

	if fs := f.CompareBody(got, Options{CompareValues: true}); len(fs) != 0 {
		t.Errorf("differing ids should normalize away, got %v", fs)
	}
}

func TestCompareBodyCatchesAMissingRequiredField(t *testing.T) {
	f, err := LoadFixture(writeTempFixture(t, sampleFixture))
	if err != nil {
		t.Fatalf("LoadFixture: %v", err)
	}
	// mediaType omitted -- exactly the class of bug that breaks ABS clients.
	got := mustJSON(t, `{"libraries":[{"id":"01HXYZ","name":"Books"}]}`)

	fs := f.CompareBody(got, Options{})
	if findingAt(fs, "libraries[0].mediaType", KindMissingField) == nil {
		t.Fatalf("expected missing_field at libraries[0].mediaType, got %v", fs)
	}
}
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `go test ./internal/absync/conformance/ -run 'TestLoadFixture|TestCompareBody' -v`
Expected: FAIL — build error, `undefined: LoadFixture`.

- [ ] **Step 3: Write minimal implementation**

Create `internal/absync/conformance/fixture.go`:
```go
// file: internal/absync/conformance/fixture.go
// version: 1.0.0
// guid: 9c8a5f72-4e16-4b93-a05d-73f1e6community
// last-edited: 2026-07-29

package conformance

import (
	"encoding/json"
	"fmt"
	"os"
)

// FixtureRequest is the request half of a captured pair.
type FixtureRequest struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Body   any    `json:"body"`
}

// FixtureResponse is the response half of a captured pair.
type FixtureResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    any               `json:"body"`
}

// Fixture is one request/response pair captured verbatim from a real
// Audiobookshelf server. Bodies are stored raw; normalization happens here at
// compare time so the on-disk record stays faithful to what ABS returned.
type Fixture struct {
	Request  FixtureRequest  `json:"request"`
	Response FixtureResponse `json:"response"`
}

// LoadFixture reads and parses a fixture file written by
// scripts/abs_capture_fixtures.py.
func LoadFixture(path string) (*Fixture, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read fixture %s: %w", path, err)
	}
	var f Fixture
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("parse fixture %s: %w", path, err)
	}
	return &f, nil
}

// CompareBody normalizes both the fixture body and got, then diffs them.
// A nil/empty result means our response is conformant with real ABS.
func (f *Fixture) CompareBody(got any, opts Options) []Finding {
	n := NewNormalizer()
	return Compare(n.Normalize(f.Response.Body), n.Normalize(got), opts)
}
```

**Note to implementer:** the `guid:` line contains the placeholder `73f1e6community`. Replace the whole GUID with a fresh one from `uuidgen | tr '[:upper:]' '[:lower:]'` before committing.

- [ ] **Step 4: Run the full package test suite**

Run:
```bash
go test ./internal/absync/conformance/ -v
go test ./internal/absync/conformance/ -race
go vet ./internal/absync/conformance/
```
Expected: all tests PASS under both plain and `-race`; no vet output.

- [ ] **Step 5: Verify the harness works against a REAL captured fixture**

This proves the instrument works on real data, not just hand-written literals. Run:
```bash
cat > /tmp/abs_real_fixture_check_test.go <<'EOF'
package conformance

import "testing"

// TestRealFixtureSelfCompare loads a real captured fixture and compares it to
// itself. It must be perfectly conformant; if not, the normalizer or differ is
// broken (e.g. map iteration order leaking, or a mutating Normalize).
func TestRealFixtureSelfCompare(t *testing.T) {
	f, err := LoadFixture("../../../testdata/abs-fixtures/get_api_libraries.json")
	if err != nil {
		t.Skipf("real fixture not captured yet: %v", err)
	}
	if fs := f.CompareBody(f.Response.Body, Options{CompareValues: true}); len(fs) != 0 {
		t.Fatalf("a real fixture must self-compare clean, got %v", fs)
	}
}
EOF
cp /tmp/abs_real_fixture_check_test.go internal/absync/conformance/realfixture_test.go
go test ./internal/absync/conformance/ -run TestRealFixtureSelfCompare -v
```
Expected: PASS (or SKIP with a clear message if Task 5 has not been run yet).

Then add the required header to the new file:
```bash
python3 - <<'EOF'
import pathlib, subprocess
p = pathlib.Path("internal/absync/conformance/realfixture_test.go")
guid = subprocess.check_output(["uuidgen"]).decode().strip().lower()
p.write_text(
    "// file: internal/absync/conformance/realfixture_test.go\n"
    "// version: 1.0.0\n"
    f"// guid: {guid}\n"
    "// last-edited: 2026-07-29\n\n" + p.read_text()
)
EOF
gofmt -w internal/absync/conformance/realfixture_test.go
go test ./internal/absync/conformance/ -v
```
Expected: PASS.

- [ ] **Step 6: Add the changelog fragment**

Run:
```bash
python3 - <<'EOF'
import pathlib, subprocess
guid = subprocess.check_output(["uuidgen"]).decode().strip().lower()
p = pathlib.Path("changelog.d/20260729_000000_abs_sync_phase0_oracle.md")
p.write_text(
    "<!-- file: changelog.d/20260729_000000_abs_sync_phase0_oracle.md -->\n"
    "<!-- version: 1.0.0 -->\n"
    f"<!-- guid: {guid} -->\n"
    "<!-- last-edited: 2026-07-29 -->\n\n"
    "### Added\n\n"
    "- **Audiobookshelf conformance harness (Phase 0).** Added a pinned ABS 2.36.x\n"
    "  reference oracle (`testdata/abs-oracle/`), a fixture capture script\n"
    "  (`scripts/abs_capture_fixtures.py`), golden fixtures (`testdata/abs-fixtures/`),\n"
    "  and `internal/absync/conformance` -- a differ that checks field presence and\n"
    "  JSON type, not just values, so a response missing a field an ABS client\n"
    "  hard-requires fails the build instead of failing opaquely on a phone.\n"
)
EOF
```

- [ ] **Step 7: Commit**

```bash
git add internal/absync/conformance/ changelog.d/
git commit -m "test(abs-sync): wire golden fixtures to the conformance differ"
```

---

### Task 7: Client network-layer audit (the mode-deciding question)

**Files:**
- Create: `docs/reference/abs-client-network-audit.md`
- Modify: `docs/specs/2026-07-29-abs-sync-api-design.md` (§3.0 — record the resolved mode)

**Interfaces:**
- Consumes: nothing in code. This task resolves the one open topology question in the spec.
- Produces: a decision record that selects Mode B or Mode C, which determines whether spec §3.1–3.5 (our own JWT + refresh rotation + grace) is built at all in Phase 1.

**Why this is a task and not a footnote:** the spec's Mode B (stock client + Cloudflare service-token headers) requires the client to attach its custom headers to **every** request. On iOS, audio streaming usually goes through `AVURLAsset`/`AVPlayer` and downloads through a background `URLSession`, both of which bypass an app's normal request-building code unless headers are injected explicitly (`AVURLAssetHTTPHeaderFieldsKey`, or per-task headers). If headers are missing there, library browsing authenticates fine and **playback 403s at the Cloudflare edge** — a failure that looks like a server bug and would burn hours to diagnose later.

- [ ] **Step 1: Determine header coverage in ShelfPlayer's networking layer**

Clone and inspect (read-only; do not modify):
```bash
cd /tmp && rm -rf ShelfPlayer && git clone --depth 1 https://github.com/rasmuslos/ShelfPlayer.git
cd /tmp/ShelfPlayer
grep -rn "AVURLAssetHTTPHeaderFieldsKey\|httpAdditionalHeaders\|setValue(.*forHTTPHeaderField\|allHTTPHeaderFields" --include=*.swift . | head -40
grep -rln "customHeader\|CustomHeader\|additionalHeaders" --include=*.swift . | head -20
grep -rn "AVURLAsset(" --include=*.swift . | head -20
```
Record for each of these paths whether user-configured custom headers are attached:
1. `/status` and `/ping` (pre-auth discovery)
2. `/login`
3. normal JSON API calls
4. **audio streaming (`AVURLAsset` / `AVPlayerItem`)**
5. **offline/background download tasks**

- [ ] **Step 2: Do the same for Plappa, and confirm the known `/status` gap**

```bash
cd /tmp && rm -rf plappa && git clone --depth 1 https://github.com/LeoKlaus/plappa.git
cd /tmp/plappa
grep -rn "AVURLAssetHTTPHeaderFieldsKey\|httpAdditionalHeaders\|setValue(.*forHTTPHeaderField" --include=*.swift . | head -40
```
Cross-reference upstream issue <https://github.com/LeoKlaus/plappa/issues/330> ("Custom header appears to be missing on audiobookshelf status request") and record its current state.

- [ ] **Step 3: Write the audit document**

Create `docs/reference/abs-client-network-audit.md` with the required 4-line `<!-- -->` header (fresh GUID from `uuidgen`), containing:
- A table: rows = the 5 request paths from Step 1, columns = ShelfPlayer / Plappa, cells = "headers attached" / "NOT attached" / "undetermined", each with a `file:line` citation or an explicit "could not determine".
- A **verdict** section: does Mode B survive? Mode B requires *all five* rows to attach headers (with the `/status` row waivable via the Access bypass policy).
- If Mode B fails: state plainly that Mode C (WARP device session, split-tunnel Include mode scoped to the one hostname) is the selected mode, per the owner's accepted fallback.

State uncertainty explicitly. "Could not determine from source" is a valid, useful finding; a confident guess here is worse than an admission, because it silently picks the topology.

- [ ] **Step 4: Record the resolved mode in the spec**

Edit `docs/specs/2026-07-29-abs-sync-api-design.md` §3.0: replace the "Phase 0 must verify the streaming/download path specifically" sentence with the resolved outcome, linking to the audit document. Bump the spec's `version:` and `last-edited:` header lines.

**Then update §7's phase table:** if Mode C is selected, mark Phase 1's §3.1–3.5 scope (JWT, refresh rotation, grace, argon2id) as **not required**, and note that Phase 1 reduces to the ABS router group + `Cf-Access-Jwt-Assertion` trust + `/ping`,`/status`,`/login` shims. This is the single largest scope reduction available in the whole project — do not leave it implicit.

- [ ] **Step 5: Commit**

```bash
git add docs/reference/abs-client-network-audit.md docs/specs/2026-07-29-abs-sync-api-design.md
git commit -m "docs(abs-sync): audit iOS client header coverage and resolve the credential mode"
```

---

## Definition of done for Phase 0

- [ ] `docker compose up -d` in `testdata/abs-oracle/` yields a working ABS 2.36.x server with two books scanned.
- [ ] `python3 scripts/abs_capture_fixtures.py` writes fixtures for every endpoint in spec §7's surface, with all statuses recorded (non-200s reported, not hidden).
- [ ] `go test ./internal/absync/conformance/ -race` passes, including the real-fixture self-compare.
- [ ] The differ demonstrably catches a missing required field and a type mismatch (covered by tests).
- [ ] `docs/reference/abs-client-network-audit.md` exists and selects Mode B or Mode C with cited evidence.
- [ ] The spec is updated with the resolved mode and any Phase 1 scope reduction.
- [ ] A `changelog.d/` fragment exists.
- [ ] `make ci` is green.

## Test strategy

- **TDD throughout:** every Go unit is a failing test first (Tasks 2, 3, 4, 6 each begin with RED).
- **The harness is tested on real data, not only literals** (Task 6 Step 5): a real captured fixture must self-compare clean, which catches a mutating normalizer or map-ordering leak that hand-written tests could miss.
- **`-race` on the whole package**, per repo convention.
- **Negative tests are the point:** the differ's value is catching a *missing* field, so the suite asserts findings appear, not merely that clean cases pass.

## Rollback

Phase 0 adds only test scaffolding, fixtures, and documentation. It ships **no production code paths** — nothing is wired into the server, no routes are registered, no dependencies are added. Reverting the phase is a plain `git revert` of its commits with zero runtime impact. The oracle is a local-only container (`docker compose down -v`) that never runs in production.
