// file: internal/books/searchindex/translate_internal_test.go
// version: 0.1.0
// guid: 2c7e91d4-5b08-4f36-a1d9-6e4b0c8f3a57
// last-edited: 2026-11-28

package searchindex

import (
	"errors"
	"testing"

	"github.com/blevesearch/bleve/v2"
	"github.com/falkcorp/mediacore/storage/index"
)

// These tests hand convert the bleve trees HEAD can build and read the index
// query it returns, for the cases a parsed search cannot isolate: HEAD's
// translator already lowercases a pattern term, so the lowercasing that belongs
// to books (a fuzzy term is not analysed by the engine) shows only here.

func describe(t *testing.T, q index.Query) index.Node {
	t.Helper()
	n, err := index.Describe(q)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestFuzzyTermIsLowercasedByBooks(t *testing.T) {
	fq := bleve.NewFuzzyQuery("KNIGHTFAL")
	fq.SetField("title")
	q, _, err := convert(fq)
	if err != nil {
		t.Fatal(err)
	}
	if n := describe(t, q); n.Op != index.OpString || n.Text != "title:knightfal~1" {
		t.Errorf("fuzzy = %+v, want QueryString title:knightfal~1 (lowercased: the engine does not analyse a fuzzy term)", n)
	}
}

func TestLeafWeightIsCarriedAndAGroupsIsDropped(t *testing.T) {
	mq := bleve.NewMatchQuery("gotham")
	mq.SetField("title")
	mq.SetBoost(3)
	q, _, err := convert(mq)
	if err != nil {
		t.Fatal(err)
	}
	if n := describe(t, q); n.Op != index.OpMatch || n.Boost != 3 {
		t.Errorf("weighted match = %+v, want Boost(Match, 3)", n)
	}

	// underscorePatternQuery weights the conjunction it builds for a multi-part
	// pattern. bleve never reads a conjunction's weight, so HEAD's scores carry
	// none; index.Boost on a group would multiply every leaf under it.
	a := bleve.NewMatchQuery("arcane")
	a.SetField("title")
	p := bleve.NewPrefixQuery("ch")
	p.SetField("title")
	cq := bleve.NewConjunctionQuery(a, p)
	cq.SetBoost(5)
	q, _, err = convert(cq)
	if err != nil {
		t.Fatal(err)
	}
	n := describe(t, q)
	if n.Op != index.OpAnd || n.Boost != 0 {
		t.Fatalf("conjunction = %+v, want an unweighted And", n)
	}
	for _, c := range n.Children {
		if c.Boost != 0 {
			t.Errorf("leaf %+v picked up the group's weight", c)
		}
	}
}

func TestWildcardWithNoLiteralCharacterIsRefused(t *testing.T) {
	wq := bleve.NewWildcardQuery("*")
	wq.SetField("title")
	if _, _, err := convert(wq); !errors.Is(err, ErrNotExpressible) {
		t.Errorf("a bare * = %v, want ErrNotExpressible", err)
	}
}

func TestUnfieldedWildcardFansOutOverTheTextFields(t *testing.T) {
	wq := bleve.NewWildcardQuery("*ight*")
	q, _, err := convert(wq)
	if err != nil {
		t.Fatal(err)
	}
	n := describe(t, q)
	if n.Op != index.OpOr || len(n.Children) != len(allFields) {
		t.Fatalf("unfielded wildcard = %+v, want an Or over %d fields", n, len(allFields))
	}
	for _, c := range n.Children {
		if c.Op != index.OpWildcard || c.Text != "*ight*" {
			t.Errorf("child = %+v", c)
		}
	}
}
