// file: internal/plugins/maintenance/combine_journal_check_test.go
// version: 1.0.0
// guid: 8c3f5a17-d2e4-4b96-a1c8-0e7b9d4f2a53
// last-edited: 2026-10-26

package maintenance

import (
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
	"github.com/falkcorp/audiobook-organizer/internal/merge"
	"github.com/falkcorp/audiobook-organizer/pkg/plugin/sdk"
)

type combineKVFake struct{ m map[string][]byte }

func (k *combineKVFake) GetRaw(key string) ([]byte, error) { return k.m[key], nil }
func (k *combineKVFake) ScanPrefix(prefix string) ([]database.KVPair, error) {
	var out []database.KVPair
	for key, v := range k.m {
		if strings.HasPrefix(key, prefix) {
			out = append(out, database.KVPair{Key: key, Value: v})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out, nil
}

// The check is report-only and read-only: it names the op the purge's refusal
// points at, and requests no write capability.
func TestCombineJournalCheck_DefIsReadOnlyAndNamedByTheRefusal(t *testing.T) {
	def := (&Plugin{}).combineJournalCheckDef()
	if def.ID != merge.CombineJournalCheckOp {
		t.Fatalf("op id %q is not the one the purge refusal names (%q)", def.ID, merge.CombineJournalCheckOp)
	}
	for _, c := range def.Capabilities {
		if c == sdk.CapLibraryWrite {
			t.Fatal("the check must not request a write capability")
		}
	}
}

func TestCombineJournalCheck_ListsEveryUnreadableRowByKey(t *testing.T) {
	kv := &combineKVFake{m: map[string][]byte{
		"merge:combine-journal:01GOOD":  []byte(`{"id":"01GOOD","survivor_id":"s"}`),
		"merge:combine-journal:01BAD-A": []byte("{not json"),
		"merge:combine-journal:01BAD-B": []byte(`"a string, not an object"`),
	}}
	rep := &fakeReporter{}
	err := checkCombineJournalRows(kv, rep)
	var ue *merge.UnreadableCombineJournalsError
	if !errors.As(err, &ue) || len(ue.Rows) != 2 {
		t.Fatalf("want an UnreadableCombineJournalsError with 2 rows, got %v", err)
	}
	logs := strings.Join(rep.logs, "\n")
	for _, key := range []string{"merge:combine-journal:01BAD-A", "merge:combine-journal:01BAD-B"} {
		if !strings.Contains(logs, key) {
			t.Errorf("log does not name %s:\n%s", key, logs)
		}
	}
	if strings.Contains(logs, "01GOOD\"") {
		t.Errorf("a readable row was reported as unreadable:\n%s", logs)
	}
	if !strings.Contains(logs, "3 rows, 2 unreadable") {
		t.Errorf("summary missing:\n%s", logs)
	}
}

func TestCombineJournalCheck_CleanStorePasses(t *testing.T) {
	kv := &combineKVFake{m: map[string][]byte{
		"merge:combine-journal:01GOOD": []byte(`{"id":"01GOOD","survivor_id":"s"}`),
		"unrelated:key":                []byte("{not json"),
	}}
	rep := &fakeReporter{}
	if err := checkCombineJournalRows(kv, rep); err != nil {
		t.Fatalf("a store with only readable journals must pass: %v", err)
	}
	if !strings.Contains(strings.Join(rep.logs, "\n"), "1 rows, 0 unreadable") {
		t.Errorf("summary missing: %v", rep.logs)
	}
}
