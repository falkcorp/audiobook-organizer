// file: internal/books/config_residual_test.go
// version: 1.0.0
// guid: 3c8e1a57-9d02-4f64-a6b1-e5d70c2b9f48
// last-edited: 2026-11-19

package books

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/mediacore/module"
	"github.com/falkcorp/mediacore/secrets"
)

// residualBlobStore is the one row the residual codec touches.
type residualBlobStore struct {
	database.SettingsStore
	rows map[string]string
}

func (m *residualBlobStore) GetSetting(k string) (*database.Setting, error) {
	v, ok := m.rows[k]
	if !ok {
		return nil, errors.New("not found")
	}
	return &database.Setting{Key: k, Value: v}, nil
}

func (m *residualBlobStore) SetSetting(k, v, _ string, _ bool) error { m.rows[k] = v; return nil }

// residualSecretStore is the four secret methods of module.ConfigAccess over a map.
type residualSecretStore struct {
	module.ConfigAccess
	mu sync.Mutex
	m  map[secrets.Name]string
}

func (s *residualSecretStore) Secret(_ context.Context, n secrets.Name) (secrets.Value, secrets.Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.m[n]
	if !ok {
		return secrets.Value{}, secrets.Meta{Name: n}, secrets.ErrNotSet
	}
	return secrets.NewValue(v), secrets.Meta{Name: n, Set: true}, nil
}

func (s *residualSecretStore) PutSecret(_ context.Context, n secrets.Name, v secrets.Value, _ string) (secrets.Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[n] = v.Reveal()
	return secrets.Meta{Name: n, Set: true}, nil
}

func (s *residualSecretStore) DeleteSecret(_ context.Context, n secrets.Name, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.m, n)
	return nil
}

func (s *residualSecretStore) ListSecrets(_ context.Context, prefix string) ([]secrets.Meta, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []secrets.Meta
	for n := range s.m {
		if strings.HasPrefix(string(n), prefix) {
			out = append(out, secrets.Meta{Name: n, Set: true})
		}
	}
	slices.SortFunc(out, func(a, b secrets.Meta) int { return strings.Compare(string(a.Name), string(b.Name)) })
	return out, nil
}

func newResidual() (*configResidual, *residualBlobStore, *residualSecretStore) {
	st := &residualBlobStore{rows: map[string]string{}}
	sc := &residualSecretStore{m: map[secrets.Name]string{}}
	return &configResidual{store: st, acc: sc}, st, sc
}

func raw(s string) json.RawMessage { return json.RawMessage(s) }

// TestResidual_CredentialsNeverReachTheBlob: the metadata sources' credentials
// are stored as secrets, the blob row holds the list without them, Load puts
// them back, an empty string keeps the stored one, null deletes it, and a source
// that left the list takes its credentials with it.
func TestResidual_CredentialsNeverReachTheBlob(t *testing.T) {
	r, st, sc := newResidual()
	ctx := t.Context()
	list := `[{"id":"hardcover","name":"Hardcover","enabled":true,"credentials":{"api_token":"joker-token"}},
	  {"id":"goodreads","name":"G","enabled":false,"credentials":{"apiKey":"penguin-key"}}]`
	if err := r.Save(ctx, map[string]json.RawMessage{"metadata_sources": raw(list)}, nil); err != nil {
		t.Fatal(err)
	}
	if blob := st.rows[residualBlobKey]; strings.Contains(blob, "joker-token") || strings.Contains(blob, "penguin-key") || strings.Contains(blob, `"credentials"`) {
		t.Fatalf("a credential reached the settings blob: %s", blob)
	}
	if sc.m["integrations.metadata.hardcover.credentials.api_token"] != "joker-token" || sc.m["integrations.metadata.goodreads.credentials.api_key"] != "penguin-key" {
		t.Fatalf("secrets = %v", sc.m)
	}
	got, err := r.Load(ctx, []string{"metadata_sources"})
	if err != nil {
		t.Fatal(err)
	}
	var back []struct {
		ID          string            `json:"id"`
		Credentials map[string]string `json:"credentials"`
	}
	if err := json.Unmarshal(got["metadata_sources"], &back); err != nil {
		t.Fatal(err)
	}
	if len(back) != 2 || back[0].Credentials["api_token"] != "joker-token" || back[1].Credentials["apiKey"] != "penguin-key" {
		t.Fatalf("Load = %s", got["metadata_sources"])
	}
	// A client that read the redacted form sends blanks back: nothing changes.
	blank := `[{"id":"hardcover","name":"Hardcover","enabled":true,"credentials":{"api_token":""}},{"id":"goodreads","credentials":{"apiKey":null}}]`
	if err := r.Save(ctx, map[string]json.RawMessage{"metadata_sources": raw(blank)}, nil); err != nil {
		t.Fatal(err)
	}
	if sc.m["integrations.metadata.hardcover.credentials.api_token"] != "joker-token" {
		t.Fatal("a blank credential cleared the stored one")
	}
	if _, has := sc.m["integrations.metadata.goodreads.credentials.api_key"]; has {
		t.Fatal("a null credential did not delete the secret")
	}
	// hardcover alone: goodreads is gone from the list.
	if err := r.Save(ctx, map[string]json.RawMessage{"metadata_sources": raw(`[{"id":"hardcover","credentials":{}}]`)}, nil); err != nil {
		t.Fatal(err)
	}
	if len(sc.m) != 1 {
		t.Fatalf("a source that left the list kept secrets: %v", sc.m)
	}
	if err := r.Save(ctx, nil, []string{"metadata_sources"}); err != nil {
		t.Fatal(err)
	}
	if len(sc.m) != 0 {
		t.Fatalf("unsetting the list kept secrets: %v", sc.m)
	}
}

