// file: internal/operations/registry/legacy_op_dedupe_test.go
// version: 1.0.0
// guid: c07f4b91-2a63-4d58-8e1c-b5920f7a3d46
// last-edited: 2026-08-22

package registry

import "testing"

// TestSameParamsIgnoringLegacyID covers the comparison EnqueueOp's dedupe uses.
//
// The cases are grouped by what they protect. The "must NOT merge" group is the
// load-bearing half: a comparison that ignored too much would pass every
// "must merge" case and silently collapse genuinely different requests into one
// run — which for a maintenance job means an operator's real apply being
// discarded because a dry run was already active.
func TestSameParamsIgnoringLegacyID(t *testing.T) {
	tests := []struct {
		name string
		a, b string
		want bool
	}{
		// --- must merge ---
		{
			name: "byte-identical",
			a:    `{"legacy_op_id":"A","job_id":"fix-modes","dry_run":true}`,
			b:    `{"legacy_op_id":"A","job_id":"fix-modes","dry_run":true}`,
			want: true,
		},
		{
			name: "differ ONLY in legacy_op_id — the whole point",
			a:    `{"legacy_op_id":"A","job_id":"fix-modes","dry_run":true}`,
			b:    `{"legacy_op_id":"B","job_id":"fix-modes","dry_run":true}`,
			want: true,
		},
		{
			name: "key order differs and legacy_op_id is present",
			a:    `{"legacy_op_id":"A","job_id":"fix-modes"}`,
			b:    `{"job_id":"fix-modes","legacy_op_id":"B"}`,
			want: true,
		},
		{
			name: "no other keys at all",
			a:    `{"legacy_op_id":"A"}`,
			b:    `{"legacy_op_id":"B"}`,
			want: true,
		},

		// --- must NOT merge: real parameters differ ---
		{
			name: "dry_run differs — a preview must never absorb a real apply",
			a:    `{"legacy_op_id":"A","job_id":"cleanup-series","dry_run":true}`,
			b:    `{"legacy_op_id":"B","job_id":"cleanup-series","dry_run":false}`,
			want: false,
		},
		{
			name: "job_id differs",
			a:    `{"legacy_op_id":"A","job_id":"fix-modes"}`,
			b:    `{"legacy_op_id":"B","job_id":"cleanup-series"}`,
			want: false,
		},
		{
			name: "a real key is present-but-zero on one side and absent on the other",
			a:    `{"legacy_op_id":"A","job_id":"x","dry_run":false}`,
			b:    `{"legacy_op_id":"B","job_id":"x"}`,
			want: false,
		},
		{
			name: "nested value differs",
			a:    `{"legacy_op_id":"A","matches":[{"id":1}]}`,
			b:    `{"legacy_op_id":"B","matches":[{"id":2}]}`,
			want: false,
		},

		// --- must NOT merge: no bridge key, so the exact byte rule stands ---
		{
			name: "neither carries legacy_op_id and the bytes differ",
			a:    `{"book_ids":["1"],"rename":true}`,
			b:    `{"rename":true,"book_ids":["1"]}`, // same work, but key order differs
			want: false,                              // unchanged: exact comparison still applies
		},
		{
			name: "only one side carries legacy_op_id",
			a:    `{"legacy_op_id":"A","job_id":"x"}`,
			b:    `{"job_id":"x"}`,
			want: false,
		},

		// --- non-objects ---
		{
			name: "params are not JSON objects and differ",
			a:    `[1,2]`,
			b:    `[3,4]`,
			want: false,
		},
		{
			name: "empty objects are equal by the fast path",
			a:    `{}`,
			b:    `{}`,
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := sameParamsIgnoringLegacyID([]byte(tc.a), []byte(tc.b)); got != tc.want {
				t.Fatalf("sameParamsIgnoringLegacyID(%s, %s) = %v, want %v", tc.a, tc.b, got, tc.want)
			}
			// The relation must be symmetric: EnqueueOp compares the incoming
			// request against a stored row, and which side is which is an
			// accident of call order, not something the caller controls.
			if got := sameParamsIgnoringLegacyID([]byte(tc.b), []byte(tc.a)); got != tc.want {
				t.Fatalf("not symmetric: (%s, %s) = %v, want %v", tc.b, tc.a, got, tc.want)
			}
		})
	}
}

// TestParamsWithoutLegacyID_ReportsPresence pins the flag the comparison branches
// on. If this ever reported true for params that do not carry the key, every
// non-bridge op in the registry would silently switch from exact byte-equality
// to the key-wise comparison — a change to ~140 defs that no other test asserts.
func TestParamsWithoutLegacyID_ReportsPresence(t *testing.T) {
	if _, had := paramsWithoutLegacyID([]byte(`{"job_id":"x"}`)); had {
		t.Fatal("reported legacy_op_id present in params that do not carry it")
	}
	m, had := paramsWithoutLegacyID([]byte(`{"legacy_op_id":"A","job_id":"x"}`))
	if !had {
		t.Fatal("reported legacy_op_id absent in params that carry it")
	}
	if _, still := m[legacyOpIDKey]; still {
		t.Fatal("legacy_op_id survived the strip")
	}
	if len(m) != 1 {
		t.Fatalf("strip removed more than the one key: %v", m)
	}
}
