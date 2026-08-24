// file: internal/database/scan_state_test.go
// version: 1.0.0
// guid: 173f0b7b-3de0-4b05-ada3-d78eec9b899c
// last-edited: 2026-08-23

package database

import (
	jsonv1 "encoding/json"
	jsonv2 "encoding/json/v2"
	"strings"
	"testing"
)

// marshalers is every JSON implementation that can serialize a book_file row
// during the v1 -> v2 migration. internal/database is still on v1; 17 files
// elsewhere already import encoding/json/v2. A row written by one is read back
// by the other, so ScanState's shape has to be identical under both.
var marshalers = []struct {
	name string
	fn   func(any) ([]byte, error)
}{
	{"v1", func(v any) ([]byte, error) { return jsonv1.Marshal(v) }},
	{"v2", func(v any) ([]byte, error) { return jsonv2.Marshal(v) }},
}

// Every book_file row that existed before the staged scan was serialized with no
// "scan" key. Those rows MUST decode as non-provisional: dedup, version-grouping
// and the bulk write paths gate on IsProvisional, so if a legacy row decoded as
// provisional the entire pre-existing library would drop out of dedup the moment
// this field shipped.
func TestBookFile_LegacyRowIsNotProvisional(t *testing.T) {
	const legacy = `{"id":"f1","book_id":"b1","file_path":"/x/y.m4b","file_hash":"abc"}`

	for _, m := range []struct {
		name string
		fn   func([]byte, any) error
	}{
		{"v1", func(b []byte, v any) error { return jsonv1.Unmarshal(b, v) }},
		{"v2", func(b []byte, v any) error { return jsonv2.Unmarshal(b, v) }},
	} {
		t.Run(m.name, func(t *testing.T) {
			var f BookFile
			if err := m.fn([]byte(legacy), &f); err != nil {
				t.Fatalf("unmarshal legacy row: %v", err)
			}
			if f.IsProvisional() {
				t.Error("a pre-existing row with no \"scan\" key decoded as PROVISIONAL. " +
					"Every such row would silently drop out of dedup, version-grouping " +
					"and bulk apply/merge/organize the moment this field shipped.")
			}
			if f.DeepScanExhausted() {
				t.Error("a legacy row reports its deep-scan retries as exhausted; Attempts must default to 0")
			}
			if f.FileHash != "abc" {
				t.Errorf("adding Scan disturbed an existing field: FileHash=%q want %q", f.FileHash, "abc")
			}
		})
	}
}

// The spec drafted these as `omitempty`. That is wrong in two different ways,
// and only one of them is visible under v1:
//
//   - v1: omitempty does not omit an EMPTY STRUCT, so BookFile.Scan would write
//     "scan":{} into every row.
//   - v2: omitempty means "encodes to an empty JSON value", and false/0 are not
//     empty JSON values, so the inner bools and Attempts would be emitted too.
//
// omitzero means "the Go value is zero" in both. This test is the reason the
// tags cannot drift back.
func TestBookFile_ZeroScanIsOmittedByEveryMarshaler(t *testing.T) {
	for _, m := range marshalers {
		t.Run(m.name, func(t *testing.T) {
			raw, err := m.fn(BookFile{ID: "f1", BookID: "b1", FilePath: "/x/y.m4b"})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			// Match the KEY, not the substring: BookFile already has a
			// "skip_scan" field, and asserting on a bare "scan" passes or fails
			// for the wrong reason.
			if strings.Contains(string(raw), `"scan":`) {
				t.Errorf("a zero ScanState was serialized by %s: %s\n"+
					"The tag must be omitzero, not omitempty.", m.name, raw)
			}
		})
	}
}

