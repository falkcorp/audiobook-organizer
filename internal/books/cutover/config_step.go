// file: internal/books/cutover/config_step.go
// version: 1.0.0
// guid: 6b15d3f6-9b1f-4a8b-8f43-50f9c0d2b9aa
// last-edited: 2026-11-11

package cutover

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
	"github.com/falkcorp/mediacore/tasks"
)

// M2, config and secrets (03 §3.8 row M2; 05-config §1.12.2): HEAD's settings
// blob moves into core's config store under canonical keys, the plaintext
// credentials of the blob and HEAD's four encrypted rows move into core's
// secrets store, and the blob keeps only its class-i fields for M6. It is a
// cutover data step (Breaking, BootBlocking, behind books' level step) over
// five exact keys: the four encrypted rows and the blob.
//
// One chunk, one batch. The runner steps a step once per present source key,
// in key order, and commits a chunk's writes with its cursor in one batch.
// M2's whole job is one unit, so the first item of each chunk plans it from
// the committed state, applies it into that chunk's batch, and every later
// item of the same chunk counts as already current. A crash leaves HEAD's
// rows or the finished result, never half: and a boot that resumes after the
// chunk committed plans from the committed state again, finds nothing left
// to move, and writes nothing.
//
// M2 never touches the encryption key file. It reads the key to decrypt the
// four rows and the guard below stops the upgrade when a row needs a key
// that is not there.

