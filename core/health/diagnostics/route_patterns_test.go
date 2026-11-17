// file: core/health/diagnostics/route_patterns_test.go
// version: 1.0.0
// guid: 5b1e7c34-92a6-4d08-b3f1-6a0d8e2c7f49
// last-edited: 2026-11-16

package diagnostics

import (
	"fmt"
	"strings"
	"testing"
)

// routePlanted are the names no route field may carry out of a bundle.
var routePlanted = []string{"bruce", "Hush", "Users", "selina"}

func routeLeaks(s string) string {
	for _, n := range routePlanted {
		if strings.Contains(s, n) {
			return n
		}
	}
	return ""
}

// TestRoutePatterns_NilIsFailClosed: with no registered patterns (a nil
// Config.RoutePatterns, an empty list, or a list with no usable entry) no route
// value is kept: even a well-formed template goes through Text.
func TestRoutePatterns_NilIsFailClosed(t *testing.T) {
	for name, set := range map[string]routeSet{
		"nil set":        nil,
		"empty list":     newRouteSet([]string{}),
		"nil list":       newRouteSet(nil),
		"only locations": newRouteSet([]string{"/Users/bruce/Hush", "smb://nas.example/x", ""}),
	} {
		st := keysBuild()
		st.routes = set
		for _, key := range []string{"route", "pattern"} {
			for _, in := range []string{"GET /api/v1/books/{id}", "/api/v1/files/{path...}", "/livez", "/metrics", "GET /api/v1/books/bruce"} {
				out, changed := st.scrub(key, in)
				if out == in || !changed {
					t.Errorf("%s: scrub(%q, %q) = %q (changed %v), want it wiped", name, key, in, out, changed)
				}
				if n := routeLeaks(out); n != "" {
					t.Errorf("%s: scrub(%q, %q) = %q holds %q", name, key, in, out, n)
				}
			}
		}
	}
}

// TestRoutePatterns_ExactKept: a value that is exactly a registered pattern is
// kept verbatim, under either key, and is not counted as a change.
func TestRoutePatterns_ExactKept(t *testing.T) {
	pats := []string{"GET /api/v1/books/{id}", "/api/v1/files/{path...}", "POST /api/v1/x/", "/livez"}
	st := keysBuild()
	st.routes = newRouteSet(pats)
	for _, key := range []string{"route", "pattern"} {
		for _, p := range pats {
			if out, changed := st.scrub(key, p); out != p || changed {
				t.Errorf("scrub(%q, %q) = %q (changed %v), want it kept", key, p, out, changed)
			}
		}
	}
	// The set is for these two keys only.
	if out, _ := st.scrub("msg", pats[0]); out == pats[0] {
		t.Errorf("a registered pattern under another key is kept: %q", out)
	}
}

// TestRoutePatterns_NearMissWiped: a value that is not exactly a registered
// pattern is wiped, whatever it shares with one. The planted name sits where a
// path parameter would be, and the variants are the ones a prefix match, a case
// fold or a trim would let through.
func TestRoutePatterns_NearMissWiped(t *testing.T) {
	st := keysBuild()
	st.routes = newRouteSet([]string{"GET /api/v1/books/{id}", "/api/v1/files/{path...}", "/livez"})
	for _, in := range []string{
		"GET /api/v1/books/bruce",
		"GET /api/v1/books/{id}/bruce",
		"GET /api/v1/books/{id}/",
		"GET /api/v1/books/{id}x",
		"GET /api/v1/books/{id} bruce",
		"GET /api/v1/books/{id}\nx", // a name on a second line is the documented one-line-at-a-time limit, not tested here
		"GET /api/v1/books/{id}?p=/Users/bruce",
		"get /api/v1/books/{id}",
		"GET /API/v1/books/{id}",
		" GET /api/v1/books/{id}",
		"GET /api/v1/books/{id} ",
		"GET  /api/v1/books/{id}",
		"/api/v1/books/{id}",
		"POST /api/v1/books/{id}",
		"/api/v1/files/Users/bruce/Hush",
		"/api/v1/files/{path...}/bruce",
		"/api/v1/files/{path}",
		"/api/v1/files",
		"/livez/bruce",
		"/livez ",
		"/LIVEZ",
		"/api/v1/files/%2FUsers%2Fbruce",
		"/Users/bruce/Hush",
	} {
		for _, key := range []string{"route", "pattern"} {
			out, changed := st.scrub(key, in)
			if out == in || !changed {
				t.Errorf("scrub(%q, %q) = %q (changed %v), want it wiped", key, in, out, changed)
			}
			if n := routeLeaks(out); n != "" {
				t.Errorf("scrub(%q, %q) = %q holds %q", key, in, out, n)
			}
		}
	}
}

