// file: core/rbac/seeds_labels_test.go
// version: 0.1.0
// guid: 17873c1b-d901-4619-a2c4-7205bc52e11d
// last-edited: 2026-11-20

package rbac_test

import (
	"context"
	"slices"
	"testing"

	"github.com/falkcorp/mediacore/internal/bootseam"
	"github.com/falkcorp/mediacore/rbac"
)

// seedRegions records the regions entered.
type seedRegions struct{ entered []string }

func (l *seedRegions) Enter(label string) func() {
	l.entered = append(l.entered, label)
	return func() {}
}

// TestCommitSeeds_RunsInItsCrashMatrixRegion: CommitSeeds' batch runs inside
// the bootseam region LabelCommitSeeds (F4-08 A5), so the crash matrix can kill
// and fail it by name once F4-08 wires P7.
func TestCommitSeeds_RunsInItsCrashMatrixRegion(t *testing.T) {
	f := boot(t, newEnv(t), setup{modules: []module{gotham()}})
	log := &seedRegions{}
	ctx := bootseam.With(context.Background(), &bootseam.Seams{Labels: log})
	if err := f.svc.CommitSeeds(ctx); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(log.entered, []string{rbac.LabelCommitSeeds}) {
		t.Errorf("CommitSeeds entered %v, want [%s]", log.entered, rbac.LabelCommitSeeds)
	}
}
