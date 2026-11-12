// file: core/kernel/restore_archive_files_test.go
// version: 1.0.0
// guid: 8a4c1e72-5d39-4f6b-a0e7-2b9d3c6f1a85
// last-edited: 2026-11-12

package kernel

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/restorejournal"
)

// archiveHost is a swapHost whose archive extractor hands back a store saved
// at the first boot (cave = 1), after a second boot moved the live one on.
type archiveHost struct {
	*swapHost
	cfg Config
}

// newArchiveHost boots the host twice, so that restoring the saved store is a
// visible change, and sets the extractor to return id.
func newArchiveHost(t *testing.T, id string) *archiveHost {
	t.Helper()
	h := newSwapHost(t)
	h.close(h.mustBoot(h.config(nil)))
	saved := filepath.Join(t.TempDir(), "archived.pebble")
	copyTree(t, h.db, saved)
	h.close(h.mustBoot(h.config(nil, caveStep(2, "bane"))))
	c := h.config(nil)
	c.ArchiveExtract = func(_ context.Context, _, dir string) (string, error) {
		copyTree(t, saved, dir)
		return id, nil
	}
	return &archiveHost{swapHost: h, cfg: c}
}

// keyring is the default keyring path of the host's state directory.
func (h *archiveHost) keyring() string { return filepath.Join(h.state, "secrets.keyring") }

// gap is HEAD's gap-fill file beside the host's database.
func (h *archiveHost) gap() string { return filepath.Join(filepath.Dir(h.db), "config.yaml") }

func (h *archiveHost) archive() string { return filepath.Join(h.t.TempDir(), "joker.tar.gz") }