// TestRoutePatterns_LocationShapedEntriesCannotSmuggle: a list entry that
// spells a location is dropped when the set is built (it has no route shape), so
// the value equal to it is not kept: a producer cannot make a real path pass by
// getting it into the list. The one route in the list is kept beside them.
func TestRoutePatterns_LocationShapedEntriesCannotSmuggle(t *testing.T) {
	entries := []string{
		"/Users/bruce/Hush",
		"GET /Users/bruce/My Books/Hush",
		"smb://nas.example/home/bruce/Hush",
		"/api/../Users/bruce",
		"/api/v1/a b",
		`C:\Users\bruce\Hush`,
		"/api/%2FUsers%2Fbruce%2FHush",
		"~bruce/Hush",
		"&#47;Users&#47;bruce",
		"/api/v1/books/{id}?p=/Users/bruce",
		"/srv/lib/Hush",
		"/api/v1/x\nx",
		"GET  /api/v1/x",
		"/api/v1//bruce",
		"/api/" + strings.Repeat("a", 300),
		"",
	}
	const legit = "GET /api/v1/books/{id}"
	set := newRouteSet(append(append([]string{}, entries...), legit))
	if !set.has(legit) {
		t.Fatalf("the route in the list was dropped with the locations")
	}
	if len(set) != 1 {
		t.Errorf("the set holds %d entries (%v), want only %q", len(set), set, legit)
	}
	st := keysBuild()
	st.routes = set
	for _, e := range entries {
		if set.has(e) {
			t.Errorf("the location-shaped entry %q is in the set", e)
		}
		for _, key := range []string{"route", "pattern"} {
			out, _ := st.scrub(key, e)
			if n := routeLeaks(out); n != "" {
				t.Errorf("scrub(%q, %q) = %q holds %q", key, e, out, n)
			}
		}
	}
}

// TestRoutePatterns_RegisteredLiteralIsMembershipNotShape pins what the filter
// does not do: a route the router registered with literal segments that read
// like a path is a constant of the program and is kept, and a request path is
// kept only by being equal to one, never by looking like one.
func TestRoutePatterns_RegisteredLiteralIsMembershipNotShape(t *testing.T) {
	st := keysBuild()
	st.routes = newRouteSet([]string{"/api/v1/files/Users/bruce"})
	if out, _ := st.scrub("route", "/api/v1/files/Users/bruce"); out != "/api/v1/files/Users/bruce" {
		t.Errorf("a registered literal route was not kept: %q", out)
	}
	for _, in := range []string{"/api/v1/files/Users/bruce/Hush", "/api/v1/files/Users/selina", "/api/v1/files/Users/bruce/"} {
		out, _ := st.scrub("route", in)
		if n := routeLeaks(out); n != "" {
			t.Errorf("a request path that looks like the route is kept: %q -> %q (%s)", in, out, n)
		}
	}
}

// TestRoutePatterns_ListIsBounded: a list longer than maxRoutePatterns is not a
// route table and is ignored whole; one at the bound is used whole.
func TestRoutePatterns_ListIsBounded(t *testing.T) {
	mk := func(n int) []string {
		out := make([]string, n)
		for i := range out {
			out[i] = fmt.Sprintf("GET /api/v1/r%d/{id}", i)
		}
		return out
	}
	at := newRouteSet(mk(maxRoutePatterns))
	if len(at) != maxRoutePatterns || !at.has("GET /api/v1/r0/{id}") || !at.has(fmt.Sprintf("GET /api/v1/r%d/{id}", maxRoutePatterns-1)) {
		t.Errorf("a list of %d patterns kept %d", maxRoutePatterns, len(at))
	}
	over := newRouteSet(mk(maxRoutePatterns + 1))
	if len(over) != 0 || over.has("GET /api/v1/r0/{id}") {
		t.Errorf("a list of %d patterns kept %d", maxRoutePatterns+1, len(over))
	}
}
