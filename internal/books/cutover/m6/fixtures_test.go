// file: internal/books/cutover/m6/fixtures_test.go
// version: 1.0.1
// guid: c4d1e7a9-3b52-4f08-9e61-5a2c8d0b7f13
// last-edited: 2026-11-19

package m6_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/books/cutover/m6"
	"github.com/falkcorp/mediacore/secrets"
)

// Every fixture row is synthetic: Batman titles, storylines and people, the
// reserved 192.0.2.0/24 and .invalid hosts, generated secret values. No row is
// a real library row.

const (
	rootDir     = "/batcave/library"
	importOne   = "/batcave/import/gotham"
	importTwo   = "/batcave/library/incoming" // inside root_dir: a nested root
	importThree = "/batcave/import/arkham"
)

// secretValues are the values M2 left in the store, by name.
var secretValues = map[secrets.Name]string{
	"integrations.metadata.hardcover.api_token":           "fixture-hardcover-token-robin",
	"integrations.metadata.google-books.api_key":          "fixture-google-key-oracle",
	"integrations.ai.openai.api_key":                      "fixture-openai-key-nightwing",
	"integrations.download.deluge.password":               "fixture-deluge-password-oracle",
	"integrations.download.deluge.daemon_password":        "fixture-deluge-daemon-oracle",
	"integrations.notify.webhook-1.secret":                "fixture-webhook-hmac-batsignal",
	"integrations.notify.webhook-2.secret":                "fixture-webhook-hmac-batsignal",
	"integrations.metadata.openlibrary.credentials.token": "fixture-openlibrary-token-azrael",
}

// blobTree is the config JSON of the fixture: what M2 leaves in the blob for
// M6, one member per field of 05-config's rename table marked class i.
func blobTree() map[string]any {
	return map[string]any{
		"root_dir": rootDir,
		"path_aliases": []any{
			map[string]any{"root": rootDir, "windows": "W:", "unc": `\\gotham\books`, "smb_url": "smb://gotham/books"},
		},
		"metadata_sources": []any{
			map[string]any{"id": "hardcover", "name": "Hardcover", "enabled": true, "priority": 1, "base_url": "https://hardcover.invalid/graphql",
				"rate_limit": map[string]any{"tier": "high"}},
			map[string]any{"id": "google-books", "name": "Google Books", "enabled": true, "priority": 2, "base_url": "https://books.invalid"},
			map[string]any{"id": "openlibrary", "name": "Open Library", "enabled": false, "priority": 3,
				"rate_limit": map[string]any{"rps": 1.5, "burst": 4, "max_retries": 5, "timeout_seconds": 12}},
		},
		"openai_base_url": "https://llm.invalid/v1",
		"ai_endpoints": []any{
			map[string]any{"id": "local-llm", "label": "Wayne Tower LLM", "protocol": "openai_compat", "url": "http://192.0.2.10:8080/v1",
				"chat_model": "alfred-7b", "capability_models": map[string]any{"llm.dedup_review": "alfred-13b"},
				"priority": 10, "concurrency": 2, "enabled": true,
				"capabilities": []any{"llm.dedup_review", "embed.text", "mystery.cape"}, "features": []any{"json_schema"},
				"auth_ref": "openai_api_key", "host_roots": map[string]any{"batcave-lib": "/mnt/lib"}},
			map[string]any{"id": "local-uv", "label": "Whisper box", "protocol": "local_process", "url": "local:uv-openai-whisper",
				"enabled": true, "concurrency": 0, "capabilities": []any{"transcribe.batch"}},
		},
		"protected_paths": []any{"/batcave/seeding"},
		"download_client": map[string]any{"torrent": map[string]any{"type": "deluge", "deluge": map[string]any{"host": "192.0.2.20", "port": 58846, "username": "alfred"}}},
		"plugins": map[string]any{"webhook": map[string]any{"enabled": true, "settings": map[string]any{
			"urls": "http://192.0.2.30:9000/hook, ,https://signal.invalid/a", "events": "book.imported,scan.completed,bat.signal"}}},
		"deluge_web_url": "http://192.0.2.20:8112",
		"itunes": map[string]any{"path_mappings": []any{
			map[string]any{"from": "file://localhost/W:/itunes/Media", "to": rootDir + "/itunes"},
			map[string]any{"from": "X:/Elsewhere", "to": "/arkham/none"},
		}},
	}
}

