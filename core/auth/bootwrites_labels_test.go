// file: core/auth/bootwrites_labels_test.go
// version: 0.1.0
// guid: 3a15ad79-196d-4bb1-8bd8-0e6588736f21
// last-edited: 2026-11-20

package auth_test

import (
	"context"
	"slices"
	"sync"
	"testing"

	"github.com/falkcorp/mediacore/auth"
	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/rbac"
)

// regionLog is a bootseam.Labeler that records the regions entered.
type regionLog struct {
	mu      sync.Mutex
	entered []string
}

func (l *regionLog) Enter(label string) func() {
	l.mu.Lock()
	l.entered = append(l.entered, label)
	l.mu.Unlock()
	return func() {}
}

// TestBootWrites_EveryCommitCarriesItsCrashMatrixLabel: each of BootWrites'
// store commits runs inside its bootseam region (F4-08 A5), so the crash matrix
// can kill and fail it by name once F4-08 wires P4d step 4. The mint path and the
// disabled path between them reach every label of BootWritesLabels.
func TestBootWrites_EveryCommitCarriesItsCrashMatrixLabel(t *testing.T) {
	reached := map[string]bool{}
	boot := func(t *testing.T, f *fx, files auth.StateFiles) []string {
		t.Helper()
		log := &regionLog{}
		ctx := bootseam.With(context.Background(), &bootseam.Seams{Labels: log})
		if err := auth.BootWrites(ctx, f.restoreDeps(files, nil)); err != nil {
			t.Fatalf("BootWrites: %v", err)
		}
		for _, l := range log.entered {
			reached[l] = true
		}
		return log.entered
	}

	f, files := bootFx(t)
	f.user("bruce-wayne", rbac.RoleAdmin)
	f.setSettings(auth.Settings{ReadOnlyKey: true})
	got := boot(t, f, files)
	for _, want := range []string{auth.LabelBootSystemUser, auth.LabelBootBootstrapMint, auth.LabelBootStartupKey} {
		if !slices.Contains(got, want) {
			t.Errorf("the minting boot entered %v, want %s", got, want)
		}
	}

	g, gfiles := bootFx(t)
	g.setSettings(auth.Settings{BootstrapEnabled: new(false)})
	if got := boot(t, g, gfiles); !slices.Contains(got, auth.LabelBootBootstrapDelete) {
		t.Errorf("the boot with bootstrap disabled entered %v, want %s", got, auth.LabelBootBootstrapDelete)
	}
	for _, l := range auth.BootWritesLabels() {
		if !reached[l] {
			t.Errorf("no boot entered %s", l)
		}
	}
}
