// file: core/storage/index/query_test.go
// version: 1.0.0
// guid: 98edda0b-19f9-47d7-af16-403bdfa4f5d0
// last-edited: 2026-11-11

package index_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/falkcorp/mediacore/storage/index"
)

//go:fix inline
func TestQueryConstructorsDescribeTheirTree(t *testing.T) {
	d1 := time.Date(1939, time.May, 1, 0, 0, 0, 0, time.UTC)
	q := index.And(
		index.Match("title", "dark knight"),
		index.Or(index.Term("genre", "noir"), index.Prefix("author", "kane")),
		index.Not(index.Term("language", "fr")),
		index.Range("year", new(int64(1939)), nil),
		index.Range("published", nil, &d1),
		index.QueryString("author:finger"),
	)
	n, err := index.Describe(q)
	if err != nil {
		t.Fatal(err)
	}
	if n.Op != index.OpAnd || len(n.Children) != 6 {
		t.Fatalf("root %+v", n)
	}
	if c := n.Children[0]; c.Op != index.OpMatch || c.Field != "title" || c.Text != "dark knight" {
		t.Fatalf("match %+v", c)
	}
	if c := n.Children[1]; c.Op != index.OpOr || c.Children[1].Op != index.OpPrefix {
		t.Fatalf("or %+v", c)
	}
	if c := n.Children[2]; c.Op != index.OpNot || len(c.Children) != 1 {
		t.Fatalf("not %+v", c)
	}
	if c := n.Children[3]; c.Min != int64(1939) || c.Max != nil {
		t.Fatalf("int range %+v", c)
	}
	if c := n.Children[4]; c.Min != nil || c.Max != d1 {
		t.Fatalf("time range %+v", c)
	}
	if c := n.Children[5]; c.Op != index.OpString || c.Text != "author:finger" {
		t.Fatalf("query string %+v", c)
	}
}

func TestInvalidQueriesAreReportedNotPanicked(t *testing.T) {
	for name, q := range map[string]index.Query{
		"nil":             nil,
		"empty field":     index.Term("", "x"),
		"empty and":       index.And(),
		"empty or":        index.Or(),
		"unbounded range": index.Range[int64]("n", nil, nil),
		"inverted range":  index.Range("n", new(2.5), new(1.5)),
		"bad child":       index.And(index.Term("a", "b"), index.Match("", "x")),
		"nil child":       index.Not(nil),
	} {
		if _, err := index.Describe(q); !errors.Is(err, index.ErrInvalidQuery) {
			t.Errorf("%s: %v, want ErrInvalidQuery", name, err)
		}
	}
}

func TestSpecValidate(t *testing.T) {
	ok := index.Spec{
		Name: "batcomputer", MappingVersion: "1",
		Fields:   []index.Field{{Name: "title", Kind: index.Text}, {Name: "genre", Kind: index.Keyword, Facet: true}},
		Rebuild:  func(context.Context, index.RebuildWriter) error { return nil },
		Document: func(context.Context, string) (index.Doc, bool, error) { return nil, false, nil },
	}
	if err := ok.Validate(); err != nil {
		t.Fatal(err)
	}
	for name, mut := range map[string]func(*index.Spec){
		"name":          func(s *index.Spec) { s.Name = "Bat Computer" },
		"version":       func(s *index.Spec) { s.MappingVersion = " " },
		"no rebuild":    func(s *index.Spec) { s.Rebuild = nil },
		"no document":   func(s *index.Spec) { s.Document = nil },
		"no fields":     func(s *index.Spec) { s.Fields = nil },
		"dup field":     func(s *index.Spec) { s.Fields = append(s.Fields, s.Fields[0]) },
		"reserved name": func(s *index.Spec) { s.Fields = []index.Field{{Name: "_id", Kind: index.Text}} },
		"bad kind":      func(s *index.Spec) { s.Fields = []index.Field{{Name: "x"}} },
		"number facet":  func(s *index.Spec) { s.Fields = []index.Field{{Name: "n", Kind: index.Number, Facet: true}} },
		"date analyzer": func(s *index.Spec) { s.Fields = []index.Field{{Name: "d", Kind: index.Date, Analyzer: "standard"}} },
	} {
		s := ok
		s.Fields = append([]index.Field(nil), ok.Fields...)
		mut(&s)
		err := s.Validate()
		if !errors.Is(err, index.ErrInvalidSpec) {
			t.Errorf("%s: %v, want ErrInvalidSpec", name, err)
		} else if !strings.Contains(err.Error(), "batcomputer") && name != "name" {
			t.Errorf("%s: error does not name the index: %v", name, err)
		}
	}
}

func TestStateAndKindNames(t *testing.T) {
	if index.Ready.String() != "ready" || index.Rebuilding.String() != "rebuilding" ||
		index.Degraded.String() != "degraded" || index.StateInvalid.String() != "invalid" {
		t.Fatal("state names")
	}
	if index.Text.String() != "text" || index.FieldInvalid.String() != "invalid" {
		t.Fatal("kind names")
	}
}
