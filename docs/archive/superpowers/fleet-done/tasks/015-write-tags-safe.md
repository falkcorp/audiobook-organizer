# Task 015: WriteTagsSafe — pre-flight guard + migrate all call sites

**Depends on:** none
**Estimated effort:** M
**Wave:** 6 (features, independent)

## Goal

Implement `WriteTagsSafe` as a pre-flight wrapper around all tag-write call sites. Before
writing tags, it validates the target path is not protected and falls back to `os.Copy` on
non-reflink filesystems. Then migrate all existing tag-write call sites to use it.

## Context

- Current tag-write flow: `taglib.WriteImage` + tag field writes in `internal/tagger/`
- Protected paths: iTunes mount paths must NEVER be written to
- `isProtectedPath` check in `internal/server/` needs to be accessible to the tagger layer
- Call sites for tag writes: bulk write-back, single-file write, cover embed
  (search for `tagger.WriteTags`, `taglib.Write`, `WriteBack` in `internal/`)
- Production is Linux ZFS; copy fallback is needed for non-ZFS/non-reflink mounts

## Files to create/modify

- `internal/fileops/write_tags_safe.go` — implement `WriteTagsSafe` (may already exist as stub)
- All callers of the current tag-write functions across `internal/`

## Instructions

### 1. Check if `write_tags_safe.go` already exists

```bash
ls internal/fileops/write_tags_safe.go
cat internal/fileops/write_tags_safe.go
```

If it exists as a stub, implement the body. If not, create it.

### 2. Implement `WriteTagsSafe`

```go
// WriteTagsSafe performs a pre-flight protected-path check, then writes tags.
// If the filesystem does not support in-place writes (e.g., read-only mounts),
// it copies to a temp file first, writes tags there, then replaces the original.
func WriteTagsSafe(ctx context.Context, filePath string, isProtected func(string) bool, writeFn func(string) error) error {
    if isProtected(filePath) {
        return fmt.Errorf("write_tags_safe: path %q is protected and cannot be modified", filePath)
    }

    // Try direct write first
    if err := writeFn(filePath); err == nil {
        return nil
    }

    // Fallback: write to temp file, then atomic replace
    tmp, err := os.CreateTemp(filepath.Dir(filePath), ".ao-tags-*.tmp")
    if err != nil {
        return fmt.Errorf("create temp file: %w", err)
    }
    tmpPath := tmp.Name()
    tmp.Close()
    defer os.Remove(tmpPath)

    // Copy original to temp
    if err := copyFile(filePath, tmpPath); err != nil {
        return fmt.Errorf("copy to temp: %w", err)
    }
    // Write tags to temp
    if err := writeFn(tmpPath); err != nil {
        return fmt.Errorf("write tags to temp: %w", err)
    }
    // Atomic replace
    return os.Rename(tmpPath, filePath)
}
```

### 3. Find all tag-write call sites

```bash
grep -rn "WriteTags\|WriteImage\|WriteBack\|tagger\.Write" internal/ --include="*.go" | grep -v "_test.go"
```

For each call site, wrap with `WriteTagsSafe(ctx, path, isProtected, func(p string) error { ... })`.

### 4. Ensure `isProtected` is injectable

The `isProtected` predicate should come from the server/plugin layer (where `protectedPathCache`
lives), passed down to the tagger. Do NOT create a package-level singleton — pass it as a
function argument.

## Test

```bash
go test ./internal/fileops/... -v -count=1
go test ./internal/tagger/... -v -count=1
make ci
```

## Commit

```
feat(fileops): WriteTagsSafe pre-flight guard + migrate all tag-write call sites
```

## PR title

`feat(fileops): WriteTagsSafe — protected path guard for tag writes`

## After merging

Mark `- [ ] **WriteTagsSafe**` and `- [ ] **Migrate all call sites**` as `- [x]` in `TODO.md`.
