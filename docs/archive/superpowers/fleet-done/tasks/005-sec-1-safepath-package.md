# Task 005: SEC-AUDIT-1 — Create `internal/security/safepath` typed package

**Depends on:** task 004 (CodeQL MaD pack) ideally merged first, but technically independent
**Estimated effort:** M (4–6 hours)
**Wave:** 3 (run after task 004 merges)

## Goal

Create `internal/security/safepath/` — a typed `SafePath` newtype that wraps a validated path
string. Callers that construct a `SafePath` have proven the path is within an allowed root.
Tasks 006–010 (Wave 4) will convert call sites to use this type, making path injection
compile-time impossible for validated paths.

## Context

- The project already has `internal/util/path.go` with `SafeJoin` and `WithinRoot` free functions
- The new package is a newtype layer on top — it doesn't replace those functions, it wraps them
- `SafePath` is an opaque type; its string value can only be obtained after validation
- This package must be created BEFORE tasks 006–010 which use it
- PebbleDB is the production database; no SQLite-only code needed
- All new files need headers: `// file: path\n// version: 1.0.0\n// guid: <uuid>\n// last-edited: YYYY-MM-DD`

## Files to create

- `internal/security/safepath/safepath.go` — main package
- `internal/security/safepath/safepath_test.go` — unit tests

## Instructions

### `internal/security/safepath/safepath.go`

```go
// file: internal/security/safepath/safepath.go
// version: 1.0.0
package safepath

import (
    "fmt"
    "path/filepath"
    "strings"
)

// SafePath is an opaque type representing a path that has been validated to
// lie within a known-safe root directory. Obtain one via Join or Validate;
// never construct directly from a string.
type SafePath struct{ s string }

// String returns the validated absolute path.
func (p SafePath) String() string { return p.s }

// Join validates that joining root with parts does not escape root, then
// returns a SafePath. Returns an error if any part would escape root.
func Join(root string, parts ...string) (SafePath, error) {
    elems := append([]string{root}, parts...)
    joined := filepath.Join(elems...)
    cleaned := filepath.Clean(joined)
    cleanRoot := filepath.Clean(root)
    if cleaned != cleanRoot && !strings.HasPrefix(cleaned, cleanRoot+string(filepath.Separator)) {
        return SafePath{}, fmt.Errorf("safepath: %q escapes root %q", cleaned, cleanRoot)
    }
    return SafePath{s: cleaned}, nil
}

// Validate asserts that an already-resolved path lies within root and wraps
// it as a SafePath. Use when the path was resolved by an external mechanism
// (e.g., os.Readlink) and needs validation.
func Validate(root, path string) (SafePath, error) {
    cleanRoot := filepath.Clean(root)
    cleaned := filepath.Clean(path)
    if cleaned != cleanRoot && !strings.HasPrefix(cleaned, cleanRoot+string(filepath.Separator)) {
        return SafePath{}, fmt.Errorf("safepath: %q escapes root %q", cleaned, cleanRoot)
    }
    return SafePath{s: cleaned}, nil
}

// MustJoin is like Join but panics on error. Only use in tests or where the
// inputs are compile-time constants.
func MustJoin(root string, parts ...string) SafePath {
    p, err := Join(root, parts...)
    if err != nil {
        panic(err)
    }
    return p
}
```

### `internal/security/safepath/safepath_test.go`

Write tests covering:
- `Join` with a normal relative sub-path returns correct SafePath
- `Join` with `..` traversal returns error
- `Join` with absolute path that escapes root returns error
- `Validate` with path inside root succeeds
- `Validate` with path outside root returns error
- `MustJoin` panics on escape attempt
- `String()` returns the cleaned absolute path

Use standard `testing` package. Minimum 7 test cases.

## Test

```bash
go test ./internal/security/safepath/... -v -count=1
make ci   # ensure nothing else broke
```

## Commit

```
feat(security): add SafePath typed newtype for validated filesystem paths (SEC-AUDIT-1)
```

## PR title

`feat(security): SafePath typed path boundary — SEC-AUDIT-1`

## After merging

Mark `- [ ] **SEC-AUDIT-1**` as `- [x]` in `TODO.md`.
Tasks 006–010 can now start.
