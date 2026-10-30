// file: internal/server/absauth/hash_cost_guard_test.go
// version: 1.0.0
// guid: 6e0b7d24-91c3-4f8a-b5d6-3a28c4f1e907
// last-edited: 2026-10-29

package absauth

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// SetHashCostForTest makes every password HashPassword writes cheap to guess.
// It exists for test binaries' TestMain; a call from production code would ship
// that. TestMain lives in a _test.go file, so no non-test file may name it.
func TestSetHashCostForTestIsNotCalledByProductionCode(t *testing.T) {
	// os.Getwd, not runtime.Caller: CI builds with -trimpath.
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := dir
	for {
		if _, err := os.Stat(filepath.Join(root, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(root)
		if parent == root {
			t.Fatalf("go.mod not found above %s", dir)
		}
		root = parent
	}
	defining := filepath.Join(root, "internal", "server", "absauth", "password.go")
	scanned := 0
	for _, top := range []string{"internal", "cmd", "pkg", "tools"} {
		err := filepath.WalkDir(filepath.Join(root, top), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				name := d.Name()
				if path != filepath.Join(root, top) &&
					(name == "testdata" || strings.HasPrefix(name, ".") || strings.HasPrefix(name, "_")) {
					return fs.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") || path == defining {
				return nil
			}
			scanned++
			src, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if strings.Contains(string(src), "SetHashCostForTest") {
				rel, _ := filepath.Rel(root, path)
				t.Errorf("%s names SetHashCostForTest; production code must not lower the password hash cost", rel)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned < 500 {
		t.Fatalf("scanned only %d files; the walk is broken, not the code", scanned)
	}
}

func TestSetHashCostForTestChangesWhatIsWrittenAndStillVerifies(t *testing.T) {
	oldT, oldM := hashTime, hashMemory
	t.Cleanup(func() { hashTime, hashMemory = oldT, oldM })

	SetHashCostForTest(1, 64)
	algo, hash, err := HashPassword("correct horse")
	if err != nil {
		t.Fatal(err)
	}
	if algo != AlgoArgon2id || !strings.Contains(hash, "m=64,t=1,p=2$") {
		t.Fatalf("hash %q not written at the lowered cost", hash)
	}
	if ok, _ := VerifyPassword(algo, hash, "correct horse"); !ok {
		t.Fatal("a hash written at the lowered cost must verify")
	}
	if ok, _ := VerifyPassword(algo, hash, "wrong horse"); ok {
		t.Fatal("a wrong password must still fail at the lowered cost")
	}
}
