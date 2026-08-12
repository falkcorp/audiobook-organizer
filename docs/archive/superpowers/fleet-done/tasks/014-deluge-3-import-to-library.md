# Task 014: DELUGE-3 — `importToLibrary`: reflink + `core.move_storage`

**Depends on:** none (DELUGE-1 and DELUGE-2 are already merged)
**Estimated effort:** M
**Wave:** 6 (features, independent)
**Spec:** `docs/superpowers/bot-tasks/2026-04-29-deluge-3-import-to-library.md`

## Goal

Implement `importToLibrary` in the Deluge plugin: reflink the downloaded file from the Deluge
download directory into the library path, update the DB record, then call Deluge's
`core.move_storage` to keep Deluge seeding from the new location.

## Context

- DELUGE-1: `deluge_hash`, `deluge_original_path`, `imported_from_deluge_at` columns exist on `book_files`
- DELUGE-2: `protectedPathCache` with TTL and `IsProtected()` exist
- The Deluge plugin: `internal/plugins/deluge/` (check exact location)
- Reflink: use `golang.org/x/sys/unix.IoctlFileClone` or fall back to `os.Copy` if reflink fails
- `core.move_storage` is a Deluge RPC call: `client.Call("core.move_storage", [torrentHash, newPath])`
- Protected paths: NEVER write to iTunes paths — `isProtectedPath` check must wrap any write
- Production is Linux (ZFS); macOS may not support reflink → always implement fallback to Copy
- PebbleDB is the production DB; update `book_files` via the store interface

## Files to modify/create

- `internal/plugins/deluge/import.go` (new or edit existing import file)
- `internal/server/deluge_import_unix.go` (wire the new importToLibrary call)
- `internal/database/store.go` (add `MarkFileImportedFromDeluge` if not present)
- `internal/database/pebble_store.go` (implement `MarkFileImportedFromDeluge`)

## Instructions

### 1. Implement `importToLibrary(ctx, torrentHash, srcPath, dstPath string) error`

```go
// importToLibrary reflinks src into dst (falls back to copy), updates the DB,
// then calls core.move_storage so Deluge continues seeding from dst.
func (s *Service) importToLibrary(ctx context.Context, torrentHash, srcPath, dstPath string) error {
    // 1. Validate both paths
    if s.isProtectedPath(srcPath) {
        return fmt.Errorf("source path %q is protected", srcPath)
    }

    // 2. Ensure parent directory exists
    if err := os.MkdirAll(filepath.Dir(dstPath), 0755); err != nil {
        return fmt.Errorf("create destination dir: %w", err)
    }

    // 3. Reflink (best-effort) or copy
    if err := reflinkOrCopy(srcPath, dstPath); err != nil {
        return fmt.Errorf("reflink/copy: %w", err)
    }

    // 4. Update DB: set imported_from_deluge_at, deluge_original_path
    if err := s.store.MarkFileImportedFromDeluge(ctx, srcPath, dstPath, torrentHash); err != nil {
        slog.Warn("failed to update deluge import record", "err", err)
        // non-fatal: file is already copied
    }

    // 5. Call core.move_storage (best-effort)
    if err := s.delugeClient.MoveStorage(ctx, torrentHash, filepath.Dir(dstPath)); err != nil {
        slog.Warn("core.move_storage failed; Deluge will seed from original path", "err", err, "torrent", torrentHash)
        // non-fatal: the import succeeded even if seeding location doesn't update
    }

    return nil
}
```

### 2. Implement `reflinkOrCopy(src, dst string) error`

```go
func reflinkOrCopy(src, dst string) error {
    if err := tryReflink(src, dst); err == nil {
        return nil
    }
    // Fall back to copy
    in, err := os.Open(src)
    if err != nil { return err }
    defer in.Close()
    out, err := os.Create(dst)
    if err != nil { return err }
    defer out.Close()
    _, err = io.Copy(out, in)
    return err
}
```

Use `golang.org/x/sys/unix.IoctlFileClone` for the reflink attempt on Linux.
On macOS / when it fails with EOPNOTSUPP, fall through to copy.

### 3. Wire into the "Import" button handler

In `internal/server/deluge_import_unix.go`, find the handler for `POST /api/v1/deluge/import`
and call `importToLibrary` after the torrent is confirmed downloaded.

### 4. Add `MarkFileImportedFromDeluge` to store interface + PebbleDB

```go
// In store.go interface:
MarkFileImportedFromDeluge(ctx context.Context, originalPath, libraryPath, torrentHash string) error

// In pebble_store.go:
// Set book_files.imported_from_deluge_at = now(), deluge_original_path = originalPath
// Match by file path or torrent hash
```

## Test

```bash
go test ./internal/plugins/deluge/... -v -count=1
go test ./internal/server/... -run TestDeluge -v -count=1
make ci
```

## Commit

```
feat(deluge): importToLibrary with reflink + core.move_storage (DELUGE-3)
```

## PR title

`feat(deluge): importToLibrary reflink + seeding location update — DELUGE-3`

## After merging

Mark `- [ ] **DELUGE-3**` as `- [x]` in `TODO.md`.
