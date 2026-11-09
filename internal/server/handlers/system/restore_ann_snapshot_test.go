// file: internal/server/handlers/system/restore_ann_snapshot_test.go
// version: 1.0.8
// guid: b3587e2b-0397-4f38-8a01-ae6bdd068607
// last-edited: 2026-11-08

package system_test

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// R1-15 round-2 review (B1): a restore writes Pebble files behind the GatedDB
// the HNSW store counts writes through, so the handler marks the live store
// diverged and removes every clean-stop marker that could vouch for the old
// graph. Round 3 (B1-r3) moved the mark before the first write:
// TestRestoreBackup_MarksTheStoreBeforeTheFirstWrite. The server half (the
// wiring, and a quiet stop writing no marker) is
// TestRestoreBackup_NoCleanStopMarkerAfterARestore in package server.

const testCleanStopMarker = `{"version":1,"model":"bge-m3","at":"2026-11-08T00:00:00Z"}`

type tarEntry struct{ name, body string }

// writeTar writes an uncompressed tar archive: the restore sniffs the codec
// from the bytes and falls back to plain tar. A name that ends in a slash is
// written as a directory.
func writeTar(t *testing.T, path string, entries []tarEntry) {
	t.Helper()
	f, err := os.Create(path)
	require.NoError(t, err)
	tw := tar.NewWriter(f)
	for _, e := range entries {
		hdr := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if strings.HasSuffix(e.name, "/") {
			hdr = &tar.Header{Name: e.name, Mode: 0o755, Typeflag: tar.TypeDir}
		}
		require.NoError(t, tw.WriteHeader(hdr))
		_, err := tw.Write([]byte(e.body))
		require.NoError(t, err)
	}
	require.NoError(t, tw.Close())
	require.NoError(t, f.Close())
}

func plantMarker(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "hnsw.clean-stop"), []byte(testCleanStopMarker), 0o644))
}

