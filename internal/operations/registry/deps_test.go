// file: internal/operations/registry/deps_test.go
// version: 2.1.1
// guid: e1f2a3b4-c5d6-7e8f-9a0b-1c2d3e4f5a6b
// last-edited: 2026-09-02

package registry

import (
	"fmt"
	"maps"
	"testing"

	"github.com/falkcorp/audiobook-organizer/internal/database"
)

// fakeDepStore is a minimal in-memory DepStore for unit testing the evaluator.
type fakeDepStore struct {
	depRev         map[string]uint64            // key(sub) → rev
	completion     map[string]uint64            // ckey(sub, opType) → rev
	fileCompletion map[string]map[string]uint64 // ckey(sub, opType) → fileID→rev
	files          map[string][]string          // bookID → file IDs
	books          map[string]*database.Book    // bookID → book (nil entry = not found)
}

func newFakeDepStore() *fakeDepStore {
	return &fakeDepStore{
		depRev:         make(map[string]uint64),
		completion:     make(map[string]uint64),
		fileCompletion: make(map[string]map[string]uint64),
		files:          make(map[string][]string),
		books:          make(map[string]*database.Book),
	}
}

func depStoreSubKey(sub Subject) string {
	return sub.Type + ":" + sub.ID
}

func depStoreCKey(sub Subject, opType string) string {
	return sub.Type + ":" + sub.ID + ":" + opType
}

func (f *fakeDepStore) GetDepRev(sub database.OpSubject) (uint64, error) {
	return f.depRev[sub.Type+":"+sub.ID], nil
}

func (f *fakeDepStore) GetOpCompletion(sub database.OpSubject, opType string) (uint64, bool, error) {
	key := sub.Type + ":" + sub.ID + ":" + opType
	rev, ok := f.completion[key]
	return rev, ok, nil
}

func (f *fakeDepStore) ListFileCompletions(sub database.OpSubject, opType string) (map[string]uint64, error) {
	key := sub.Type + ":" + sub.ID + ":" + opType
	m, ok := f.fileCompletion[key]
	if !ok {
		return nil, nil
	}
	// Return a copy so callers can't mutate the test state.
	out := make(map[string]uint64, len(m))
	maps.Copy(out, m)
	return out, nil
}

func (f *fakeDepStore) BookFiles(bookID string) ([]string, error) {
	return f.files[bookID], nil
}

// GetBookByID satisfies DepStore. Returns (nil, nil) when the book is not in
// the map, mirroring PebbleStore.GetBookByID's not-found contract.
func (f *fakeDepStore) GetBookByID(id string) (*database.Book, error) {
	book, ok := f.books[id]
	if !ok {
		return nil, nil // not found → (nil, nil), same as PebbleStore
	}
	return book, nil
}

// TestEvaluate_OpCompleted_FreshVsStale verifies the core freshness check:
// a completion at a stale rev does NOT satisfy; at the current rev it does.
func TestEvaluate_OpCompleted_FreshVsStale(t *testing.T) {
	st := newFakeDepStore()
	sub := Subject{Type: "book", ID: "b1"}
	st.depRev[depStoreSubKey(sub)] = 2
	req := Requirement{Kind: ReqOpCompleted, OpType: "fp"}

	// no completion record → unmet
	ok, reason, err := Satisfied(st, req, sub)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("should be unmet without any completion record")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason when unmet")
	}

	// completion at rev 1 but current dep_rev is 2 → stale → unmet
	st.completion[depStoreCKey(sub, "fp")] = 1
	ok, _, err = Satisfied(st, req, sub)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("stale completion (rev 1 < current 2) must not satisfy")
	}

	// completion at rev 2 → fresh → satisfied
	st.completion[depStoreCKey(sub, "fp")] = 2
	ok, _, err = Satisfied(st, req, sub)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("fresh completion (rev 2 == current 2) must satisfy")
	}
}

