// file: internal/plugins/deluge/import.go
// version: 1.0.3
// guid: f1e2d3c4-b5a6-7890-cdef-0123456789ab
// last-edited: 2026-07-12

package deluge

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	delugeclient "github.com/falkcorp/audiobook-organizer/internal/deluge"
	"github.com/falkcorp/audiobook-organizer/internal/logging"
)

// importToLibrary reflinks src into dst (falls back to copy), updates the DB,
// then calls core.move_storage so Deluge continues seeding from dst.
//
//lint:ignore U1000 kept: deluge import mechanism; server shim was removed pointing at a not-yet-existing deluge.ImportToLibrary — wire-up inconsistency to resolve (2026-07-12)
func (p *Plugin) importToLibrary(ctx context.Context, torrentHash, srcPath, dstPath string) error {
	if p == nil {
		return fmt.Errorf("plugin not initialized")
	}

	// 1. Validate both paths
	if p.cache != nil && p.cache.IsProtected(srcPath) {
		return fmt.Errorf("source path %q is protected", srcPath)
	}

	// 2. Ensure parent directory exists
	if err := os.MkdirAll(filepath.Dir(dstPath), 0o755); err != nil {
		return fmt.Errorf("create destination dir: %w", err)
	}

	// 3. Reflink (best-effort) or copy
	if err := delugeclient.ReflinkOrCopy(srcPath, dstPath); err != nil {
		return fmt.Errorf("reflink/copy: %w", err)
	}

	// 4. Update DB: set imported_from_deluge_at, deluge_original_path
	if p.store != nil {
		if err := p.store.MarkFileImportedFromDeluge(ctx, srcPath, dstPath, torrentHash); err != nil {
			logging.Warn(ctx, "failed to update deluge import record", "err", err)
			// non-fatal: file is already copied
		}
	}

	// 5. Call core.move_storage (best-effort)
	if p.client != nil && torrentHash != "" {
		if err := p.client.MoveStorage([]string{torrentHash}, filepath.Dir(dstPath)); err != nil {
			logging.Warn(ctx, "core.move_storage failed; Deluge will seed from original path", "err", err, "torrent", torrentHash)
			// non-fatal: the import succeeded even if seeding location doesn't update
		}
	}

	return nil
}
