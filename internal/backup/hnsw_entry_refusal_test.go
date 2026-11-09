// file: internal/backup/hnsw_entry_refusal_test.go
// version: 1.0.11
// guid: 9cd75ebf-5ec4-40ed-8d74-b6dd4f2da595
// last-edited: 2026-11-09

package backup

import (
	"archive/tar"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// R1-15 round-4 (the archive residual): a backup holds one root, the database
// directory (addToArchive), and the HNSW snapshot directory sits beside the
// database, not in it. An archive entry in that directory is therefore never
// from a real backup, and extracting one could plant a clean-stop marker that
// vouches for a graph the restored database does not match. ExtractStore
// refuses such an archive in the same read that verifies it, before anything
// is written (scanArchive); extractArchive refuses the entry too, without
// writing it.

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

// extractStoreRefused runs ExtractStore on archive into a fresh, empty store
// directory and returns its error, failing the test when anything was
// written: the scan refuses before the first write, so the extraction never
// runs (afterEntryHook), the store directory stays empty and no extraction
// directory is made beside it.
func extractStoreRefused(t *testing.T, archive string) error {
	t.Helper()
	extracted := 0
	afterEntryHook = func(string) { extracted++ }
	t.Cleanup(func() { afterEntryHook = nil })
	dst := emptyStoreDir(t)
	_, err := ExtractStore(context.Background(), archive, dst)
	if extracted != 0 {
		t.Errorf("the extraction wrote %d entries before the refusal; the scan must refuse first", extracted)
	}
	if entries, _ := os.ReadDir(dst); len(entries) != 0 {
		t.Errorf("a refused archive left %d entries in the store directory", len(entries))
	}
	if left, _ := filepath.Glob(filepath.Join(filepath.Dir(dst), ".extract-*")); len(left) != 0 {
		t.Errorf("a refused archive left extraction directories: %v", left)
	}
	return err
}

// TestExtractStore_RefusesAClimbingEntryUnwritten: an entry that climbs out
// of the target is refused before anything is written, including the
// round-4 review's probe, which joins back into the snapshot directory of a
// target named arkham.
func TestExtractStore_RefusesAClimbingEntryUnwritten(t *testing.T) {
	for _, name := range []string{"../bane", "x/../../arkham/hnsw/hnsw.clean-stop"} {
		t.Run(name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "no-mans-land.tar")
			writePlainTar(t, archive, [][2]string{
				{"audiobooks.pebble/MANIFEST-000001", "batcave"},
				{name, "venom"},
			})
			if err := extractStoreRefused(t, archive); !errors.Is(err, ErrArchiveEntryEscapes) {
				t.Fatalf("ExtractStore = %v; want ErrArchiveEntryEscapes", err)
			}
		})
	}
}

func TestExtractStore_RefusesAnHNSWSnapshotEntryUnwritten(t *testing.T) {
	marker := [2]string{"hnsw/hnsw.clean-stop", `{"version":1,"model":"bge-m3"}`}
	layouts := []struct {
		name    string
		entries [][2]string
	}{
		// The refusal comes mid-read, after a large entry: a verified
		// extraction must still hash every byte, or the checksum would
		// mismatch and hide the refusal.
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
			for _, sealed := range []bool{false, true} {
				name := layout.name + "/" + codec.Name()
				if sealed {
					name += "/with its sidecar"
				}
				t.Run(name, func(t *testing.T) {
					archive := filepath.Join(t.TempDir(), "the-court-of-owls"+codec.Extension())
					writeArchive(t, archive, codec, layout.entries)
					if sealed {
						sealArchive(t, archive)
					}
					if err := extractStoreRefused(t, archive); !errors.Is(err, ErrArchiveCarriesHNSWSnapshot) {
						t.Fatalf("ExtractStore = %v; want ErrArchiveCarriesHNSWSnapshot", err)
					}
				})
			}
		}
	}
}

