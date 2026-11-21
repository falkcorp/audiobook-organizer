// file: core/kernel/restore_staging_sweep_unix_test.go
// version: 0.1.0
// guid: 2f8b6d39-7c14-4a5e-b9d3-81e0c46a72fb
// last-edited: 2026-11-21

//go:build unix

package kernel

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// TestRestoreSweepsOrphanStaging: storage restore-offline in place removes
// what an earlier restore staged and never linked an intent for, on both
// filesystems, and leaves everything else where it is, naming each one it
// leaves in a warning: a staging directory with anything beside staging/,
// one that is a symbolic link or holds one, one an intent was linked for
// (its journal is there; silently: the swap's own), one whose restore/<id>
// holds a stray file or is a symbolic link, and an entry that is not a
// directory. A Stage stopped after it created restore/<id> and before it
// linked the intent leaves that directory empty: it is removed with the
// staging beside it.
func TestRestoreSweepsOrphanStaging(t *testing.T) {
	h, _ := newCrossDeviceHost(t)
	name := h.completedUpgrade()
	stateAside := filepath.Join(h.state, restorejournal.AsideDirName)
	dbAside := filepath.Join(filepath.Dir(h.db), restorejournal.AsideDirName)
	const (
		orphanState = "20261101T010000Z-0rphan01"
		orphanDB    = "20261101T010000Z-0rphan02"
		extra       = "20261101T010000Z-keep0001"
		previous    = "20261101T010000Z-keep0002"
		link        = "20261101T010000Z-keep0003"
		innerLink   = "20261101T010000Z-keep0004"
		linked      = "20261101T010000Z-keep0005"
		notDir      = "20261101T010000Z-keep0006"
		stopped     = "20261101T010000Z-0rphan03"
		stoppedFull = "20261101T010000Z-keep0007"
		stoppedLink = "20261101T010000Z-keep0008"
	)
	write(t, filepath.Join(stateAside, orphanState, "staging/state/encryption_key"), "joker", 0o600)
	write(t, filepath.Join(dbAside, orphanDB, "staging/db/MANIFEST-000001"), "penguin", 0o600)
	write(t, filepath.Join(stateAside, extra, "staging/state/encryption_key"), "riddler", 0o600)
	write(t, filepath.Join(stateAside, extra, "notes.txt"), "two-face", 0o600)
	write(t, filepath.Join(dbAside, previous, "staging/state/encryption_key"), "scarecrow", 0o600)
	write(t, filepath.Join(dbAside, previous, "previous/main.pebble/MANIFEST-000001"), "bane", 0o600)
	elsewhere := t.TempDir()
	write(t, filepath.Join(elsewhere, "staging/state/encryption_key"), "catwoman", 0o600)
	if err := os.Symlink(elsewhere, filepath.Join(stateAside, link)); err != nil {
		t.Fatal(err)
	}
	outside := write(t, filepath.Join(t.TempDir(), "alfred.key"), "alfred", 0o600)
	if err := os.MkdirAll(filepath.Join(stateAside, innerLink, "staging/state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(stateAside, innerLink, "staging/state/encryption_key")); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(stateAside, linked, "staging/state/encryption_key"), "harley", 0o600)
	write(t, filepath.Join(restorejournal.JournalDir(h.state, linked), restorejournal.JournalName), "{}\n", 0o600)
	write(t, filepath.Join(stateAside, stopped, "staging/state/encryption_key"), "poison-ivy", 0o600)
	if err := os.MkdirAll(restorejournal.JournalDir(h.state, stopped), 0o700); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(stateAside, stoppedFull, "staging/state/encryption_key"), "killer-croc", 0o600)
	write(t, filepath.Join(restorejournal.JournalDir(h.state, stoppedFull), "notes.txt"), "hush", 0o600)
	write(t, filepath.Join(stateAside, stoppedLink, "staging/state/encryption_key"), "clayface", 0o600)
	emptyElsewhere := t.TempDir()
	if err := os.Symlink(emptyElsewhere, restorejournal.JournalDir(h.state, stoppedLink)); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(stateAside, notDir), "mad-hatter", 0o600)

	res, err := RestoreOffline(h.ctx, h.keyed(h.config(nil)), RestoreOfflineRequest{Checkpoint: name, AcceptLoss: true})
	if err != nil {
		t.Fatalf("restore with an earlier restore's staging left = %v, want it to restore", err)
	}
	if got := read(t, h.keyFile()); got != "wayne" {
		t.Fatalf("key file = %q, want the checkpoint's wayne", got)
	}
	warnings := strings.Join(res.Warnings, "\n")
	for _, p := range []string{filepath.Join(stateAside, orphanState), filepath.Join(dbAside, orphanDB),
		filepath.Join(stateAside, stopped), restorejournal.JournalDir(h.state, stopped)} {
		if _, err := os.Lstat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s is still there (%v), want an orphan staging directory removed", p, err)
		}
		if !strings.Contains(warnings, "removed "+p) {
			t.Errorf("warnings do not name the removed %s:\n%s", p, warnings)
		}
	}
	for _, c := range []struct {
		root   string
		kept   string
		report bool
	}{
		{filepath.Join(stateAside, extra), "notes.txt", true},
		{filepath.Join(dbAside, previous), "previous/main.pebble/MANIFEST-000001", true},
		{filepath.Join(stateAside, link), "", true},
		{filepath.Join(stateAside, innerLink), "staging/state/encryption_key", true},
		{filepath.Join(stateAside, linked), "staging/state/encryption_key", false},
		{filepath.Join(stateAside, notDir), "", true},
		{filepath.Join(stateAside, stoppedFull), "staging/state/encryption_key", true},
		{filepath.Join(stateAside, stoppedLink), "staging/state/encryption_key", true},
	} {
		if _, err := os.Lstat(filepath.Join(c.root, c.kept)); err != nil {
			t.Errorf("%s was removed (%v), want it left in place", filepath.Join(c.root, c.kept), err)
		}
		if got := strings.Contains(warnings, "left in place: "+c.root+" "); got != c.report {
			t.Errorf("a warning names %s left in place = %v, want %v:\n%s", c.root, got, c.report, warnings)
		}
	}
	if got := read(t, filepath.Join(elsewhere, "staging/state/encryption_key")); got != "catwoman" {
		t.Errorf("what the symbolic link points at = %q, want it untouched", got)
	}
	for _, p := range []string{filepath.Join(restorejournal.JournalDir(h.state, stoppedFull), "notes.txt"),
		restorejournal.JournalDir(h.state, stoppedLink), emptyElsewhere} {
		if _, err := os.Lstat(p); err != nil {
			t.Errorf("%s was removed (%v), want it left in place", p, err)
		}
	}
	if got := read(t, outside); got != "alfred" {
		t.Errorf("the file the inner symbolic link points at = %q, want it untouched", got)
	}
}