func markerPresent(t *testing.T, dir string) bool {
	t.Helper()
	_, err := os.Stat(filepath.Join(dir, "hnsw.clean-stop"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("stat marker: %v", err)
	}
	return err == nil
}

func TestRestoreBackup_DistrustsTheHNSWSnapshot(t *testing.T) {
	cases := []struct {
		name    string
		entries []tarEntry
		// request names another backup than the one written, when set.
		request string
		verify  bool
		sidecar string
		// custom restores into root/gotham instead of the live database's
		// directory.
		custom    bool
		plantLive bool
		// noBackupDir points the backup directory at one that does not exist.
		noBackupDir bool
		// link names a symlink beside the live database that points at its
		// snapshot directory, made before the restore.
		link string
		// wantAbsent lists paths under root no entry may have written.
		wantAbsent []string
		// refused: the restore wrote nothing, so the snapshot keeps its trust
		// and a marker planted beside the live database stays.
		refused    bool
		wantStatus int
	}{
		{
			// The production default: the restore overwrites the live database.
			name:       "default target",
			entries:    []tarEntry{{"audiobooks.pebble/MANIFEST-000001", "batcave"}},
			plantLive:  true,
			wantStatus: http.StatusOK,
		},
		{
			name:       "custom target",
			entries:    []tarEntry{{"audiobooks.pebble/MANIFEST-000001", "batcave"}},
			custom:     true,
			plantLive:  true,
			wantStatus: http.StatusOK,
		},
		{
			// tar -C <dir> -cf <archive> . writes the target itself first, as
			// "./": the restored database is still under the custom target,
			// and so is the snapshot directory whose marker must go.
			name: "custom target, an archive whose first entry is the target itself",
			entries: []tarEntry{
				{"./", ""},
				{"./audiobooks.pebble/MANIFEST-000001", "batcave"},
			},
			custom:     true,
			plantLive:  true,
			wantStatus: http.StatusOK,
		},
		{
			// The extraction writes a database file, then fails on an entry
			// whose parent is that file: files are on disk although the
			// restore failed.
			name: "the extraction fails partway",
			entries: []tarEntry{
				{"audiobooks.pebble/MANIFEST-000001", "batcave"},
				{"audiobooks.pebble/MANIFEST-000001/joker", "hahaha"},
			},
			plantLive:  true,
			wantStatus: http.StatusInternalServerError,
		},
		{
			// Round 4 (B1-r4): an entry that climbs out of the target is
			// refused before anything is written, wherever it lands.
			name: "refused: an entry climbs out of the target",
			entries: []tarEntry{
				{"audiobooks.pebble/MANIFEST-000001", "batcave"},
				{"../joker", "hahaha"},
			},
			plantLive:  true,
			refused:    true,
			wantStatus: http.StatusBadRequest,
		},
		{
			// Entry names cannot see a symlink already in the target, so the
			// extraction also checks where each entry lands and refuses one
			// inside a snapshot directory, unwritten. The entry is not the
			// marker, so the deferred marker removal cannot be what keeps
			// the directory clean. The prepare step's 400 for the same
			// refusal (round 8, N1).
			name: "a symlink in the target routes an entry into the snapshot directory",
			entries: []tarEntry{
				{"audiobooks.pebble/MANIFEST-000001", "batcave"},
				{"wayne-manor/hnsw.graph", "a graph from somewhere else"},
			},
			link:       "wayne-manor",
			wantAbsent: []string{"hnsw/hnsw.graph"},
			wantStatus: http.StatusBadRequest,
		},
		{
			// Round 4: a real backup never holds the snapshot directory, so
			// an archive that does is refused before it writes anything
			// (TestRestoreBackup_RefusesAnArchiveCarryingTheHNSWSnapshot).
			name: "refused: the archive carries a marker",
			entries: []tarEntry{
				{"audiobooks.pebble/MANIFEST-000001", "batcave"},
				{"hnsw/hnsw.clean-stop", testCleanStopMarker},
			},
			plantLive:  true,
			refused:    true,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "refused: the backup is not there",
			entries:    []tarEntry{{"audiobooks.pebble/MANIFEST-000001", "batcave"}},
			request:    "year-one.tar",
			plantLive:  true,
			refused:    true,
			wantStatus: http.StatusInternalServerError,
		},
		{
			name:        "refused: the backup directory cannot be read",
			entries:     []tarEntry{{"audiobooks.pebble/MANIFEST-000001", "batcave"}},
			noBackupDir: true,
			plantLive:   true,
			refused:     true,
			wantStatus:  http.StatusInternalServerError,
		},
		{
			name:       "refused: no checksum to verify against",
			entries:    []tarEntry{{"audiobooks.pebble/MANIFEST-000001", "batcave"}},
			verify:     true,
			plantLive:  true,
			refused:    true,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "refused: the checksum does not match",
			entries:    []tarEntry{{"audiobooks.pebble/MANIFEST-000001", "batcave"}},
			verify:     true,
			sidecar:    "00  the-killing-joke.tar\n",
			plantLive:  true,
			refused:    true,
			wantStatus: http.StatusConflict,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			dbPath := filepath.Join(root, "audiobooks.pebble")
			require.NoError(t, os.MkdirAll(dbPath, 0o755))
			prevDB, prevBackup := config.AppConfig.DatabasePath, config.AppConfig.BackupDir
			config.AppConfig.DatabasePath = dbPath
			config.AppConfig.BackupDir = filepath.Join(root, "backups")
			t.Cleanup(func() { config.AppConfig.DatabasePath, config.AppConfig.BackupDir = prevDB, prevBackup })
			require.NoError(t, os.MkdirAll(config.AppConfig.BackupDir, 0o755))
			archive := filepath.Join(config.AppConfig.BackupDir, "the-killing-joke.tar")
			writeTar(t, archive, tc.entries)
			if tc.sidecar != "" {
				require.NoError(t, os.WriteFile(archive+".sha256", []byte(tc.sidecar), 0o644))
			}
			if tc.noBackupDir {
				config.AppConfig.BackupDir = filepath.Join(root, "arkham")
			}

			req := map[string]any{"backup_filename": "the-killing-joke.tar", "verify": tc.verify}
			if tc.request != "" {
				req["backup_filename"] = tc.request
			}
			liveDir := filepath.Join(root, "hnsw")
			restoredDir := liveDir
			if tc.custom {
				target := filepath.Join(root, "gotham")
				restoredDir = filepath.Join(target, "hnsw")
				plantMarker(t, restoredDir)
				req["target_path"] = target
			}
			if tc.plantLive {
				plantMarker(t, liveDir)
			}
			if tc.link != "" {
				require.NoError(t, os.MkdirAll(liveDir, 0o755))
				require.NoError(t, os.Symlink(liveDir, filepath.Join(root, tc.link)))
			}
			body, err := json.Marshal(req)
			require.NoError(t, err)

			h, _ := newTestHandler(t)
			var reasons []string
			h.WithANNSnapshotDivergence(func(reason string) { reasons = append(reasons, reason) })
			w := run(http.MethodPost, "/backup/restore", "/backup/restore", body, func(r *gin.Engine) {
				r.POST("/backup/restore", h.RestoreBackup)
			})

			require.Equal(t, tc.wantStatus, w.Code, w.Body.String())
			for _, rel := range tc.wantAbsent {
				_, err := os.Stat(filepath.Join(root, rel))
				assert.True(t, os.IsNotExist(err), "%s was written (stat: %v)", rel, err)
			}
			if tc.refused {
				assert.Empty(t, reasons, "a restore that wrote nothing must not mark the store diverged")
				assert.True(t, markerPresent(t, liveDir), "a restore that wrote nothing must leave the marker")
				return
			}
			require.Len(t, reasons, 1, "the live store must be marked diverged once")
			assert.Contains(t, reasons[0], "restore")
			assert.False(t, markerPresent(t, liveDir), "no marker may vouch for the live database's snapshot")
			assert.False(t, markerPresent(t, restoredDir), "no marker may vouch for the restored database's snapshot")
		})
	}
}