// TestExtractStore_VerifiesInTheScanRead: verification shares the read that
// scans the entries, so the checksum still covers every byte, a mismatch
// still wins over what the scan found, and an archive the scan cannot read
// to the end is refused before anything is written.
func TestExtractStore_VerifiesInTheScanRead(t *testing.T) {
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
			if _, verified, err := extractStore(context.Background(), whole, emptyStoreDir(t)); err != nil || !verified {
				t.Fatalf("a whole archive with its own checksum: verified %v, %v", verified, err)
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
			if err := extractStoreRefused(t, cut); err == nil {
				t.Error("an unverified cut archive was extracted; want it refused before any write")
			}
			// The cut archive's own checksum matches, so the scan's failure
			// is what the caller hears.
			sealArchive(t, cut)
			if err := extractStoreRefused(t, cut); err == nil || errors.Is(err, ErrChecksumMismatch) {
				t.Errorf("a cut archive with its own checksum = %v; want the scan's error, not a mismatch", err)
			}
			// The whole archive's checksum does not, and the mismatch wins.
			sidecar, err := os.ReadFile(checksumSidecarPath(whole))
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(checksumSidecarPath(cut), sidecar, 0o644); err != nil {
				t.Fatal(err)
			}
			if err := extractStoreRefused(t, cut); !errors.Is(err, ErrChecksumMismatch) {
				t.Errorf("a cut archive with the whole one's checksum = %v; want ErrChecksumMismatch", err)
			}
		})
	}
}

// TestExtractArchive_RefusesAClimbingEntryUnwritten: the round-4 review's
// probe, extracted without the scan (an archive that changed after it):
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

// TestExtractArchive_RefusesAnHNSWSnapshotEntryUnwritten: ExtractStore's
// scan refuses such an archive first, so this guards one that changed
// between the two reads. The entry is never written, whatever came before it.
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

// caseAlias reports whether, in dir, the name alias opens the directory
// name, which it creates: whether the filesystem folds alias to name. A test
// that needs a case-insensitive filesystem skips without one; this asks the
// filesystem rather than guessing from GOOS, and asks per alias, because
// plain case folding and the long s are separate questions.
func caseAlias(t *testing.T, dir, name, alias string) bool {
	t.Helper()
	mustMkdir(t, filepath.Join(dir, name))
	want, err := os.Stat(filepath.Join(dir, name))
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.Stat(filepath.Join(dir, alias))
	return err == nil && os.SameFile(got, want)
}

