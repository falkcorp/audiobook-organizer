// file: internal/books/cutover/head_blob_test.go
// version: 1.0.0
// guid: 35db8787-c9e5-4623-ab45-c5df7dab19d0
// last-edited: 2026-11-11

package cutover

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func blobMap(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("unmarshal %q: %v", s, err)
	}
	return m
}

func sub(t *testing.T, m map[string]any, key string) map[string]any {
	t.Helper()
	v, ok := m[key].(map[string]any)
	if !ok {
		t.Fatalf("%q is not an object in %v", key, m)
	}
	return v
}

// runTransform checks the shared contract of a one-argument transform: a flat
// input changes, and the output (already nested) is then left unchanged.
func runTransform(t *testing.T, fn func(string) (string, bool), flat string) map[string]any {
	t.Helper()
	out, changed := fn(flat)
	if !changed {
		t.Fatalf("flat input was not migrated: %s", flat)
	}
	again, changed2 := fn(out)
	if changed2 || again != out {
		t.Fatalf("second run changed the blob (changed=%v)", changed2)
	}
	return blobMap(t, out)
}

func TestHeadMigrateEmbeddingBlob(t *testing.T) {
	m := runTransform(t, headMigrateEmbeddingBlob, `{"embedding_enabled":true,"embedding_model":"batcave-embed","embedding_dimensions":768,"embedding_base_url":"http://192.0.2.10:11434","vector_index_backend":"gotham-hnsw","root_dir":"/gotham"}`)
	e := sub(t, m, "embedding")
	if e["enabled"] != true || e["model"] != "batcave-embed" || e["dimensions"] != float64(768) || e["base_url"] != "http://192.0.2.10:11434" || e["vector_backend"] != "gotham-hnsw" {
		t.Fatalf("embedding = %v", e)
	}
	for _, k := range []string{"embedding_enabled", "embedding_model", "embedding_dimensions", "embedding_base_url", "vector_index_backend"} {
		if _, ok := m[k]; ok {
			t.Fatalf("flat key %q survived", k)
		}
	}
	if m["root_dir"] != "/gotham" {
		t.Fatalf("unrelated key lost: %v", m)
	}
	nested := `{"embedding":{"enabled":true,"model":"batcave-embed"}}`
	if out, changed := headMigrateEmbeddingBlob(nested); changed || out != nested {
		t.Fatalf("nested input changed: %s", out)
	}
}

func TestHeadMigrateDedupBlob(t *testing.T) {
	m := runTransform(t, headMigrateDedupBlob, `{"dedup_book_high_threshold":0.9,"dedup_auto_merge_enabled":true,"dedup_review_model":"alfred-review","dedup":{"author_low_threshold":0.4}}`)
	d := sub(t, m, "dedup")
	if d["book_high_threshold"] != 0.9 || d["auto_merge_enabled"] != true || d["review_model"] != "alfred-review" {
		t.Fatalf("dedup = %v", d)
	}
	if d["author_low_threshold"] != 0.4 {
		t.Fatalf("existing nested member lost: %v", d)
	}
	if _, ok := m["dedup_book_high_threshold"]; ok {
		t.Fatalf("flat key survived")
	}
	nested := `{"dedup":{"book_high_threshold":0.9}}`
	if out, changed := headMigrateDedupBlob(nested); changed || out != nested {
		t.Fatalf("nested input changed: %s", out)
	}
}

