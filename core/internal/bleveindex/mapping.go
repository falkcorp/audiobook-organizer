// file: core/internal/bleveindex/mapping.go
// version: 1.0.0
// guid: 4a2f8d21-9b4c-4b9f-8c64-7e1d3a6c8b22
// last-edited: 2026-11-11

package bleveindex

import (
	"fmt"
	"strconv"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/keyword"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/standard"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search/query"

	"github.com/falkcorp/mediacore/storage/index"
)

// buildMapping turns a Spec's fields into a bleve mapping. The mapping is
// closed (Dynamic false): a document field the Spec did not declare is not
// indexed, so the on-disk index is exactly what MappingVersion names.
func buildMapping(s index.Spec) (mapping.IndexMapping, error) {
	im := bleve.NewIndexMapping()
	doc := bleve.NewDocumentMapping()
	doc.Dynamic = false
	for _, f := range s.Fields {
		var fm *mapping.FieldMapping
		switch f.Kind {
		case index.Text:
			fm = bleve.NewTextFieldMapping()
			a, err := analyzerName(f.Analyzer, standard.Name)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", f.Name, err)
			}
			fm.Analyzer = a
		case index.Keyword:
			fm = bleve.NewTextFieldMapping()
			a, err := analyzerName(f.Analyzer, keyword.Name)
			if err != nil {
				return nil, fmt.Errorf("field %q: %w", f.Name, err)
			}
			fm.Analyzer = a
		case index.Number:
			fm = bleve.NewNumericFieldMapping()
		case index.Date:
			fm = bleve.NewDateTimeFieldMapping()
		case index.Bool:
			fm = bleve.NewBooleanFieldMapping()
		default:
			return nil, fmt.Errorf("field %q: kind %s", f.Name, f.Kind)
		}
		fm.Store = f.Store
		fm.DocValues = f.Facet
		doc.AddFieldMappingsAt(f.Name, fm)
	}
	im.DefaultMapping = doc
	return im, nil
}

// analyzerName resolves a Field.Analyzer: "" is the kind's default, and
// "standard" and "keyword" are bleve's stock analyzers. A module-registered
// analyzer needs a registration surface the contract does not have yet, so any
// other name is refused rather than silently indexed with the default.
func analyzerName(name, def string) (string, error) {
	switch name {
	case "":
		return def, nil
	case "standard":
		return standard.Name, nil
	case "keyword":
		return keyword.Name, nil
	}
	return "", fmt.Errorf("%w: analyzer %q is not registered", index.ErrInvalidSpec, name)
}

// translate turns an engine-neutral query into a bleve query over this index's
// fields. A field the Spec does not declare is ErrInvalidQuery, never a silent
// match-nothing: a typo in a field name is a bug, not an empty result.
func translate(kinds map[string]index.FieldKind, n index.Node) (query.Query, error) {
	bad := func(format string, a ...any) error {
		return fmt.Errorf("%w: %s", index.ErrInvalidQuery, fmt.Sprintf(format, a...))
	}
	field := func() (index.FieldKind, error) {
		k, ok := kinds[n.Field]
		if !ok {
			return 0, bad("unknown field %q", n.Field)
		}
		return k, nil
	}
	switch n.Op {
	case index.OpMatch:
		k, err := field()
		if err != nil {
			return nil, err
		}
		if k != index.Text && k != index.Keyword {
			return nil, bad("match on %s field %q", k, n.Field)
		}
		q := bleve.NewMatchQuery(n.Text)
		q.SetField(n.Field)
		return q, nil
	case index.OpTerm:
		k, err := field()
		if err != nil {
			return nil, err
		}
		switch k {
		case index.Text, index.Keyword:
			q := bleve.NewTermQuery(n.Text)
			q.SetField(n.Field)
			return q, nil
		case index.Bool:
			v, err := strconv.ParseBool(n.Text)
			if err != nil {
				return nil, bad("term %q on bool field %q", n.Text, n.Field)
			}
			q := bleve.NewBoolFieldQuery(v)
			q.SetField(n.Field)
			return q, nil
		case index.Number:
			v, err := strconv.ParseFloat(n.Text, 64)
			if err != nil {
				return nil, bad("term %q on number field %q", n.Text, n.Field)
			}
			incl := true
			q := bleve.NewNumericRangeInclusiveQuery(&v, &v, &incl, &incl)
			q.SetField(n.Field)
			return q, nil
		}
		return nil, bad("term on %s field %q", k, n.Field)
	case index.OpPrefix:
		k, err := field()
		if err != nil {
			return nil, err
		}
		if k != index.Text && k != index.Keyword {
			return nil, bad("prefix on %s field %q", k, n.Field)
		}
		q := bleve.NewPrefixQuery(n.Text)
		q.SetField(n.Field)
		return q, nil
	case index.OpRange:
		k, err := field()
		if err != nil {
			return nil, err
		}
		return translateRange(k, n, bad)
	case index.OpAnd, index.OpOr:
		qs := make([]query.Query, 0, len(n.Children))
		for _, c := range n.Children {
			q, err := translate(kinds, c)
			if err != nil {
				return nil, err
			}
			qs = append(qs, q)
		}
		if n.Op == index.OpAnd {
			return bleve.NewConjunctionQuery(qs...), nil
		}
		return bleve.NewDisjunctionQuery(qs...), nil
	case index.OpNot:
		if len(n.Children) != 1 {
			return nil, bad("not takes one query")
		}
		c, err := translate(kinds, n.Children[0])
		if err != nil {
			return nil, err
		}
		// bleve's boolean query needs a positive clause: "everything except c".
		q := bleve.NewBooleanQuery()
		q.AddMust(bleve.NewMatchAllQuery())
		q.AddMustNot(c)
		return q, nil
	case index.OpString:
		return bleve.NewQueryStringQuery(n.Text), nil
	}
	return nil, bad("operator %d", n.Op)
}

// translateRange builds a numeric or date range. An open end is a nil number
// pointer or a zero time. An int64 bound is held as a float64 (bleve's numeric
// fields are), exact up to 2^53.
func translateRange(k index.FieldKind, n index.Node, bad func(string, ...any) error) (query.Query, error) {
	incl := true
	switch k {
	case index.Number:
		lo, err := num(n.Min)
		if err != nil {
			return nil, bad("range on number field %q: %v", n.Field, err)
		}
		hi, err := num(n.Max)
		if err != nil {
			return nil, bad("range on number field %q: %v", n.Field, err)
		}
		q := bleve.NewNumericRangeInclusiveQuery(lo, hi, &incl, &incl)
		q.SetField(n.Field)
		return q, nil
	case index.Date:
		lo, err := when(n.Min)
		if err != nil {
			return nil, bad("range on date field %q: %v", n.Field, err)
		}
		hi, err := when(n.Max)
		if err != nil {
			return nil, bad("range on date field %q: %v", n.Field, err)
		}
		q := bleve.NewDateRangeInclusiveQuery(lo, hi, &incl, &incl)
		q.SetField(n.Field)
		return q, nil
	}
	return nil, bad("range on %s field %q", k, n.Field)
}

func num(v any) (*float64, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case int64:
		f := float64(x)
		return &f, nil
	case float64:
		return &x, nil
	}
	return nil, fmt.Errorf("bound %T is not a number", v)
}

func when(v any) (time.Time, error) {
	switch x := v.(type) {
	case nil:
		return time.Time{}, nil
	case time.Time:
		return x, nil
	}
	return time.Time{}, fmt.Errorf("bound %T is not a time", v)
}
