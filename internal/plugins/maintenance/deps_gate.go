// file: internal/plugins/maintenance/deps_gate.go
// version: 0.1.0
// guid: 4c7e1a95-2d38-4b06-8f41-a09e63d2b715
// last-edited: 2026-11-20

package maintenance

import (
	"context"
	"fmt"
	"io/fs"
	"reflect"

	"github.com/falkcorp/mediacore/entity"
	"github.com/falkcorp/mediacore/library"
	"github.com/falkcorp/mediacore/tasks"

	database "github.com/falkcorp/audiobook-organizer/internal/books/store"
	"github.com/falkcorp/audiobook-organizer/internal/books/writer"
	"github.com/falkcorp/audiobook-organizer/internal/logger"
	"github.com/falkcorp/audiobook-organizer/pkg/ops"
)

// The books module's own dependency values for the converted groups (F5-18c).
//
// Each group's t6g<k>Deps is declared by the group and built by its owner. These
// builders are the owner's side: a Store whose method set is exactly the group's
// read interface, a NewWriter whose every method is ONE writer.Gate.Do over the
// run's tasks.Writer stamped with the method's whole row, and the plain
// dependencies the shells need. They are dark: nothing calls a builder until
// F5-18 hands its result to the group's Kinds function.
//
// They do not build on the head* adapters (headT6G2Store, headT6g3Writer, ...).
// Those read the plugin's own deps and are deleted with the v2 definitions; a
// builder that reused one would make that deletion a rewrite.

// booksGate is one run's gated write path for one group's writer.
type booksGate struct{ g *writer.Gate }

// newBooksGate builds the gate over the run's writer and the group's rows. A nil
// w is a ReadOnly kind (core hands a ReadOnly run no tasks.Writer): every gated
// write then refuses instead of dereferencing nil. The methods whose row is
// empty never reach w (see writer.Gate.Do), so they work for a ReadOnly kind as
// they did.
func newBooksGate(w tasks.Writer, rows map[string][]ops.Resource) booksGate {
	return booksGate{g: writer.ForRun(w, writer.Rows(rows))}
}

// runless is the context a writer method that takes none gives the gate. The
// executor's writer gates on the attempt's own state (its stop, its fence, its
// hold), never on the context it is handed, so a method with no context of its
// own loses nothing by it: the stopped-run refusal does not depend on this
// value, and the store calls these methods make take no context to cancel.
// NewWriter(w) carries no run context to give instead (changing it to carry one
// re-signs every deps factory of five groups and the tests of all of them; see
// seams.d/F5-18c.md).
func runless() context.Context { return context.Background() }

// do is one gated store call.
func (b booksGate) do(ctx context.Context, method string, subject entity.Ref, call func(context.Context) error) error {
	return b.g.Do(ctx, method, subject, func(ctx context.Context) ([]tasks.Change, error) { return nil, call(ctx) })
}

// srcCap resolves one optional capability of a store through its decorator
// chain, or says which one the store lacks. The groups whose source is the
// ops store use it for every method that is not on that interface.
func srcCap[T any](src any, what string) (T, error) {
	v, ok := database.AsCapability[T](src)
	if !ok {
		var zero T
		return zero, fmt.Errorf("store %T has no %s", src, what)
	}
	return v, nil
}

// bookRef and kindRef are the subjects of a gated write: the entity
// when the call names one, else the resource as a whole.
func bookRef(id string) entity.Ref {
	return tasks.ResID(writer.Module, string(ops.ResBooks), id)
}

func kindRef(r ops.Resource) entity.Ref { return tasks.Res(writer.Module, string(r)) }

// groupRows is a group's view of the shared method-to-resource table over the
// group's writer interfaces, with the group's SetRaw rename (a different key
// family per group). A method with no row is absent, which the gate refuses.
func groupRows(rename map[string]string, writers ...reflect.Type) map[string][]ops.Resource {
	return t6WriterView(rename, writers...)
}

// reportPathOver is the confining report-path resolver for a library root: the
// one Plugin.resolveReportPath is (library.ReportPathNotify), not a
// pass-through. An explicit path outside {root}/.reports is refused.
func reportPathOver(rootDir func() string) func(explicit, fileName string) (string, error) {
	return func(explicit, fileName string) (string, error) {
		return library.ReportPathNotify(rootDir(), explicit, fileName, func(dir string, was fs.FileMode) {
			logger.New("maintenance").Warn("removed world write from the report directory, its mode was %o dir=%s", was, logger.SanitizeLogValue(dir))
		})
	}
}
