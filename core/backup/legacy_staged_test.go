// file: core/backup/legacy_staged_test.go
// version: 0.1.0
// guid: ec3e04fe-fc67-47e7-9f75-dfc7fce40c15
// last-edited: 2026-11-22

package backup_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/falkcorp/mediacore/backup"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// stagedLegacyStore lays out a staging directory the way a legacy restore
// does (the store as db/, nothing else) and returns it with the entries a
// host that extracted the archive itself would record.
func stagedLegacyStore(t *testing.T) (staging string, entries []backup.Entry) {
	t.Helper()
	staging = t.TempDir()
	if err := os.Mkdir(filepath.Join(staging, "db"), 0o700); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{"CURRENT": "MANIFEST-000001\n", "000001.sst": "the-batcave-ledger"} {
		if err := os.WriteFile(filepath.Join(staging, "db", name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, backup.Entry{Path: "db/" + name, Size: int64(len(body)), SHA256: shaHex([]byte(body)), Class: backup.SystemOfRecord})
	}
	return staging, entries
}

// TestWriteLegacyStagedRoundTrips: the manifest WriteLegacyStaged writes is the
// one ReadStagedManifest decodes as a legacy manifest and Recheck accepts, under
// a size check and under a full re-hash, and the hash it returns is the one the
// intent carries.
func TestWriteLegacyStagedRoundTrips(t *testing.T) {
	staging, entries := stagedLegacyStore(t)
	sha, err := backup.WriteLegacyStaged(staging, shaHex([]byte("archive")), "gzip", entries)
	if err != nil {
		t.Fatal(err)
	}
	in := restorejournal.Intent{
		Kind: restorejournal.KindArchive, Staging: staging, ManifestSHA256: sha,
		Targets: []restorejournal.Target{{From: filepath.Join(staging, "db"), To: filepath.Join(t.TempDir(), "live")}},
	}
	m, got, legacy, err := backup.ReadStagedManifest(in)
	if err != nil {
		t.Fatal(err)
	}
	if m != nil || !legacy {
		t.Fatalf("ReadStagedManifest = %+v, legacy %v; want a legacy manifest", m, legacy)
	}
	if len(got) != len(entries) {
		t.Fatalf("read back %d entries, wrote %d", len(got), len(entries))
	}
	for i := range got { // the manifest is written sorted by path
		if i > 0 && got[i-1].Path >= got[i].Path {
			t.Fatalf("entries are not sorted by path: %+v", got)
		}
	}
	ctx := context.Background()
	if err := backup.Recheck(nil)(ctx, in); err != nil {
		t.Fatalf("size recheck: %v", err)
	}
	if err := backup.Recheck(func() bool { return true })(ctx, in); err != nil {
		t.Fatalf("full re-hash: %v", err)
	}
	// Anything but the returned hash is refused.
	in.ManifestSHA256 = shaHex([]byte("another manifest"))
	if _, _, _, err := backup.ReadStagedManifest(in); err == nil {
		t.Fatal("a manifest that does not hash to the intent's was read")
	}
}

// TestWriteLegacyStagedLeavesTheStoreAlone: the manifest goes beside db/, never
// inside it, because the store directory is the one moved onto the live path.
func TestWriteLegacyStagedLeavesTheStoreAlone(t *testing.T) {
	staging, entries := stagedLegacyStore(t)
	if _, err := backup.WriteLegacyStaged(staging, shaHex([]byte("archive")), "none", entries); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(staging, "legacy-manifest.json")); err != nil {
		t.Fatalf("no manifest beside the store: %v", err)
	}
	ls, err := os.ReadDir(filepath.Join(staging, "db"))
	if err != nil || len(ls) != len(entries) {
		t.Fatalf("the store directory holds %d files after the write (%v), want %d", len(ls), err, len(entries))
	}
}

// TestWriteLegacyStagedRefusals: a manifest Recheck could not use is never
// written.
func TestWriteLegacyStagedRefusals(t *testing.T) {
	staging, entries := stagedLegacyStore(t)
	sum := shaHex([]byte("archive"))
	for name, tc := range map[string]struct {
		dir, sum string
		entries  []backup.Entry
	}{
		"no staging directory":    {"", sum, entries},
		"no archive digest":       {staging, "", entries},
		"a path that climbs":      {staging, sum, []backup.Entry{{Path: "../db/CURRENT", Size: 1, SHA256: sum}}},
		"an absolute path":        {staging, sum, []backup.Entry{{Path: "/db/CURRENT", Size: 1, SHA256: sum}}},
		"an empty path":           {staging, sum, []backup.Entry{{Path: "", Size: 1, SHA256: sum}}},
		"an entry with no digest": {staging, sum, []backup.Entry{{Path: "db/CURRENT", Size: 1}}},
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := backup.WriteLegacyStaged(tc.dir, tc.sum, "gzip", tc.entries); err == nil {
				t.Fatalf("WriteLegacyStaged = %q, nil; want a refusal", got)
			}
		})
	}
	if _, err := os.Lstat(filepath.Join(staging, "legacy-manifest.json")); err == nil {
		t.Fatal("a refused write left a manifest behind")
	}
}

// TestLegacyArchiveID: the file name's stem after LegacyIDPrefix, or the
// digest's first 16 digits for a name the catalogue could not address.
func TestLegacyArchiveID(t *testing.T) {
	sum := "0123456789abcdef0123456789abcdef"
	for name, tc := range map[string]struct{ file, want string }{
		"a plain archive":     {"gotham_backup.tar.gz", "legacy-gotham_backup"},
		"a name with a dot":   {"gotham.pebble.tar.zst", "legacy-0123456789abcdef"},
		"a name with a space": {"gotham backup.tar", "legacy-0123456789abcdef"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := string(backup.LegacyArchiveID(tc.file, sum)); got != tc.want {
				t.Fatalf("LegacyArchiveID(%q) = %q, want %q", tc.file, got, tc.want)
			}
		})
	}
}
