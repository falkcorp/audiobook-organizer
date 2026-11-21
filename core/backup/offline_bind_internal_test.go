// file: core/backup/offline_bind_internal_test.go
// version: 0.1.0
// guid: 3b7e9d14-6a2f-4c58-91d0-8e4f2a6c5b71
// last-edited: 2026-11-21

package backup

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/internal/backuparchive"
)

// The offline extractor checks the archive against its checksum sidecar and
// then opens the path again to extract it. swapOnErr (legacy_bind_internal_test.go)
// lands inside the check: extractStore asks ctx.Err once on entry, and the
// sidecar check's copy asks it before every read, so the second call renames
// the replacement over the path after the check opened the original and before
// the extraction opens the path.

// TestExtractStoreRefusesACoreArchiveReplacedAfterTheChecksumCheck: a
// mediacore-backup/1 archive renamed over the path after the sidecar check is
// refused. The replacement is a whole, sound archive whose entries match its
// own manifest, so only the digest of the bytes the extraction read, held to
// the one the check matched, tells it apart. Extracted directly, the same
// replacement succeeds: the refusal is the binding, not damage.
func TestExtractStoreRefusesACoreArchiveReplacedAfterTheChecksumCheck(t *testing.T) {
	path, _ := tinyCore(t, t.TempDir(), "01JCZ8Y5W2B3C4D5E6F7G8H9JK", true)
	other, _ := tinyCore(t, t.TempDir(), "01JCZ8Y5W2B3C4D5E6F7G8H9JM", false)
	var swapErr error
	ctx := &swapOnErr{Context: context.Background(), swap: func() { swapErr = os.Rename(other, path) }}
	id, err := extractStore(ctx, path, t.TempDir(), Disk{})
	if swapErr != nil || ctx.calls < 2 {
		t.Fatalf("fixture: the archive was not replaced (%d Err calls): %v", ctx.calls, swapErr)
	}
	if !errors.Is(err, ErrChecksumMismatch) || !errors.Is(err, backuparchive.ErrChanged) || id != "" {
		t.Fatalf("a core archive replaced after the checksum check: id %q, err %v; want ErrChecksumMismatch and backuparchive.ErrChanged", id, err)
	}
	if err := os.Remove(path + ChecksumSidecar); err != nil {
		t.Fatal(err)
	}
	if id, err := extractStore(context.Background(), path, t.TempDir(), Disk{}); err != nil || id != "01JCZ8Y5W2B3C4D5E6F7G8H9JM" {
		t.Fatalf("the replacement extracted on its own: id %q, err %v", id, err)
	}
}

// TestExtractStoreRefusesALegacyArchiveReplacedAfterTheChecksumCheck: a
// legacy-abo/0 archive renamed over the path after the sidecar check is
// refused before one byte is written: the scan's digest is held to the one the
// check matched. Extracted directly, the replacement succeeds.
func TestExtractStoreRefusesALegacyArchiveReplacedAfterTheChecksumCheck(t *testing.T) {
	path, _ := tinyLegacy(t, t.TempDir(), "Alfred", true)
	other, _ := tinyLegacy(t, t.TempDir(), "Bane", false)
	var swapErr error
	ctx := &swapOnErr{Context: context.Background(), swap: func() { swapErr = os.Rename(other, path) }}
	store := t.TempDir()
	id, err := extractStore(ctx, path, store, Disk{})
	if swapErr != nil || ctx.calls < 2 {
		t.Fatalf("fixture: the archive was not replaced (%d Err calls): %v", ctx.calls, swapErr)
	}
	if !errors.Is(err, ErrChecksumMismatch) || !errors.Is(err, backuparchive.ErrChanged) || id != "" || !strings.Contains(err.Error(), "checksum check accepted") {
		t.Fatalf("a legacy archive replaced after the checksum check: id %q, err %v; want the extractor's ErrChecksumMismatch and backuparchive.ErrChanged", id, err)
	}
	if ents, _ := os.ReadDir(store); len(ents) != 0 {
		t.Fatalf("the refused legacy extraction wrote %d entries", len(ents))
	}
	if err := os.Remove(path + ChecksumSidecar); err != nil {
		t.Fatal(err)
	}
	if id, err := extractStore(context.Background(), path, t.TempDir(), Disk{}); err != nil || !IsLegacyID(id) {
		t.Fatalf("the replacement extracted on its own: id %q, err %v", id, err)
	}
}

// TestExtractStoreRefusesALegacyArchiveReplacedBetweenTheScanAndTheExtraction:
// a legacy-abo/0 archive renamed over the path after the scan matched the
// checksum check's digest, and before the extraction opens the path again, is
// refused with the same verification failure (409), not a bare ErrChanged
// (500). The free-space check between the two is where the swap lands.
func TestExtractStoreRefusesALegacyArchiveReplacedBetweenTheScanAndTheExtraction(t *testing.T) {
	path, _ := tinyLegacy(t, t.TempDir(), "Alfred", true)
	other, _ := tinyLegacy(t, t.TempDir(), "Selina", false)
	var swapped bool
	var swapErr error
	disk := Disk{
		Device: func(string) (uint64, error) {
			if !swapped {
				swapped, swapErr = true, os.Rename(other, path)
			}
			return 1, nil
		},
		Avail: func(string) (uint64, error) { return 1 << 40, nil },
	}
	id, err := extractStore(context.Background(), path, t.TempDir(), disk)
	if !swapped || swapErr != nil {
		t.Fatalf("fixture: the archive was not replaced between the scan and the extraction: %v", swapErr)
	}
	if !errors.Is(err, ErrChecksumMismatch) || !errors.Is(err, backuparchive.ErrChanged) || id != "" {
		t.Fatalf("a legacy archive replaced after the scan: id %q, err %v; want ErrChecksumMismatch and backuparchive.ErrChanged", id, err)
	}
}
