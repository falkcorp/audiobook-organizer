// file: core/httpapi/page_test.go
// version: 1.0.0
// guid: 3ad5265f-f483-41f8-87b7-ba6456e4ad26
// last-edited: 2026-11-07

package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/rbac"
)

type listGadgetsReq struct {
	PageParams
	Owner string `query:"owner"`
}

func TestPage_OffsetDefaultHasTotal(t *testing.T) {
	var seen []PageParams
	r, g := newRouter(t, "listGadgets")
	Handle(g, Route{Method: http.MethodGet, Path: "/gadgets", Name: "listGadgets", Perm: rbac.Public},
		func(_ context.Context, in *In[listGadgetsReq]) (Page[Gadget], error) {
			seen = append(seen, in.Req.PageParams)
			return Page[Gadget]{Items: []Gadget{}, Limit: in.Req.Limit, Offset: in.Req.Offset}, nil
		})
	h := mustBuild(t, r)

	rec := serve(h, http.MethodGet, "/api/batcave/v1/gadgets?owner=Selina+Kyle", "")
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	// Total is present even when zero: offset paging always carries it (SIMP-8).
	if _, ok := body["total"]; !ok || body["limit"] != float64(defaultPageLimit) {
		t.Fatalf("default page = %s", rec.Body.String())
	}
	for _, q := range []string{"limit=0", "limit=5000&offset=-3", "limit=7&offset=14"} {
		serve(h, http.MethodGet, "/api/batcave/v1/gadgets?"+q, "")
	}
	want := []PageParams{{50, 0}, {50, 0}, {1000, 0}, {7, 14}}
	if fmt.Sprint(seen) != fmt.Sprint(want) {
		t.Fatalf("normalised params = %v, want %v", seen, want)
	}
	rec = serve(h, http.MethodGet, "/api/batcave/v1/gadgets?limit=many", "")
	var p Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil || rec.Code != 422 || p.Errors[0].Field != "/query/limit" {
		t.Fatalf("bad limit = %d %s", rec.Code, rec.Body.String())
	}
}

// feed yields n gadgets positioned by index.
func feed(n int) iter.Seq2[Positioned[string], error] {
	return func(yield func(Positioned[string], error) bool) {
		for i := range n {
			if !yield(Positioned[string]{Pos: []byte{byte(i)}, Item: fmt.Sprintf("gadget-%d", i)}, nil) {
				return
			}
		}
	}
}

func TestCursor_FilterMismatch(t *testing.T) {
	const owner, other uint64 = 0xB47, 0x7015
	cur := EncodeCursor([]byte("cave/42"), owner)
	pos, err := DecodeCursor(cur, owner)
	if err != nil || string(pos) != "cave/42" {
		t.Fatalf("round trip = %q, %v", pos, err)
	}
	var he *Error
	if _, err := DecodeCursor(cur, other); !errors.As(err, &he) || he.Status != 400 || he.Code != "invalid_cursor" {
		t.Fatalf("other filter = %v, want 400 invalid_cursor", err)
	}
	if _, err := DecodeCursor("!!", owner); !errors.As(err, &he) || he.Code != "invalid_cursor" {
		t.Fatalf("malformed = %v, want invalid_cursor", err)
	}
	if _, err := PaginateCursor(t.Context(), CursorParams{Cursor: cur, Limit: 2}, feed(5), other); !errors.As(err, &he) {
		t.Fatalf("PaginateCursor with another filter = %v, want invalid_cursor", err)
	}

	page, err := PaginateCursor(t.Context(), CursorParams{Limit: 2}, feed(5), owner)
	if err != nil || strings.Join(page.Items, ",") != "gadget-0,gadget-1" {
		t.Fatalf("first page = %+v, %v", page, err)
	}
	next, err := DecodeCursor(page.NextCursor, owner)
	if err != nil || len(next) != 1 || next[0] != 2 {
		t.Fatalf("NextCursor position = %v, %v; want the first item not returned", next, err)
	}
	last, err := PaginateCursor(t.Context(), CursorParams{Limit: 10}, feed(3), owner)
	if err != nil || len(last.Items) != 3 || last.NextCursor != "" {
		t.Fatalf("last page = %+v, %v", last, err)
	}
}
