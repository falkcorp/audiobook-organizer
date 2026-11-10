// file: internal/books/production_shaped_test.go
// version: 1.0.0
// guid: 2e522628-7990-43c4-8080-fa8436bdee80
// last-edited: 2026-11-10

package books

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/mediacore/app"
	"github.com/falkcorp/mediacore/storage"
)

// The production-shaped store's boot-time measurement (F2-14; 09 §9.3 "Boot
// time"). scripts/r2corpus/gen_production_shaped.py grows F2-12's stage-1 store
// to the owner's P1 counts and sizes and writes store_manifest.json beside it;
// this test boots the current build on a copy and records the time of every boot
// phase and step and the restart-to-ready time. The result sets the R2 release
// note's expected first-boot time and the runbook's DEPLOY_READY_TIMEOUT.
//
// Environment (F2-15 extends the job and reads the same names):
//
//	R2_SHAPED_STORE          the generated store; unset skips the test
//	R2_SHAPED_OUT            directory boot_time.json is written to
//	R2_SHAPED_READY_TIMEOUT  longest wait for ready on /health (default 25m)

const (
	shapedStoreManifest = "store_manifest.json"
	shapedBootTimeFile  = "boot_time.json"
	shapedDefaultWait   = 25 * time.Minute
)

// shapedPhase is one sample of <namespace>_kernel_boot_phase_seconds: step ""
// is the phase total, any other step is one migration step inside the phase.
type shapedPhase struct {
	Phase   string  `json:"phase"`
	Step    string  `json:"step"`
	Seconds float64 `json:"seconds"`
}

// shapedBootTime is boot_time.json (format coined by F2-14). Phases and
// ReadySeconds are the first boot, the one that runs every migration step;
// SteadyReadySeconds is a second boot of the same store, the steady one.
type shapedBootTime struct {
	Phases             []shapedPhase `json:"phases"`
	ReadySeconds       float64       `json:"ready_seconds"`
	SteadyReadySeconds float64       `json:"steady_ready_seconds"`
}

// copyShapedStore copies the generated store at src into dst for a boot,
// without store_manifest.json (the manifest lists every file except itself, so
// a copy holding it would not match). Pebble never rewrites a table in place,
// so tables (.sst, .blob) are hard linked when the filesystem allows it: a
// production-sized store is copied once, not once per copy. Every other file
// (WAL, MANIFEST, OPTIONS, the settings key, the search index) is a real copy a
// boot may write to.
func copyShapedStore(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if rel == shapedStoreManifest {
			return nil
		}
		if ext := filepath.Ext(rel); ext == ".sst" || ext == ".blob" {
			if os.Link(path, target) == nil {
				return nil
			}
		}
		return copyShapedFile(path, target)
	})
}

func copyShapedFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// shapedBootPhases is every kernel_boot_phase_seconds sample the app serves, in
// phase order then step order.
func shapedBootPhases(t *testing.T, a *app.App) []shapedPhase {
	t.Helper()
	mfs, err := a.Metrics().Gatherer().Gather()
	require.NoError(t, err)
	var out []shapedPhase
	for _, mf := range mfs {
		if !strings.HasSuffix(mf.GetName(), "_kernel_boot_phase_seconds") {
			continue
		}
		for _, m := range mf.GetMetric() {
			var p shapedPhase
			for _, l := range m.GetLabel() {
				switch l.GetName() {
				case "phase":
					p.Phase = l.GetValue()
				case "step":
					p.Step = l.GetValue()
				}
			}
			p.Seconds = m.GetGauge().GetValue()
			out = append(out, p)
		}
	}
	slices.SortFunc(out, func(a, b shapedPhase) int {
		if c := strings.Compare(a.Phase, b.Phase); c != 0 {
			return c
		}
		return strings.Compare(a.Step, b.Step)
	})
	return out
}

