// file: core/library/mapper_test.go
// version: 1.0.0
// guid: c5080780-ac27-4acb-836b-60caa5b18964
// last-edited: 2026-11-11

package library

import (
	"errors"
	"testing"
)

const mapSrc = "download:gotham-1"

func mapOf(ms ...Mapping) *pathMapper { return newPathMapper(ms, nil) }

func TestPathMapperLongestPrefixBoundary(t *testing.T) {
	dl := Path{Root: "dl", Rel: ""}
	m := mapOf(
		Mapping{Source: mapSrc, Remote: "/dl", Local: dl},
		Mapping{Source: mapSrc, Remote: "/dl/complete", Local: Path{Root: "books", Rel: "incoming"}},
		Mapping{Source: mapSrc, Remote: `C:\Downloads\complete`, Local: Path{Root: "win", Rel: "c"}},
		Mapping{Source: mapSrc, Remote: `\\arkham\share`, Local: Path{Root: "unc", Rel: ""}},
		Mapping{Source: mapSrc, Remote: "file:///D:/Wayne%20Audio", Local: Path{Root: "wayne", Rel: ""}},
		Mapping{Source: mapSrc, Remote: `c:\downloads\fold`, Local: Path{Root: "fold", Rel: "f"}, Fold: true},
		Mapping{Source: mapSrc, Remote: `E:\Cave`, Local: Path{Root: "cave", Rel: ""}},
		Mapping{Source: "other", Remote: "/dl", Local: Path{Root: "wrong", Rel: ""}},
	)
	tests := []struct {
		name, remote string
		want         Path
		unmapped     bool
	}{
		{"shorter prefix is not a boundary match", "/dl-old/x.m4b", Path{}, true},
		{"shorter mapping takes non-nested", "/dl/other/Joker.m4b", Path{Root: "dl", Rel: "other/Joker.m4b"}, false},
		{"longest nested wins", "/dl/complete/Gotham Nights/part1.m4b", Path{Root: "books", Rel: "incoming/Gotham Nights/part1.m4b"}, false},
		{"complete-old is not under complete", "/dl/complete-old/x", Path{Root: "dl", Rel: "complete-old/x"}, false},
		{"windows backslashes", `C:\Downloads\complete\Gotham Nights\part1.m4b`, Path{Root: "win", Rel: "c/Gotham Nights/part1.m4b"}, false},
		{"windows forward slashes", "C:/Downloads/complete/Joker.m4b", Path{Root: "win", Rel: "c/Joker.m4b"}, false},
		{"UNC", `\\arkham\share\Scarecrow\fear.m4b`, Path{Root: "unc", Rel: "Scarecrow/fear.m4b"}, false},
		{"file URL percent space", "file:///D:/Wayne%20Audio/Joker%20Laughs.m4b", Path{Root: "wayne", Rel: "Joker Laughs.m4b"}, false},
		{"file URL case-insensitive scheme", "FILE://localhost/D:/Wayne%20Audio/x.m4b", Path{Root: "wayne", Rel: "x.m4b"}, false},
		{"fold matches other case", `C:\Downloads\Fold\Robin.m4b`, Path{Root: "fold", Rel: "f/Robin.m4b"}, false},
		{"fold keeps remainder case", `C:\DOWNLOADS\FOLD\ROBIN.m4b`, Path{Root: "fold", Rel: "f/ROBIN.m4b"}, false},
		{"non-fold does not fold (drive)", `e:\Cave\Bat.m4b`, Path{}, true},
		{"non-fold does not fold (dir)", `E:\cave\Bat.m4b`, Path{}, true},
		{"exact prefix gives Local", "/dl/complete", Path{Root: "books", Rel: "incoming"}, false},
		{"exact prefix with trailing slash", "/dl/complete/", Path{Root: "books", Rel: "incoming"}, false},
		{"exact windows prefix", `C:\Downloads\complete`, Path{Root: "win", Rel: "c"}, false},
		{"root mapping exact", "/dl", Path{Root: "dl"}, false},
		{"posix backslash in name untouched", `/dl/Bat\Man.m4b`, Path{Root: "dl", Rel: `Bat\Man.m4b`}, false},
		{"other source not consulted", "/nowhere", Path{}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got, err := m.ToLocal(mapSrc, tc.remote)
			if tc.unmapped {
				if !errors.Is(err, ErrUnmapped) {
					t.Fatalf("ToLocal(%q) = %v, %v; want ErrUnmapped", tc.remote, got, err)
				}
				if got != (Path{}) {
					t.Fatalf("returned a best-effort path %v with an error", got)
				}
				return
			}
			if err != nil || got != tc.want {
				t.Fatalf("ToLocal(%q) = %v, %v; want %v", tc.remote, got, err, tc.want)
			}
		})
	}
}

