// file: scripts/r2corpus/overlay/config.go
// version: 1.1.0
// guid: 9a3f1e6b-2d48-4c07-b5e1-6f8d0c3a7b29
// last-edited: 2026-11-10

//go:build r2corpus_overlay

package main

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// setBlobField rewrites one field of the stored settings blob through HEAD's
// own writer of the row (SetSetting("config_blob", ..., "json", false), as
// internal/config/persistence.go saves it). The value keeps its JSON type: a
// type-mismatched field makes R0 refuse to boot (persistence.go:1074), and R0
// must boot on the overlay to record its answers.
func (w *writer) setBlobField(field string, value any) {
	row, err := w.db.GetSetting("config_blob")
	must(err, "read config_blob")
	if row == nil {
		must(fmt.Errorf("stage 1 has no config_blob"), "read config_blob")
	}
	var blob map[string]json.RawMessage
	must(json.Unmarshal([]byte(row.Value), &blob), "decode config_blob")
	raw, err := json.Marshal(value)
	must(err, "encode blob field "+field)
	blob[field] = raw
	out, err := json.Marshal(blob)
	must(err, "encode config_blob")
	must(w.db.SetSetting("config_blob", string(out), "json", false), "write config_blob")
}

// ── M2: settings ───────────────────────────────────────────────────────────

// writeCorruptSettingsSecret replaces one of HEAD's four encrypted rows with a
// record whose ciphertext the key does not decrypt (a corrupt secret row; the
// record itself decodes, because an undecodable setting: row stops R0's boot,
// settings.go collectSettings). It is basic_auth_password, not one of the
// three API keys: stage 1's gap-fill config.yaml holds the OpenAI, Google
// Books and Hardcover keys, and R0's boot re-encrypts those rows from it, so
// R0's answer on the overlay would still show the secret set. The gap-fill
// file does not name basic_auth_password, so R0's answer and the manifest
// agree that the secret is unreadable.
func writeCorruptSettingsSecret(w *writer, label string) {
	w.setUndecryptable("basic_auth_password")
}

// setUndecryptable stores secret setting name as a well-formed record whose
// value is base64 of bytes that are not an AES-GCM ciphertext under the
// store's key (settings.go EncryptValue: base64(nonce || sealed)).
func (w *writer) setUndecryptable(name string) {
	key := "setting:" + name
	w.setJSON(key, database.Setting{
		Key: name, Value: base64.StdEncoding.EncodeToString([]byte("not-a-valid-ciphertext-for-arkham")),
		Type: "string", IsSecret: true,
	})
	w.corrupt = append(w.corrupt, key)
}

// writeBlobFieldUnmapped adds a field no rename-table row names (HEAD never
// had it); R0's decode drops it silently.
func writeBlobFieldUnmapped(w *writer, label string) {
	w.setBlobField("batcomputer_uplink_mode", "encrypted-relay")
}

// ── M5: system log ─────────────────────────────────────────────────────────

func writeCorruptSyslog(w *writer, label string) {
	at := time.Now().UTC().Format(time.RFC3339Nano)
	key := "syslog:" + at + ":r2corpus-overlay"
	w.setTorn(key, `{"id":0,"source":"r2corpus-overlay","level":"info","message":"Arkham Asylum breakout`)
}

// ── M6: integrations, import roots, AI jobs ────────────────────────────────

