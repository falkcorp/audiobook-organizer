// file: internal/books/config_strip_boot.go
// version: 1.0.0
// guid: 5b8e2d94-3a17-4c60-9f2b-d1e07a6c3b48
// last-edited: 2026-11-20

package books

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover"
	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
)

// The strip of config.yaml's gap-fill keys (M2b), run by books' repairs hook as
// the Degraded startup step gapFillStrip once the flip is declared and M2a has
// committed. M2a imported the keys' values into core's stores; the file is HEAD's
// gap-fill source, so a value left in it is a second, plaintext copy of a secret
// and a line the retired-name check keeps warning about. The strip keeps any key
// whose destination holds nothing (the file is then the only copy) and always
// keeps database_path.
//
// Rollback past the flip is restore-offline from the pre-migration checkpoint,
// which holds config.yaml (StateFiles' gap_fill, byte for byte). A strip that
// removed a value from the file before that checkpoint was shown to hold the
// file would make the rollback lose it, so the strip writes only when the latest
// checkpoint's copy of config.yaml is byte-identical to the file as it stands
// (checkpointGuard). No config.yaml.prev is kept: the checkpoint is the one
// retained plaintext copy (TestConfigCutover_NoPlaintextSecretOnDisk).

// gapFillStateLabel is the label books' StateFiles gives config.yaml.
const gapFillStateLabel = "gap_fill"

// errStripUnguarded is what the guard refuses with: nothing was written.
var errStripUnguarded = errors.New("books: config.yaml was not stripped")

// latestCheckpoint is the part of *storage.Checkpoints the guard reads.
type latestCheckpoint interface {
	Latest() (cp storage.Checkpoint, ok bool, err error)
}

// checkpointGuard wraps the file operations of the strip: before the first call
// that changes config.yaml (the rewrite, the in-place rewrite, the removal) it
// checks that the latest checkpoint holds the file byte for byte. The strip
// never calls a changing operation when it has nothing to strip, so a file
// that is already clean is not checked and raises nothing.
//
// StripGapFill reports a failed file operation by its class only ("file
// operation failed"), so the guard keeps its own refusal (refused) for run to
// return: the operator needs to read why, and it carries no value.
type checkpointGuard struct {
	cutover.FileOps
	path    string
	cps     latestCheckpoint
	refused error
}

func (g *checkpointGuard) WriteTempFile(dir, pattern string, data []byte, meta cutover.FileMeta) (string, error) {
	if err := g.verify(); err != nil {
		return "", err
	}
	return g.FileOps.WriteTempFile(dir, pattern, data, meta)
}

func (g *checkpointGuard) WriteInPlace(path string, data []byte) error {
	if err := g.verify(); err != nil {
		return err
	}
	return g.FileOps.WriteInPlace(path, data)
}

// Remove guards the removal of config.yaml itself; the strip's removal of a
// temp file an interrupted run left is not a change to it.
func (g *checkpointGuard) Remove(path string) error {
	if path == g.path {
		if err := g.verify(); err != nil {
			return err
		}
	}
	return g.FileOps.Remove(path)
}

// verify is the whole rule: the latest checkpoint is restorable, records
// config.yaml, its copy matches its recorded digest, and the copy equals the
// file now. Every refusal names why and says what to do, never a value.
func (g *checkpointGuard) verify() error {
	err := g.check()
	if err != nil {
		g.refused = err
	}
	return err
}

func (g *checkpointGuard) check() error {
	cur, err := g.FileOps.ReadFile(g.path)
	if err != nil {
		return fmt.Errorf("%w: read config.yaml to compare it with the checkpoint: %v", errStripUnguarded, err)
	}
	cp, ok, err := g.cps.Latest()
	switch {
	case err != nil:
		return fmt.Errorf("%w: the checkpoint directory could not be read: %v", errStripUnguarded, err)
	case !ok:
		return fmt.Errorf("%w: no pre-migration checkpoint holds config.yaml, so rolling back past the flip would lose what a strip removes", errStripUnguarded)
	case !cp.Restorable():
		return fmt.Errorf("%w: the latest checkpoint %s is %s and cannot be restored, so it is no rollback for config.yaml", errStripUnguarded, cp.Name, cp.Status)
	}
	idx := slices.IndexFunc(cp.StateFiles, func(f storage.CheckpointStateFile) bool { return f.Label == gapFillStateLabel })
	if idx < 0 {
		return fmt.Errorf("%w: checkpoint %s was taken without a copy of config.yaml", errStripUnguarded, cp.Name)
	}
	held, err := g.FileOps.ReadFile(cp.StateCopy(gapFillStateLabel))
	if err != nil {
		return fmt.Errorf("%w: checkpoint %s's copy of config.yaml could not be read: %v", errStripUnguarded, cp.Name, err)
	}
	if sum := sha256.Sum256(held); hex.EncodeToString(sum[:]) != cp.StateFiles[idx].SHA256 {
		return fmt.Errorf("%w: checkpoint %s's copy of config.yaml no longer matches the digest recorded when it was taken", errStripUnguarded, cp.Name)
	}
	if !bytes.Equal(held, cur) {
		return fmt.Errorf("%w: config.yaml differs from checkpoint %s's copy (edited since the migration), so a strip would remove a value no checkpoint holds; restore-offline would not bring it back", errStripUnguarded, cp.Name)
	}
	return nil
}

