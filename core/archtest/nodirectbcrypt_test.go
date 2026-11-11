// file: core/archtest/nodirectbcrypt_test.go
// version: 1.0.0
// guid: a4ffff66-cc75-4a2c-a845-1a8d30294b2c
// last-edited: 2026-11-10

package archtest

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestBcryptMisuseDetection(t *testing.T) {
	cases := []struct {
		name string
		src  string
		want []string
	}{
		{"plain verify", `package p
import "golang.org/x/crypto/bcrypt"
func f() { _ = bcrypt.CompareHashAndPassword(nil, nil) }`, []string{"3: bcrypt.CompareHashAndPassword"}},
		{"plain hash", `package p
import "golang.org/x/crypto/bcrypt"
func f() { bcrypt.GenerateFromPassword(nil, bcrypt.DefaultCost) }`, []string{"3: bcrypt.GenerateFromPassword"}},
		{"alias", `package p
import bc "golang.org/x/crypto/bcrypt"
func f() { _ = bc.CompareHashAndPassword(nil, nil) }`, []string{"3: bcrypt.CompareHashAndPassword"}},
		{"dot import", `package p
import . "golang.org/x/crypto/bcrypt"
func f() { _ = CompareHashAndPassword(nil, nil) }`, []string{"3: bcrypt.CompareHashAndPassword"}},
		{"unrelated local named bcrypt", `package p
type h struct{}
func (h) CompareHashAndPassword() {}
func f() { bcrypt := h{}; bcrypt.CompareHashAndPassword() }`, nil},
		{"alias of another package named bcrypt", `package p
import bcrypt "example.com/other/bcrypt"
func f() { _ = bcrypt.CompareHashAndPassword(nil, nil) }`, nil},
		{"bcrypt imported but only the cost constant used", `package p
import "golang.org/x/crypto/bcrypt"
var c = bcrypt.DefaultCost`, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := bcryptMisuse("x.go", tc.src)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("hits = %v, want %v", got, tc.want)
			}
		})
	}
}

// bcryptRecorder is a testing.TB that records failures instead of stopping.
type bcryptRecorder struct {
	testing.TB
	errs []string
}

func (r *bcryptRecorder) Helper() {}
func (r *bcryptRecorder) Errorf(f string, a ...any) {
	r.errs = append(r.errs, strings.TrimSpace(fmt.Sprintf(f, a...)))
}
func (r *bcryptRecorder) Fatalf(f string, a ...any) {
	r.errs = append(r.errs, "FATAL "+strings.TrimSpace(fmt.Sprintf(f, a...)))
	r.TB.FailNow()
}

// NoDirectBcrypt over a scratch module: the allowed file may call bcrypt, any
// other non-test file may not (one behind a build tag included), a test file
// may, a nested module is left to its own check, and an allowed entry that
// names no file is reported as stale.
func TestNoDirectBcrypt_AllowListAndStaleEntries(t *testing.T) {
	dir := t.TempDir()
	write := func(rel, src string) {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	call := "package %s\nimport \"golang.org/x/crypto/bcrypt\"\nfunc F() { _ = bcrypt.CompareHashAndPassword(nil, nil) }\n"
	write("cave/cave.go", fmt.Sprintf(call, "cave"))
	write("cave/cave_test.go", fmt.Sprintf(call, "cave"))
	write("arkham/arkham.go", fmt.Sprintf(call, "arkham"))
	write("arkham/tagged.go", "//go:build joker\n\n"+fmt.Sprintf(call, "arkham"))
	write("blackgate/go.mod", "module example.com/blackgate\n\ngo 1.24\n")
	write("blackgate/b.go", fmt.Sprintf(call, "blackgate"))
	write("go.mod", "module example.com/gotham\n\ngo 1.24\n")
	rec := &bcryptRecorder{TB: t}
	NoDirectBcrypt(rec, dir, []string{"cave/cave.go", "wayne/manor.go"})
	got := strings.Join(rec.errs, "\n")
	for _, want := range []string{"arkham/arkham.go:3: bcrypt.CompareHashAndPassword", "arkham/tagged.go:5: bcrypt.CompareHashAndPassword", "allowed file wayne/manor.go is not a scanned non-test file"} {
		if !strings.Contains(got, want) {
			t.Errorf("findings lack %q:\n%s", want, got)
		}
	}
	for _, bad := range []string{"cave/cave.go:", "cave_test.go", "blackgate"} {
		if strings.Contains(got, bad) {
			t.Errorf("findings name %q, which is allowed or a test file:\n%s", bad, got)
		}
	}
}