// writeIntegrationFieldInvalid adds an AI endpoint row (a native ai/<id>
// instance after M6, 05-metadata M1.10) whose URL does not parse. No
// integration field can be undecodable while R0 still boots: the blob is
// type-checked as a whole (persistence.go:1074), every plugin setting is a
// string, and R0's validator refuses a value it does not accept (an unknown
// backup_compression stops the boot), so M6's undecodable rows are the import
// root and the AI job below. HEAD validates ai_endpoints only on a PUT that
// carries them (ai_endpoints.go validateAIEndpoints), so the stored row boots;
// M6 keeps the instance and reports it misconfigured. The row is not the
// Deluge URL because DELUGE_WEB_URL is env-locked in the corpus' boot, which
// would mask the stored value in R0's answers.
func writeIntegrationFieldInvalid(w *writer, label string) {
	row, err := w.db.GetSetting("config_blob")
	must(err, "read config_blob")
	var blob map[string]json.RawMessage
	must(json.Unmarshal([]byte(row.Value), &blob), "decode config_blob")
	var rows []map[string]any
	if raw, ok := blob["ai_endpoints"]; ok && string(raw) != "null" {
		must(json.Unmarshal(raw, &rows), "decode ai_endpoints")
	}
	rows = append(rows, map[string]any{
		"id": "oracle-relay", "label": "Oracle relay (Clocktower)", "protocol": "openai_compat",
		"url": "http://oracle.invalid:not-a-port/v1", "chat_model": "clocktower-7b", "embed_model": "",
		"priority": 50, "concurrency": 1, "enabled": true, "capabilities": []string{"llm.filename_parse"},
		"require_gpu": false,
	})
	w.setBlobField("ai_endpoints", rows)
}

// writeCorruptImportRoot is an import root that does not decode. Its path is
// a directory of stage 1's import root holding a book (Hush), so books rows
// reference it by path prefix (HEAD's CountBooksByPathPrefix) as 05-storage's
// TestCutoverQuarantinedParentKeepsReferencingRows needs, while R0, which
// skips the undecodable row, still reaches the book through import_path:1.
func writeCorruptImportRoot(w *writer, label string) {
	key := "import_path:9"
	w.setTorn(key, `{"id":9,"path":"`+storeRoot+`/import/Selina Kyle","name":"Selina Kyle intake`)
}

func writeCorruptAIJob(w *writer, label string) {
	key := "aijob:" + newID()
	w.setTorn(key, `{"id":"`+key[len("aijob:"):]+`","type":"dedup_review","status":"submit`)
}

// aiJobStates are the AI jobs the overlay writes, one per status (HEAD's
// AIJob.Status vocabulary); R0's API can only make them through OpenAI.
var aiJobStates = []struct{ label, status string }{
	{"aijob-pending", "pending"},
	{"aijob-submitted", "submitted"},
	{"aijob-completed-with-errors", "completed_with_errors"},
	{"aijob-failed", "failed"},
}

func writeAIJob(w *writer, label string) {
	found := false
	for _, s := range aiJobStates {
		if s.label != label {
			continue
		}
		found = true
		id := newID()
		created := time.Now().UTC().Add(-2 * time.Hour)
		job := database.AIJob{
			ID: id, Type: "dedup_review", CustomIDPrefix: id, Status: "pending",
			ItemCount: 3, CreatedAt: created,
		}
		payload := []byte(`{"pairs":[["The Long Halloween","Dark Victory"],["Hush","Hush (Unabridged)"],["Knightfall","Knightquest"]]}`)
		must(w.db.CreateAIJob(job, payload), "create ai job")
		batch := "batch_r2corpus_" + lower(id)
		switch s.status {
		case "submitted":
			must(w.db.MarkAIJobSubmitted(id, batch), "submit ai job")
		case "completed_with_errors":
			must(w.db.MarkAIJobSubmitted(id, batch), "submit ai job")
			must(w.db.MarkAIJobCompleted(id, "completed_with_errors", 2, 1,
				[]database.AIJobRowError{{CustomID: id + "-2", Error: "the model answered with no verdict"}}), "complete ai job")
		case "failed":
			must(w.db.MarkAIJobFailed(id, "batch expired before it ran"), "fail ai job")
		}
		if s.status == "submitted" || s.status == "completed_with_errors" {
		}
	}
	if !found {
		must(fmt.Errorf("no table entry for case %s", label), "case table")
	}
}