func TestHeadMigrateMetadataScoringBlob(t *testing.T) {
	m := runTransform(t, headMigrateMetadataScoringBlob, `{"metadata_embedding_scoring_enabled":true,"metadata_embedding_min_score":0.5,"metadata_embedding_best_match_min":0.8,"metadata_llm_scoring_enabled":true,"metadata_llm_rerank_epsilon":0.05,"metadata_llm_rerank_top_k":3,"write_backup_before_tag_write":true}`)
	s := sub(t, m, "metadata_scoring")
	if s["embedding_enabled"] != true || s["embedding_min_score"] != 0.5 || s["embedding_best_match"] != 0.8 || s["llm_enabled"] != true || s["llm_rerank_epsilon"] != 0.05 || s["llm_rerank_top_k"] != float64(3) {
		t.Fatalf("metadata_scoring = %v", s)
	}
	if _, ok := m["write_backup_before_tag_write"]; ok {
		t.Fatalf("removed key write_backup_before_tag_write survived")
	}
	if out, changed := headMigrateMetadataScoringBlob(`{"metadata_scoring":{}}`); changed {
		t.Fatalf("nested input changed: %s", out)
	}
}

func TestHeadMigrateAIBackendBlob(t *testing.T) {
	local := runTransform(t, headMigrateAIBackendBlob, `{"openai_api_key":"","enable_ai_parsing":true,"embedding":{"enabled":true,"base_url":"http://192.0.2.10:11434","model":"alfred-embed"}}`)
	b := sub(t, local, "ai_backend")
	if b["embedding_mode"] != "local" || b["llm_mode"] != "disabled" || b["local_base_url"] != "http://192.0.2.10:11434" || b["local_embedding_model"] != "alfred-embed" {
		t.Fatalf("ai_backend = %v", b)
	}
	cloud := runTransform(t, headMigrateAIBackendBlob, `{"openai_api_key":"not-a-real-key","enable_ai_parsing":true}`)
	b = sub(t, cloud, "ai_backend")
	if b["embedding_mode"] != "openai" || b["llm_mode"] != "openai" {
		t.Fatalf("ai_backend = %v", b)
	}
	off := runTransform(t, headMigrateAIBackendBlob, `{"openai_api_key":"not-a-real-key","embedding":{"enabled":false}}`)
	b = sub(t, off, "ai_backend")
	if b["embedding_mode"] != "disabled" || b["llm_mode"] != "disabled" {
		t.Fatalf("ai_backend = %v", b)
	}
	// No legacy signal at all: untouched. Already present: untouched.
	for _, in := range []string{`{"root_dir":"/gotham"}`, `{"ai_backend":{"llm_mode":"local"},"openai_api_key":"x"}`} {
		if out, changed := headMigrateAIBackendBlob(in); changed || out != in {
			t.Fatalf("unexpected change of %s -> %s", in, out)
		}
	}
}

func rowIDs(t *testing.T, m map[string]any) []string {
	t.Helper()
	rows, ok := m["ai_endpoints"].([]any)
	if !ok {
		t.Fatalf("ai_endpoints = %v", m["ai_endpoints"])
	}
	var ids []string
	for _, r := range rows {
		ids = append(ids, r.(map[string]any)["id"].(string))
	}
	return ids
}