func write(t *testing.T, path, body string, mode os.FileMode) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	// WriteFile's mode is masked by the umask and kept for an existing file.
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func read(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// filesHolding lists the files under root whose content is exactly body.
func filesHolding(t *testing.T, root, body string) []string {
	t.Helper()
	var out []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil || !d.Type().IsRegular() {
			return err
		}
		if b, err := os.ReadFile(p); err == nil && bytes.Equal(b, []byte(body)) {
			out = append(out, p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

func warned(res RestoreOfflineResult, sub string) bool {
	return strings.Contains(strings.Join(res.Warnings, "\n"), sub)
}

// TestRestoreOfflineBringsBackKeyringAndStateFiles, the archive half
// (05-backup §5B.3.4, §5B.9; 05-config §1.12.2, §2.4): --archive with
// --keyring and --gap-fill puts the operator's two files back at the
// state-file table's paths, mode 0600, and sets the live files aside rather
// than deleting them; without the flags the live keyring and gap-fill file
// stay byte-identical (DATA-11); a legacy archive restored without --gap-fill
// warns.
func TestRestoreOfflineBringsBackKeyringAndStateFiles(t *testing.T) {
	t.Run("both files", func(t *testing.T) {
		h := newArchiveHost(t, "legacy-joker_20261112")
		write(t, h.keyring(), "live-keyring", 0o600)
		write(t, h.gap(), "live: gap\n", 0o600)
		exported := write(t, filepath.Join(t.TempDir(), "gotham.keyring"), "exported-keyring", 0o600)
		gapFill := write(t, filepath.Join(t.TempDir(), "config.yaml"), "root_dir: /batcave\n", 0o644)
		res, err := RestoreOffline(h.ctx, h.cfg, RestoreOfflineRequest{Archive: h.archive(), Keyring: exported, GapFill: gapFill, AcceptLoss: true})
		if err != nil {
			t.Fatalf("RestoreOffline = %v", err)
		}
		if got := read(t, h.keyring()); got != "exported-keyring" {
			t.Errorf("keyring = %q, want the export's", got)
		}
		if got := read(t, h.gap()); got != "root_dir: /batcave\n" {
			t.Errorf("gap-fill file = %q, want the operator's", got)
		}
		for _, p := range []string{h.keyring(), h.gap()} {
			if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
				t.Errorf("%s: %v, %v; want mode 0600", p, fi, err)
			}
		}
		// Set aside, not deleted: the replaced files survive in the swap's
		// private directories.
		if got := filesHolding(t, h.state, "live-keyring"); len(got) != 1 {
			t.Errorf("the replaced keyring is kept %d times (%v), want once", len(got), got)
		}
		if got := filesHolding(t, h.state, "live: gap\n"); len(got) != 1 {
			t.Errorf("the replaced gap-fill file is kept %d times (%v), want once", len(got), got)
		}
		if warned(res, "--gap-fill") {
			t.Errorf("warned about a gap-fill file that was given: %v", res.Warnings)
		}
		if got := h.readCave(); got != "1" {
			t.Errorf("cave after the restore = %q, want the archive's 1", got)
		}
	})
	t.Run("neither file", func(t *testing.T) {
		h := newArchiveHost(t, "legacy-joker_20261112")
		write(t, h.keyring(), "live-keyring", 0o600)
		write(t, h.gap(), "live: gap\n", 0o600)
		res, err := RestoreOffline(h.ctx, h.cfg, RestoreOfflineRequest{Archive: h.archive(), AcceptLoss: true})
		if err != nil {
			t.Fatalf("RestoreOffline = %v", err)
		}
		if got := read(t, h.keyring()); got != "live-keyring" {
			t.Errorf("keyring = %q, want the live one kept (DATA-11)", got)
		}
		if got := read(t, h.gap()); got != "live: gap\n" {
			t.Errorf("gap-fill file = %q, want the live one kept", got)
		}
		if !warned(res, "--gap-fill") || !warned(res, ".encryption_key") {
			t.Errorf("a legacy archive without --gap-fill warned %v", res.Warnings)
		}
	})
	t.Run("a core archive needs no gap-fill warning", func(t *testing.T) {
		h := newArchiveHost(t, "01JARCHIVEOFTHEBATCAVE0001")
		res, err := RestoreOffline(h.ctx, h.cfg, RestoreOfflineRequest{Archive: h.archive(), AcceptLoss: true})
		if err != nil {
			t.Fatalf("RestoreOffline = %v", err)
		}
		if warned(res, "--gap-fill") {
			t.Errorf("a mediacore archive warned about the gap-fill file: %v", res.Warnings)
		}
	})
	t.Run("declared paths", func(t *testing.T) {
		h := newArchiveHost(t, "legacy-joker_20261112")
		keyDest := filepath.Join(h.state, "keys", "bat.keyring")
		gapDest := filepath.Join(h.state, "gap", "bat.yaml")
		c := h.cfg
		c.StorageStateFiles = []storage.StateFile{{Label: "keyring", Path: keyDest}, {Label: "gap_fill", Path: gapDest}, {Label: "encryption_key", Path: h.keyFile()}}
		exported := write(t, filepath.Join(t.TempDir(), "k"), "exported-keyring", 0o600)
		gapFill := write(t, filepath.Join(t.TempDir(), "g"), "root_dir: /batcave\n", 0o600)
		if _, err := RestoreOffline(h.ctx, c, RestoreOfflineRequest{Archive: h.archive(), Keyring: exported, GapFill: gapFill, AcceptLoss: true}); err != nil {
			t.Fatalf("RestoreOffline = %v", err)
		}
		if read(t, keyDest) != "exported-keyring" || read(t, gapDest) != "root_dir: /batcave\n" {
			t.Errorf("the files are not at the declared paths")
		}
		if _, err := os.Stat(h.keyring()); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a keyring was also placed at the default path: %v", err)
		}
	})
}

// TestRestoreOfflineRefusesUnusableKeyringAndGapFill: a file that is a link,
// readable by others (the keyring), empty, too large, missing, or relative,
// and --keyring with --checkpoint, are refused with nothing staged and the
// live files untouched, the archive already extracted notwithstanding.
func TestRestoreOfflineRefusesUnusableKeyringAndGapFill(t *testing.T) {
	big := strings.Repeat("g", maxOperatorFileBytes+1)
	for _, tc := range []struct {
		name  string
		build func(t *testing.T, dir string) RestoreOfflineRequest
		want  string
	}{
		{"keyring readable by others", func(t *testing.T, dir string) RestoreOfflineRequest {
			return RestoreOfflineRequest{Keyring: write(t, filepath.Join(dir, "k"), "exported-keyring", 0o644)}
		}, "readable by group or others"},
		{"keyring is a symlink", func(t *testing.T, dir string) RestoreOfflineRequest {
			real := write(t, filepath.Join(dir, "real"), "exported-keyring", 0o600)
			link := filepath.Join(dir, "link")
			if err := os.Symlink(real, link); err != nil {
				t.Fatal(err)
			}
			return RestoreOfflineRequest{Keyring: link}
		}, "not a regular file"},
		{"keyring is empty", func(t *testing.T, dir string) RestoreOfflineRequest {
			return RestoreOfflineRequest{Keyring: write(t, filepath.Join(dir, "k"), "", 0o600)}
		}, "is empty"},
		{"keyring is missing", func(t *testing.T, dir string) RestoreOfflineRequest {
			return RestoreOfflineRequest{Keyring: filepath.Join(dir, "absent")}
		}, "no such file"},
		{"gap-fill is too large", func(t *testing.T, dir string) RestoreOfflineRequest {
			return RestoreOfflineRequest{GapFill: write(t, filepath.Join(dir, "g"), big, 0o600)}
		}, "limit"},
		{"gap-fill is a directory", func(t *testing.T, dir string) RestoreOfflineRequest {
			return RestoreOfflineRequest{GapFill: dir}
		}, "not a regular file"},
		{"relative keyring", func(t *testing.T, _ string) RestoreOfflineRequest {
			return RestoreOfflineRequest{Keyring: "gotham.keyring"}
		}, "not an absolute path"},
		{"relative gap-fill", func(t *testing.T, _ string) RestoreOfflineRequest {
			return RestoreOfflineRequest{GapFill: "config.yaml"}
		}, "not an absolute path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newArchiveHost(t, "legacy-joker_20261112")
			write(t, h.keyring(), "live-keyring", 0o600)
			req := tc.build(t, t.TempDir())
			req.Archive, req.AcceptLoss = h.archive(), true
			_, err := RestoreOffline(h.ctx, h.cfg, req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RestoreOffline = %v, want a refusal containing %q", err, tc.want)
			}
			h.assertNothingStaged()
			if got := read(t, h.keyring()); got != "live-keyring" {
				t.Errorf("live keyring = %q after a refusal", got)
			}
			if got := h.readCave(caveStep(2, "bane")); got != "bane" {
				t.Errorf("cave = %q, want the live store's bane after a refused restore", got)
			}
		})
	}
	t.Run("with a checkpoint", func(t *testing.T) {
		h := newSwapHost(t)
		name := h.failedUpgrade()
		exported := write(t, filepath.Join(t.TempDir(), "k"), "exported-keyring", 0o600)
		for _, req := range []RestoreOfflineRequest{
			{Checkpoint: name, Keyring: exported},
			{Checkpoint: name, GapFill: exported},
		} {
			if _, err := RestoreOffline(h.ctx, h.config(nil), req); err == nil || !strings.Contains(err.Error(), "go with --archive") {
				t.Fatalf("RestoreOffline(%+v) = %v, want a refusal", req, err)
			}
		}
		h.assertNothingStaged()
	})
}

// TestRestoreOfflineIntoCopiesTheFilesBesideTheStore: --into puts them next
// to the copy as a checkpoint's state files are, <into>.state/<label>, and
// touches no live file.
func TestRestoreOfflineIntoCopiesTheFilesBesideTheStore(t *testing.T) {
	h := newArchiveHost(t, "legacy-joker_20261112")
	write(t, h.keyring(), "live-keyring", 0o600)
	exported := write(t, filepath.Join(t.TempDir(), "k"), "exported-keyring", 0o600)
	gapFill := write(t, filepath.Join(t.TempDir(), "g"), "root_dir: /batcave\n", 0o600)
	into := filepath.Join(filepath.Dir(h.db), ".mediacore-checkpoints", "scratch", "rehearsal")
	res, err := RestoreOffline(h.ctx, h.cfg, RestoreOfflineRequest{Archive: h.archive(), Into: into, Keyring: exported, GapFill: gapFill})
	if err != nil {
		t.Fatalf("--into = %v", err)
	}
	state := into + ".state"
	if res.IntoState != state {
		t.Fatalf("IntoState = %q, want %q", res.IntoState, state)
	}
	for label, want := range map[string]string{"keyring": "exported-keyring", "gap_fill": "root_dir: /batcave\n"} {
		p := filepath.Join(state, label)
		if got := read(t, p); got != want {
			t.Errorf("%s = %q, want %q", p, got, want)
		}
		if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o600 {
			t.Errorf("%s: %v, %v; want mode 0600", p, fi, err)
		}
	}
	if got := read(t, h.keyring()); got != "live-keyring" {
		t.Errorf("--into changed the live keyring: %q", got)
	}
	if _, err := os.Stat(h.gap()); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("--into installed a gap-fill file beside the live store: %v", err)
	}
	h.assertNothingStaged()
	if !warned(res, "delete it after the rehearsal") {
		t.Errorf("warnings %v", res.Warnings)
	}

	// A refused file leaves no copy, no state directory and no sidecar behind.
	bad := filepath.Join(filepath.Dir(into), "refused")
	loose := write(t, filepath.Join(t.TempDir(), "loose"), "exported-keyring", 0o644)
	if _, err := RestoreOffline(h.ctx, h.cfg, RestoreOfflineRequest{Archive: h.archive(), Into: bad, Keyring: loose}); err == nil {
		t.Fatal("--into with a keyring readable by others succeeded")
	}
	for _, p := range []string{bad, bad + ".state", bad + restorejournal.LevelsSuffix} {
		if _, err := os.Stat(p); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("%s left behind after a refused --into: %v", p, err)
		}
	}
}