// TestExtractArchive_KeepsEntriesOutOfTheSnapshotDirs (R1-15 round-4
// verification review, SHOULD-FIX 1; round 5, B1-r5 and S1-r5): entry names
// cannot keep an entry out of a snapshot directory the filesystem routes it
// into, so Extract compares each directory on the way to the destination
// with the snapshot directories by identity, and does not write a file
// through a symlink.
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
		// wantErr is the refusal; ErrArchiveCarriesHNSWSnapshot when nil.
		wantErr error
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
			// A write through a dangling symlink creates its target. The
			// destination's own name is that symlink, and since round 8
			// (B1-r8) landsIn resolves the whole path, so it refuses one
			// that does not resolve before writeEntry's symlink refusal is
			// reached; so for the three below.
			name: "a dangling symlink as the entry itself",
			setup: func(t *testing.T, base string) (string, string) {
				target, snap := filepath.Join(base, "gotham"), filepath.Join(base, "hnsw")
				mustMkdir(t, target, snap)
				mustSymlink(t, filepath.Join(snap, "hnsw.clean-stop"), filepath.Join(target, "alfred"))
				return target, snap
			},
			entries: []entry{{name: "alfred"}},
			landed:  "hnsw.clean-stop",
			wantErr: ErrUnresolvableSymlink,
		},
		{
			// B1-r5, the reviewer's first shape: the kernel resolves m's
			// relative target from m's physical parent, x/y, so lnk/m lands
			// in the live snapshot directory. Joined lexically to lnk, as
			// the round-5 resolver did, it seemed to land above the target.
			name: "a relative symlink resolved from its physical parent",
			setup: func(t *testing.T, base string) (string, string) {
				snap := filepath.Join(base, "hnsw")
				mustMkdir(t, filepath.Join(base, "x", "y"), snap)
				mustSymlink(t, filepath.Join("x", "y"), filepath.Join(base, "lnk"))
				mustSymlink(t, filepath.Join("..", "..", "hnsw", "hnsw.clean-stop"), filepath.Join(base, "x", "y", "m"))
				return base, snap
			},
			entries: []entry{{name: "lnk/m"}},
			landed:  "hnsw.clean-stop",
			wantErr: ErrUnresolvableSymlink,
		},
		{
			// B1-r5, the second shape: the same under a custom target, one
			// level further down, into the live snapshot directory.
			name: "a relative symlink under a custom target",
			setup: func(t *testing.T, base string) (string, string) {
				target, snap := filepath.Join(base, "gotham"), filepath.Join(base, "hnsw")
				mustMkdir(t, filepath.Join(target, "x", "y"), snap)
				mustSymlink(t, filepath.Join("x", "y"), filepath.Join(target, "lnk"))
				mustSymlink(t, filepath.Join("..", "..", "..", "hnsw", "hnsw.clean-stop"), filepath.Join(target, "x", "y", "m"))
				return target, snap
			},
			entries: []entry{{name: "lnk/m"}},
			landed:  "hnsw.clean-stop",
			wantErr: ErrUnresolvableSymlink,
		},
		{
			// B1-r5, the third shape: the target itself is a symlink to x/y.
			name: "a target that is a symlink",
			setup: func(t *testing.T, base string) (string, string) {
				snap := filepath.Join(base, "hnsw")
				mustMkdir(t, filepath.Join(base, "x", "y"), snap)
				mustSymlink(t, filepath.Join("x", "y"), filepath.Join(base, "arkham"))
				mustSymlink(t, filepath.Join("..", "..", "hnsw", "hnsw.clean-stop"), filepath.Join(base, "x", "y", "m"))
				return filepath.Join(base, "arkham"), snap
			},
			entries: []entry{{name: "m"}},
			landed:  "hnsw.clean-stop",
			wantErr: ErrUnresolvableSymlink,
		},
		{
			// S1-r5: on a case-insensitive filesystem HNSW is the snapshot
			// directory, whatever its spelling in the path.
			name: "the snapshot directory as the target, in another case",
			setup: func(t *testing.T, base string) (string, string) {
				if !caseAlias(t, base, "hnsw", "HNSW") {
					t.Skip("the filesystem does not fold HNSW to hnsw")
				}
				return filepath.Join(base, "HNSW"), filepath.Join(base, "hnsw")
			},
			entries: []entry{{name: "hnsw.clean-stop"}},
			landed:  "hnsw.clean-stop",
		},
		{
			// S1-r5: and with a long s (U+017F), which folds to s.
			name: "the snapshot directory as the target, with a long s",
			setup: func(t *testing.T, base string) (string, string) {
				if !caseAlias(t, base, "hnsw", "hn\u017fw") {
					t.Skip("the filesystem does not fold a long s to s")
				}
				return filepath.Join(base, "hn\u017fw"), filepath.Join(base, "hnsw")
			},
			entries: []entry{{name: "hnsw.clean-stop"}},
			landed:  "hnsw.clean-stop",
		},
		{
			// The snapshot directory does not exist yet, so it has no
			// identity: its parent's identity and its name, compared without
			// case, stand in for it. Through a symlink to the parent, the
			// entry would create the directory.
			name: "a symlink to the parent of a snapshot directory not made yet",
			setup: func(t *testing.T, base string) (string, string) {
				target, live := filepath.Join(base, "gotham"), filepath.Join(base, "wayne-manor")
				mustMkdir(t, target, live)
				mustSymlink(t, live, filepath.Join(target, "lnk"))
				return target, filepath.Join(live, "hnsw")
			},
			entries: []entry{{name: "lnk/HNSW/hnsw.clean-stop"}},
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
		{
			// The walk includes the destination itself: a directory entry
			// that is a symlink to the snapshot directory is refused, not
			// accepted as an existing directory by MkdirAll.
			name: "a directory entry that is a symlink to the snapshot directory",
			setup: func(t *testing.T, base string) (string, string) {
				target, snap := filepath.Join(base, "gotham"), filepath.Join(base, "hnsw")
				mustMkdir(t, target, snap)
				mustSymlink(t, snap, filepath.Join(target, "wayne-manor"))
				return target, snap
			},
			entries: []entry{{name: "wayne-manor/", dir: true}},
			landed:  "batcave",
		},
		{
			// R1-15 round-6 verification review, B1-r6: the snapshot
			// directory's path is a dangling symlink. os.Stat cannot give
			// it an identity, and standing its parent and its name in for
			// it, as for a directory not made yet, let the entry create the
			// symlink's target: the snapshot directory from then on.
			name: "a dangling symlink at the snapshot directory",
			setup: func(t *testing.T, base string) (string, string) {
				snap := filepath.Join(base, "hnsw")
				mustSymlink(t, "joker", snap)
				return base, snap
			},
			entries: []entry{{name: "joker/hnsw.clean-stop"}},
			landed:  "hnsw.clean-stop",
			wantErr: ErrUnresolvableSymlink,
		},
		{
			// B1-r6 one level up: a dangling symlink among the snapshot
			// directory's ancestors refuses every entry. The entry has no
			// hnsw component, which entryRefusal refuses by name since
			// round 8 (N2), before this check is reached.
			name: "a dangling symlink above the snapshot directory",
			setup: func(t *testing.T, base string) (string, string) {
				mustSymlink(t, "joker", filepath.Join(base, "arkham"))
				return base, filepath.Join(base, "arkham", "hnsw")
			},
			entries: []entry{{name: "joker/robin/hnsw.clean-stop"}},
			landed:  "hnsw.clean-stop",
			wantErr: ErrUnresolvableSymlink,
		},
		{
			// B1-r6 for the restored database's snapshot directory, under
			// a custom target.
			name: "a dangling symlink at the snapshot directory under a custom target",
			setup: func(t *testing.T, base string) (string, string) {
				target := filepath.Join(base, "gotham")
				mustMkdir(t, target)
				mustSymlink(t, "joker", filepath.Join(target, "hnsw"))
				return target, filepath.Join(target, "hnsw")
			},
			entries: []entry{{name: "joker/hnsw.clean-stop"}},
			landed:  "hnsw.clean-stop",
			wantErr: ErrUnresolvableSymlink,
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
			want := tc.wantErr
			if want == nil {
				want = ErrArchiveCarriesHNSWSnapshot
			}
			err = extractArchive(archive, target, []string{snap})
			if _, err := os.Lstat(filepath.Join(snap, tc.landed)); !os.IsNotExist(err) {
				t.Errorf("%s exists in the snapshot directory after the extraction (%v)", tc.landed, err)
			}
			if !errors.Is(err, want) {
				t.Fatalf("extractArchive = %v; want %v", err, want)
			}
		})
	}

	// writeEntry's own refusal: a live symlink at the destination, to a file
	// outside the snapshot directories, which landsIn lets through.
	t.Run("a symlink to a file elsewhere as the entry itself", func(t *testing.T) {
		base := t.TempDir()
		target, snap := filepath.Join(base, "gotham"), filepath.Join(base, "hnsw")
		ledger := filepath.Join(base, "riddler", "ledger")
		mustMkdir(t, target, snap, filepath.Dir(ledger))
		if err := os.WriteFile(ledger, []byte("the riddler's ledger"), 0o644); err != nil {
			t.Fatal(err)
		}
		mustSymlink(t, ledger, filepath.Join(target, "alfred"))
		archive := filepath.Join(base, "the-dark-knight.tar")
		writePlainTar(t, archive, [][2]string{{"alfred", "batcave"}})
		if err := extractArchive(archive, target, []string{snap}); !errors.Is(err, ErrRestoreThroughSymlink) {
			t.Fatalf("extractArchive = %v; want ErrRestoreThroughSymlink", err)
		}
		if got, err := os.ReadFile(ledger); err != nil || string(got) != "the riddler's ledger" {
			t.Errorf("the link's target reads %q (%v)", got, err)
		}
		if fi, err := os.Lstat(filepath.Join(target, "alfred")); err != nil || fi.Mode()&os.ModeSymlink == 0 {
			t.Errorf("the symlink was replaced (lstat: %v)", err)
		}
	})

	t.Run("an entry outside the snapshot directories", func(t *testing.T) {
		base := t.TempDir()
		archive := filepath.Join(base, "the-dark-knight.tar")
		writePlainTar(t, archive, [][2]string{{"audiobooks.pebble/MANIFEST-000001", "batcave"}})
		target := filepath.Join(base, "gotham")
		if err := extractArchive(archive, target, []string{filepath.Join(target, "hnsw"), filepath.Join(base, "hnsw")}); err != nil {
			t.Fatalf("extractArchive = %v; want the entry written", err)
		}
	})

	// B1-r6: a snapshot directory the restore cannot resolve refuses every
	// entry, not only one aimed at it: where the directory is cannot be
	// told, so no destination can be cleared of it. A dangling link, and
	// one that loops.
	for name, link := range map[string]string{"dangling": "joker", "looping": "hnsw"} {
		t.Run("an entry outside a "+name+" snapshot directory", func(t *testing.T) {
			base := t.TempDir()
			archive := filepath.Join(base, "the-dark-knight.tar")
			writePlainTar(t, archive, [][2]string{{"audiobooks.pebble/MANIFEST-000001", "batcave"}})
			target := filepath.Join(base, "gotham")
			mustMkdir(t, target)
			snap := filepath.Join(target, "hnsw")
			mustSymlink(t, link, snap)
			err := extractArchive(archive, target, []string{snap})
			if !errors.Is(err, ErrUnresolvableSymlink) {
				t.Fatalf("extractArchive = %v; want ErrUnresolvableSymlink", err)
			}
			if _, err := os.Lstat(filepath.Join(target, "audiobooks.pebble")); !os.IsNotExist(err) {
				t.Errorf("the extraction wrote into the target (%v)", err)
			}
		})
	}
}