func TestHeadMigrateAIEndpointsBlob(t *testing.T) {
	blob := `{"ai_backend":{"embedding_mode":"local","llm_mode":"local","local_base_url":"http://192.0.2.10:11434","local_embedding_model":"alfred-embed","local_llm_model":"alfred-chat"},"whisper_endpoints":[{"url":"http://192.0.2.20:8765","label":"Batwing","priority":1,"concurrency":2,"capabilities":["local"]},{"url":"http://192.0.2.21:8765","label":"Batmobile","priority":2,"concurrency":0,"capabilities":[]}]}`
	out, changed := headMigrateAIEndpointsBlob(blob, headAIEndpointsSeed{HasOpenAIKey: true})
	if !changed {
		t.Fatal("blob without ai_endpoints was not migrated")
	}
	m := blobMap(t, out)
	if got, want := rowIDs(t, m), []string{"local-llm", "openai", "whisper-1", "whisper-2", "local-uv"}; !slices.Equal(got, want) {
		t.Fatalf("ids = %v, want %v (same port twice falls back to position)", got, want)
	}
	rows := m["ai_endpoints"].([]any)
	local := rows[0].(map[string]any)
	caps := local["capabilities"].([]any)
	has := func(c string) bool { return slices.Contains(caps, any(c)) }
	if !has("llm.filename_parse") || !has("embed.text") || has("llm.dedup_review") {
		t.Fatalf("local row capabilities = %v", caps)
	}
	openai := rows[1].(map[string]any)
	if openai["auth_ref"] != "openai_api_key" || openai["url"] != "https://api.openai.com/v1" || openai["chat_model"] != "gpt-5-mini" {
		t.Fatalf("openai row = %v", openai)
	}
	if rows[2].(map[string]any)["concurrency"] != float64(2) || rows[3].(map[string]any)["concurrency"] != float64(1) {
		t.Fatalf("whisper concurrency = %v / %v", rows[2], rows[3])
	}
	uv := rows[4].(map[string]any)
	if slices.Contains(uv["capabilities"].([]any), any("transcribe.batch")) {
		t.Fatalf("uv row ticked transcribe.batch although whisper servers exist: %v", uv)
	}

	// Without a key there is no openai row; without whisper servers uv takes batch.
	out2, changed := headMigrateAIEndpointsBlob(`{"root_dir":"/gotham"}`, headAIEndpointsSeed{})
	if !changed {
		t.Fatal("not migrated")
	}
	m2 := blobMap(t, out2)
	if got := rowIDs(t, m2); !slices.Equal(got, []string{"local-uv"}) {
		t.Fatalf("ids = %v", got)
	}
	uv2 := m2["ai_endpoints"].([]any)[0].(map[string]any)
	if !slices.Contains(uv2["capabilities"].([]any), any("transcribe.batch")) {
		t.Fatalf("uv row = %v", uv2)
	}

	// Idempotent: present (even empty) is kept; null counts as absent.
	for _, in := range []string{`{"ai_endpoints":[]}`, out} {
		if o, ch := headMigrateAIEndpointsBlob(in, headAIEndpointsSeed{HasOpenAIKey: true}); ch || o != in {
			t.Fatalf("present ai_endpoints changed: %s", o)
		}
	}
	if _, ch := headMigrateAIEndpointsBlob(`{"ai_endpoints":null}`, headAIEndpointsSeed{}); !ch {
		t.Fatal("null ai_endpoints should be treated as absent")
	}
	if _, ch := headMigrateAIEndpointsBlob(`not json`, headAIEndpointsSeed{}); ch {
		t.Fatal("non-JSON blob must be left alone")
	}
}

func TestHeadMigrateITunesBlob(t *testing.T) {
	m := runTransform(t, headMigrateITunesBlob, `{"itunes_sync_enabled":true,"itunes_sync_interval":60,"itunes_library_read_path":"/gotham/lib.xml","itunes_library_write_path":"/gotham/out.xml","itunes_path_trim_enabled":true,"itunes_windows_root_path":"W:/batcave","itunes_media_root":"/gotham/media","itl_write_back_enabled":true,"itunes_auto_write_back":true}`)
	i := sub(t, m, "itunes")
	if i["library_read_path"] != "/gotham/lib.xml" || i["library_write_path"] != "/gotham/out.xml" || i["path_trim_enabled"] != true || i["windows_root_path"] != "W:/batcave" || i["media_root"] != "/gotham/media" {
		t.Fatalf("itunes = %v", i)
	}
	for _, k := range []string{"itunes_sync_enabled", "itunes_sync_interval", "itl_write_back_enabled", "itunes_auto_write_back", "itunes_media_root"} {
		if _, ok := m[k]; ok {
			t.Fatalf("flat key %q survived", k)
		}
	}
}