// secretMetaOf is cutover.SecretMeta over the App's secret access: ListSecrets
// returns the metadata of every set secret the manifest admits, never a value,
// so the strip decrypts nothing. A name the manifest does not admit, or that is
// not set, is "holds nothing": the file's value is kept, the safe answer.
type secretMetaOf struct{ acc module.ConfigAccess }

func (s secretMetaOf) Meta(ctx context.Context, n secrets.Name) (secrets.Meta, error) {
	metas, err := s.acc.ListSecrets(ctx, string(n))
	if err != nil {
		return secrets.Meta{}, err
	}
	for _, m := range metas {
		if m.Name == n {
			return m, nil
		}
	}
	return secrets.Meta{Name: n}, nil
}

// gapFillStrip is the strip step's inputs, gathered at Register and Attach.
type gapFillStrip struct {
	path        string // config.yaml beside the database
	acc         module.ConfigAccess
	legacy      storage.Reader
	done        func(ctx context.Context) (bool, error) // M2a committed
	checkpoints latestCheckpoint
	files       cutover.FileOps // nil: the OS
}

// run is the step: nothing before M2a has committed, then StripGapFill through
// the checkpoint guard. An error, or a report that leaves a gap-fill key other
// than database_path or had to write in place, is returned so the Degraded step
// reports it on /health; the log carries names and counts, never a value.
func (s *gapFillStrip) run(ctx context.Context) error {
	done, err := s.done(ctx)
	switch {
	case err != nil:
		return fmt.Errorf("books: read whether M2a committed: %w", err)
	case !done:
		return nil
	}
	files := s.files
	if files == nil {
		files = cutover.OSFiles()
	}
	holder := cutover.NewGapFillHolder(secretMetaOf{s.acc}, s.legacy, func(_ context.Context, p config.Path) (json.RawMessage, error) {
		st, ok := s.acc.Lookup(p)
		if !ok {
			return nil, fmt.Errorf("%s is not a registered key", p)
		}
		return st.Effective, nil
	})
	guard := &checkpointGuard{FileOps: files, path: s.path, cps: s.checkpoints}
	rep, err := cutover.StripGapFill(ctx, guard, s.path, holder)
	if guard.refused != nil {
		return guard.refused
	}
	if err != nil {
		return err
	}
	if rep.Result == cutover.StripStripped || rep.Result == cutover.StripDeleted || rep.TempsRemoved > 0 {
		bootLog.Info("config.yaml %s (kept %d key(s), %d temp file(s) removed)", rep.Result, len(rep.Kept), rep.TempsRemoved)
	}
	var problems []string
	if kept := slices.DeleteFunc(slices.Clone(rep.Kept), func(k string) bool { return k == "database_path" }); len(kept) > 0 {
		problems = append(problems, "it still holds "+strings.Join(kept, ", ")+", whose destination holds no value")
	}
	if rep.InPlace {
		problems = append(problems, "its directory is not writable, so it was rewritten in place (not atomic)")
	}
	if len(problems) > 0 {
		return fmt.Errorf("books: config.yaml's gap-fill keys: %s", strings.Join(problems, "; "))
	}
	return nil
}

// newGapFillStrip builds the step's inputs; admin is the storage admin Attach
// received, whose DataMigrationDone is the M2a gate.
func newGapFillStrip(dbPath string, acc module.ConfigAccess, legacy storage.Reader, admin *storage.Admin, files cutover.FileOps) (*gapFillStrip, error) {
	abs, err := filepath.Abs(dbPath)
	if err != nil {
		return nil, fmt.Errorf("books: database_path %q: %w", dbPath, err)
	}
	cps, err := storage.CheckpointsOf(storage.FS{}, abs, "", nil)
	if err != nil {
		return nil, fmt.Errorf("books: the checkpoint directory: %w", err)
	}
	return &gapFillStrip{
		path: gapFillPath(abs), acc: acc, legacy: legacy, checkpoints: cps, files: files,
		done: func(ctx context.Context) (bool, error) { return admin.DataMigrationDone(ctx, cutover.ConfigCutoverID) },
	}, nil
}

// gapFillInputs is what Register hands the strip.
type gapFillInputs struct {
	acc    module.ConfigAccess
	legacy storage.Reader
}

// stripGapFill is the gapFillStrip step: it does nothing unless the flip is
// declared and the Facade bound (the inputs were stored), and, without the
// storage admin that answers whether M2a committed, it cannot know and so does
// not strip.
func (m *booksModule) stripGapFill(ctx context.Context) error {
	in := m.gapFillIn.Load()
	if in == nil || !m.declaresFlip() {
		return nil
	}
	admin := m.storageAdmin.Load()
	if admin == nil {
		bootLog.Warn("config.yaml's gap-fill keys were not stripped: the storage admin is not attached, so whether M2a committed is unknown")
		return nil
	}
	s, err := newGapFillStrip(m.dbPath, in.acc, in.legacy, admin, nil)
	if err != nil {
		return err
	}
	return s.run(ctx)
}