// stopReadyStore returns an HNSW store whose MarkCleanStop would write a
// marker in dir: complete, exported to dir, and bound to a database of its own
// that no write has touched.
func stopReadyStore(t *testing.T, dir string) *database.HNSWEmbeddingStore {
	t.Helper()
	s := database.NewHNSWEmbeddingStore(4)
	require.NoError(t, s.Upsert(context.Background(), "book", "the-long-halloween", []float32{1, 0, 0, 0}, nil))
	s.SetComplete(true)
	ps, err := database.NewPebbleStoreInMemory("restore-truth")
	require.NoError(t, err)
	t.Cleanup(func() { _ = ps.Close() })
	s.BindTruthSource(ps.DB(), "")
	require.NoError(t, s.Export(dir))
	return s
}

// TestRestoreBackup_MarksTheStoreBeforeTheFirstWrite (R1-15 round-3 review,
// B1-r3): the kernel's drain does not wait for handler goroutines, so a stop
// can run MarkCleanStop while a restore is extracting, and the process can
// exit before the handler returns. The live store must therefore be marked
// diverged before the restore writes anything, not after: a stop that lands
// at that point, or anywhere after it, refuses the marker.
func TestRestoreBackup_MarksTheStoreBeforeTheFirstWrite(t *testing.T) {
	entries := []tarEntry{
		{"audiobooks.pebble/MANIFEST-000001", "batcave"},
		{"audiobooks.pebble/OPTIONS-000002", "alfred"},
	}

	// The control: with no restore, the same store writes its marker, so the
	// refusal below is the restore's doing and not the fixture's.
	t.Run("no restore", func(t *testing.T) {
		snap := t.TempDir()
		written, reason, err := stopReadyStore(t, snap).MarkCleanStop(snap)
		require.NoError(t, err)
		require.True(t, written, "the fixture must be a store a stop lets write a marker: %s", reason)
	})

	t.Run("a stop during the restore", func(t *testing.T) {
		root := t.TempDir()
		dbPath := filepath.Join(root, "audiobooks.pebble")
		require.NoError(t, os.MkdirAll(dbPath, 0o755))
		prevDB, prevBackup := config.AppConfig.DatabasePath, config.AppConfig.BackupDir
		config.AppConfig.DatabasePath = dbPath
		config.AppConfig.BackupDir = filepath.Join(root, "backups")
		t.Cleanup(func() { config.AppConfig.DatabasePath, config.AppConfig.BackupDir = prevDB, prevBackup })
		require.NoError(t, os.MkdirAll(config.AppConfig.BackupDir, 0o755))
		writeTar(t, filepath.Join(config.AppConfig.BackupDir, "a-death-in-the-family.tar"), entries)

		snap := filepath.Join(root, "stop-snapshot")
		store := stopReadyStore(t, snap)

		var onDiskAtMark []string
		var written bool
		var reason string
		fired := 0
		h, _ := newTestHandler(t)
		h.WithANNSnapshotDivergence(func(r string) {
			fired++
			for _, e := range entries {
				if _, err := os.Stat(filepath.Join(root, e.name)); err == nil {
					onDiskAtMark = append(onDiskAtMark, e.name)
				}
			}
			store.MarkDiverged(r)
			// A stop that lands now, while the extraction is still to run.
			var err error
			written, reason, err = store.MarkCleanStop(snap)
			require.NoError(t, err)
		})
		body, err := json.Marshal(map[string]any{"backup_filename": "a-death-in-the-family.tar"})
		require.NoError(t, err)
		w := run(http.MethodPost, "/backup/restore", "/backup/restore", body, func(r *gin.Engine) {
			r.POST("/backup/restore", h.RestoreBackup)
		})

		require.Equal(t, http.StatusOK, w.Code, w.Body.String())
		require.Equal(t, 1, fired, "the live store must be marked diverged once")
		assert.Empty(t, onDiskAtMark, "the store must be marked diverged before the restore writes its first file")
		assert.False(t, written, "a stop after the mark must refuse the marker")
		assert.Contains(t, reason, "restore")
		assert.False(t, markerPresent(t, snap), "no marker may vouch for the graph while the restore writes")
	})
}

