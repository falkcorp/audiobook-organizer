// file: core/library/oracle_test.go
// version: 1.0.0
// guid: 60051e6c-dc64-449b-832c-f9c7503d514d
// last-edited: 2026-11-11

package library

import (
	"context"
	"errors"
	"testing"
)

func TestPathSetOracleAnswersBothDirections(t *testing.T) {
	seeded := []Path{
		{Root: "downloads", Rel: "Arkham/Joker.m4b"},
		{Root: "downloads", Rel: "Gotham Central"}, // a seeded directory
	}
	o := NewPathSetOracle(func(context.Context) ([]Path, error) { return seeded, nil })
	cases := []struct {
		name        string
		p           Path
		wantFile    bool // Protected
		wantSubtree bool // ProtectedUnder
	}{
		{"a listed file", Path{Root: "downloads", Rel: "Arkham/Joker.m4b"}, true, true},
		{"inside a listed directory", Path{Root: "downloads", Rel: "Gotham Central/Book 1/01.m4b"}, true, true},
		{"the listed directory itself", Path{Root: "downloads", Rel: "Gotham Central"}, true, true},
		{"a directory holding a listed file", Path{Root: "downloads", Rel: "Arkham"}, false, true},
		{"the root holds everything listed", Path{Root: "downloads"}, false, true},
		{"a sibling with a shared prefix", Path{Root: "downloads", Rel: "Arkham/Joker.m4b.bak"}, false, false},
		{"a sibling directory with a shared prefix", Path{Root: "downloads", Rel: "Gotham"}, false, false},
		{"another root", Path{Root: "books-library", Rel: "Arkham/Joker.m4b"}, false, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, loaded, err := o.Protected(context.Background(), c.p)
			if err != nil || !loaded || got != c.wantFile {
				t.Errorf("Protected = %v, %v, %v; want %v", got, loaded, err, c.wantFile)
			}
			got, loaded, err = o.ProtectedUnder(context.Background(), c.p)
			if err != nil || !loaded || got != c.wantSubtree {
				t.Errorf("ProtectedUnder = %v, %v, %v; want %v", got, loaded, err, c.wantSubtree)
			}
		})
	}
}

// A list that cannot be read is "unknown", which refuses the move; so is no list.
func TestPathSetOracleWithoutAListIsUnknown(t *testing.T) {
	boom := errors.New("the client is not answering")
	for name, o := range map[string]*PathSetOracle{
		"an error":     NewPathSetOracle(func(context.Context) ([]Path, error) { return nil, boom }),
		"no function":  NewPathSetOracle(nil),
		"a nil oracle": nil,
	} {
		t.Run(name, func(t *testing.T) {
			p := Path{Root: "downloads", Rel: "x"}
			if protected, loaded, err := o.Protected(context.Background(), p); loaded || err == nil || protected {
				t.Errorf("Protected = %v, %v, %v; want unknown with an error", protected, loaded, err)
			}
			if protected, loaded, err := o.ProtectedUnder(context.Background(), p); loaded || err == nil || protected {
				t.Errorf("ProtectedUnder = %v, %v, %v; want unknown with an error", protected, loaded, err)
			}
		})
	}
}

// Registered with a manager, a seeding oracle that is a PathSetOracle lets a
// directory Move through when nothing seeded lies in it, and refuses when it does.
func TestPathSetOracleDecidesADirectoryMove(t *testing.T) {
	seeded := []Path{{Root: "import-1", Rel: "Arkham/Cells/joker.m4b"}}
	o := NewPathSetOracle(func(context.Context) ([]Path, error) { return seeded, nil })
	r, _ := transferRig(t, rigOpts{oracles: []ProtectionOracle{o}})
	r.put("import-1", "Arkham/Cells/joker.m4b", gas)
	r.put("import-1", "Wing/two-face.m4b", "heads")
	dst := Path{Root: "books-library", Rel: "Wing"}
	if _, err := r.m.Transfer(ctxbg(), books, Path{Root: "import-1", Rel: "Wing"}, dst, withMkdir(move())); err != nil {
		t.Errorf("a directory with nothing seeded in it: %v", err)
	}
	_, err := r.m.Transfer(ctxbg(), books, Path{Root: "import-1", Rel: "Arkham"}, Path{Root: "books-library", Rel: "Arkham"}, withMkdir(move()))
	if !errors.Is(err, ErrSourceProtected) {
		t.Errorf("a directory holding a seeded file = %v, want ErrSourceProtected", err)
	}
	if !r.exists("import-1", "Arkham/Cells/joker.m4b") {
		t.Error("the seeded file moved")
	}
}
