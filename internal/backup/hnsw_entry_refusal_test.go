// file: internal/backup/hnsw_entry_refusal_test.go
// version: 1.0.4
// guid: 9cd75ebf-5ec4-40ed-8d74-b6dd4f2da595
// last-edited: 2026-11-08

package backup

import (
	"archive/tar"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// R1-15 round-4 (the archive residual): a backup holds one root, the database
// directory (addToArchive), and the HNSW snapshot directory sits beside the
// database, not in it. An archive entry in that directory is therefore never
// from a real backup, and extracting one could plant a clean-stop marker that
// vouches for a graph the restored database does not match. PrepareRestoreIn
// refuses such an archive in the same read that verifies it, before anything
// is written; extractArchive refuses the entry too, without writing it.

// writeArchive writes a tar archive of regular files through codec.
func writeArchive(t *testing.T, path string, codec Codec, entries [][2]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	cw, err := codec.NewWriter(f, 0)
	if err != nil {
		t.Fatal(err)
	}
	tw := tar.NewWriter(cw)
	for _, e := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: e[0], Mode: 0o644, Size: int64(len(e[1])), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e[1])); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := cw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
}

// incompressible returns n bytes no codec shrinks, so a compressed archive
// spans many blocks and a decompressor reads ahead of the tar reader.
func incompressible(n int) string {
	b := make([]byte, n)
	x := uint32(0x5ca1ab1e)
	for i := range b {
		x = x*1664525 + 1013904223
		b[i] = byte(x >> 24)
	}
	return string(b)
}

// sealArchive writes the checksum sidecar CreateBackup would write for path.
func sealArchive(t *testing.T, path string) {
	t.Helper()
	sum, err := calculateFileChecksum(path, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeChecksumSidecar(path, sum); err != nil {
		t.Fatal(err)
	}
}

func TestEntryRefusal(t *testing.T) {
	cases := map[string]error{
		"hnsw/hnsw.clean-stop":                  ErrArchiveCarriesHNSWSnapshot,
		"hnsw/graph.bin":                        ErrArchiveCarriesHNSWSnapshot,
		"hnsw":                                  ErrArchiveCarriesHNSWSnapshot,
		"hnsw/":                                 ErrArchiveCarriesHNSWSnapshot,
		"/hnsw/hnsw.clean-stop":                 ErrArchiveCarriesHNSWSnapshot,
		"./hnsw/hnsw.clean-stop":                ErrArchiveCarriesHNSWSnapshot,
		"audiobooks.pebble/../hnsw/x":           ErrArchiveCarriesHNSWSnapshot,
		"audiobooks.pebble/./../hnsw/graph.bin": ErrArchiveCarriesHNSWSnapshot,
		"HNSW/hnsw.clean-stop":                  ErrArchiveCarriesHNSWSnapshot,
		"audiobooks.pebble/hnsw/x":              ErrArchiveCarriesHNSWSnapshot, // any depth (round 8, N2)
		// Climbing out is refused before the snapshot check, and whatever
		// joining the name to the target would give: the round-4 review's
		// probe lands in the snapshot directory of a target named arkham.
		"../hnsw/hnsw.clean-stop":             ErrArchiveEntryEscapes,
		"audiobooks.pebble/../../hnsw/x":      ErrArchiveEntryEscapes,
		"x/../../arkham/hnsw/hnsw.clean-stop": ErrArchiveEntryEscapes,
		"..":                                  ErrArchiveEntryEscapes,
		"../":                                 ErrArchiveEntryEscapes,
		"/../joker":                           ErrArchiveEntryEscapes,
		"audiobooks.pebble/MANIFEST-000001":   nil,
		"audiobooks.pebble/hnsw.clean-stop":   nil,
		"hnsw-old/hnsw.clean-stop":            nil,
		"hnswx":                               nil,
		"..batcave/x":                         nil,
		"...":                                 nil,
		"audiobooks.pebble/..":                nil,
		"":                                    nil,
	}
	for name, want := range cases {
		err := entryRefusal(name)
		if want == nil {
			if err != nil {
				t.Errorf("entryRefusal(%q) = %v, want nil", name, err)
			}
			continue
		}
		if !errors.Is(err, want) {
			t.Errorf("entryRefusal(%q) = %v, want %v", name, err, want)
		}
	}
}

// TestPrepareRestoreIn_RefusesAClimbingEntry: an entry that climbs out of the
// target is refused before anything is written, including the round-4
// review's probe, which joins back into the snapshot directory of a target
// named arkham.
func TestPrepareRestoreIn_RefusesAClimbingEntry(t *testing.T) {
	for _, name := range []string{"../bane", "x/../../arkham/hnsw/hnsw.clean-stop"} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			archive := filepath.Join(dir, "no-mans-land.tar")
			writePlainTar(t, archive, [][2]string{
				{"audiobooks.pebble/MANIFEST-000001", "batcave"},
				{name, "venom"},
			})
			r, err := PrepareRestoreIn(dir, "no-mans-land.tar", false)
			if !errors.Is(err, ErrArchiveEntryEscapes) || r != nil {
				t.Fatalf("PrepareRestoreIn = %v, %v; want ErrArchiveEntryEscapes and no prepared restore", r, err)
			}
		})
	}
}