// restoreProbe is a restore that must be refused before it writes anything
// into, or marks, the live database's HNSW snapshot directory.
type restoreProbe struct {
	name string
	// setup, when set, prepares root (the live database's directory, whose
	// snapshot directory exists) before the restore; it may skip the test.
	setup func(t *testing.T, root string)
	// entries builds the archive; root is the live database's directory.
	entries func(root string) []tarEntry
	// target returns the request's target_path; nil means the default.
	target func(root string) string
	// marked: the restore is refused inside the extraction, after the
	// divergence mark, so it is marked once; otherwise it is not marked.
	marked     bool
	wantStatus int
}

// runRestoreProbe runs p, verified and not, and checks the refusal: the
// status, the divergence mark (none unless p.marked), no marker in the live
// snapshot directory, and no entry written where it would have landed.
func runRestoreProbe(t *testing.T, p restoreProbe) {
	for _, verify := range []bool{false, true} {
		name := p.name + "/unverified"
		if verify {
			name = p.name + "/verified"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			dbPath := filepath.Join(root, "audiobooks.pebble")
			prevDB, prevBackup := config.AppConfig.DatabasePath, config.AppConfig.BackupDir
			config.AppConfig.DatabasePath = dbPath
			config.AppConfig.BackupDir = filepath.Join(root, "backups")
			t.Cleanup(func() { config.AppConfig.DatabasePath, config.AppConfig.BackupDir = prevDB, prevBackup })
			require.NoError(t, os.MkdirAll(config.AppConfig.BackupDir, 0o755))
			// The live server persists its graph here, so it exists.
			snapshotDir := database.HNSWSnapshotDir(dbPath)
			require.NoError(t, os.MkdirAll(snapshotDir, 0o755))
			if p.setup != nil {
				p.setup(t, root)
			}

			entries := p.entries(root)
			archive := filepath.Join(config.AppConfig.BackupDir, "hush.tar")
			writeTar(t, archive, entries)
			if verify {
				raw, err := os.ReadFile(archive)
				require.NoError(t, err)
				sum := sha256.Sum256(raw)
				require.NoError(t, os.WriteFile(archive+".sha256", []byte(hex.EncodeToString(sum[:])+"  hush.tar\n"), 0o644))
			}
			req := map[string]any{"backup_filename": "hush.tar", "verify": verify}
			target := root
			if p.target != nil {
				target = p.target(root)
				req["target_path"] = target
			}

			h, _ := newTestHandler(t)
			var reasons []string
			h.WithANNSnapshotDivergence(func(reason string) { reasons = append(reasons, reason) })
			body, err := json.Marshal(req)
			require.NoError(t, err)
			w := run(http.MethodPost, "/backup/restore", "/backup/restore", body, func(r *gin.Engine) {
				r.POST("/backup/restore", h.RestoreBackup)
			})

			require.Equal(t, p.wantStatus, w.Code, w.Body.String())
			if p.marked {
				assert.Len(t, reasons, 1, "a restore refused inside the extraction is marked once")
			} else {
				assert.Empty(t, reasons, "a refused restore must not mark the store diverged")
			}
			assert.False(t, markerPresent(t, snapshotDir), "a refused restore wrote a marker into the live snapshot directory")
			for _, e := range entries {
				_, err := os.Stat(filepath.Join(target, e.name))
				assert.True(t, os.IsNotExist(err), "%s was written by a refused restore (stat: %v)", e.name, err)
			}
		})
	}
}

