// file: internal/books/config_strip_boot_test.go
// version: 1.0.0
// guid: c4a19e72-6d03-4f58-b7e1-90d2a58f3c16
// last-edited: 2026-11-20

package books

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// stripAcc is the part of module.ConfigAccess the strip reads: which secrets
// are set (ListSecrets) and a key's effective value (Lookup).
type stripAcc struct {
	module.ConfigAccess
	set     map[secrets.Name]secrets.Meta
	eff     map[config.Path]string
	listed  int
	listErr error
}

func (a *stripAcc) ListSecrets(_ context.Context, prefix string) ([]secrets.Meta, error) {
	a.listed++
	if a.listErr != nil {
		return nil, a.listErr
	}
	var out []secrets.Meta
	for n, m := range a.set {
		if strings.HasPrefix(string(n), prefix) {
			out = append(out, m)
		}
	}
	return out, nil
}

func (a *stripAcc) Lookup(p config.Path) (config.KeyState, bool) {
	v, ok := a.eff[p]
	return config.KeyState{Path: p, Effective: json.RawMessage(v)}, ok
}

// fakeCheckpoints is the latest checkpoint the guard reads.
type fakeCheckpoints struct {
	cp    storage.Checkpoint
	ok    bool
	err   error
	calls int
	// goneFrom, when set, makes the checkpoint vanish from that call on (a
	// sweep deleting it between two writes).
	goneFrom int
}

func (f *fakeCheckpoints) Latest() (storage.Checkpoint, bool, error) {
	f.calls++
	if f.goneFrom != 0 && f.calls >= f.goneFrom {
		return storage.Checkpoint{}, false, nil
	}
	return f.cp, f.ok, f.err
}

// stripRig is a config.yaml, a checkpoint holding a copy of it, and the stores
// that say the keys' destinations hold their values.
type stripRig struct {
	path  string
	acc   *stripAcc
	cps   *fakeCheckpoints
	done  bool
	files cutover.FileOps
}

const stripFileHead = "port: 8080\nopenai_api_key: harley-quinn-token\nroot_dir: /arkham/library\n"

func openAIName(t *testing.T) secrets.Name {
	t.Helper()
	row, ok := cutover.Table().Row("openai_api_key")
	require.True(t, ok)
	return secrets.Name(row.Targets[0].Path)
}

func newStripRig(t *testing.T, fileText string) *stripRig {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.WriteFile(path, []byte(fileText), 0o600))
	cpDir := filepath.Join(dir, "cp")
	require.NoError(t, os.MkdirAll(filepath.Join(cpDir, "state"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(cpDir, "state", gapFillStateLabel), []byte(fileText), 0o600))
	sum := sha256.Sum256([]byte(fileText))
	root, ok := cutover.Table().Row("root_dir")
	require.True(t, ok)
	return &stripRig{
		path: path, done: true,
		cps: &fakeCheckpoints{ok: true, cp: storage.Checkpoint{
			Name: "cp-1", Dir: cpDir, Status: "completed",
			StateFiles: []storage.CheckpointStateFile{{Label: gapFillStateLabel, SHA256: hex.EncodeToString(sum[:])}},
		}},
		acc: &stripAcc{
			set: map[secrets.Name]secrets.Meta{openAIName(t): {Name: openAIName(t), Set: true}},
			eff: map[config.Path]string{config.Path(root.Targets[0].List): `["/arkham/library"]`},
		},
	}
}

func (r *stripRig) strip() *gapFillStrip {
	return &gapFillStrip{
		path: r.path, acc: r.acc, checkpoints: r.cps, files: r.files,
		done: func(context.Context) (bool, error) { return r.done, nil },
	}
}

func (r *stripRig) file(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(r.path)
	require.NoError(t, err)
	return string(b)
}

// leftovers lists what sits beside config.yaml other than the checkpoint
// directory: a strip that refused must leave no temp file behind.
func (r *stripRig) leftovers(t *testing.T) []string {
	t.Helper()
	ents, err := os.ReadDir(filepath.Dir(r.path))
	require.NoError(t, err)
	var out []string
	for _, e := range ents {
		if e.Name() != "config.yaml" && e.Name() != "cp" {
			out = append(out, e.Name())
		}
	}
	return out
}

// TestGapFillStrip_StripsOnlyWhenTheCheckpointHoldsTheFile: with the latest
// checkpoint's copy equal to the file, the managed keys go and the rest stays.
func TestGapFillStrip_StripsOnlyWhenTheCheckpointHoldsTheFile(t *testing.T) {
	r := newStripRig(t, stripFileHead)
	require.NoError(t, r.strip().run(t.Context()))
	got := r.file(t)
	assert.Equal(t, "port: 8080\n", got, "the managed keys are stripped, the operator's own stays")
	assert.Equal(t, 1, r.cps.calls, "the guard consulted the checkpoint once, before the first write")
	assert.Empty(t, r.leftovers(t))
}