// TestResidual_RedactBlanksValuesKeepsKeys: a response carries which credentials
// and plugin settings are set, never what they are.
func TestResidual_RedactBlanksValuesKeepsKeys(t *testing.T) {
	r, _, _ := newResidual()
	got := r.Redact("metadata_sources", raw(`[{"id":"h","credentials":{"api_token":"joker-token","empty":""}}]`))
	if strings.Contains(string(got), "joker-token") || !strings.Contains(string(got), `"api_token":""`) {
		t.Fatalf("metadata_sources redact = %s", got)
	}
	got = r.Redact("plugins", raw(`{"webhook":{"enabled":true,"settings":{"secret":"riddler","urls":"https://a.example.test"}}}`))
	if strings.Contains(string(got), "riddler") || strings.Contains(string(got), "a.example.test") || !strings.Contains(string(got), `"secret":""`) {
		t.Fatalf("plugins redact = %s", got)
	}
	if got := r.Redact("openai_base_url", raw(`"https://llm.example.test"`)); string(got) != `"https://llm.example.test"` {
		t.Fatalf("an unrelated head was changed: %s", got)
	}
}

// TestResidual_PluginsSplit: the webhook secret becomes one record per URL (the
// same value), every other plugin is one settings record, none of it is in the
// blob, Load rebuilds the map, and shrinking the URL list drops the extra record.
func TestResidual_PluginsSplit(t *testing.T) {
	r, st, sc := newResidual()
	ctx := t.Context()
	plugins := `{"webhook":{"enabled":true,"settings":{"urls":"https://a.example.test, ,https://b.example.test","secret":"riddler"}},
	  "bat-signal":{"enabled":false,"settings":{"token":"oswald","mode":"night"}}}`
	if err := r.Save(ctx, map[string]json.RawMessage{"plugins": raw(plugins)}, nil); err != nil {
		t.Fatal(err)
	}
	if blob := st.rows[residualBlobKey]; strings.Contains(blob, "riddler") || strings.Contains(blob, "oswald") || strings.Contains(blob, "bat-signal") {
		t.Fatalf("plugin secrets reached the blob: %s", blob)
	}
	if sc.m["integrations.notify.webhook-1.secret"] != "riddler" || sc.m["integrations.notify.webhook-2.secret"] != "riddler" || len(sc.m) != 3 {
		t.Fatalf("secrets = %v", sc.m)
	}
	if !strings.Contains(sc.m["modules.books.plugins.bat-signal.settings"], "oswald") {
		t.Fatalf("plugin record = %v", sc.m)
	}
	got, err := r.Load(ctx, []string{"plugins"})
	if err != nil {
		t.Fatal(err)
	}
	var back map[string]struct {
		Enabled  bool              `json:"enabled"`
		Settings map[string]string `json:"settings"`
	}
	if err := json.Unmarshal(got["plugins"], &back); err != nil {
		t.Fatal(err)
	}
	if !back["webhook"].Enabled || back["webhook"].Settings["secret"] != "riddler" || back["webhook"].Settings["urls"] == "" ||
		back["bat-signal"].Settings["token"] != "oswald" {
		t.Fatalf("Load = %s", got["plugins"])
	}
	// One URL left, no secret sent: the stored secret stays for it, the second record goes.
	one := `{"webhook":{"enabled":true,"settings":{"urls":"https://a.example.test"}}}`
	if err := r.Save(ctx, map[string]json.RawMessage{"plugins": raw(one)}, nil); err != nil {
		t.Fatal(err)
	}
	if sc.m["integrations.notify.webhook-1.secret"] != "riddler" || len(sc.m) != 1 {
		t.Fatalf("after shrinking: %v", sc.m)
	}
	// An explicit null clears the shared secret.
	clear := `{"webhook":{"enabled":true,"settings":{"urls":"https://a.example.test","secret":null}}}`
	if err := r.Save(ctx, map[string]json.RawMessage{"plugins": raw(clear)}, nil); err != nil {
		t.Fatal(err)
	}
	if len(sc.m) != 0 {
		t.Fatalf("null did not clear the webhook secret: %v", sc.m)
	}
}