// TestRestoreBackup_RefusesAnArchiveCarryingTheHNSWSnapshot (R1-15 round 4):
// a backup holds only the database directory, and the HNSW snapshot directory
// sits beside it, so an archive with an entry there is not a real backup.
// Extracting it could plant a clean-stop marker that a crash mid-restore
// leaves vouching for a graph the restored database does not match. The
// restore refuses it before writing anything, so the refusal costs nothing:
// no file, no divergence mark. Each probe below is one the round-4
// verification review reproduced or named.
func TestRestoreBackup_RefusesAnArchiveCarryingTheHNSWSnapshot(t *testing.T) {
	probes := []restoreProbe{
		{
			// The entry is named from database.HNSWSnapshotDir, which pins the
			// backup package's copy of the directory name to the real one.
			name: "by name",
			entries: func(root string) []tarEntry {
				snap := filepath.Base(database.HNSWSnapshotDir(filepath.Join(root, "audiobooks.pebble")))
				return []tarEntry{
					{"audiobooks.pebble/MANIFEST-000001", "batcave"},
					{snap + "/hnsw.clean-stop", testCleanStopMarker},
					{"audiobooks.pebble/OPTIONS-000002", "alfred"},
				}
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			// B1-r4: the bare name cleans to "../<base>/hnsw/...", which the
			// round-4 check did not count as the snapshot directory, while
			// the extraction joins it to the target and lands in it.
			name: "climbing out of the target and back in",
			entries: func(root string) []tarEntry {
				return []tarEntry{
					{"audiobooks.pebble/MANIFEST-000001", "batcave"},
					{"x/../../" + filepath.Base(root) + "/hnsw/hnsw.clean-stop", testCleanStopMarker},
					{"audiobooks.pebble/OPTIONS-000002", "alfred"},
				}
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			// SHOULD-FIX 1: a target_path above the live snapshot directory
			// reaches it with an ordinary name, no "..".
			name: "a target above the snapshot directory",
			entries: func(root string) []tarEntry {
				return []tarEntry{
					{"audiobooks.pebble/MANIFEST-000001", "batcave"},
					{filepath.Base(root) + "/hnsw/hnsw.clean-stop", testCleanStopMarker},
				}
			},
			target:     func(root string) string { return filepath.Dir(root) },
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "the snapshot directory as the target",
			entries: func(string) []tarEntry {
				return []tarEntry{{"hnsw.clean-stop", testCleanStopMarker}}
			},
			target: func(root string) string {
				return database.HNSWSnapshotDir(filepath.Join(root, "audiobooks.pebble"))
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "a target inside the snapshot directory",
			entries: func(string) []tarEntry {
				return []tarEntry{{"audiobooks.pebble/MANIFEST-000001", "batcave"}}
			},
			target: func(root string) string {
				return filepath.Join(database.HNSWSnapshotDir(filepath.Join(root, "audiobooks.pebble")), "gotham")
			},
			wantStatus: http.StatusBadRequest,
		},
		{
			// SHOULD-FIX 2: an archive with no entries restores nothing, so
			// it is refused before the mark rather than marked for nothing.
			// A 500, like a truncated archive: the file is damaged, not
			// crafted.
			name:       "an empty archive",
			entries:    func(string) []tarEntry { return nil },
			wantStatus: http.StatusInternalServerError,
		},
	}
	for _, p := range probes {
		runRestoreProbe(t, p)
	}
}

// symlinks makes each link (relative to root) a symlink to its target.
func symlinks(t *testing.T, root string, links map[string]string) {
	t.Helper()
	for link, target := range links {
		require.NoError(t, os.MkdirAll(filepath.Dir(filepath.Join(root, link)), 0o755))
		require.NoError(t, os.Symlink(target, filepath.Join(root, link)))
	}
}

// skipUnlessFolded skips the test unless the filesystem opens root/hnsw
// (the live snapshot directory, which exists) under alias. It asks the
// filesystem, per alias, rather than guessing from GOOS.
func skipUnlessFolded(t *testing.T, root, alias string) {
	t.Helper()
	want, err := os.Stat(filepath.Join(root, "hnsw"))
	require.NoError(t, err)
	if got, err := os.Stat(filepath.Join(root, alias)); err != nil || !os.SameFile(got, want) {
		t.Skipf("the filesystem does not fold %q to hnsw", alias)
	}
}

// TestRestoreBackup_ComparesDirectoriesByIdentity (R1-15 round-5
// verification review): the round-5 checks compared path strings, which
// the filesystem does not. B1-r5: a relative symlink resolves from its
// physical parent, not from the path that reached it, so each of the
// reviewer's three shapes wrote the live marker; they are now refused inside
// the extraction, after the mark: each destination is a symlink that does
// not resolve, a 409 since round 8 resolves the whole path (B1-r8), where
// writeEntry's refusal to write through a symlink gave a 500. S1-r5: a
// case-insensitive filesystem opens the snapshot
// directory under HNSW or hnſw; the target check now compares directories by
// identity and refuses those with a 400.
func TestRestoreBackup_ComparesDirectoriesByIdentity(t *testing.T) {
	marker := func(name string) func(string) []tarEntry {
		return func(string) []tarEntry { return []tarEntry{{name, testCleanStopMarker}} }
	}
	probes := []restoreProbe{
		{
			name: "a relative symlink resolved from its physical parent",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, "x", "y"), 0o755))
				symlinks(t, root, map[string]string{
					"lnk":   filepath.Join("x", "y"),
					"x/y/m": filepath.Join("..", "..", "hnsw", "hnsw.clean-stop"),
				})
			},
			entries:    marker("lnk/m"),
			marked:     true,
			wantStatus: http.StatusConflict,
		},
		{
			name: "a relative symlink under a custom target",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, "gotham", "x", "y"), 0o755))
				symlinks(t, root, map[string]string{
					"gotham/lnk":   filepath.Join("x", "y"),
					"gotham/x/y/m": filepath.Join("..", "..", "..", "hnsw", "hnsw.clean-stop"),
				})
			},
			entries:    marker("lnk/m"),
			target:     func(root string) string { return filepath.Join(root, "gotham") },
			marked:     true,
			wantStatus: http.StatusConflict,
		},
		{
			name: "a target that is a symlink",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, "x", "y"), 0o755))
				symlinks(t, root, map[string]string{
					"arkham": filepath.Join("x", "y"),
					"x/y/m":  filepath.Join("..", "..", "hnsw", "hnsw.clean-stop"),
				})
			},
			entries:    marker("m"),
			target:     func(root string) string { return filepath.Join(root, "arkham") },
			marked:     true,
			wantStatus: http.StatusConflict,
		},
		{
			name:       "the snapshot directory as the target, in another case",
			setup:      func(t *testing.T, root string) { skipUnlessFolded(t, root, "HNSW") },
			entries:    marker("hnsw.clean-stop"),
			target:     func(root string) string { return filepath.Join(root, "HNSW") },
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "the snapshot directory as the target, with a long s",
			setup:      func(t *testing.T, root string) { skipUnlessFolded(t, root, "hn\u017fw") },
			entries:    marker("hnsw.clean-stop"),
			target:     func(root string) string { return filepath.Join(root, "hn\u017fw") },
			wantStatus: http.StatusBadRequest,
		},
	}
	for _, p := range probes {
		runRestoreProbe(t, p)
	}
}