// blobRow is the settings row HEAD stores the blob in.
func blobRow(t testing.TB, tree map[string]any) []byte {
	t.Helper()
	cfg, err := json.Marshal(tree)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(map[string]any{"key": "config_blob", "value": string(cfg), "type": "json", "is_secret": false})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func mustJSON(t testing.TB, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

var base = time.Date(2026, 11, 1, 3, 0, 0, 0, time.UTC)

// jobRow is HEAD's aijob row.
func jobRow(t testing.TB, id, typ, batch, status string, attempts int) []byte {
	t.Helper()
	row := map[string]any{"id": id, "type": typ, "batch_id": batch, "status": status, "apply_attempts": attempts,
		"created_at": base, "submitted_at": base.Add(time.Minute), "completed_at": base.Add(time.Hour), "last_apply_at": base.Add(2 * time.Hour),
		"item_count": 40, "success_count": 38, "error_count": 2, "custom_id_prefix": "nightwing-"}
	return mustJSON(t, row)
}

// headRows is the whole set of rows M6 drains, plus bystanders, as HEAD wrote
// them.
func headRows(t testing.TB) map[string][]byte {
	t.Helper()
	rows := map[string][]byte{
		m6.KeyBlob:                                   blobRow(t, blobTree()),
		m6.KeyImportCounter:                          []byte("3"),
		"import_path:1":                              mustJSON(t, map[string]any{"id": 1, "path": importOne, "name": "Gotham drop", "enabled": true, "created_at": base, "last_scan": base.Add(48 * time.Hour), "book_count": 17}),
		"import_path:2":                              mustJSON(t, map[string]any{"id": 2, "path": importTwo, "name": "Incoming", "enabled": true, "created_at": base.Add(time.Hour), "book_count": 0}),
		"import_path:3":                              mustJSON(t, map[string]any{"id": 3, "path": importThree, "name": "Arkham", "enabled": false, "created_at": base.Add(2 * time.Hour), "last_scan": base.Add(72 * time.Hour), "book_count": 5}),
		"import_path:path:" + importOne:              []byte("1"),
		"import_path:path:" + importTwo:              []byte("2"),
		"import_path:path:" + importThree:            []byte("3"),
		"provider_daily_budget:hardcover:2026-11-18": []byte("100"),
		"provider_throttle:audible":                  []byte("1789000000"),
		"idx:sidx:dirty:01JYEARONE":                  []byte("1"),
		"idx:sidx:dirty:01JHUSH":                     []byte("1"),
		// bystanders: M6 must leave these byte for byte.
		"counter:book":  []byte("7"),
		"setting:theme": []byte(`{"key":"theme","value":"dark","type":"string","is_secret":false}`),
	}
	jobs := []struct{ id, typ, batch, status string }{
		{"01JKNIGHTFALL", "dedup_review", "batch-knightfall", "completed"},
		{"01JNOMANSLAND", "author_dedup_full", "batch-nomansland", "submitted"},
		{"01JBATSIGNAL", "bat_signal", "batch-batsignal", "pending"},
		{"01JHUSH", "dedup_review", "batch-hush", "apply_failed"},
		{"01JLONGHALLOWEEN", "dedup_review", "", "failed"},
		{"01JYEARONE", "author_dedup_full", "batch-yearone", "expired"},
		{"01JKILLINGJOKE", "dedup_review", "batch-killingjoke", "completed_with_errors"},
	}
	for i, j := range jobs {
		rows["aijob:"+j.id] = jobRow(t, j.id, j.typ, j.batch, j.status, i%3)
		rows["aijob_payload:"+j.id] = []byte(`{"requests":["` + j.id + `"]}`)
		if j.batch != "" {
			rows["aijob_batch:"+j.batch] = []byte(j.id)
		}
	}
	// Seven jobs: 7 aijob + 7 payload + 6 batch rows.
	return rows
}
