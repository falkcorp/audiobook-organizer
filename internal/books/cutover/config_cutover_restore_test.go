// file: internal/books/cutover/config_cutover_restore_test.go
// version: 1.0.0
// guid: a2d85c19-4e60-4b7f-93a1-5c7e0f284b6d
// last-edited: 2026-11-11

package cutover

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/cockroachdb/pebble/v2/vfs"

	"github.com/falkcorp/mediacore/storage"
)

// restoredDir is where the drill puts the restored store.
const restoredDir = "/batcave/restored"

// sidecars reads the files that sit beside the store (its format and level
// records), which the pre-migration checkpoint does not carry: a restore
// brings the store's own back from the pre-upgrade install.
func sidecars(t testing.TB, fsys vfs.FS) map[string][]byte {
	t.Helper()
	names, err := fsys.List(rigDir)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string][]byte{}
	for _, n := range names {
		if !strings.HasPrefix(n, "audiobooks.pebble.") || strings.HasSuffix(n, ".migration-checkpoint") {
			continue
		}
		f, err := fsys.Open(fsys.PathJoin(rigDir, n))
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		if _, err := buf.ReadFrom(vfsReader{f}); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		out[n] = buf.Bytes()
	}
	return out
}

// vfsReader reads a vfs.File sequentially.
type vfsReader struct{ f vfs.File }

func (r vfsReader) Read(p []byte) (int, error) { return r.f.Read(p) }

// TestConfigCutover_RestoreDrill is the binary-free half of the restore drill
// (05-storage §5S.3.12; 09 §9.3): after M2 ran, the pre-migration checkpoint
// the boot took restores a store the pre-cutover build opens, whose settings
// rows are byte-identical to what they were, whose rows the frozen HEAD codec
// still decrypts to the same credentials with the untouched key file, and
// which holds nothing M2 wrote under core's keyspaces. The other half, the
// pre-freeze release's binary booting the restored store, is F2-15's
// TestRollbackRestoreBootsPreviousBuild (internal/books/restore_drill_test.go),
// which runs M2 as soon as books registers it (F3-07).
func TestConfigCutover_RestoreDrill(t *testing.T) {
	r := newRig(t, "stage1", seedKnown)
	mem := r.newMem(t)
	before, beforeCore := r.dumpAt(t, mem, rigStore)
	side := sidecars(t, mem)
	if len(side) == 0 {
		t.Fatal("the seeded store has no sidecar files: the restore below would open a store without its format record")
	}
	if len(beforeCore) != 0 {
		t.Fatalf("the seeded pre-cutover store already holds %d rows under core's config and secrets", len(beforeCore))
	}
	if _, ok := before["setting:basic_auth_password"]; !ok || len(before) < 6 {
		t.Fatalf("the seeded store holds %d legacy keys and the gate row is present: %v; the byte comparison below needs the rows", len(before), ok)
	}
	keyBefore, err := os.ReadFile(filepath.Join(r.stateDir, ".encryption_key"))
	if err != nil {
		t.Fatal(err)
	}

	snap, err := r.runOn(t, mem)
	if err != nil {
		t.Fatalf("M2: %v", err)
	}
	if _, still := snap.Legacy["setting:basic_auth_password"]; still || len(snap.Secrets) == 0 {
		t.Fatal("M2 left the store as it was: the drill would restore nothing")
	}

	cps, err := storage.CheckpointsOf(storage.FSOf(mem), rigStore, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	cp, ok, err := cps.Latest()
	if err != nil || !ok {
		t.Fatalf("the upgrade boot took no pre-migration checkpoint (ok %v, err %v)", ok, err)
	}
	if !cp.Restorable() {
		t.Fatalf("the checkpoint %s is not restorable (status %s)", cp.Name, cp.Status)
	}

	// The restore: the checkpoint's store copy and the install's own sidecars.
	if err := mem.MkdirAll(restoredDir, 0o700); err != nil {
		t.Fatal(err)
	}
	restored := restoredDir + "/audiobooks.pebble"
	if ok, err := vfs.Clone(mem, mem, mem.PathJoin(cp.Dir, "db"), restored); err != nil || !ok {
		t.Fatalf("copying the checkpoint's store (ok %v): %v", ok, err)
	}
	for n, raw := range side {
		f, err := mem.Create(restoredDir+"/"+n, vfs.WriteCategoryUnspecified)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.Write(raw); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
	}

	got, gotCore := r.dumpAt(t, mem, restored)
	for _, k := range slices.Sorted(keysOfBytes(before)) {
		if g, ok := got[k]; !ok || !bytes.Equal(g, before[k]) {
			t.Errorf("legacy key %s is not byte-identical after the restore (present %v)", k, ok)
		}
	}
	for k := range got {
		if _, ok := before[k]; !ok {
			t.Errorf("legacy key %s appeared in the restored store", k)
		}
	}
	if len(gotCore) != 0 {
		t.Errorf("the restored store holds %d rows under core's config and secrets: M2's writes leaked into the checkpoint", len(gotCore))
	}

	// What the pre-freeze build reads: the four rows decrypt, with the key
	// file M2 never touched, to the credentials they held.
	key, err := os.ReadFile(filepath.Join(r.stateDir, ".encryption_key"))
	if err != nil || !bytes.Equal(key, keyBefore) {
		t.Fatalf("the key file changed (err %v)", err)
	}
	for name, want := range knownRows() {
		s, err := decodeSettingRow(got["setting:"+name])
		if err != nil {
			t.Errorf("row %s does not decode after the restore: %v", name, err)
			continue
		}
		plain, err := headDecrypt(key, s.Value)
		if err != nil || plain != want {
			t.Errorf("row %s no longer decrypts to its credential after the restore (err %v)", name, err)
		}
	}
	if _, err := decodeSettingRow(got[string(blobKey())]); err != nil {
		t.Errorf("the settings blob does not decode after the restore: %v", err)
	}
}

func keysOfBytes(m map[string][]byte) func(yield func(string) bool) {
	return func(yield func(string) bool) {
		for k := range m {
			if !yield(k) {
				return
			}
		}
	}
}