// danglingLiveSnapshot replaces the live snapshot directory, which
// runRestoreProbe makes, with a relative symlink to root/joker, which does
// not exist.
func danglingLiveSnapshot(t *testing.T, root string) {
	t.Helper()
	require.NoError(t, os.Remove(filepath.Join(root, "hnsw")))
	symlinks(t, root, map[string]string{"hnsw": "joker"})
}

// TestRestoreBackup_RefusesAnUnresolvableSnapshotDirectory (R1-15 round-6
// verification review, B1-r6): a snapshot directory whose path is a
// symlink that does not resolve has no identity to compare with, and an
// entry could create the symlink's target, which is the snapshot directory
// from then on: the reviewer's probe wrote a vouching marker there that a
// crash during the extraction would have left. The restore checks both
// snapshot directories before it marks the store or writes anything, and
// refuses with a 409: the server's directory, not the request, is what
// stands in the way. A custom target that cannot be compared with the live
// snapshot directory for the same reason is refused before the archive is
// read.
func TestRestoreBackup_RefusesAnUnresolvableSnapshotDirectory(t *testing.T) {
	probes := []restoreProbe{
		{
			// The reviewer's handler probe, without its named pipe: the
			// pipe held the extraction so the probe could see the marker,
			// and the extraction no longer starts.
			name:  "a dangling live snapshot directory",
			setup: danglingLiveSnapshot,
			entries: func(string) []tarEntry {
				return []tarEntry{
					{"audiobooks.pebble/MANIFEST-000000", "batcave"},
					{"joker/hnsw.clean-stop", testCleanStopMarker},
					{"audiobooks.pebble/OPTIONS-000002", "bane"},
					{"audiobooks.pebble/MANIFEST-000001", "batcave"},
				}
			},
			wantStatus: http.StatusConflict,
		},
		{
			name:  "a custom target where the dangling live snapshot directory points",
			setup: danglingLiveSnapshot,
			entries: func(string) []tarEntry {
				return []tarEntry{
					{"audiobooks.pebble/MANIFEST-000001", "batcave"},
					{"hnsw.clean-stop", testCleanStopMarker},
				}
			},
			target:     func(root string) string { return filepath.Join(root, "joker") },
			wantStatus: http.StatusConflict,
		},
		{
			name: "a dangling restored snapshot directory",
			setup: func(t *testing.T, root string) {
				require.NoError(t, os.MkdirAll(filepath.Join(root, "gotham"), 0o755))
				symlinks(t, root, map[string]string{"gotham/hnsw": "joker"})
			},
			entries: func(string) []tarEntry {
				return []tarEntry{
					{"audiobooks.pebble/MANIFEST-000001", "batcave"},
					{"joker/hnsw.clean-stop", testCleanStopMarker},
				}
			},
			target:     func(root string) string { return filepath.Join(root, "gotham") },
			wantStatus: http.StatusConflict,
		},
	}
	for _, p := range probes {
		runRestoreProbe(t, p)
	}
}
