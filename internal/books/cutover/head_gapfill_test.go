// file: internal/books/cutover/head_gapfill_test.go
// version: 1.0.0
// guid: 28813d37-3158-4d74-b977-a1df6e7d55f9
// last-edited: 2026-11-11

package cutover

import (
	"reflect"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// topLevelTagNames returns every json and mapstructure tag name of headConfig's
// top-level fields.
func topLevelTagNames() map[string]bool {
	names := map[string]bool{}
	t := reflect.TypeOf(headConfig{})
	for i := 0; i < t.NumField(); i++ {
		for _, tag := range []string{"json", "mapstructure"} {
			if v, ok := t.Field(i).Tag.Lookup(tag); ok {
				if name, _, _ := strings.Cut(v, ","); name != "" && name != "-" {
					names[name] = true
				}
			}
		}
	}
	return names
}

func TestHeadManagedFileKeys(t *testing.T) {
	if len(headManagedFileKeys) != 15 {
		t.Fatalf("len = %d, want 15", len(headManagedFileKeys))
	}
	tags := topLevelTagNames()
	secrets := headSecretRows()
	seen := map[string]bool{}
	for _, k := range headManagedFileKeys {
		if seen[k] {
			t.Errorf("duplicate key %q", k)
		}
		seen[k] = true
		if !tags[k] && !slices.Contains(secrets, k) {
			t.Errorf("%q is neither a headConfig tag name nor a secret row", k)
		}
	}
	// 12 settings then 3 secrets, in HEAD's order.
	for _, k := range headManagedFileKeys[12:] {
		if !slices.Contains(secrets, k) {
			t.Errorf("%q should be a secret row", k)
		}
	}
}

func TestHeadParseGapFillReadsExactlyFiveKeys(t *testing.T) {
	data := []byte(`
openai_api_key: bat-openai-fixture
google_books_api_key: bat-books-fixture
hardcover_api_token: bat-hardcover-fixture
root_dir: /gotham/library
enable_ai_parsing: true
database_path: /gotham/db
playlist_dir: /gotham/playlists
setup_complete: true
organization_strategy: copy
scan_on_startup: true
auto_organize: false
folder_naming_pattern: "{author}"
file_naming_pattern: "{title}"
concurrent_scans: 4
log_level: debug
basic_auth_password: ignored-fixture
`)
	v, present, err := headParseGapFill(data)
	if err != nil {
		t.Fatal(err)
	}
	want := headFileValues{OpenAIAPIKey: "bat-openai-fixture", GoogleBooksAPIKey: "bat-books-fixture", HardcoverAPIToken: "bat-hardcover-fixture", RootDir: "/gotham/library", EnableAIParsing: true}
	if v != want {
		t.Fatalf("values = %+v", v)
	}
	if len(present) != 5 {
		t.Fatalf("present = %v", present)
	}
	for _, k := range []string{"openai_api_key", "google_books_api_key", "hardcover_api_token", "root_dir", "enable_ai_parsing"} {
		if !present[k] {
			t.Errorf("%q not recorded", k)
		}
	}
}

func TestHeadParseGapFillUsableValuesOnly(t *testing.T) {
	// Empty strings, false, wrong types and different-case keys carry nothing.
	v, present, err := headParseGapFill([]byte("openai_api_key: \"\"\nroot_dir: 42\nenable_ai_parsing: false\nHARDCOVER_API_TOKEN: x\ngoogle_books_api_key: [a]\n"))
	if err != nil || v != (headFileValues{}) || len(present) != 0 {
		t.Fatalf("v=%+v present=%v err=%v", v, present, err)
	}
	if present == nil {
		t.Fatal("a parsed file must return a non-nil present map")
	}
	// "yes" is a YAML 1.1 bool in some parsers but a string in yaml.v3: not true.
	v, _, _ = headParseGapFill([]byte("enable_ai_parsing: \"true\"\n"))
	if v.EnableAIParsing {
		t.Fatal("string \"true\" read as a bool")
	}
}

func TestHeadParseGapFillUnparsableFile(t *testing.T) {
	for _, in := range []string{"root_dir: [unclosed", "\t- not: valid: yaml: ::", "key: \"unterminated"} {
		v, present, err := headParseGapFill([]byte(in))
		if err != nil || v != (headFileValues{}) || present != nil {
			t.Fatalf("%q: v=%+v present=%v err=%v", in, v, present, err)
		}
	}
	// An empty file parses as nothing.
	if v, _, err := headParseGapFill(nil); err != nil || v != (headFileValues{}) {
		t.Fatalf("empty: %+v %v", v, err)
	}
}

func TestHeadDropManagedKeysDropsAllFifteenCaseInsensitively(t *testing.T) {
	m := map[string]any{}
	for i, k := range headManagedFileKeys {
		if i%2 == 0 {
			m[k] = "x"
		} else {
			m[strings.ToUpper(k)] = "x"
		}
	}
	m["Root_Dir"] = "mixed"
	m["ROOT_DIRS"] = "stays"
	m["my_own_key"] = "stays"
	m["log_levels"] = "stays"
	headDropManagedKeys(m)
	if len(m) != 3 || m["ROOT_DIRS"] != "stays" || m["my_own_key"] != "stays" || m["log_levels"] != "stays" {
		t.Fatalf("remaining = %v", m)
	}
}

func TestHeadDropManagedKeysRemovedKeysAndNested(t *testing.T) {
	var m map[string]any
	if err := yaml.Unmarshal([]byte(`
enable_sqlite: false
Auto_Fetch_Metadata: true
metadata_scoring:
  write_backup_before: true
  embedding_enabled: true
download_client:
  torrent:
    qbittorrent:
      password: robin-fixture
    deluge:
      host: 192.0.2.30
  usenet:
    api_key: nope-fixture
catalog:
  language: english
operator_note: keep me
`), &m); err != nil {
		t.Fatal(err)
	}
	headDropManagedKeys(m)
	if _, ok := m["enable_sqlite"]; ok {
		t.Error("enable_sqlite kept")
	}
	if _, ok := m["Auto_Fetch_Metadata"]; ok {
		t.Error("Auto_Fetch_Metadata kept")
	}
	ms := m["metadata_scoring"].(map[string]any)
	if _, ok := ms["write_backup_before"]; ok || ms["embedding_enabled"] != true {
		t.Errorf("metadata_scoring = %v", ms)
	}
	dc := m["download_client"].(map[string]any)
	if _, ok := dc["usenet"]; ok {
		t.Error("download_client.usenet kept")
	}
	torrent := dc["torrent"].(map[string]any)
	if _, ok := torrent["qbittorrent"]; ok {
		t.Error("qbittorrent block kept")
	}
	if torrent["deluge"].(map[string]any)["host"] != "192.0.2.30" {
		t.Errorf("deluge settings lost: %v", torrent)
	}
	if m["catalog"].(map[string]any)["language"] != "english" {
		t.Error("catalog.language (a live setting) was dropped")
	}
	if m["operator_note"] != "keep me" {
		t.Error("operator key dropped")
	}
	// A map left empty by a nested removal is removed too.
	n := map[string]any{"metadata_scoring": map[string]any{"write_backup_before": true}}
	headDropManagedKeys(n)
	if len(n) != 0 {
		t.Fatalf("empty parent kept: %v", n)
	}
}

func TestHeadRemovedKeys(t *testing.T) {
	if len(headRemovedKeys) != 23 {
		t.Fatalf("len = %d", len(headRemovedKeys))
	}
	if msg, ok := headRemovedKeyMessage("enable_sqlite"); !ok || !strings.Contains(msg, "PebbleDB is the only backend") {
		t.Fatalf("enable_sqlite: %q %v", msg, ok)
	}
	if _, ok := headRemovedKeyMessage("root_dir"); ok {
		t.Fatal("root_dir is not a removed key")
	}
	if msg, _ := headRemovedKeyMessage("auth_rate_limit_per_minute"); !strings.HasSuffix(msg, "not a setting") {
		t.Fatalf("composed message = %q", msg)
	}
	for _, rk := range headRemovedKeys {
		if rk.key == "" || rk.message == "" {
			t.Errorf("empty entry %+v", rk)
		}
	}
	m := map[string]any{"a": map[string]any{"b": 1}, "x.y": 2}
	if !headHasConfigKey(m, "a.b") || !headHasConfigKey(m, "x.y") || headHasConfigKey(m, "a.c") || headHasConfigKey(m, "a.b.c") || headHasConfigKey(m, "q") {
		t.Fatal("headHasConfigKey")
	}
}