// configRowNames are the four encrypted rows and the blob, the step's Source
// keys, in key order.
func configSourceKeys() [][]byte {
	names := append([]string{blobRowName}, headSecretRows()...)
	keys := make([][]byte, 0, len(names))
	for _, n := range names {
		keys = append(keys, settingKey(n))
	}
	// Byte order, which is what the runner scans.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && bytes.Compare(keys[j-1], keys[j]) > 0; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}

// ConfigStepOptions are what M2 needs besides the two importers. F3-07 builds
// them from the booted core; the tests build them from a temp directory.
type ConfigStepOptions struct {
	// LevelStepVersion is books' version of the level step (BooksLevelStep)
	// this step runs behind; it must be above 0.
	LevelStepVersion int
	// StateDir is the secure state directory HEAD keeps its settings key in
	// (core.paths.state_dir); <root_dir>/.appdata/ is searched after it.
	StateDir string
	// ConfigFile is the operator's config.yaml next to the database ("" when
	// there is none): M2 reads its gap-fill values; StripGapFill strips it.
	ConfigFile string
	// Legacy reads books' legacy range, the rows M2 plans from. It is called
	// during the step, after the store is open; F3-07 returns
	// db.Legacy("books") through a variable it sets once Prepare has run.
	// Step cannot take it from the runner: the runner hands a step one source
	// pair, not the range.
	Legacy func() storage.Reader
	// AllowKeyRegen reports core.secrets.allow_key_regen: when no key file is
	// found, true lets the step go on and treat the four rows as unreadable
	// (quarantined sealed, their names marked unreadable) instead of
	// stopping the upgrade. nil = false.
	AllowKeyRegen func() bool
	// Files is the filesystem for config.yaml; nil = the OS.
	Files FileOps
	// Logger receives the step's report lines: names, counts and classes,
	// never a value. nil discards.
	Logger *slog.Logger
}

// errConfigStep wraps what M2 refuses outright.
var errConfigStep = errors.New("cutover: config step")

// KeyGuardError is the guard's refusal: a settings row holds an encrypted
// value and no key file was found (or the key could not be read), so the
// upgrade stops before anything is written. It names the file, the
// directories searched and the override; never a value.
type KeyGuardError struct {
	File     string
	Searched []string
	Reason   string
}

func (e *KeyGuardError) Error() string {
	return fmt.Sprintf("%v: an encrypted settings row needs %s, which %s (searched: %s); restore the file there, or set ABO_CORE_SECRETS_ALLOW_KEY_REGEN=true to go on with the rows unreadable (their ciphertext is quarantined sealed and the secrets are marked unreadable)",
		errConfigStep, e.File, e.Reason, strings.Join(e.Searched, ", "))
}

func (e *KeyGuardError) Unwrap() error { return errConfigStep }

// configStep is one M2.
type configStep struct {
	cfg  config.Importer
	sec  secrets.Importer
	opts ConfigStepOptions
	log  *slog.Logger

	mu   sync.Mutex
	last *datamigrate.StepWriter
}

// ConfigStep is M2. cfg and sec are core's importers (config.Store.Importer,
// secrets.Store.Importer); both are valid by the time the migration list
// runs. See ConfigStepOptions.
func ConfigStep(cfg config.Importer, sec secrets.Importer, opts ConfigStepOptions) datamigrate.DataMigration {
	if opts.LevelStepVersion <= 0 {
		panic("cutover: ConfigStepOptions.LevelStepVersion is books' version of its level step")
	}
	if opts.Files == nil {
		opts.Files = osFiles{}
	}
	log := opts.Logger
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	s := &configStep{cfg: cfg, sec: sec, opts: opts, log: log}
	var source []storage.Range
	for _, k := range configSourceKeys() {
		source = append(source, storage.Range{Start: k, End: append(bytes.Clone(k), 0)})
	}
	sensitive := []string{string(blobKey())}
	for _, n := range headSecretRows() {
		sensitive = append(sensitive, string(settingKey(n)))
	}
	return datamigrate.DataMigration{
		ID:              ConfigCutoverID,
		Owner:           BooksOwner,
		Description:     "settings blob and secrets into core's config and secrets stores",
		Source:          source,
		Parallelism:     tasks.Sequential("tens of settings rows"),
		Mode:            datamigrate.BootBlocking,
		Compat:          storage.Breaking,
		RaisesLevel:     BooksCutoverLevel,
		RewritesInPlace: true,
		After:           []storage.StepRef{BooksLevelStepRef(opts.LevelStepVersion)},
		Cutover:         true,
		Sensitive:       sensitive,
		Residual:        [][]byte{blobKey()},
		Preflight:       s.preflight,
		Step:            s.step,
		Verify:          s.verify,
	}
}

// ---- reading the inputs ----

// get reads one absolute key, nil when absent.
func get(ctx context.Context, r storage.Reader, key []byte) ([]byte, error) {
	v, err := r.Get(ctx, key)
	if errors.Is(err, storage.ErrNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return bytes.Clone(v), nil
}

// loadInputs reads everything M2a plans from. A *KeyGuardError is the guard.
func (s *configStep) loadInputs(ctx context.Context, r storage.Reader) (*m2Input, error) {
	in := &m2Input{Rows: map[string]*rowState{}}
	raws := map[string][]byte{}
	for _, name := range headSecretRows() {
		v, err := get(ctx, r, settingKey(name))
		if err != nil {
			return nil, fmt.Errorf("%w: read %s: %v", errConfigStep, settingKey(name), errClass(err))
		}
		raws[name] = v
	}
	blob, err := get(ctx, r, blobKey())
	if err != nil {
		return nil, fmt.Errorf("%w: read the settings blob: %v", errConfigStep, errClass(err))
	}
	in.Blob = blob
	for name, dst := range map[string]*bool{
		headLogRetentionZeroMigratedKey: &in.Markers.LogRetentionZero,
		headEmbedCoverArtMigratedKey:    &in.Markers.EmbedCoverArt,
	} {
		v, err := get(ctx, r, settingKey(name))
		if err != nil {
			return nil, fmt.Errorf("%w: read a marker row: %v", errConfigStep, errClass(err))
		}
		*dst = v != nil
	}
	if v, err := get(ctx, r, settingKey(headMaintenanceMarkerKey)); err != nil {
		return nil, fmt.Errorf("%w: read a marker row: %v", errConfigStep, errClass(err))
	} else if v != nil {
		if row, derr := decodeSettingRow(v); derr == nil && row.Value == "true" {
			in.MaintSet = true
		}
	}
	if s.opts.ConfigFile != "" {
		if data, err := s.opts.Files.ReadFile(s.opts.ConfigFile); err == nil {
			in.File, in.FileHas, _ = headParseGapFill(data)
		}
	}
	if rowsNeedKey(raws) {
		dirs := keyDirs(s.opts.StateDir, peekRootDir(in.Blob, in.File.RootDir))
		found := headFindKey(dirs...)
		file := headKeyFileName
		switch {
		case found == "":
			if s.allowRegen() {
				in.Key = nil
			} else {
				return nil, &KeyGuardError{File: file, Searched: dirs, Reason: "was not found"}
			}
		default:
			k, err := headReadKey(headKeyPath(found))
			if err != nil {
				if !s.allowRegen() {
					return nil, &KeyGuardError{File: file, Searched: dirs, Reason: "could not be read as a 32-byte key"}
				}
			} else {
				in.Key = k
			}
		}
	}
	for _, name := range headSecretRows() {
		in.Rows[name] = readRow(name, raws[name], in.Key)
	}
	return in, nil
}

func (s *configStep) allowRegen() bool { return s.opts.AllowKeyRegen != nil && s.opts.AllowKeyRegen() }

// errClass names an error's class without its text (a storage error's text
// can carry a key).
func errClass(err error) string {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return "the context ended"
	case errors.Is(err, storage.ErrClosed):
		return "storage is closed"
	case errors.Is(err, storage.ErrConflict):
		return "a concurrent write conflicted"
	}
	return "the store refused the operation"
}

// ---- Preflight ----

// preflight stops the upgrade, writing nothing, for the two states HEAD
// itself refuses to run in: a settings blob that does not decode (HEAD:
// "refusing to run on defaults"), and an encrypted row with no key to read it
// (the guard). Everything else M2 imports or quarantines.
func (s *configStep) preflight(ctx context.Context, r storage.Reader) ([]datamigrate.Blocker, error) {
	in, err := s.loadInputs(ctx, r)
	var guard *KeyGuardError
	switch {
	case errors.As(err, &guard):
		return []datamigrate.Blocker{{Key: settingKey(headSecretRows()[0]), Family: "setting", Reason: "an encrypted settings row has no key to read it: " + guard.Reason,
			Fix: fmt.Sprintf("restore %s in one of: %s; or set ABO_CORE_SECRETS_ALLOW_KEY_REGEN=true to go on with the rows unreadable", guard.File, strings.Join(guard.Searched, ", "))}}, nil
	case err != nil:
		return nil, err
	}
	if _, err := buildPlan(in); err != nil {
		return []datamigrate.Blocker{{Key: blobKey(), Family: "setting", Reason: err.Error(),
			Fix: "repair or restore the settings blob on the pre-freeze release: it refuses to run on this blob too"}}, nil
	}
	return nil, nil
}

// ---- Step ----

// step is M2's Step. See the comment on the file.
func (s *configStep) step(ctx context.Context, kv storage.KV, w *datamigrate.StepWriter) error {
	s.mu.Lock()
	if s.last == w {
		s.mu.Unlock()
		w.Count("already-current")
		return nil
	}
	s.last = w
	s.mu.Unlock()

	if s.opts.Legacy == nil {
		return fmt.Errorf("%w: ConfigStepOptions.Legacy is not set", errConfigStep)
	}
	r := s.opts.Legacy()
	if r == nil {
		return fmt.Errorf("%w: books' legacy range is not open yet", errConfigStep)
	}
	in, err := s.loadInputs(ctx, r)
	if err != nil {
		return err
	}
	p, err := buildPlan(in)
	if err != nil {
		return fmt.Errorf("%w: %v", errConfigStep, err)
	}
	if p.empty() {
		w.Count("already-current")
		return nil
	}
	return s.apply(ctx, p, w)
}

// by is the actor recorded on every write M2 stages.
const by = ConfigCutoverID

// apply stages the plan into the chunk's batch. A value config or secrets
// refuses is quarantined, sealed, rather than dropped or failing the chunk;
// any other error (I/O, a closed store) fails the chunk, which commits
// nothing.
func (s *configStep) apply(ctx context.Context, p *plan, w *datamigrate.StepWriter) error {
	b := w.Batch()
	var quarantine []quarantined
	quarantine = append(quarantine, p.Quarantines...)

	for _, sw := range p.Secrets {
		err := s.sec.ImportSecret(ctx, b, secrets.Write{Name: sw.Name, Value: secrets.NewValue(sw.Value), By: by})
		switch {
		case err == nil:
			w.Count("secret-imported")
		case errors.Is(err, storage.ErrImportConflict) && sw.FromFile:
			// config.yaml gap-fills only an empty value, and this name holds one.
			s.log.Info("cutover: config.yaml's value was not applied: the name already holds a value", "step", ConfigCutoverID, "secret", string(sw.Name))
			w.Count("gap-fill-already-present")
		case errors.Is(err, storage.ErrImportConflict):
			quarantine = append(quarantine, quarantined{Key: sw.Source, Raw: []byte(sw.Value), Reason: reasonSecretConflict})
		default:
			return fmt.Errorf("%w: import of %s failed: %v", errConfigStep, sw.Name, errClass(err))
		}
	}
	for _, u := range p.Unreadable {
		if err := s.sec.ImportUnreadable(ctx, b, u.Name, u.Reason, by); err != nil {
			return fmt.Errorf("%w: marking %s unreadable failed: %v", errConfigStep, u.Name, errClass(err))
		}
		w.Count("secret-unreadable")
	}
	for _, st := range p.Stages {
		err := s.cfg.Stage(b, st.Path, st.Value, by)
		switch {
		case err == nil:
			w.Count("key-staged")
		case errors.Is(err, config.ErrUnknownKey):
			quarantine = append(quarantine, quarantined{Key: quarantineKey(st.Member), Raw: st.Original, Reason: reasonRefusedUnknown})
		case errors.Is(err, config.ErrInvalid) && !isFieldError(err):
			quarantine = append(quarantine, quarantined{Key: quarantineKey(st.Member), Raw: st.Original, Reason: reasonRefusedStore})
		case isFieldError(err) || errors.Is(err, config.ErrInvalid):
			quarantine = append(quarantine, quarantined{Key: quarantineKey(st.Member), Raw: st.Original, Reason: reasonRefusedValue})
		default:
			return fmt.Errorf("%w: staging %s failed: %v", errConfigStep, st.Path, errClass(err))
		}
	}
	moved := map[string]bool{} // quarantining a source key deletes it in the same batch
	for _, q := range quarantine {
		if err := w.Quarantine(q.Key, q.Raw, q.Reason); err != nil {
			return fmt.Errorf("%w: quarantine failed: %v", errConfigStep, errClass(err))
		}
		moved[string(q.Key)] = true
		w.Count("quarantined")
	}
	for _, key := range p.DeleteRows {
		if moved[string(key)] {
			continue // a chunk may write a key once, and the quarantine already deleted this row
		}
		if err := w.Delete(key); err != nil {
			return fmt.Errorf("%w: delete failed: %v", errConfigStep, errClass(err))
		}
		w.Count("row-deleted")
	}
	if p.BlobNew != nil {
		if err := w.SetIfUnchanged(p.BlobKey, p.BlobExpect, p.BlobNew); err != nil {
			return fmt.Errorf("%w: blob rewrite failed: %v", errConfigStep, errClass(err))
		}
		w.Count("blob-rewritten")
	}
	for _, name := range p.Dropped {
		s.log.Info("cutover: setting dropped by rule", "step", ConfigCutoverID, "setting", name)
	}
	for _, note := range p.Reported {
		s.log.Info("cutover: "+note, "step", ConfigCutoverID)
	}
	return nil
}

// isFieldError reports a *config.FieldError anywhere in err.
func isFieldError(err error) bool {
	var fe *config.FieldError
	return errors.As(err, &fe)
}

// ---- Verify ----

// verify is the step's invariant gate, over the store alone: the four rows
// are gone and the blob holds no member that moved. That the config oracle
// holds (effective config before = after) is a test, not a runtime check: it
// needs the registry the step does not have.
func (s *configStep) verify(ctx context.Context, r storage.Reader) (datamigrate.VerifyReport, error) {
	var rep datamigrate.VerifyReport
	for _, name := range headSecretRows() {
		v, err := get(ctx, r, settingKey(name))
		if err != nil {
			return rep, err
		}
		if v != nil {
			rep.Failures = append(rep.Failures, fmt.Sprintf("the %s row is still present", name))
		}
	}
	raw, err := get(ctx, r, blobKey())
	if err != nil {
		return rep, err
	}
	if raw == nil {
		return rep, nil
	}
	row, err := decodeSettingRow(raw)
	if err != nil {
		rep.Failures = append(rep.Failures, "the settings blob row is not a settings row")
		return rep, nil
	}
	b, err := headDecodeBlobFields([]byte(row.Value))
	if err != nil {
		rep.Failures = append(rep.Failures, "the settings blob does not decode")
		return rep, nil
	}
	for _, name := range leftoverMembers(b) {
		rep.Failures = append(rep.Failures, name+" is still in the settings blob")
	}
	return rep, nil
}

// headDecodeBlobFields decodes a blob's top-level members into the JSON
// tree, without HEAD's transforms (Verify looks at what is stored).
func headDecodeBlobFields(blob []byte) (map[string]any, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(blob, &fields); err != nil {
		return nil, err
	}
	return decodeTree(fields)
}
