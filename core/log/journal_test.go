// file: core/log/journal_test.go
// version: 1.0.0
// guid: 8a2d4c71-9e03-4f6b-b5d8-1c7e3a09f642
// last-edited: 2026-11-13

package log_test

import (
	"context"
	"go/parser"
	"go/token"
	"reflect"
	"slices"
	"strconv"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/log"
)

// stubJournal is the smallest value that is both a JournalStore and an
// Importer: the compile-time half of the SDK's frozen shape.
type stubJournal struct{}

func (stubJournal) Append(context.Context, []log.Record) error { return nil }
func (stubJournal) Query(context.Context, log.JournalQuery) (log.JournalPage, error) {
	return log.JournalPage{}, nil
}
func (stubJournal) Prune(context.Context, time.Time, int) (int, bool, error) { return 0, false, nil }
func (stubJournal) Import(context.Context, []log.ImportedRecord) error       { return nil }

var (
	_ log.JournalStore = stubJournal{}
	_ log.Importer     = stubJournal{}
)

// TestJournalSDK_Shape pins the declared method sets and fields of 05-logging
// §3's journal types: they are frozen by the API-stability gate, so a change
// here must be a decision, not a drive-by edit.
func TestJournalSDK_Shape(t *testing.T) {
	methods := func(v any) []string {
		ty := reflect.TypeOf(v).Elem()
		var out []string
		for method := range ty.Methods() {
			out = append(out, method.Name+" "+method.Type.String())
		}
		return out
	}
	wantStore := []string{
		"Append func(context.Context, []log.Record) error",
		"Prune func(context.Context, time.Time, int) (int, bool, error)",
		"Query func(context.Context, log.JournalQuery) (log.JournalPage, error)",
	}
	if got := methods((*log.JournalStore)(nil)); !slices.Equal(got, wantStore) {
		t.Errorf("JournalStore methods = %q, want %q", got, wantStore)
	}
	wantImporter := []string{"Import func(context.Context, []log.ImportedRecord) error"}
	if got := methods((*log.Importer)(nil)); !slices.Equal(got, wantImporter) {
		t.Errorf("Importer methods = %q, want %q", got, wantImporter)
	}

	fields := func(v any) []string {
		ty := reflect.TypeOf(v)
		var out []string
		for f := range ty.Fields() {
			out = append(out, f.Name+" "+f.Type.String())
		}
		return out
	}
	cases := []struct {
		name string
		v    any
		want []string
	}{
		{"ImportedRecord", log.ImportedRecord{}, []string{"Record log.Record", "SourceID string"}},
		{"JournalQuery", log.JournalQuery{}, []string{"MinLevel slog.Level", "Module string", "Since time.Time", "Until time.Time", "Text string", "Cursor string", "Limit int"}},
		{"JournalPage", log.JournalPage{}, []string{"Records []log.Record", "Next string"}},
	}
	for _, c := range cases {
		if got := fields(c.v); !slices.Equal(got, c.want) {
			t.Errorf("%s fields = %q, want %q", c.name, got, c.want)
		}
	}
	// ImportedRecord embeds Record, so an importer reads rec.Time directly.
	var ir log.ImportedRecord
	ir.Record.Message = "the signal"
	if ir.Message != "the signal" {
		t.Errorf("ImportedRecord does not promote Record's fields")
	}
}

// TestJournalSDK_NamesOnlyLog: log is L0 (R4), and the journal types name
// nothing but the standard library and log's own types, so log never imports
// storage for them. The check reads journal.go's own import block.
func TestJournalSDK_NamesOnlyLog(t *testing.T) {
	f, err := parser.ParseFile(token.NewFileSet(), "journal.go", nil, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	allowed := []string{"context", "log/slog", "time"}
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Contains(allowed, p) {
			t.Errorf("journal.go imports %q: the SDK types may import only %q", p, allowed)
		}
	}
}