// TestGapFillStrip_RefusesWithoutAProvenCheckpoint: every way the checkpoint can
// fail to hold config.yaml byte for byte refuses the strip before it writes, and
// the file is exactly as it was.
func TestGapFillStrip_RefusesWithoutAProvenCheckpoint(t *testing.T) {
	cases := []struct {
		name   string
		break_ func(t *testing.T, r *stripRig)
		want   string
	}{
		{"the file was edited after the checkpoint", func(t *testing.T, r *stripRig) {
			require.NoError(t, os.WriteFile(r.path, []byte(stripFileHead+"setup_complete: true\n"), 0o600))
		}, "differs from checkpoint cp-1"},
		{"no checkpoint exists", func(_ *testing.T, r *stripRig) { r.cps.ok = false }, "no pre-migration checkpoint"},
		{"the checkpoint cannot be listed", func(_ *testing.T, r *stripRig) { r.cps.err = errors.New("permission denied") }, "could not be read"},
		{"the checkpoint was consumed by a restore", func(_ *testing.T, r *stripRig) { r.cps.cp.Status = "consumed" }, "cannot be restored"},
		{"the checkpoint has no copy of config.yaml", func(_ *testing.T, r *stripRig) { r.cps.cp.StateFiles = nil }, "without a copy of config.yaml"},
		{"the copy is gone", func(t *testing.T, r *stripRig) {
			require.NoError(t, os.Remove(r.cps.cp.StateCopy(gapFillStateLabel)))
		}, "could not be read"},
		{"the copy no longer matches its recorded digest", func(t *testing.T, r *stripRig) {
			require.NoError(t, os.WriteFile(r.cps.cp.StateCopy(gapFillStateLabel), []byte("port: 1\n"), 0o600))
		}, "digest"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := newStripRig(t, stripFileHead)
			c.break_(t, r)
			before := r.file(t)
			err := r.strip().run(t.Context())
			require.Error(t, err)
			assert.ErrorIs(t, err, errStripUnguarded)
			assert.Contains(t, err.Error(), c.want)
			assert.Equal(t, before, r.file(t), "a refused strip changed config.yaml")
			assert.Empty(t, r.leftovers(t))
			assert.NotContains(t, err.Error(), "harley-quinn-token", "the refusal carries a value")
		})
	}
}

// TestGapFillStrip_GuardsEveryWayTheStripChangesTheFile: the guard stands in
// front of the rewrite, the in-place rewrite and the removal of the file.
func TestGapFillStrip_GuardsEveryWayTheStripChangesTheFile(t *testing.T) {
	t.Run("rewrite in place when the directory is not writable", func(t *testing.T) {
		r := newStripRig(t, stripFileHead)
		r.files = noTempFiles{cutover.OSFiles()}
		err := r.strip().run(t.Context())
		require.Error(t, err, "an in-place rewrite is reported on /health")
		assert.Contains(t, err.Error(), "in place")
		assert.Equal(t, "port: 8080\n", r.file(t))
	})
	t.Run("in-place rewrite refused without a checkpoint", func(t *testing.T) {
		r := newStripRig(t, stripFileHead)
		r.files = noTempFiles{cutover.OSFiles()}
		r.cps.ok = false
		require.ErrorIs(t, r.strip().run(t.Context()), errStripUnguarded)
		assert.Equal(t, stripFileHead, r.file(t))
	})
	t.Run("in-place rewrite checks again after the temp-file attempt", func(t *testing.T) {
		// The checkpoint is there for the rewrite and gone for the in-place
		// write that follows it: each changing call verifies for itself.
		r := newStripRig(t, stripFileHead)
		r.files = noTempFiles{cutover.OSFiles()}
		r.cps.goneFrom = 2
		require.ErrorIs(t, r.strip().run(t.Context()), errStripUnguarded)
		assert.Equal(t, stripFileHead, r.file(t))
		assert.Equal(t, 2, r.cps.calls)
	})
	t.Run("removal of a file that held only managed keys", func(t *testing.T) {
		only := "openai_api_key: harley-quinn-token\nroot_dir: /arkham/library\n"
		r := newStripRig(t, only)
		require.NoError(t, r.strip().run(t.Context()))
		_, err := os.Stat(r.path)
		assert.ErrorIs(t, err, fs.ErrNotExist, "the strip removes a file it emptied")

		r = newStripRig(t, only)
		r.cps.ok = false
		require.ErrorIs(t, r.strip().run(t.Context()), errStripUnguarded)
		assert.Equal(t, only, r.file(t), "a refused removal left the file")
	})
}

