// file: internal/plugins/maintenance/decode_guard_test.go
// version: 1.0.0
// guid: 4d7a2e91-b3f0-4c58-8e16-a9c5d02f7b34
// last-edited: 2026-10-09

package maintenance

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/serverdecode"
)

// storeSpyDeps counts OpsStore calls so a guard test can prove the operation
// refused before it touched the store.
type storeSpyDeps struct {
	fakeDeps
	opsStoreCalls int
}

func (d *storeSpyDeps) OpsStore() OpsStore {
	d.opsStoreCalls++
	return d.fakeDeps.OpsStore()
}

// Each maintenance operation that decodes audio in-process must refuse, before
// any store read, unless the host opted in.
func TestDecodeOps_RefuseWithoutServerDecode(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")

	cases := []struct {
		name string
		run  func(p *Plugin) error
	}{
		{"extract-wav-clips", func(p *Plugin) error {
			return p.runExtractWAVClips(context.Background(), nil, &fakeReporter{})
		}},
		{"transcribe-book-intros", func(p *Plugin) error {
			return p.runIntroTranscribe(context.Background(), nil, &fakeReporter{})
		}},
		{"transcribe-book-intros extract_only", func(p *Plugin) error {
			return p.runIntroTranscribe(context.Background(), json.RawMessage(`{"extract_only":true}`), &fakeReporter{})
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s, err := database.NewPebbleStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = s.Close() })
			deps := &storeSpyDeps{fakeDeps: fakeDeps{store: s}}
			err = tc.run(New(deps))
			if !errors.Is(err, serverdecode.ErrRefused) {
				t.Fatalf("err = %v, want ErrRefused", err)
			}
			if tc.name == "extract-wav-clips" && deps.opsStoreCalls != 0 {
				t.Fatalf("extract-wav-clips touched the store %d times before refusing", deps.opsStoreCalls)
			}
		})
	}
}

// reparse_only never decodes audio, so it must keep working with the switch off.
func TestIntroTranscribe_ReparseOnlyStillRunsWithoutServerDecode(t *testing.T) {
	t.Setenv(serverdecode.EnvVar, "")
	s, err := database.NewPebbleStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	p := New(fakeDeps{store: s})
	err = p.runIntroTranscribe(context.Background(), json.RawMessage(`{"reparse_only":true}`), &fakeReporter{})
	if errors.Is(err, serverdecode.ErrRefused) {
		t.Fatalf("reparse_only was refused: %v", err)
	}
	if err != nil {
		t.Fatalf("reparse_only on an empty library: %v", err)
	}
}