// shapedReady reports whether GET /health says "ready":true, read inside the
// API envelope as scripts/deploy_wait_ready.py reads it.
func shapedReady(c *composed) (bool, error) {
	w := c.serve(http.MethodGet, "/health", nil)
	if w.Code != http.StatusOK {
		return false, fmt.Errorf("GET /health: %d %s", w.Code, w.Body.String())
	}
	var doc struct {
		Data struct {
			Ready bool `json:"ready"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &doc); err != nil {
		return false, fmt.Errorf("GET /health: %w: %s", err, w.Body.String())
	}
	return doc.Data.Ready, nil
}

// bootShapedStore boots the composition on the store copy at work as serve runs
// it (on disk, the stored settings loaded, the key the corpus was written with)
// and returns the boot's phase samples and the seconds from the start of the
// boot to "ready":true on /health.
func bootShapedStore(t *testing.T, work string, key []byte, wait time.Duration) ([]shapedPhase, float64) {
	t.Helper()
	stateDir := filepath.Join(work, "state")
	edit := func(c *config.Config) {
		c.DatabasePath = filepath.Join(work, "db", "audiobooks.pebble")
		c.RootDir = filepath.Join(work, "library")
		// The corpus was written by an R0 that served the ABS surface (the
		// boot spec's ABS_API_ENABLED, ABS_AUTH_MODES and ABS_JWT_SECRET): the
		// stored settings say so, and a boot that names it differently is
		// refused (errABSFlagDrift).
		c.ABSAPIEnabled = true
		c.ABSAuthModes = "jwt"
		c.ABSJWTSecret = "synthetic-shaped-store-secret-0123456789abcdef"
	}
	opt := func(o *app.Options) {
		o.StorageFS = storage.FS{}
		loadConfigFromDB = config.LoadConfigFromDatabase
		// The harness writes its own key into the state directory; the corpus'
		// secrets are encrypted under the one stage 1 holds.
		require.NoError(t, os.WriteFile(database.EncryptionKeyPath(stateDir), key, 0o600))
	}
	start := time.Now()
	r := runComposedIn(t, stateDir, edit, nil, opt, nil)
	var ready bool
	for deadline := start.Add(wait); ; time.Sleep(50 * time.Millisecond) {
		var err error
		ready, err = shapedReady(r.composed)
		require.NoError(t, err)
		if ready {
			break
		}
		require.Truef(t, time.Now().Before(deadline), "/health never reported ready within %s", wait)
	}
	seconds := time.Since(start).Seconds()
	phases := shapedBootPhases(t, r.app)
	require.NoError(t, r.stop())
	return phases, seconds
}

// TestProductionShapedStore_BootTime boots the current build on a copy of the
// production-shaped store (R2_SHAPED_STORE), after checking the copy against
// store_manifest.json so the boot never runs on a partial store, and writes
// $R2_SHAPED_OUT/boot_time.json.
func TestProductionShapedStore_BootTime(t *testing.T) {
	src := os.Getenv("R2_SHAPED_STORE")
	if src == "" {
		t.Skip("R2_SHAPED_STORE is not set: no production-shaped store to boot (CI generates one; see scripts/r2corpus/gen_production_shaped.py)")
	}
	out := os.Getenv("R2_SHAPED_OUT")
	require.NotEmpty(t, out, "R2_SHAPED_OUT: where boot_time.json goes")
	require.NoError(t, os.MkdirAll(out, 0o755))
	wait := shapedDefaultWait
	if v := os.Getenv("R2_SHAPED_READY_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		require.NoError(t, err, "R2_SHAPED_READY_TIMEOUT")
		wait = d
	}

	var entries []r2ManifestEntry
	raw, err := os.ReadFile(filepath.Join(src, shapedStoreManifest))
	require.NoError(t, err, "the generator writes %s beside the store", shapedStoreManifest)
	require.NoError(t, json.Unmarshal(raw, &entries))
	require.NotEmpty(t, entries, shapedStoreManifest)

	work := t.TempDir()
	require.NoError(t, copyShapedStore(src, work))
	verifyStoreFiles(t, work, entries)
	if t.Failed() {
		t.Fatal("the copy does not match store_manifest.json: not booting on a partial store")
	}
	key, err := os.ReadFile(database.EncryptionKeyPath(filepath.Join(work, "state")))
	require.NoError(t, err, "the store holds no settings key")

	phases, ready := bootShapedStore(t, work, key, wait)
	_, steady := bootShapedStore(t, work, key, wait)

	assert.True(t, slices.ContainsFunc(phases, func(p shapedPhase) bool { return p.Phase == "P4b" }),
		"no P4b sample on kernel_boot_phase_seconds: %v", phases)
	for _, p := range phases {
		assert.GreaterOrEqual(t, p.Seconds, 0.0, "%s/%s", p.Phase, p.Step)
	}
	assert.Greater(t, ready, 0.0)
	assert.Greater(t, steady, 0.0)

	doc, err := json.MarshalIndent(shapedBootTime{Phases: phases, ReadySeconds: ready, SteadyReadySeconds: steady}, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(out, shapedBootTimeFile), append(doc, '\n'), 0o644))
	t.Logf("first boot ready in %.1fs, steady boot %.1fs, %d phase samples", ready, steady, len(phases))
}

// copyShapedStore leaves store_manifest.json behind, links tables and copies
// everything else, so a boot's writes never reach the generated store.
func TestCopyShapedStore_LinksTablesAndLeavesTheManifestBehind(t *testing.T) {
	src, dst := t.TempDir(), filepath.Join(t.TempDir(), "work")
	write := func(rel, body string) {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(src, rel)), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(src, rel), []byte(body), 0o644))
	}
	write("db/audiobooks.pebble/000016.sst", "table")
	write("db/audiobooks.pebble/000014.log", "wal")
	write("state/.encryption_key", "key")
	write(shapedStoreManifest, "[]")
	require.NoError(t, copyShapedStore(src, dst))

	_, err := os.Stat(filepath.Join(dst, shapedStoreManifest))
	assert.ErrorIs(t, err, fs.ErrNotExist, "store_manifest.json was copied")
	for rel, body := range map[string]string{
		"db/audiobooks.pebble/000016.sst": "table", "db/audiobooks.pebble/000014.log": "wal", "state/.encryption_key": "key",
	} {
		got, err := os.ReadFile(filepath.Join(dst, rel))
		require.NoError(t, err, rel)
		assert.Equal(t, body, string(got), rel)
	}
	// Appending to a copied WAL leaves the generated store alone.
	f, err := os.OpenFile(filepath.Join(dst, "db/audiobooks.pebble/000014.log"), os.O_APPEND|os.O_WRONLY, 0)
	require.NoError(t, err)
	_, err = f.WriteString("-boot")
	require.NoError(t, err)
	require.NoError(t, f.Close())
	got, err := os.ReadFile(filepath.Join(src, "db/audiobooks.pebble/000014.log"))
	require.NoError(t, err)
	assert.Equal(t, "wal", string(got), "a boot's write reached the generated store")
}
