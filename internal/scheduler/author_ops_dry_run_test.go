// file: internal/scheduler/author_ops_dry_run_test.go
// version: 1.0.0
// guid: c85db25f-dd3e-4ee4-9c2a-560665e7027c
// last-edited: 2026-09-25

package scheduler

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/cache"
	dbmocks "github.com/falkcorp/audiobook-organizer/internal/database/mocks"
	opsregistry "github.com/falkcorp/audiobook-organizer/internal/operations/registry"
)

// scheduler.author-split-scan and scheduler.resolve-production-authors
// ignored their params and wrote on every run until 2026-09-25. The scheduled
// trigger sends schedulerExtraOpParams{}; that must now be a dry run.

func runSchedulerAuthorOp(t *testing.T, store ExtraOpsStore, deps ExtraOpsDeps, opID string, params any) (*splitRelinkReporter, error) {
	t.Helper()
	m := dbmocks.NewMockStore(t)
	m.EXPECT().UpsertOpDefinitionV2(mock.Anything).Return(nil).Maybe()
	reg := opsregistry.New(m, slog.New(slog.DiscardHandler), 1, nil)
	r := NewExtraOpsRegistrar(store, deps)
	require.NoError(t, r.RegisterAuthorSplitScanOp(reg))
	require.NoError(t, r.RegisterResolveProductionAuthorsOp(reg))
	def, ok := reg.Def(opID)
	require.True(t, ok)
	var raw json.RawMessage
	if params != nil {
		b, err := json.Marshal(params)
		require.NoError(t, err)
		raw = b
	}
	rep := &splitRelinkReporter{}
	return rep, def.Run(context.Background(), raw, rep)
}

// The scheduled trigger's exact params value is a dry run.
func TestAuthorSplitScanOp_ScheduledParamsAreDryRun(t *testing.T) {
	st := newSplitRelinkStore(t)
	composite, err := st.CreateAuthor("Alice Smith & Bob Jones")
	require.NoError(t, err)
	book := mkSplitTrashedBook(t, st, composite.ID)

	rep, err := runSchedulerAuthorOp(t, st, ExtraOpsDeps{}, "scheduler.author-split-scan", schedulerExtraOpParams{})
	require.NoError(t, err)

	still, err := st.GetAuthorByID(composite.ID)
	require.NoError(t, err)
	require.NotNil(t, still, "scheduled dry run deleted the composite")
	alice, err := st.GetAuthorByName("Alice Smith")
	require.NoError(t, err)
	require.Nil(t, alice, "scheduled dry run created a split author")
	credits, err := st.GetBookAuthors(book)
	require.NoError(t, err)
	require.Len(t, credits, 1)
	require.Equal(t, composite.ID, credits[0].AuthorID)
	require.Contains(t, strings.Join(rep.logs, "\n"), "DRY RUN: would split 1 composite authors")
}

func TestAuthorSplitScanOp_DisagreeingKeysAreAnError(t *testing.T) {
	st := newSplitRelinkStore(t)
	_, err := runSchedulerAuthorOp(t, st, ExtraOpsDeps{}, "scheduler.author-split-scan",
		json.RawMessage(`{"dry_run":false,"dryRun":true}`))
	require.ErrorContains(t, err, "disagree")
}

// The dry run neither fetches (which applies metadata) nor invalidates the
// dedup cache; the apply invalidates it.
func TestResolveProductionAuthorsOp_DefaultIsDryRun(t *testing.T) {
	st := newSplitRelinkStore(t)
	prod, err := st.CreateAuthor("Tantor Media")
	require.NoError(t, err)
	mkSplitTrashedBook(t, st, prod.ID)

	c := cache.New[gin.H]("test-dedup", time.Hour)
	c.Set("author-duplicates", gin.H{"k": 1})
	deps := ExtraOpsDeps{DedupCache: c}

	rep, err := runSchedulerAuthorOp(t, st, deps, "scheduler.resolve-production-authors", schedulerExtraOpParams{})
	require.NoError(t, err)
	_, cached := c.Get("author-duplicates")
	require.True(t, cached, "dry run invalidated the dedup cache")
	require.Contains(t, strings.Join(rep.logs, "\n"), "DRY RUN: would re-fetch metadata for")

	_, err = runSchedulerAuthorOp(t, st, deps, "scheduler.resolve-production-authors", json.RawMessage(`{"dry_run":false}`))
	require.NoError(t, err)
	_, cached = c.Get("author-duplicates")
	require.False(t, cached, "apply should invalidate the dedup cache")
}