// TestRestoreStagingIsPrivate: a restore's staged copy of a state file
// (the encryption key among them) is 0600 from the moment it exists, and
// every directory of its staging chain from .mediacore-restore down is 0700,
// one found wider included: a kill leaves the copy for the next restore's
// sweep, and until then no other user reads it.
func TestRestoreStagingIsPrivate(t *testing.T) {
	old := syscall.Umask(0)
	defer syscall.Umask(old)

	state := filepath.Join(t.TempDir(), "state")
	aside := filepath.Join(state, restorejournal.AsideDirName)
	if err := os.MkdirAll(aside, 0o755); err != nil {
		t.Fatal(err)
	}
	leaf := filepath.Join(aside, "20261101T010000Z-0rphan01", "staging", "state")
	if err := mkdirStaging(vfs.Default, leaf); err != nil {
		t.Fatal(err)
	}
	for d := leaf; ; d = filepath.Dir(d) {
		fi, err := os.Stat(d)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode().Perm() != 0o700 {
			t.Errorf("%s is %04o, want 0700", d, fi.Mode().Perm())
		}
		if d == aside {
			break
		}
	}

	src := write(t, filepath.Join(t.TempDir(), "encryption_key"), "batcave-secrets-key-material", 0o600)
	sum := sha256.Sum256([]byte("batcave-secrets-key-material"))
	dst := filepath.Join(leaf, "encryption_key")
	probe := &probeFS{FS: vfs.Default}
	if err := copyVerified(probe, src, dst, hex.EncodeToString(sum[:])); err != nil {
		t.Fatal(err)
	}
	if len(probe.modes) == 0 {
		t.Fatal("the probe saw no writable handle: this test checks nothing")
	}
	for _, m := range probe.modes {
		if m != 0o600 {
			t.Fatalf("the staged copy was %04o when it was opened for writing, want 0600", m)
		}
	}
	if got := read(t, dst); got != "batcave-secrets-key-material" {
		t.Fatalf("the staged copy holds %q", got)
	}
}