// TestResidual_RefusesWhatItCannotStore: M2 kept no enabled flag for a plugin but
// the webhook, so a write enabling another is refused; a credential that is not a
// string, and a list that is not one, are refused; nothing is written.
func TestResidual_RefusesWhatItCannotStore(t *testing.T) {
	for name, set := range map[string]map[string]string{
		"enabled other plugin": {"plugins": `{"bat-signal":{"enabled":true,"settings":{"a":"b"}}}`},
		"numeric credential":   {"metadata_sources": `[{"id":"h","credentials":{"k":5}}]`},
		"not a list":           {"metadata_sources": `{"id":"h"}`},
		"credential, no id":    {"metadata_sources": `[{"credentials":{"k":"v"}}]`},
		"valid then invalid":   {"metadata_sources": `[{"id":"a","credentials":{"k":"v"}},{"id":"b","credentials":{"k":5}}]`},
		"webhook secret int":   {"plugins": `{"webhook":{"settings":{"urls":"https://a.example.test","secret":5}}}`},
		"plugin then enabled":  {"plugins": `{"a":{"settings":{"x":"y"}},"b":{"enabled":true,"settings":{"x":"y"}}}`},
	} {
		t.Run(name, func(t *testing.T) {
			r, st, sc := newResidual()
			in := map[string]json.RawMessage{}
			for k, v := range set {
				in[k] = raw(v)
			}
			if err := r.Save(t.Context(), in, nil); err == nil {
				t.Fatal("Save accepted it")
			}
			if len(st.rows) != 0 || len(sc.m) != 0 {
				t.Fatalf("a refused Save wrote: rows=%v secrets=%v", st.rows, sc.m)
			}
		})
	}
}

// TestResidual_BlobPassesThroughOtherFields: fields the codec does not own, in
// the blob row, survive a Save of one head, and a dotted head is addressed in its
// nested object.
func TestResidual_BlobPassesThroughOtherFields(t *testing.T) {
	r, st, _ := newResidual()
	st.rows[residualBlobKey] = `{"keep_me":{"a":1},"download_client":{"type":"deluge","host":"192.0.2.9"}}`
	if err := r.Save(t.Context(), map[string]json.RawMessage{"download_client.host": raw(`"192.0.2.10"`), "openai_base_url": raw(`"https://llm.example.test"`)}, nil); err != nil {
		t.Fatal(err)
	}
	var blob map[string]any
	if err := json.Unmarshal([]byte(st.rows[residualBlobKey]), &blob); err != nil {
		t.Fatal(err)
	}
	dc := blob["download_client"].(map[string]any)
	if blob["keep_me"] == nil || dc["host"] != "192.0.2.10" || dc["type"] != "deluge" || blob["openai_base_url"] != "https://llm.example.test" {
		t.Fatalf("blob = %v", blob)
	}
	got, err := r.Load(t.Context(), []string{"download_client.host", "openai_base_url", "deluge_web_url"})
	if err != nil {
		t.Fatal(err)
	}
	if string(got["download_client.host"]) != `"192.0.2.10"` || got["deluge_web_url"] != nil {
		t.Fatalf("Load = %v", got)
	}
}