func TestHeadMigrateMaintenanceBlob(t *testing.T) {
	m := runTransform(t, headMigrateMaintenanceBlob, `{"maintenance_window_enabled":true,"maintenance_window_start":1,"maintenance_window_end":4,"maintenance_window_purge_old_logs":true,"maintenance_window_acoustid_online_lookup":true,"acoustid_online_lookup_nightly_limit":50}`)
	mt := sub(t, m, "maintenance")
	if mt["enabled"] != true || mt["window_start"] != float64(1) || mt["window_end"] != float64(4) || mt["purge_old_logs"] != true || mt["acoustid_online_lookup"] != true || mt["acoustid_nightly_limit"] != float64(50) {
		t.Fatalf("maintenance = %v", mt)
	}
	if _, ok := m["acoustid_online_lookup_nightly_limit"]; ok {
		t.Fatal("flat nightly limit survived")
	}
}

func TestHeadMigrateScheduledBlob(t *testing.T) {
	m := runTransform(t, headMigrateScheduledBlob, `{"scheduled_dedup_refresh_enabled":true,"scheduled_dedup_refresh_interval":30,"scheduled_reconcile_on_startup":true,"scheduled":{"library_scan":{"enabled":true,"interval":360},"dedup_refresh":{"on_startup":true}}}`)
	s := sub(t, m, "scheduled")
	d := sub(t, s, "dedup_refresh")
	if d["enabled"] != true || d["interval"] != float64(30) || d["on_startup"] != true {
		t.Fatalf("dedup_refresh = %v (existing sibling must merge, not be zeroed)", d)
	}
	if sub(t, s, "library_scan")["interval"] != float64(360) {
		t.Fatalf("library_scan lost: %v", s)
	}
	if sub(t, s, "reconcile")["on_startup"] != true {
		t.Fatalf("reconcile = %v", s["reconcile"])
	}
	if _, ok := m["scheduled_dedup_refresh_enabled"]; ok {
		t.Fatal("flat key survived")
	}
	// The sentinel is scheduled_dedup_refresh_enabled: another flat key alone does nothing.
	in := `{"scheduled_reconcile_enabled":true}`
	if out, changed := headMigrateScheduledBlob(in); changed || out != in {
		t.Fatalf("blob without the sentinel changed: %s", out)
	}
}

func TestHeadMigrateAutoUpdateBlob(t *testing.T) {
	m := runTransform(t, headMigrateAutoUpdateBlob, `{"auto_update_enabled":true,"auto_update_channel":"stable","auto_update_check_minutes":15,"auto_update_window_start":2,"auto_update_window_end":5}`)
	a := sub(t, m, "auto_update")
	if a["enabled"] != true || a["channel"] != "stable" || a["check_minutes"] != float64(15) || a["window_start"] != float64(2) || a["window_end"] != float64(5) {
		t.Fatalf("auto_update = %v", a)
	}
}

func TestHeadMigrateLogRetentionBlob(t *testing.T) {
	out, changed := headMigrateLogRetentionBlob(`{"log_retention_days":0,"root_dir":"/gotham"}`)
	if !changed {
		t.Fatal("stored 0 was not dropped")
	}
	m := blobMap(t, out)
	if _, ok := m["log_retention_days"]; ok || m["root_dir"] != "/gotham" {
		t.Fatalf("blob = %v", m)
	}
	for _, in := range []string{`{"log_retention_days":30}`, `{"log_retention_days":-1}`, `{"root_dir":"/gotham"}`} {
		if o, ch := headMigrateLogRetentionBlob(in); ch || o != in {
			t.Fatalf("%s changed to %s", in, o)
		}
	}
}

func TestHeadMigrateEmbedCoverArtBlob(t *testing.T) {
	out, changed := headMigrateEmbedCoverArtBlob(`{"embed_cover_art":false,"root_dir":"/gotham"}`)
	if !changed {
		t.Fatal("stored false was not dropped")
	}
	if m := blobMap(t, out); m["embed_cover_art"] != nil || m["root_dir"] != "/gotham" {
		t.Fatalf("blob = %v", m)
	}
	for _, in := range []string{`{"embed_cover_art":true}`, `{"root_dir":"/gotham"}`} {
		if o, ch := headMigrateEmbedCoverArtBlob(in); ch || o != in {
			t.Fatalf("%s changed to %s", in, o)
		}
	}
}

