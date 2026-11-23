// file: internal/books/r2store_boot_test.go
// version: 0.1.0
// guid: 4d7a2e91-35c8-4b06-9f1e-a82c6d0b7e35
// last-edited: 2026-11-22

package books

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
)

// TestR2StoreBootsAtR3 is the R3 release's one promise about the store R2
// left: this build boots it, to ready, through the real books declaration (the
// upgrade floor, the level step, the empty cutover, no rewrite), and the boot
// changes no step's ledger. The store is the frozen testdata/r2store, which the
// cutover code that built it no longer exists to rebuild (R3-02e-1).
//
// "No step's ledger": every key core storage keeps a step's record under (the
// data steps' state, the versioned steps' applied records, the level stamps) is
// the same key with the same value after the boot as before it. A boot that
// found a step to run, or one this build registers that the R2 store lacks,
// writes there. The rows outside the ledger (the epoch, the lock, the log) are
// the boot's own and are not compared. The three first-boot tests this replaces
// (TestCheckpointPrecedesEveryWrite, TestStampWrittenAfterTheCheckpoint,
// TestProjectionKeepsRecordIDOnABOsBoot) asserted a HEAD store's first boot;
// the floor refuses every such store. The checkpoint order and its state files
// are core's (core/storage TestCheckpointPrecedesEveryWrite and
// TestCheckpointIncludesStateFiles); the row IDs and the projection sidecar
// of an accepted store are this test's unchanged rows and sidecars.
func TestR2StoreBootsAtR3(t *testing.T) {
	work := copyR2Store(t)
	before := rawKVs(t, r2StoreDB(work))
	sidecars := []string{database.StorageFormatSidecarPath(r2StoreDB(work)), r2StoreDB(work) + ".storage-levels"}
	sidecarsBefore := map[string]string{}
	for _, f := range sidecars {
		b, err := os.ReadFile(f)
		require.NoError(t, err, "the R2 store has no sidecar %s", filepath.Base(f))
		sidecarsBefore[f] = string(b)
	}

	r2StoreRun(t, work, nil)

	after := rawKVs(t, r2StoreDB(work))

	// The control: the ledger the comparison covers is not empty.
	ledger := 0
	for k := range before {
		if strings.HasPrefix(k, "preference:migration_") || strings.HasPrefix(k, "c/storage/") {
			ledger++
		}
	}
	require.Greater(t, ledger, database.LegacyCap, "the fixture's ledger is smaller than books' versions alone")

	const epoch = "c/storage/epoch\x00\x01" // every boot advances it
	for _, pre := range []string{"c/", "preference:migration_", "preference:db_version", "m/"} {
		var added, removed, changed []string
		for k, v := range after {
			if !strings.HasPrefix(k, pre) || k == epoch {
				continue
			}
			if old, ok := before[k]; !ok {
				added = append(added, k)
			} else if old != v {
				changed = append(changed, k)
			}
		}
		for k := range before {
			if _, ok := after[k]; strings.HasPrefix(k, pre) && k != epoch && !ok {
				removed = append(removed, k)
			}
		}
		assert.Empty(t, added, "the boot wrote %s rows an R2 store lacks: a step this build registers is not in the R2 ledger", pre)
		assert.Empty(t, removed, "the boot deleted %s rows", pre)
		assert.Empty(t, changed, "the boot rewrote %s rows", pre)
	}
	// The sidecars older builds read to refuse this store are as R2 wrote them:
	// the legacy projection above R0's supported format (so R0 refuses it) and
	// the level vector. The boot rewrites neither.
	for _, f := range sidecars {
		b, err := os.ReadFile(f)
		require.NoError(t, err)
		assert.Equal(t, sidecarsBefore[f], string(b), "the boot rewrote %s", filepath.Base(f))
	}
	assert.Equal(t, strconv.Itoa(database.SupportedStorageFormat+1), strings.TrimSpace(sidecarsBefore[sidecars[0]]), ".storage-format must stay above R0's supported format")
	assert.Contains(t, r2StoreDone(t, r2StoreDB(work), r2StoreSteps...), r2StoreSteps[0])
	for id, done := range r2StoreDone(t, r2StoreDB(work), r2StoreSteps...) {
		assert.True(t, done, "%s is no longer done", id)
	}
}
