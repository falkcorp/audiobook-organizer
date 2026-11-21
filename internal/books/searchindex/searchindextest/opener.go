// file: internal/books/searchindex/searchindextest/opener.go
// version: 0.1.0
// guid: aa56853d-b575-4bb7-a401-610002c5f05e
// last-edited: 2026-11-20

// Package searchindextest is test support for internal/books/searchindex: a
// stand-in index.Opener over bleve, and the native half of the
// TestBooksIndexRebuiltAtCutover harness.
//
// The stand-in exists because core's engine (core/internal/bleveindex) cannot be
// imported from this module (Go's internal rule). It mirrors that engine's
// translation of index.Query and its on-disk layout
// (<db parent>/index/<module>/<name>.bleve), nothing more: no dirty set, no
// facets, no reconciler. It is test code. The fold replaces it with the public
// engine fixture core is adding (seams.d/F7-09-T6.md, "switch at the fold").
package searchindextest

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/blevesearch/bleve/v2"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/keyword"
	"github.com/blevesearch/bleve/v2/analysis/analyzer/standard"
	"github.com/blevesearch/bleve/v2/mapping"
	"github.com/blevesearch/bleve/v2/search/query"
	"github.com/falkcorp/mediacore/storage"
	"github.com/falkcorp/mediacore/storage/index"
)

// IndexDir is where the engine keeps an index: <db parent>/index/<module>/<name>.bleve.
func IndexDir(dbParent, module, name string) string {
	return filepath.Join(dbParent, "index", module, name+".bleve")
}

// Opener is the stand-in index.Opener.
type Opener struct {
	DBParent string
	Module   string
	// HoldRebuild, if non-nil, parks the rebuild until it is closed, so a test can
	// observe the index while it is Rebuilding.
	HoldRebuild <-chan struct{}
}

// Open opens (creating if need be) the index for s and starts its rebuild in the
// background. The index is Rebuilding until the rebuild returns; a rebuild
// failure leaves it Degraded.
func (o *Opener) Open(ctx context.Context, s index.Spec) (index.Index, error) {
	if err := s.Validate(); err != nil {
		return nil, err
	}
	dir := IndexDir(o.DBParent, o.Module, s.Name)
	if err := os.RemoveAll(dir); err != nil { // the mapping version is a baseline here: always rebuild
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o750); err != nil {
		return nil, err
	}
	m, kinds, err := buildMapping(s)
	if err != nil {
		return nil, err
	}
	bi, err := bleve.New(dir, m)
	if err != nil {
		return nil, err
	}
	rctx, cancel := context.WithCancel(context.Background())
	ix := &idx{bi: bi, kinds: kinds, cancel: cancel, done: make(chan struct{})}
	ix.state.Store(uint32(index.Rebuilding))
	go func() {
		defer close(ix.done)
		if o.HoldRebuild != nil {
			select {
			case <-o.HoldRebuild:
			case <-rctx.Done():
				return
			}
		}
		if err := s.Rebuild(rctx, writer{ix: ix, ctx: rctx}); err != nil {
			ix.setErr(err)
			ix.state.Store(uint32(index.Degraded))
			return
		}
		ix.state.Store(uint32(index.Ready))
	}()
	return ix, nil
}

type writer struct {
	ix  *idx
	ctx context.Context
}

func (w writer) Put(id string, d index.Doc) error {
	if err := w.ctx.Err(); err != nil {
		return err
	}
	return w.ix.bi.Index(id, map[string]any(d))
}

type idx struct {
	bi     bleve.Index
	kinds  map[string]index.FieldKind
	state  atomic.Uint32
	cancel context.CancelFunc
	done   chan struct{}

	mu  sync.Mutex
	err error
}

func (i *idx) setErr(err error) { i.mu.Lock(); i.err = err; i.mu.Unlock() }

// RebuildErr is the error a failed rebuild left, if any.
func (i *idx) RebuildErr() error { i.mu.Lock(); defer i.mu.Unlock(); return i.err }