// noTempFiles is a directory the process cannot create files in.
type noTempFiles struct{ cutover.FileOps }

func (noTempFiles) WriteTempFile(string, string, []byte, cutover.FileMeta) (string, error) {
	return "", fs.ErrPermission
}

// TestGapFillStrip_ACleanFileNeedsNoCheckpoint: a file with nothing to strip is
// never compared with the checkpoint, so one that was already stripped (the
// checkpoint's copy still holds the keys) raises nothing on every later boot.
func TestGapFillStrip_ACleanFileNeedsNoCheckpoint(t *testing.T) {
	r := newStripRig(t, "port: 8080\n")
	r.cps.ok = false
	require.NoError(t, r.strip().run(t.Context()))
	assert.Equal(t, 0, r.cps.calls)
	assert.Equal(t, "port: 8080\n", r.file(t))
}

// TestGapFillStrip_WaitsForM2a: before M2a has committed the file is the only
// copy of its keys; nothing is read, stripped or checked.
func TestGapFillStrip_WaitsForM2a(t *testing.T) {
	r := newStripRig(t, stripFileHead)
	r.done = false
	require.NoError(t, r.strip().run(t.Context()))
	assert.Equal(t, stripFileHead, r.file(t))
	assert.Zero(t, r.cps.calls)
	assert.Zero(t, r.acc.listed)

	s := r.strip()
	s.done = func(context.Context) (bool, error) { return false, errors.New("storage closed") }
	require.Error(t, s.run(t.Context()), "an unreadable record is an error, never a quiet strip")
	assert.Equal(t, stripFileHead, r.file(t))
}

// TestGapFillStrip_AKeyWhoseDestinationIsEmptyIsKeptAndReported: the file is
// the only copy, so the key stays and /health says so; database_path alone is
// the expected state and raises nothing.
func TestGapFillStrip_AKeyWhoseDestinationIsEmptyIsKeptAndReported(t *testing.T) {
	r := newStripRig(t, stripFileHead)
	r.acc.set = nil // the OpenAI key was never imported
	err := r.strip().run(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "openai_api_key")
	assert.NotContains(t, err.Error(), "root_dir", "root_dir's destination holds it")
	assert.NotContains(t, err.Error(), "harley-quinn-token")
	assert.Contains(t, r.file(t), "openai_api_key: harley-quinn-token", "the only copy was kept")
	assert.NotContains(t, r.file(t), "root_dir", "a held key was stripped beside it")

	r = newStripRig(t, "port: 8080\ndatabase_path: /gotham/books.pebble\nroot_dir: /arkham/library\n")
	require.NoError(t, r.strip().run(t.Context()), "database_path alone is never a warning")
	assert.Contains(t, r.file(t), "database_path", "database_path is always kept")
	assert.NotContains(t, r.file(t), "root_dir")
}

// TestSecretMetaOf_ExactNameOnly: ListSecrets matches by prefix; the strip must
// not take a sibling secret's record for the key's.
func TestSecretMetaOf_ExactNameOnly(t *testing.T) {
	name := secrets.Name("integrations.ai.openai.api_key")
	acc := &stripAcc{set: map[secrets.Name]secrets.Meta{
		"integrations.ai.openai.api_key_backup": {Name: "integrations.ai.openai.api_key_backup", Set: true},
	}}
	m, err := secretMetaOf{acc}.Meta(t.Context(), name)
	require.NoError(t, err)
	assert.False(t, m.Set, "a prefix sibling is not the key")
	acc.set[name] = secrets.Meta{Name: name, Set: true, Err: secrets.ErrCorrupt}
	m, err = secretMetaOf{acc}.Meta(t.Context(), name)
	require.NoError(t, err)
	assert.True(t, m.Set)
	assert.ErrorIs(t, m.Err, secrets.ErrCorrupt, "an unreadable record's marker passes through")
}

// TestGapFillStrip_ASecretStoreThatCannotBeListedStripsNothing: the strip cannot
// tell what the destinations hold, so it changes nothing, not even the keys it
// could have shown to be held.
func TestGapFillStrip_ASecretStoreThatCannotBeListedStripsNothing(t *testing.T) {
	r := newStripRig(t, stripFileHead)
	r.acc.listErr = errors.New("keyring unavailable")
	err := r.strip().run(t.Context())
	require.Error(t, err)
	assert.Equal(t, stripFileHead, r.file(t))
	assert.Empty(t, r.leftovers(t))
}