func TestPrepareRestoreIn_RefusesAnHNSWSnapshotEntry(t *testing.T) {
	marker := [2]string{"hnsw/hnsw.clean-stop", `{"version":1,"model":"bge-m3"}`}
	layouts := []struct {
		name    string
		entries [][2]string
	}{
		// The refusal comes mid-read, after a large entry: a verified
		// prepare must still hash every byte, or the checksum would mismatch
		// and hide the refusal.
		{"after a large entry", [][2]string{
			{"audiobooks.pebble/000001.sst", incompressible(1 << 20)},
			marker,
			{"audiobooks.pebble/MANIFEST-000001", "batcave"},
		}},
		// The refusal comes first, with most of the archive still unread:
		// zstd's decoder is reading ahead in its own goroutine when the scan
		// stops, so the drain that finishes the hash must wait for it (run
		// with -race).
		{"before a large entry", [][2]string{
			marker,
			{"audiobooks.pebble/000002.sst", incompressible(4 << 20)},
			{"audiobooks.pebble/MANIFEST-000001", "batcave"},
		}},
	}
	for _, layout := range layouts {
		for _, codec := range []Codec{gzipCodec{}, zstdCodec{}, noneCodec{}} {
			for _, verify := range []bool{false, true} {
				name := layout.name + "/" + codec.Name()
				if verify {
					name += "/verified"
				}
				t.Run(name, func(t *testing.T) {
					dir := t.TempDir()
					archive := filepath.Join(dir, "the-court-of-owls"+codec.Extension())
					writeArchive(t, archive, codec, layout.entries)
					if verify {
						sealArchive(t, archive)
					}
					r, err := PrepareRestoreIn(dir, filepath.Base(archive), verify)
					if !errors.Is(err, ErrArchiveCarriesHNSWSnapshot) || r != nil {
						t.Fatalf("PrepareRestoreIn = %v, %v; want ErrArchiveCarriesHNSWSnapshot and no prepared restore", r, err)
					}
				})
			}
		}
	}
}