// WaitRebuilt blocks until the rebuild has returned, and reports its error.
func (i *idx) WaitRebuilt(ctx context.Context) error {
	select {
	case <-i.done:
		return i.RebuildErr()
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Rebuilt is what a test needs beyond index.Index.
type Rebuilt interface {
	index.Index
	WaitRebuilt(ctx context.Context) error
	RebuildErr() error
}

var _ Rebuilt = (*idx)(nil)

func (i *idx) State() index.State { return index.State(i.state.Load()) }

func (i *idx) Search(ctx context.Context, q index.Query, p index.Page) (index.Result, error) {
	if i.State() == index.Degraded {
		return index.Result{}, index.ErrDegraded
	}
	bq, err := i.translate(q)
	if err != nil {
		return index.Result{}, err
	}
	req := bleve.NewSearchRequestOptions(bq, max(p.Size, 0), max(p.From, 0), false)
	if len(p.SortBy) > 0 {
		req.SortBy(p.SortBy)
	} else {
		req.SortBy([]string{"-_score", "_id"})
	}
	res, err := i.bi.SearchInContext(ctx, req)
	if err != nil {
		return index.Result{}, err
	}
	out := index.Result{Total: res.Total}
	for _, h := range res.Hits {
		out.Hits = append(out.Hits, index.Hit{ID: h.ID, Score: h.Score})
	}
	return out, nil
}

func (i *idx) Count(ctx context.Context, q index.Query) (uint64, error) {
	r, err := i.Search(ctx, q, index.Page{Size: 0})
	return r.Total, err
}

func (i *idx) Facets(context.Context, index.Query, []string, int) (map[string][]index.FacetValue, error) {
	return nil, errors.New("searchindextest: facets are not implemented by the stand-in")
}

func (i *idx) MarkDirty(*storage.Batch, string) error {
	return errors.New("searchindextest: the stand-in has no dirty set")
}

func (i *idx) Close() error {
	i.cancel()
	<-i.done
	return i.bi.Close()
}

func buildMapping(s index.Spec) (mapping.IndexMapping, map[string]index.FieldKind, error) {
	im := bleve.NewIndexMapping()
	doc := bleve.NewDocumentMapping()
	doc.Dynamic = false
	kinds := map[string]index.FieldKind{}
	for _, f := range s.Fields {
		kinds[f.Name] = f.Kind
		var fm *mapping.FieldMapping
		switch f.Kind {
		case index.Text:
			fm = bleve.NewTextFieldMapping()
			fm.Analyzer = standard.Name
			if f.Analyzer == "keyword" {
				fm.Analyzer = keyword.Name
			} else if f.Analyzer != "" && f.Analyzer != "standard" {
				return nil, nil, fmt.Errorf("searchindextest: analyzer %q is not supported by the stand-in", f.Analyzer)
			}
		case index.Keyword:
			fm = bleve.NewTextFieldMapping()
			fm.Analyzer = keyword.Name
		case index.Number:
			fm = bleve.NewNumericFieldMapping()
		case index.Date:
			fm = bleve.NewDateTimeFieldMapping()
		case index.Bool:
			fm = bleve.NewBooleanFieldMapping()
		default:
			return nil, nil, fmt.Errorf("searchindextest: field %q has kind %v", f.Name, f.Kind)
		}
		fm.Store, fm.IncludeInAll = false, false
		doc.AddFieldMappingsAt(f.Name, fm)
	}
	im.DefaultMapping = doc
	return im, kinds, nil
}

// Budgets mirror core's engine (mapping.go).
const (
	maxNodes  = 1024
	maxDepth  = 32
	maxStrLen = 4 << 10
)

// translate mirrors core's translation of the engine-facing tree.
func (i *idx) translate(q index.Query) (query.Query, error) {
	n, err := index.Describe(q)
	if err != nil {
		return nil, err
	}
	count := 0
	return i.node(n, 0, &count)
}

func (i *idx) node(n index.Node, depth int, count *int) (query.Query, error) {
	*count++
	if *count > maxNodes || depth > maxDepth {
		return nil, fmt.Errorf("%w: query too large", index.ErrInvalidQuery)
	}
	field := func() error {
		if _, ok := i.kinds[n.Field]; !ok {
			return fmt.Errorf("%w: no field %q", index.ErrInvalidQuery, n.Field)
		}
		return nil
	}
	switch n.Op {
	case index.OpMatch:
		if err := field(); err != nil {
			return nil, err
		}
		q := bleve.NewMatchQuery(n.Text)
		q.SetField(n.Field)
		return q, nil
	case index.OpTerm:
		if err := field(); err != nil {
			return nil, err
		}
		q := bleve.NewTermQuery(n.Text)
		q.SetField(n.Field)
		return q, nil
	case index.OpPrefix:
		if err := field(); err != nil {
			return nil, err
		}
		q := bleve.NewPrefixQuery(n.Text)
		q.SetField(n.Field)
		return q, nil
	case index.OpRange:
		if err := field(); err != nil {
			return nil, err
		}
		return i.rangeNode(n)
	case index.OpAnd, index.OpOr:
		kids := make([]query.Query, 0, len(n.Children))
		for _, c := range n.Children {
			k, err := i.node(c, depth+1, count)
			if err != nil {
				return nil, err
			}
			kids = append(kids, k)
		}
		if n.Op == index.OpAnd {
			return bleve.NewConjunctionQuery(kids...), nil
		}
		return bleve.NewDisjunctionQuery(kids...), nil
	case index.OpNot:
		k, err := i.node(n.Children[0], depth+1, count)
		if err != nil {
			return nil, err
		}
		*count += 63 // core charges a Not 64 nodes
		b := bleve.NewBooleanQuery()
		b.AddMust(bleve.NewMatchAllQuery())
		b.AddMustNot(k)
		return b, nil
	case index.OpString:
		if len(n.Text) > maxStrLen {
			return nil, fmt.Errorf("%w: query string too long", index.ErrInvalidQuery)
		}
		if strings.Contains(n.Text, "/") && regexLike(n.Text) {
			return nil, fmt.Errorf("%w: regular expressions are not supported", index.ErrInvalidQuery)
		}
		parsed, err := query.NewQueryStringQuery(n.Text).Parse()
		if err != nil {
			return nil, fmt.Errorf("%w: %v", index.ErrInvalidQuery, err)
		}
		if leadingWildcard(parsed) {
			return nil, fmt.Errorf("%w: a leading wildcard is not supported", index.ErrInvalidQuery)
		}
		return parsed, nil
	}
	return nil, fmt.Errorf("%w: operator %v", index.ErrInvalidQuery, n.Op)
}

// regexLike reports an unescaped /…/ pair.
func regexLike(s string) bool {
	open := false
	esc := false
	for _, r := range s {
		switch {
		case esc:
			esc = false
		case r == '\\':
			esc = true
		case r == '/':
			open = !open
		}
	}
	return !open && strings.Count(strings.ReplaceAll(s, `\/`, ""), "/") >= 2
}

func leadingWildcard(q query.Query) bool {
	switch v := q.(type) {
	case *query.WildcardQuery:
		return strings.HasPrefix(v.Wildcard, "*") || strings.HasPrefix(v.Wildcard, "?")
	case *query.RegexpQuery:
		return true
	case *query.ConjunctionQuery:
		if slices.ContainsFunc(v.Conjuncts, leadingWildcard) {
			return true
		}
	case *query.DisjunctionQuery:
		if slices.ContainsFunc(v.Disjuncts, leadingWildcard) {
			return true
		}
	case *query.BooleanQuery:
		for _, c := range []query.Query{v.Must, v.Should, v.MustNot} {
			if c != nil && leadingWildcard(c) {
				return true
			}
		}
	}
	return false
}

func (i *idx) rangeNode(n index.Node) (query.Query, error) {
	t := true
	switch i.kinds[n.Field] {
	case index.Number:
		lo, err := toFloat(n.Min)
		if err != nil {
			return nil, err
		}
		hi, err := toFloat(n.Max)
		if err != nil {
			return nil, err
		}
		q := bleve.NewNumericRangeInclusiveQuery(lo, hi, &t, &t)
		q.SetField(n.Field)
		return q, nil
	case index.Date:
		var lo, hi time.Time
		if v, ok := n.Min.(time.Time); ok {
			lo = v
		}
		if v, ok := n.Max.(time.Time); ok {
			hi = v
		}
		q := bleve.NewDateRangeInclusiveQuery(lo, hi, &t, &t)
		q.SetField(n.Field)
		return q, nil
	}
	return nil, fmt.Errorf("%w: field %q is not rangeable", index.ErrInvalidQuery, n.Field)
}

func toFloat(v any) (*float64, error) {
	switch x := v.(type) {
	case nil:
		return nil, nil
	case int64:
		f := float64(x)
		return &f, nil
	case float64:
		return &x, nil
	}
	return nil, fmt.Errorf("%w: numeric bound of type %T", index.ErrInvalidQuery, v)
}