// The migration-safety property for the field THIS change owns: the "scan"
// object must be byte-identical under both marshalers, so a row written before
// the store moves to v2 carries the same scan state as one written after.
//
// Deliberately scoped to the scan object rather than the whole row. The rest of
// BookFile does NOT have this property today: 12 numeric fields (track_number,
// track_count, disc_number, disc_count, duration, file_size, bitrate_kbps,
// sample_rate_hz, channels, bit_depth, acoustid_fingerprint_duration_sec,
// acoustid_online_score) are tagged omitempty, which v1 omits at zero and v2
// emits as 0. That is pre-existing, affects every omitempty bool/int in the
// codebase, and is filed separately -- see
// todo.d/20260823-omitempty-changes-meaning-under-jsonv2.md. Asserting it here
// would block this change on somebody else's defect; asserting nothing would
// let this field acquire the same defect unnoticed.
func TestBookFile_ScanObjectSerializesIdenticallyAcrossMarshalers(t *testing.T) {
	for _, f := range []BookFile{
		{ID: "f1", BookID: "b1", FilePath: "/a.m4b"},
		{ID: "f2", BookID: "b1", FilePath: "/b.m4b", Scan: ScanState{NeedsDeep: true}},
		{ID: "f3", BookID: "b1", FilePath: "/c.m4b", Scan: ScanState{
			NeedsDeep: true, HashStale: true, HeaderBad: true, Attempts: 2, LastError: "ffprobe: exit 1",
		}},
		// Load-bearing: the scan object is PRESENT while NeedsDeep and HashStale
		// are FALSE. A true bool serializes the same under omitempty and
		// omitzero, so a table where every populated case sets NeedsDeep:true
		// cannot tell the two tags apart -- verified by mutation, this case is
		// the only one that fails when a bool field is retagged omitempty.
		{ID: "f4", BookID: "b1", FilePath: "/d.m4b", Scan: ScanState{HeaderBad: true}},
		// Same shape for the numeric field.
		{ID: "f5", BookID: "b1", FilePath: "/e.m4b", Scan: ScanState{LastError: "held"}},
	} {
		v1Out, err := jsonv1.Marshal(f)
		if err != nil {
			t.Fatalf("v1 marshal %s: %v", f.ID, err)
		}
		v2Out, err := jsonv2.Marshal(f)
		if err != nil {
			t.Fatalf("v2 marshal %s: %v", f.ID, err)
		}
		v1Scan, v2Scan := extractScanObject(string(v1Out)), extractScanObject(string(v2Out))
		if v1Scan != v2Scan {
			t.Errorf("%s: the scan object differs between marshalers, so this field changes shape when the store migrates.\n v1: %s\n v2: %s",
				f.ID, v1Scan, v2Scan)
		}
	}
}

// extractScanObject returns the "scan":{...} member of a serialized BookFile, or
// "" when the key is absent. ScanState has no nested objects, so scanning to the
// first closing brace is sufficient and avoids decoding the whole row.
func extractScanObject(raw string) string {
	const key = `"scan":`
	i := strings.Index(raw, key)
	if i < 0 {
		return ""
	}
	rest := raw[i:]
	end := strings.Index(rest, "}")
	if end < 0 {
		return rest
	}
	return rest[:end+1]
}

func TestBookFile_ScanRoundTrips(t *testing.T) {
	want := ScanState{
		NeedsDeep: true, HashStale: true, HeaderBad: true,
		Attempts: 2, LastError: "ffprobe: exit 1",
	}
	for _, m := range marshalers {
		t.Run(m.name, func(t *testing.T) {
			raw, err := m.fn(BookFile{ID: "f1", FilePath: "/x.m4b", Scan: want})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if !strings.Contains(string(raw), `"scan":`) {
				t.Fatalf("a non-zero ScanState was omitted by %s: %s", m.name, raw)
			}
			var got BookFile
			if err := jsonv1.Unmarshal(raw, &got); err != nil {
				t.Fatalf("v1 unmarshal of %s output: %v", m.name, err)
			}
			if got.Scan != want {
				t.Errorf("ScanState did not round-trip through %s:\n got %+v\nwant %+v", m.name, got.Scan, want)
			}
		})
	}
}