// TestEvaluate_AllFiles_Coverage verifies that AllFiles=true requires every file
// of the book to have a fresh completion record.
func TestEvaluate_AllFiles_Coverage(t *testing.T) {
	st := newFakeDepStore()
	sub := Subject{Type: "book", ID: "b1"}
	st.depRev[depStoreSubKey(sub)] = 1
	st.files["b1"] = []string{"f1", "f2"}
	req := Requirement{Kind: ReqOpCompleted, OpType: "fp", AllFiles: true}

	// Only f1 complete → partial → unmet.
	st.fileCompletion[depStoreCKey(sub, "fp")] = map[string]uint64{"f1": 1}
	ok, _, err := Satisfied(st, req, sub)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("partial file coverage (f1 only) must not satisfy")
	}

	// Both files at current rev → satisfied.
	st.fileCompletion[depStoreCKey(sub, "fp")]["f2"] = 1
	ok, _, err = Satisfied(st, req, sub)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("full file coverage must satisfy")
	}

	// One file at stale rev → not satisfied.
	st.fileCompletion[depStoreCKey(sub, "fp")]["f1"] = 0
	ok, _, err = Satisfied(st, req, sub)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("file with stale rev must not satisfy AllFiles requirement")
	}
}

// TestAllSatisfied_CombinesReqs verifies AllSatisfied returns false on first
// unmet requirement and true when all are met.
func TestAllSatisfied_CombinesReqs(t *testing.T) {
	st := newFakeDepStore()
	sub := Subject{Type: "book", ID: "b2"}
	st.depRev[depStoreSubKey(sub)] = 1

	reqs := []Requirement{
		{Kind: ReqOpCompleted, OpType: "fp"},
		{Kind: ReqOpCompleted, OpType: "scan"},
	}

	// Neither satisfied yet.
	ok, unmet, err := AllSatisfied(st, reqs, sub)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("expected not satisfied")
	}
	if unmet == "" {
		t.Fatal("expected non-empty firstUnmet")
	}

	// Satisfy only fp.
	st.completion[depStoreCKey(sub, "fp")] = 1
	ok, unmet, err = AllSatisfied(st, reqs, sub)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("scan still unmet, expected not satisfied")
	}
	if unmet == "" {
		t.Fatal("expected non-empty firstUnmet when scan is unmet")
	}

	// Satisfy both.
	st.completion[depStoreCKey(sub, "scan")] = 1
	ok, _, err = AllSatisfied(st, reqs, sub)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("both requirements met, expected satisfied")
	}
}

// TestReqFieldSet_FieldSet_Satisfied verifies that a ReqFieldSet requirement
// is satisfied when the named field is non-empty on the subject book.
func TestReqFieldSet_FieldSet_Satisfied(t *testing.T) {
	st := newFakeDepStore()
	sub := Subject{Type: "book", ID: "b3"}
	sig := "dGVzdA==" // non-empty base64
	st.books["b3"] = &database.Book{ID: "b3", BookSigV1: &sig}
	req := Requirement{Kind: ReqFieldSet, Field: "book_sig_v1"}

	ok, reason, err := Satisfied(st, req, sub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Fatalf("expected satisfied when book_sig_v1 is set, got reason: %q", reason)
	}
}

// TestReqFieldSet_FieldUnset_Nil verifies that a nil *string field is unmet.
func TestReqFieldSet_FieldUnset_Nil(t *testing.T) {
	st := newFakeDepStore()
	sub := Subject{Type: "book", ID: "b4"}
	st.books["b4"] = &database.Book{ID: "b4"} // BookSigV1 is nil
	req := Requirement{Kind: ReqFieldSet, Field: "book_sig_v1"}

	ok, reason, err := Satisfied(st, req, sub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected unmet when book_sig_v1 is nil")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason when field is nil")
	}
}

// TestReqFieldSet_FieldUnset_Empty verifies that an empty-string *string field is unmet.
func TestReqFieldSet_FieldUnset_Empty(t *testing.T) {
	st := newFakeDepStore()
	sub := Subject{Type: "book", ID: "b5"}
	empty := ""
	st.books["b5"] = &database.Book{ID: "b5", BookSigV1: &empty}
	req := Requirement{Kind: ReqFieldSet, Field: "book_sig_v1"}

	ok, reason, err := Satisfied(st, req, sub)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Fatal("expected unmet when book_sig_v1 is empty string")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason when field is empty")
	}
}

// TestReqFieldSet_UnknownField_Error verifies that an unknown field name
// returns an error rather than silently evaluating to false.
func TestReqFieldSet_UnknownField_Error(t *testing.T) {
	st := newFakeDepStore()
	sub := Subject{Type: "book", ID: "b6"}
	st.books["b6"] = &database.Book{ID: "b6"}
	req := Requirement{Kind: ReqFieldSet, Field: "narrator"} // not in allow-list

	ok, _, err := Satisfied(st, req, sub)
	if err == nil {
		t.Fatal("expected error for unknown field name, got nil")
	}
	if ok {
		t.Fatal("expected not satisfied when field name is unknown")
	}
}