// TestPrepareRestoreIn_VerifiesInTheScanRead: verification shares the read
// that scans the entries, so the checksum still covers every byte, a mismatch
// still wins over what the scan found, and an archive the scan cannot read to
// the end is refused before anything is written.
func TestPrepareRestoreIn_VerifiesInTheScanRead(t *testing.T) {
	entries := [][2]string{
		{"audiobooks.pebble/000001.sst", incompressible(1 << 20)},
		{"audiobooks.pebble/MANIFEST-000001", "batcave"},
	}
	for _, codec := range []Codec{gzipCodec{}, zstdCodec{}, noneCodec{}} {
		t.Run(codec.Name(), func(t *testing.T) {
			dir := t.TempDir()
			whole := filepath.Join(dir, "zero-year"+codec.Extension())
			writeArchive(t, whole, codec, entries)
			sealArchive(t, whole)
			if _, err := PrepareRestoreIn(dir, filepath.Base(whole), true); err != nil {
				t.Fatalf("a whole archive with its own checksum: %v", err)
			}

			raw, err := os.ReadFile(whole)
			if err != nil {
				t.Fatal(err)
			}
			// Cut inside the large entry: the scan fails partway.
			cut := filepath.Join(dir, "zero-year-cut"+codec.Extension())
			if err := os.WriteFile(cut, raw[:len(raw)/2], 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := PrepareRestoreIn(dir, filepath.Base(cut), false); err == nil {
				t.Error("unverified prepare of a cut archive succeeded; want it refused before any write")
			}
			// The cut archive's own checksum matches, so the scan's failure
			// is what the caller hears.
			sealArchive(t, cut)
			if _, err := PrepareRestoreIn(dir, filepath.Base(cut), true); err == nil || errors.Is(err, ErrChecksumMismatch) {
				t.Errorf("verified prepare of a cut archive with its own checksum = %v; want the scan's error, not a mismatch", err)
			}
			// The whole archive's checksum does not, and the mismatch wins.
			sidecar, err := os.ReadFile(checksumSidecarPath(whole))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(checksumSidecarPath(cut), sidecar, 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := PrepareRestoreIn(dir, filepath.Base(cut), true); !errors.Is(err, ErrChecksumMismatch) {
				t.Errorf("verified prepare of a cut archive with the whole one's checksum = %v; want ErrChecksumMismatch", err)
			}
		})
	}
}

// TestExtractArchive_RefusesAClimbingEntryUnwritten: the round-4 review's
// probe, extracted without the prepare (an archive that changed after it):
// the target is named arkham, so the entry would land in arkham/hnsw.
func TestExtractArchive_RefusesAClimbingEntryUnwritten(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "no-mans-land.tar")
	writePlainTar(t, archive, [][2]string{
		{"audiobooks.pebble/MANIFEST-000001", "batcave"},
		{"x/../../arkham/hnsw/hnsw.clean-stop", `{"version":1,"model":"bge-m3"}`},
	})
	target := filepath.Join(t.TempDir(), "arkham")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := extractArchive(archive, target, nil); !errors.Is(err, ErrArchiveEntryEscapes) {
		t.Fatalf("extractArchive = %v; want ErrArchiveEntryEscapes", err)
	}
	if _, err := os.Stat(filepath.Join(target, "hnsw")); !os.IsNotExist(err) {
		t.Errorf("the HNSW snapshot directory exists after the refusal (%v); the entry must not be written", err)
	}
}

// TestExtractArchive_RefusesAnHNSWSnapshotEntryUnwritten: PrepareRestoreIn
// refuses such an archive first, so this guards one that changed between the
// two reads. The entry is never written, whatever came before it.
func TestExtractArchive_RefusesAnHNSWSnapshotEntryUnwritten(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "under-the-hood.tar")
	writePlainTar(t, archive, [][2]string{
		{"audiobooks.pebble/MANIFEST-000001", "batcave"},
		{"hnsw/hnsw.clean-stop", `{"version":1,"model":"bge-m3"}`},
	})
	target := t.TempDir()
	if err := extractArchive(archive, target, nil); !errors.Is(err, ErrArchiveCarriesHNSWSnapshot) {
		t.Fatalf("extractArchive = %v; want ErrArchiveCarriesHNSWSnapshot", err)
	}
	if _, err := os.Stat(filepath.Join(target, "hnsw")); !os.IsNotExist(err) {
		t.Errorf("the HNSW snapshot directory exists after the refusal (%v); the entry must not be written", err)
	}
}

