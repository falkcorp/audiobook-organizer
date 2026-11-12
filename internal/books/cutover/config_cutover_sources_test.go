// file: internal/books/cutover/config_cutover_sources_test.go
// version: 1.0.0
// guid: 4a1e7c20-9d36-4b58-b2f4-6c80d1e39a57
// last-edited: 2026-11-11

package cutover

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/config"
	"github.com/falkcorp/mediacore/secrets"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/datamigrate"
)

// The runner steps M2 once per present source key (the blob and the four
// rows). An install with none of them never runs it, and M2b would then strip
// config.yaml's values from a file nothing imported them from. These tests are
// the review's probe for that (TestProbeNoBlobButConfigYAML), and the census
// that stops it.

// dropSources deletes the settings blob and the four encrypted rows from the
// seeded store, except the rows named in keep.
func dropSources(keep ...string) func(*rig) {
	return func(r *rig) {
		prev := r.mutate
		r.mutate = func(t testing.TB, lr storage.LegacyRange) {
			if prev != nil {
				prev(t, lr)
			}
			for _, n := range append([]string{blobRowName}, headSecretRows()...) {
				if slices.Contains(keep, n) {
					continue
				}
				if err := lr.Delete(context.Background(), settingKey(n)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

// withRows adds plain settings rows to the seeded store.
func withRows(rows map[string]string) func(*rig) {
	return func(r *rig) {
		prev := r.mutate
		r.mutate = func(t testing.TB, lr storage.LegacyRange) {
			if prev != nil {
				prev(t, lr)
			}
			for name, v := range rows {
				if err := lr.Set(context.Background(), settingKey(name), settingRow(name, v, "string", false)); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
}

// withFile replaces the rig's config.yaml.
func withFile(content string) func(*rig) {
	return func(r *rig) {
		if r.confFile == "" {
			r.confFile = r.t.TempDir() + "/config.yaml"
		}
		if err := os.WriteFile(r.confFile, []byte(content), 0o600); err != nil {
			r.t.Fatal(err)
		}
	}
}

// requireBlocked boots r and requires the cutover preflight to refuse with
// nothing written; it returns the refusal's report (the error and the log).
func requireBlocked(t *testing.T, r *rig) string {
	t.Helper()
	mem := r.newMem(t)
	before := r.dump(t, mem)
	_, err := r.runOn(t, mem)
	if !errors.Is(err, storage.ErrCutoverBlocked) {
		t.Fatalf("boot error = %v, want storage.ErrCutoverBlocked", err)
	}
	if _, after := changedOnly(before, r.dump(t, mem)); len(after) != 0 {
		t.Errorf("a refused boot changed the store: %v", after)
	}
	report := err.Error() + r.logs.String()
	for _, v := range []string{fxFileOpenAI, fxFileGoogle, fxFileHardc, "gotham-library"} {
		if strings.Contains(report, v) {
			t.Errorf("the refusal carries a value (%q):\n%s", v, report)
		}
	}
	return report
}

const handWritten = "root_dir: /mnt/gotham-library\nopenai_api_key: " + fxFileOpenAI + "\nhardcover_api_token: " + fxFileHardc + "\n"

func TestConfigCutover_NoSourceRow(t *testing.T) {
	t.Run("config.yaml values and no row to run M2 for refuse the upgrade", func(t *testing.T) {
		r := newRig(t, "stage1", dropSources(), withFile(handWritten))
		report := requireBlocked(t, r)
		for _, want := range []string{"root_dir", "openai_api_key", "hardcover_api_token", "settings blob", "Settings, Save"} {
			if !strings.Contains(report, want) {
				t.Errorf("the refusal does not say %q:\n%s", want, report)
			}
		}
	})

	t.Run("each gap-fill key alone refuses, by name", func(t *testing.T) {
		for key, line := range map[string]string{
			"openai_api_key":       "openai_api_key: " + fxFileOpenAI + "\n",
			"google_books_api_key": "google_books_api_key: " + fxFileOpenAI + "\n",
			"hardcover_api_token":  "hardcover_api_token: " + fxFileOpenAI + "\n",
			"root_dir":             "root_dir: /mnt/gotham-library\n",
			"enable_ai_parsing":    "enable_ai_parsing: true\n",
		} {
			r := newRig(t, "stage1", dropSources(), withFile(line))
			if report := requireBlocked(t, r); !strings.Contains(report, key) {
				t.Errorf("%s alone: the refusal does not name it:\n%s", key, report)
			}
		}
	})

	t.Run("no usable gap-fill value needs no source: M2 is done and nothing is lost", func(t *testing.T) {
		r := newRig(t, "stage1", dropSources(), withFile("log_level: debug\nopenai_api_key: \"\"\nenable_ai_parsing: false\noperator_note: keep-me\n"))
		snap, err := r.run(t)
		if err != nil {
			t.Fatalf("M2: %v", err)
		}
		if snap.State != "done" || len(snap.Secrets) != 0 {
			t.Errorf("state %q, %d secrets; want done and none", snap.State, len(snap.Secrets))
		}
	})

	t.Run("no file at all needs no source", func(t *testing.T) {
		r := newRig(t, "stage1", dropSources(), func(r *rig) { r.confFile = "" })
		if snap, err := r.run(t); err != nil || snap.State != "done" {
			t.Fatalf("M2: state %v, err %v", snap, err)
		}
	})

	t.Run("a blob makes every gap-fill value importable: no refusal", func(t *testing.T) {
		r := newRig(t, "stage1", withFile(handWritten))
		if _, err := r.run(t); err != nil {
			t.Fatalf("M2: %v", err)
		}
	})

	t.Run("no blob, a secret row, and config.yaml's root_dir: it has nowhere to go", func(t *testing.T) {
		r := newRig(t, "stage1", seedKnown, dropSources("openai_api_key"), withFile("root_dir: /mnt/gotham-library\n"))
		if report := requireBlocked(t, r); !strings.Contains(report, "root_dir") {
			t.Errorf("the refusal does not name root_dir:\n%s", report)
		}
	})

	t.Run("no blob, a secret row, and config.yaml's other secrets: M2 runs and imports them", func(t *testing.T) {
		r := newRig(t, "stage1", seedKnown, dropSources("openai_api_key"), withFile("google_books_api_key: "+fxFileGoogle+"\nlog_level: debug\n"))
		snap, err := r.run(t)
		if err != nil {
			t.Fatalf("M2: %v", err)
		}
		if got := snap.Secrets["integrations.ai.openai.api_key"]; got != fxOpenAI {
			t.Errorf("the row's secret was not imported (readable: %v)", got != "")
		}
		if got := snap.Secrets["integrations.metadata.google-books.api_key"]; got != fxFileGoogle {
			t.Errorf("config.yaml's secret was not imported (readable: %v)", got != "")
		}
		if _, still := snap.Legacy["setting:"+blobRowName]; still {
			t.Error("a blob appeared from nowhere")
		}
	})
}

// TestConfigCutover_LegacyPerKeyRows: HEAD reads per-key rows when there is no
// blob (LoadConfigFromDatabase's legacy path), so an install from before the
// blob has settings M2 would leave behind. Only the names applySetting maps
// onto a field count: HEAD keeps many other rows under setting: (maintenance
// flags, the pause gate), which no step of M2 owns.
func TestConfigCutover_LegacyPerKeyRows(t *testing.T) {
	t.Run("per-key rows and no blob refuse, naming the rows", func(t *testing.T) {
		r := newRig(t, "stage1", dropSources(), withRows(map[string]string{"root_dir": "/mnt/gotham-library", "scan_on_startup": "true"}), withFile("log_level: debug\n"))
		report := requireBlocked(t, r)
		for _, want := range []string{"2 per-key settings row(s)", "root_dir", "scan_on_startup", "Settings, Save"} {
			if !strings.Contains(report, want) {
				t.Errorf("the refusal does not say %q:\n%s", want, report)
			}
		}
		if strings.Contains(report, "gotham-library") {
			t.Error("the refusal carries a row's value")
		}
	})

	t.Run("more than five rows are counted and five shown", func(t *testing.T) {
		rows := map[string]string{}
		for _, k := range []string{"root_dir", "scan_on_startup", "auto_organize", "log_level", "concurrent_scans", "playlist_dir", "setup_complete"} {
			rows[k] = "x"
		}
		r := newRig(t, "stage1", dropSources(), withRows(rows), withFile("log_level: debug\n"))
		if report := requireBlocked(t, r); !strings.Contains(report, "7 per-key settings row(s)") {
			t.Errorf("the refusal does not count seven:\n%s", report)
		}
	})

	t.Run("with a blob the per-key rows are stale and HEAD ignores them too", func(t *testing.T) {
		r := newRig(t, "stage1", withRows(map[string]string{"root_dir": "/stale/path"}))
		if _, err := r.run(t); err != nil {
			t.Fatalf("M2 refused over a stale row beside a blob: %v", err)
		}
	})

	t.Run("state rows HEAD keeps under setting: are not settings", func(t *testing.T) {
		r := newRig(t, "stage1", dropSources(), withRows(map[string]string{
			"purge_legacy_fp_done": "true", "remux_done": "true", "log_retention_zero_migrated": "true",
			"maintenance_window_migrated": "true", "embed_cover_art_default_on_migrated": "true",
		}), withFile("log_level: debug\n"))
		snap, err := r.run(t)
		if err != nil || snap.State != "done" {
			t.Fatalf("M2 refused over state rows (state %v): %v", snap, err)
		}
	})

	t.Run("the frozen key list holds no secret row and no internal row", func(t *testing.T) {
		for _, n := range headSecretRows() {
			if headLegacySettingKeys[n] {
				t.Errorf("%s is one of the four encrypted rows, which are source rows, not legacy per-key rows", n)
			}
		}
		for _, n := range []string{"maintenance_window_migrated", "maintenance_window_last_run", "maintenance_window_update_completed", "config_blob"} {
			if headLegacySettingKeys[n] {
				t.Errorf("%s is internal state, not a setting", n)
			}
		}
		if len(headLegacySettingKeys) != 127 {
			t.Errorf("%d legacy keys, want the 127 applySetting maps onto fields", len(headLegacySettingKeys))
		}
	})
}

// unreadableFile is a config.yaml read that fails.
type unreadableFile struct{ err error }

func (u unreadableFile) ReadFile(string) ([]byte, error)     { return nil, u.err }
func (u unreadableFile) Resolve(p string) (string, error)    { return p, nil }
func (u unreadableFile) Stat(string) (FileMeta, bool, error) { return FileMeta{}, false, nil }
func (u unreadableFile) WriteTempFile(string, string, []byte, FileMeta) (string, error) {
	return "", u.err
}
func (u unreadableFile) WriteInPlace(string, []byte) error { return u.err }
func (u unreadableFile) Rename(string, string) error       { return u.err }
func (u unreadableFile) Remove(string) error               { return u.err }
func (u unreadableFile) SyncDir(string) error              { return u.err }
func (u unreadableFile) ReadDir(string) ([]string, error)  { return nil, u.err }

// emptyReader is a store with no rows.
type emptyReader struct{ storage.Reader }

func (emptyReader) Get(context.Context, []byte) ([]byte, error) { return nil, storage.ErrNotFound }

// TestConfigCutover_ConfigFileReadError: a config.yaml that exists and cannot
// be read is not "no file". Read as empty, M2 would skip its gap-fill imports
// and M2b would strip the file on a later boot, deleting values nothing
// imported.
func TestConfigCutover_ConfigFileReadError(t *testing.T) {
	denied := &fs.PathError{Op: "open", Path: "config.yaml", Err: fs.ErrPermission}

	t.Run("the preflight refuses with the file's name and a way out", func(t *testing.T) {
		r := newRig(t, "stage1", withFile(handWritten), func(r *rig) { r.files = unreadableFile{err: denied} })
		report := requireBlocked(t, r)
		for _, want := range []string{"config.yaml", "cannot be read", "permission denied", "readable by the service user"} {
			if !strings.Contains(report, want) {
				t.Errorf("the refusal does not say %q:\n%s", want, report)
			}
		}
	})

	t.Run("a read that is not a permission error refuses too", func(t *testing.T) {
		r := newRig(t, "stage1", withFile(handWritten), func(r *rig) { r.files = unreadableFile{err: errors.New("i/o error")} })
		if report := requireBlocked(t, r); !strings.Contains(report, "cannot be read") {
			t.Errorf("the refusal does not say the file cannot be read:\n%s", report)
		}
	})

	t.Run("a missing file is no file", func(t *testing.T) {
		r := newRig(t, "stage1", func(r *rig) {
			r.files = unreadableFile{err: &fs.PathError{Op: "open", Path: "config.yaml", Err: fs.ErrNotExist}}
		})
		if _, err := r.run(t); err != nil {
			t.Fatalf("M2 refused over a file that is not there: %v", err)
		}
	})

	t.Run("the step itself fails the chunk, as a resumed cutover that skips the preflight would", func(t *testing.T) {
		s := &configStep{opts: ConfigStepOptions{ConfigFile: "config.yaml", Files: unreadableFile{err: denied}}}
		_, err := s.loadInputs(context.Background(), emptyReader{})
		var fe *ConfigFileError
		if !errors.As(err, &fe) || !errors.Is(err, errConfigStep) {
			t.Fatalf("loadInputs = %v, want a *ConfigFileError", err)
		}
		if fe.Reason != "permission denied" || strings.Contains(err.Error(), "config.yaml\"") {
			t.Errorf("reason %q, text %q", fe.Reason, err.Error())
		}
	})
}

// outcomeSink is a StepWriter's chunk that only counts.
type outcomeSink struct{ counts map[string]int }

func (outcomeSink) Set(_, _ []byte) error                  { return nil }
func (outcomeSink) Delete([]byte) error                    { return nil }
func (outcomeSink) SetIfAbsent(_, _ []byte) error          { return nil }
func (outcomeSink) SetIfUnchanged(_, _, _ []byte) error    { return nil }
func (outcomeSink) DeleteIfUnchanged(_, _ []byte) error    { return nil }
func (outcomeSink) Hold(_ []byte, _ string)                {}
func (o outcomeSink) Count(outcome string)                 { o.counts[outcome]++ }
func (outcomeSink) Quarantine(_, _ []byte, _ string) error { return nil }
func (outcomeSink) Batch() *storage.Batch                  { return nil }

// TestConfigStep_ApplyCountsThePlansOutcomes: the planner's own outcomes
// (userinfo-record, recovered-from-config-yaml) reach the runner's per-step
// counts. They were collected into the plan and never flushed, so no operator
// could see them.
func TestConfigStep_ApplyCountsThePlansOutcomes(t *testing.T) {
	logs := &lockedBuffer{}
	s := &configStep{log: slog.New(slog.NewTextHandler(logs, nil))}
	sink := outcomeSink{counts: map[string]int{}}
	p := &plan{
		Counts:   map[string]int{"recovered-from-config-yaml": 2, "userinfo-record": 1},
		Reported: []string{"secret openai_api_key recovered from config.yaml after an undecryptable row"},
	}
	if err := s.apply(context.Background(), p, datamigrate.NewStepWriter(sink, sink)); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if sink.counts["recovered-from-config-yaml"] != 2 || sink.counts["userinfo-record"] != 1 {
		t.Errorf("counts = %v, want recovered-from-config-yaml 2 and userinfo-record 1", sink.counts)
	}
	if !strings.Contains(logs.String(), "recovered from config.yaml after an undecryptable row") {
		t.Errorf("the report line was not logged:\n%s", logs.String())
	}
}

// TestStripGapFill_HolderOverRealStores: M2b's holder, built by
// NewGapFillHolder over core's real stores, answers false before M2a has
// imported anything (the strip keeps the file's copy of each secret and of
// enable_ai_parsing) and true for every gap-fill key once it has (the strip
// removes them). The corpus's config.yaml holds all five gap-fill keys, and its
// enable_ai_parsing is HEAD's default (true), which the importer stores as no
// row at all: the holder reads the effective value, so that line is still
// stripped.
func TestStripGapFill_HolderOverRealStores(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, "stage1")
	mem := r.newMem(t)

	holderFor := func(e *bootEnv) GapFillHolder {
		effective := func(ctx context.Context, p config.Path) (json.RawMessage, error) {
			snap, err := read(ctx, e)
			if err != nil {
				return nil, err
			}
			v, _ := snap.Effective(string(p))
			return v, nil
		}
		legacy, _ := e.db.Legacy(BooksOwner)
		return NewGapFillHolder(e.sec, legacy, effective)
	}
	copyFile := func(t *testing.T) string {
		t.Helper()
		raw, err := os.ReadFile(r.confFile)
		if err != nil {
			t.Fatal(err)
		}
		p := t.TempDir() + "/config.yaml"
		if err := os.WriteFile(p, raw, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	var before, after StripReport
	var afterFile []byte
	r.preMigrate = func(ctx context.Context, e *bootEnv) error {
		var err error
		before, err = StripGapFill(ctx, nil, copyFile(t), holderFor(e))
		return err
	}
	err := r.boot(ctx, mem, []datamigrate.DataMigration{r.migration()}, func(ctx context.Context, e *bootEnv) error {
		var err error
		after, err = StripGapFill(ctx, nil, r.confFile, holderFor(e))
		afterFile, _ = os.ReadFile(r.confFile)
		return err
	})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	// Before M2a the three secrets' destinations are empty: the strip keeps
	// the file's copy. enable_ai_parsing's effective value is already the
	// file's (true is the default), so there is nothing to lose.
	if want := []string{"google_books_api_key", "hardcover_api_token", "openai_api_key"}; !slices.Equal(before.Kept, want) {
		t.Errorf("before M2a the strip kept %v, want %v", before.Kept, want)
	}
	// Every managed key of the corpus's file is gone once M2a imported them,
	// and the file held nothing else the strip would leave: it is removed.
	if len(after.Kept) != 0 || (after.Result != StripStripped && after.Result != StripDeleted) {
		t.Errorf("after M2a the strip = %+v, want stripped or deleted with nothing kept", after)
	}
	for _, key := range headManagedFileKeys {
		if strings.Contains(string(afterFile), key+":") {
			t.Errorf("the file still holds %s after M2a and the strip", key)
		}
	}
}

// fakeMeta is the secrets store's Meta, answering set for every name.
type fakeMeta struct {
	set bool
	err error
}

func (f fakeMeta) Meta(context.Context, secrets.Name) (secrets.Meta, error) {
	return secrets.Meta{Set: f.set}, f.err
}

// TestNewGapFillHolder_ReadsEffectiveValues: the config keys are held by their
// effective value (stored or default), never by the presence of a row, since
// the importer skips a value equal to its default.
func TestNewGapFillHolder_ReadsEffectiveValues(t *testing.T) {
	ctx := context.Background()
	effective := func(m map[string]string) func(context.Context, config.Path) (json.RawMessage, error) {
		return func(_ context.Context, p config.Path) (json.RawMessage, error) {
			v, ok := m[string(p)]
			if !ok {
				return nil, errors.New("no such key: " + fxFileOpenAI)
			}
			return json.RawMessage(v), nil
		}
	}
	held := func(t *testing.T, key string, sec SecretMeta, legacy storage.Reader, eff map[string]string) (bool, error) {
		t.Helper()
		return NewGapFillHolder(sec, legacy, effective(eff))(ctx, key)
	}
	const roots, aiParsing = "core.library.roots", "modules.books.ai.enable_ai_parsing"

	for _, tc := range []struct {
		name string
		key  string
		eff  map[string]string
		want bool
	}{
		{"enable_ai_parsing effectively true (the default)", "enable_ai_parsing", map[string]string{aiParsing: "true"}, true},
		{"enable_ai_parsing effectively false", "enable_ai_parsing", map[string]string{aiParsing: "false"}, false},
		{"enable_ai_parsing null", "enable_ai_parsing", map[string]string{aiParsing: "null"}, false},
		{"root_dir with a root listed", "root_dir", map[string]string{roots: `[{"path":"/mnt/gotham"}]`}, true},
		{"root_dir with an empty list", "root_dir", map[string]string{roots: "[]"}, false},
		{"root_dir with null", "root_dir", map[string]string{roots: "null"}, false},
		{"root_dir with a value that is no list", "root_dir", map[string]string{roots: `"/mnt/gotham"`}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := held(t, tc.key, fakeMeta{}, nil, tc.eff)
			if err != nil || got != tc.want {
				t.Errorf("held = %v, %v; want %v", got, err, tc.want)
			}
		})
	}

	t.Run("a secret is held when its name is set", func(t *testing.T) {
		for _, set := range []bool{true, false} {
			got, err := held(t, "openai_api_key", fakeMeta{set: set}, nil, nil)
			if err != nil || got != set {
				t.Errorf("Meta.Set = %v: held = %v, %v", set, got, err)
			}
		}
	})

	t.Run("a read error is an error, never a held value, and carries no text", func(t *testing.T) {
		for _, key := range []string{"enable_ai_parsing", "root_dir"} {
			got, err := held(t, key, fakeMeta{}, nil, nil)
			if err == nil || got || !errors.Is(err, errConfigStep) || strings.Contains(err.Error(), fxFileOpenAI) {
				t.Errorf("%s: held = %v, err = %v", key, got, err)
			}
		}
		got, err := held(t, "openai_api_key", fakeMeta{err: errors.New(fxFileOpenAI)}, nil, nil)
		if err == nil || got || strings.Contains(err.Error(), fxFileOpenAI) {
			t.Errorf("secret: held = %v, err = %v", got, err)
		}
	})

	t.Run("a key that is not gap-fill is refused", func(t *testing.T) {
		if _, err := held(t, "log_level", fakeMeta{}, nil, nil); err == nil {
			t.Error("a non-gap-fill key was answered")
		}
	})
}

// TestConfigPlan_NoBlobIsRefusedByThePlanner: Preflight's census stops the
// no-blob shapes first, but a resumed cutover skips Preflight, and the chunk
// must then fail with names and write nothing rather than plan a blob it does
// not have.
func TestConfigPlan_NoBlobIsRefusedByThePlanner(t *testing.T) {
	has := func(keys ...string) map[string]bool {
		m := map[string]bool{}
		for _, k := range keys {
			m[k] = true
		}
		return m
	}
	t.Run("a root_dir and nothing to receive it", func(t *testing.T) {
		_, err := buildPlan(&m2Input{File: headFileValues{RootDir: "/mnt/gotham-library"}, FileHas: has("root_dir")})
		var nb *noBlobError
		if !errors.As(err, &nb) || !slices.Equal(nb.Keys, []string{"root_dir"}) {
			t.Fatalf("buildPlan = %v, want a *noBlobError naming root_dir", err)
		}
		if strings.Contains(err.Error(), "gotham-library") {
			t.Error("the refusal carries the value")
		}
	})
	t.Run("enable_ai_parsing too", func(t *testing.T) {
		_, err := buildPlan(&m2Input{File: headFileValues{EnableAIParsing: true}, FileHas: has("root_dir", "enable_ai_parsing")})
		var nb *noBlobError
		if !errors.As(err, &nb) || !slices.Equal(nb.Keys, []string{"root_dir", "enable_ai_parsing"}) {
			t.Fatalf("buildPlan = %v, want both keys named", err)
		}
	})
	t.Run("file secrets alone need no blob", func(t *testing.T) {
		p, err := buildPlan(&m2Input{File: headFileValues{OpenAIAPIKey: fxFileOpenAI}, FileHas: has("openai_api_key")})
		if err != nil {
			t.Fatalf("buildPlan: %v", err)
		}
		if len(p.Secrets) != 1 || p.Secrets[0].Name != "integrations.ai.openai.api_key" || !p.Secrets[0].FromFile {
			t.Errorf("secrets = %+v, want the file's OpenAI key imported", p.Secrets)
		}
	})
}