// TestReqFieldSet_MissingBook_Unmet verifies that a missing book (GetBookByID
// returns nil, nil) is treated as unmet, not as an error.
func TestReqFieldSet_MissingBook_Unmet(t *testing.T) {
	st := newFakeDepStore()
	sub := Subject{Type: "book", ID: "no-such-book"}
	// Do NOT add "no-such-book" to st.books — GetBookByID returns (nil, nil).
	req := Requirement{Kind: ReqFieldSet, Field: "book_sig_v1"}

	ok, reason, err := Satisfied(st, req, sub)
	if err != nil {
		t.Fatalf("missing book should be unmet (not error), got: %v", err)
	}
	if ok {
		t.Fatal("expected unmet when book does not exist")
	}
	if reason == "" {
		t.Fatal("expected non-empty reason when book is missing")
	}
}

// TestReqFieldSet_AllAllowedFields verifies that every field in the allow-list
// can be both satisfied (non-empty) and unsatisfied (nil). This catches the
// case where a field is listed but its accessor predicate is broken.
func TestReqFieldSet_AllAllowedFields(t *testing.T) {
	cases := []struct {
		field    string
		populate func(b *database.Book)
	}{
		{
			field: "book_sig_v1",
			populate: func(b *database.Book) {
				v := "sig"
				b.BookSigV1 = &v
			},
		},
		{
			field: "metadata_source_hash",
			populate: func(b *database.Book) {
				v := "abc123"
				b.MetadataSourceHash = &v
			},
		},
		{
			field: "asin",
			populate: func(b *database.Book) {
				v := "B001234567"
				b.ASIN = &v
			},
		},
		{
			field: "isbn13",
			populate: func(b *database.Book) {
				v := "9781234567890"
				b.ISBN13 = &v
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.field+"/set", func(t *testing.T) {
			st := newFakeDepStore()
			b := &database.Book{ID: "bx"}
			tc.populate(b)
			st.books["bx"] = b
			sub := Subject{Type: "book", ID: "bx"}
			req := Requirement{Kind: ReqFieldSet, Field: tc.field}
			ok, reason, err := Satisfied(st, req, sub)
			if err != nil {
				t.Fatalf("field %q set: unexpected error: %v", tc.field, err)
			}
			if !ok {
				t.Fatalf("field %q set: expected satisfied, got reason %q", tc.field, reason)
			}
		})
		t.Run(tc.field+"/unset", func(t *testing.T) {
			st := newFakeDepStore()
			st.books["bx"] = &database.Book{ID: "bx"} // all fields nil
			sub := Subject{Type: "book", ID: "bx"}
			req := Requirement{Kind: ReqFieldSet, Field: tc.field}
			ok, _, err := Satisfied(st, req, sub)
			if err != nil {
				t.Fatalf("field %q unset: unexpected error: %v", tc.field, err)
			}
			if ok {
				t.Fatalf("field %q unset: expected not satisfied when field is nil", tc.field)
			}
		})
	}
}

// TestCycleDetection verifies that CheckRequirementCycle catches a direct A→B→A cycle.
func TestCycleDetection(t *testing.T) {
	defs := map[string][]Requirement{
		"a": {{Kind: ReqOpCompleted, OpType: "b"}},
		"b": {{Kind: ReqOpCompleted, OpType: "a"}},
	}
	if err := CheckRequirementCycle(defs); err == nil {
		t.Fatal("expected cycle error for A→B→A")
	}
}

// TestCycleDetection_NoCycle verifies a valid linear chain does not trigger cycle error.
func TestCycleDetection_NoCycle(t *testing.T) {
	defs := map[string][]Requirement{
		"a": {},
		"b": {{Kind: ReqOpCompleted, OpType: "a"}},
		"c": {{Kind: ReqOpCompleted, OpType: "b"}},
	}
	if err := CheckRequirementCycle(defs); err != nil {
		t.Fatalf("expected no cycle, got: %v", err)
	}
}

// TestCycleDetection_SelfLoop verifies that a self-referential op is caught.
func TestCycleDetection_SelfLoop(t *testing.T) {
	defs := map[string][]Requirement{
		"a": {{Kind: ReqOpCompleted, OpType: "a"}},
	}
	if err := CheckRequirementCycle(defs); err == nil {
		t.Fatal("expected cycle error for self-loop A→A")
	}
}

// TestCycleDetection_LongChain verifies a longer cycle is detected.
func TestCycleDetection_LongChain(t *testing.T) {
	// a→b→c→d→a is a cycle of length 4.
	defs := map[string][]Requirement{
		"a": {{Kind: ReqOpCompleted, OpType: "b"}},
		"b": {{Kind: ReqOpCompleted, OpType: "c"}},
		"c": {{Kind: ReqOpCompleted, OpType: "d"}},
		"d": {{Kind: ReqOpCompleted, OpType: "a"}},
	}
	if err := CheckRequirementCycle(defs); err == nil {
		t.Fatal("expected cycle error for a→b→c→d→a")
	}
}

// TestCycleDetection_IgnoresFieldSet verifies that ReqFieldSet requirements
// are not traversed in cycle detection (they have no OpType graph edge).
func TestCycleDetection_IgnoresFieldSet(t *testing.T) {
	defs := map[string][]Requirement{
		"a": {{Kind: ReqFieldSet, Field: "narrator"}},
		"b": {{Kind: ReqOpCompleted, OpType: "a"}},
	}
	if err := CheckRequirementCycle(defs); err != nil {
		t.Fatalf("field_set req should not create a graph edge, got: %v", err)
	}
}

// TestCycleDetection_CycleErrorNamesNode verifies the error message includes
// one of the cycle nodes for debuggability.
func TestCycleDetection_CycleErrorNamesNode(t *testing.T) {
	defs := map[string][]Requirement{
		"x": {{Kind: ReqOpCompleted, OpType: "y"}},
		"y": {{Kind: ReqOpCompleted, OpType: "x"}},
	}
	err := CheckRequirementCycle(defs)
	if err == nil {
		t.Fatal("expected cycle error")
	}
	errStr := err.Error()
	if errStr == "" {
		t.Fatal("cycle error must have a non-empty message")
	}
	// The message must mention "cycle" or one of the involved node names.
	found := false
	for _, word := range []string{"cycle", "x", "y"} {
		if len(errStr) > 0 && contains(errStr, word) {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("cycle error message %q should mention the cycle or node names", errStr)
	}
}

// contains is a simple substring check (avoid importing strings just for tests).
func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && containsHelper(s, sub))
}