// TestExtractArchive_SkipsTheTargetItself: an entry that names the target
// itself is skipped, as "/" always was, so the extraction counts the entries
// the scan counts (ErrArchiveEmpty).
func TestExtractArchive_SkipsTheTargetItself(t *testing.T) {
	dir := t.TempDir()
	archive := filepath.Join(dir, "death-in-the-family.tar")
	writePlainTar(t, archive, [][2]string{
		{".", "a file in place of the target"},
		{"./audiobooks.pebble/MANIFEST-000001", "batcave"},
	})
	target := filepath.Join(dir, "gotham")
	mustMkdir(t, target)
	if err := extractArchive(archive, target, nil); err != nil {
		t.Fatalf("extractArchive = %v; want the entry naming the target skipped", err)
	}
	if _, err := os.Stat(filepath.Join(target, "audiobooks.pebble", "MANIFEST-000001")); err != nil {
		t.Fatalf("the database entry was not extracted: %v", err)
	}
}

// TestScanArchive_CountsWhatTheExtractionCounts: the scan skips an entry
// that names the target itself, as the extraction does, so an archive of
// only such entries is ErrArchiveEmpty at the scan, before ExtractStore
// makes anything, and one entry the extraction writes is enough.
func TestScanArchive_CountsWhatTheExtractionCounts(t *testing.T) {
	for name, tc := range map[string]struct {
		entries [][2]string
		want    error
	}{
		"only the target itself": {[][2]string{{"/", ""}, {"./", ""}, {"robin/../", ""}}, ErrArchiveEmpty},
		"and one entry":          {[][2]string{{"./", ""}, {"./audiobooks.pebble/MANIFEST-000001", "batcave"}}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "a-death-in-the-family.tar")
			writePlainTar(t, archive, tc.entries)
			sum, err := scanArchive(archive)
			if sum == "" {
				t.Fatalf("no digest for a readable archive (%v)", err)
			}
			if !errors.Is(err, tc.want) || (tc.want == nil) != (err == nil) {
				t.Fatalf("scanArchive = %v; want %v", err, tc.want)
			}
		})
	}
}

