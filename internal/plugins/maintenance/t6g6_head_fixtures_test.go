// file: internal/plugins/maintenance/t6g6_head_fixtures_test.go
// version: 1.0.1
// guid: 5f0c9a3e-7d41-4b86-9e52-3a1c8d6f2b70
// last-edited: 2026-11-14

package maintenance

import (
	"context"
	"log/slog"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

// t6g6OrphanFixture gives a test store the retired-inclusive file list the Via
// scan reads, the way the shells hand it to the core.
type t6g6OrphanFixture struct{ orphanFileScanner }

func (s t6g6OrphanFixture) AllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	return database.AllBookFilesCoreIncludingRetired(s.orphanFileScanner)
}

// t6g6RecoverFixture is the same for the recover planner's store.
type t6g6RecoverFixture struct{ recoverStore }

func (s t6g6RecoverFixture) AllBookFilesCoreIncludingRetired() ([]database.BookFileCore, error) {
	return database.AllBookFilesCoreIncludingRetired(s.recoverStore)
}

// t6g6TestPlanRecover builds the env a HEAD-shaped run hands the recover core
// (head env, store for reads and writes, scan stand-down) and plans over it.
func t6g6TestPlanRecover(ctx context.Context, store recoverStore, scan ScanController, rootDir string, params recoverMissingParams, reporter sdk.Reporter) (recoverPlan, error) {
	env := recoverMissingFilesEnv{
		t6g6Env: t6g6HeadEnv(reporter, params.Apply),
		Store:   t6g6RecoverFixture{store},
		Writer:  store,
	}
	env.StandDown = t6g6HeadStandDown(scan, reporter)
	return t6g6PlanRecoverVia(ctx, env, rootDir, params)
}

// t6g6TestLogf discards the stale-hold log lines.
func t6g6TestLogf(slog.Level, string) {}