func containsHelper(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Compile-time guard: fakeDepStore must implement DepStore.
var _ DepStore = (*fakeDepStore)(nil)

// Compile-time guard: Satisfied, AllSatisfied, CheckRequirementCycle must exist.
var _ = fmt.Sprintf // suppress unused import if guards are removed

// TestSubjectsFromParams verifies I2: subjectsFromParams handles both the v1
// single-subject shape {"book_id":"..."} and the batched
// {"subjects":[{"type":"book","id":"..."},...]} shape that batchFire writes
// into params.  worker.go iterates subjectsFromParams to fire one
// notifyDepCompletion per subject after a batch op completes.
func TestSubjectsFromParams(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		input string
		want  []Subject
	}{
		{
			name:  "empty",
			input: `{}`,
			want:  nil,
		},
		{
			name:  "nil params",
			input: ``,
			want:  nil,
		},
		{
			name:  "v1 single book_id",
			input: `{"book_id":"book-42"}`,
			want:  []Subject{{Type: "book", ID: "book-42"}},
		},
		{
			name:  "batched subjects two entries",
			input: `{"subjects":[{"type":"book","id":"book-a"},{"type":"book","id":"book-b"}]}`,
			want: []Subject{
				{Type: "book", ID: "book-a"},
				{Type: "book", ID: "book-b"},
			},
		},
		{
			name:  "batched subjects skips empty IDs",
			input: `{"subjects":[{"type":"book","id":""},{"type":"book","id":"book-c"}]}`,
			want:  []Subject{{Type: "book", ID: "book-c"}},
		},
		{
			name:  "batched subjects empty list",
			input: `{"subjects":[]}`,
			want:  []Subject{},
		},
		{
			name:  "uppercase json keys (legacy serialisation)",
			input: `{"Type":"book","ID":"book-99"}`,
			// Neither key matches — returns nil.
			want: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := subjectsFromParams([]byte(tc.input))
			if len(got) != len(tc.want) {
				t.Fatalf("len mismatch: got %d %+v, want %d %+v", len(got), got, len(tc.want), tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Errorf("entry[%d]: got %+v, want %+v", i, got[i], tc.want[i])
				}
			}
		})
	}
}
