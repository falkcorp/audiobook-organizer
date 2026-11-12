// file: internal/books/cutover/config_cutover_sources_test.go
// version: 1.0.2
// guid: 4a1e7c20-9d36-4b58-b2f4-6c80d1e39a57
// last-edited: 2026-11-11

package cutover

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"log/slog"
	"maps"
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
			"enable_ai_parsing":    "enable_ai_parsing: false\n",
		} {
			r := newRig(t, "stage1", dropSources(), withFile(line))
			if report := requireBlocked(t, r); !strings.Contains(report, key) {
				t.Errorf("%s alone: the refusal does not name it:\n%s", key, report)
			}
		}
	})

	t.Run("enable_ai_parsing: true is HEAD's default, a no-op that loses nothing", func(t *testing.T) {
		r := newRig(t, "stage1", dropSources(), withFile("enable_ai_parsing: true\n"))
		if snap, err := r.run(t); err != nil || snap.State != "done" {
			t.Fatalf("M2 refused over a default (state %v): %v", snap, err)
		}
	})

	t.Run("no usable gap-fill value needs no source: M2 is done and nothing is lost", func(t *testing.T) {
		r := newRig(t, "stage1", dropSources(), withFile("log_level: info\nopenai_api_key: \"\"\nenable_ai_parsing: true\noperator_note: keep-me\n"))
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
		r := newRig(t, "stage1", seedKnown, dropSources("openai_api_key"), withFile("google_books_api_key: "+fxFileGoogle+"\nlog_level: info\n"))
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

// TestConfigCutover_NoBlobFileSettings: with no settings blob, HEAD's effective
// settings include what config.yaml holds, read through viper (the root command
// points viper at the file, and the docs point --config at the file beside the
// database). A value that differs from HEAD's default has nowhere to go, and
// the strip would delete it, so Preflight refuses and names the keys (never the
// values). A value equal to the default loses nothing. The review's probe is
// the first row: a nine-setting file, no blob, no rows, which M2 once let
// through to a strip that deleted the file.
func TestConfigCutover_NoBlobFileSettings(t *testing.T) {
	const probe = "organization_strategy: copy\nfolder_naming_pattern: '{author}/{title}'\nfile_naming_pattern: '{title} - {track:02d}'\nlog_level: debug\nconcurrent_scans: 3\nenable_ai_parsing: false\nscan_on_startup: true\nauto_organize: true\nplaylist_dir: /mnt/gotham-playlists\n"
	for _, tc := range []struct {
		name    string
		file    string
		blocked []string // keys the refusal names; nil = M2 goes on
		absent  []string // keys the refusal must not name
	}{
		{"the review's nine-setting file", probe,
			[]string{"organization_strategy", "folder_naming_pattern", "log_level", "concurrent_scans", "enable_ai_parsing", "scan_on_startup", "playlist_dir"},
			[]string{"file_naming_pattern", "auto_organize"}}, // these two equal HEAD's defaults
		{"organization_strategy off its default", "organization_strategy: copy\n", []string{"organization_strategy"}, nil},
		{"organization_strategy at its default", "organization_strategy: auto\n", nil, nil},
		{"log_level off its default", "log_level: debug\n", []string{"log_level"}, nil},
		{"log_level at its default", "log_level: info\n", nil, nil},
		{"concurrent_scans off its default", "concurrent_scans: 3\n", []string{"concurrent_scans"}, nil},
		{"concurrent_scans at its default, a YAML int", "concurrent_scans: 10\n", nil, nil},
		{"enable_ai_parsing: false", "enable_ai_parsing: false\n", []string{"enable_ai_parsing"}, nil},
		{"enable_ai_parsing: true, the default", "enable_ai_parsing: true\n", nil, nil},
		{"scan_on_startup off its default", "scan_on_startup: true\n", []string{"scan_on_startup"}, nil},
		{"scan_on_startup at its default", "scan_on_startup: false\n", nil, nil},
		{"auto_organize off its default", "auto_organize: false\n", []string{"auto_organize"}, nil},
		{"auto_organize at its default", "auto_organize: true\n", nil, nil},
		{"playlist_dir set", "playlist_dir: /mnt/gotham-playlists\n", []string{"playlist_dir"}, nil},
		{"setup_complete set", "setup_complete: true\n", []string{"setup_complete"}, nil},
		{"folder_naming_pattern off its default", "folder_naming_pattern: '{author}/{title}'\n", []string{"folder_naming_pattern"}, nil},
		{"file_naming_pattern off its default", "file_naming_pattern: '{title}'\n", []string{"file_naming_pattern"}, nil},
		{"a key spelled in another case, which viper reads", "Log_Level: debug\n", []string{"log_level"}, nil},
		{"database_path alone: M2 never stores it, blob or no blob", "database_path: /batcave/db/audiobooks.pebble\n", nil, nil},
		{"null values are unset keys", "log_level:\nplaylist_dir:\n", nil, nil},
		{"a file that is not YAML, which HEAD ignores", ": : [not yaml\n", nil, nil},
		{"keys that are not settings", "operator_note: keep-me\nlistener:\n  addr: 192.0.2.7:8080\n", nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRig(t, "stage1", dropSources(), withFile(tc.file))
			if tc.blocked == nil {
				if snap, err := r.run(t); err != nil || snap.State != "done" {
					t.Fatalf("M2 refused (state %v): %v", snap, err)
				}
				return
			}
			report := requireBlocked(t, r)
			for _, k := range tc.blocked {
				if !strings.Contains(report, k) {
					t.Errorf("the refusal does not name %s:\n%s", k, report)
				}
			}
			for _, k := range tc.absent {
				if strings.Contains(report, k) {
					t.Errorf("the refusal names %s, which equals HEAD's default:\n%s", k, report)
				}
			}
			for _, v := range []string{"gotham-playlists", "debug"} {
				if strings.Contains(report, v) {
					t.Errorf("the refusal carries a value (%q):\n%s", v, report)
				}
			}
			if !strings.Contains(report, "save the settings once") {
				t.Errorf("the refusal has no Fix:\n%s", report)
			}
		})
	}

	t.Run("with a blob the blob shadows the file: no refusal", func(t *testing.T) {
		r := newRig(t, "stage1", withFile(probe))
		if _, err := r.run(t); err != nil {
			t.Fatalf("M2 refused over a file the blob shadows: %v", err)
		}
	})

	t.Run("the strip after a refused upgrade never ran, and the file is whole", func(t *testing.T) {
		r := newRig(t, "stage1", dropSources(), withFile(probe))
		requireBlocked(t, r)
		raw, err := os.ReadFile(r.confFile)
		if err != nil || string(raw) != probe {
			t.Errorf("the file changed under a refused upgrade (err %v)", err)
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
		}), withFile("log_level: info\n"))
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

// realHolder is M2b's holder over a booted environment's real stores: the
// secrets store, books' legacy range and the settings snapshot's effective
// values, as F3-07 wires it.
func realHolder(e *bootEnv) GapFillHolder {
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

// TestStripGapFill_HolderOverRealStores: M2b's holder, built by
// NewGapFillHolder over core's real stores, answers false before M2a has
// imported anything (the strip keeps the file's copy of each secret and of
// enable_ai_parsing) and true for every gap-fill key once it has (the strip
// removes them). database_path is kept either way: M2 never stores it, so no
// destination can hold it. The corpus's config.yaml holds all five gap-fill
// keys, and its
// enable_ai_parsing is HEAD's default (true), which the importer stores as no
// row at all: the holder reads the effective value, so that line is still
// stripped.
func TestStripGapFill_HolderOverRealStores(t *testing.T) {
	ctx := context.Background()
	r := newRig(t, "stage1")
	mem := r.newMem(t)

	holderFor := realHolder
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
	// the file's copy, and database_path's. enable_ai_parsing's effective value
	// is already the file's (true is the default), so there is nothing to lose.
	if want := []string{"database_path", "google_books_api_key", "hardcover_api_token", "openai_api_key"}; !slices.Equal(before.Kept, want) {
		t.Errorf("before M2a the strip kept %v, want %v", before.Kept, want)
	}
	// Every managed key of the corpus's file but database_path is gone once M2a
	// imported them. database_path stays, listed as kept, so the file is
	// rewritten, not removed.
	if !slices.Equal(after.Kept, []string{"database_path"}) || after.Result != StripStripped {
		t.Errorf("after M2a the strip = %+v, want stripped with only database_path kept", after)
	}
	for _, key := range headManagedFileKeys {
		if has := strings.Contains(string(afterFile), key+":"); has != (key == "database_path") {
			t.Errorf("the file holds %s = %v after M2a and the strip, want %v", key, has, key == "database_path")
		}
	}
}

// fakeMeta is the secrets store's Meta, answering set for every name. err is
// the call's error; merr is Meta.Err, the record's own state (an unreadable
// marker or a record that fails to decrypt), which the store reports as a
// result with Set true, not as a call error.
type fakeMeta struct {
	set  bool
	err  error
	merr error
}

func (f fakeMeta) Meta(context.Context, secrets.Name) (secrets.Meta, error) {
	return secrets.Meta{Set: f.set, Err: f.merr}, f.err
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

	t.Run("an unreadable record holds no value", func(t *testing.T) {
		// Set is true for an unreadable marker and for a record whose ciphertext
		// fails to decrypt; neither is a value the file's copy may be dropped for.
		for _, merr := range []error{secrets.ErrKeyUnavailable, secrets.ErrCorrupt} {
			for _, key := range []string{"openai_api_key", "google_books_api_key", "hardcover_api_token"} {
				got, err := held(t, key, fakeMeta{set: true, merr: merr}, nil, nil)
				if err != nil || got {
					t.Errorf("%s with Meta.Err %v: held = %v, %v; want false, nil", key, merr, got, err)
				}
			}
		}
		got, err := held(t, "openai_api_key", fakeMeta{set: true}, nil, nil)
		if err != nil || !got {
			t.Errorf("a readable set record: held = %v, %v; want true", got, err)
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

// TestStripGapFill_UnreadableMarkerKeepsALaterFileSecret: the override
// (AllowKeyRegen) lets M2 go on over rows it cannot decrypt, leaving an
// unreadable marker for each. The marker is not a value: a key an operator then
// adds to config.yaml, hoping it is used, must be kept and listed by the strip,
// not deleted because the secrets store reports the name as set.
func TestStripGapFill_UnreadableMarkerKeepsALaterFileSecret(t *testing.T) {
	r := guardRig(t, withoutKey, withFile("log_level: info\n"), func(r *rig) { r.allowRegen = true })
	mem := r.newMem(t)
	var rep StripReport
	var after string
	var unreadable []string
	err := r.boot(context.Background(), mem, []datamigrate.DataMigration{r.migration()}, func(ctx context.Context, e *bootEnv) error {
		snap, err := read(ctx, e)
		if err != nil {
			return err
		}
		unreadable = slices.Sorted(maps.Keys(snap.Unreadable))
		line := "openai_api_key: " + fxFileOpenAI + "\n"
		if err := os.WriteFile(r.confFile, []byte("log_level: info\n"+line), 0o600); err != nil {
			return err
		}
		rep, err = StripGapFill(ctx, nil, r.confFile, realHolder(e))
		raw, _ := os.ReadFile(r.confFile)
		after = string(raw)
		return err
	})
	if err != nil {
		t.Fatalf("boot: %v", err)
	}
	if !slices.Contains(unreadable, "integrations.ai.openai.api_key") {
		t.Fatalf("the precondition failed: the openai key is not an unreadable marker (unreadable: %v)", unreadable)
	}
	if !slices.Contains(rep.Kept, "openai_api_key") {
		t.Errorf("the strip did not keep the key over an unreadable marker: %+v", rep)
	}
	if !strings.Contains(after, "openai_api_key:") {
		t.Errorf("the strip deleted the operator's key from the file:\n%s", after)
	}
}

// TestHeadParseFileSettings: config.yaml is read the way viper reads it. A key
// matches in any case, a null is an unset key, a YAML int compares equal to the
// default's JSON number, an unparsable file reads as empty, and only the ten
// viper-read keys are returned: database_path is not one (M2 never stores it)
// and neither are the secrets or root_dir (the gap-fill keys have their own
// census).
func TestHeadParseFileSettings(t *testing.T) {
	got := headParseFileSettings([]byte("Log_Level: debug\nCONCURRENT_SCANS: 3\nplaylist_dir:\nscan_on_startup: true\ndatabase_path: /batcave/db\nroot_dir: /mnt/gotham\nopenai_api_key: " + fxFileOpenAI + "\noperator_note: keep-me\n"))
	want := map[string]any{"log_level": "debug", "concurrent_scans": float64(3), "scan_on_startup": true}
	if !maps.Equal(got, want) {
		t.Errorf("headParseFileSettings = %v, want %v", got, want)
	}
	if got := headParseFileSettings([]byte(": : [not yaml\n")); len(got) != 0 {
		t.Errorf("an unparsable file read as %v, want empty", got)
	}
	if got := headParseFileSettings(nil); len(got) != 0 {
		t.Errorf("no file read as %v, want empty", got)
	}
	if slices.Contains(headViperFileKeys, "database_path") {
		t.Error("database_path is in headViperFileKeys: M2 never stores it, so a file's value loses nothing")
	}
	if len(headViperFileKeys) != 10 {
		t.Errorf("headViperFileKeys has %d keys, want 10", len(headViperFileKeys))
	}
}

// TestFileBlobBound: only what loses its value without a blob is named, in
// sorted order. A viper key at HEAD's default loses nothing; root_dir counts
// only when it is a usable value.
func TestFileBlobBound(t *testing.T) {
	in := &m2Input{
		FileHas:      map[string]bool{"root_dir": true},
		FileSettings: map[string]any{"log_level": "debug", "organization_strategy": "auto", "enable_ai_parsing": true, "concurrent_scans": float64(10), "auto_organize": false},
	}
	if got, want := fileBlobBound(in), []string{"auto_organize", "log_level", "root_dir"}; !slices.Equal(got, want) {
		t.Errorf("fileBlobBound = %v, want %v", got, want)
	}
	in.FileSettings["enable_ai_parsing"] = false
	if got, want := fileBlobBound(in), []string{"auto_organize", "enable_ai_parsing", "log_level", "root_dir"}; !slices.Equal(got, want) {
		t.Errorf("fileBlobBound = %v, want %v", got, want)
	}
	if got := fileBlobBound(&m2Input{}); len(got) != 0 {
		t.Errorf("an empty input is bound by %v", got)
	}
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
	t.Run("enable_ai_parsing: false is HEAD's effective value with no blob", func(t *testing.T) {
		_, err := buildPlan(&m2Input{FileSettings: map[string]any{"enable_ai_parsing": false}})
		var nb *noBlobError
		if !errors.As(err, &nb) || !slices.Equal(nb.Keys, []string{"enable_ai_parsing"}) {
			t.Fatalf("buildPlan = %v, want a *noBlobError naming enable_ai_parsing", err)
		}
		_, err = buildPlan(&m2Input{File: headFileValues{RootDir: "/mnt/gotham-library"}, FileHas: has("root_dir"), FileSettings: map[string]any{"enable_ai_parsing": false}})
		if !errors.As(err, &nb) || !slices.Equal(nb.Keys, []string{"enable_ai_parsing", "root_dir"}) {
			t.Fatalf("buildPlan = %v, want both keys named, sorted", err)
		}
	})
	t.Run("a viper-read setting that differs from its default", func(t *testing.T) {
		_, err := buildPlan(&m2Input{FileSettings: map[string]any{"log_level": "debug", "concurrent_scans": float64(3), "organization_strategy": "auto"}})
		var nb *noBlobError
		if !errors.As(err, &nb) || !slices.Equal(nb.Keys, []string{"concurrent_scans", "log_level"}) {
			t.Fatalf("buildPlan = %v, want the two non-default keys named, not the default one", err)
		}
		if strings.Contains(err.Error(), "debug") {
			t.Error("the refusal carries a value")
		}
	})
	t.Run("defaults and enable_ai_parsing: true need no blob", func(t *testing.T) {
		_, err := buildPlan(&m2Input{FileSettings: map[string]any{"enable_ai_parsing": true, "log_level": "info", "concurrent_scans": float64(10)}})
		if err != nil {
			t.Fatalf("buildPlan refused values equal to HEAD's defaults: %v", err)
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