func TestBookFile_IsProvisional(t *testing.T) {
	cases := []struct {
		name string
		scan ScanState
		want bool
	}{
		{"fresh row", ScanState{}, false},
		{"awaiting deep pass", ScanState{NeedsDeep: true}, true},
		// A stale hash means the file CHANGED and the deep pass has not caught
		// up. The row keeps its old hash deliberately, but that hash no longer
		// describes the bytes, so it must not be matched on.
		{"changed file, deep pass pending", ScanState{NeedsDeep: true, HashStale: true}, true},
		// HeaderBad describes the TITLE, not the content. Once the deep pass has
		// run the hash is real, so the row is a legitimate dedup candidate even
		// though its title came from the path.
		{"unparseable header, deep pass done", ScanState{HeaderBad: true}, false},
		// Retries exhausted but NeedsDeep still set: the content was never read,
		// so it stays out of dedup. Being surfaced to the operator is not the
		// same as being safe to merge.
		{"deep pass failed for good", ScanState{NeedsDeep: true, Attempts: DeepScanMaxAttempts, LastError: "corrupt"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := (BookFile{Scan: tc.scan}).IsProvisional(); got != tc.want {
				t.Errorf("IsProvisional()=%v want %v for %+v", got, tc.want, tc.scan)
			}
		})
	}
}

// The retry ceiling is a product decision (round 5: "3 attempts, with backoff"),
// so it is pinned to its literal value. Expressing this test only in terms of
// DeepScanMaxAttempts made it tautological -- verified by mutation: raising the
// constant to 4 left the whole suite green.
func TestDeepScanMaxAttempts_IsThree(t *testing.T) {
	if DeepScanMaxAttempts != 3 {
		t.Errorf("DeepScanMaxAttempts=%d, want 3. Changing the retry ceiling is a "+
			"product decision, not a refactor: at 3 a row stops retrying and is "+
			"surfaced. If this moved on purpose, change the literal here too.",
			DeepScanMaxAttempts)
	}
}

func TestBookFile_DeepScanExhausted(t *testing.T) {
	for _, tc := range []struct {
		attempts int
		want     bool
	}{{0, false}, {1, false}, {DeepScanMaxAttempts - 1, false}, {DeepScanMaxAttempts, true}, {DeepScanMaxAttempts + 1, true}} {
		if got := (BookFile{Scan: ScanState{Attempts: tc.attempts}}).DeepScanExhausted(); got != tc.want {
			t.Errorf("Attempts=%d: DeepScanExhausted()=%v want %v", tc.attempts, got, tc.want)
		}
	}
}

// BookFile and BookFileCore answer the same two questions, and a caller holding
// either must get the same answer for the same row. Core() is the only bridge
// between them, so this also pins that Core() carries Scan across -- a projection
// that dropped it would make IsProvisional answer false for a provisional row,
// failing OPEN in exactly the direction that merges un-hashed books.
func TestScanPredicatesAgreeAcrossBookFileAndCore(t *testing.T) {
	states := []ScanState{
		{},
		{NeedsDeep: true},
		{HeaderBad: true},
		{NeedsDeep: true, HashStale: true},
		{Attempts: DeepScanMaxAttempts - 1},
		{Attempts: DeepScanMaxAttempts},
		{NeedsDeep: true, Attempts: DeepScanMaxAttempts, LastError: "corrupt"},
	}
	for _, st := range states {
		bf := BookFile{ID: "f1", BookID: "b1", FilePath: "/x.m4b", Scan: st}
		core := bf.Core()

		if core.Scan != bf.Scan {
			t.Errorf("Core() did not carry Scan across for %+v: got %+v", st, core.Scan)
			continue
		}
		if got, want := core.IsProvisional(), bf.IsProvisional(); got != want {
			t.Errorf("IsProvisional disagrees for %+v: BookFileCore=%v BookFile=%v", st, got, want)
		}
		if got, want := core.DeepScanExhausted(), bf.DeepScanExhausted(); got != want {
			t.Errorf("DeepScanExhausted disagrees for %+v: BookFileCore=%v BookFile=%v", st, got, want)
		}
	}
}
