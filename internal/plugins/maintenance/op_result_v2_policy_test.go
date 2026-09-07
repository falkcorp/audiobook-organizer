// file: internal/plugins/maintenance/op_result_v2_policy_test.go
// version: 1.0.0
// guid: 6b3f1e07-9c42-4a58-b0d1-72e845f3c916
// last-edited: 2026-09-07

package maintenance

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestNoV1OperationResultWrites pins the migration this package just made.
//
// UpdateOperationResultData resolves a v1 `operation:` row and returns
// "operation not found" when there is none. There is never one for a live op:
// the run context carries the v2 id (registry_wire.go installs
// opRunContextDecorator on the v2 registry) and nothing has minted a v1 row
// since the v1 minter was retired. So every call from this package was a
// guaranteed failure — reconcile-scan and ai-dedup-batch RETURNED it and failed
// the op after all their work was done, and clear-apply-rename-failures merely
// logged it, which meant the result data it promises was silently never stored.
//
// A compiler cannot catch a reintroduction: the method still exists on the store
// for the v1 history readers, and calling it type-checks fine. The failure is
// only visible at runtime, in prod, at the end of a long op. Hence a source
// check rather than a behavioural one — it is the only thing that fails fast.
//
// The replacement is registry.ReporterSetResult, which writes the run's own v2
// row and reports its own failures loudly.
func TestNoV1OperationResultWrites(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	var offenders []string
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		// Skip this file: it names the forbidden symbol in prose and would
		// otherwise flag itself.
		if name == "op_result_v2_policy_test.go" {
			continue
		}
		src, err := os.ReadFile(filepath.Clean(name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		scanned++
		for i, line := range strings.Split(string(src), "\n") {
			code, _, _ := strings.Cut(line, "//")
			if strings.Contains(code, "UpdateOperationResultData(") {
				offenders = append(offenders,
					fmt.Sprintf("%s:%d: %s", name, i+1, strings.TrimSpace(line)))
			}
		}
	}

	if scanned == 0 {
		t.Fatal("scanned no .go files — the check would pass vacuously")
	}
	if len(offenders) > 0 {
		t.Errorf("maintenance ops must persist results with registry.ReporterSetResult "+
			"(writes the v2 row), not store.UpdateOperationResultData (resolves a v1 row "+
			"that no live op has, so it always fails). Found %d call(s):\n  %s",
			len(offenders), strings.Join(offenders, "\n  "))
	}
}
