// file: internal/books/cutover/m6/whisper_local_test.go
// version: 0.1.0
// guid: 0b5e9c3a-6f12-4d7b-9a48-3c1e7d52f8a6
// last-edited: 2026-11-22

package m6_test

import (
	"context"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/books/adapters/whisper"
	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/integrationstest"
	"github.com/falkcorp/mediacore/storage"
)

// uvRunner is a Runner that decodes nothing: the registry only builds the
// instance and asks it for its health.
type uvRunner struct{}

func (uvRunner) Model() string                                      { return "base.en" }
func (uvRunner) Check(context.Context) error                        { return nil }
func (uvRunner) Transcribe(context.Context, string) (string, error) { return "", nil }

// loadWhisperRegistry is the two books whisper adapters registered in a real
// Registry over the store M6 wrote, and loaded.
func loadWhisperRegistry(ctx context.Context, t *testing.T, r *rig, db *storage.DB) *integrations.Registry {
	t.Helper()
	ks, ok := db.Keyspace(ownerInteg)
	if !ok {
		t.Fatal("no integrations keyspace")
	}
	reg, err := integrations.New(integrations.Options{
		Store: integrations.NativeInstanceStore(ks), NewBatch: ks.NewBatch,
		Secrets: liveSecrets{r}, Audit: quietAudit{},
	})
	if err != nil {
		t.Fatalf("registry: %v", err)
	}
	sd, err := whisper.ServerDescriptor()
	if err != nil {
		t.Fatal(err)
	}
	ld, err := whisper.LocalDescriptor(uvRunner{})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Register("books", sd, ld); err != nil {
		t.Fatalf("register: %v", err)
	}
	if err := reg.Load(ctx, nil); err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Cleanup(func() { _ = reg.Close(context.Background()) })
	return reg
}

// TestWhisperLocal_RecordPutsThroughARealRegistry: HEAD's local_process row
// carries the marker URL "local:uv-openai-whisper", which names no network
// address. books.whisper_local declares no url setting and core refuses a
// destination-named setting that is no URL with a host, so a record carrying it
// is refused at Put and serves as misconfigured. M6 writes no url for a local
// row, and the record it wrote is accepted by the Registry with the whisper
// adapters registered.
func TestWhisperLocal_RecordPutsThroughARealRegistry(t *testing.T) {
	withLive(t, nil, func(ctx context.Context, r *rig, db *storage.DB, s *snap) {
		reg := loadWhisperRegistry(ctx, t, r, db)
		ref := integrations.Ref{Kind: integrations.KindAI, Instance: "local-uv"}
		cfg, ok := reg.Config(ref)
		if !ok {
			t.Fatal("ai/local-uv is not served")
		}
		if _, has := cfg.Settings["url"]; has {
			t.Errorf("the local row carries a url setting: %v", cfg.Settings["url"])
		}
		h, err := reg.Health(ctx, ref)
		if err != nil {
			t.Fatal(err)
		}
		if h.Status == integrations.HealthMisconfigured {
			t.Errorf("ai/local-uv is misconfigured: %q", h.Message)
		}
		admin := integrationstest.As(ctx, auth.MethodSession, integrationstest.Manage, integrationstest.Admin)
		if _, err := reg.Put(admin, cfg, nil); err != nil {
			t.Errorf("the Registry refuses the record M6 wrote for the local row at Put: %v", err)
		}
	})
}