func TestHeadCheckConfigBlob(t *testing.T) {
	if err := headCheckConfigBlob(`{}`); err != nil {
		t.Fatalf("empty object refused: %v", err)
	}
	for _, in := range []string{"", "null", "[]", `"batman"`, "42", "{bad"} {
		if err := headCheckConfigBlob(in); err == nil {
			t.Fatalf("%q accepted", in)
		}
	}
}

func TestHeadDecodeBlobRefusesNonObjects(t *testing.T) {
	for _, in := range []string{"", "null", "[]", `"batman"`, "{bad"} {
		_, err := headDecodeBlob([]byte(in), headSeed{}, headMarkers{})
		if err == nil || !strings.Contains(err.Error(), "unreadable") {
			t.Fatalf("%q: err = %v", in, err)
		}
	}
}

func TestHeadDecodeBlobTypeMismatchLeaksNoContent(t *testing.T) {
	_, err := headDecodeBlob([]byte(`{"root_dir":"/batcave/sentinel-path","scan_on_startup":"sentinel-joker"}`), headSeed{}, headMarkers{})
	if err == nil {
		t.Fatal("type mismatch accepted")
	}
	msg := err.Error()
	if !strings.Contains(msg, "does not match the config schema") {
		t.Fatalf("err = %v", err)
	}
	if strings.Contains(msg, "sentinel-joker") || strings.Contains(msg, "sentinel-path") {
		t.Fatalf("error repeats blob content: %v", err)
	}
}

func TestHeadDecodeBlobAbsentFieldsStayZero(t *testing.T) {
	b, err := headDecodeBlob([]byte(`{"root_dir":"/gotham/library"}`), headSeed{}, headMarkers{})
	if err != nil {
		t.Fatal(err)
	}
	if b.Config.RootDir != "/gotham/library" {
		t.Fatalf("RootDir = %q", b.Config.RootDir)
	}
	if b.Config.SetupComplete || b.Config.ScanOnStartup || b.Config.EmbedCoverArt || b.Config.LogRetentionDays != 0 || b.Config.Embedding.Enabled {
		t.Fatalf("absent fields are not zero: %+v", b.Config.Embedding)
	}
	if _, ok := b.Fields["root_dir"]; !ok {
		t.Fatal("Fields lacks root_dir")
	}
	if _, ok := b.Fields["setup_complete"]; ok {
		t.Fatal("Fields invented setup_complete")
	}
	// The ai_endpoints transform always adds the key to a blob that has none.
	if len(b.Config.AIEndpoints) != 1 || b.Config.AIEndpoints[0].ID != "local-uv" {
		t.Fatalf("AIEndpoints = %+v", b.Config.AIEndpoints)
	}
	// HasOpenAIKey flows into the transform.
	b, err = headDecodeBlob([]byte(`{}`), headSeed{HasOpenAIKey: true}, headMarkers{})
	if err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, e := range b.Config.AIEndpoints {
		ids = append(ids, e.ID)
	}
	if !slices.Equal(ids, []string{"openai", "local-uv"}) {
		t.Fatalf("ids = %v", ids)
	}
}

