// file: core/archtest/batch_by_value_test.go
// version: 1.0.0
// guid: 490949dd-e935-4360-ba8e-44de5104024b
// last-edited: 2026-11-07

package archtest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoBatchByValueFindings: every by-value position is reported, and the
// pointer forms (*Batch, &Batch{}, new(Batch)) and selections of a field or
// method named Batch are not.
func TestNoBatchByValueFindings(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	user := write("user.go", `package gordon

import st "github.com/falkcorp/mediacore/storage"

type holder struct {
	ok  *st.Batch
	bad st.Batch
}

func good(b *st.Batch) *st.Batch { return b }
func badParam(b st.Batch)         {}
func badResult() (b st.Batch)     { return }
func badSlice(b []st.Batch)       {}
func badMap(m map[string]st.Batch) {}
func selection(h holder) { _ = h.ok; var x struct{ Batch int }; _ = x.Batch }
`)
	inside := write("storage.go", `package storage

type Batch struct{ n int }

func newBatch() *Batch { return &Batch{n: 1} }
func alloc() *Batch    { return new(Batch) }
func copyOf(b Batch)    {}
`)
	other := write("other.go", `package alfred

func Batch() {}
`)
	got, err := NoBatchByValueFindings([]string{user, inside, other})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]int{"user.go": 5, "storage.go": 1}
	count := map[string]int{}
	for _, f := range got {
		count[filepath.Base(strings.SplitN(f, ":", 2)[0])]++
	}
	for file, n := range want {
		if count[file] != n {
			t.Errorf("%s: %d findings, want %d\n%s", file, count[file], n, strings.Join(got, "\n"))
		}
	}
	if count["other.go"] != 0 {
		t.Errorf("a package that cannot name storage.Batch was reported: %v", got)
	}
}
