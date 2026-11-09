// file: internal/books/reset_rejected_test.go
// version: 1.0.0
// guid: 0cc6cf83-979a-477f-804b-19c1bb765953
// last-edited: 2026-11-09

package books

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// stageFactoryReset stages `storage reset --kind factory --confirm RESET`
// over h's stopped store, lets edit change the staged intent in the slot,
// and returns its id.
func (h *offlineABO) stageFactoryReset(edit func(*restorejournal.Intent)) string {
	h.t.Helper()
	out, err := h.run("storage reset", "--kind", "factory", "--confirm", "RESET")
	require.NoError(h.t, err, out)
	p := restorejournal.PendingPath(h.state)
	raw, err := os.ReadFile(p)
	require.NoError(h.t, err)
	var in restorejournal.Intent
	require.NoError(h.t, json.Unmarshal(raw, &in))
	require.Equal(h.t, restorejournal.ResetFactory, in.Reset)
	if edit != nil {
		edit(&in)
		raw, err = json.Marshal(in)
		require.NoError(h.t, err)
		require.NoError(h.t, os.WriteFile(p, raw, 0o600))
	}
	return in.ID
}

// requireResetRejected checks the boot after a rejected factory reset: the
// intent's journal ends rejected, the slot is empty, the store was never
// set aside (its stored settings are the ones the reset was to discard),
// and the files the reset hook deletes are all still there.
func (h *offlineABO) requireResetRejected(id string) {
	h.t.Helper()
	recs := journalOf(h.t, h.state, id)
	require.NotEmpty(h.t, recs)
	steps := make([]string, 0, len(recs))
	for _, r := range recs {
		steps = append(steps, r.Step)
	}
	require.Equal(h.t, "rejected", steps[len(steps)-1], "journal %v", steps)
	assert.NotContains(h.t, steps, "set_aside_started", "journal %v", steps)
	assert.NotContains(h.t, steps, "hook_done", "journal %v", steps)
	require.False(h.t, h.slot().Busy(), "slot after the rejecting boot = %+v", h.slot())
	assert.False(h.t, exists(restorejournal.SetAsideDest(h.state, h.path, id, h.path)), "the rejected reset set the store aside")
	assert.Equal(h.t, h.root, h.storedRoot(), "the rejected reset lost the stored settings")
	assert.True(h.t, exists(h.dump), "the rejected reset deleted the Open Library dumps")
	for _, f := range []string{"Year One", "The Killing Joke", "Hush.m4b"} {
		assert.True(h.t, exists(filepath.Join(h.root, f)), "the rejected reset deleted %s", f)
	}
}

// TestFactoryResetRejectedLeavesStoreAndSettings (05-storage §5S.3.15;
// SEC7-5, DATA7-6): P0a rejects a staged factory reset whose intent has
// expired, or that another binary staged, and the boot then serves the store
// it had, with every stored setting, the library files and the Open Library
// data as they were; nothing is set aside and the reset hook never runs.
func TestFactoryResetRejectedLeavesStoreAndSettings(t *testing.T) {
	for _, c := range []struct {
		name string
		edit func(*restorejournal.Intent)
	}{
		{"expired", func(in *restorejournal.Intent) { in.ExpiresAt = time.Now().UTC().Add(-time.Hour) }},
		{"staged by another binary", func(in *restorejournal.Intent) { in.StagedByBuild = "an-older-abo-build" }},
	} {
		t.Run(c.name, func(t *testing.T) {
			h := newOfflineABO(t)
			id := h.stageFactoryReset(c.edit)
			h.boot()
			h.requireResetRejected(id)
		})
	}
}

// TestFactoryResetIntentRejectedAfterRollback (05-storage §5S.3.15): a
// factory reset staged by this build and then booted by an older one (a
// rollback before the reset applied) is rejected at P0a by the older build,
// whose binary ID is not the intent's StagedByBuild; the store and its
// settings stay as they were.
func TestFactoryResetIntentRejectedAfterRollback(t *testing.T) {
	h := newOfflineABO(t)
	id := h.stageFactoryReset(nil)
	staged, err := app.BinaryID(h.opts)
	require.NoError(t, err)
	disk := h.disk
	h.disk = func(o *app.Options) {
		disk(o)
		o.Version = "v0.0.1-before-the-reset"
	}
	older, err := app.BinaryID(func() app.Options { o := h.opts; h.disk(&o); return o }())
	require.NoError(t, err)
	require.NotEqual(t, staged, older, "the rollback boots the build that staged the reset")
	h.boot()
	h.disk = disk
	h.requireResetRejected(id)
}

// TestFactoryResetWithoutLocationFile (05-storage §5S.3.15; R10-6): ABO
// keeps no backup.location.json at R1 (05-backup §5B.3.6 lands with F8), and
// a factory reset staged without one sets the store aside and runs its hook
// to the end; the swap writes no location file of its own.
func TestFactoryResetWithoutLocationFile(t *testing.T) {
	h := newOfflineABO(t)
	location := filepath.Join(h.state, "backup.location.json")
	require.False(t, exists(location), "the fixture keeps a location file")
	id := h.stageFactoryReset(nil)
	h.boot()
	recs := journalOf(t, h.state, id)
	require.Equal(t, "hook_done", recs[len(recs)-1].Step, "journal %+v", recs)
	require.False(t, h.slot().Busy(), "slot after the reset boot = %+v", h.slot())
	assert.True(t, exists(restorejournal.SetAsideDest(h.state, h.path, id, h.path)), "the old store was not set aside")
	assert.False(t, exists(location), "the reset invented a location file")
}