// TestStatKeepOuts (B1-r6): the extraction looks the snapshot directories up
// at each entry and refuses one whose path, or one of whose ancestors, is a
// symlink that does not resolve. A directory not made yet is fine.
func TestStatKeepOuts(t *testing.T) {
	base := t.TempDir()
	live := filepath.Join(base, "gotham", "hnsw")
	mustMkdir(t, live)
	absent := filepath.Join(base, "gotham", "batcave", "hnsw")
	mustSymlink(t, "joker", filepath.Join(base, "harley"))
	mustSymlink(t, "riddler", filepath.Join(base, "riddler"))
	// A directory the process may not search: a missing name under it and
	// one it may not see cannot be told apart, so it fails closed too.
	locked := filepath.Join(base, "arkham")
	mustMkdir(t, locked)
	if os.Geteuid() != 0 {
		if err := os.Chmod(locked, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })
	}
	for name, tc := range map[string]struct {
		dirs []string
		want error
	}{
		"existing and not made yet":           {[]string{live, absent}, nil},
		"none":                                {nil, nil},
		"dangling":                            {[]string{live, filepath.Join(base, "harley")}, ErrUnresolvableSymlink},
		"under a dangling link":               {[]string{filepath.Join(base, "harley", "hnsw"), live}, ErrUnresolvableSymlink},
		"looping":                             {[]string{filepath.Join(base, "riddler", "hnsw")}, ErrUnresolvableSymlink},
		"under a directory it may not search": {[]string{filepath.Join(locked, "hnsw")}, ErrUnresolvableSymlink},
	} {
		t.Run(name, func(t *testing.T) {
			if name == "under a directory it may not search" && os.Geteuid() == 0 {
				t.Skip("root searches any directory")
			}
			_, got := statKeepOuts(tc.dirs)
			if !errors.Is(got, tc.want) || (tc.want == nil) != (got == nil) {
				t.Fatalf("statKeepOuts = %v; want %v", got, tc.want)
			}
		})
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

// TestExtractStore_RefusesAnEmptyArchiveUnwritten (R1-15 round-4
// verification review, SHOULD-FIX 2): an archive with nothing to restore is
// refused by the scan, before anything is written.
func TestExtractStore_RefusesAnEmptyArchiveUnwritten(t *testing.T) {
	for _, codec := range []Codec{gzipCodec{}, zstdCodec{}, noneCodec{}} {
		for _, sealed := range []bool{false, true} {
			name := codec.Name()
			if sealed {
				name += "/with its sidecar"
			}
			t.Run(name, func(t *testing.T) {
				archive := filepath.Join(t.TempDir(), "the-long-halloween"+codec.Extension())
				writeArchive(t, archive, codec, nil)
				if sealed {
					sealArchive(t, archive)
				}
				if err := extractStoreRefused(t, archive); !errors.Is(err, ErrArchiveEmpty) {
					t.Fatalf("ExtractStore = %v; want ErrArchiveEmpty", err)
				}
			})
		}
	}
	// The extraction skips an entry that names the target itself ("/",
	// "./", or anything else that cleans to "."), so an archive of only such
	// entries restores nothing too.
	for name, entries := range map[string][][2]string{
		"slash":     {{"/", ""}},
		"dot slash": {{"./", ""}, {"/", ""}, {"robin/../", ""}},
	} {
		t.Run("only names the extraction skips/"+name, func(t *testing.T) {
			archive := filepath.Join(t.TempDir(), "the-long-halloween.tar")
			writePlainTar(t, archive, entries)
			if err := extractStoreRefused(t, archive); !errors.Is(err, ErrArchiveEmpty) {
				t.Fatalf("ExtractStore = %v; want ErrArchiveEmpty", err)
			}
		})
	}
	// A 0-byte file sniffs as an uncompressed tar with no entries.
	t.Run("zero bytes", func(t *testing.T) {
		archive := filepath.Join(t.TempDir(), "the-long-halloween.tar")
		if err := os.WriteFile(archive, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := extractStoreRefused(t, archive); !errors.Is(err, ErrArchiveEmpty) {
			t.Fatalf("ExtractStore = %v; want ErrArchiveEmpty", err)
		}
	})
}

// TestExtractArchive_ReplacesAFileRatherThanWritingIntoIt (R1-15 round-6
// verification review, SF1): a file already at an entry's destination can be
// a hard link to a file in a snapshot directory, or to a Pebble checkpoint's,
// and no directory comparison sees a hard link: it is the same inode under
// another name. Truncating and writing the file at the destination wrote
// that inode, so the snapshot's file changed. The extraction writes each
// entry to a new file in the destination's directory and renames it into
// place, which replaces the name and leaves the inode, with every other name
// for it, as it was.
func TestExtractArchive_ReplacesAFileRatherThanWritingIntoIt(t *testing.T) {
	base := t.TempDir()
	snap := filepath.Join(base, "hnsw")
	target := filepath.Join(base, "gotham")
	mustMkdir(t, snap, filepath.Join(target, "audiobooks.pebble"))
	graph := filepath.Join(snap, "graph.bin")
	if err := os.WriteFile(graph, []byte("the long halloween"), 0o644); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(target, "audiobooks.pebble", "OPTIONS-000002")
	if err := os.Link(graph, dest); err != nil {
		t.Skipf("the filesystem does not make hard links: %v", err)
	}
	archive := filepath.Join(base, "dark-victory.tar")
	writePlainTar(t, archive, [][2]string{{"audiobooks.pebble/OPTIONS-000002", "hush"}})
	if err := extractArchive(archive, target, []string{snap}); err != nil {
		t.Fatalf("extractArchive = %v; want the entry written", err)
	}
	if got, err := os.ReadFile(graph); err != nil || string(got) != "the long halloween" {
		t.Errorf("the snapshot's file reads %q (%v) after the extraction; want it unchanged", got, err)
	}
	if got, err := os.ReadFile(dest); err != nil || string(got) != "hush" {
		t.Errorf("the destination reads %q (%v); want the entry's content", got, err)
	}
}

// dirNames returns the names in dir, sorted.
func dirNames(t *testing.T, dir string) []string {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(ents))
	for _, e := range ents {
		names = append(names, e.Name())
	}
	return names
}

// TestExtractArchive_RenamesEachEntryIntoPlace (SF1): each entry is
// written to a new file beside its destination and renamed over it, so a
// destination is either its old file or the entry's whole file, never a
// part of it, and the new file takes the entry's mode. Nothing of the new
// file is left behind, whether the entry was written or not.
func TestExtractArchive_RenamesEachEntryIntoPlace(t *testing.T) {
	t.Run("written", func(t *testing.T) {
		base := t.TempDir()
		archive := filepath.Join(base, "year-one.tar")
		f, err := os.Create(archive)
		if err != nil {
			t.Fatal(err)
		}
		tw := tar.NewWriter(f)
		for _, e := range []struct {
			name string
			mode int64
		}{{"audiobooks.pebble/MANIFEST-000001", 0o640}, {"audiobooks.pebble/CURRENT", 0o600}} {
			if err := tw.WriteHeader(&tar.Header{Name: e.name, Mode: e.mode, Size: 7, Typeflag: tar.TypeReg}); err != nil {
				t.Fatal(err)
			}
			if _, err := tw.Write([]byte("batcave")); err != nil {
				t.Fatal(err)
			}
		}
		if err := tw.Close(); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(base, "gotham")
		if err := extractArchive(archive, target, nil); err != nil {
			t.Fatalf("extractArchive = %v", err)
		}
		db := filepath.Join(target, "audiobooks.pebble")
		if got := dirNames(t, db); len(got) != 2 || got[0] != "CURRENT" || got[1] != "MANIFEST-000001" {
			t.Errorf("the database directory holds %q; want only the two entries", got)
		}
		for name, want := range map[string]os.FileMode{"MANIFEST-000001": 0o640, "CURRENT": 0o600} {
			fi, err := os.Stat(filepath.Join(db, name))
			if err != nil {
				t.Fatal(err)
			}
			if fi.Mode().Perm() != want {
				t.Errorf("%s has mode %v; want %v", name, fi.Mode().Perm(), want)
			}
		}
	})

	t.Run("cut off inside an entry", func(t *testing.T) {
		base := t.TempDir()
		full := filepath.Join(base, "full.tar")
		writePlainTar(t, full, [][2]string{
			{"audiobooks.pebble/MANIFEST-000001", "batcave"},
			{"audiobooks.pebble/OPTIONS-000002", strings.Repeat("hush", 4096)},
		})
		raw, err := os.ReadFile(full)
		if err != nil {
			t.Fatal(err)
		}
		// The second entry's header starts at 1024 (a 512-byte header, the
		// first entry's 7 bytes padded to 512); cut inside its data.
		archive := filepath.Join(base, "cut.tar")
		if err := os.WriteFile(archive, raw[:1024+512+2048], 0o644); err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(base, "gotham")
		db := filepath.Join(target, "audiobooks.pebble")
		mustMkdir(t, db)
		old := filepath.Join(db, "OPTIONS-000002")
		if err := os.WriteFile(old, []byte("the old options"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := extractArchive(archive, target, nil); err == nil {
			t.Fatal("extractArchive = nil; want the cut-off entry's error")
		}
		if got, err := os.ReadFile(old); err != nil || string(got) != "the old options" {
			t.Errorf("the destination reads %.40q (%v) after a failed entry; want its old file", got, err)
		}
		if got := dirNames(t, db); len(got) != 2 || got[0] != "MANIFEST-000001" || got[1] != "OPTIONS-000002" {
			t.Errorf("the database directory holds %q; want the written entry and the old file only", got)
		}
	})
}

// TestExtractArchive_ReadsTheSnapshotDirsAtEachEntry: the snapshot
// directories are looked up again for every entry. A directory that did not
// exist when the extraction began (the server's periodic HNSW export can
// make one) has no identity until it is made, and a symlink planted to it
// resolves into it from then on. The directory is made between the two
// entries (afterEntryHook).
func TestExtractArchive_ReadsTheSnapshotDirsAtEachEntry(t *testing.T) {
	base := t.TempDir()
	target, live := filepath.Join(base, "gotham"), filepath.Join(base, "wayne-manor")
	snap := filepath.Join(live, "hnsw")
	mustMkdir(t, target, live)
	mustSymlink(t, snap, filepath.Join(target, "lnk"))
	var after []string
	afterEntryHook = func(name string) {
		after = append(after, name)
		if name == "first" {
			mustMkdir(t, snap)
		}
	}
	t.Cleanup(func() { afterEntryHook = nil })

	archive := filepath.Join(base, "knightfall.tar")
	writePlainTar(t, archive, [][2]string{
		{"first", "bane"},
		{"lnk/hnsw.clean-stop", "a marker"},
	})
	err := extractArchive(archive, target, []string{snap})
	if len(after) != 1 || after[0] != "first" {
		t.Fatalf("entries extracted: %q; want only the first", after)
	}
	if !errors.Is(err, ErrArchiveCarriesHNSWSnapshot) {
		t.Fatalf("extractArchive = %v; want ErrArchiveCarriesHNSWSnapshot", err)
	}
	if _, err := os.Lstat(filepath.Join(snap, "hnsw.clean-stop")); !os.IsNotExist(err) {
		t.Errorf("the marker was written into the snapshot directory made during the extraction (%v)", err)
	}
}
