// file: scripts/r2corpus/overlay/config.go
// version: 1.0.0
// guid: 9a3f1e6b-2d48-4c07-b5e1-6f8d0c3a7b29
// last-edited: 2026-11-10

//go:build r2corpus_overlay

package main

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// blobFieldKey names one field of the settings blob in a case's key list:
// the row is setting:config_blob and the field is after the '#'.
func blobFieldKey(field string) string { return "setting:config_blob#" + field }

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

// blobHasField reports whether the stored blob already names field.
func (w *writer) blobHasField(field string) bool {
	row, err := w.db.GetSetting("config_blob")
	must(err, "read config_blob")
	var blob map[string]json.RawMessage
	must(json.Unmarshal([]byte(row.Value), &blob), "decode config_blob")
	_, ok := blob[field]
	return ok
}

// ── M2: settings ───────────────────────────────────────────────────────────

// writeCorruptSettingsSecret replaces one of HEAD's four encrypted rows with a
// record whose ciphertext the key does not decrypt (a corrupt secret row; the
// record itself decodes, because an undecodable setting: row stops R0's boot,
// settings.go collectSettings).
func writeCorruptSettingsSecret(w *writer, label string) {
	w.setJSON("setting:openai_api_key", database.Setting{
		Key: "openai_api_key", Value: "bm90LWEtdmFsaWQtY2lwaGVydGV4dC1mb3ItYXJraGFt", Type: "string", IsSecret: true,
	})
	w.record(label, "setting:openai_api_key")
}

// writeBlobFieldUnmapped adds a field no rename-table row names (HEAD never
// had it); R0's decode drops it silently.
func writeBlobFieldUnmapped(w *writer, label string) {
	w.setBlobField("batcomputer_uplink_mode", "encrypted-relay")
	w.record(label, blobFieldKey("batcomputer_uplink_mode"))
}

// ── M5: system log ─────────────────────────────────────────────────────────

func writeCorruptSyslog(w *writer, label string) {
	at := time.Now().UTC().Format(time.RFC3339Nano)
	key := "syslog:" + at + ":r2corpus-overlay"
	w.setTorn(key, `{"id":0,"source":"r2corpus-overlay","level":"info","message":"Arkham Asylum breakout`)
	w.record(label, key)
}

// ── M6: integrations, import roots, AI jobs ────────────────────────────────

// writeIntegrationFieldInvalid stores an integration instance field that is
// not a URL (the Deluge instance's web URL), keeping its JSON type. No
// integration field can be undecodable while R0 still boots: the blob is
// type-checked as a whole (persistence.go:1074) and R0's validator refuses a
// value it does not accept (an unknown backup_compression stops the boot), so
// M6's undecodable rows are the import root and the AI job below. M6 keeps an
// instance with invalid settings and reports it (05-metadata M1.10).
func writeIntegrationFieldInvalid(w *writer, label string) {
	if !w.blobHasField("deluge_web_url") {
		must(fmt.Errorf("stage 1's blob has no deluge_web_url"), label)
	}
	w.setBlobField("deluge_web_url", "deluge.invalid:not-a-port/::")
	w.record(label, blobFieldKey("deluge_web_url"))
}

func writeCorruptImportRoot(w *writer, label string) {
	key := "import_path:9"
	w.setTorn(key, `{"id":9,"path":"`+storeRoot+`/arkham","name":"Arkham Intake`)
	w.record(label, key)
}

func writeCorruptAIJob(w *writer, label string) {
	key := "aijob:" + newID()
	w.setTorn(key, `{"id":"`+key[len("aijob:"):]+`","type":"dedup_review","status":"submit`)
	w.record(label, key)
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
	for _, s := range aiJobStates {
		if s.label != label {
			continue
		}
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
		w.record(label, "aijob:"+id, "aijob_payload:"+id)
		if s.status == "submitted" || s.status == "completed_with_errors" {
			w.record(label, "aijob_batch:"+batch)
		}
	}
}
