// file: internal/server/handlers/opaccess/served_result_test.go
// version: 1.0.0
// guid: 5a0c83e7-29d1-4f6b-b4e8-0d7f1a9c3b52
// last-edited: 2026-10-26

package opaccess

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/falkcorp/audiobook-organizer/internal/auth"
	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// ServedResult removes zip_path from a diagnostics export's result for a caller
// without settings.manage and serves every other result unchanged.
func TestReader_ServedResult(t *testing.T) {
	const export = `{"zip_path":"/lib/Gotham/exports/hush.zip","narrowed_to":["library.view"]}`
	row := func(def string, data *string) *database.OperationV2Row {
		return &database.OperationV2Row{ID: "op", DefID: def, ResultData: data}
	}
	view := []auth.Permission{auth.PermLibraryView}
	settings := []auth.Permission{auth.PermLibraryView, auth.PermSettingsManage}

	c, _ := ctxHolding()
	got := NewRepairs(fixers{}, noOps, holdsOnly(view...)).For(c).ServedResult(row("diagnostics.export", ptr(export)))
	require.NotNil(t, got)
	require.NotContains(t, *got, "zip_path")
	require.NotContains(t, *got, "hush.zip")
	require.Contains(t, *got, `"narrowed_to":["library.view"]`, "the recorded scope stays")

	c, _ = ctxHolding()
	got = NewRepairs(fixers{}, noOps, holdsOnly(settings...)).For(c).ServedResult(row("diagnostics.export", ptr(export)))
	require.Equal(t, export, *got, "settings.manage reads it whole, as it may download it")

	c, _ = ctxHolding()
	scan := ptr(`{"zip_path":"kept","scanned":3}`)
	require.Equal(t, scan, NewRepairs(fixers{}, noOps, holdsOnly(view...)).For(c).ServedResult(row("library.scan", scan)),
		"another operation's result is not touched")

	c, _ = ctxHolding()
	require.Nil(t, NewRepairs(fixers{}, noOps, holdsOnly(view...)).For(c).ServedResult(row("diagnostics.export", ptr("{not json"))), "unparseable: withheld")
	require.Nil(t, NewRepairs(fixers{}, noOps, holdsOnly(view...)).For(c).ServedResult(row("diagnostics.export", nil)))
	require.Nil(t, NewRepairs(fixers{}, noOps, holdsOnly(view...)).For(c).ServedResult(nil))
	unwired := NewRepairs(fixers{}, noOps, nil).For(c).ServedResult(row("diagnostics.export", ptr(export)))
	require.NotNil(t, unwired)
	require.NotContains(t, *unwired, "zip_path", "an unwired check holds nothing")
}
