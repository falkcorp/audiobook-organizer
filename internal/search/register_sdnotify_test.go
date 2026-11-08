// file: internal/search/register_sdnotify_test.go
// version: 2.0.0
// guid: a1da032b-4362-43dd-8fbe-154f8e481e27
// last-edited: 2026-11-08

package search

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/config"
	"github.com/stretchr/testify/require"
)

// TestIndexServiceStartExtendsStartTimeout: opening the Bleve index is a long
// startup step, so IndexService.Start calls the injected extender (the
// module's Deps.ExtendStart) before it opens the index, and not before.
func TestIndexServiceStartExtendsStartTimeout(t *testing.T) {
	svc := &IndexService{cfg: &config.Config{DatabasePath: filepath.Join(t.TempDir(), "audiobooks.pebble")}}
	var steps []string
	var openedAtCall bool
	svc.SetStartExtender(func(step string) {
		steps = append(steps, step)
		openedAtCall = svc.idx != nil
	})
	require.NoError(t, svc.Start(context.Background()))
	t.Cleanup(func() { _ = svc.Stop(context.Background()) })

	require.Equal(t, []string{"search index open"}, steps)
	require.False(t, openedAtCall, "the extension must be requested before the index opens")
	require.NotNil(t, svc.idx, "the index opened")

	// An already-open service has no step to extend for (Start is idempotent).
	require.NoError(t, svc.Start(context.Background()))
	require.Len(t, steps, 1)
}

// TestIndexServiceStartWithoutExtenderOpens: no injected extender (tests, a
// build without the kernel) leaves Start working, and SetStartExtender(nil)
// removes one.
func TestIndexServiceStartWithoutExtenderOpens(t *testing.T) {
	svc := &IndexService{cfg: &config.Config{DatabasePath: filepath.Join(t.TempDir(), "audiobooks.pebble")}}
	called := false
	svc.SetStartExtender(func(string) { called = true })
	svc.SetStartExtender(nil)
	require.NoError(t, svc.Start(context.Background()))
	t.Cleanup(func() { _ = svc.Stop(context.Background()) })
	require.False(t, called)
	require.NotNil(t, svc.idx)

	var nilSvc *IndexService
	nilSvc.SetStartExtender(func(string) {}) // a nil service ignores it, like its other methods
}
