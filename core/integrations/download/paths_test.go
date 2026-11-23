// file: core/integrations/download/paths_test.go
// version: 2.0.0
// guid: daba7df7-02a3-4d43-ae3b-cd72d7252769
// last-edited: 2026-11-22

package download_test

import (
	"context"
	"strings"
	"testing"

	"github.com/falkcorp/mediacore/integrations"
	"github.com/falkcorp/mediacore/integrations/download"
	"github.com/falkcorp/mediacore/library"
)

// The matching rules themselves (longest prefix on a separator boundary, Fold,
// UNC, file URLs, traversal) are core/library's and are tested there
// (library.TestPathMapper*); these tests pin that the Pool applies them to a
// download instance's own rows and to nothing else.

func TestPaths_Source(t *testing.T) {
	if got := download.Source("seedbox"); got != "download:seedbox" {
		t.Errorf("Source = %q, want download:seedbox", got)
	}
}

// A client on Windows reports drive paths; with Fold on the row they map into
// a library root, and a Windows path no row covers is left unmapped.
func TestPaths_WindowsRemote(t *testing.T) {
	h := newHarness(t)
	f := h.addFake(seedbox, crateAdapter, 0)
	f.Add(item("aa", "Year One", `D:\dl\Batman`, "completed", 1))
	f.Add(item("bb", "Hush", `d:\DL\Batman\Hush`, "completed", 1))
	f.Add(item("cc", "Owls", `D:\dl-old\Owls`, "completed", 1))
	f.Add(item("dd", "Joker", `E:\dl\Joker`, "completed", 1))
	f.Add(item("ee", "Robin", `D:\dl\..\secret`, "completed", 1))
	exact := mapper(map[integrations.InstanceID][]library.Mapping{"seedbox": {lmap(`D:\dl`, "library", "incoming")}})
	fold := mapper(map[integrations.InstanceID][]library.Mapping{"seedbox": {func() library.Mapping {
		m := lmap(`D:\dl`, "library", "incoming")
		m.Fold = true
		return m
	}()}})
	for name, tc := range map[string]struct {
		paths library.PathMapper
		want  map[download.ItemID]library.Path
	}{
		"exact case": {exact, map[download.ItemID]library.Path{
			"aa": lpath("library", "incoming/Batman"), "bb": {}, "cc": {}, "dd": {}, "ee": {},
		}},
		"fold": {fold, map[download.ItemID]library.Path{
			"aa": lpath("library", "incoming/Batman"), "bb": lpath("library", "incoming/Batman/Hush"), "cc": {}, "dd": {}, "ee": {},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			items, err := h.newPool(download.PoolOptions{Paths: tc.paths}).Items(context.Background(), seedbox, download.ItemFilter{})
			if err != nil {
				t.Fatal(err)
			}
			for _, it := range items {
				if it.LocalPath != tc.want[it.Ref.ID] {
					t.Errorf("%s (%s): LocalPath = %v, want %v", it.Ref.ID, it.RemotePath, it.LocalPath, tc.want[it.Ref.ID])
				}
			}
		})
	}
}

// Relocate speaks the client's own syntax: a library path under a Windows row
// goes out as a drive path, and one whose name would add a separator or a
// drive switch there is refused before anything is sent.
func TestPaths_RelocateWindowsRemote(t *testing.T) {
	h := newHarness(t)
	f := h.addFake(seedbox, crateAdapter, 0)
	f.Add(item("aa", "Year One", `D:\dl`, "completed", 1))
	p := h.newPool(download.PoolOptions{Paths: mapper(map[integrations.InstanceID][]library.Mapping{"seedbox": {lmap(`D:\library`, "library", "")}})})
	refs := []download.ItemRef{{Instance: seedbox, ID: "aa"}}
	ctx := context.Background()

	if err := p.Relocate(ctx, refs, lpath("library", "Batman/Hush")); err != nil {
		t.Fatal(err)
	}
	if mv := f.Scenario().Moves(); len(mv) != 1 || mv[0].Dir != `D:\library\Batman\Hush` {
		t.Fatalf("moves = %+v, want one to D:\\library\\Batman\\Hush", mv)
	}
	for name, rel := range map[string]string{
		"a backslash traversal in one name": `Batman\..\..\secret`,
		"a drive switch":                    "E:",
		"a colon in a name":                 "Hush:Year One",
	} {
		err := p.Relocate(ctx, refs, lpath("library", rel))
		if classOf(err) != integrations.ClassInvalid || !strings.Contains(err.Error(), "no mapping covers") {
			t.Errorf("%s: err = %v, want ClassInvalid \"no mapping covers\"", name, err)
		}
	}
	if got := len(f.Scenario().Moves()); got != 1 {
		t.Errorf("a refused Relocate sent something: %d moves", got)
	}
}