func TestPathMapperFirstListedWinsTies(t *testing.T) {
	m := mapOf(
		Mapping{Source: mapSrc, Remote: "/dl", Local: Path{Root: "first"}},
		Mapping{Source: mapSrc, Remote: "/dl/", Local: Path{Root: "second"}},
	)
	got, err := m.ToLocal(mapSrc, "/dl/x")
	if err != nil || got != (Path{Root: "first", Rel: "x"}) {
		t.Fatalf("got %v, %v", got, err)
	}
}

func TestPathMapperNoUnicodeFolding(t *testing.T) {
	const nfc = "Caf\u00e9"
	const nfd = "Cafe\u0301"
	for _, fold := range []bool{false, true} {
		m := mapOf(
			Mapping{Source: mapSrc, Remote: "/dl/" + nfc, Local: Path{Root: "p"}, Fold: fold},
			Mapping{Source: mapSrc, Remote: `C:\` + nfc, Local: Path{Root: "w"}, Fold: fold},
		)
		if _, err := m.ToLocal(mapSrc, "/dl/"+nfd+"/x"); !errors.Is(err, ErrUnmapped) {
			t.Errorf("fold=%v: NFD posix remote matched an NFC prefix: %v", fold, err)
		}
		if _, err := m.ToLocal(mapSrc, `C:\`+nfd+`\x`); !errors.Is(err, ErrUnmapped) {
			t.Errorf("fold=%v: NFD windows remote matched an NFC prefix: %v", fold, err)
		}
		got, err := m.ToLocal(mapSrc, "/dl/"+nfc+"/"+nfd+".m4b")
		if err != nil || got != (Path{Root: "p", Rel: nfd + ".m4b"}) {
			t.Errorf("fold=%v: remainder bytes changed: %q, %v", fold, got.Rel, err)
		}
	}
	// Fold is ASCII only: U+00C9 does not fold to U+00E9.
	m := mapOf(Mapping{Source: mapSrc, Remote: `C:\` + "caf\u00e9", Local: Path{Root: "w"}, Fold: true})
	if _, err := m.ToLocal(mapSrc, `c:\`+"CAF\u00c9"+`\x`); !errors.Is(err, ErrUnmapped) {
		t.Errorf("non-ASCII letters were case folded: %v", err)
	}
	if got, err := m.ToLocal(mapSrc, `c:\`+"CAf\u00e9"+`\x`); err != nil || got != (Path{Root: "w", Rel: "x"}) {
		t.Errorf("ASCII fold failed: %v, %v", got, err)
	}
	// Fold never applies to a POSIX remote.
	pm := mapOf(Mapping{Source: mapSrc, Remote: "/dl", Local: Path{Root: "p"}, Fold: true})
	if _, err := pm.ToLocal(mapSrc, "/DL/x"); !errors.Is(err, ErrUnmapped) {
		t.Errorf("posix remote was folded: %v", err)
	}
}

func TestPathMapperToRemoteRoundTrip(t *testing.T) {
	m := mapOf(
		Mapping{Source: mapSrc, Remote: "/dl/complete", Local: Path{Root: "books", Rel: "incoming"}},
		Mapping{Source: mapSrc, Remote: `D:\Wayne\Audio`, Local: Path{Root: "wayne", Rel: "audio"}},
		Mapping{Source: mapSrc, Remote: "C:/Cave", Local: Path{Root: "cave"}},
		Mapping{Source: mapSrc, Remote: `\\arkham\share`, Local: Path{Root: "unc"}},
		Mapping{Source: mapSrc, Remote: "file:///D:/Wayne%20Audio", Local: Path{Root: "furl"}},
		Mapping{Source: mapSrc, Remote: "file:///dl/Batcave", Local: Path{Root: "furlposix", Rel: "bc"}},
	)
	for _, remote := range []string{
		"/dl/complete/Gotham Nights/part1.m4b",
		"/dl/complete",
		`D:\Wayne\Audio\Joker.m4b`,
		`D:\Wayne\Audio\Sub Dir\Joker.m4b`,
		"C:/Cave/Bat Suit/x.m4b",
		`\\arkham\share\Scarecrow\fear.m4b`,
		"file:///D:/Wayne%20Audio/Joker%20Laughs.m4b",
		"file:///D:/Wayne%20Audio/100%25%20Bat.m4b",
		"file:///dl/Batcave/Robin%20%231.m4b",
	} {
		p, err := m.ToLocal(mapSrc, remote)
		if err != nil {
			t.Fatalf("ToLocal(%q): %v", remote, err)
		}
		back, err := m.ToRemote(mapSrc, p)
		if err != nil || back != remote {
			t.Errorf("ToRemote(%v) = %q, %v; want %q", p, back, err, remote)
		}
	}
	// A Remote with a trailing separator does not double it.
	tm := mapOf(
		Mapping{Source: mapSrc, Remote: "/dl/", Local: Path{Root: "a"}},
		Mapping{Source: mapSrc, Remote: `C:\Cave\`, Local: Path{Root: "b"}},
	)
	if got, _ := tm.ToRemote(mapSrc, Path{Root: "a", Rel: "x/y"}); got != "/dl/x/y" {
		t.Errorf("posix trailing slash: %q", got)
	}
	if got, _ := tm.ToRemote(mapSrc, Path{Root: "b", Rel: "x"}); got != `C:\Cave\x` {
		t.Errorf("windows trailing separator: %q", got)
	}
	// A root mapping at "/" yields an absolute path.
	rm := mapOf(Mapping{Source: mapSrc, Remote: "/", Local: Path{Root: "a"}})
	if got, _ := rm.ToRemote(mapSrc, Path{Root: "a", Rel: "x"}); got != "/x" {
		t.Errorf("root remote: %q", got)
	}
	// Longest Local.Rel wins; sibling prefix is not a boundary match.
	lm := mapOf(
		Mapping{Source: mapSrc, Remote: "/all", Local: Path{Root: "r"}},
		Mapping{Source: mapSrc, Remote: "/deep", Local: Path{Root: "r", Rel: "a/b"}},
	)
	if got, _ := lm.ToRemote(mapSrc, Path{Root: "r", Rel: "a/b/c"}); got != "/deep/c" {
		t.Errorf("longest local: %q", got)
	}
	if got, _ := lm.ToRemote(mapSrc, Path{Root: "r", Rel: "a/bc"}); got != "/all/a/bc" {
		t.Errorf("boundary local: %q", got)
	}
	for _, p := range []Path{{Root: "nope", Rel: "x"}, {Root: "r", Rel: "../x"}} {
		if got, err := lm.ToRemote(mapSrc, p); err == nil || got != "" {
			t.Errorf("ToRemote(%v) = %q, %v; want error", p, got, err)
		}
	}
	if _, err := lm.ToRemote("other", Path{Root: "r", Rel: "x"}); !errors.Is(err, ErrUnmapped) {
		t.Errorf("other source: %v", err)
	}
}

func TestPathMapperRejectsEscape(t *testing.T) {
	m := mapOf(
		Mapping{Source: mapSrc, Remote: "/dl", Local: Path{Root: "p"}},
		Mapping{Source: mapSrc, Remote: "file:///D:/Wayne", Local: Path{Root: "w"}},
		Mapping{Source: mapSrc, Remote: `C:\Cave`, Local: Path{Root: "c"}},
	)
	for _, remote := range []string{
		"/dl/../etc/passwd",
		"/dl/a/../../x",
		"/dl/./x",
		"/dl//x",
		"/dl/a\x00b",
		`C:\Cave\..\x`,
		`C:\Cave\a\\b`,
		"file:///D:/Wayne/%2e%2e/x",
		"file:///D:/Wayne/%2E%2E",
		"file:///D:/Wayne/a%2Fb",
		"file:///D:/Wayne/a%5Cb",
		"file:///D:/Wayne/%zz",
		"file:///D:/Wayne/a%00b",
		"file:///D:/Wayne/%",
		"file://",
	} {
		got, err := m.ToLocal(mapSrc, remote)
		if err == nil || got != (Path{}) {
			t.Errorf("ToLocal(%q) = %v, %v; want an error and no path", remote, got, err)
		}
	}
	if _, err := m.ToLocal(mapSrc, "/dl/../x"); !errors.Is(err, ErrBadRel) {
		t.Errorf("dot-dot error should wrap ErrBadRel: %v", err)
	}
	if _, err := m.ToLocal(mapSrc, "file:///D:/Wayne/%zz"); !errors.Is(err, ErrBadRel) {
		t.Errorf("bad escape should wrap ErrBadRel: %v", err)
	}
	// An encoded %2F is refused, not kept escaped: its legal spelling %252F decodes to a literal %2F.
	got, err := m.ToLocal(mapSrc, "file:///D:/Wayne/a%252Fb")
	if err != nil || got != (Path{Root: "w", Rel: "a%2Fb"}) {
		t.Errorf("double-encoded: %v, %v", got, err)
	}
	// Odd inputs never panic and never return a path.
	for _, s := range []string{"", "C:", "C", ":", "/", `\\`, "file:", "file:/", "file:///", "FILE://x", "%zz", "..", "."} {
		if got, err := m.ToLocal(mapSrc, s); err == nil && got.Root != "p" && got.Root != "w" && got.Root != "c" {
			t.Errorf("ToLocal(%q) = %v, nil", s, got)
		}
	}
	// A mapping whose own Remote is unusable never matches.
	bad := mapOf(
		Mapping{Source: mapSrc, Remote: "relative/dir", Local: Path{Root: "x"}},
		Mapping{Source: mapSrc, Remote: "file:///a%zz", Local: Path{Root: "x"}},
		Mapping{Source: mapSrc, Remote: "", Local: Path{Root: "x"}},
	)
	if _, err := bad.ToLocal(mapSrc, "relative/dir/f"); !errors.Is(err, ErrUnmapped) {
		t.Errorf("relative mapping matched: %v", err)
	}
	// A mapping with an invalid Local fails closed.
	inv := mapOf(Mapping{Source: mapSrc, Remote: "/dl", Local: Path{Root: "p", Rel: "../x"}})
	if got, err := inv.ToLocal(mapSrc, "/dl/a"); err == nil {
		t.Errorf("invalid Local accepted: %v", got)
	}
}

func TestPathMapperAbsoluteUnderRootFallback(t *testing.T) {
	var asked []string
	resolve := func(abs string) (Path, error) {
		asked = append(asked, abs)
		if abs == "/library/Gotham Nights/part1.m4b" {
			return Path{Root: "books", Rel: "Gotham Nights/part1.m4b"}, nil
		}
		return Path{}, ErrNotUnderRoot
	}
	m := newPathMapper([]Mapping{{Source: mapSrc, Remote: "/dl", Local: Path{Root: "dl"}}}, resolve)
	got, err := m.ToLocal(mapSrc, "/library/Gotham Nights/part1.m4b")
	if err != nil || got != (Path{Root: "books", Rel: "Gotham Nights/part1.m4b"}) {
		t.Fatalf("fallback = %v, %v", got, err)
	}
	// Unknown source falls back too.
	if got, err := m.ToLocal("worker:fingerprint", "/library/Gotham Nights/part1.m4b"); err != nil || got.Root != "books" {
		t.Fatalf("unknown source fallback = %v, %v", got, err)
	}
	// resolve error: ErrUnmapped wrapping the cause.
	_, err = m.ToLocal(mapSrc, "/elsewhere/x.m4b")
	if !errors.Is(err, ErrUnmapped) || !errors.Is(err, ErrNotUnderRoot) {
		t.Fatalf("resolve error = %v; want ErrUnmapped wrapping ErrNotUnderRoot", err)
	}
	// Never asked for anything that is not an absolute host path.
	asked = nil
	for _, r := range []string{"relative/x.m4b", "", `C:\Cave\x.m4b`, "file:///library/Gotham%20Nights/part1.m4b", `\\arkham\share\x`} {
		if got, err := m.ToLocal(mapSrc, r); !errors.Is(err, ErrUnmapped) || got != (Path{}) {
			t.Errorf("ToLocal(%q) = %v, %v; want ErrUnmapped", r, got, err)
		}
	}
	if len(asked) != 0 {
		t.Errorf("resolve was called for non-absolute remotes: %v", asked)
	}
	// A resolve that returns an invalid Path is an error.
	bad := newPathMapper(nil, func(string) (Path, error) { return Path{Root: "r", Rel: "../x"}, nil })
	if got, err := bad.ToLocal(mapSrc, "/x"); err == nil {
		t.Errorf("invalid resolved path accepted: %v", got)
	}
	// nil resolve: ErrUnmapped.
	if _, err := newPathMapper(nil, nil).ToLocal(mapSrc, "/x"); !errors.Is(err, ErrUnmapped) {
		t.Errorf("nil resolve: %v", err)
	}
}

func TestPathMapperMappingsCopy(t *testing.T) {
	in := []Mapping{
		{Source: mapSrc, Remote: "/b", Local: Path{Root: "r", Rel: "b"}},
		{Source: "itunes", Remote: "/i", Local: Path{Root: "r", Rel: "i"}},
		{Source: mapSrc, Remote: "/a", Local: Path{Root: "r", Rel: "a"}, Fold: true},
	}
	m := newPathMapper(in, nil)
	got := m.Mappings(mapSrc)
	want := []Mapping{in[0], in[2]}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("Mappings = %+v; want %+v", got, want)
	}
	got[0].Remote = "/hacked"
	got[1] = Mapping{}
	in[0].Remote = "/mutated-input"
	again := m.Mappings(mapSrc)
	if again[0] != want[0] || again[1] != want[1] {
		t.Fatalf("mapper changed through a returned slice or the input: %+v", again)
	}
	if got, err := m.ToLocal(mapSrc, "/b/x"); err != nil || got != (Path{Root: "r", Rel: "b/x"}) {
		t.Fatalf("ToLocal after mutation = %v, %v", got, err)
	}
	if m.Mappings("nobody") != nil {
		t.Fatal("unknown source should have no mappings")
	}
}