// TestExtractArchive_KeepsEntriesOutOfTheSnapshotDirs (R1-15 round-4
// verification review, SHOULD-FIX 1): entry names cannot keep an entry out
// of a snapshot directory the filesystem routes it into, so Extract checks
// where each entry lands, symlinks resolved, before it creates anything.
func TestExtractArchive_KeepsEntriesOutOfTheSnapshotDirs(t *testing.T) {
	type entry struct {
		name string
		dir  bool
	}
	cases := []struct {
		name string
		// setup prepares the filesystem and returns the target and the
		// snapshot directory; base is an empty directory.
		setup   func(t *testing.T, base string) (target, snap string)
		entries []entry
		// landed is where the refused entry would have been written,
		// relative to the snapshot directory.
		landed string
	}{
		{
			name: "a target above the snapshot directory",
			setup: func(t *testing.T, base string) (string, string) {
				return base, filepath.Join(base, "arkham", "hnsw")
			},
			entries: []entry{{name: "arkham/hnsw/hnsw.clean-stop"}},
			landed:  "hnsw.clean-stop",
		},
		{
			name: "the snapshot directory as the target",
			setup: func(t *testing.T, base string) (string, string) {
				snap := filepath.Join(base, "hnsw")
				return snap, snap
			},
			entries: []entry{{name: "hnsw.clean-stop"}},
			landed:  "hnsw.clean-stop",
		},
		{
			name: "a symlinked parent",
			setup: func(t *testing.T, base string) (string, string) {
				target, snap := filepath.Join(base, "gotham"), filepath.Join(base, "hnsw")
				mustMkdir(t, target, snap)
				mustSymlink(t, snap, filepath.Join(target, "wayne-manor"))
				return target, snap
			},
			entries: []entry{{name: "wayne-manor/graph.bin"}},
			landed:  "graph.bin",
		},
		{
			// A write through a dangling symlink creates its target.
			name: "a dangling symlink as the entry itself",
			setup: func(t *testing.T, base string) (string, string) {
				target, snap := filepath.Join(base, "gotham"), filepath.Join(base, "hnsw")
				mustMkdir(t, target, snap)
				mustSymlink(t, filepath.Join(snap, "hnsw.clean-stop"), filepath.Join(target, "alfred"))
				return target, snap
			},
			entries: []entry{{name: "alfred"}},
			landed:  "hnsw.clean-stop",
		},
		{
			// Checked before MkdirAll, which would create it.
			name: "a directory entry through a symlink",
			setup: func(t *testing.T, base string) (string, string) {
				target, snap := filepath.Join(base, "gotham"), filepath.Join(base, "hnsw")
				mustMkdir(t, target, snap)
				mustSymlink(t, snap, filepath.Join(target, "wayne-manor"))
				return target, snap
			},
			entries: []entry{{name: "wayne-manor/batcave/", dir: true}},
			landed:  "batcave",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target, snap := tc.setup(t, t.TempDir())
			archive := filepath.Join(t.TempDir(), "the-dark-knight.tar")
			f, err := os.Create(archive)
			if err != nil {
				t.Fatal(err)
			}
			tw := tar.NewWriter(f)
			for _, e := range tc.entries {
				h := &tar.Header{Name: e.name, Mode: 0o644, Typeflag: tar.TypeReg}
				if e.dir {
					h.Typeflag, h.Mode = tar.TypeDir, 0o755
				}
				if err := tw.WriteHeader(h); err != nil {
					t.Fatal(err)
				}
			}
			if err := tw.Close(); err != nil {
				t.Fatal(err)
			}
			if err := f.Close(); err != nil {
				t.Fatal(err)
			}
			if err := extractArchive(archive, target, []string{snap}); !errors.Is(err, ErrArchiveCarriesHNSWSnapshot) {
				t.Fatalf("extractArchive = %v; want ErrArchiveCarriesHNSWSnapshot", err)
			}
			if _, err := os.Lstat(filepath.Join(snap, tc.landed)); !os.IsNotExist(err) {
				t.Errorf("%s exists in the snapshot directory after the refusal (%v)", tc.landed, err)
			}
		})
	}

	t.Run("an entry outside the snapshot directories", func(t *testing.T) {
		base := t.TempDir()
		archive := filepath.Join(base, "the-dark-knight.tar")
		writePlainTar(t, archive, [][2]string{{"audiobooks.pebble/MANIFEST-000001", "batcave"}})
		target := filepath.Join(base, "gotham")
		if err := extractArchive(archive, target, []string{filepath.Join(target, "hnsw"), filepath.Join(base, "hnsw")}); err != nil {
			t.Fatalf("extractArchive = %v; want the entry written", err)
		}
	})
}

func TestPathsOverlap(t *testing.T) {
	base := t.TempDir()
	snap := filepath.Join(base, "gotham", "hnsw")
	mustMkdir(t, snap)
	mustSymlink(t, filepath.Join(base, "gotham"), filepath.Join(base, "wayne-manor"))
	cases := map[string]bool{
		snap:                                    true,  // the directory itself
		filepath.Join(base, "gotham"):           true,  // above it
		base:                                    true,  // further above
		filepath.Join(snap, "batcave"):          true,  // inside it, not created yet
		filepath.Join(base, "wayne-manor"):      true,  // above it, through a symlink
		filepath.Join(base, "wayne-manor/hnsw"): true,  // the directory, through a symlink
		filepath.Join(base, "gotham", "hnsw2"):  false, // a sibling sharing the prefix
		filepath.Join(base, "arkham"):           false, // a sibling of its parent
	}
	for p, want := range cases {
		if got := PathsOverlap(p, snap); got != want {
			t.Errorf("PathsOverlap(%q, snapshot) = %v, want %v", p, got, want)
		}
	}
}