func TestHeadDecodeBlobChainsFlatBlob(t *testing.T) {
	flat := `{"embedding_enabled":true,"embedding_model":"batcave-embed","embedding_base_url":"http://192.0.2.10:11434","dedup_book_high_threshold":0.9,"auto_update_enabled":true,"auto_update_channel":"stable","maintenance_window_enabled":true,"maintenance_window_start":1,"maintenance_window_end":4,"scheduled_dedup_refresh_enabled":true,"itunes_sync_enabled":true,"itunes_media_root":"/gotham/media"}`
	b, err := headDecodeBlob([]byte(flat), headSeed{}, headMarkers{})
	if err != nil {
		t.Fatal(err)
	}
	c := b.Config
	if !c.Embedding.Enabled || c.Embedding.Model != "batcave-embed" || c.Dedup.BookHighThreshold != 0.9 || !c.AutoUpdate.Enabled || c.AutoUpdate.Channel != "stable" || !c.Maintenance.Enabled || c.Maintenance.WindowEnd != 4 || !c.Scheduled.DedupRefresh.Enabled || c.ITunes.MediaRoot != "/gotham/media" {
		t.Fatalf("flat blob did not decode into the nested config: %+v", c)
	}
	// ai_backend is derived from the (migrated) legacy fields, before ai_endpoints.
	if c.AIBackend.EmbeddingMode != "local" || c.AIBackend.LocalBaseURL != "http://192.0.2.10:11434" {
		t.Fatalf("AIBackend = %+v", c.AIBackend)
	}
	if len(c.AIEndpoints) == 0 || c.AIEndpoints[0].ID != "local-llm" {
		t.Fatalf("AIEndpoints = %+v", c.AIEndpoints)
	}
}

func TestHeadDecodeBlobMarkers(t *testing.T) {
	in := []byte(`{"log_retention_days":0,"embed_cover_art":false}`)

	b, err := headDecodeBlob(in, headSeed{}, headMarkers{})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := b.Fields["log_retention_days"]; ok {
		t.Fatal("log_retention_days=0 kept although the marker is absent")
	}
	if _, ok := b.Fields["embed_cover_art"]; ok {
		t.Fatal("embed_cover_art=false kept although the marker is absent")
	}

	b, err = headDecodeBlob(in, headSeed{}, headMarkers{LogRetentionZero: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(b.Fields["log_retention_days"]) != "0" {
		t.Fatal("log_retention_days=0 dropped although its marker row exists")
	}
	if _, ok := b.Fields["embed_cover_art"]; ok {
		t.Fatal("embed_cover_art one-shot skipped by the wrong marker")
	}

	b, err = headDecodeBlob(in, headSeed{}, headMarkers{EmbedCoverArt: true})
	if err != nil {
		t.Fatal(err)
	}
	if string(b.Fields["embed_cover_art"]) != "false" {
		t.Fatal("embed_cover_art=false dropped although its marker row exists")
	}
	if _, ok := b.Fields["log_retention_days"]; ok {
		t.Fatal("log_retention one-shot skipped by the wrong marker")
	}

	// Non-zero retention and embed_cover_art=true are operator choices in both states.
	b, err = headDecodeBlob([]byte(`{"log_retention_days":30,"embed_cover_art":true}`), headSeed{}, headMarkers{})
	if err != nil {
		t.Fatal(err)
	}
	if b.Config.LogRetentionDays != 30 || !b.Config.EmbedCoverArt {
		t.Fatalf("operator values lost: %+v", b.Config.LogRetentionDays)
	}
}

func TestHeadDecodeBlobTypeProbeRunsBeforeOneShots(t *testing.T) {
	// A blob whose types do not match is refused even though a one-shot
	// would have rewritten part of it.
	_, err := headDecodeBlob([]byte(`{"embed_cover_art":false,"scan_on_startup":"sentinel"}`), headSeed{}, headMarkers{})
	if err == nil || !strings.Contains(err.Error(), "does not match the config schema") {
		t.Fatalf("err = %v", err)
	}
}

func TestHeadMarkerRowNames(t *testing.T) {
	// The settings rows whose presence means a one-shot already ran; the
	// reader of the settings store builds headMarkers from them.
	if headLogRetentionZeroMigratedKey != "log_retention_zero_migrated" || headEmbedCoverArtMigratedKey != "embed_cover_art_default_on_migrated" {
		t.Fatal("marker row names changed")
	}
}