func mustMkdir(t *testing.T, dirs ...string) {
	t.Helper()
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func mustSymlink(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		t.Fatal(err)
	}
}

// TestPrepareRestoreIn_RefusesAnEmptyArchive (R1-15 round-4 verification
// review, SHOULD-FIX 2): an archive with nothing to restore is refused in the
// prepare step, so the handler never marks the snapshot diverged for it.
func TestPrepareRestoreIn_RefusesAnEmptyArchive(t *testing.T) {
	for _, codec := range []Codec{gzipCodec{}, zstdCodec{}, noneCodec{}} {
		for _, verify := range []bool{false, true} {
			name := codec.Name()
			if verify {
				name += "/verified"
			}
			t.Run(name, func(t *testing.T) {
				dir := t.TempDir()
				archive := filepath.Join(dir, "the-long-halloween"+codec.Extension())
				writeArchive(t, archive, codec, nil)
				if verify {
					sealArchive(t, archive)
				}
				r, err := PrepareRestoreIn(dir, filepath.Base(archive), verify)
				if !errors.Is(err, ErrArchiveEmpty) || r != nil {
					t.Fatalf("PrepareRestoreIn = %v, %v; want ErrArchiveEmpty and no prepared restore", r, err)
				}
			})
		}
	}
	// The extraction skips an entry whose name is empty once its leading
	// slashes go, so an archive of only such entries restores nothing too.
	t.Run("only a name the extraction skips", func(t *testing.T) {
		dir := t.TempDir()
		f, err := os.Create(filepath.Join(dir, "the-long-halloween.tar"))
		if err != nil {
			t.Fatal(err)
		}
		tw := tar.NewWriter(f)
		if err := tw.WriteHeader(&tar.Header{Name: "/", Mode: 0o755, Typeflag: tar.TypeDir}); err != nil {
			t.Fatal(err)
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareRestoreIn(dir, "the-long-halloween.tar", false); !errors.Is(err, ErrArchiveEmpty) {
			t.Fatalf("PrepareRestoreIn = %v; want ErrArchiveEmpty", err)
		}
	})
	// A 0-byte file sniffs as an uncompressed tar with no entries.
	t.Run("zero bytes", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "the-long-halloween.tar"), nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := PrepareRestoreIn(dir, "the-long-halloween.tar", false); !errors.Is(err, ErrArchiveEmpty) {
			t.Fatalf("PrepareRestoreIn = %v; want ErrArchiveEmpty", err)
		}
	})
}

// TestPrepareRestoreIn_RecordsTheArchiveRoot (R1-15 round-4 verification
// review, NIT): the restore handler derives the restored database's snapshot
// directory from the archive's own root, not from today's database name.
func TestPrepareRestoreIn_RecordsTheArchiveRoot(t *testing.T) {
	cases := map[string][][2]string{
		"audiobooks-2025.pebble": {
			{"audiobooks-2025.pebble/MANIFEST-000001", "batcave"},
			{"audiobooks.pebble/MANIFEST-000001", "batcave"},
		},
		"audiobooks.pebble": {{"./audiobooks.pebble/OPTIONS-000002", "alfred"}},
		"database":          {{"/database/MANIFEST-000001", "batcave"}},
	}
	for want, entries := range cases {
		t.Run(want, func(t *testing.T) {
			dir := t.TempDir()
			writePlainTar(t, filepath.Join(dir, "a-lonely-place-of-dying.tar"), entries)
			r, err := PrepareRestoreIn(dir, "a-lonely-place-of-dying.tar", false)
			if err != nil {
				t.Fatal(err)
			}
			if got := r.Root(); got != want {
				t.Errorf("Root() = %q, want %q", got, want)
			}
		})
	}
}
